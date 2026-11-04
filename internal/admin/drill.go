package admin

import (
	"sort"
	"time"
)

const (
	CheckPassed  = "passed"
	CheckFailed  = "failed"
	CheckSkipped = "skipped"
)

const (
	KindRestore = "restore"
	KindTables  = "tables"
	KindRows    = "rows"
	KindSQL     = "sql"
	KindObjects = "objects"
	KindCleanup = "cleanup"
)

const MaxDrillTables = 500

type DrillCheck struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Message  string `json:"message,omitempty"`
}

type DrillTable struct {
	Database string `json:"database"`
	Name     string `json:"name"`
	Rows     int64  `json:"rows"`
}

type DrillReport struct {
	Target        string       `json:"target,omitempty"`
	RestoredBytes int64        `json:"restored_bytes"`
	Checks        []DrillCheck `json:"checks"`
	Tables        []DrillTable `json:"tables,omitempty"`
	TotalTables   int          `json:"total_tables"`
	TotalRows     int64        `json:"total_rows"`
	CleanedUp     bool         `json:"cleaned_up"`
}

func (r *DrillReport) Add(c DrillCheck) {
	if len(c.Message) > 1000 {
		c.Message = c.Message[:1000] + "…"
	}
	r.Checks = append(r.Checks, c)
}

func (r *DrillReport) FirstFailure() *DrillCheck {
	if r == nil {
		return nil
	}
	for i := range r.Checks {
		if r.Checks[i].Status == CheckFailed {
			return &r.Checks[i]
		}
	}
	return nil
}

func (r *DrillReport) Passed() bool {
	if r == nil || len(r.Checks) == 0 {
		return false
	}
	return r.FirstFailure() == nil
}

func (r *DrillReport) AddTables(t []DrillTable) {
	r.TotalTables += len(t)
	for _, tb := range t {
		r.TotalRows += tb.Rows
	}
	r.Tables = append(r.Tables, t...)
	sort.SliceStable(r.Tables, func(i, j int) bool {
		if r.Tables[i].Rows != r.Tables[j].Rows {
			return r.Tables[i].Rows > r.Tables[j].Rows
		}
		if r.Tables[i].Database != r.Tables[j].Database {
			return r.Tables[i].Database < r.Tables[j].Database
		}
		return r.Tables[i].Name < r.Tables[j].Name
	})
	if len(r.Tables) > MaxDrillTables {
		r.Tables = r.Tables[:MaxDrillTables]
	}
}

type DrillResult struct {
	RunID         string       `json:"run_id"`
	Job           string       `json:"job"`
	Snapshot      string       `json:"snapshot"`
	Target        string       `json:"target"`
	State         string       `json:"state"`
	Trigger       string       `json:"trigger,omitempty"`
	RequestedBy   *Actor       `json:"requested_by,omitempty"`
	StartedAt     time.Time    `json:"started_at"`
	FinishedAt    *time.Time   `json:"finished_at,omitempty"`
	DurationMS    *int64       `json:"duration_ms,omitempty"`
	RestoredBytes int64        `json:"restored_bytes"`
	Checks        []DrillCheck `json:"checks"`
	Tables        []DrillTable `json:"tables,omitempty"`
	TotalTables   int          `json:"total_tables"`
	TotalRows     int64        `json:"total_rows"`
	CleanedUp     bool         `json:"cleaned_up"`
	Error         string       `json:"error,omitempty"`
}

func DrillResultOf(run Run, rep *DrillReport) *DrillResult {
	out := &DrillResult{
		RunID: run.ID, Job: run.Job, Snapshot: run.Snapshot, Target: run.Target, State: run.State, Trigger: run.Trigger,
		RequestedBy: run.RequestedBy, FinishedAt: run.FinishedAt, DurationMS: run.DurationMS, Error: run.Error, Checks: []DrillCheck{},
	}
	switch {
	case run.StartedAt != nil:
		out.StartedAt = *run.StartedAt
	case run.QueuedAt != nil:
		out.StartedAt = *run.QueuedAt
	}
	if rep != nil {
		if out.Target == "" {
			out.Target = rep.Target
		}
		out.RestoredBytes, out.Tables, out.CleanedUp = rep.RestoredBytes, rep.Tables, rep.CleanedUp
		out.TotalTables, out.TotalRows = rep.TotalTables, rep.TotalRows
		if rep.Checks != nil {
			out.Checks = rep.Checks
		}
	}
	return out
}

type PruneSummary struct {
	Kept              int   `json:"kept"`
	Removed           int   `json:"removed"`
	Errors            int   `json:"errors"`
	FreedBytes        int64 `json:"freed_bytes"`
	RunRecordsRemoved int   `json:"run_records_removed"`
}
