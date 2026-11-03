package crypt

import (
	"bufio"
	"bytes"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"cistern/internal/thirdparty/xcrypto/chacha20poly1305"
)

const (
	intro        = "age-encryption.org/v1\n"
	x25519Label  = "age-encryption.org/v1/X25519"
	fileKeySize  = 16
	streamNonce  = 16
	chunkSize    = 64 * 1024
	encChunkSize = chunkSize + chacha20poly1305.Overhead
	bodyColumns  = 64
)

var (
	b64 = base64.RawStdEncoding.Strict()

	ErrNoIdentityMatch = errors.New("age: no identity matched any of the recipients")
	ErrHeaderMAC       = errors.New("age: bad header MAC")
	ErrTruncated       = errors.New("age: payload truncated")
)

type stanza struct {
	typ  string
	args []string
	body []byte
}

func Encrypt(dst io.Writer, recipients ...*Recipient) (io.WriteCloser, error) {
	if len(recipients) == 0 {
		return nil, errors.New("age: no recipients")
	}
	fileKey := make([]byte, fileKeySize)
	if _, err := rand.Read(fileKey); err != nil {
		return nil, err
	}
	var hdr bytes.Buffer
	hdr.WriteString(intro)
	for _, r := range recipients {
		s, err := wrapX25519(r, fileKey)
		if err != nil {
			return nil, err
		}
		writeStanza(&hdr, s)
	}
	hdr.WriteString("---")
	mac, err := headerMAC(fileKey, hdr.Bytes())
	if err != nil {
		return nil, err
	}
	hdr.WriteString(" " + b64.EncodeToString(mac) + "\n")

	nonce := make([]byte, streamNonce)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	hdr.Write(nonce)
	if _, err := dst.Write(hdr.Bytes()); err != nil {
		return nil, err
	}
	aead, err := payloadAEAD(fileKey, nonce)
	if err != nil {
		return nil, err
	}
	return &streamWriter{dst: dst, aead: aead, buf: make([]byte, 0, chunkSize)}, nil
}

func wrapX25519(r *Recipient, fileKey []byte) (*stanza, error) {
	eph, err := r.pub.Curve().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(r.pub)
	if err != nil {
		return nil, err
	}
	ephPub := eph.PublicKey().Bytes()
	salt := append(append([]byte{}, ephPub...), r.pub.Bytes()...)
	key, err := hkdf.Key(sha256.New, shared, salt, x25519Label, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	body := aead.Seal(nil, make([]byte, chacha20poly1305.NonceSize), fileKey, nil)
	return &stanza{typ: "X25519", args: []string{b64.EncodeToString(ephPub)}, body: body}, nil
}

func writeStanza(w *bytes.Buffer, s *stanza) {
	w.WriteString("-> " + s.typ)
	for _, a := range s.args {
		w.WriteString(" " + a)
	}
	w.WriteString("\n")
	enc := b64.EncodeToString(s.body)
	for len(enc) >= bodyColumns {
		w.WriteString(enc[:bodyColumns] + "\n")
		enc = enc[bodyColumns:]
	}
	w.WriteString(enc + "\n")
}

func headerMAC(fileKey, header []byte) ([]byte, error) {
	key, err := hkdf.Key(sha256.New, fileKey, nil, "header", 32)
	if err != nil {
		return nil, err
	}
	h := hmac.New(sha256.New, key)
	h.Write(header)
	return h.Sum(nil), nil
}

type aeadCipher interface {
	Seal(dst, nonce, plaintext, ad []byte) []byte
	Open(dst, nonce, ciphertext, ad []byte) ([]byte, error)
}

func payloadAEAD(fileKey, nonce []byte) (aeadCipher, error) {
	key, err := hkdf.Key(sha256.New, fileKey, nonce, "payload", chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	return chacha20poly1305.New(key)
}

func chunkNonce(counter uint64, last bool) []byte {
	n := make([]byte, chacha20poly1305.NonceSize)
	for i := 10; i >= 3; i-- {
		n[i] = byte(counter)
		counter >>= 8
	}
	if last {
		n[11] = 1
	}
	return n
}

type streamWriter struct {
	dst     io.Writer
	aead    aeadCipher
	buf     []byte
	counter uint64
	closed  bool
	err     error
}

func (w *streamWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("age: write after close")
	}
	if w.err != nil {
		return 0, w.err
	}
	total := len(p)
	for len(p) > 0 {

		if len(w.buf) == chunkSize {
			if err := w.flush(false); err != nil {
				return total - len(p), err
			}
		}
		n := copy(w.buf[len(w.buf):chunkSize], p)
		w.buf = w.buf[:len(w.buf)+n]
		p = p[n:]
	}
	return total, nil
}

func (w *streamWriter) flush(last bool) error {
	out := w.aead.Seal(nil, chunkNonce(w.counter, last), w.buf, nil)
	w.counter++
	w.buf = w.buf[:0]
	if _, err := w.dst.Write(out); err != nil {
		w.err = err
		return err
	}
	return nil
}

func (w *streamWriter) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err != nil {
		return w.err
	}
	return w.flush(true)
}

