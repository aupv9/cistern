// Package s3test là server S3 giả lập trong bộ nhớ, dùng trong test. Chỉ hỗ trợ path-style.
//
// Server kiểm chặt như S3 thật ở những điểm client dễ sai: chữ ký SigV4 (tính lại từ yêu cầu nhận
// được), x-amz-content-sha256, Content-MD5, Content-MD5 bắt buộc khi có Object Lock, kích thước
// part tối thiểu, If-None-Match. Bucket có Object Lock thì có versioning: DELETE tạo delete marker,
// bản đã khóa không xóa được trước hạn.
package s3test

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cistern/internal/s3"
)

const (
	Bucket    = "backups"
	Region    = "us-east-1"
	AccessKey = "TESTACCESSKEY"
	SecretKey = "TESTSECRETKEY"
)

var Creds = s3.Credentials{AccessKeyID: AccessKey, SecretAccessKey: SecretKey}

type Version struct {
	ID           string
	Data         []byte
	ETag         string
	ContentType  string
	Modified     time.Time
	DeleteMarker bool
	LockMode     string
	RetainUntil  time.Time
}

type upload struct {
	key         string
	contentType string
	parts       map[int][]byte
	lockMode    string
	retainUntil time.Time
}

// Fault được trả cho yêu cầu khớp đầu tiên rồi bị gỡ khỏi hàng đợi.
type Fault struct {
	Method string // rỗng = mọi method
	Query  string // tham số phải có trong query, vd. "partNumber"; rỗng = không kiểm
	Status int
	// AfterProcessing: xử lý yêu cầu bình thường rồi vẫn trả Status (mô phỏng mất phản hồi).
	AfterProcessing bool
}

type Server struct {
	*httptest.Server
	ObjectLock bool
	MaxKeys    int
	// CompleteError: CompleteMultipartUpload trả 200 kèm <Error> trong thân.
	CompleteError bool
	// BeforeGet được gọi (không giữ khóa) trước khi phục vụ GET object, vd. để sửa object giữa chừng.
	BeforeGet func(key string)

	mu       sync.Mutex
	objects  map[string][]*Version
	uploads  map[string]*upload
	faults   []Fault
	requests []string
	seq      int
}

func New(objectLock bool) *Server {
	s := &Server{ObjectLock: objectLock, MaxKeys: 1000, objects: map[string][]*Version{}, uploads: map[string]*upload{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

func (s *Server) Inject(f Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, f)
}

func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// Current trả về bản hiện hành (không phải delete marker) của key.
func (s *Server) Current(key string) (*Version, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current(key)
}

func (s *Server) current(key string) (*Version, bool) {
	vs := s.objects[key]
	if len(vs) == 0 || vs[len(vs)-1].DeleteMarker {
		return nil, false
	}
	return vs[len(vs)-1], true
}

// Versions trả về mọi phiên bản của key, cũ trước.
func (s *Server) Versions(key string) []*Version {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Version(nil), s.objects[key]...)
}

// Keys trả về các key có bản hiện hành.
func (s *Server) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ks []string
	for k := range s.objects {
		if _, ok := s.current(k); ok {
			ks = append(ks, k)
		}
	}
	sort.Strings(ks)
	return ks
}

func (s *Server) OpenUploads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uploads)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message><RequestId>fake</RequestId></Error>", code, msg)
}

func writeXML(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/xml")
	b, _ := xml.Marshal(v)
	w.Write([]byte(xml.Header))
	w.Write(b)
}

