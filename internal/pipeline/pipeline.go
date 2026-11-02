// Package pipeline: chiều ghi SHA-256(plain) → gzip → age → SHA-256(stored);
// chiều đọc kiểm lại cả hai checksum. Dạng stream, không giữ cả artifact trong bộ nhớ.
package pipeline

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"

	"cistern/internal/crypt"
)

const (
	CompressionGzip = "gzip"
	CompressionNone = "none"
	EncryptionAge   = "age-x25519"
	EncryptionNone  = "none"
)

// ErrChecksum được trả về khi dữ liệu đọc ra không khớp manifest.
var ErrChecksum = errors.New("checksum mismatch")

type Options struct {
	Compression string // gzip | none
	Level       int    // gzip 1–9
	Recipients  []*crypt.Recipient
}

func (o Options) Encryption() string {
	if len(o.Recipients) > 0 {
		return EncryptionAge
	}
	return EncryptionNone
}

type Result struct {
	PlainSize    int64
	StoredSize   int64
	PlainSHA256  string
	StoredSHA256 string
}

type counter struct {
	w io.Writer
	h hash.Hash
	n int64
}

func newCounter(w io.Writer) *counter { return &counter{w: w, h: sha256.New()} }

func (c *counter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.h.Write(p[:n])
	c.n += int64(n)
	return n, err
}

func (c *counter) sum() string { return hex.EncodeToString(c.h.Sum(nil)) }

// Writer nhận dữ liệu gốc từ driver và ghi dữ liệu đã xử lý vào dst.
type Writer struct {
	plain, stored *counter
	gz            *gzip.Writer
	enc           io.WriteCloser
	closed        bool
	err           error
}

func NewWriter(dst io.Writer, o Options) (*Writer, error) {
	w := &Writer{stored: newCounter(dst)}
	var next io.Writer = w.stored
	if len(o.Recipients) > 0 {
		enc, err := crypt.Encrypt(next, o.Recipients...)
		if err != nil {
			return nil, err
		}
		w.enc, next = enc, enc
	}
	switch o.Compression {
	case CompressionGzip:
		gz, err := gzip.NewWriterLevel(next, o.Level)
		if err != nil {
			return nil, err
		}
		w.gz, next = gz, gz
	case CompressionNone, "":
	default:
		return nil, fmt.Errorf("unknown compression %q", o.Compression)
	}
	w.plain = newCounter(next)
	return w, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("pipeline: write after close")
	}
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.plain.Write(p)
	if err != nil {
		w.err = err
	}
	return n, err
}

// Close đẩy phần còn lại của gzip và khối cuối của age; không đóng dst.
func (w *Writer) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err != nil {
		return w.err
	}
	if w.gz != nil {
		if err := w.gz.Close(); err != nil {
			w.err = err
			return err
		}
	}
	if w.enc != nil {
		if err := w.enc.Close(); err != nil {
			w.err = err
			return err
		}
	}
	return nil
}

// Result chỉ có nghĩa sau khi Close thành công.
func (w *Writer) Result() Result {
	return Result{
		PlainSize:    w.plain.n,
		StoredSize:   w.stored.n,
		PlainSHA256:  w.plain.sum(),
		StoredSHA256: w.stored.sum(),
	}
}

type ReadOptions struct {
	Compression string
	Encryption  string
	Identities  []*crypt.Identity
	Expect      Result
}

type hashReader struct {
	r io.Reader
	h hash.Hash
	n int64
}

func (h *hashReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.h.Write(p[:n])
	h.n += int64(n)
	return n, err
}

func (h *hashReader) sum() string { return hex.EncodeToString(h.h.Sum(nil)) }

// Reader trả dữ liệu gốc; lỗi checksum được trả thay cho io.EOF khi đọc hết stream.
type Reader struct {
	src           io.Reader
	stored, plain *hashReader
	gz            *gzip.Reader
	expect        Result
	done          bool
	err           error
}

func NewReader(src io.Reader, o ReadOptions) (*Reader, error) {
	r := &Reader{src: src, expect: o.Expect}
	r.stored = &hashReader{r: src, h: sha256.New()}
	var next io.Reader = r.stored
	switch o.Encryption {
	case EncryptionAge:
		if len(o.Identities) == 0 {
			return nil, errors.New("artifact is encrypted: private key (identity_file) required")
		}
		dec, err := crypt.Decrypt(next, o.Identities...)
		if err != nil {
			return nil, err
		}
		next = dec
	case EncryptionNone, "":
	default:
		return nil, fmt.Errorf("unknown encryption %q", o.Encryption)
	}
	switch o.Compression {
	case CompressionGzip:
		gz, err := gzip.NewReader(next)
		if err != nil {
			return nil, err
		}
		r.gz, next = gz, gz
	case CompressionNone, "":
	default:
		return nil, fmt.Errorf("unknown compression %q", o.Compression)
	}
	r.plain = &hashReader{r: next, h: sha256.New()}
	return r, nil
}

func (r *Reader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.done {
		return 0, io.EOF
	}
	n, err := r.plain.Read(p)
	if err == io.EOF {
		r.done = true
		if verr := r.verify(); verr != nil {
			r.err = verr
			return n, verr
		}
	} else if err != nil {
		r.err = err
	}
	return n, err
}

func (r *Reader) verify() error {
	// Phần dư sau cuối stream gzip/age (nếu có) vẫn phải tính vào checksum lưu trữ.
	if _, err := io.Copy(io.Discard, r.stored); err != nil {
		return err
	}
	e := r.expect
	if got := r.stored.sum(); got != e.StoredSHA256 || r.stored.n != e.StoredSize {
		return fmt.Errorf("%w: stored sha256 %s (%d bytes), manifest %s (%d bytes)", ErrChecksum, got, r.stored.n, e.StoredSHA256, e.StoredSize)
	}
	if got := r.plain.sum(); got != e.PlainSHA256 || r.plain.n != e.PlainSize {
		return fmt.Errorf("%w: plain sha256 %s (%d bytes), manifest %s (%d bytes)", ErrChecksum, got, r.plain.n, e.PlainSHA256, e.PlainSize)
	}
	return nil
}

// Close đọc nốt phần còn lại để kiểm checksum nếu người dùng dừng giữa chừng.
func (r *Reader) Close() error {
	if !r.done && r.err == nil {
		if _, err := io.Copy(io.Discard, r); err != nil {
			return err
		}
	}
	if r.gz != nil {
		r.gz.Close()
	}
	return r.err
}

// VerifyStored chỉ kiểm kích thước và SHA-256 của bản lưu (khi không có private key).
func VerifyStored(src io.Reader, e Result) error {
	h := &hashReader{r: src, h: sha256.New()}
	if _, err := io.Copy(io.Discard, h); err != nil {
		return err
	}
	if got := h.sum(); got != e.StoredSHA256 || h.n != e.StoredSize {
		return fmt.Errorf("%w: stored sha256 %s (%d bytes), manifest %s (%d bytes)", ErrChecksum, got, h.n, e.StoredSHA256, e.StoredSize)
	}
	return nil
}
