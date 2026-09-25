package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

// Run is one entry in the scrape history.
type Run struct {
	ID         int    `json:"id"`
	Started    string `json:"started"`
	DurationMs int64  `json:"duration_ms"`
	Trigger    string `json:"trigger"` // startup | schedule | manual
	Source     string `json:"source"`  // browser | http
	Status     string `json:"status"`  // ok | error | cancelled
	Found      int    `json:"found"`
	New        int    `json:"new"`
	Error      string `json:"error,omitempty"`
}

// Status is what the dashboard polls / receives over SSE.
type Status struct {
	Running    bool    `json:"running"`
	Phase      string  `json:"phase"`
	Trigger    string  `json:"trigger,omitempty"`
	Progress   int     `json:"progress"` // games collected so far in this run
	LastRun    *Run    `json:"last_run"`
	LastOK     *Run    `json:"last_ok"`
	NextRun    string  `json:"next_run"`
	Total      int     `json:"total"`
	NewInFeed  int     `json:"new_in_feed"`
	Uptime     int64   `json:"uptime_sec"`
	Version    string  `json:"version"`
	Chrome     bool    `json:"chrome_available"`
	AutoScrape bool    `json:"auto_scrape"`
	Interval   float64 `json:"interval_hours"`
}

type Runner struct {
	cfg     *SettingsStore
	store   *Store
	hub     *Hub
	started time.Time
	chrome  string

	mu       sync.Mutex
	running  bool
	phase    string
	trigger  string
	progress int
	cancel   context.CancelFunc
	runs     []Run
	runsPath string
	nextRun  time.Time
	wake     chan struct{}
}

func NewRunner(cfg *SettingsStore, st *Store, hub *Hub, runsPath string) *Runner {
	r := &Runner{cfg: cfg, store: st, hub: hub, runsPath: runsPath, started: time.Now(),
		chrome: findChrome(), wake: make(chan struct{}, 1), phase: "idle"}
	if b, err := os.ReadFile(runsPath); err == nil {
		json.Unmarshal(b, &r.runs)
	}
	return r
}

func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.cfg.Get()
	st := Status{Running: r.running, Phase: r.phase, Trigger: r.trigger, Progress: r.progress,
		Total: r.store.Count(), Uptime: int64(time.Since(r.started).Seconds()), Version: version,
		Chrome: r.chrome != "", AutoScrape: s.AutoScrape, Interval: s.IntervalHours}
	cutoff := float64(time.Now().Unix()) - float64(s.NewItemsDays)*86400
	for _, it := range r.store.Snapshot() {
		if it.FirstSeen >= cutoff && !it.Hidden {
			st.NewInFeed++
		}
	}
	if n := len(r.runs); n > 0 {
		lr := r.runs[n-1]
		st.LastRun = &lr
		for i := n - 1; i >= 0; i-- {
			if r.runs[i].Status == "ok" {
				ok := r.runs[i]
				st.LastOK = &ok
				break
			}
		}
	}
	if s.AutoScrape && !r.nextRun.IsZero() && !r.running {
		st.NextRun = r.nextRun.Format(time.RFC3339)
	}
	return st
}

func (r *Runner) Runs() []Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Run, len(r.runs))
	copy(out, r.runs)
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

func (r *Runner) ClearRuns() {
	r.mu.Lock()
	r.runs = nil
	r.mu.Unlock()
	os.Remove(r.runsPath)
}

func (r *Runner) emitStatus() { r.hub.Emit("status", r.Status()) }

func (r *Runner) setPhase(p string) {
	r.mu.Lock()
	r.phase = p
	r.mu.Unlock()
	r.emitStatus()
}

// Wake makes the scheduler recompute its next run (after a settings change).
func (r *Runner) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Trigger starts a scrape in the background. Returns an error if one is already running.
func (r *Runner) Trigger(trigger string) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return errors.New("a scrape is already running")
	}
	r.running = true
	r.trigger = trigger
	r.progress = 0
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.mu.Unlock()
	go r.run(ctx, trigger)
	return nil
}

func (r *Runner) Cancel() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running || r.cancel == nil {
		return false
	}
	r.cancel()
	return true
}