func Decrypt(src io.Reader, identities ...*Identity) (io.Reader, error) {
	if len(identities) == 0 {
		return nil, errors.New("age: no identities")
	}
	br := bufio.NewReader(src)
	stanzas, hdrForMAC, mac, err := parseHeader(br)
	if err != nil {
		return nil, err
	}
	var fileKey []byte
	for _, s := range stanzas {
		if s.typ != "X25519" {
			continue
		}
		for _, id := range identities {
			if k, err := unwrapX25519(id, s); err == nil {
				fileKey = k
				break
			}
		}
		if fileKey != nil {
			break
		}
	}
	if fileKey == nil {
		return nil, ErrNoIdentityMatch
	}
	want, err := headerMAC(fileKey, hdrForMAC)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(want, mac) {
		return nil, ErrHeaderMAC
	}
	nonce := make([]byte, streamNonce)
	if _, err := io.ReadFull(br, nonce); err != nil {
		return nil, fmt.Errorf("age: reading payload nonce: %w", err)
	}
	aead, err := payloadAEAD(fileKey, nonce)
	if err != nil {
		return nil, err
	}
	return &streamReader{src: br, aead: aead, enc: make([]byte, encChunkSize)}, nil
}

func unwrapX25519(id *Identity, s *stanza) ([]byte, error) {
	if len(s.args) != 1 {
		return nil, errors.New("age: invalid X25519 stanza")
	}
	ephBytes, err := b64.DecodeString(s.args[0])
	if err != nil {
		return nil, err
	}
	if len(s.body) != fileKeySize+chacha20poly1305.Overhead {
		return nil, errors.New("age: invalid X25519 stanza body")
	}
	eph, err := id.priv.Curve().NewPublicKey(ephBytes)
	if err != nil {
		return nil, err
	}
	shared, err := id.priv.ECDH(eph)
	if err != nil {
		return nil, err
	}
	salt := append(append([]byte{}, ephBytes...), id.priv.PublicKey().Bytes()...)
	key, err := hkdf.Key(sha256.New, shared, salt, x25519Label, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, make([]byte, chacha20poly1305.NonceSize), s.body, nil)
}

const maxHeaderLine = 16 * 1024

func readLine(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		b, err := br.ReadByte()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return "", fmt.Errorf("age: reading header: %w", err)
		}
		sb.WriteByte(b)
		if b == '\n' {
			return sb.String(), nil
		}
		if sb.Len() > maxHeaderLine {
			return "", errors.New("age: header line too long")
		}
	}
}

func parseHeader(br *bufio.Reader) ([]*stanza, []byte, []byte, error) {
	var raw bytes.Buffer
	line, err := readLine(br)
	if err != nil {
		return nil, nil, nil, err
	}
	if line != intro {
		return nil, nil, nil, errors.New("age: not an age v1 file")
	}
	raw.WriteString(line)
	var stanzas []*stanza
	for {
		line, err := readLine(br)
		if err != nil {
			return nil, nil, nil, err
		}
		switch {
		case strings.HasPrefix(line, "--- "):
			raw.WriteString("---")
			mac, err := b64.DecodeString(strings.TrimSuffix(line[4:], "\n"))
			if err != nil || len(mac) != sha256.Size {
				return nil, nil, nil, errors.New("age: malformed header MAC")
			}
			if len(stanzas) == 0 {
				return nil, nil, nil, errors.New("age: no recipient stanzas")
			}
			return stanzas, raw.Bytes(), mac, nil
		case strings.HasPrefix(line, "-> "):
			raw.WriteString(line)
			fields := strings.Split(strings.TrimSuffix(line[3:], "\n"), " ")
			for _, f := range fields {
				if f == "" {
					return nil, nil, nil, errors.New("age: malformed stanza")
				}
			}
			s := &stanza{typ: fields[0], args: fields[1:]}
			for {
				bl, err := readLine(br)
				if err != nil {
					return nil, nil, nil, err
				}
				raw.WriteString(bl)
				enc := strings.TrimSuffix(bl, "\n")
				if len(enc) > bodyColumns {
					return nil, nil, nil, errors.New("age: stanza body line too long")
				}
				chunk, err := b64.DecodeString(enc)
				if err != nil {
					return nil, nil, nil, fmt.Errorf("age: malformed stanza body: %w", err)
				}
				s.body = append(s.body, chunk...)
				if len(enc) < bodyColumns {
					break
				}
			}
			stanzas = append(stanzas, s)
		default:
			return nil, nil, nil, errors.New("age: malformed header")
		}
	}
}

type streamReader struct {
	src     *bufio.Reader
	aead    aeadCipher
	enc     []byte
	plain   []byte
	counter uint64
	done    bool
	err     error
}

func (r *streamReader) Read(p []byte) (int, error) {
	for len(r.plain) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		if r.done {
			return 0, io.EOF
		}
		r.err = r.next()
	}
	n := copy(p, r.plain)
	r.plain = r.plain[n:]
	return n, nil
}

func (r *streamReader) next() error {
	n, err := io.ReadFull(r.src, r.enc)
	last := false
	switch {
	case err == io.EOF:
		return ErrTruncated
	case err == io.ErrUnexpectedEOF:
		last = true
	case err != nil:
		return err
	default:
		if _, perr := r.src.Peek(1); perr == io.EOF {
			last = true
		} else if perr != nil {
			return perr
		}
	}
	if n < chacha20poly1305.Overhead {
		return ErrTruncated
	}
	out, err := r.aead.Open(r.enc[:0:0], chunkNonce(r.counter, last), r.enc[:n], nil)
	if err != nil {
		if !last {

			if _, err2 := r.aead.Open(nil, chunkNonce(r.counter, true), r.enc[:n], nil); err2 == nil {
				return errors.New("age: trailing data after final chunk")
			}
		}
		return fmt.Errorf("age: chunk %d: %w", r.counter, err)
	}
	if last && len(out) == 0 && r.counter > 0 {
		return errors.New("age: empty final chunk")
	}
	r.counter++
	r.plain = out
	r.done = last
	return nil
}
