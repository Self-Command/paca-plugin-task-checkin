package worker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Self-Command/paca-plugin-task-checkin/internal/model"
	"net/http"
	"strconv"
	"strings"
)

type device struct{ ID, Project, Connection string }

func (w *Worker) device(ctx context.Context, r *http.Request) (device, error) {
	if err := w.control(ctx); err != nil {
		return device{}, err
	}
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || len(strings.TrimPrefix(header, "Bearer ")) != 64 {
		return device{}, errors.New("paired integration token required")
	}
	var d device
	err := w.DB.QueryRow(ctx, "SELECT id::text,project_id::text,connection_id FROM devices WHERE token_hash=$1 AND enabled", tokenHash(strings.TrimPrefix(header, "Bearer "))).Scan(&d.ID, &d.Project, &d.Connection)
	return d, err
}
func (w *Worker) changes(out http.ResponseWriter, r *http.Request) {
	d, err := w.device(r.Context(), r)
	if err != nil {
		fail(out, 401, "valid paired device required")
		return
	}
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil || after < 0 {
		fail(out, 400, "valid cursor required")
		return
	}
	rows, err := w.DB.Query(r.Context(), "SELECT c.cursor,c.payload,m.state,i.status_revision,r.status FROM changes c JOIN records r ON r.id=c.record_id JOIN media m ON m.id=r.media_id JOIN instances i ON i.id=c.instance_id WHERE c.project_id=$1 AND c.connection_id=$2 AND c.cursor>$3 ORDER BY c.cursor LIMIT 100", d.Project, d.Connection, after)
	if err != nil {
		fail(out, 503, "sync feed unavailable")
		return
	}
	defer rows.Close()
	items := []any{}
	next := after
	for rows.Next() {
		var cursor int64
		var raw []byte
		var state, status string
		var currentRevision int
		if err = rows.Scan(&cursor, &raw, &state, &currentRevision, &status); err != nil {
			fail(out, 503, "sync row unavailable")
			return
		}
		var item map[string]any
		if json.Unmarshal(raw, &item) != nil {
			fail(out, 503, "sync record invalid")
			return
		}
		item["cursor"] = cursor
		item["media_expired"] = state == "expired"
		item["current_revision"] = currentRevision
		item["status"] = status
		items = append(items, item)
		next = cursor
	}
	if rows.Err() != nil {
		fail(out, 503, "feed read failed")
		return
	}
	writeJSON(out, 200, map[string]any{"items": items, "next_cursor": next, "has_more": len(items) == 100})
}
func (w *Worker) syncMedia(out http.ResponseWriter, r *http.Request) {
	d, err := w.device(r.Context(), r)
	if err != nil {
		fail(out, 401, "paired device required")
		return
	}
	if !model.UUID.MatchString(r.PathValue("id")) {
		fail(out, 400, "invalid media ID")
		return
	}
	var object, state string
	err = w.DB.QueryRow(r.Context(), "SELECT m.object_key,m.state FROM media m JOIN changes c ON c.instance_id=m.instance_id JOIN records r ON r.id=c.record_id AND r.media_id=m.id WHERE m.id=$1 AND c.project_id=$2 AND c.connection_id=$3 LIMIT 1", r.PathValue("id"), d.Project, d.Connection).Scan(&object, &state)
	if err != nil {
		fail(out, 404, "media not associated with this vault")
		return
	}
	if state == "expired" {
		fail(out, 410, "photo expired; check-in record remains available")
		return
	}
	if state != "committed" {
		fail(out, 404, "media unavailable")
		return
	}
	w.streamPhoto(out, r, object)
}
func (w *Worker) prepareReceipt(out http.ResponseWriter, r *http.Request) {
	d, err := w.device(r.Context(), r)
	if err != nil {
		fail(out, 401, "paired device required")
		return
	}
	var input struct {
		Cursor       int64  `json:"cursor"`
		Op           string `json:"op_id"`
		Status       string `json:"status"`
		BaseStatus   string `json:"base_status"`
		Path         string `json:"path"`
		BaseRevision int    `json:"base_revision"`
		DetailsHash  string `json:"details_sha256"`
	}
	if !readJSON(out, r, &input) {
		return
	}
	if input.Cursor < 1 || len(input.Op) < 8 || len(input.Op) > 128 || len(input.Status) > 128 || len(input.Path) > 1024 {
		fail(out, 400, "invalid receipt")
		return
	}
	var raw []byte
	var current int
	err = w.DB.QueryRow(r.Context(), "SELECT c.payload,i.status_revision FROM changes c JOIN instances i ON i.id=c.instance_id WHERE c.cursor=$1 AND c.project_id=$2 AND c.connection_id=$3", input.Cursor, d.Project, d.Connection).Scan(&raw, &current)
	if err != nil {
		fail(out, 404, "change not associated with this device")
		return
	}
	if input.BaseRevision != current {
		fail(out, 409, "newer check-in revision; pull again")
		return
	}
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	if number, ok := payload["revision"].(float64); !ok || int(number) != current {
		fail(out, 409, "stale record may sync photos only; pull latest state")
		return
	}
	source, _ := payload["source"].(map[string]any)
	expected := map[string]any{"source_ref": source["source_ref"], "tasknotes_status": input.Status, "base_status": input.BaseStatus, "path": input.Path, "revision": current, "instance_id": payload["instance_id"], "details_sha256": input.DetailsHash}
	body, _ := json.Marshal(expected)
	id, err := randomID()
	if err != nil {
		fail(out, 503, "randomness unavailable")
		return
	}
	var storedID, stored string
	err = w.DB.QueryRow(r.Context(), "INSERT INTO receipts(id,device_id,change_cursor,op_id,expected) VALUES($1,$2,$3,$4,$5::jsonb) ON CONFLICT(device_id,op_id) DO UPDATE SET op_id=receipts.op_id RETURNING id::text,expected::text", id, d.ID, input.Cursor, input.Op, string(body)).Scan(&storedID, &stored)
	if err != nil {
		fail(out, 503, "receipt save failed")
		return
	}
	var prior any
	_ = json.Unmarshal([]byte(stored), &prior)
	if model.Hash(prior) != model.Hash(expected) {
		fail(out, 409, "operation ID reused with different expected state")
		return
	}
	writeJSON(out, 200, map[string]any{"id": storedID, "state": "prepared", "expected": expected})
}
func (w *Worker) ackReceipt(out http.ResponseWriter, r *http.Request) {
	d, err := w.device(r.Context(), r)
	if err != nil {
		fail(out, 401, "paired device required")
		return
	}
	var input struct {
		Status string `json:"status"`
		Path   string `json:"path"`
	}
	if !readJSON(out, r, &input) {
		return
	}
	result, err := w.DB.Exec(r.Context(), "UPDATE receipts SET state='confirmed' WHERE id=$1 AND device_id=$2 AND expected->>'tasknotes_status'=$3 AND expected->>'path'=$4 AND state IN('prepared','observed','confirmed')", r.PathValue("id"), d.ID, input.Status, input.Path)
	if err != nil {
		fail(out, 503, "receipt confirmation failed")
		return
	}
	if result.RowsAffected() != 1 {
		fail(out, 409, "read-back differs from expected state")
		return
	}
	writeJSON(out, 200, map[string]any{"confirmed": true})
}
func (w *Worker) saveConflict(out http.ResponseWriter, r *http.Request) {
	d, err := w.device(r.Context(), r)
	if err != nil {
		fail(out, 401, "paired device required")
		return
	}
	var input struct {
		Instance string         `json:"instance_id"`
		Revision int            `json:"base_revision"`
		Local    map[string]any `json:"local"`
	}
	if !readJSON(out, r, &input) {
		return
	}
	if !model.UUID.MatchString(input.Instance) {
		fail(out, 400, "invalid instance")
		return
	}
	raw, _ := json.Marshal(input.Local)
	id, err := randomID()
	if err != nil {
		fail(out, 503, "randomness unavailable")
		return
	}
	var storedID string
	var stored []byte
	err = w.DB.QueryRow(r.Context(), "INSERT INTO conflicts(id,device_id,instance_id,base_revision,local) SELECT $1,$2,i.id,$4,$5::jsonb FROM instances i WHERE i.id=$3 AND i.project_id=$6 AND i.status_revision=$4 ON CONFLICT(device_id,instance_id,base_revision) DO UPDATE SET id=conflicts.id RETURNING id::text,local", id, d.ID, input.Instance, input.Revision, string(raw), d.Project).Scan(&storedID, &stored)
	if err != nil {
		fail(out, 409, "instance changed or conflict unavailable")
		return
	}
	var prior any
	_ = json.Unmarshal(stored, &prior)
	if model.Hash(prior) != model.Hash(input.Local) {
		fail(out, 409, "local conflict changed; refresh first")
		return
	}
	writeJSON(out, 201, map[string]any{"id": storedID})
}
func (w *Worker) resolveConflict(out http.ResponseWriter, r *http.Request) {
	d, err := w.device(r.Context(), r)
	if err != nil {
		fail(out, 401, "paired device required")
		return
	}
	var input struct {
		Keep     string `json:"keep"`
		Revision int    `json:"base_revision"`
		StatusID string `json:"paca_status_id"`
	}
	if !readJSON(out, r, &input) {
		return
	}
	if input.Keep != "server" && input.Keep != "local" {
		fail(out, 400, "choose local or server")
		return
	}
	if input.Keep == "local" && !model.UUID.MatchString(input.StatusID) {
		fail(out, 400, "explicit Paca state mapping required for local choice")
		return
	}
	if input.Keep == "local" {
		var statuses struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if w.call(r.Context(), "GET", "/projects/"+d.Project+"/task-statuses", nil, &statuses) != nil {
			fail(out, 503, "project statuses unavailable")
			return
		}
		valid := false
		for _, s := range statuses.Items {
			if s.ID == input.StatusID {
				valid = true
			}
		}
		if !valid {
			fail(out, 400, "state does not belong to this project")
			return
		}
	}
	tx, err := w.DB.Begin(r.Context())
	if err != nil {
		fail(out, 503, "database unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	var instance string
	var revision int
	var state string
	err = tx.QueryRow(r.Context(), "SELECT c.instance_id::text,i.status_revision,c.state FROM conflicts c JOIN instances i ON i.id=c.instance_id WHERE c.id=$1 AND c.device_id=$2 AND i.project_id=$3 FOR UPDATE OF c,i", r.PathValue("id"), d.ID, d.Project).Scan(&instance, &revision, &state)
	if err != nil {
		fail(out, 404, "conflict unavailable")
		return
	}
	if state != "open" {
		if state != "resolved_"+input.Keep {
			fail(out, 409, "conflict already resolved with a different choice")
			return
		}
		writeJSON(out, 200, map[string]any{"resolved": true, "keep": input.Keep, "revision": revision})
		return
	}
	if revision != input.Revision {
		fail(out, 409, "newer check-in; reload conflict")
		return
	}
	if input.Keep == "local" {
		revision++
		if _, err = tx.Exec(r.Context(), "UPDATE instances SET status_revision=$2 WHERE id=$1", instance, revision); err != nil {
			fail(out, 503, "revision update failed")
			return
		}
		if _, err = tx.Exec(r.Context(), "INSERT INTO changes(project_id,connection_id,instance_id,record_id,revision,payload) SELECT project_id,connection_id,instance_id,record_id,$2,payload||jsonb_build_object('revision',$2,'logical_status','mapped','paca_status_id',$3::text) FROM changes WHERE instance_id=$1 ORDER BY cursor DESC LIMIT 1", instance, revision, input.StatusID); err != nil {
			fail(out, 503, "resolution sync change failed")
			return
		}
		if _, err = tx.Exec(r.Context(), "INSERT INTO outbox(instance_id,revision,status_id) VALUES($1,$2,$3)", instance, revision, input.StatusID); err != nil {
			fail(out, 503, "status outbox failed")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), "UPDATE conflicts SET state=$2 WHERE id=$1", r.PathValue("id"), "resolved_"+input.Keep); err != nil {
		fail(out, 503, "conflict update failed")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		fail(out, 503, "resolution commit unknown; retry")
		return
	}
	writeJSON(out, 200, map[string]any{"resolved": true, "revision": revision, "keep": input.Keep})
}
