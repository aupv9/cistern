package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cistern/internal/admin"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestInternalAPI(t *testing.T) {
	metricsAddr, addr := freePort(t), freePort(t)
	release := make(chan struct{})
	opt := Options{
		Listen: metricsAddr, APIListen: addr, Metrics: http.NotFoundHandler(), Grace: 5 * time.Second, Log: quiet(), Location: time.UTC, APIToken: testToken,
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
	c, _ = NewClient("http://"+addr, testToken, nil)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := c.Status(context.Background()); err == nil {
			break
		}
	}

	resp, err := http.Post("http://"+addr+"/internal/v1/runs", "application/json", strings.NewReader(`{"op":"backup","job":"db"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	if code := get(t, "http://"+addr+"/status", ""); code != http.StatusUnauthorized {
		t.Fatalf("status on the API port without a token: %d", code)
	}
	if code := get(t, "http://"+addr+"/healthz", ""); code != http.StatusOK {
		t.Fatalf("healthz on the API port: %d", code)
	}
	for _, p := range []string{"/internal/v1/runs", "/internal/v1/runs/20260101T000000Z-abcdef01"} {
		if code := get(t, "http://"+metricsAddr+p, testToken); code != http.StatusNotFound {
			t.Fatalf("%s on the metrics port: %d, want 404", p, code)
		}
	}
	if code := get(t, "http://"+metricsAddr+"/status", ""); code != http.StatusOK {
		t.Fatalf("status on the metrics port: %d", code)
	}
	onMetrics, _ := NewClient("http://"+metricsAddr, testToken, nil)
	if _, err := onMetrics.StartRun(context.Background(), &RunRequest{Op: "backup", Job: "db"}); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "api_listen") {
		t.Fatalf("run accepted on the metrics port: %v", err)
	}
	if _, err := onMetrics.Status(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a client pointed at the metrics port must see the daemon as unavailable: %v", err)
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
	if _, err := c.StartRun(context.Background(), &RunRequest{Op: "compact", Job: "db"}); !errors.As(err, &ae) || ae.Status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown op must be rejected: %v", err)
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

func get(t *testing.T, url, token string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
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
	addr, apiAddr := freePort(t), freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- Run(ctx, Options{Listen: addr, APIListen: apiAddr, Log: quiet(), Execute: func(context.Context, *RunRequest) (Result, error) { return Result{}, nil }})
	}()
	c, _ := NewClient("http://"+addr, testToken, nil)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if resp, err := http.Get("http://" + addr + "/healthz"); err == nil {
			resp.Body.Close()
			break
		}
	}
	if _, err := c.Status(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("status without the API: %v", err)
	}
	if _, err := c.StartRun(context.Background(), &RunRequest{Op: "backup", Job: "db"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("api must be disabled: %v", err)
	}
	if conn, err := net.Dial("tcp", apiAddr); err == nil {
		conn.Close()
		t.Fatal("the API port listens without a token")
	}
	cancel()
	<-errc
}

func TestAPIListenMustDiffer(t *testing.T) {
	addr := freePort(t)
	err := Run(context.Background(), Options{Listen: addr, APIListen: addr, APIToken: testToken, Log: quiet(),
		Execute: func(context.Context, *RunRequest) (Result, error) { return Result{}, nil }})
	if err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("same listener accepted: %v", err)
	}
}

func TestInternalAPITLS(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeSelfSigned(t, dir)
	addr, apiAddr := freePort(t), freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- Run(ctx, Options{Listen: addr, APIListen: apiAddr, APIToken: testToken, APICertFile: certFile, APIKeyFile: keyFile, Log: quiet(), Location: time.UTC,
			Execute: func(context.Context, *RunRequest) (Result, error) { return Result{}, nil }})
	}()
	hc, err := HTTPClient(certFile)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := NewClient("https://"+apiAddr, testToken, hc)
	var st *Status
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if st, err = c.Status(context.Background()); err == nil {
			break
		}
	}
	if err != nil || !st.API {
		t.Fatalf("status over TLS: %+v %v", st, err)
	}
	untrusted, _ := NewClient("https://"+apiAddr, testToken, nil)
	if _, err := untrusted.Status(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("untrusted certificate accepted: %v", err)
	}
	plain, _ := NewClient("http://"+apiAddr, testToken, nil)
	if _, err := plain.Status(context.Background()); err == nil {
		t.Fatal("plain HTTP accepted on the TLS listener")
	}
	if _, err := HTTPClient(filepath.Join(dir, "missing.pem")); err == nil {
		t.Fatal("missing ca_file accepted")
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func writeSelfSigned(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "cistern"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600)
	return certFile, keyFile
}

func TestLineRing(t *testing.T) {
	r := newLineRing(3)
	r.Write([]byte("a\nb\nc"))
	r.Write([]byte("d\ne\n"))
	if got := strings.Join(r.lines(), ","); got != "b,cd,e" {
		t.Fatalf("lines: %s", got)
	}
}
