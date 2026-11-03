package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
)

var ErrNotImplemented = errors.New("not implemented")

type Driver interface {
	Name() string
	Check(ctx context.Context) error
	Backup(ctx context.Context, sink Sink) (Info, error)
	Restore(ctx context.Context, src Source) error
}

type Sink interface {
	Create(name string) (io.WriteCloser, error)
	Previous() Source
}

type Source interface {
	List() []string

	Open(name string) (io.ReadCloser, error)
}

type Snapshotted interface {
	SnapshotID() string
}

type Resolver interface {
	Snapshot(id string) (Source, error)
}

type Aborter interface{ Abort() }

func Abort(rc io.ReadCloser) {
	if a, ok := rc.(Aborter); ok {
		a.Abort()
		return
	}
	rc.Close()
}

type Info struct {
	Version, Host string
	Extra         map[string]string
	DependsOn     []string
}

type Forgetter interface {
	Forget(ctx context.Context, info Info) error
}

type Params map[string]string

type Factory func(p Params) (Driver, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[name]; dup {
		panic("driver: duplicate registration " + name)
	}
	registry[name] = f
}

func New(name string, p Params) (Driver, error) {
	mu.RLock()
	f, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("driver: unknown driver %q", name)
	}
	return f(p)
}

func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
