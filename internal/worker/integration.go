package worker

import (
	"crypto/subtle"
	"encoding/json"
	"github.com/Self-Command/paca-plugin-task-checkin/internal/model"
	"net/http"
	"strings"
)

func (w *Worker) internalAllowed(r *http.Request) bool {
	return w.ActionSecret != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+w.ActionSecret)) == 1
}
func (w *Worker) taskPlan(out http.ResponseWriter, r *http.Request) {
	if !w.internalAllowed(r) {
		fail(out, 401, "internal authorization required")
		return
	}
	var input struct {
		Project string `json:"project_id"`
		Task    string `json:"task_id"`
	}
	if !readJSON(out, r, &input) {
		return
	}
	if !model.UUID.MatchString(input.Project) || !model.UUID.MatchString(input.Task) {
		fail(out, 400, "invalid identity")
		return
	}
	if err := w.control(r.Context()); err != nil {
		fail(out, 503, "host unavailable")
		return
	}
	i, err := w.prepare(r.Context(), input.Project, input.Task)
	if err != nil {
        if err.Error() == "task is not eligible for check-in" {
            writeJSON(out, 200, map[string]any{"enabled": false})
            return
        }
		fail(out, 409, err.Error())
		return
	}
	var ended bool
	if err = w.DB.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM records WHERE instance_id=$1 AND kind='due')", i.ID).Scan(&ended); err != nil {
		fail(out, 503, "record unavailable")
		return
	}
	writeJSON(out, 200, map[string]any{"enabled": true, "instance_id": i.ID, "revision": i.Revision, "start": i.Start, "due": i.Due, "start_minutes": i.StartMinutes, "due_minutes": i.DueMinutes, "end_succeeded": ended, "state": i.State})
}

// A passes a candidate echo with changed fields. Only a previously registered, scoped
// receipt can authorize a status-only acknowledgement; it never authorizes time edits.
func (w *Worker) matchWriteback(out http.ResponseWriter, r *http.Request) {
	if !w.internalAllowed(r) {
		fail(out, 401, "internal authorization required")
		return
	}
	if err := w.control(r.Context()); err != nil {
		fail(out, 503, "host unavailable")
		return
	}
	var input struct {
		Source      string   `json:"source_ref"`
		Connection  string   `json:"connection_id"`
		Path        string   `json:"path"`
		Status      string   `json:"status"`
		Fields      []string `json:"changed_fields"`
		DetailsHash string   `json:"details_sha256"`
	}
	if !readJSON(out, r, &input) {
		return
	}
	for _, f := range input.Fields {
		if f != "status" && f != "details" {
			writeJSON(out, 200, map[string]any{"matched": false})
			return
		}
	}
	rows, err := w.DB.Query(r.Context(), "SELECT r.id::text,r.expected FROM receipts r JOIN devices d ON d.id=r.device_id WHERE d.enabled AND d.connection_id=$1 AND r.expected->>'source_ref'=$2 AND r.expected->>'path'=$3 AND r.expected->>'tasknotes_status'=$4 AND r.state IN('prepared','observed','confirmed') AND r.expected->>'details_sha256'=$5 AND r.created_at>NOW()-INTERVAL '7 days' AND EXISTS(SELECT 1 FROM instances i WHERE i.id::text=r.expected->>'instance_id' AND i.status_revision=(r.expected->>'revision')::integer AND i.state IN('active','frozen')) ORDER BY r.created_at DESC LIMIT 1", input.Connection, input.Source, input.Path, input.Status, input.DetailsHash)
	if err != nil {
		fail(out, 503, "receipt lookup unavailable")
		return
	}
	defer rows.Close()
	var id string
	var expected json.RawMessage
	if !rows.Next() {
		writeJSON(out, 200, map[string]any{"matched": false})
		return
	}
	if rows.Scan(&id, &expected) != nil {
		fail(out, 503, "receipt invalid")
		return
	}
	// Persist observation independently of eventual D acknowledgement, for crash recovery.
	if _, err = w.DB.Exec(r.Context(), "UPDATE receipts SET state=CASE WHEN state='prepared' THEN 'observed' ELSE state END WHERE id=$1", id); err != nil {
		fail(out, 503, "receipt observation failed")
		return
	}
	writeJSON(out, 200, map[string]any{"matched": true, "receipt_id": id, "status": strings.TrimSpace(input.Status)})
}
