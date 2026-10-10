package worker

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type sourceStatus struct {
	State      string `json:"state"`
	Connection string `json:"connection_id"`
	Ref        string `json:"source_ref"`
}

func (w *Worker) sourceStatus(ctx context.Context, project, task string) (sourceStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var status sourceStatus
	err := w.call(ctx, "GET", "/plugins/com.selfcommand.tasknotes-webhook/projects/"+project+"/tasks/"+task+"/source-status", nil, &status)
	return status, err
}

func (w *Worker) classifyDeliveries(ctx context.Context) error {
	_, err := w.DB.Exec(ctx, "INSERT INTO sync_policies(project_id,connection_id,record_id) SELECT DISTINCT project_id,connection_id,record_id FROM changes WHERE connection_id<>'' ON CONFLICT DO NOTHING")
	if err != nil {
		return err
	}
	rows, err := w.DB.Query(ctx, "SELECT p.project_id::text,p.connection_id,p.record_id::text,i.task_id::text,i.source,p.reason FROM sync_policies p JOIN records r ON r.id=p.record_id JOIN instances i ON i.id=r.instance_id WHERE p.state='active' AND p.reason<>'user_recovery' AND (p.checked_at IS NULL OR p.checked_at<NOW()-INTERVAL '60 seconds') ORDER BY p.checked_at NULLS FIRST LIMIT 10")
	if err != nil {
		return err
	}
	type candidate struct {
		Project, Connection, Record, Task, Reason string
		Raw                                       []byte
	}
	items := []candidate{}
	for rows.Next() {
		var item candidate
		if err = rows.Scan(&item.Project, &item.Connection, &item.Record, &item.Task, &item.Raw, &item.Reason); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		status, lookupErr := w.sourceStatus(ctx, item.Project, item.Task)
		if lookupErr != nil {
			_, _ = w.DB.Exec(ctx, "UPDATE sync_policies SET checked_at=NOW() WHERE connection_id=$1 AND record_id=$2", item.Connection, item.Record)
			continue
		}
		var source map[string]any
		_ = json.Unmarshal(item.Raw, &source)
		ref, _ := source["source_ref"].(string)
		if status.State == "deleted" && status.Connection == item.Connection && status.Ref == ref && ref != "" {
			tx, beginErr := w.DB.Begin(ctx)
			if beginErr != nil {
				return beginErr
			}
			n, updateErr := tx.Exec(ctx, "UPDATE sync_policies SET state='archived_deleted',reason='task_deleted',revision=revision+1,checked_at=NOW(),updated_at=NOW() WHERE connection_id=$1 AND record_id=$2 AND state='active' AND reason<>'user_recovery'", item.Connection, item.Record)
			if updateErr == nil && n.RowsAffected() > 0 {
				updateErr = deliveryEvent(ctx, tx, device{Project: item.Project, Connection: item.Connection}, item.Record, true)
			}
			if updateErr != nil {
				_ = tx.Rollback(ctx)
				return updateErr
			}
			if updateErr = tx.Commit(ctx); updateErr != nil {
				return updateErr
			}
			continue
		}
		if status.State == "unlinked" || status.Connection != item.Connection || status.Ref != ref {
			tx, beginErr := w.DB.Begin(ctx)
			if beginErr != nil {
				return beginErr
			}
			_, updateErr := tx.Exec(ctx, "UPDATE sync_policies SET reason='source_missing',checked_at=NOW(),updated_at=NOW() WHERE connection_id=$1 AND record_id=$2 AND state='active' AND reason<>'user_recovery'", item.Connection, item.Record)
			if updateErr == nil {
				_, updateErr = tx.Exec(ctx, "UPDATE sync_deliveries SET state='needs_action',error_code='source_missing',updated_at=NOW() WHERE record_id=$1 AND state IN('pending','retry_wait') AND device_id IN(SELECT id FROM devices WHERE connection_id=$2 AND project_id=$3)", item.Record, item.Connection, item.Project)
			}
			if updateErr == nil && item.Reason != "source_missing" {
				updateErr = deliveryEvent(ctx, tx, device{Project: item.Project, Connection: item.Connection}, item.Record, true)
			}
			if updateErr != nil {
				_ = tx.Rollback(ctx)
				return updateErr
			}
			if updateErr = tx.Commit(ctx); updateErr != nil {
				return updateErr
			}
			continue
		}
		if item.Reason == "source_missing" && status.State == "active" {
			tx, beginErr := w.DB.Begin(ctx)
			if beginErr != nil {
				return beginErr
			}
			_, updateErr := tx.Exec(ctx, "UPDATE sync_policies SET reason='',revision=revision+1,checked_at=NOW(),updated_at=NOW() WHERE connection_id=$1 AND record_id=$2 AND state='active' AND reason='source_missing'", item.Connection, item.Record)
			if updateErr == nil {
				_, updateErr = tx.Exec(ctx, "UPDATE sync_deliveries SET state='pending',attempts=0,error_code='',next_attempt=NOW(),updated_at=NOW() WHERE record_id=$1 AND error_code='source_missing' AND device_id IN(SELECT id FROM devices WHERE connection_id=$2 AND project_id=$3)", item.Record, item.Connection, item.Project)
			}
			if updateErr == nil {
				updateErr = deliveryEvent(ctx, tx, device{Project: item.Project, Connection: item.Connection}, item.Record, true)
			}
			if updateErr != nil {
				_ = tx.Rollback(ctx)
				return updateErr
			}
			if updateErr = tx.Commit(ctx); updateErr != nil {
				return updateErr
			}
			continue
		}
		_, err = w.DB.Exec(ctx, "UPDATE sync_policies SET checked_at=NOW() WHERE connection_id=$1 AND record_id=$2", item.Connection, item.Record)
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) historicSource(ctx context.Context, d device, task string) (map[string]any, error) {
	var raw []byte
	err := w.DB.QueryRow(ctx, "SELECT i.source FROM instances i JOIN changes c ON c.instance_id=i.id WHERE i.project_id=$1 AND i.task_id=$2 AND c.connection_id=$3 ORDER BY i.created_at DESC LIMIT 1", d.Project, task, d.Connection).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var source map[string]any
	if json.Unmarshal(raw, &source) != nil || source["connection_id"] != d.Connection {
		return nil, errors.New("unscoped historical source")
	}
	source["source_state"] = "unlinked"
	status, err := w.sourceStatus(ctx, d.Project, task)
	if err != nil {
		return nil, err
	}
	if status.State == "deleted" && status.Connection == d.Connection && status.Ref == source["source_ref"] {
		source["source_state"] = "deleted"
	}
	return source, nil
}