// Loop is the background scheduler.
func (r *Runner) Loop(ctx context.Context) {
	first := true
	for {
		s := r.cfg.Get()
		var wait time.Duration
		r.mu.Lock()
		switch {
		case !s.AutoScrape:
			r.nextRun = time.Time{}
			wait = 24 * time.Hour
		case len(r.runs) == 0 || first && r.store.Count() == 0:
			wait = 3 * time.Second
			r.nextRun = time.Now().Add(wait)
		default:
			last := r.runs[len(r.runs)-1]
			t, _ := time.Parse(time.RFC3339, last.Started)
			next := t.Add(time.Duration(last.DurationMs) * time.Millisecond).Add(time.Duration(s.IntervalHours * float64(time.Hour)))
			wait = time.Until(next)
			if wait < 5*time.Second {
				wait = 5 * time.Second
			}
			r.nextRun = time.Now().Add(wait)
		}
		r.mu.Unlock()
		r.emitStatus()
		first = false

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-r.wake:
			timer.Stop()
		case <-timer.C:
			if s.AutoScrape {
				trig := "schedule"
				r.mu.Lock()
				if len(r.runs) == 0 {
					trig = "startup"
				}
				r.mu.Unlock()
				if err := r.Trigger(trig); err == nil {
					r.waitIdle(ctx)
				}
			}
		}
	}
}