func (s *Server) takeFault(r *http.Request) *Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, f := range s.faults {
		if (f.Method == "" || f.Method == r.Method) && (f.Query == "" || r.URL.Query().Has(f.Query)) {
			s.faults = append(s.faults[:i], s.faults[i+1:]...)
			return &f
		}
	}
	return nil
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
	s.mu.Unlock()

	if err := s3.Verify(r, Creds, Region); err != nil {
		writeError(w, http.StatusForbidden, "SignatureDoesNotMatch", err.Error())
		return
	}
	if h := r.Header.Get("X-Amz-Content-Sha256"); h != s3.UnsignedPayload {
		sum := sha256.Sum256(body)
		if h != hex.EncodeToString(sum[:]) {
			writeError(w, http.StatusBadRequest, "XAmzContentSHA256Mismatch", "payload hash mismatch")
			return
		}
	}
	if m := r.Header.Get("Content-Md5"); m != "" {
		sum := md5.Sum(body)
		if m != base64.StdEncoding.EncodeToString(sum[:]) {
			writeError(w, http.StatusBadRequest, "BadDigest", "Content-MD5 mismatch")
			return
		}
	}

	if s.BeforeGet != nil && r.Method == http.MethodGet && !r.URL.Query().Has("list-type") && !r.URL.Query().Has("object-lock") {
		_, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if key != "" {
			s.BeforeGet(key)
		}
	}
	// Như S3: PUT phải có Content-Length (không nhận chunked transfer encoding).
	if r.Method == http.MethodPut && r.ContentLength < 0 {
		writeError(w, http.StatusLengthRequired, "MissingContentLength", "You must provide the Content-Length HTTP header.")
		return
	}
	if f := s.takeFault(r); f != nil {
		if f.AfterProcessing {
			s.handle(httptest.NewRecorder(), r, body)
		}
		writeError(w, f.Status, "InternalError", "injected fault")
		return
	}
	s.handle(w, r, body)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request, body []byte) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	if bucket != Bucket {
		writeError(w, http.StatusNotFound, "NoSuchBucket", "no such bucket "+bucket)
		return
	}
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()

	if key == "" {
		switch {
		case r.Method == http.MethodGet && q.Has("object-lock"):
			if !s.ObjectLock {
				writeError(w, http.StatusNotFound, "ObjectLockConfigurationNotFoundError", "no object lock")
				return
			}
			writeXML(w, struct {
				XMLName           xml.Name `xml:"ObjectLockConfiguration"`
				ObjectLockEnabled string
			}{ObjectLockEnabled: "Enabled"})
		case r.Method == http.MethodGet && q.Get("list-type") == "2":
			s.list(w, q)
		default:
			writeError(w, http.StatusNotImplemented, "NotImplemented", r.Method+" bucket")
		}
		return
	}

	switch {
	case r.Method == http.MethodPut && q.Has("uploadId"):
		u := s.uploads[q.Get("uploadId")]
		if u == nil {
			writeError(w, http.StatusNotFound, "NoSuchUpload", "no such upload")
			return
		}
		if s.ObjectLock && r.Header.Get("Content-Md5") == "" {
			writeError(w, http.StatusBadRequest, "InvalidRequest", "Content-MD5 required for Object Lock")
			return
		}
		n, _ := strconv.Atoi(q.Get("partNumber"))
		u.parts[n] = append([]byte(nil), body...)
		sum := md5.Sum(body)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	case r.Method == http.MethodPut:
		mode, until, ok := s.lockHeaders(w, r)
		if !ok {
			return
		}
		if r.Header.Get("If-None-Match") == "*" {
			if _, exists := s.current(key); exists {
				writeError(w, http.StatusPreconditionFailed, "PreconditionFailed", "object exists")
				return
			}
		}
		sum := md5.Sum(body)
		v := s.put(key, append([]byte(nil), body...), `"`+hex.EncodeToString(sum[:])+`"`, mode, until)
		v.ContentType = r.Header.Get("Content-Type")
		w.Header().Set("ETag", v.ETag)
	case r.Method == http.MethodPost && q.Has("uploads"):
		mode, until, ok := s.lockHeaders(w, r)
		if !ok {
			return
		}
		s.seq++
		id := fmt.Sprintf("upload-%d", s.seq)
		s.uploads[id] = &upload{key: key, parts: map[int][]byte{}, lockMode: mode, retainUntil: until, contentType: r.Header.Get("Content-Type")}
		writeXML(w, struct {
			XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
			Bucket   string
			Key      string
			UploadId string
		}{Bucket: Bucket, Key: key, UploadId: id})
	case r.Method == http.MethodPost && q.Has("uploadId"):
		s.complete(w, r, key, q.Get("uploadId"), body)
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		if _, ok := s.uploads[q.Get("uploadId")]; !ok {
			writeError(w, http.StatusNotFound, "NoSuchUpload", "no such upload")
			return
		}
		delete(s.uploads, q.Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet:
		v, ok := s.current(key)
		if !ok {
			writeError(w, http.StatusNotFound, "NoSuchKey", "no such key")
			return
		}
		if m := r.Header.Get("If-Match"); m != "" && m != v.ETag {
			writeError(w, http.StatusPreconditionFailed, "PreconditionFailed", "ETag does not match")
			return
		}
		w.Header().Set("ETag", v.ETag)
		if v.ContentType != "" {
			w.Header().Set("Content-Type", v.ContentType)
		}
		w.Write(v.Data)
	case r.Method == http.MethodDelete:
		if vid := q.Get("versionId"); vid != "" {
			s.deleteVersion(w, key, vid)
			return
		}
		if s.ObjectLock {
			s.seq++
			s.objects[key] = append(s.objects[key], &Version{ID: fmt.Sprintf("v%d", s.seq), DeleteMarker: true})
		} else {
			delete(s.objects, key)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusNotImplemented, "NotImplemented", r.Method)
	}
}

func (s *Server) lockHeaders(w http.ResponseWriter, r *http.Request) (string, time.Time, bool) {
	mode := r.Header.Get("X-Amz-Object-Lock-Mode")
	untilStr := r.Header.Get("X-Amz-Object-Lock-Retain-Until-Date")
	if mode == "" && untilStr == "" {
		return "", time.Time{}, true
	}
	if !s.ObjectLock {
		writeError(w, http.StatusBadRequest, "InvalidRequest", "Bucket is missing Object Lock Configuration")
		return "", time.Time{}, false
	}
	if mode != s3.LockGovernance && mode != s3.LockCompliance {
		writeError(w, http.StatusBadRequest, "InvalidArgument", "bad lock mode")
		return "", time.Time{}, false
	}
	until, err := time.Parse(time.RFC3339, untilStr)
	if err != nil || !until.After(time.Now()) {
		writeError(w, http.StatusBadRequest, "InvalidArgument", "bad retain-until date")
		return "", time.Time{}, false
	}
	if r.Method == http.MethodPut && r.Header.Get("Content-Md5") == "" {
		writeError(w, http.StatusBadRequest, "InvalidRequest", "Content-MD5 required for Object Lock")
		return "", time.Time{}, false
	}
	return mode, until, true
}

