package pipeline

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"cistern/internal/crypt"
)

func write(t *testing.T, plain []byte, o Options) ([]byte, Result) {
	t.Helper()
	var dst bytes.Buffer
	w, err := NewWriter(&dst, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(w, bytes.NewReader(plain)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return dst.Bytes(), w.Result()
}

func TestRoundTrip(t *testing.T) {
	id, _ := crypt.GenerateIdentity()
	plain := bytes.Repeat([]byte("cistern backup row\n"), 20000)
	rnd := make([]byte, 100)
	rand.Read(rnd)
	plain = append(plain, rnd...)

	for _, tc := range []struct {
		name string
		o    Options
	}{
		{"plain", Options{Compression: CompressionNone}},
		{"gzip", Options{Compression: CompressionGzip, Level: 6}},
		{"age", Options{Compression: CompressionNone, Recipients: []*crypt.Recipient{id.Recipient()}}},
		{"gzip+age", Options{Compression: CompressionGzip, Level: 9, Recipients: []*crypt.Recipient{id.Recipient()}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored, res := write(t, plain, tc.o)
			if res.PlainSize != int64(len(plain)) || res.StoredSize != int64(len(stored)) {
				t.Fatalf("sizes: %+v", res)
			}
			r, err := NewReader(bytes.NewReader(stored), ReadOptions{
				Compression: tc.o.Compression, Encryption: tc.o.Encryption(),
				Identities: []*crypt.Identity{id}, Expect: res,
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatal("plaintext mismatch")
			}
			if err := VerifyStored(bytes.NewReader(stored), res); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Bản lưu gzip+age phải giải được bằng công cụ chuẩn: age -d | gunzip.
func TestManualRecovery(t *testing.T) {
	id, _ := crypt.GenerateIdentity()
	plain := []byte("SELECT 1;\n")
	stored, _ := write(t, plain, Options{Compression: CompressionGzip, Level: 6, Recipients: []*crypt.Recipient{id.Recipient()}})
	dec, err := crypt.Decrypt(bytes.NewReader(stored), id)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(gz)
	if !bytes.Equal(got, plain) {
		t.Fatal("mismatch")
	}
}

func TestDetectsCorruption(t *testing.T) {
	plain := bytes.Repeat([]byte("x"), 5000)
	stored, res := write(t, plain, Options{Compression: CompressionNone})

	// Sửa 1 byte: cả VerifyStored và Reader phải báo lỗi.
	bad := append([]byte{}, stored...)
	bad[100] ^= 1
	if err := VerifyStored(bytes.NewReader(bad), res); !errors.Is(err, ErrChecksum) {
		t.Fatalf("VerifyStored: %v", err)
	}
	r, _ := NewReader(bytes.NewReader(bad), ReadOptions{Compression: CompressionNone, Expect: res})
	if _, err := io.ReadAll(r); !errors.Is(err, ErrChecksum) {
		t.Fatalf("Reader: %v", err)
	}

	// Đọc dở rồi Close vẫn phải kiểm checksum.
	r, _ = NewReader(bytes.NewReader(bad), ReadOptions{Compression: CompressionNone, Expect: res})
	r.Read(make([]byte, 10))
	if err := r.Close(); !errors.Is(err, ErrChecksum) {
		t.Fatalf("Close: %v", err)
	}

	// Thừa byte cuối.
	if err := VerifyStored(bytes.NewReader(append(stored, 0)), res); !errors.Is(err, ErrChecksum) {
		t.Fatalf("trailing: %v", err)
	}
}

func TestEncryptedNeedsIdentity(t *testing.T) {
	id, _ := crypt.GenerateIdentity()
	stored, res := write(t, []byte("a"), Options{Compression: CompressionGzip, Level: 1, Recipients: []*crypt.Recipient{id.Recipient()}})
	if _, err := NewReader(bytes.NewReader(stored), ReadOptions{Compression: CompressionGzip, Encryption: EncryptionAge, Expect: res}); err == nil {
		t.Fatal("expected error without identity")
	}
}
