// Package repo: giao diện Repository với hai hiện thực local (ghi file tạm rồi rename) và S3.
// Khóa là đường dẫn dạng "a/b/c" (dấu /, không có đoạn rỗng, "." hay "..").
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
	// Put ghi toàn bộ r vào key; nếu r trả lỗi, key không được tạo.
	Put(ctx context.Context, key string, r io.Reader) error
	// PutIfAbsent ghi data nếu key chưa tồn tại, ngược lại trả ErrExist. Dùng cho khóa job.
	PutIfAbsent(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// List trả về mọi key bắt đầu bằng prefix, đã sắp xếp.
	List(ctx context.Context, prefix string) ([]string, error)
	// Delete xóa key; không lỗi nếu key không tồn tại.
	Delete(ctx context.Context, key string) error
}

// ValidKey từ chối khóa có thể thoát khỏi repository.
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
