package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testApp(t *testing.T) http.Handler {
	dir := t.TempDir()
	hub := NewHub(10, "")
	cfg := NewSettingsStore(dir + "/settings.json")
	st := NewStore(dir + "/s.json")
	a := &App{cfg: cfg, store: st, hub: hub, runner: NewRunner(cfg, st, hub, dir+"/runs.json"), auth: NewAuth("admin", "secret", dir, hub)}
	return a.routes()
}

func TestAuthFlow(t *testing.T) {
	h := testApp(t)
	do := func(method, path, body, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do("GET", "/feed.xml", "", ""); w.Code != 200 {
		t.Errorf("feed must be public, got %d", w.Code)
	}
	if w := do("GET", "/health", "", ""); w.Code != 200 {
		t.Errorf("health must be public, got %d", w.Code)
	}
	if w := do("GET", "/api/items", "", ""); w.Code != 401 {
		t.Errorf("api must be 401, got %d", w.Code)
	}
	if w := do("GET", "/", "", ""); w.Code != 302 || w.Header().Get("Location") != "/login" {
		t.Errorf("page must redirect to /login, got %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := do("POST", "/login", `{"username":"admin","password":"nope"}`, ""); w.Code != 401 {
		t.Errorf("bad login got %d", w.Code)
	}
	w := do("POST", "/login", `{"username":"admin","password":"secret","remember":true,"next":"//evil.com"}`, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"next":"/"`) {
		t.Fatalf("login got %d %s", w.Code, w.Body.String())
	}
	ck := w.Result().Cookies()[0]
	cookie := ck.Name + "=" + ck.Value
	if !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie flags: %+v", ck)
	}
	if w := do("GET", "/api/items", "", cookie); w.Code != 200 {
		t.Errorf("authed api got %d", w.Code)
	}
	if w := do("GET", "/api/items", "", cookie+"x"); w.Code != 401 {
		t.Errorf("tampered cookie got %d", w.Code)
	}
}
