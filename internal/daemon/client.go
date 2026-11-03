package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cistern/internal/admin"
)

var ErrUnavailable = errors.New("daemon unavailable")

type APIError struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
}

func (e *APIError) Error() string { return fmt.Sprintf("daemon: %s (%d)", e.Message, e.Status) }

type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

func NewClient(baseURL, token string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("daemon url: want http(s)://host:port, got %q", baseURL)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{base: u, token: token, http: hc}, nil
}

type Status struct {
	Version  string       `json:"version"`
	Host     string       `json:"host"`
	Timezone string       `json:"timezone"`
	API      bool         `json:"api"`
	Tasks    []TaskStatus `json:"tasks"`
	Running  []RunningOp  `json:"running"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %v", ErrUnavailable, method, path, errors.Unwrap(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("%w: reading response: %v", ErrUnavailable, err)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error apiError `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error.Message == "" {
			e.Error.Message = http.StatusText(resp.StatusCode)
		}
		ae := &APIError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message, Fields: e.Error.Fields}
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return fmt.Errorf("%w: the daemon rejected the token", ErrUnavailable)
		case resp.StatusCode == http.StatusNotFound && path != "" && strings.HasPrefix(path, "/internal/") && e.Error.Code == "":
			return fmt.Errorf("%w: the daemon internal API is not enabled (daemon.api_token)", ErrUnavailable)
		case resp.StatusCode >= 500:
			return fmt.Errorf("%w: %s", ErrUnavailable, ae.Message)
		}
		return ae
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: invalid response: %v", ErrUnavailable, err)
	}
	return nil
}

func (c *Client) Status(ctx context.Context) (*Status, error) {
	var st Status
	if err := c.do(ctx, http.MethodGet, "/status", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (c *Client) StartRun(ctx context.Context, req *RunRequest) (string, error) {
	var out struct {
		RunID string `json:"run_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/internal/v1/runs", req, &out); err != nil {
		return "", err
	}
	return out.RunID, nil
}

func (c *Client) Run(ctx context.Context, id string) (*admin.Run, error) {
	var run admin.Run
	err := c.do(ctx, http.MethodGet, "/internal/v1/runs/"+url.PathEscape(id), nil, &run)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (c *Client) Runs(ctx context.Context, active bool) ([]admin.Run, error) {
	var out struct {
		Runs []admin.Run `json:"runs"`
	}
	q := ""
	if active {
		q = "?active=true"
	}
	if err := c.do(ctx, http.MethodGet, "/internal/v1/runs"+q, nil, &out); err != nil {
		return nil, err
	}
	return out.Runs, nil
}
