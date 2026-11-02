// Package s3 là client S3 tự viết (không phụ thuộc SDK): SigV4, put đơn hoặc multipart,
// get, list v2, delete, If-None-Match, Object Lock. Tự thử lại yêu cầu khi lỗi mạng, 5xx hoặc 429.
package s3

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

const (
	MinPartSize = 5 << 20
	MaxParts    = 10000
	maxAttempts = 4
)

type Config struct {
	Endpoint           string // https://s3.ap-southeast-1.amazonaws.com, http://minio:9000
	Region             string // mặc định us-east-1
	Bucket             string
	PathStyle          bool
	Credentials        Credentials
	PartSize           int64 // mặc định 16 MiB
	CAFile             string
	InsecureSkipVerify bool
	Log                *slog.Logger
}

type Client struct {
	ep        *url.URL
	region    string
	bucket    string
	pathStyle bool
	cred      Credentials
	partSize  int64
	http      *http.Client
	log       *slog.Logger
	now       func() time.Time
	backoff   func(attempt int) time.Duration
}

func New(cfg Config) (*Client, error) {
	ep, err := url.Parse(cfg.Endpoint)
	if err != nil || (ep.Scheme != "http" && ep.Scheme != "https") || ep.Host == "" {
		return nil, fmt.Errorf("s3: invalid endpoint %q (want http(s)://host[:port])", cfg.Endpoint)
	}
	if ep.Path != "" && ep.Path != "/" {
		return nil, fmt.Errorf("s3: endpoint must not contain a path: %q", cfg.Endpoint)
	}
	if cfg.Bucket == "" {
		return nil, errors.New("s3: bucket is required")
	}
	if cfg.Credentials.AccessKeyID == "" || cfg.Credentials.SecretAccessKey == "" {
		return nil, errors.New("s3: access key and secret key are required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.PartSize == 0 {
		cfg.PartSize = 16 << 20
	}
	if cfg.PartSize < MinPartSize {
		return nil, fmt.Errorf("s3: part size must be at least %d bytes", MinPartSize)
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureSkipVerify}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("s3: ca_file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("s3: ca_file %s: no certificates found", cfg.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Client{
		ep: ep, region: cfg.Region, bucket: cfg.Bucket, pathStyle: cfg.PathStyle,
		cred: cfg.Credentials, partSize: cfg.PartSize, log: log, now: time.Now,
		backoff: func(a int) time.Duration { return time.Duration(a*a) * 300 * time.Millisecond },
		http: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				TLSClientConfig:       tlsCfg,
				ResponseHeaderTimeout: 2 * time.Minute,
				MaxIdleConnsPerHost:   4,
			},
			// Chuyển hướng (vd. sai region) làm hỏng chữ ký: trả lỗi rõ ràng thay vì đi theo.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// ---------- lỗi ----------

var (
	ErrNotFound           = errors.New("s3: not found")
	ErrPreconditionFailed = errors.New("s3: precondition failed")
)

type Error struct {
	Op         string
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (e *Error) Error() string {
	s := fmt.Sprintf("s3 %s: %d %s", e.Op, e.StatusCode, e.Code)
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.StatusCode == http.StatusMovedPermanently || e.Code == "PermanentRedirect" || e.Code == "AuthorizationHeaderMalformed" {
		s += " (check region and endpoint)"
	}
	return s
}

func (e *Error) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrPreconditionFailed:
		// 409 ConditionalRequestConflict: một ghi có điều kiện khác đang diễn ra cùng key.
		return e.StatusCode == http.StatusPreconditionFailed || e.Code == "ConditionalRequestConflict"
	}
	return false
}

func retryable(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.StatusCode >= 500 || e.StatusCode == http.StatusTooManyRequests || e.Code == "SlowDown" || e.Code == "RequestTimeout"
	}
	return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

type xmlError struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	RequestID string   `xml:"RequestId"`
}

func parseError(op string, status int, body []byte) *Error {
	e := &Error{Op: op, StatusCode: status, Code: http.StatusText(status)}
	var x xmlError
	if xml.Unmarshal(body, &x) == nil && x.Code != "" {
		e.Code, e.Message, e.RequestID = x.Code, x.Message, x.RequestID
	}
	return e
}

// ---------- yêu cầu ----------

type request struct {
	op      string
	method  string
	key     string
	query   url.Values
	header  http.Header
	body    []byte
	noRetry bool
}

