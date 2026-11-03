package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"cistern/internal/admin"
)

func TestManifestDownloadTotalsAndNoRole(t *testing.T) {
	e := newEnv(t, withDaemon, nil)
	op := e.login("alice@example.com", "operator")
	var ref struct {
		RunID string `json:"run_id"`
	}
	op.json("POST", "/api/v1/jobs/app/backup", nil, 202, &ref)
	run := op.wait(ref.RunID)

	code, data := op.do("GET", "/api/v1/jobs/app/snapshots/"+run.Snapshot+"/manifest.json", nil)
	var m map[string]any
	if code != 200 || json.Unmarshal(data, &m) != nil || m["id"] != run.Snapshot {
		t.Fatalf("manifest download: %d %s", code, data)
	}
	op.expectError("GET", "/api/v1/jobs/app/snapshots/20990101T000000Z-abcd/manifest.json", nil, 404, codeNotFound)

	var ov overviewResponse
	op.json("GET", "/api/v1/overview", nil, 200, &ov)
	for _, j := range ov.Jobs {
		if j.Name == "app" && (len(j.RecentBackups) != 1 || j.RecentBackups[0].ID != run.ID) {
			t.Fatalf("run point id: %+v", j.RecentBackups)
		}
	}

	var page struct {
		Entries []AuditEntry `json:"entries"`
		Total   *int         `json:"total"`
	}
	op.json("GET", "/api/v1/audit?limit=1", nil, 200, &page)
	if page.Total == nil || *page.Total < 2 || len(page.Entries) != 1 {
		t.Fatalf("total: %+v", page)
	}
	op.json("GET", "/api/v1/audit?action=prune.run", nil, 200, &page)

	sess := e.srv.newSession(admin.Actor{ID: "x", Name: "x"}, []string{})
	rec := httptest.NewRecorder()
	e.srv.issueSession(rec, sess)
	cookie := strings.SplitN(strings.TrimPrefix(rec.Header().Get("Set-Cookie"), "cistern_session="), ";", 2)[0]
	u := &user{e: e, cookie: cookie, csrf: sess.CSRF}
	u.expectError("GET", "/api/v1/me", nil, 403, codeForbidden)
	u.expectError("GET", "/api/v1/overview", nil, 403, codeForbidden)
}
