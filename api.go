package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

type App struct {
	cfg    *SettingsStore
	store  *Store
	hub    *Hub
	runner *Runner
	user   string
	pass   string
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// auth protects everything except the public feed and health check.
func (a *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.pass == "" || r.URL.Path == "/feed.xml" || r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(a.user)) != 1 || subtle.ConstantTimeCompare([]byte(p), []byte(a.pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="DLsite RSS"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ItemView is an Item plus computed display fields.
type ItemView struct {
	ProductID string  `json:"product_id"`
	Title     string  `json:"title"`
	Circle    string  `json:"circle"`
	Rank      int     `json:"rank"`
	PrevRank  int     `json:"prev_rank"`
	FirstSeen float64 `json:"first_seen"`
	LastSeen  float64 `json:"last_seen"`
	Cover     string  `json:"cover"`
	URL       string  `json:"url"`
	Category  string  `json:"category"`
	Price     string  `json:"price"`
	Hidden    bool    `json:"hidden"`
	IsNew     bool    `json:"is_new"`
	Active    bool    `json:"active"` // present in the most recent successful scrape
}

func (a *App) handleItems(w http.ResponseWriter, r *http.Request) {
	s := a.cfg.Get()
	now := float64(time.Now().Unix())
	cutoff := now - float64(s.NewItemsDays)*86400
	var lastScrape float64
	if st := a.runner.Status(); st.LastOK != nil {
		if t, err := time.Parse(time.RFC3339, st.LastOK.Started); err == nil {
			lastScrape = float64(t.Unix()) - 60
		}
	}
	items := a.store.Snapshot()
	out := make([]ItemView, 0, len(items))
	for _, it := range items {
		out = append(out, ItemView{
			ProductID: it.ProductID, Title: it.Title, Circle: it.Circle, Rank: it.Rank, PrevRank: it.PrevRank,
			FirstSeen: it.FirstSeen, LastSeen: it.LastSeen, Cover: normURL(it.CoverURL),
			URL: workBaseURL + it.ProductID + ".html", Category: it.Category, Price: it.Price, Hidden: it.Hidden,
			IsNew: it.FirstSeen >= cutoff, Active: it.LastSeen == 0 || it.LastSeen >= lastScrape,
		})
	}
	writeJSON(w, 200, out)
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /feed.xml", a.handleFeed)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("OK")) })

	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, a.runner.Status()) })
	mux.HandleFunc("GET /api/items", a.handleItems)
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, a.runner.Runs()) })
	mux.HandleFunc("DELETE /api/runs", func(w http.ResponseWriter, r *http.Request) {
		a.runner.ClearRuns()
		a.hub.Emit("status", a.runner.Status())
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /api/scrape", func(w http.ResponseWriter, r *http.Request) {
		if err := a.runner.Trigger("manual"); err != nil {
			apiErr(w, 409, err.Error())
			return
		}
		a.runner.emitStatus()
		writeJSON(w, 202, map[string]bool{"started": true})
	})
	mux.HandleFunc("POST /api/scrape/cancel", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"cancelled": a.runner.Cancel()})
	})

	mux.HandleFunc("DELETE /api/items/{pid}", func(w http.ResponseWriter, r *http.Request) {
		pid := r.PathValue("pid")
		if !a.store.Delete(pid) {
			apiErr(w, 404, "not found")
			return
		}
		a.hub.Info("Deleted %s from state.", pid)
		a.hub.Emit("items", map[string]int{"total": a.store.Count()})
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/items/{pid}/hide", func(w http.ResponseWriter, r *http.Request) {
		hide := r.URL.Query().Get("hidden") != "false"
		if !a.store.SetHidden(r.PathValue("pid"), hide) {
			apiErr(w, 404, "not found")
			return
		}
		a.hub.Info("%s %s from the feed.", r.PathValue("pid"), map[bool]string{true: "hidden", false: "restored"}[hide])
		a.hub.Emit("items", map[string]int{"total": a.store.Count()})
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/items/clear", func(w http.ResponseWriter, r *http.Request) {
		a.store.Clear()
		a.hub.Warn("State cleared — all tracked games removed.")
		a.hub.Emit("items", map[string]int{"total": 0})
		a.hub.Emit("status", a.runner.Status())
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, a.cfg.Get()) })
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		s := a.cfg.Get()
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&s); err != nil {
			apiErr(w, 400, "invalid JSON: "+err.Error())
			return
		}
		if err := a.cfg.Set(s); err != nil {
			apiErr(w, 400, err.Error())
			return
		}
		a.hub.Info("Settings saved (interval %.2gh, window %dd, target %d, mode %s).", s.IntervalHours, s.NewItemsDays, s.TargetGames, s.Mode)
		a.runner.Wake()
		a.runner.emitStatus()
		writeJSON(w, 200, a.cfg.Get())
	})
	mux.HandleFunc("POST /api/webhook/test", func(w http.ResponseWriter, r *http.Request) {
		if err := a.runner.TestWebhook(); err != nil {
			apiErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	mux.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 {
			n = 500
		}
		writeJSON(w, 200, a.hub.Recent(n))
	})
	mux.HandleFunc("DELETE /api/logs", func(w http.ResponseWriter, r *http.Request) {
		a.hub.Clear()
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/events", a.handleEvents)

	mux.HandleFunc("GET /api/export", func(w http.ResponseWriter, r *http.Request) {
		b, err := a.store.Export()
		if err != nil {
			apiErr(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="dlsite_seen_titles.json"`)
		w.Write(b)
	})
	mux.HandleFunc("POST /api/import", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			apiErr(w, 400, err.Error())
			return
		}
		n, err := a.store.Replace(b)
		if err != nil {
			apiErr(w, 400, "import failed: "+err.Error())
			return
		}
		a.hub.Info("Imported state: %d entries.", n)
		a.hub.Emit("items", map[string]int{"total": n})
		a.hub.Emit("status", a.runner.Status())
		writeJSON(w, 200, map[string]int{"imported": n})
	})

	mux.Handle("GET /", http.FileServerFS(webFS()))
	return a.auth(mux)
}

// handleEvents streams log lines, status and item-change notifications (SSE).
func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		apiErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := a.hub.Subscribe()
	defer a.hub.Unsubscribe(ch)

	send := func(typ string, data []byte) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data)
		fl.Flush()
	}
	st, _ := json.Marshal(a.runner.Status())
	send("status", st)

	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			send(ev.Type, ev.Data)
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}
