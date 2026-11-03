package daemon

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"cistern/internal/admin"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestInternalAPI(t *testing.T) {
	addr := freePort(t)
	release := make(chan struct{})
	opt := Options{
		Listen: addr, Grace: 5 * time.Second, Log: quiet(), Location: time.UTC, APIToken: testToken,
		Prepare: func(req *RunRequest) error {
			if req.Job == "missing" {
				return ErrNotFound
			}
			if req.Target == "bad" {
				return &InvalidError{Message: "bad target", Fields: map[string]string{"target": "bad"}}
			}
			return nil
		},
		Execute: func(ctx context.Context, req *RunRequest) (Result, error) {
			req.Log.Info("working", "job", req.Job)
			req.Progress.AddArtifact()
			if req.Job == "slow" {
				<-release
			}
			if req.Job == "broken" {
				return Result{}, errors.New("dump crashed")
			}
			return Result{Snapshot: "20260101T000000Z-abcd", StoredBytes: 42}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, opt) }()
	var c *Client
	for i := 0; i < 50; i++ {
		c, _ = NewClient("http://"+addr, testToken, nil)
		if _, err := c.Status(context.Background()); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	resp, err := http.Post("http://"+addr+"/internal/v1/runs", "application/json", strings.NewReader(`{"op":"backup","job":"db"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	bad, _ := NewClient("http://"+addr, testToken+"x", nil)
	if _, err := bad.StartRun(context.Background(), &RunRequest{Op: "backup", Job: "db"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("wrong token: %v", err)
	}

	actor := &admin.Actor{ID: "u1", Name: "U"}
	id, err := c.StartRun(context.Background(), &RunRequest{Op: "backup", Job: "db", RequestedBy: actor})
	if err != nil || !admin.ValidID(id) {
		t.Fatalf("start: %q %v", id, err)
	}
	run := waitRun(t, c, id)
	if run.State != admin.StateSucceeded || run.Snapshot != "20260101T000000Z-abcd" || run.Trigger != admin.TriggerUI ||
		run.RequestedBy.ID != "u1" || len(run.LogTail) == 0 || !strings.Contains(run.LogTail[0], "working") || run.Progress.ArtifactsDone != 1 {
		t.Fatalf("run: %+v", run)
	}

	fid, _ := c.StartRun(context.Background(), &RunRequest{Op: "backup", Job: "broken"})
	if r := waitRun(t, c, fid); r.State != admin.StateFailed || r.Error != "dump crashed" {
		t.Fatalf("failed run: %+v", r)
	}

	sid, err := c.StartRun(context.Background(), &RunRequest{Op: "verify", Job: "slow", RunID: admin.NewID(time.Now())})
	if err != nil {
		t.Fatal(err)
	}
	var ae *APIError
	if _, err := c.StartRun(context.Background(), &RunRequest{Op: "backup", Job: "slow"}); !errors.As(err, &ae) || ae.Status != http.StatusConflict {
		t.Fatalf("busy: %v", err)
	}
	if _, err := c.StartRun(context.Background(), &RunRequest{Op: "backup", Job: "missing"}); !errors.As(err, &ae) || ae.Status != http.StatusNotFound {
		t.Fatalf("missing job: %v", err)
	}
	if _, err := c.StartRun(context.Background(), &RunRequest{Op: "restore", Job: "db", Target: "bad"}); !errors.As(err, &ae) || ae.Status != http.StatusUnprocessableEntity || ae.Fields["target"] != "bad" {
		t.Fatalf("invalid: %v", err)
	}
	if _, err := c.StartRun(context.Background(), &RunRequest{Op: "prune", Job: "db"}); !errors.As(err, &ae) || ae.Status != http.StatusUnprocessableEntity {
		t.Fatalf("prune must be rejected: %v", err)
	}
	st, err := c.Status(context.Background())
	if err != nil || !st.API || len(st.Running) != 1 || st.Running[0].Job != "slow" || st.Running[0].RunID != sid || st.Timezone != "UTC" {
		t.Fatalf("status: %+v %v", st, err)
	}
	active, err := c.Runs(context.Background(), true)
	if err != nil || len(active) != 1 || active[0].ID != sid {
		t.Fatalf("active runs: %+v %v", active, err)
	}
	if _, err := c.Run(context.Background(), admin.NewID(time.Now())); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown run: %v", err)
	}
	close(release)
	waitRun(t, c, sid)
	cancel()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func waitRun(t *testing.T, c *Client, id string) *admin.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, err := c.Run(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if r.State == admin.StateSucceeded || r.State == admin.StateFailed {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish", id)
	return nil
}

func TestAPIDisabledWithoutToken(t *testing.T) {
	addr := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- Run(ctx, Options{Listen: addr, Log: quiet(), Execute: func(context.Context, *RunRequest) (Result, error) { return Result{}, nil }})
	}()
	c, _ := NewClient("http://"+addr, testToken, nil)
	var err error
	for i := 0; i < 50; i++ {
		if _, err = c.Status(context.Background()); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := c.StartRun(context.Background(), &RunRequest{Op: "backup", Job: "db"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("api must be disabled: %v", err)
	}
	cancel()
	<-errc
}

func TestLineRing(t *testing.T) {
	r := newLineRing(3)
	r.Write([]byte("a\nb\nc"))
	r.Write([]byte("d\ne\n"))
	if got := strings.Join(r.lines(), ","); got != "b,cd,e" {
		t.Fatalf("lines: %s", got)
	}
}
