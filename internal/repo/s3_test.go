package repo

import (
	"context"
	"strings"
	"testing"
	"time"

	"cistern/internal/s3"
	"cistern/internal/s3/s3test"
)

func newS3(t *testing.T, objectLock bool, mode string) (*S3, *s3test.Server) {
	t.Helper()
	srv := s3test.New(objectLock)
	t.Cleanup(srv.Close)
	r, err := NewS3(S3Config{
		Endpoint: srv.URL, Region: s3test.Region, Bucket: s3test.Bucket, PathStyle: true,
		AccessKeyID: s3test.AccessKey, SecretAccessKey: s3test.SecretKey, PartSizeMB: 5,
		LockMode: mode, LockDays: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, srv
}

func TestS3Contract(t *testing.T) {
	r, _ := newS3(t, false, "")
	testContract(t, r)
	if err := r.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestS3ObjectLock(t *testing.T) {
	ctx := context.Background()
	r, srv := newS3(t, true, s3.LockGovernance)
	if err := r.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Put(ctx, "job/s1/manifest.json", strings.NewReader("{}")); err != nil {
		t.Fatal(err)
	}
	if err := r.PutIfAbsent(ctx, "locks/job.lock", []byte("x")); err != nil {
		t.Fatal(err)
	}
	m, _ := srv.Current("job/s1/manifest.json")
	if m.LockMode != s3.LockGovernance || m.RetainUntil.Sub(time.Now()) < 6*24*time.Hour {
		t.Errorf("manifest lock: %s until %v", m.LockMode, m.RetainUntil)
	}
	if l, _ := srv.Current("locks/job.lock"); l.LockMode != "" {
		t.Error("job lock file must not be locked")
	}

	if err := r.Delete(ctx, "job/s1/manifest.json"); err != nil {
		t.Fatal(err)
	}
	if keys, _ := r.List(ctx, "job/"); len(keys) != 0 {
		t.Fatalf("deleted key still listed: %v", keys)
	}
	if vs := srv.Versions("job/s1/manifest.json"); len(vs) != 2 || string(vs[0].Data) != "{}" {
		t.Fatalf("locked version lost: %+v", vs)
	}

	r.Delete(ctx, "locks/job.lock")
	if err := r.PutIfAbsent(ctx, "locks/job.lock", []byte("y")); err != nil {
		t.Fatalf("re-acquire lock: %v", err)
	}

	bad, _ := newS3(t, false, s3.LockCompliance)
	if err := bad.Check(ctx); err == nil || !strings.Contains(err.Error(), "Object Lock") {
		t.Fatalf("check: %v", err)
	}
}
