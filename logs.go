package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// LogEntry is one line of the live log.
type LogEntry struct {
	ID    int64  `json:"id"`
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// Event is pushed to every connected browser over SSE.
type Event struct {
	Type string          `json:"type"` // log | status | items
	Data json.RawMessage `json:"data"`
}

// Hub keeps a ring buffer of recent log lines, mirrors them to stdout and a
// log file, and fans events out to SSE subscribers.
type Hub struct {
	mu     sync.Mutex
	ring   []LogEntry
	max    int
	nextID int64
	subs   map[chan Event]struct{}
	file   *os.File
}

func NewHub(max int, logPath string) *Hub {
	h := &Hub{max: max, subs: map[chan Event]struct{}{}}
	if logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			h.file = f
		}
	}
	return h
}

func (h *Hub) logf(level, format string, a ...any) {
	e := LogEntry{Time: time.Now().Format("2006-01-02 15:04:05"), Level: level, Msg: fmt.Sprintf(format, a...)}
	h.mu.Lock()
	h.nextID++
	e.ID = h.nextID
	h.ring = append(h.ring, e)
	if len(h.ring) > h.max {
		h.ring = h.ring[len(h.ring)-h.max:]
	}
	line := fmt.Sprintf("%s [%s] %s\n", e.Time, strings.ToUpper(e.Level), e.Msg)
	os.Stdout.WriteString(line)
	if h.file != nil {
		h.file.WriteString(line)
	}
	h.mu.Unlock()
	b, _ := json.Marshal(e)
	h.publish(Event{Type: "log", Data: b})
}

func (h *Hub) Info(f string, a ...any)  { h.logf("info", f, a...) }
func (h *Hub) Warn(f string, a ...any)  { h.logf("warn", f, a...) }
func (h *Hub) Error(f string, a ...any) { h.logf("error", f, a...) }

// Recent returns the last n log entries.
func (h *Hub) Recent(n int) []LogEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n <= 0 || n > len(h.ring) {
		n = len(h.ring)
	}
	out := make([]LogEntry, n)
	copy(out, h.ring[len(h.ring)-n:])
	return out
}

func (h *Hub) Clear() {
	h.mu.Lock()
	h.ring = nil
	h.mu.Unlock()
}

func (h *Hub) Subscribe() chan Event {
	ch := make(chan Event, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) Unsubscribe(ch chan Event) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *Hub) publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // slow client: drop rather than block the scraper
		}
	}
}

// Emit publishes a non-log event (status / items).
func (h *Hub) Emit(typ string, v any) {
	b, _ := json.Marshal(v)
	h.publish(Event{Type: typ, Data: b})
}