func (c *Client) url(key string, q url.Values) *url.URL {
	u := *c.ep
	var path string
	if c.pathStyle {
		path = "/" + c.bucket
		if key != "" {
			path += "/" + key
		}
	} else {
		u.Host = c.bucket + "." + c.ep.Host
		path = "/" + key
	}
	u.Path = path
	u.RawPath = uriEncode(path, false)
	u.RawQuery = canonicalQuery(q)
	return &u
}

func (c *Client) sleep(ctx context.Context, attempt int) error {
	t := time.NewTimer(c.backoff(attempt))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do gửi yêu cầu (thử lại khi được phép) và trả về phản hồi 2xx; lỗi đã được phân tích thành *Error.
func (c *Client) do(ctx context.Context, rq request) (*http.Response, error) {
	payloadHash := EmptySHA256
	if rq.body != nil {
		payloadHash = hexSHA256(rq.body)
	}
	u := c.url(rq.key, rq.query)
	if u.EscapedPath() != u.RawPath {
		return nil, fmt.Errorf("s3: cannot encode key %q", rq.key)
	}
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, attempt); err != nil {
				return nil, err
			}
			c.log.Warn("retrying s3 request", "op", rq.op, "key", rq.key, "attempt", attempt+1, "err", last)
		}
		req, err := http.NewRequestWithContext(ctx, rq.method, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.URL = u
		switch {
		case len(rq.body) > 0:
			body := rq.body
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
			req.ContentLength = int64(len(body))
		case rq.body != nil:
			// Body rỗng phải là http.NoBody: reader rỗng khiến Go gửi chunked, S3 trả 411.
			req.Body = http.NoBody
			req.GetBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
		}
		for k, vs := range rq.header {
			req.Header[k] = vs
		}
		Sign(req, c.cred, c.region, payloadHash, c.now())
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			last = fmt.Errorf("s3 %s: %w", rq.op, err)
		} else if resp.StatusCode/100 == 2 {
			return resp, nil
		} else {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			last = parseError(rq.op, resp.StatusCode, body)
		}
		if rq.noRetry || !retryable(last) {
			return nil, last
		}
	}
	return nil, last
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
}

// ---------- Object Lock ----------

const (
	LockGovernance = "GOVERNANCE"
	LockCompliance = "COMPLIANCE"
)

type Retention struct {
	Mode  string // GOVERNANCE | COMPLIANCE
	Until time.Time
}

type PutOptions struct {
	IfNoneMatch bool       // chỉ ghi khi key chưa tồn tại (412 nếu đã có)
	Retention   *Retention // Object Lock cho object này
	ContentType string     // mặc định application/octet-stream
}

func (o PutOptions) contentType() string {
	if o.ContentType != "" {
		return o.ContentType
	}
	return "application/octet-stream"
}

func (o PutOptions) apply(h http.Header, onComplete bool) {
	if o.Retention != nil && !onComplete {
		h.Set("X-Amz-Object-Lock-Mode", o.Retention.Mode)
		h.Set("X-Amz-Object-Lock-Retain-Until-Date", o.Retention.Until.UTC().Format("2006-01-02T15:04:05Z"))
	}
}

func contentMD5(b []byte) string {
	sum := md5.Sum(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// ---------- thao tác ----------

func (c *Client) putOnce(ctx context.Context, key string, data []byte, o PutOptions, noRetry bool) error {
	h := http.Header{}
	h.Set("Content-Type", o.contentType())
	h.Set("Content-Md5", contentMD5(data)) // bắt buộc khi có Object Lock; luôn gửi để S3 kiểm toàn vẹn
	if o.IfNoneMatch {
		h.Set("If-None-Match", "*")
	}
	o.apply(h, false)
	resp, err := c.do(ctx, request{op: "PutObject", method: http.MethodPut, key: key, header: h, body: nonNil(data), noRetry: noRetry})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// PutObject ghi một object từ bộ nhớ.
func (c *Client) PutObject(ctx context.Context, key string, data []byte, o PutOptions) error {
	if !o.IfNoneMatch {
		return c.putOnce(ctx, key, data, o, false)
	}
	// Ghi có điều kiện: nếu lần trước thực ra đã thành công nhưng mất phản hồi, lần thử lại nhận 412.
	// Khi đó đọc lại và coi là thành công nếu nội dung đúng là của mình.
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if serr := c.sleep(ctx, attempt); serr != nil {
				return serr
			}
		}
		err = c.putOnce(ctx, key, data, o, true)
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrPreconditionFailed) {
			if attempt > 0 {
				if cur, gerr := c.getBytes(ctx, key); gerr == nil && bytes.Equal(cur, data) {
					return nil
				}
			}
			return err
		}
		if !retryable(err) {
			return err
		}
	}
	return err
}

