package s3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"cistern/internal/driver"
	s3c "cistern/internal/s3"
)

const (
	indexArtifact = "index.json"
	indexFormat   = 1
)

var knownParams = map[string]bool{
	"endpoint": true, "region": true, "bucket": true, "prefix": true, "path_style": true,
	"access_key_id": true, "secret_access_key": true, "session_token": true,
	"ca_file": true, "insecure_skip_verify": true, "part_size_mb": true, "full": true,
	"restore_bucket": true, "restore_prefix": true, "only": true, "overwrite": true,
}

func init() {
	driver.Register("s3", func(p driver.Params) (driver.Driver, error) { return New(p) })
}

type Entry struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"last_modified"`
	ContentType  string    `json:"content_type,omitempty"`
	Artifact     string    `json:"artifact"`
	Snapshot     string    `json:"snapshot,omitempty"`
}

type Index struct {
	Format   int     `json:"format"`
	Endpoint string  `json:"endpoint"`
	Bucket   string  `json:"bucket"`
	Prefix   string  `json:"prefix"`
	Objects  []Entry `json:"objects"`
}

type Driver struct {
	p                                   driver.Params
	endpoint, bucket, prefix            string
	full, overwrite, pathStyle, noVerif bool
	partSizeMB                          int
}

func New(p driver.Params) (*Driver, error) {
	var unknown []string
	for k := range p {
		if !knownParams[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("s3: unknown params: %s", strings.Join(unknown, ", "))
	}
	d := &Driver{p: p, endpoint: p["endpoint"], bucket: p["bucket"], prefix: p["prefix"], partSizeMB: 16}
	if d.endpoint == "" || d.bucket == "" {
		return nil, errors.New("s3: params endpoint and bucket are required")
	}
	var err error
	for _, b := range []struct {
		key string
		dst *bool
	}{{"full", &d.full}, {"overwrite", &d.overwrite}, {"path_style", &d.pathStyle}, {"insecure_skip_verify", &d.noVerif}} {
		if v := p[b.key]; v != "" {
			if *b.dst, err = strconv.ParseBool(v); err != nil {
				return nil, fmt.Errorf("s3: param %s: %w", b.key, err)
			}
		}
	}
	if v := p["part_size_mb"]; v != "" {
		if d.partSizeMB, err = strconv.Atoi(v); err != nil || d.partSizeMB < 5 {
			return nil, fmt.Errorf("s3: param part_size_mb must be an integer >= 5, got %q", v)
		}
	}
	return d, nil
}

func (d *Driver) Name() string { return "s3" }

func (d *Driver) client(bucket string) (*s3c.Client, error) {
	cred := s3c.Credentials{AccessKeyID: d.p["access_key_id"], SecretAccessKey: d.p["secret_access_key"], SessionToken: d.p["session_token"]}
	if cred.AccessKeyID == "" && cred.SecretAccessKey == "" {
		cred = s3c.Credentials{AccessKeyID: os.Getenv("AWS_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"), SessionToken: os.Getenv("AWS_SESSION_TOKEN")}
	}
	return s3c.New(s3c.Config{
		Endpoint: d.endpoint, Region: d.p["region"], Bucket: bucket, PathStyle: d.pathStyle,
		Credentials: cred, PartSize: int64(d.partSizeMB) << 20,
		CAFile: d.p["ca_file"], InsecureSkipVerify: d.noVerif,
	})
}

func artifactName(key string) string {
	h := sha256.Sum256([]byte(key))
	x := hex.EncodeToString(h[:])
	return "objects/" + x[:2] + "/" + x[2:]
}

func (d *Driver) Check(ctx context.Context) error {
	c, err := d.client(d.bucket)
	if err != nil {
		return err
	}
	return c.Probe(ctx, d.prefix)
}

func (d *Driver) previous(sink driver.Sink) (map[string]Entry, string, string) {
	if d.full {
		return nil, "", "full (full=true)"
	}
	src := sink.Previous()
	if src == nil {
		return nil, "", "full (no readable previous snapshot)"
	}
	snap, ok := src.(driver.Snapshotted)
	if !ok {
		return nil, "", "full (previous snapshot has no ID)"
	}
	rc, err := src.Open(indexArtifact)
	if err != nil {
		return nil, "", "full (previous index: " + err.Error() + ")"
	}
	var idx Index
	err = json.NewDecoder(rc).Decode(&idx)
	if cerr := rc.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, "", "full (previous index: " + err.Error() + ")"
	}
	if idx.Format != indexFormat || idx.Endpoint != d.endpoint || idx.Bucket != d.bucket || idx.Prefix != d.prefix {
		return nil, "", "full (source changed since previous snapshot)"
	}
	m := make(map[string]Entry, len(idx.Objects))
	for _, e := range idx.Objects {
		if e.Snapshot == "" {
			e.Snapshot = snap.SnapshotID()
		}
		m[e.Key] = e
	}
	return m, snap.SnapshotID(), "incremental from " + snap.SnapshotID()
}

func (d *Driver) Backup(ctx context.Context, sink driver.Sink) (driver.Info, error) {
	c, err := d.client(d.bucket)
	if err != nil {
		return driver.Info{}, err
	}
	objs, err := c.ListObjectsInfo(ctx, d.prefix)
	if err != nil {
		return driver.Info{}, err
	}
	prev, _, mode := d.previous(sink)
	idx := Index{Format: indexFormat, Endpoint: d.endpoint, Bucket: d.bucket, Prefix: d.prefix, Objects: []Entry{}}
	deps := map[string]bool{}
	var copied, reused int
	var copiedBytes int64
	for _, o := range objs {
		if e, ok := prev[o.Key]; ok && e.ETag == o.ETag && e.Size == o.Size {
			idx.Objects = append(idx.Objects, e)
			deps[e.Snapshot] = true
			reused++
			continue
		}
		e, err := d.copyObject(ctx, c, sink, o)
		if err != nil {
			return driver.Info{}, err
		}
		idx.Objects = append(idx.Objects, e)
		copied++
		copiedBytes += o.Size
	}

	w, err := sink.Create(indexArtifact)
	if err != nil {
		return driver.Info{}, err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	if err := enc.Encode(idx); err != nil {
		return driver.Info{}, err
	}
	if err := w.Close(); err != nil {
		return driver.Info{}, err
	}
	var dependsOn []string
	for id := range deps {
		dependsOn = append(dependsOn, id)
	}
	sort.Strings(dependsOn)
	return driver.Info{
		Host: d.endpoint + "/" + d.bucket + "/" + d.prefix,
		Extra: map[string]string{
			"objects": strconv.Itoa(len(objs)), "copied": strconv.Itoa(copied), "reused": strconv.Itoa(reused),
			"copied_bytes": strconv.FormatInt(copiedBytes, 10), "mode": mode,
		},
		DependsOn: dependsOn,
	}, nil
}

func (d *Driver) copyObject(ctx context.Context, c *s3c.Client, sink driver.Sink, o s3c.ObjectInfo) (Entry, error) {

	rc, meta, err := c.GetObjectWith(ctx, o.Key, s3c.GetOptions{IfMatch: o.ETag})
	if errors.Is(err, s3c.ErrPreconditionFailed) {
		return Entry{}, fmt.Errorf("object %q changed during backup; run the job again", o.Key)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("object %q: %w", o.Key, err)
	}
	defer rc.Close()
	e := Entry{Key: o.Key, Size: o.Size, ETag: o.ETag, LastModified: o.LastModified, ContentType: meta.ContentType, Artifact: artifactName(o.Key)}
	w, err := sink.Create(e.Artifact)
	if err != nil {
		return Entry{}, err
	}
	n, err := io.Copy(w, rc)
	if err != nil {
		return Entry{}, fmt.Errorf("object %q: %w", o.Key, err)
	}
	if n != o.Size {
		return Entry{}, fmt.Errorf("object %q changed during backup (%d bytes listed, %d read); run the job again", o.Key, o.Size, n)
	}
	return e, w.Close()
}

func readIndex(src driver.Source) (*Index, error) {
	rc, err := src.Open(indexArtifact)
	if err != nil {
		return nil, err
	}
	var idx Index
	err = json.NewDecoder(rc).Decode(&idx)
	if err != nil {
		driver.Abort(rc)
		return nil, fmt.Errorf("index.json: %w", err)
	}
	if err := rc.Close(); err != nil {
		return nil, err
	}
	if idx.Format != indexFormat {
		return nil, fmt.Errorf("index.json: unsupported format %d", idx.Format)
	}
	return &idx, nil
}

func (d *Driver) Restore(ctx context.Context, src driver.Source) error {
	idx, err := readIndex(src)
	if err != nil {
		return err
	}
	bucket := d.p["restore_bucket"]
	if bucket == "" {
		bucket = idx.Bucket
	}
	restorePrefix, hasRestorePrefix := d.p["restore_prefix"]
	targetKey := func(key string) string {
		if hasRestorePrefix {
			return restorePrefix + strings.TrimPrefix(key, idx.Prefix)
		}
		return key
	}
	var sel []Entry
	for _, e := range idx.Objects {
		if strings.HasPrefix(e.Key, d.p["only"]) {
			sel = append(sel, e)
		}
	}
	if len(sel) == 0 {
		return fmt.Errorf("nothing to restore: no object in the snapshot matches only=%q", d.p["only"])
	}
	c, err := d.client(bucket)
	if err != nil {
		return err
	}
	if !d.overwrite {
		existing, err := c.ListObjects(ctx, targetKey(idx.Prefix))
		if err != nil {
			return err
		}
		have := make(map[string]bool, len(existing))
		for _, k := range existing {
			have[k] = true
		}
		var clash []string
		for _, e := range sel {
			if have[targetKey(e.Key)] {
				clash = append(clash, targetKey(e.Key))
			}
		}
		if n := len(clash); n > 0 {
			if n > 3 {
				clash = append(clash[:3], "…")
			}
			return fmt.Errorf("%d object(s) already exist in s3://%s (%s): restore under another prefix (--set restore_prefix=restored/) or pass --set overwrite=true",
				n, bucket, strings.Join(clash, ", "))
		}
	}

	return d.restoreObjects(ctx, c, src, sel, targetKey, nil)
}

func (d *Driver) restoreObjects(ctx context.Context, c *s3c.Client, src driver.Source, sel []Entry, targetKey func(string) string, n *atomic.Int64) error {
	sources := map[string]driver.Source{}
	open := func(e Entry) (io.ReadCloser, error) {
		s := src
		if e.Snapshot != "" {
			if cached, ok := sources[e.Snapshot]; ok {
				s = cached
			} else {
				r, ok := src.(driver.Resolver)
				if !ok {
					return nil, fmt.Errorf("object %q is stored in snapshot %s, which this source cannot open", e.Key, e.Snapshot)
				}
				var err error
				if s, err = r.Snapshot(e.Snapshot); err != nil {
					return nil, err
				}
				sources[e.Snapshot] = s
			}
		}
		return s.Open(e.Artifact)
	}
	for _, e := range sel {
		rc, err := open(e)
		if err != nil {
			return fmt.Errorf("object %q: %w", e.Key, err)
		}
		var in io.Reader = rc
		if n != nil {
			in = driver.CountingReader{R: rc, N: n}
		}
		if err := c.Upload(ctx, targetKey(e.Key), in, s3c.PutOptions{ContentType: e.ContentType}); err != nil {
			driver.Abort(rc)
			return fmt.Errorf("object %q: %w", e.Key, err)
		}
		if err := rc.Close(); err != nil {
			return fmt.Errorf("object %q: %w", e.Key, err)
		}
	}
	return nil
}
