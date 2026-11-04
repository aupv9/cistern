package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"cistern/internal/admin"
	"cistern/internal/driver"
	s3c "cistern/internal/s3"
)

const markerName = ".cistern-drill"

func DrillPrefix(restorePrefix, suffix string) string {
	return restorePrefix + strings.TrimPrefix(suffix, "_") + "/"
}

func (d *Driver) Drill(ctx context.Context, src driver.Source, spec driver.DrillSpec) (*admin.DrillReport, error) {
	rep := &admin.DrillReport{Checks: []admin.DrillCheck{}}
	if spec.Suffix == "" {
		return rep, errors.New("a drill needs a restore suffix")
	}
	idx, err := readIndex(src)
	if err != nil {
		return rep, err
	}
	bucket := d.p["restore_bucket"]
	if bucket == "" {
		bucket = idx.Bucket
	}
	prefix := DrillPrefix(d.p["restore_prefix"], spec.Suffix)
	if bucket == idx.Bucket && (strings.HasPrefix(prefix, idx.Prefix) || strings.HasPrefix(idx.Prefix, prefix)) {
		return rep, fmt.Errorf("the drill prefix s3://%s/%s overlaps the backed-up prefix %q: set restore_bucket or a restore_prefix outside it on the drill target", bucket, prefix, idx.Prefix)
	}
	c, err := d.client(bucket)
	if err != nil {
		return rep, err
	}
	marker := driver.DrillMarker(spec.Job)
	markerKey := prefix + markerName

	existing, err := c.ListObjectsInfo(ctx, prefix)
	if err != nil {
		return rep, err
	}
	if len(existing) > 0 {
		ours, err := hasMarker(ctx, c, markerKey, marker)
		if err != nil {
			return rep, err
		}
		if !ours {
			rep.CleanedUp = true
			rep.Add(admin.DrillCheck{Name: admin.KindRestore, Kind: admin.KindRestore, Status: admin.CheckFailed,
				Message: fmt.Sprintf("s3://%s/%s already has %d object(s) and no drill marker of job %s; they were left untouched", bucket, prefix, len(existing), spec.Job)})
			return rep, nil
		}
		if err := deleteAll(ctx, c, existing, markerKey); err != nil {
			rep.Add(admin.DrillCheck{Name: admin.KindRestore, Kind: admin.KindRestore, Status: admin.CheckFailed, Message: "removing the previous drill copy: " + err.Error()})
			return rep, nil
		}
	}
	if err := c.PutObject(ctx, markerKey, []byte(marker+"\n"), s3c.PutOptions{ContentType: "text/plain"}); err != nil {
		return rep, fmt.Errorf("writing the drill marker: %w", err)
	}

	var sel []Entry
	var wantBytes int64
	for _, e := range idx.Objects {
		if strings.HasPrefix(e.Key, d.p["only"]) {
			sel = append(sel, e)
			wantBytes += e.Size
		}
	}
	var restored atomic.Int64
	ok := true
	if err := d.restoreObjects(ctx, c, src, sel, func(key string) string { return prefix + strings.TrimPrefix(key, idx.Prefix) }, &restored); err != nil {
		ok = false
		rep.Add(admin.DrillCheck{Name: admin.KindRestore, Kind: admin.KindRestore, Status: admin.CheckFailed, Message: err.Error()})
		rep.Add(admin.DrillCheck{Name: admin.KindObjects, Kind: admin.KindObjects, Status: admin.CheckSkipped, Message: "the restore failed"})
	} else {
		rep.Add(admin.DrillCheck{Name: admin.KindRestore, Kind: admin.KindRestore, Status: admin.CheckPassed,
			Actual: strconv.Itoa(len(sel)) + " objects", Message: "copied into s3://" + bucket + "/" + prefix})
	}
	rep.RestoredBytes = restored.Load()

	if ok {
		got, err := c.ListObjectsInfo(ctx, prefix)
		chk := admin.DrillCheck{Name: admin.KindObjects, Kind: admin.KindObjects,
			Expected: fmt.Sprintf("%d objects, %d bytes", len(sel), wantBytes)}
		if err != nil {
			chk.Status, chk.Message = admin.CheckFailed, err.Error()
		} else {
			var n int
			var size int64
			for _, o := range got {
				if o.Key == markerKey {
					continue
				}
				n++
				size += o.Size
			}
			chk.Actual = fmt.Sprintf("%d objects, %d bytes", n, size)
			chk.Status = admin.CheckPassed
			if n != len(sel) || size != wantBytes {
				chk.Status, chk.Message = admin.CheckFailed, "the drill copy differs from the snapshot's index.json"
			}
		}
		rep.Add(chk)
	}

	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	if spec.Keep {
		rep.Add(admin.DrillCheck{Name: admin.KindCleanup, Kind: admin.KindCleanup, Status: admin.CheckSkipped,
			Message: "keep: true, the drill copy stays in s3://" + bucket + "/" + prefix + " until the next drill"})
		return rep, nil
	}
	left, err := c.ListObjectsInfo(cctx, prefix)
	if err == nil {
		err = deleteAll(cctx, c, left, markerKey)
	}
	if err == nil {
		err = c.DeleteObject(cctx, markerKey)
	}
	if err != nil {
		rep.Add(admin.DrillCheck{Name: admin.KindCleanup, Kind: admin.KindCleanup, Status: admin.CheckFailed, Message: err.Error()})
		return rep, nil
	}
	rep.CleanedUp = true
	rep.Add(admin.DrillCheck{Name: admin.KindCleanup, Kind: admin.KindCleanup, Status: admin.CheckPassed,
		Message: "deleted everything under s3://" + bucket + "/" + prefix})
	return rep, nil
}

func hasMarker(ctx context.Context, c *s3c.Client, key, marker string) (bool, error) {
	rc, err := c.GetObject(ctx, key)
	if errors.Is(err, s3c.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 4096))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(data)) == marker, nil
}

func deleteAll(ctx context.Context, c *s3c.Client, objs []s3c.ObjectInfo, keep string) error {
	for _, o := range objs {
		if o.Key == keep {
			continue
		}
		if err := c.DeleteObject(ctx, o.Key); err != nil {
			return fmt.Errorf("deleting %q: %w", o.Key, err)
		}
	}
	return nil
}
