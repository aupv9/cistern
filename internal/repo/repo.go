package repo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

var (
	ErrNotExist = errors.New("object does not exist")
	ErrExist    = errors.New("object already exists")
)

type Repository interface {
	Put(ctx context.Context, key string, r io.Reader) error

	PutIfAbsent(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	List(ctx context.Context, prefix string) ([]string, error)

	Delete(ctx context.Context, key string) error
}

func ValidKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") {
		return fmt.Errorf("invalid key %q", key)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "\\\x00") {
			return fmt.Errorf("invalid key %q", key)
		}
	}
	return nil
}
