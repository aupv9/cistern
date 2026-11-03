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

func testContract(t *testing.T, r Repository) {
	ctx := context.Background()
	if err := r.Put(ctx, "job/s1/manifest.json", strings.NewReader("{}")); err != nil {
		t.Fatal(err)
	}
	if err := r.Put(ctx, "job/s1/artifacts/db/a.dump.gz", strings.NewReader("aaa")); err != nil {
		t.Fatal(err)
	}
	rc, err := r.Get(ctx, "job/s1/artifacts/db/a.dump.gz")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "aaa" {
		t.Fatalf("got %q", b)
	}

	if err := r.Put(ctx, "job/s1/artifacts/bad", &failingReader{n: 3}); err == nil {
		t.Fatal("expected error")
	}
	keys, _ := r.List(ctx, "job/")
	if want := []string{"job/s1/artifacts/db/a.dump.gz", "job/s1/manifest.json"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("List = %v", keys)
	}
	if _, err := r.Get(ctx, "job/nope"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("Get missing: %v", err)
	}

	if err := r.PutIfAbsent(ctx, "locks/job.lock", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := r.PutIfAbsent(ctx, "locks/job.lock", []byte("2")); !errors.Is(err, ErrExist) {
		t.Fatalf("second PutIfAbsent: %v", err)
	}
	if err := r.Delete(ctx, "locks/job.lock"); err != nil {
		t.Fatal(err)
	}
	if err := r.PutIfAbsent(ctx, "locks/job.lock", []byte("3")); err != nil {
		t.Fatalf("PutIfAbsent after delete: %v", err)
	}

	for _, k := range []string{"job/s1/artifacts/db/a.dump.gz", "job/s1/manifest.json", "locks/job.lock"} {
		if err := r.Delete(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Delete(ctx, "job/s1/manifest.json"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if keys, _ := r.List(ctx, ""); len(keys) != 0 {
		t.Fatalf("keys left: %v", keys)
	}
	for _, k := range []string{"", "/abs", "../x", "a/../../x", "a//b", "a/", `a\b`} {
		if err := r.Put(ctx, k, strings.NewReader("x")); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
}

func TestLocal(t *testing.T) {
	l, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	testContract(t, l)

	if entries, _ := os.ReadDir(l.Root); len(entries) != 0 {
		t.Fatalf("root not empty: %v", entries)
	}
	if matches, _ := filepath.Glob(filepath.Join(l.Root, "*", tmpPrefix+"*")); len(matches) != 0 {
		t.Fatalf("temp files left: %v", matches)
	}
}
