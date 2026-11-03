package crypt

import (
	"bufio"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	recipientHRP = "age"
	identityHRP  = "age-secret-key-"
)

type Recipient struct{ pub *ecdh.PublicKey }

type Identity struct{ priv *ecdh.PrivateKey }

func GenerateIdentity() (*Identity, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{priv: k}, nil
}

func ParseRecipient(s string) (*Recipient, error) {
	hrp, data, err := bech32Decode(s)
	if err != nil {
		return nil, fmt.Errorf("malformed recipient %q: %w", s, err)
	}
	if hrp != recipientHRP {
		return nil, fmt.Errorf("malformed recipient %q: unexpected type %q", s, hrp)
	}
	pub, err := ecdh.X25519().NewPublicKey(data)
	if err != nil {
		return nil, fmt.Errorf("malformed recipient %q: %w", s, err)
	}
	return &Recipient{pub: pub}, nil
}

func ParseIdentity(s string) (*Identity, error) {
	hrp, data, err := bech32Decode(s)
	if err != nil {
		return nil, fmt.Errorf("malformed secret key: %w", err)
	}
	if hrp != identityHRP {
		return nil, fmt.Errorf("malformed secret key: unknown type %q", hrp)
	}
	priv, err := ecdh.X25519().NewPrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("malformed secret key: %w", err)
	}
	return &Identity{priv: priv}, nil
}

func (r *Recipient) String() string {
	s, _ := bech32Encode(recipientHRP, r.pub.Bytes())
	return s
}

func (i *Identity) String() string {
	s, _ := bech32Encode(identityHRP, i.priv.Bytes())
	return strings.ToUpper(s)
}

func (i *Identity) Recipient() *Recipient { return &Recipient{pub: i.priv.PublicKey()} }

func WriteIdentityFile(w io.Writer, id *Identity, now time.Time) error {
	_, err := fmt.Fprintf(w, "# created: %s\n# public key: %s\n%s\n",
		now.Format(time.RFC3339), id.Recipient(), id)
	return err
}

func ParseIdentities(r io.Reader) ([]*Identity, error) {
	var ids []*Identity
	err := scanKeyLines(r, func(line string) error {
		id, err := ParseIdentity(line)
		if err == nil {
			ids = append(ids, id)
		}
		return err
	})
	if err == nil && len(ids) == 0 {
		err = errors.New("no identities found")
	}
	return ids, err
}

func ParseRecipients(r io.Reader) ([]*Recipient, error) {
	var rs []*Recipient
	err := scanKeyLines(r, func(line string) error {
		rc, err := ParseRecipient(line)
		if err == nil {
			rs = append(rs, rc)
		}
		return err
	})
	if err == nil && len(rs) == 0 {
		err = errors.New("no recipients found")
	}
	return rs, err
}

func scanKeyLines(r io.Reader, f func(string) error) error {
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := f(line); err != nil {
			return fmt.Errorf("line %d: %w", n, err)
		}
	}
	return sc.Err()
}
