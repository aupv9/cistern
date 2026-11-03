package engine

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"cistern/internal/config"
	"cistern/internal/driver"
	"cistern/internal/manifest"
	"cistern/internal/metrics"
	"cistern/internal/repo"
	"cistern/internal/retention"
)

// incompleteAge: thư mục snapshot không có manifest và cũ hơn mức này được coi là bản dở bỏ lại.
const incompleteAge = time.Hour

const (
	ActionKeep             = "keep"
	ActionRemove           = "remove"
	ActionRemoveIncomplete = "remove-incomplete"
)

type PruneItem struct {
	Snapshot   string    `json:"snapshot"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Action     string    `json:"action"`
	Reasons    []string  `json:"reasons,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type PruneReport struct {
	Job     string      `json:"job"`
	DryRun  bool        `json:"dry_run"`
	Items   []PruneItem `json:"items"`
	Kept    int         `json:"kept"`
	Removed int         `json:"removed"` // với dry_run: số snapshot sẽ bị xóa
	Errors  int         `json:"errors"`
}

// Prune áp retention cho job (docs/architecture.md#retention). dryRun chỉ báo cáo, không khóa, không xóa.
func (e *Engine) Prune(ctx context.Context, jobName string, dryRun bool) (*PruneReport, error) {
	job, err := e.Cfg.Job(jobName)
	if err != nil {
		return nil, err
	}
	start := e.now()
	rep, err := e.lockedPrune(ctx, job, dryRun)
	if dryRun {
		return rep, err
	}
	failErr := err
	if err == nil && rep.Errors > 0 {
		failErr = fmt.Errorf("%d snapshot(s) could not be removed", rep.Errors)
	}
	if failErr != nil {
		e.Metrics.Set(metrics.PruneLastFailure, metrics.Job(job.Name), unix(e.now()))
	}
	ev := e.event(job, "prune", start, failErr)
	if rep != nil {
		ev.Removed = rep.Removed
	}
	e.notifier.Send(ctx, ev)
	e.publish(ctx, job)
	return rep, err
}

