package driver

import (
	"os"
	"path/filepath"
)

// PrivateDir tạo thư mục tạm quyền 0700 trong $CISTERN_TMPDIR (mặc định thư mục tạm hệ thống)
// cho file chứa bí mật; cleanup xóa toàn bộ. Tiến trình bị kill có thể để lại thư mục này.
func PrivateDir() (dir string, cleanup func(), err error) {
	base := os.Getenv("CISTERN_TMPDIR")
	if base == "" {
		base = os.TempDir()
	}
	dir, err = os.MkdirTemp(base, "cistern-")
	if err != nil {
		return "", nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}

// WriteSecretFile ghi file quyền 0600 trong dir.
func WriteSecretFile(dir, name string, data []byte) (string, error) {
	p := filepath.Join(dir, name)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	return p, f.Close()
}