func (r *Runner) waitIdle(ctx context.Context) {
	for {
		r.mu.Lock()
		run := r.running
		r.mu.Unlock()
		if !run {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (r *Runner) run(ctx context.Context, trigger string) {
	start := time.Now()
	s := r.cfg.Get()
	r.hub.Info("=== Scrape started (%s) — mode=%s term=%s category=%s target=%d ===", trigger, s.Mode, s.Term, s.Category, s.TargetGames)
	r.setPhase("starting")

	scraped, source, err := r.scrape(ctx, s)

	run := Run{Started: start.Format(time.RFC3339), Trigger: trigger, Source: source, Found: len(scraped)}
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		run.Status, run.Error = "cancelled", "cancelled by user"
		r.hub.Warn("Scrape cancelled.")
	case err != nil:
		run.Status, run.Error = "error", err.Error()
		r.hub.Error("Scrape failed: %v", err)
	default:
		r.setPhase("saving")
		added, serr := r.store.Merge(scraped, float64(time.Now().UnixNano())/1e9)
		if serr != nil {
			r.hub.Error("Failed to save state: %v", serr)
		}
		run.Status, run.New = "ok", len(added)
		for i, it := range added {
			if i == 15 {
				r.hub.Info("… and %d more new games.", len(added)-15)
				break
			}
			r.hub.Info("New game: #%d %s [%s]", it.Rank, it.Title, it.ProductID)
		}
		r.hub.Info("Total: %d games scraped via %s, %d new.", len(scraped), source, len(added))
		if len(added) > 0 && s.WebhookURL != "" {
			r.notify(s, added)
		}
	}
	run.DurationMs = time.Since(start).Milliseconds()

	r.mu.Lock()
	run.ID = 1
	if n := len(r.runs); n > 0 {
		run.ID = r.runs[n-1].ID + 1
	}
	r.runs = append(r.runs, run)
	if len(r.runs) > 100 {
		r.runs = r.runs[len(r.runs)-100:]
	}
	b, _ := json.MarshalIndent(r.runs, "", "  ")
	os.WriteFile(r.runsPath, b, 0o644)
	r.running, r.phase, r.cancel, r.progress = false, "idle", nil, 0
	r.mu.Unlock()

	r.hub.Info("=== Scrape finished: %s in %.1fs ===", run.Status, time.Since(start).Seconds())
	r.emitStatus()
	r.hub.Emit("items", map[string]int{"total": r.store.Count()})
	r.Wake()
}

// scrape picks a source according to the mode and falls back when needed.
func (r *Runner) scrape(ctx context.Context, s Settings) ([]Scraped, string, error) {
	wantBrowser := s.Mode == "browser" || (s.Mode == "auto" && r.chrome != "")
	if s.Mode == "auto" && r.chrome == "" {
		r.hub.Warn("Chromium not found — using plain HTTP (only the top ~30 per category are available).")
	}
	if s.Mode == "browser" && r.chrome == "" {
		return nil, "browser", errors.New("browser mode selected but Chromium was not found (set CHROME_PATH)")
	}
	if wantBrowser {
		res, err := r.scrapeBrowser(ctx, s)
		if err == nil && len(res) > 0 {
			return res, "browser", nil
		}
		if err == nil {
			err = errors.New("browser scrape returned no games")
		}
		if s.Mode == "browser" || ctx.Err() != nil {
			return nil, "browser", err
		}
		r.hub.Warn("Browser scrape failed (%v) — falling back to HTTP.", err)
	}
	res, err := r.scrapeHTTP(ctx, s)
	return res, "http", err
}

func (r *Runner) filter(s Settings, in []Scraped) []Scraped {
	if !s.GamesOnly {
		return in
	}
	types := categoryTypes[s.Category]
	var out []Scraped
	for _, e := range in {
		if e.Category == "" || types[e.Category] {
			out = append(out, e)
		}
	}
	return out
}

func (r *Runner) scrapeHTTP(ctx context.Context, s Settings) ([]Scraped, error) {
	r.setPhase("fetching (http)")
	u := s.PageURL()
	r.hub.Info("HTTP GET %s", u)
	html, err := fetchHTTP(ctx, u, s.Locale)
	if err != nil {
		return nil, err
	}
	blocks, err := parseBlocks(html)
	if err != nil {
		return nil, err
	}
	r.hub.Info("Parsed %d ranking list(s) on the page.", len(blocks))
	res := r.filter(s, bestBlock(blocks, s.Category))
	if len(res) > s.TargetGames {
		res = res[:s.TargetGames]
	}
	if len(res) == 0 {
		return nil, errors.New("no ranking rows found (page layout may have changed)")
	}
	r.mu.Lock()
	r.progress = len(res)
	r.mu.Unlock()
	return res, nil
}

func (r *Runner) scrapeBrowser(ctx context.Context, s Settings) ([]Scraped, error) {
	r.setPhase("launching browser")
	r.hub.Info("Launching headless Chromium (%s)…", r.chrome)
	b, err := NewBrowser(ctx, r.chrome)
	if err != nil {
		return nil, fmt.Errorf("could not start Chromium: %w", err)
	}
	defer b.Close()

	var all []Scraped
	seen := map[string]bool{}
	maxRank := 0
	for page := 1; page <= s.MaxPages; page++ {
		if ctx.Err() != nil {
			return all, ctx.Err()
		}
		u := s.FullURL(page)
		r.setPhase(fmt.Sprintf("page %d", page))
		r.hub.Info("Loading page %d: %s", page, u)
		html, err := b.Fetch(u, 75*time.Second)
		if err != nil {
			if page == 1 {
				return nil, fmt.Errorf("page 1: %w", err)
			}
			r.hub.Warn("Page %d failed (%v) — stopping pagination.", page, err)
			break
		}
		blocks, err := parseBlocks(html)
		if err != nil {
			return nil, err
		}
		rows := r.filter(s, bestBlock(blocks, s.Category))
		added := 0
		restart := len(rows) > 0 && rows[0].Rank > 0 && rows[0].Rank <= maxRank // rank numbering restarted
		for _, e := range rows {
			if seen[e.ProductID] {
				continue
			}
			seen[e.ProductID] = true
			if e.Rank == 0 || restart || (page > 1 && e.Rank <= maxRank) {
				e.Rank = len(all) + 1
			}
			if e.Rank > maxRank {
				maxRank = e.Rank
			}
			all = append(all, e)
			added++
			if len(all) >= s.TargetGames {
				break
			}
		}
		r.mu.Lock()
		r.progress = len(all)
		r.mu.Unlock()
		r.emitStatus()
		r.hub.Info("Page %d: %d new games, %d total.", page, added, len(all))
		if len(all) >= s.TargetGames {
			r.hub.Info("Reached target of %d games.", s.TargetGames)
			break
		}
		if added == 0 {
			r.hub.Info("No new games on this page — end of ranking.")
			break
		}
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		case <-time.After(time.Duration(s.PageDelaySec) * time.Second):
		}
	}
	return all, nil
}

// notify posts new games to a Discord-compatible webhook.
func (r *Runner) notify(s Settings, added []Item) {
	type embed struct {
		Title       string         `json:"title"`
		URL         string         `json:"url"`
		Description string         `json:"description"`
		Color       int            `json:"color"`
		Thumbnail   map[string]any `json:"thumbnail,omitempty"`
	}
	sort.Slice(added, func(i, j int) bool { return added[i].Rank < added[j].Rank })
	var embeds []embed
	for i, it := range added {
		if i >= 10 {
			break
		}
		embeds = append(embeds, embed{Title: fmt.Sprintf("#%d  %s", it.Rank, it.Title), URL: workBaseURL + it.ProductID + ".html",
			Description: it.Circle, Color: 0xe60026, Thumbnail: map[string]any{"url": normURL(it.CoverURL)}})
	}
	body, _ := json.Marshal(map[string]any{
		"username": "DLsite RSS",
		"content":  fmt.Sprintf("**%d new game(s)** entered the DLsite ranking", len(added)),
		"embeds":   embeds,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", s.WebhookURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.hub.Warn("Webhook failed: %v", err)
		return
	}
	resp.Body.Close()
	r.hub.Info("Webhook sent (HTTP %d).", resp.StatusCode)
}

// TestWebhook sends a sample message.
func (r *Runner) TestWebhook() error {
	s := r.cfg.Get()
	if s.WebhookURL == "" {
		return errors.New("no webhook URL configured")
	}
	body, _ := json.Marshal(map[string]any{"username": "DLsite RSS", "content": "Test message from DLsite RSS — webhook works."})
	resp, err := http.Post(s.WebhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}
