package worker

import (
	"encoding/json"
	"errors"
	"github.com/Self-Command/paca-plugin-task-checkin/internal/model"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (w *Worker) syncInfo(out http.ResponseWriter, r *http.Request) {
	d, err := w.device(r.Context(), r)
	if err != nil {
		fail(out, 401, "paired device required")
		return
	}
	var states struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if w.call(r.Context(), "GET", "/projects/"+d.Project+"/task-statuses", nil, &states) != nil {
		fail(out, 503, "project states unavailable")
		return
	}
	writeJSON(out, 200, map[string]any{"connection_id": d.Connection, "statuses": states.Items, "project_id": d.Project, "capabilities": []string{"checkin"}})
}
func (w *Worker) syncSource(out http.ResponseWriter, r *http.Request) {
	d, err := w.device(r.Context(), r)
	if err != nil {
		fail(out, 401, "paired device required")
		return
	}
	task := r.PathValue("task")
	if !model.UUID.MatchString(task) {
		fail(out, 400, "invalid task")
		return
	}
	raw, err := w.source(r.Context(), d.Project, task)
	if err != nil {
		var api apiError
		if !errors.As(err, &api) || api.Code != 404 {
			writeJSON(out, 503, map[string]any{"code": "unavailable", "error": "来源暂时无法读取，请稍后重试。", "retryable": true})
			return
		}
		if r.Header.Get("X-Sync-Version") != "2" {
			writeJSON(out, 404, map[string]any{"code": "source_missing", "error": "来源需要重新关联。", "retryable": false})
			return
		}
		history, historyErr := w.historicSource(r.Context(), d, task)
		if historyErr != nil {
			if !errors.Is(historyErr, pgx.ErrNoRows) {
				writeJSON(out, 503, map[string]any{"code": "unavailable", "error": "来源暂不可用，请稍后重试。", "retryable": true})
				return
			}
			writeJSON(out, 404, map[string]any{"code": "source_missing", "error": "来源需要重新关联。", "retryable": false})
			return
		}
		writeJSON(out, 200, history)
		return
	}
	var source map[string]any
	if json.Unmarshal(raw, &source) != nil || source["connection_id"] != d.Connection {
		fail(out, 404, "source not associated with device")
		return
	}
	status, statusErr := w.sourceStatus(r.Context(), d.Project, task)
	if statusErr != nil {
		writeJSON(out, 503, map[string]any{"code": "unavailable", "error": "来源暂时无法读取，请稍后重试。", "retryable": true})
		return
	}
	if status.State != "active" || status.Connection != d.Connection || status.Ref != source["source_ref"] {
		if r.Header.Get("X-Sync-Version") != "2" {
			writeJSON(out, 404, map[string]any{"code": "source_missing", "error": "来源需要重新关联。", "retryable": false})
			return
		}
		source["source_state"] = "unlinked"
		if status.State == "deleted" && status.Connection == d.Connection && status.Ref == source["source_ref"] {
			source["source_state"] = "deleted"
		}
		writeJSON(out, 200, source)
		return
	}
	source["source_state"] = "active"
	writeJSON(out, 200, source)
}