func (e *Engine) lockedPrune(ctx context.Context, job *config.Job, dryRun bool) (*PruneReport, error) {
	r, prefix, err := e.repoFor(job)
	if err != nil {
		return nil, err
	}
	if !dryRun {
		unlock, err := e.lock(ctx, r, prefix, job, "prune", "")
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	return e.prune(ctx, r, prefix, job, dryRun)
}

// prune giả định đã giữ khóa job (trừ khi dryRun).
func (e *Engine) prune(ctx context.Context, r repo.Repository, prefix string, job *config.Job, dryRun bool) (*PruneReport, error) {
	log := e.Log.With("job", job.Name)
	rep := &PruneReport{Job: job.Name, DryRun: dryRun}

	// 1. Quét repository: thư mục có manifest.json là snapshot hoàn tất, không có là bản dở.
	jobDir := path.Join(prefix, job.Name) + "/"
	keys, err := r.List(ctx, jobDir)
	if err != nil {
		return nil, err
	}
	dirs := map[string][]string{}
	hasManifest := map[string]bool{}
	for _, k := range keys {
		id, rest, ok := strings.Cut(strings.TrimPrefix(k, jobDir), "/")
		if !ok || !manifest.ValidSnapshotID(id) {
			continue
		}
		dirs[id] = append(dirs[id], k)
		if rest == "manifest.json" {
			hasManifest[id] = true
		}
	}

	manifests := map[string]*manifest.Manifest{}
	var snaps []retention.Snapshot
	for id := range hasManifest {
		m, err := readManifest(ctx, r, manifest.ManifestKey(prefix, job.Name, id))
		if err != nil {
			// Không đọc được manifest: không xóa thứ mình không hiểu.
			rep.Items = append(rep.Items, PruneItem{Snapshot: id, Action: ActionKeep, Reasons: []string{"unreadable manifest"}, Error: err.Error()})
			rep.Errors++
			continue
		}
		manifests[id] = m
		snaps = append(snaps, retention.Snapshot{ID: id, FinishedAt: m.FinishedAt, DependsOn: m.DependsOn})
	}

	// 2–4. Áp GFS, giữ chuỗi depends_on, xóa phần còn lại.
	var decisions []retention.Decision
	if job.Retention == nil {
		log.Warn("no retention policy configured; keeping all snapshots")
		for _, s := range snaps {
			decisions = append(decisions, retention.Decision{Snapshot: s, Keep: true, Reasons: []string{"no retention policy"}})
		}
		sort.Slice(decisions, func(i, j int) bool { return decisions[i].FinishedAt.After(decisions[j].FinishedAt) })
	} else {
		p := job.Retention
		decisions = retention.Apply(retention.Policy{Last: p.Last, Hourly: p.Hourly, Daily: p.Daily, Weekly: p.Weekly, Monthly: p.Monthly, Yearly: p.Yearly}, snaps, e.Cfg.Location())
	}
	for _, d := range decisions {
		item := PruneItem{Snapshot: d.ID, FinishedAt: d.FinishedAt, Action: ActionKeep, Reasons: d.Reasons}
		if !d.Keep {
			item.Action = ActionRemove
			if !dryRun {
				if err := e.deleteSnapshot(ctx, r, prefix, job, manifests[d.ID], dirs[d.ID]); err != nil {
					item.Error = err.Error()
					rep.Errors++
					log.Error("removing snapshot failed", "snapshot", d.ID, "err", err)
				} else {
					log.Info("snapshot removed", "snapshot", d.ID)
				}
			}
		}
		rep.Items = append(rep.Items, item)
	}

	// 5. Bản dở (không manifest) cũ hơn 1 giờ.
	var incomplete []string
	for id := range dirs {
		if !hasManifest[id] {
			incomplete = append(incomplete, id)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(incomplete)))
	for _, id := range incomplete {
		t, err := time.Parse("20060102T150405Z", id[:16])
		if err != nil {
			continue
		}
		item := PruneItem{Snapshot: id, Action: ActionKeep, Reasons: []string{"incomplete, may still be running"}}
		if e.now().Sub(t) > incompleteAge {
			item.Action, item.Reasons = ActionRemoveIncomplete, []string{"incomplete, older than 1h"}
			if !dryRun {
				if err := deleteKeys(ctx, r, dirs[id]); err != nil {
					item.Error = err.Error()
					rep.Errors++
				}
			}
		}
		rep.Items = append(rep.Items, item)
	}

	for _, it := range rep.Items {
		switch {
		case it.Action == ActionKeep:
			rep.Kept++
		case it.Action == ActionRemove && it.Error == "":
			rep.Removed++
		case it.Action == ActionRemove:
			rep.Kept++ // xóa thất bại: snapshot vẫn còn
		}
	}

	// 6. Metrics.
	if !dryRun {
		l := metrics.Job(job.Name)
		e.Metrics.Set(metrics.Snapshots, l, float64(len(manifests)-rep.Removed))
		e.Metrics.Add(metrics.PruneRemoved, l, float64(rep.Removed))
	}
	return rep, nil
}

// deleteSnapshot: dữ liệu ngoài repository (Forgetter) trước — lỗi thì giữ snapshot; rồi manifest,
// cuối cùng artifact. Bị ngắt giữa chừng thì phần còn lại thành bản dở, lần prune sau dọn tiếp.
func (e *Engine) deleteSnapshot(ctx context.Context, r repo.Repository, prefix string, job *config.Job, m *manifest.Manifest, keys []string) error {
	drv, err := driver.New(m.Driver, driver.Params(job.Params))
	if err != nil {
		return err
	}
	if f, ok := drv.(driver.Forgetter); ok {
		info := driver.Info{Version: m.Source.Version, Host: m.Source.Host, Extra: m.Source.Extra, DependsOn: m.DependsOn}
		if err := f.Forget(ctx, info); err != nil {
			return fmt.Errorf("forget external data: %w", err)
		}
	}
	mk := manifest.ManifestKey(prefix, job.Name, m.ID)
	if err := r.Delete(ctx, mk); err != nil {
		return err
	}
	rest := make([]string, 0, len(keys))
	for _, k := range keys {
		if k != mk {
			rest = append(rest, k)
		}
	}
	return deleteKeys(ctx, r, rest)
}

func deleteKeys(ctx context.Context, r repo.Repository, keys []string) error {
	for _, k := range keys {
		if err := r.Delete(ctx, k); err != nil {
			return err
		}
	}
	return nil
}
