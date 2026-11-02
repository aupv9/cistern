package s3_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"cistern/internal/s3"
	"cistern/internal/s3/s3test"
)

func newClient(t *testing.T, objectLock bool) (*s3.Client, *s3test.Server) {
	t.Helper()
	srv := s3test.New(objectLock)
	t.Cleanup(srv.Close)
	c, err := s3.New(s3.Config{
		Endpoint: srv.URL, Region: s3test.Region, Bucket: s3test.Bucket, PathStyle: true,
		Credentials: s3test.Creds, PartSize: s3.MinPartSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	s3.SetBackoff(c, func(int) time.Duration { return time.Millisecond })
	return c, srv
}

func random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func TestPutGetListDelete(t *testing.T) {
	ctx := context.Background()
	c, srv := newClient(t, false)
	srv.MaxKeys = 2 // ép phân trang

	keys := []string{"p/job/a", "p/job/b@x=y+z", "p/job/c d", "p/locks/job.lock", "q/other"}
	for i, k := range keys {
		if err := c.PutObject(ctx, k, []byte{byte(i)}, s3.PutOptions{}); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	got, err := c.ListObjects(ctx, "p/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "p/job/a,p/job/b@x=y+z,p/job/c d,p/locks/job.lock" {
		t.Fatalf("list: %v", got)
	}
	rc, err := c.GetObject(ctx, "p/job/b@x=y+z")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(b, []byte{1}) {
		t.Fatalf("get: %v", b)
	}
	if err := c.DeleteObject(ctx, "p/job/a"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteObject(ctx, "p/job/a"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if _, err := c.GetObject(ctx, "p/job/a"); !errors.Is(err, s3.ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	if err := c.PutObject(ctx, "p/empty", nil, s3.PutOptions{}); err != nil {
		t.Fatalf("empty object: %v", err)
	}
}

func TestUploadMultipart(t *testing.T) {
	ctx := context.Background()
	c, srv := newClient(t, false)
	for _, size := range []int{0, 100, s3.MinPartSize - 1, s3.MinPartSize, s3.MinPartSize + 1, 2*s3.MinPartSize + 12345} {
		data := random(size)
		if err := c.Upload(ctx, "k", bytes.NewReader(data), s3.PutOptions{}); err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if !bytes.Equal(srv.Bytes("k"), data) {
			t.Fatalf("size %d: content mismatch", size)
		}
		v, _ := srv.Current("k")
		multipart := strings.Contains(v.ETag, "-")
		if multipart != (size >= s3.MinPartSize) {
			t.Errorf("size %d: multipart=%v (etag %s)", size, multipart, v.ETag)
		}
	}
}

type failAfter struct {
	r io.Reader
	n int
}

func (f *failAfter) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("driver crashed")
	}
	if len(p) > f.n {
		p = p[:f.n]
	}
	n, err := f.r.Read(p)
	f.n -= n
	return n, err
}

func TestUploadAbortsOnReaderError(t *testing.T) {
	ctx := context.Background()
	c, srv := newClient(t, false)
	err := c.Upload(ctx, "k", &failAfter{r: bytes.NewReader(random(3 * s3.MinPartSize)), n: 2*s3.MinPartSize + 10}, s3.PutOptions{})
	if err == nil || !strings.Contains(err.Error(), "driver crashed") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := srv.Current("k"); ok || srv.OpenUploads() != 0 {
		t.Fatalf("object created or upload not aborted (open uploads: %d)", srv.OpenUploads())
	}
	// Lỗi trước khi đủ một part: không có yêu cầu ghi nào.
	before := len(srv.Requests())
	c.Upload(ctx, "k2", &failAfter{r: bytes.NewReader(random(100)), n: 50}, s3.PutOptions{})
	if len(srv.Requests()) != before {
		t.Fatalf("requests sent for failed small upload: %v", srv.Requests()[before:])
	}
}

func TestCompleteReturns200WithError(t *testing.T) {
	c, srv := newClient(t, false)
	srv.CompleteError = true
	err := c.Upload(context.Background(), "k", bytes.NewReader(random(s3.MinPartSize+1)), s3.PutOptions{})
	if err == nil || !strings.Contains(err.Error(), "InternalError") {
		t.Fatalf("err = %v", err)
	}
	if srv.OpenUploads() != 0 {
		t.Fatal("upload not aborted")
	}
}

func TestRetries(t *testing.T) {
	ctx := context.Background()
	c, srv := newClient(t, false)
	srv.Inject(s3test.Fault{Method: "PUT", Query: "partNumber", Status: 503})
	srv.Inject(s3test.Fault{Method: "PUT", Query: "partNumber", Status: 500})
	srv.Inject(s3test.Fault{Method: "GET", Status: 500})
	data := random(s3.MinPartSize + 10)
	if err := c.Upload(ctx, "k", bytes.NewReader(data), s3.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(srv.Bytes("k"), data) {
		t.Fatal("content mismatch after retries")
	}
	if _, err := c.ListObjects(ctx, ""); err != nil {
		t.Fatalf("list after retry: %v", err)
	}
	// 4xx không thử lại.
	srv.Inject(s3test.Fault{Method: "PUT", Status: 403})
	n := len(srv.Requests())
	if err := c.PutObject(ctx, "x", []byte("1"), s3.PutOptions{}); err == nil {
		t.Fatal("expected 403")
	}
	if len(srv.Requests())-n != 1 {
		t.Fatalf("4xx retried: %d requests", len(srv.Requests())-n)
	}
}

func TestConditionalPut(t *testing.T) {
	ctx := context.Background()
	c, srv := newClient(t, false)
	o := s3.PutOptions{IfNoneMatch: true}
	if err := c.PutObject(ctx, "lock", []byte("me"), o); err != nil {
		t.Fatal(err)
	}
	if err := c.PutObject(ctx, "lock", []byte("other"), o); !errors.Is(err, s3.ErrPreconditionFailed) {
		t.Fatalf("second put: %v", err)
	}
	// Phản hồi đầu bị mất (server đã ghi), lần thử lại nhận 412 nhưng nội dung là của mình → thành công.
	srv.Inject(s3test.Fault{Method: "PUT", Status: 500, AfterProcessing: true})
	if err := c.PutObject(ctx, "lock2", []byte("mine"), o); err != nil {
		t.Fatalf("lost response: %v", err)
	}
	if string(srv.Bytes("lock2")) != "mine" {
		t.Fatal("lock2 content")
	}
}

func TestObjectLock(t *testing.T) {
	ctx := context.Background()
	c, srv := newClient(t, true)
	if ok, err := c.ObjectLockEnabled(ctx); err != nil || !ok {
		t.Fatalf("ObjectLockEnabled = %v %v", ok, err)
	}
	until := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	ret := &s3.Retention{Mode: s3.LockCompliance, Until: until}
	if err := c.Upload(ctx, "small", bytes.NewReader([]byte("x")), s3.PutOptions{Retention: ret}); err != nil {
		t.Fatal(err)
	}
	if err := c.Upload(ctx, "big", bytes.NewReader(random(s3.MinPartSize+1)), s3.PutOptions{Retention: ret}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"small", "big"} {
		v, _ := srv.Current(k)
		if v.LockMode != s3.LockCompliance || !v.RetainUntil.Equal(until) {
			t.Errorf("%s: lock %s until %v", k, v.LockMode, v.RetainUntil)
		}
	}
	// DELETE chỉ tạo delete marker; bản bị khóa vẫn còn.
	if err := c.DeleteObject(ctx, "small"); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.Current("small"); ok {
		t.Fatal("still current after delete")
	}
	vs := srv.Versions("small")
	if len(vs) != 2 || !vs[1].DeleteMarker || string(vs[0].Data) != "x" {
		t.Fatalf("versions: %+v", vs)
	}

	plain, _ := newClient(t, false)
	if ok, err := plain.ObjectLockEnabled(ctx); err != nil || ok {
		t.Fatalf("plain bucket: %v %v", ok, err)
	}
	if err := plain.PutObject(ctx, "k", []byte("x"), s3.PutOptions{Retention: ret}); err == nil {
		t.Fatal("retention accepted on bucket without Object Lock")
	}
}

func TestBadCredentialsNotRetried(t *testing.T) {
	srv := s3test.New(false)
	defer srv.Close()
	c, _ := s3.New(s3.Config{Endpoint: srv.URL, Bucket: s3test.Bucket, PathStyle: true,
		Credentials: s3.Credentials{AccessKeyID: s3test.AccessKey, SecretAccessKey: "wrong"}, PartSize: s3.MinPartSize})
	err := c.PutObject(context.Background(), "k", []byte("x"), s3.PutOptions{})
	if err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") || len(srv.Requests()) != 1 {
		t.Fatalf("err=%v requests=%d", err, len(srv.Requests()))
	}
}
