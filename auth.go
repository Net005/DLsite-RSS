package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const cookieName = "dlrss_session"

// Auth implements a form login with signed, stateless session cookies.
// Cookie value: "<expiryUnix>.<hex hmac>". The HMAC key is derived from a random
// secret persisted in DATA_DIR and the admin password, so changing the password
// (or deleting session.key) logs everyone out.
type Auth struct {
	user, pass string
	key        []byte
	hub        *Hub

	mu    sync.Mutex
	fails map[string]*failState
}

type failState struct {
	n     int
	until time.Time
}

func NewAuth(user, pass, dataDir string, hub *Hub) *Auth {
	a := &Auth{user: user, pass: pass, hub: hub, fails: map[string]*failState{}}
	if pass == "" {
		return a
	}
	keyPath := filepath.Join(dataDir, "session.key")
	secret, err := os.ReadFile(keyPath)
	if err != nil || len(secret) < 32 {
		secret = make([]byte, 32)
		rand.Read(secret)
		os.WriteFile(keyPath, secret, 0o600)
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(pass))
	a.key = m.Sum(nil)
	return a
}

func (a *Auth) Enabled() bool { return a.pass != "" }

func (a *Auth) sign(exp int64) string {
	e := strconv.FormatInt(exp, 10)
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(e))
	return e + "." + hex.EncodeToString(m.Sum(nil))
}

func (a *Auth) valid(r *http.Request) bool {
	if !a.Enabled() {
		return true
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(a.sign(exp))) == 1
}

func secureReq(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (a *Auth) setCookie(w http.ResponseWriter, r *http.Request, remember bool) {
	ttl := 12 * time.Hour
	c := &http.Cookie{Name: cookieName, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureReq(r)}
	if remember {
		ttl = 30 * 24 * time.Hour
		c.MaxAge = int(ttl.Seconds())
	} // otherwise a browser-session cookie that also expires server-side after 12 h
	c.Value = a.sign(time.Now().Add(ttl).Unix())
	http.SetCookie(w, c)
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// throttle allows 5 failures, then locks the IP out for 5 minutes.
func (a *Auth) locked(ip string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if f := a.fails[ip]; f != nil && time.Now().Before(f.until) {
		return time.Until(f.until)
	}
	return 0
}

func (a *Auth) fail(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.fails[ip]
	if f == nil {
		f = &failState{}
		a.fails[ip] = f
	}
	f.n++
	if f.n >= 5 {
		f.until = time.Now().Add(5 * time.Minute)
		f.n = 0
	}
}

func (a *Auth) succeed(ip string) {
	a.mu.Lock()
	delete(a.fails, ip)
	a.mu.Unlock()
}

// safeNext only allows local paths to avoid open redirects.
func safeNext(n string) string {
	if n == "" || !strings.HasPrefix(n, "/") || strings.HasPrefix(n, "//") || strings.HasPrefix(n, "/\\") || strings.HasPrefix(n, "/login") {
		return "/"
	}
	return n
}

func (a *Auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !a.Enabled() {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if r.Method == http.MethodGet {
		if a.valid(r) {
			http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusFound)
			return
		}
		b, _ := webAssets.ReadFile("web/login.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
		return
	}
	// POST (JSON from the login page, or a classic form post)
	var user, pass, next string
	var remember bool
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Remember bool   `json:"remember"`
			Next     string `json:"next"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body)
		user, pass, remember, next = body.Username, body.Password, body.Remember, body.Next
	} else {
		r.ParseForm()
		user, pass, remember, next = r.PostFormValue("username"), r.PostFormValue("password"), r.PostFormValue("remember") != "", r.PostFormValue("next")
	}
	ip := clientIP(r)
	if d := a.locked(ip); d > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(d.Seconds())))
		apiErr(w, http.StatusTooManyRequests, "Too many failed attempts. Try again in "+d.Round(time.Second).String()+".")
		return
	}
	okU := subtle.ConstantTimeCompare([]byte(user), []byte(a.user)) == 1
	okP := subtle.ConstantTimeCompare([]byte(pass), []byte(a.pass)) == 1
	if !okU || !okP {
		a.fail(ip)
		a.hub.Warn("Failed login attempt from %s.", ip)
		time.Sleep(600 * time.Millisecond) // slow down guessing
		apiErr(w, http.StatusUnauthorized, "Invalid username or password.")
		return
	}
	a.succeed(ip)
	a.setCookie(w, r, remember)
	a.hub.Info("Login from %s.", ip)
	writeJSON(w, 200, map[string]string{"next": safeNext(next)})
}

func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureReq(r)})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// Middleware: public paths pass; API calls get 401 JSON; pages redirect to /login.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !a.Enabled() || p == "/feed.xml" || p == "/health" || p == "/login" || p == "/app.css" || a.valid(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(p, "/api/") {
			apiErr(w, http.StatusUnauthorized, "not logged in")
			return
		}
		dest := "/login"
		if p != "/" {
			dest += "?next=" + url.QueryEscape(r.URL.RequestURI())
		}
		http.Redirect(w, r, dest, http.StatusFound)
	})
}
