package main

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

//go:embed web
var webAssets embed.FS

var version = "dev"

func webFS() fs.FS {
	sub, _ := fs.Sub(webAssets, "web")
	return sub
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	dataDir := env("DATA_DIR", "./data")
	port := env("PORT", "6050")
	host := env("HOST", "0.0.0.0")
	os.MkdirAll(dataDir, 0o755)

	hub := NewHub(1000, filepath.Join(dataDir, "dlsite-rss.log"))
	cfg := NewSettingsStore(filepath.Join(dataDir, "settings.json"))
	if err := cfg.Load(); err != nil {
		hub.Error("Failed to load settings (using defaults): %v", err)
	}
	// Optional environment overrides for first start / container deployments.
	s := cfg.Get()
	if v, err := strconv.ParseFloat(os.Getenv("SCRAPE_INTERVAL_HOURS"), 64); err == nil && os.Getenv("SETTINGS_FROM_ENV") != "" {
		s.IntervalHours = v
	}
	if v := os.Getenv("PUBLIC_URL"); v != "" {
		s.PublicURL = v
	}
	if v := os.Getenv("WEBHOOK_URL"); v != "" {
		s.WebhookURL = v
	}
	if v := os.Getenv("SCRAPE_MODE"); v != "" {
		s.Mode = v
	}
	if err := s.Validate(); err == nil {
		s2 := s
		cfg.mu.Lock()
		cfg.v = s2
		cfg.mu.Unlock()
	}

	st := NewStore(filepath.Join(dataDir, "dlsite_seen_titles.json"))
	if n, err := st.Load(); err != nil {
		hub.Error("Failed to load state: %v", err)
	} else if n == 0 {
		hub.Info("No state found — starting fresh.")
	} else {
		hub.Info("Loaded state: %d entries.", n)
	}

	runner := NewRunner(cfg, st, hub, filepath.Join(dataDir, "runs.json"))
	if runner.chrome != "" {
		hub.Info("Chromium found: %s", runner.chrome)
	} else {
		hub.Warn("Chromium not found — only HTTP mode (top ~30) will work.")
	}
	app := &App{cfg: cfg, store: st, hub: hub, runner: runner, auth: NewAuth(env("ADMIN_USER", "admin"), os.Getenv("ADMIN_PASS"), dataDir, hub)}
	if !app.auth.Enabled() {
		hub.Warn("ADMIN_PASS is not set — the control panel is open to anyone who can reach this port.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go runner.Loop(ctx)

	srv := &http.Server{Addr: host + ":" + port, Handler: app.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		runner.Cancel()
		sc, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		srv.Shutdown(sc)
	}()
	hub.Info("DLsite RSS %s listening on http://%s:%s (data: %s)", version, host, port, dataDir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		hub.Error("Server error: %v", err)
		os.Exit(1)
	}
}