func (c *Client) getBytes(ctx context.Context, key string) ([]byte, error) {
	rc, err := c.GetObject(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// Upload đọc r tới hết và ghi vào key. Dữ liệu nhỏ hơn một part: PutObject; lớn hơn: multipart,
// bộ nhớ dùng tối đa một part. Nếu r trả lỗi, object không được tạo và upload dở bị hủy.
func (c *Client) Upload(ctx context.Context, key string, r io.Reader, o PutOptions) error {
	buf := make([]byte, c.partSize)
	n, rerr := io.ReadFull(r, buf)
	switch {
	case rerr == io.EOF || rerr == io.ErrUnexpectedEOF:
		return c.PutObject(ctx, key, buf[:n], o)
	case rerr != nil:
		return rerr
	}

	uploadID, err := c.createMultipart(ctx, key, o)
	if err != nil {
		return err
	}
	abort := func(cause error) error {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := c.abortMultipart(actx, key, uploadID); err != nil {
			c.log.Warn("aborting multipart upload failed; add a lifecycle rule AbortIncompleteMultipartUpload", "key", key, "upload_id", uploadID, "err", err)
		}
		return cause
	}
	var parts []completedPart
	for num := 1; ; num++ {
		if num > MaxParts {
			return abort(fmt.Errorf("s3: object exceeds %d parts of %d MiB; increase part_size_mb", MaxParts, c.partSize>>20))
		}
		etag, err := c.uploadPart(ctx, key, uploadID, num, buf[:n])
		if err != nil {
			return abort(err)
		}
		parts = append(parts, completedPart{PartNumber: num, ETag: etag})
		if rerr == io.ErrUnexpectedEOF {
			break
		}
		n, rerr = io.ReadFull(r, buf)
		if rerr == io.EOF {
			break
		}
		if rerr != nil && rerr != io.ErrUnexpectedEOF {
			return abort(rerr)
		}
	}
	if err := c.completeMultipart(ctx, key, uploadID, parts, o); err != nil {
		return abort(err)
	}
	return nil
}

type initiateResult struct {
	UploadID string `xml:"UploadId"`
}

func (c *Client) createMultipart(ctx context.Context, key string, o PutOptions) (string, error) {
	h := http.Header{}
	h.Set("Content-Type", o.contentType())
	o.apply(h, false)
	resp, err := c.do(ctx, request{op: "CreateMultipartUpload", method: http.MethodPost, key: key, query: url.Values{"uploads": {""}}, header: h})
	if err != nil {
		return "", err
	}
	defer drain(resp)
	var res initiateResult
	if err := xml.NewDecoder(resp.Body).Decode(&res); err != nil || res.UploadID == "" {
		return "", fmt.Errorf("s3 CreateMultipartUpload: bad response: %v", err)
	}
	return res.UploadID, nil
}

func (c *Client) uploadPart(ctx context.Context, key, uploadID string, num int, data []byte) (string, error) {
	h := http.Header{}
	h.Set("Content-Md5", contentMD5(data))
	resp, err := c.do(ctx, request{op: "UploadPart", method: http.MethodPut, key: key, header: h, body: nonNil(data),
		query: url.Values{"partNumber": {strconv.Itoa(num)}, "uploadId": {uploadID}}})
	if err != nil {
		return "", err
	}
	drain(resp)
	etag := resp.Header.Get("ETag")
	if etag == "" {
		return "", errors.New("s3 UploadPart: response has no ETag")
	}
	return etag, nil
}

type completedPart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type completeRequest struct {
	XMLName xml.Name        `xml:"CompleteMultipartUpload"`
	Parts   []completedPart `xml:"Part"`
}

func (c *Client) completeMultipart(ctx context.Context, key, uploadID string, parts []completedPart, o PutOptions) error {
	body, _ := xml.Marshal(completeRequest{Parts: parts})
	h := http.Header{}
	h.Set("Content-Type", "application/xml")
	if o.IfNoneMatch {
		h.Set("If-None-Match", "*")
	}
	resp, err := c.do(ctx, request{op: "CompleteMultipartUpload", method: http.MethodPost, key: key, header: h, body: body,
		query: url.Values{"uploadId": {uploadID}}})
	if err != nil {
		return err
	}
	// S3 có thể trả 200 rồi mới báo lỗi trong thân phản hồi.
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if rerr != nil {
		return fmt.Errorf("s3 CompleteMultipartUpload: reading response: %w", rerr)
	}
	if bytes.Contains(data, []byte("<Error>")) {
		return parseError("CompleteMultipartUpload", resp.StatusCode, data)
	}
	return nil
}

func (c *Client) abortMultipart(ctx context.Context, key, uploadID string) error {
	resp, err := c.do(ctx, request{op: "AbortMultipartUpload", method: http.MethodDelete, key: key, query: url.Values{"uploadId": {uploadID}}})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	drain(resp)
	return nil
}

// GetObject trả về nội dung object; người gọi phải Close.
func (c *Client) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, _, err := c.GetObjectWith(ctx, key, GetOptions{})
	return rc, err
}

type GetOptions struct {
	IfMatch string // ETag; object đã đổi thì trả ErrPreconditionFailed
}

type ObjectMeta struct {
	ETag        string
	Size        int64
	ContentType string
}

func (c *Client) GetObjectWith(ctx context.Context, key string, o GetOptions) (io.ReadCloser, ObjectMeta, error) {
	h := http.Header{}
	if o.IfMatch != "" {
		h.Set("If-Match", o.IfMatch)
	}
	resp, err := c.do(ctx, request{op: "GetObject", method: http.MethodGet, key: key, header: h})
	if err != nil {
		return nil, ObjectMeta{}, err
	}
	return resp.Body, ObjectMeta{ETag: resp.Header.Get("ETag"), Size: resp.ContentLength, ContentType: resp.Header.Get("Content-Type")}, nil
}

type ObjectInfo struct {
	Key          string    `xml:"Key"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
	LastModified time.Time `xml:"LastModified"`
}

type listResult struct {
	Contents              []ObjectInfo `xml:"Contents"`
	IsTruncated           bool         `xml:"IsTruncated"`
	NextContinuationToken string       `xml:"NextContinuationToken"`
}

// ListObjects trả về mọi key có tiền tố prefix (ListObjectsV2, tự phân trang).
func (c *Client) ListObjects(ctx context.Context, prefix string) ([]string, error) {
	infos, err := c.list(ctx, prefix, 0)
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(infos))
	for i, o := range infos {
		keys[i] = o.Key
	}
	return keys, nil
}

// ListObjectsInfo như ListObjects nhưng kèm ETag, kích thước, thời điểm sửa.
func (c *Client) ListObjectsInfo(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	return c.list(ctx, prefix, 0)
}

func (c *Client) list(ctx context.Context, prefix string, limit int) ([]ObjectInfo, error) {
	var keys []ObjectInfo
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		if limit > 0 {
			q.Set("max-keys", strconv.Itoa(limit))
		}
		resp, err := c.do(ctx, request{op: "ListObjectsV2", method: http.MethodGet, query: q})
		if err != nil {
			return nil, err
		}
		var res listResult
		err = xml.NewDecoder(resp.Body).Decode(&res)
		drain(resp)
		if err != nil {
			return nil, fmt.Errorf("s3 ListObjectsV2: bad response: %w", err)
		}
		keys = append(keys, res.Contents...)
		if !res.IsTruncated || limit > 0 {
			return keys, nil
		}
		if res.NextContinuationToken == "" {
			return nil, errors.New("s3 ListObjectsV2: truncated result without continuation token")
		}
		token = res.NextContinuationToken
	}
}

// Probe kiểm bucket truy cập được với quyền list.
func (c *Client) Probe(ctx context.Context, prefix string) error {
	_, err := c.list(ctx, prefix, 1)
	return err
}

// DeleteObject xóa key; trên bucket có versioning (Object Lock) chỉ tạo delete marker,
// bản đã khóa vẫn còn đến hết hạn khóa.
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	resp, err := c.do(ctx, request{op: "DeleteObject", method: http.MethodDelete, key: key})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	drain(resp)
	return nil
}

type lockConfig struct {
	ObjectLockEnabled string `xml:"ObjectLockEnabled"`
}

// ObjectLockEnabled cho biết bucket có bật Object Lock không.
func (c *Client) ObjectLockEnabled(ctx context.Context) (bool, error) {
	resp, err := c.do(ctx, request{op: "GetObjectLockConfiguration", method: http.MethodGet, query: url.Values{"object-lock": {""}}})
	if err != nil {
		var e *Error
		if errors.As(err, &e) && (e.Code == "ObjectLockConfigurationNotFoundError" || e.StatusCode == http.StatusNotFound) {
			return false, nil
		}
		return false, err
	}
	defer drain(resp)
	var cfg lockConfig
	if err := xml.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return false, fmt.Errorf("s3 GetObjectLockConfiguration: bad response: %w", err)
	}
	return cfg.ObjectLockEnabled == "Enabled", nil
}
