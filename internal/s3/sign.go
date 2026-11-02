package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4 cho S3 (https://docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html).

const (
	algorithm       = "AWS4-HMAC-SHA256"
	amzDateFormat   = "20060102T150405Z"
	EmptySHA256     = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	UnsignedPayload = "UNSIGNED-PAYLOAD"
)

type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// uriEncode: giữ A-Z a-z 0-9 - _ . ~ (và '/' khi encodeSlash=false), còn lại %XX chữ hoa.
func uriEncode(s string, encodeSlash bool) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == '~' || (c == '/' && !encodeSlash) {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, "%%%02X", c)
		}
	}
	return sb.String()
}

// canonicalQuery cũng là query string thật gửi đi, nên chữ ký và yêu cầu luôn khớp.
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

func hexSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func headerValue(req *http.Request, name string) string {
	if name == "host" {
		if req.Host != "" {
			return req.Host
		}
		return req.URL.Host
	}
	vals := req.Header.Values(name)
	for i, v := range vals {
		vals[i] = strings.Join(strings.Fields(v), " ")
	}
	return strings.Join(vals, ",")
}

func canonicalRequest(req *http.Request, signed []string, payloadHash string) string {
	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	var hdr strings.Builder
	for _, n := range signed {
		hdr.WriteString(n + ":" + headerValue(req, n) + "\n")
	}
	return strings.Join([]string{
		req.Method,
		uriEncode(path, false),
		canonicalQuery(req.URL.Query()),
		hdr.String(),
		strings.Join(signed, ";"),
		payloadHash,
	}, "\n")
}

func signature(req *http.Request, signed []string, payloadHash, region string, cred Credentials, t time.Time) (sig, scope string) {
	date := t.UTC().Format("20060102")
	scope = date + "/" + region + "/s3/aws4_request"
	sts := algorithm + "\n" + t.UTC().Format(amzDateFormat) + "\n" + scope + "\n" + hexSHA256([]byte(canonicalRequest(req, signed, payloadHash)))
	k := hmacSHA256([]byte("AWS4"+cred.SecretAccessKey), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	return hex.EncodeToString(hmacSHA256(k, sts)), scope
}

// Sign đặt X-Amz-Date, X-Amz-Content-Sha256, X-Amz-Security-Token (nếu có) và Authorization.
// Ký host và mọi header đang có trên req (trừ Authorization, User-Agent).
func Sign(req *http.Request, cred Credentials, region, payloadHash string, t time.Time) {
	req.Header.Set("X-Amz-Date", t.UTC().Format(amzDateFormat))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if cred.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", cred.SessionToken)
	}
	signed := []string{"host"}
	for k := range req.Header {
		lk := strings.ToLower(k)
		if lk == "authorization" || lk == "user-agent" || lk == "host" {
			continue
		}
		signed = append(signed, lk)
	}
	sort.Strings(signed)
	sig, scope := signature(req, signed, payloadHash, region, cred, t)
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, cred.AccessKeyID, scope, strings.Join(signed, ";"), sig))
}

// Verify kiểm chữ ký của một yêu cầu đã nhận (dùng trong server giả để bắt lỗi chính tắc hóa).
func Verify(req *http.Request, cred Credentials, region string) error {
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, algorithm+" ") {
		return errors.New("missing or unsupported Authorization")
	}
	fields := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(auth, algorithm+" "), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		fields[k] = v
	}
	credParts := strings.Split(fields["Credential"], "/")
	if len(credParts) != 5 || credParts[0] != cred.AccessKeyID || credParts[2] != region || credParts[3] != "s3" {
		return fmt.Errorf("bad credential scope %q", fields["Credential"])
	}
	t, err := time.Parse(amzDateFormat, req.Header.Get("X-Amz-Date"))
	if err != nil {
		return fmt.Errorf("bad X-Amz-Date: %w", err)
	}
	signed := strings.Split(fields["SignedHeaders"], ";")
	want, _ := signature(req, signed, req.Header.Get("X-Amz-Content-Sha256"), region, cred, t)
	if !hmac.Equal([]byte(want), []byte(fields["Signature"])) {
		return errors.New("signature does not match")
	}
	return nil
}
