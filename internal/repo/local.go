package repo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const tmpPrefix = ".cistern-tmp-"

type Local struct{ Root string }

func NewLocal(root string) (*Local, error) {
	if root == "" {
		return nil, errors.New("local repository: empty path")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	return &Local{Root: abs}, nil
}

func (l *Local) path(key string) (string, error) {
	if err := ValidKey(key); err != nil {
		return "", err
	}
	return filepath.Join(l.Root, filepath.FromSlash(key)), nil
}

func (l *Local) writeTemp(ctx context.Context, p string, r io.Reader) (string, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(p), tmpPrefix+"*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	_, err = io.Copy(f, ctxReader{ctx, r})
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

func (l *Local) Put(ctx context.Context, key string, r io.Reader) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	tmp, err := l.writeTemp(ctx, p, r)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(p))
}

func (l *Local) PutIfAbsent(ctx context.Context, key string, data []byte) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	tmp, err := l.writeTemp(ctx, p, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	defer os.Remove(tmp)

	if err := os.Link(tmp, p); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s: %w", key, ErrExist)
		}
		return err
	}
	return syncDir(filepath.Dir(p))
}

func (l *Local) Get(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", key, ErrNotExist)
	}
	return f, err
}

func (l *Local) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	err := filepath.WalkDir(l.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), tmpPrefix) {
			return nil
		}
		rel, err := filepath.Rel(l.Root, p)
		if err != nil {
			return err
		}
		if key := filepath.ToSlash(rel); strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	sort.Strings(keys)
	return keys, err
}

func (l *Local) Delete(_ context.Context, key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	for dir := filepath.Dir(p); dir != l.Root && strings.HasPrefix(dir, l.Root); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			break
		}
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	_ = d.Sync()
	return nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
