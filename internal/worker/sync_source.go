package worker

import (
	"encoding/json"
	"github.com/Self-Command/paca-plugin-task-checkin/internal/model"
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
	writeJSON(out, 200, map[string]any{"connection_id": d.Connection, "statuses": states.Items})
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
		fail(out, 404, "source unavailable")
		return
	}
	var source map[string]any
	if json.Unmarshal(raw, &source) != nil || source["connection_id"] != d.Connection {
		fail(out, 404, "source not associated with device")
		return
	}
	writeJSON(out, 200, source)
}
