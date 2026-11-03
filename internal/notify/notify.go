package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cistern/internal/config"
)

const (
	StatusSuccess = "success"
	StatusFailure = "failure"
)

type Event struct {
	Job         string    `json:"job"`
	Op          string    `json:"op"`
	Status      string    `json:"status"`
	Snapshot    string    `json:"snapshot,omitempty"`
	Error       string    `json:"error,omitempty"`
	Host        string    `json:"host"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	Artifacts   int       `json:"artifacts,omitempty"`
	StoredBytes int64     `json:"stored_bytes,omitempty"`
	Removed     int       `json:"removed,omitempty"`
}

func (e Event) Text() string {
	icon, verb := "✅", "succeeded"
	if e.Status == StatusFailure {
		icon, verb = "❌", "FAILED"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s cistern %s %s: job %s on %s", icon, e.Op, verb, e.Job, e.Host)
	if e.Snapshot != "" {
		fmt.Fprintf(&sb, ", snapshot %s", e.Snapshot)
	}
	if e.Artifacts > 0 {
		fmt.Fprintf(&sb, ", %d artifacts, %s", e.Artifacts, humanBytes(e.StoredBytes))
	}
	if e.Removed > 0 {
		fmt.Fprintf(&sb, ", removed %d snapshots", e.Removed)
	}
	fmt.Fprintf(&sb, " (%s)", e.FinishedAt.Sub(e.StartedAt).Round(time.Second))
	if e.Error != "" {
		msg := e.Error
		if len(msg) > 1500 {
			msg = msg[:1500] + "…"
		}
		fmt.Fprintf(&sb, "\n%s", msg)
	}
	return sb.String()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

type Notifier struct {
	hooks  []*config.Webhook
	log    *slog.Logger
	client *http.Client
}

func New(hooks []*config.Webhook, log *slog.Logger) *Notifier {
	return &Notifier{hooks: hooks, log: log, client: &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}}
}

func (n *Notifier) Send(ctx context.Context, ev Event) []error {
	var errs []error
	for _, h := range n.hooks {
		if !wants(h, ev.Status) {
			continue
		}
		if err := n.send(ctx, h, ev); err != nil {
			n.log.Warn("webhook failed", "job", ev.Job, "url", redact(h.URL), "err", err)
			errs = append(errs, err)
		}
	}
	return errs
}

func wants(h *config.Webhook, status string) bool {
	for _, on := range h.On {
		if on == status {
			return true
		}
	}
	return false
}

func payload(h *config.Webhook, ev Event) any {
	switch h.Format {
	case "slack", "teams":
		return map[string]string{"text": ev.Text()}
	case "telegram":
		return map[string]string{"chat_id": h.ChatID, "text": ev.Text()}
	}
	return ev
}

func (n *Notifier) send(ctx context.Context, h *config.Webhook, ev Event) error {
	body, err := json.Marshal(payload(h, ev))
	if err != nil {
		return err
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := n.client.Do(req)
		if err != nil {
			last = fmt.Errorf("%s", redact(err.Error()))
			continue
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			return nil
		}
		last = fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return last
		}
	}
	return last
}

func redact(s string) string {
	for _, f := range strings.Fields(s) {
		f = strings.Trim(f, `"':`)
		if u, err := url.Parse(f); err == nil && u.Scheme != "" && u.Host != "" {
			s = strings.ReplaceAll(s, f, u.Scheme+"://"+u.Host+"/…")
		}
	}
	return s
}
