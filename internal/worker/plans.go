package worker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Self-Command/paca-plugin-task-checkin/internal/model"
	"github.com/jackc/pgx/v5"
	"net/url"
	"time"
)

const instanceColumns = "id::text,project_id::text,task_id::text,revision,title,fingerprint,source,start_at,due_at,start_minutes,due_minutes,freeze_at,state,status_revision"

type rowScanner interface{ Scan(...any) error }

func scanInstance(row rowScanner) (model.Instance, error) {
	var i model.Instance
	err := row.Scan(&i.ID, &i.Project, &i.Task, &i.Revision, &i.Title, &i.Fingerprint, &i.Source, &i.Start, &i.Due, &i.StartMinutes, &i.DueMinutes, &i.Freeze, &i.State, &i.StatusRevision)
	return i, err
}
func (w *Worker) config(ctx context.Context, project string) (model.Config, error) {
	var raw []byte
	err := w.DB.QueryRow(ctx, "SELECT config FROM project_settings WHERE project_id=$1", project).Scan(&raw)
	var c model.Config
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c)
	if err == nil && (!c.Valid() || !c.Enabled) {
		err = errors.New("check-in is disabled or needs configuration")
	}
	return c, err
}
func (w *Worker) prepare(ctx context.Context, project, taskID string) (model.Instance, error) {
	c, err := w.config(ctx, project)
	if err != nil {
		return model.Instance{}, err
	}
	var task model.Task
	if err = w.call(ctx, "GET", "/projects/"+project+"/tasks/"+taskID, nil, &task); err != nil {
		return model.Instance{}, err
	}
	var statuses struct {
		Items []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
		} `json:"items"`
	}
	if err = w.call(ctx, "GET", "/projects/"+project+"/task-statuses", nil, &statuses); err != nil {
		return model.Instance{}, err
	}
	valid := map[string]bool{}
	categories := map[string]string{}
	done := false
	for _, status := range statuses.Items {
		valid[status.ID] = true
		categories[status.ID] = status.Category
		if status.ID == task.Status && status.Category == "done" {
			done = true
		}
	}
	if !valid[c.ProgressStatus] || !valid[c.DoneStatus] || !valid[c.ArchiveStatus] || categories[c.ProgressStatus] != "inprogress" || categories[c.DoneStatus] != "done" || categories[c.ArchiveStatus] != "done" {
		return model.Instance{}, errors.New("configure project in-progress, done and archive states with matching categories")
	}
	meta, _ := task.Custom["_integration_state_v1"].(map[string]any)
	blocked := task.Status == c.ArchiveStatus || meta["archived"] == true || meta["recurring"] == true
	start, due := model.Precise(task, "start"), model.Precise(task, "due")
	sm, dm := c.StartMinutes, c.DueMinutes
	if v, ok := meta["reminder_start_minutes"].(float64); ok {
		sm = int(v)
	}
	if v, ok := meta["reminder_due_minutes"].(float64); ok {
		dm = int(v)
	}
	var rawRule []byte
	var ruleBinding string
	var ruleRevision int
	err = w.DB.QueryRow(ctx, "SELECT config,base_fingerprint,revision FROM task_rules WHERE project_id=$1 AND task_id=$2", project, taskID).Scan(&rawRule, &ruleBinding, &ruleRevision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return model.Instance{}, err
	}
	if len(rawRule) > 0 {
		var savedRule model.Rule
		_ = json.Unmarshal(rawRule, &savedRule)
		legacy := savedRule.Start != nil || savedRule.Due != nil || savedRule.StartMinutes != nil || savedRule.DueMinutes != nil
		fingerprint := model.TaskFingerprint(task)
		if ruleBinding == "" {
			if _, err = w.DB.Exec(ctx, "UPDATE task_rules SET base_fingerprint=$3 WHERE project_id=$1 AND task_id=$2 AND base_fingerprint=''", project, taskID, fingerprint); err != nil {
				return model.Instance{}, err
			}
		} else if ruleBinding != fingerprint && legacy {
			_, _ = w.DB.Exec(ctx, "UPDATE instances SET state=CASE WHEN freeze_at<=clock_timestamp() THEN 'conflict' ELSE 'cancelled' END,last_error='source time changed; reconfirm exact time' WHERE project_id=$1 AND task_id=$2 AND state IN('active','frozen','conflict')", project, taskID)
			return model.Instance{}, errors.New("source time changed; reconfirm exact time")
		}
		var rule model.Rule
		if json.Unmarshal(rawRule, &rule) != nil {
			return model.Instance{}, errors.New("invalid task rule")
		}
		if !rule.Enabled {
			blocked = true
		}
		if rule.Start != nil || rule.Due != nil || rule.StartMinutes != nil || rule.DueMinutes != nil {
			task, err = w.migrateRuleTimes(ctx, project, task, rule, ruleRevision)
			if err != nil {
				return model.Instance{}, err
			}
			start, due = model.Precise(task, "start"), model.Precise(task, "due")
			meta, _ = task.Custom["_integration_state_v1"].(map[string]any)
			if v, ok := meta["reminder_start_minutes"].(float64); ok {
				sm = int(v)
			}
			if v, ok := meta["reminder_due_minutes"].(float64); ok {
				dm = int(v)
			}
			rule.StartMinutes = nil
			rule.DueMinutes = nil
		}
		if rule.StartMinutes != nil {
			sm = *rule.StartMinutes
		}
		if rule.DueMinutes != nil {
			dm = *rule.DueMinutes
		}
	}
	source := json.RawMessage(`{}`)
	if meta["source"] == "tasknotes" || meta["source"] == "task-sync" {
		source, err = w.source(ctx, project, taskID)
		if err != nil {
			return model.Instance{}, err
		}
	}
 if meta["source"]!="tasknotes"&&meta["source"]!="task-sync" {
 if linked,lookupErr:=w.source(ctx,project,taskID);lookupErr==nil {source=linked}
 }
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return model.Instance{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", project+":"+taskID); err != nil {
		return model.Instance{}, err
	}
	var latestRule int
	checkErr := tx.QueryRow(ctx, "SELECT revision FROM task_rules WHERE project_id=$1 AND task_id=$2", project, taskID).Scan(&latestRule)
	if checkErr != nil && !errors.Is(checkErr, pgx.ErrNoRows) {
		return model.Instance{}, checkErr
	}
	if latestRule != ruleRevision {
		return model.Instance{}, errors.New("rule changed while planning; retry")
	}
	var now time.Time
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return model.Instance{}, err
	}
	current, err := scanInstance(tx.QueryRow(ctx, "SELECT "+instanceColumns+" FROM instances WHERE project_id=$1 AND task_id=$2 AND state IN('active','frozen','conflict') FOR UPDATE", project, taskID))
	hasCurrent := err == nil
 if hasCurrent&&string(source)=="{}"&&len(current.Source)>2{source=current.Source}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return current, err
	}
	var endSucceeded bool
	if hasCurrent {
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM records WHERE instance_id=$1 AND kind='due')", current.ID).Scan(&endSucceeded); err != nil {
			return current, err
		}
	}
	if done && !endSucceeded {
		blocked = true
	}
	if blocked || start == nil && due == nil {
		if hasCurrent {
			if _, err = tx.Exec(ctx, "UPDATE instances SET state='cancelled',last_error='task cancelled, archived, completed or time missing' WHERE id=$1", current.ID); err != nil {
				return current, err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return current, err
		}
		return model.Instance{}, errors.New("task is not eligible for check-in")
	}
	fingerprint := model.Hash(map[string]any{"start": start, "due": due, "start_minutes": sm, "due_minutes": dm})
	if hasCurrent && current.Fingerprint == fingerprint {
		if current.State == "conflict" {
			return current, errors.New("schedule conflict requires cancellation")
		}
		if _, err = tx.Exec(ctx, "UPDATE instances SET title=$2,source=$3::jsonb,state=CASE WHEN freeze_at<=clock_timestamp() THEN 'frozen' ELSE state END WHERE id=$1", current.ID, task.Title, string(source)); err != nil {
			return current, err
		}
		current.Title = task.Title
		current.Source = source
		return current, tx.Commit(ctx)
	}
	if hasCurrent && !now.Before(current.Freeze) {
		_, err = tx.Exec(ctx, "UPDATE instances SET state='conflict',last_error='time changed after a window opened; cancel explicitly' WHERE id=$1", current.ID)
		if err != nil {
			return current, err
		}
		if err = tx.Commit(ctx); err != nil {
			return current, err
		}
		return current, errors.New("time frozen; cancel before rescheduling")
	}
	if hasCurrent {
		if _, err = tx.Exec(ctx, "UPDATE instances SET state='cancelled' WHERE id=$1", current.ID); err != nil {
			return current, err
		}
	}
	revision := 0
	if err = tx.QueryRow(ctx, "SELECT COALESCE(MAX(revision),0)+1 FROM instances WHERE project_id=$1 AND task_id=$2", project, taskID).Scan(&revision); err != nil {
		return current, err
	}
	id, err := randomID()
	if err != nil {
		return current, err
	}
	freeze := now
	if start != nil {
		freeze = start.Add(-time.Duration(sm) * time.Minute)
	} else {
		freeze = due.Add(-time.Duration(dm) * time.Minute)
	}
	if due != nil && due.Add(-time.Duration(dm)*time.Minute).Before(freeze) {
		freeze = due.Add(-time.Duration(dm) * time.Minute)
	}
	i := model.Instance{ID: id, Project: project, Task: taskID, Revision: revision, Fingerprint: fingerprint, Title: task.Title, Source: source, Start: start, Due: due, StartMinutes: sm, DueMinutes: dm, Freeze: freeze, State: "active"}
	_, err = tx.Exec(ctx, "INSERT INTO instances(id,project_id,task_id,revision,fingerprint,title,source,start_at,due_at,start_minutes,due_minutes,freeze_at) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11,$12)", id, project, taskID, revision, fingerprint, task.Title, string(source), start, due, sm, dm, freeze)
	if err != nil {
		return i, err
	}
	return i, tx.Commit(ctx)
}
func (w *Worker) Tick(ctx context.Context) error {
	if err := w.control(ctx); err != nil {
		return err
	}
	rows, err := w.DB.Query(ctx, "SELECT project_id::text FROM project_settings WHERE config->>'enabled'='true' AND (needs_reconcile OR last_reconciled IS NULL OR last_reconciled<NOW()-INTERVAL '60 seconds') LIMIT 5")
	if err != nil {
		return err
	}
	projects := []string{}
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		projects = append(projects, p)
	}
	rows.Close()
	for _, project := range projects {
		cursor := ""
		seen := map[string]bool{}
		failed := false
		for page := 0; page < 10000; page++ {
			var list struct {
				Items []model.Task `json:"items"`
				Next  *string      `json:"next_cursor"`
			}
			path := "/projects/" + project + "/tasks?page_size=200"
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			if err = w.call(ctx, "GET", path, nil, &list); err != nil {
				failed = true
				break
			}
			for _, task := range list.Items {
				seen[task.ID] = true
				_, _ = w.prepare(ctx, project, task.ID)
			}
			if list.Next == nil || *list.Next == "" {
				break
			}
			if *list.Next == cursor {
				failed = true
				break
			}
			cursor = *list.Next
			if page == 9999 {
				failed = true
			}
		}
		if !failed {
			ids := []string{}
			for id := range seen {
				ids = append(ids, id)
			}
			_, _ = w.DB.Exec(ctx, "UPDATE instances SET state='cancelled',last_error='task no longer exists' WHERE project_id=$1 AND NOT(task_id::text=ANY($2::text[])) AND state IN('active','frozen','conflict')", project, ids)
			_, _ = w.DB.Exec(ctx, "UPDATE project_settings SET last_reconciled=NOW(),needs_reconcile=FALSE WHERE project_id=$1", project)
		}
	}
	return w.dispatchStatus(ctx)
}
func (w *Worker) dispatchStatus(ctx context.Context) error {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	var instance, project, task, status string
	var revision int
	err = tx.QueryRow(ctx, "SELECT o.id,o.instance_id::text,i.project_id::text,i.task_id::text,o.status_id::text,o.revision FROM outbox o JOIN instances i ON i.id=o.instance_id WHERE o.state='pending' AND o.next_attempt<=NOW() ORDER BY o.id FOR UPDATE OF o SKIP LOCKED LIMIT 1").Scan(&id, &instance, &project, &task, &status, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var latest int
	var state string
	if err = tx.QueryRow(ctx, "SELECT status_revision,state FROM instances WHERE id=$1 FOR UPDATE", instance).Scan(&latest, &state); err != nil {
		return err
	}
	if revision < latest || state == "cancelled" || state == "conflict" {
		_, err = tx.Exec(ctx, "UPDATE outbox SET state='superseded' WHERE id=$1", id)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	// Read fresh core state before a retry, preserving unrelated custom fields.
	cfg, configErr := w.config(ctx, project)
	if configErr != nil {
		return configErr
	}
	var fresh model.Task
	if err = w.call(ctx, "GET", "/projects/"+project+"/tasks/"+task, nil, &fresh); err != nil {
		return err
	}
	meta, _ := fresh.Custom["_integration_state_v1"].(map[string]any)
	if fresh.Status == cfg.ArchiveStatus || meta["archived"] == true {
		_, err = tx.Exec(ctx, "UPDATE instances SET state='cancelled' WHERE id=$1", instance)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE outbox SET state='superseded' WHERE id=$1", id)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var hasEnd bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM records WHERE instance_id=$1 AND kind='due')", instance).Scan(&hasEnd); err != nil {
		return err
	}
	if fresh.Custom == nil {
		fresh.Custom = map[string]any{}
	}
	fresh.Custom["_checkin_state_v1"] = map[string]any{"v": 1, "instance_id": instance, "status_revision": revision, "end_succeeded": hasEnd, "status_id": status}
	// Serialize per-instance status operations so a late start cannot overwrite a completed end.
	if err = w.call(ctx, "PATCH", "/projects/"+project+"/tasks/"+task, map[string]any{"status_id": status, "custom_fields": map[string]any{"_checkin_state_v1": fresh.Custom["_checkin_state_v1"]}}, nil); err != nil {
		_, updateErr := tx.Exec(ctx, "UPDATE outbox SET attempts=attempts+1,next_attempt=NOW()+INTERVAL '30 seconds',last_error='task status API unavailable' WHERE id=$1", id)
		if updateErr != nil {
			return updateErr
		}
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, "UPDATE outbox SET state='applied',last_error='' WHERE id=$1", id); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE records SET status='synced' WHERE instance_id=$1", instance)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
