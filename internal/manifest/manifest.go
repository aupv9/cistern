// Package manifest định nghĩa manifest.json của snapshot, ID snapshot và bố cục khóa trong repository.
//
//	<prefix>/<job>/<snapshot-id>/manifest.json
//	<prefix>/<job>/<snapshot-id>/artifacts/<tên artifact>.gz.age
//	<prefix>/locks/<job>.lock
package manifest

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

const Format = 1

type Source struct {
	Version string            `json:"version,omitempty"`
	Host    string            `json:"host,omitempty"` // đã bỏ credential
	Extra   map[string]string `json:"extra,omitempty"`
}

type Artifact struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	PlainSize    int64  `json:"plain_size"`
	StoredSize   int64  `json:"stored_size"`
	PlainSHA256  string `json:"plain_sha256"`
	StoredSHA256 string `json:"stored_sha256"`
}

type Manifest struct {
	Format         int        `json:"format"`
	ID             string     `json:"id"`
	Job            string     `json:"job"`
	Driver         string     `json:"driver"`
	Status         string     `json:"status"` // luôn "completed"
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     time.Time  `json:"finished_at"`
	Source         Source     `json:"source"`
	Compression    string     `json:"compression"` // gzip | none
	Encryption     string     `json:"encryption"`  // age-x25519 | none
	Recipients     []string   `json:"recipients,omitempty"`
	DependsOn      []string   `json:"depends_on,omitempty"`
	Artifacts      []Artifact `json:"artifacts"`
	CisternVersion string     `json:"cistern_version"`
	Hostname       string     `json:"hostname"`
}

// Lock là nội dung locks/<job>.lock.
type Lock struct {
	Host     string    `json:"host"`
	PID      int       `json:"pid"`
	Op       string    `json:"op"`
	Snapshot string    `json:"snapshot,omitempty"`
	Started  time.Time `json:"started"`
	Expires  time.Time `json:"expires"` // started + timeout + 10 phút
}

const LockGrace = 10 * time.Minute

var (
	jobRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	segmentRe  = regexp.MustCompile(`^[A-Za-z0-9._@=+-]+$`)
	snapshotRe = regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{4}$`)
)

// NewSnapshotID trả về ID dạng YYYYMMDDTHHMMSSZ-xxxx (UTC), sắp xếp theo thời gian khi so chuỗi.
func NewSnapshotID(t time.Time) string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return t.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

func ValidSnapshotID(id string) bool { return snapshotRe.MatchString(id) }

func ValidateJobName(name string) error {
	if name == "locks" || !jobRe.MatchString(name) {
		return fmt.Errorf("invalid job name %q", name)
	}
	return nil
}

// ValidateArtifactName: các đoạn [A-Za-z0-9._@=+-] nối bằng "/", không có "." hay "..".
func ValidateArtifactName(name string) error {
	if name == "" {
		return fmt.Errorf("empty artifact name")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "." || seg == ".." || !segmentRe.MatchString(seg) {
			return fmt.Errorf("invalid artifact name %q", name)
		}
	}
	return nil
}

// ArtifactSuffix: ".gz" khi nén, ".age" khi mã hóa.
func ArtifactSuffix(compressed, encrypted bool) string {
	s := ""
	if compressed {
		s += ".gz"
	}
	if encrypted {
		s += ".age"
	}
	return s
}

func SnapshotDir(prefix, job, id string) string { return path.Join(prefix, job, id) }
func ManifestKey(prefix, job, id string) string { return path.Join(prefix, job, id, "manifest.json") }
func LockKey(prefix, job string) string         { return path.Join(prefix, "locks", job+".lock") }
func ArtifactKey(prefix, job, id, name, suffix string) string {
	return path.Join(prefix, job, id, "artifacts", name+suffix)
}
