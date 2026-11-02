// Package driver định nghĩa hợp đồng giữa engine và từng loại hệ thống.
// Driver không biết gì về nén, mã hóa hay nơi lưu; nó chỉ ghi vào Sink và đọc từ Source.
package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
)

// ErrNotImplemented được trả về bởi các driver/chức năng chưa hiện thực.
var ErrNotImplemented = errors.New("not implemented")

type Driver interface {
	Name() string
	Check(ctx context.Context) error                     // công cụ có sẵn, nguồn kết nối được
	Backup(ctx context.Context, sink Sink) (Info, error) // ghi một hoặc nhiều artifact
	Restore(ctx context.Context, src Source) error
}

type Sink interface {
	Create(name string) (io.WriteCloser, error) // engine nén, mã hóa, băm, upload khi driver ghi
	Previous() Source                           // snapshot gần nhất, cho incremental; có thể nil
}

type Source interface {
	List() []string
	// Open trả về dữ liệu gốc của artifact. Lỗi checksum trả về khi đọc hết stream hoặc khi Close
	// (Close đọc nốt phần còn lại để kiểm); driver phải kiểm lỗi của Close.
	Open(name string) (io.ReadCloser, error)
}

// Snapshotted: Source biết ID snapshot của nó (engine luôn cung cấp). Driver incremental dùng ID này
// để trỏ tới dữ liệu đã có trong snapshot trước.
type Snapshotted interface {
	SnapshotID() string
}

// Resolver: Source mở được snapshot khác mà snapshot hiện tại phụ thuộc (Info.DependsOn).
type Resolver interface {
	Snapshot(id string) (Source, error)
}

// Aborter: reader có thể đóng ngay mà không đọc nốt để kiểm checksum (dùng khi đã lỗi).
type Aborter interface{ Abort() }

// Abort đóng rc trên đường lỗi, không tải phần còn lại chỉ để kiểm checksum.
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
	DependsOn     []string // snapshot cần giữ để restore được bản này
}

// Forgetter là tùy chọn: driver có dữ liệu nằm ngoài repository (Elasticsearch).
type Forgetter interface {
	Forget(ctx context.Context, info Info) error
}

// Params là phần `params` của một job trong cấu hình.
type Params map[string]string

type Factory func(p Params) (Driver, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register gọi trong init() của driver.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[name]; dup {
		panic("driver: duplicate registration " + name)
	}
	registry[name] = f
}

// New tạo driver theo tên đã đăng ký.
func New(name string, p Params) (Driver, error) {
	mu.RLock()
	f, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("driver: unknown driver %q", name)
	}
	return f(p)
}

// Names trả về danh sách driver đã đăng ký, đã sắp xếp.
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
