package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Item is one tracked game. The JSON layout is backwards compatible with the
// old Python dlsite_seen_titles.json (extra fields are optional).
type Item struct {
	ProductID string  `json:"-"`
	Title     string  `json:"title"`
	Rank      int     `json:"rank"`
	FirstSeen float64 `json:"first_seen"`
	Circle    string  `json:"circle"`
	CoverURL  string  `json:"cover_url"`
	// Extensions (omitted in old files)
	PrevRank int     `json:"prev_rank,omitempty"`
	LastSeen float64 `json:"last_seen,omitempty"`
	Category string  `json:"category,omitempty"`
	Price    string  `json:"price,omitempty"`
	Hidden   bool    `json:"hidden,omitempty"`
}

// Store is the on-disk state file guarded by a mutex.
type Store struct {
	mu    sync.RWMutex
	path  string
	items map[string]*Item
}

func NewStore(path string) *Store { return &Store{path: path, items: map[string]*Item{}} }

func (s *Store) Load() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	m := map[string]*Item{}
	if err := json.Unmarshal(b, &m); err != nil {
		return 0, err
	}
	for k, v := range m {
		v.ProductID = k
	}
	s.items = m
	return len(m), nil
}

// saveLocked writes atomically (temp file + rename). Caller holds s.mu.
func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// Snapshot returns copies of all items.
func (s *Store) Snapshot() []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Item, 0, len(s.items))
	for _, v := range s.items {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		return out[i].ProductID < out[j].ProductID
	})
	return out
}

func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

func (s *Store) Delete(pid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[pid]; !ok {
		return false
	}
	delete(s.items, pid)
	s.saveLocked()
	return true
}

func (s *Store) SetHidden(pid string, hidden bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[pid]
	if !ok {
		return false
	}
	it.Hidden = hidden
	s.saveLocked()
	return true
}

func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = map[string]*Item{}
	s.saveLocked()
}

// Replace swaps in an imported state (same format as the file on disk).
func (s *Store) Replace(data []byte) (int, error) {
	m := map[string]*Item{}
	if err := json.Unmarshal(data, &m); err != nil {
		return 0, err
	}
	for k, v := range m {
		v.ProductID = k
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = m
	return len(m), s.saveLocked()
}

func (s *Store) Export() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return json.MarshalIndent(s.items, "", "  ")
}

// Merge folds a fresh scrape into the state and returns the newly discovered items.
func (s *Store) Merge(scraped []Scraped, now float64) (added []Item, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range scraped {
		it, ok := s.items[e.ProductID]
		if !ok {
			it = &Item{ProductID: e.ProductID, Title: e.Title, Rank: e.Rank, FirstSeen: now,
				Circle: e.Circle, CoverURL: e.CoverURL, LastSeen: now, Category: e.Category, Price: e.Price}
			s.items[e.ProductID] = it
			added = append(added, *it)
			continue
		}
		if it.Rank != e.Rank {
			it.PrevRank = it.Rank
		}
		it.Rank = e.Rank
		it.LastSeen = now
		if e.CoverURL != "" {
			it.CoverURL = e.CoverURL
		}
		if e.Circle != "" {
			it.Circle = e.Circle
		}
		if e.Category != "" {
			it.Category = e.Category
		}
		if e.Price != "" {
			it.Price = e.Price
		}
	}
	return added, s.saveLocked()
}
