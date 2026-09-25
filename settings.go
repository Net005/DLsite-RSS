package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Settings are editable at runtime from the web UI and persisted to settings.json.
type Settings struct {
	AutoScrape    bool    `json:"auto_scrape"`
	IntervalHours float64 `json:"interval_hours"`
	NewItemsDays  int     `json:"new_items_days"`
	TargetGames   int     `json:"target_games"`
	MaxPages      int     `json:"max_pages"`
	PageDelaySec  int     `json:"page_delay_sec"`
	Mode          string  `json:"mode"`     // auto | http | browser
	Term          string  `json:"term"`     // day | week | month
	Category      string  `json:"category"` // game | comic | voice
	Locale        string  `json:"locale"`
	GamesOnly     bool    `json:"games_only"`
	FeedTitle     string  `json:"feed_title"`
	PublicURL     string  `json:"public_url"`
	WebhookURL    string  `json:"webhook_url"`
}

func DefaultSettings() Settings {
	return Settings{
		AutoScrape: true, IntervalHours: 12, NewItemsDays: 7, TargetGames: 100, MaxPages: 6,
		PageDelaySec: 3, Mode: "auto", Term: "month", Category: "game", Locale: "en_US",
		GamesOnly: true, FeedTitle: "DLsite Monthly Game Ranking",
	}
}

func (s *Settings) Validate() error {
	if s.IntervalHours < 0.25 || s.IntervalHours > 24*30 {
		return errors.New("interval_hours must be between 0.25 and 720")
	}
	if s.NewItemsDays < 1 || s.NewItemsDays > 365 {
		return errors.New("new_items_days must be between 1 and 365")
	}
	if s.TargetGames < 1 || s.TargetGames > 500 {
		return errors.New("target_games must be between 1 and 500")
	}
	if s.MaxPages < 1 || s.MaxPages > 20 {
		return errors.New("max_pages must be between 1 and 20")
	}
	if s.PageDelaySec < 0 || s.PageDelaySec > 60 {
		return errors.New("page_delay_sec must be between 0 and 60")
	}
	switch s.Mode {
	case "auto", "http", "browser":
	default:
		return errors.New("mode must be auto, http or browser")
	}
	switch s.Term {
	case "day", "week", "month":
	default:
		return errors.New("term must be day, week or month")
	}
	switch s.Category {
	case "game", "comic", "voice":
	default:
		return errors.New("category must be game, comic or voice")
	}
	if !strings.HasPrefix(s.Locale, "") || len(s.Locale) < 2 || len(s.Locale) > 8 {
		return errors.New("invalid locale")
	}
	for _, u := range []string{s.PublicURL, s.WebhookURL} {
		if u == "" {
			continue
		}
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			return fmt.Errorf("invalid URL: %s", u)
		}
	}
	s.PublicURL = strings.TrimRight(s.PublicURL, "/")
	if strings.TrimSpace(s.FeedTitle) == "" {
		s.FeedTitle = "DLsite Monthly Game Ranking"
	}
	return nil
}

// Source URLs derived from the settings.
func (s Settings) PageURL() string { // server-rendered (top 30 per category block)
	return fmt.Sprintf("https://www.dlsite.com/maniax/ranking?term=%s&category=%s&locale=%s", s.Term, s.Category, s.Locale)
}
func (s Settings) FullURL(page int) string { // full ranking, needs a real browser (WAF)
	u := fmt.Sprintf("https://www.dlsite.com/maniax/ranking/%s?category=%s&locale=%s", s.Term, s.Category, s.Locale)
	if page > 1 {
		u += fmt.Sprintf("&page=%d", page)
	}
	return u
}

type SettingsStore struct {
	mu   sync.RWMutex
	path string
	v    Settings
}

func NewSettingsStore(path string) *SettingsStore {
	return &SettingsStore{path: path, v: DefaultSettings()}
}

func (s *SettingsStore) Load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	v := DefaultSettings()
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if err := v.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.v = v
	s.mu.Unlock()
	return nil
}

func (s *SettingsStore) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.v
}

func (s *SettingsStore) Set(v Settings) error {
	if err := v.Validate(); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.path, b, 0o644); err != nil {
		return err
	}
	s.mu.Lock()
	s.v = v
	s.mu.Unlock()
	return nil
}
