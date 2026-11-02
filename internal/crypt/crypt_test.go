package crypt

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBech32Vectors(t *testing.T) {
	// BIP-173 vectors hợp lệ.
	for _, s := range []string{"A12UEL5L", "abcdef1qpzry9x8gf2tvdw0s3jn54khce6mua7lmqqqxw"} {
		if _, _, err := bech32Decode(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"A12UEL5l", "a12uel5m", "1nwldj5"} {
		if _, _, err := bech32Decode(s); err == nil {
			t.Errorf("%s accepted", s)
		}
	}
}

func TestKeyRoundTrip(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id.String(), "AGE-SECRET-KEY-1") || !strings.HasPrefix(id.Recipient().String(), "age1") {
		t.Fatalf("unexpected encoding %s / %s", id, id.Recipient())
	}
	id2, err := ParseIdentity(id.String())
	if err != nil || id2.String() != id.String() {
		t.Fatalf("identity round trip: %v", err)
	}
	r2, err := ParseRecipient(id.Recipient().String())
	if err != nil || r2.String() != id.Recipient().String() {
		t.Fatalf("recipient round trip: %v", err)
	}
	if _, err := ParseRecipient(id.String()); err == nil {
		t.Fatal("secret key accepted as recipient")
	}

	var file bytes.Buffer
	if err := WriteIdentityFile(&file, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	ids, err := ParseIdentities(&file)
	if err != nil || len(ids) != 1 || ids[0].String() != id.String() {
		t.Fatalf("ParseIdentities: %v", err)
	}
}

func encrypt(t *testing.T, plain []byte, rs ...*Recipient) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := Encrypt(&buf, rs...)
	if err != nil {
		t.Fatal(err)
	}
	// Ghi lẻ từng mẩu để thử ranh giới khối.
	for p := plain; len(p) > 0; {
		n := min(len(p), 7777)
		if _, err := w.Write(p[:n]); err != nil {
			t.Fatal(err)
		}
		p = p[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decrypt(enc []byte, ids ...*Identity) ([]byte, error) {
	r, err := Decrypt(bytes.NewReader(enc), ids...)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestRoundTripSizes(t *testing.T) {
	id, _ := GenerateIdentity()
	for _, size := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3 * chunkSize, 3*chunkSize + 123} {
		plain := make([]byte, size)
		rand.Read(plain)
		enc := encrypt(t, plain, id.Recipient())
		got, err := decrypt(enc, id)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d: mismatch", size)
		}
	}
}

func TestMultipleRecipients(t *testing.T) {
	a, _ := GenerateIdentity()
	b, _ := GenerateIdentity()
	c, _ := GenerateIdentity()
	enc := encrypt(t, []byte("hello"), a.Recipient(), b.Recipient())
	if got, err := decrypt(enc, c, b); err != nil || string(got) != "hello" {
		t.Fatalf("b: %q %v", got, err)
	}
	if _, err := decrypt(enc, c); !errors.Is(err, ErrNoIdentityMatch) {
		t.Fatalf("c: %v", err)
	}
}

func TestTamperAndTruncate(t *testing.T) {
	id, _ := GenerateIdentity()
	plain := make([]byte, 2*chunkSize+10)
	rand.Read(plain)
	enc := encrypt(t, plain, id.Recipient())

	hdrEnd := bytes.Index(enc, []byte("\n--- ")) + 1
	hdrEnd += bytes.IndexByte(enc[hdrEnd:], '\n') + 1

	cases := map[string][]byte{
		"flip payload byte": flip(enc, len(enc)-100),
		"flip header byte":  flip(enc, 30),
		"flip nonce byte":   flip(enc, hdrEnd+3),
		"drop final chunk":  enc[:hdrEnd+streamNonce+2*encChunkSize],
		"cut mid chunk":     enc[:len(enc)-5],
		"trailing garbage":  append(append([]byte{}, enc...), 0),
		"header only":       enc[:hdrEnd],
		"no payload":        enc[:hdrEnd+streamNonce],
	}
	for name, data := range cases {
		if got, err := decrypt(data, id); err == nil {
			t.Errorf("%s: decrypted %d bytes without error", name, len(got))
		}
	}
}

func flip(b []byte, i int) []byte {
	c := append([]byte{}, b...)
	c[i] ^= 0x01
	return c
}

// TestAgeCLIInterop chạy khi có `age` trong PATH: hai chiều, Cistern ↔ age.
func TestAgeCLIInterop(t *testing.T) {
	ageBin, err := exec.LookPath("age")
	if err != nil {
		t.Skip("age CLI not installed")
	}
	dir := t.TempDir()
	id, _ := GenerateIdentity()
	idFile := filepath.Join(dir, "key.txt")
	f, _ := os.Create(idFile)
	WriteIdentityFile(f, id, time.Now())
	f.Close()

	plain := make([]byte, 2*chunkSize+999)
	rand.Read(plain)

	// Cistern mã hóa → age giải mã.
	enc := filepath.Join(dir, "u.age")
	os.WriteFile(enc, encrypt(t, plain, id.Recipient()), 0o600)
	out, err := exec.Command(ageBin, "-d", "-i", idFile, enc).Output()
	if err != nil || !bytes.Equal(out, plain) {
		t.Fatalf("age -d failed: %v", err)
	}

	// age mã hóa → Cistern giải mã.
	cmd := exec.Command(ageBin, "-r", id.Recipient().String())
	cmd.Stdin = bytes.NewReader(plain)
	ageEnc, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got, err := decrypt(ageEnc, id)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("decrypt age output: %v", err)
	}
}
