package worker

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Self-Command/paca-plugin-task-checkin/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
)

func (w *Worker) queueArchivePurge(ctx context.Context, tx pgx.Tx, d device, in model.DeliveryAction) (map[string]any, error) {
	var exists bool
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sync_purges WHERE connection_id=$1 AND record_id=$2)", d.Connection, in.Record).Scan(&exists)
	if err != nil {
		return nil, err
	}
	if exists {
		_, err = tx.Exec(ctx, "UPDATE sync_purges SET state='pending',attempts=0,next_attempt=NOW(),last_error='' WHERE connection_id=$1 AND record_id=$2 AND state<>'complete'", d.Connection, in.Record)
		return map[string]any{"state": "purging", "revision": in.Revision}, err
	}
	var instance, media, object, kind string
	err = tx.QueryRow(ctx, "SELECT r.instance_id::text,r.media_id::text,m.object_key,r.kind FROM records r JOIN media m ON m.id=r.media_id JOIN instances i ON i.id=r.instance_id WHERE r.id=$1 AND i.project_id=$2 FOR UPDATE OF r,m", in.Record, d.Project).Scan(&instance, &media, &object, &kind)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO sync_purges(project_id,connection_id,record_id,instance_id,media_id,object_key,op_id,revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", d.Project, d.Connection, in.Record, instance, media, object, in.Op, in.Revision+1)
	if err == nil {
		_, err = tx.Exec(ctx, "UPDATE sync_policies SET revision=revision+1,reason='user_purge',historical_path='',historical_created='',updated_at=NOW() WHERE connection_id=$1 AND record_id=$2", d.Connection, in.Record)
	}
	if err == nil {
		_, err = tx.Exec(ctx, "UPDATE grants SET expires_at=LEAST(expires_at,NOW()) WHERE instance_id=$1 AND kind=$2", instance, kind)
	}
	if err == nil {
		_, err = tx.Exec(ctx, "DELETE FROM sessions WHERE grant_id IN(SELECT id FROM grants WHERE instance_id=$1 AND kind=$2)", instance, kind)
	}
	if err == nil {
		err = deliveryEvent(ctx, tx, d, in.Record, true)
	}
	return map[string]any{"state": "purging", "revision": in.Revision + 1}, err
}

func (w *Worker) purgeArchivedRecords(ctx context.Context) error {
	rows, err := w.DB.Query(ctx, "SELECT connection_id,record_id::text,object_key,attempts FROM sync_purges WHERE state='pending' AND next_attempt<=NOW() ORDER BY created_at LIMIT 20")
	if err != nil {
		return err
	}
	type item struct {
		connection, record, object string
		attempts int
	}
	items := []item{}
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.connection, &i.record, &i.object, &i.attempts); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, i := range items {
		err = w.Objects.RemoveObject(ctx, w.Bucket, i.object, minio.RemoveObjectOptions{})
		if err == nil {
			err = w.finishArchivePurge(ctx, i.connection, i.record)
		}
		if err != nil {
			failures = append(failures, err)
			attempts := i.attempts + 1
			state, next := "pending", time.Now().Add(15*time.Minute)
			message := "清理暂未完成，将稍后重试。"
			if attempts >= 10 {
				state = "failed"
				message = "清理暂未完成，请重试清理。"
			} else {
				_, next = model.DeliveryRetry(attempts, "unavailable", time.Now())
			}
			_, saveErr := w.DB.Exec(ctx, "UPDATE sync_purges SET state=$3,attempts=$4,next_attempt=$5,last_error=$6 WHERE connection_id=$1 AND record_id=$2 AND state='pending'", i.connection, i.record, state, attempts, next, message)
			if saveErr != nil {
				failures = append(failures, saveErr)
			}
		}
	}
	return errors.Join(failures...)
}

func (w *Worker) finishArchivePurge(ctx context.Context, connection, record string) error {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var d device
	d.Connection = connection
	var instance, media, op, state string
	var revision int64
	err = tx.QueryRow(ctx, "SELECT project_id::text,instance_id::text,COALESCE(media_id::text,''),op_id,revision,state FROM sync_purges WHERE connection_id=$1 AND record_id=$2 FOR UPDATE", connection, record).Scan(&d.Project, &instance, &media, &op, &revision, &state)
	if err != nil {
		return err
	}
	if state == "complete" {
		return nil
	}
	queries := []struct {
		sql string
		args []any
	}{
		{"DELETE FROM receipts WHERE change_cursor IN(SELECT cursor FROM changes WHERE record_id=$1)", []any{record}},
		{"DELETE FROM changes WHERE record_id=$1", []any{record}},
		{"DELETE FROM sync_deliveries WHERE record_id=$1", []any{record}},
		{"DELETE FROM sync_policies WHERE record_id=$1 AND connection_id=$2", []any{record, connection}},
		{"DELETE FROM sync_delivery_events WHERE record_id=$1 AND connection_id=$2", []any{record, connection}},
		{"DELETE FROM sync_delivery_ops WHERE record_id=$1 AND NOT(connection_id=$2 AND op_id=$3)", []any{record, connection, op}},
		{"DELETE FROM records WHERE id=$1", []any{record}},
		{"DELETE FROM media WHERE id=$1", []any{media}},
		{"UPDATE instances SET state='cancelled',title='',source='{}',last_error='' WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM records WHERE instance_id=$1)", []any{instance}},
		{"DELETE FROM conflicts WHERE instance_id=$1 AND NOT EXISTS(SELECT 1 FROM records WHERE instance_id=$1)", []any{instance}},
		{"DELETE FROM outbox WHERE instance_id=$1 AND NOT EXISTS(SELECT 1 FROM records WHERE instance_id=$1)", []any{instance}},
		{"DELETE FROM sessions WHERE grant_id IN(SELECT id FROM grants WHERE instance_id=$1) AND NOT EXISTS(SELECT 1 FROM records WHERE instance_id=$1)", []any{instance}},
		{"DELETE FROM grants WHERE instance_id=$1 AND NOT EXISTS(SELECT 1 FROM records WHERE instance_id=$1)", []any{instance}},
		{"UPDATE sync_purges SET state='complete',media_id=NULL,object_key='',last_error='',cleaned_at=NOW() WHERE connection_id=$1 AND record_id=$2", []any{connection, record}},
	}
	for _, q := range queries {
		if _, err = tx.Exec(ctx, q.sql, q.args...); err != nil {
			return err
		}
	}
	if err = deliveryEvent(ctx, tx, d, record, true); err != nil {
		return err
	}
	answer, _ := json.Marshal(map[string]any{"state": "purged", "revision": revision})
	if _, err = tx.Exec(ctx, "UPDATE sync_delivery_ops SET result=$3::jsonb WHERE connection_id=$1 AND op_id=$2", connection, op, string(answer)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
