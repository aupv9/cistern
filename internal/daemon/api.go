package daemon

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"cistern/internal/admin"
)

type apiError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string, fields map[string]string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg, Fields: fields}})
}

func tokenOK(r *http.Request, token string) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func (d *daemonState) internalAPI(mux *http.ServeMux, status http.HandlerFunc) {
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !tokenOK(r, d.opt.APIToken) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeError(w, http.StatusUnauthorized, "unauthenticated", "missing or wrong bearer token", nil)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /status", auth(status))
	mux.HandleFunc("GET /internal/v1/status", auth(status))
	mux.HandleFunc("POST /internal/v1/runs", auth(d.startRun))
	mux.HandleFunc("GET /internal/v1/runs", auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"runs": d.reg.list(r.URL.Query().Get("active") == "true")})
	}))
	mux.HandleFunc("GET /internal/v1/runs/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		run, ok := d.reg.get(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "run not found", nil)
			return
		}
		writeJSON(w, http.StatusOK, run)
	}))
}

func (d *daemonState) startRun(w http.ResponseWriter, r *http.Request) {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid", "body must be application/json", nil)
		return
	}
	var req RunRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "invalid JSON body", nil)
		return
	}
	switch req.Op {
	case admin.OpBackup, admin.OpVerify, admin.OpRestore, admin.OpDrill, admin.OpPrune:
	default:
		writeError(w, http.StatusUnprocessableEntity, "invalid", "op must be backup, verify, restore, drill or prune", map[string]string{"op": "must be backup, verify, restore, drill or prune"})
		return
	}
	if req.RunID != "" && !admin.ValidID(req.RunID) {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "invalid run_id", map[string]string{"run_id": "invalid format"})
		return
	}
	req.Trigger = admin.TriggerUI
	if d.opt.Prepare != nil {
		if err := d.opt.Prepare(&req); err != nil {
			var inv *InvalidError
			switch {
			case errors.Is(err, ErrNotFound):
				writeError(w, http.StatusNotFound, "not_found", err.Error(), nil)
			case errors.As(err, &inv):
				writeError(w, http.StatusUnprocessableEntity, "invalid", inv.Message, inv.Fields)
			default:
				writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error(), nil)
			}
			return
		}
	}
	e, err := d.reg.begin(&req)
	if err != nil {
		var inv *InvalidError
		switch {
		case errors.Is(err, ErrBusy):
			writeError(w, http.StatusConflict, "conflict", err.Error(), nil)
		case errors.Is(err, ErrClosed):
			writeError(w, http.StatusServiceUnavailable, "daemon_unavailable", err.Error(), nil)
		case errors.As(err, &inv):
			writeError(w, http.StatusConflict, "conflict", inv.Message, inv.Fields)
		default:
			writeError(w, http.StatusInternalServerError, "internal", "could not start the run", nil)
		}
		return
	}
	d.opt.Log.Info("run accepted from the admin server", "run", req.RunID, "job", req.Job, "op", req.Op, "requested_by", actorID(req.RequestedBy), "approval", req.ApprovalID)
	go d.reg.execute(d.jobCtx, e, &req)
	writeJSON(w, http.StatusAccepted, map[string]string{"run_id": req.RunID})
}

func actorID(a *admin.Actor) string {
	if a == nil {
		return ""
	}
	return a.ID
}
