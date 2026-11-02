package repo

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type failingReader struct{ n int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n == 0 {
		return 0, errors.New("boom")
	}
	f.n--
	return copy(p, "data"), nil
}

func TestLocal(t *testing.T) {
	ctx := context.Background()
	l, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Put(ctx, "job/s1/manifest.json", strings.NewReader("{}")); err != nil {
		t.Fatal(err)
	}
	if err := l.Put(ctx, "job/s1/artifacts/db/a.dump.gz", strings.NewReader("aaa")); err != nil {
		t.Fatal(err)
	}
	rc, err := l.Get(ctx, "job/s1/artifacts/db/a.dump.gz")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "aaa" {
		t.Fatalf("got %q", b)
	}

	// Put thất bại giữa chừng: không để lại key hay file tạm.
	if err := l.Put(ctx, "job/s1/artifacts/bad", &failingReader{n: 3}); err == nil {
		t.Fatal("expected error")
	}
	keys, _ := l.List(ctx, "job/")
	if want := []string{"job/s1/artifacts/db/a.dump.gz", "job/s1/manifest.json"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("List = %v", keys)
	}
	entries, _ := os.ReadDir(filepath.Join(l.Root, "job/s1/artifacts"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}

	if _, err := l.Get(ctx, "job/nope"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("Get missing: %v", err)
	}

	// PutIfAbsent.
	if err := l.PutIfAbsent(ctx, "locks/job.lock", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := l.PutIfAbsent(ctx, "locks/job.lock", []byte("2")); !errors.Is(err, ErrExist) {
		t.Fatalf("second PutIfAbsent: %v", err)
	}

	// Delete dọn thư mục rỗng.
	for _, k := range []string{"job/s1/artifacts/db/a.dump.gz", "job/s1/manifest.json", "locks/job.lock"} {
		if err := l.Delete(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Delete(ctx, "job/s1/manifest.json"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if entries, _ := os.ReadDir(l.Root); len(entries) != 0 {
		t.Fatalf("root not empty: %v", entries)
	}
}

func TestLocalRejectsBadKeys(t *testing.T) {
	l, _ := NewLocal(t.TempDir())
	for _, k := range []string{"", "/abs", "../x", "a/../../x", "a//b", "a/", `a\b`} {
		if err := l.Put(context.Background(), k, strings.NewReader("x")); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
}
