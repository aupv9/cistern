package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cistern/internal/config"
)

type capture struct {
	mu     sync.Mutex
	bodies map[string]map[string]any
	hits   map[string]int
}

func (c *capture) handler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		c.mu.Lock()
		c.bodies[r.URL.Path] = m
		c.hits[r.URL.Path]++
		c.mu.Unlock()
		w.WriteHeader(status)
	}
}

func TestFormatsAndFilter(t *testing.T) {
	c := &capture{bodies: map[string]map[string]any{}, hits: map[string]int{}}
	srv := httptest.NewServer(c.handler(200))
	defer srv.Close()
	hooks := []*config.Webhook{
		{URL: srv.URL + "/slack", Format: "slack", On: []string{"failure"}},
		{URL: srv.URL + "/tg", Format: "telegram", ChatID: "-100", On: []string{"failure", "success"}},
		{URL: srv.URL + "/json", Format: "json", On: []string{"success"}},
	}
	n := New(hooks, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Now()
	ev := Event{Job: "orders", Op: "backup", Status: StatusFailure, Host: "h1", Error: "pg_dump: connection refused", StartedAt: now, FinishedAt: now.Add(3 * time.Second)}
	if errs := n.Send(context.Background(), ev); len(errs) != 0 {
		t.Fatal(errs)
	}
	if txt, _ := c.bodies["/slack"]["text"].(string); !strings.Contains(txt, "backup FAILED: job orders on h1") || !strings.Contains(txt, "connection refused") {
		t.Errorf("slack text: %q", txt)
	}
	if c.bodies["/tg"]["chat_id"] != "-100" {
		t.Errorf("telegram: %v", c.bodies["/tg"])
	}
	if c.hits["/json"] != 0 {
		t.Error("json hook fired on failure")
	}

	ev.Status, ev.Error, ev.Snapshot, ev.Artifacts, ev.StoredBytes = StatusSuccess, "", "20261102T000000Z-abcd", 3, 5<<20
	n.Send(context.Background(), ev)
	if c.hits["/slack"] != 1 || c.bodies["/json"]["snapshot"] != "20261102T000000Z-abcd" || c.bodies["/json"]["stored_bytes"].(float64) != 5<<20 {
		t.Errorf("success routing: hits=%v json=%v", c.hits, c.bodies["/json"])
	}
}

func TestRetryAndRedact(t *testing.T) {
	c := &capture{bodies: map[string]map[string]any{}, hits: map[string]int{}}
	srv := httptest.NewServer(c.handler(503))
	defer srv.Close()
	n := New([]*config.Webhook{{URL: srv.URL + "/services/T000/SECRET", Format: "slack", On: []string{"failure"}}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	errs := n.Send(context.Background(), Event{Job: "j", Op: "backup", Status: StatusFailure})
	if len(errs) != 1 || c.hits["/services/T000/SECRET"] != 3 {
		t.Fatalf("errs=%v hits=%v", errs, c.hits)
	}
	if got := redact(`Post "https://hooks.slack.com/services/T000/SECRET": dial tcp`); strings.Contains(got, "SECRET") {
		t.Errorf("not redacted: %s", got)
	}
}