func (s *Server) put(key string, data []byte, etag, mode string, until time.Time) *Version {
	s.seq++
	v := &Version{ID: fmt.Sprintf("v%d", s.seq), Data: data, ETag: etag, LockMode: mode, RetainUntil: until, Modified: time.Now().UTC()}
	if s.ObjectLock {
		s.objects[key] = append(s.objects[key], v)
	} else {
		s.objects[key] = []*Version{v}
	}
	return v
}

func (s *Server) deleteVersion(w http.ResponseWriter, key, vid string) {
	vs := s.objects[key]
	for i, v := range vs {
		if v.ID != vid {
			continue
		}
		if v.LockMode != "" && time.Now().Before(v.RetainUntil) {
			writeError(w, http.StatusForbidden, "AccessDenied", "Object is WORM protected and cannot be overwritten")
			return
		}
		s.objects[key] = append(vs[:i], vs[i+1:]...)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeError(w, http.StatusNotFound, "NoSuchVersion", "no such version")
}

type completeBody struct {
	Parts []struct {
		PartNumber int
		ETag       string
	} `xml:"Part"`
}

func (s *Server) complete(w http.ResponseWriter, r *http.Request, key, id string, body []byte) {
	u := s.uploads[id]
	if u == nil || u.key != key {
		writeError(w, http.StatusNotFound, "NoSuchUpload", "no such upload")
		return
	}
	var cb completeBody
	if err := xml.Unmarshal(body, &cb); err != nil || len(cb.Parts) == 0 {
		writeError(w, http.StatusBadRequest, "MalformedXML", "bad complete body")
		return
	}
	var data, md5s []byte
	for i, p := range cb.Parts {
		part, ok := u.parts[p.PartNumber]
		sum := md5.Sum(part)
		if !ok || p.ETag != `"`+hex.EncodeToString(sum[:])+`"` || p.PartNumber != i+1 {
			writeError(w, http.StatusBadRequest, "InvalidPart", fmt.Sprintf("part %d", p.PartNumber))
			return
		}
		if i < len(cb.Parts)-1 && len(part) < s3.MinPartSize {
			writeError(w, http.StatusBadRequest, "EntityTooSmall", fmt.Sprintf("part %d is %d bytes", p.PartNumber, len(part)))
			return
		}
		data = append(data, part...)
		md5s = append(md5s, sum[:]...)
	}
	if r.Header.Get("If-None-Match") == "*" {
		if _, exists := s.current(key); exists {
			writeError(w, http.StatusPreconditionFailed, "PreconditionFailed", "object exists")
			return
		}
	}
	if s.CompleteError {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<Error><Code>InternalError</Code><Message>completion failed after 200</Message></Error>")
		return
	}
	sum := md5.Sum(md5s)
	v := s.put(key, data, fmt.Sprintf(`"%s-%d"`, hex.EncodeToString(sum[:]), len(cb.Parts)), u.lockMode, u.retainUntil)
	v.ContentType = u.contentType
	delete(s.uploads, id)
	writeXML(w, struct {
		XMLName xml.Name `xml:"CompleteMultipartUploadResult"`
		Key     string
		ETag    string
	}{Key: key, ETag: v.ETag})
}

func (s *Server) list(w http.ResponseWriter, q map[string][]string) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	prefix, token := get("prefix"), get("continuation-token")
	max := s.MaxKeys
	if m, err := strconv.Atoi(get("max-keys")); err == nil && m > 0 && m < max {
		max = m
	}
	var keys []string
	for k := range s.objects {
		if _, ok := s.current(k); ok && strings.HasPrefix(k, prefix) && k > token {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	type content struct {
		Key          string
		LastModified string
		ETag         string
		Size         int
	}
	res := struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		Name                  string
		Prefix                string
		KeyCount              int
		MaxKeys               int
		IsTruncated           bool
		Contents              []content
		NextContinuationToken string `xml:",omitempty"`
	}{Name: Bucket, Prefix: prefix, MaxKeys: max}
	if len(keys) > max {
		keys = keys[:max]
		res.IsTruncated = true
		res.NextContinuationToken = keys[len(keys)-1]
	}
	for _, k := range keys {
		v, _ := s.current(k)
		res.Contents = append(res.Contents, content{Key: k, Size: len(v.Data), ETag: v.ETag, LastModified: v.Modified.Format("2006-01-02T15:04:05.000Z")})
	}
	res.KeyCount = len(res.Contents)
	writeXML(w, res)
}

// Bytes tiện cho test: nội dung bản hiện hành.
func (s *Server) Bytes(key string) []byte {
	if v, ok := s.Current(key); ok {
		return bytes.Clone(v.Data)
	}
	return nil
}
