package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Self-Command/paca-plugin-task-checkin/internal/model"
	"github.com/jackc/pgx/v5"
)

func deliveryEvent(ctx context.Context, tx pgx.Tx, d device, record string, shared bool) error {
	var id any = d.ID
	if shared {
		id = nil
	}
	_, err := tx.Exec(ctx, "INSERT INTO sync_delivery_events(project_id,connection_id,record_id,device_id) VALUES($1,$2,$3,$4)", d.Project, d.Connection, record, id)
	return err
}

func (w *Worker) ensureDeliveries(ctx context.Context, d device) error {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "INSERT INTO sync_policies(project_id,connection_id,record_id) SELECT DISTINCT project_id,connection_id,record_id FROM changes WHERE project_id=$1 AND connection_id=$2 ON CONFLICT DO NOTHING", d.Project, d.Connection)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "INSERT INTO sync_deliveries(device_id,record_id,state,error_code) SELECT $1,record_id,CASE WHEN reason='source_missing' THEN 'needs_action' ELSE 'pending' END,CASE WHEN reason='source_missing' THEN 'source_missing' ELSE '' END FROM sync_policies WHERE project_id=$2 AND connection_id=$3 ON CONFLICT DO NOTHING RETURNING record_id::text", d.ID, d.Project, d.Connection)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = deliveryEvent(ctx, tx, d, id, false); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (w *Worker) deliveries(out http.ResponseWriter, r *http.Request) {
	d, err := w.device(r.Context(), r)
	if err != nil {
		fail(out, 401, "有效配对码不能为空。")
		return
	}
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil || after < 0 {
		fail(out, 400, "同步进度无效。")
		return
	}
	if err = w.ensureDeliveries(r.Context(), d); err != nil {
		fail(out, 503, "同步处理状态暂不可用。")
		return
	}
	rows, err := w.DB.Query(r.Context(), "SELECT e.cursor,jsonb_build_object('record_id',e.record_id,'policy',CASE WHEN q.state='complete' THEN 'purged' WHEN q.record_id IS NOT NULL THEN 'purging' ELSE p.state END,'revision',COALESCE(p.revision,q.revision),'reason',COALESCE(p.reason,'user_purge'),'historical_path',COALESCE(p.historical_path,''),'historical_created',COALESCE(p.historical_created,''),'state',COALESCE(s.state,'pending'),'attempts',COALESCE(s.attempts,0),'next_attempt',s.next_attempt,'error_code',COALESCE(s.error_code,''),'media_verified',COALESCE(s.media_verified,false),'record_written',COALESCE(s.record_written,false),'status_verified',COALESCE(s.status_verified,false)) FROM sync_delivery_events e LEFT JOIN sync_policies p ON p.record_id=e.record_id AND p.connection_id=e.connection_id LEFT JOIN sync_purges q ON q.record_id=e.record_id AND q.connection_id=e.connection_id LEFT JOIN sync_deliveries s ON s.record_id=e.record_id AND s.device_id=$1 WHERE e.project_id=$2 AND e.connection_id=$3 AND e.cursor>$4 AND (p.record_id IS NOT NULL OR q.record_id IS NOT NULL) AND (e.device_id IS NULL OR e.device_id=$1) ORDER BY e.cursor LIMIT 100", d.ID, d.Project, d.Connection, after)
	if err != nil {
		fail(out, 503, "同步处理状态暂不可用。")
		return
	}
	defer rows.Close()
	items := []any{}
	next := after
	for rows.Next() {
		var cursor int64
		var raw []byte
		if err = rows.Scan(&cursor, &raw); err != nil {
			fail(out, 503, "处理记录暂不可用。")
			return
		}
		var item map[string]any
		if json.Unmarshal(raw, &item) != nil {
			fail(out, 503, "处理记录无效。")
			return
		}
		item["cursor"] = cursor
		items = append(items, item)
		next = cursor
	}
	if rows.Err() != nil {
		fail(out, 503, "处理记录暂不可用。")
		return
	}
	writeJSON(out, 200, map[string]any{"items": items, "next_cursor": next, "has_more": len(items) == 100})
}

type deliveryReport struct {
	Record         string `json:"record_id"`
	Op             string `json:"op_id"`
	Revision       int64  `json:"base_revision"`
	Stage          string `json:"stage"`
	Code           string `json:"error_code"`
	MediaSHA       string `json:"media_sha256"`
	MediaVerified  bool   `json:"media_verified"`
	RecordWritten  bool   `json:"record_written"`
	StatusVerified bool   `json:"status_verified"`
}

func (w *Worker) reportDelivery(out http.ResponseWriter, r *http.Request) {
	d, err := w.device(r.Context(), r)
	if err != nil {
		fail(out, 401, "授权无效。")
		return
	}
	var in deliveryReport
	if !readJSON(out, r, &in) || !model.UUID.MatchString(in.Record) || len(in.Op) < 8 || len(in.Op) > 128 || in.Revision < 1 || (in.Stage != "progress" && in.Stage != "failed" && in.Stage != "confirmed") || len(in.Code) > 64 {
		fail(out, 400, "同步回执无效。")
		return
	}
	if err = w.ensureDeliveries(r.Context(), d); err != nil {
		fail(out, 503, "同步回执暂不可用。")
		return
	}
	tx, err := w.DB.Begin(r.Context())
	if err != nil {
		fail(out, 503, "回执暂不可用。")
		return
	}
	defer tx.Rollback(r.Context())
	hash := model.Hash(map[string]any{"device": d.ID, "report": in})
	request, _ := json.Marshal(in)
	var storedHash, opState string
	var result []byte
	err = tx.QueryRow(r.Context(), "INSERT INTO sync_delivery_ops(connection_id,op_id,request_hash,actor,record_id,result,request,state) SELECT $1,$2,$3,$4,$5,'{}',$6::jsonb,'reporting' WHERE EXISTS(SELECT 1 FROM sync_policies WHERE project_id=$7 AND connection_id=$1 AND record_id=$5) ON CONFLICT(connection_id,op_id) DO UPDATE SET op_id=sync_delivery_ops.op_id RETURNING request_hash,state,result", d.Connection, in.Op, hash, d.ID, in.Record, string(request), d.Project).Scan(&storedHash, &opState, &result)
	if err != nil || storedHash != hash {
		fail(out, 409, "回执操作或来源已变化。")
		return
	}
	if opState == "applied" {
		_ = tx.Commit(r.Context())
		var prior any
		_ = json.Unmarshal(result, &prior)
		writeJSON(out, 200, prior)
		return
	}
	var policy, sha, mediaState, priorState, priorCode string
	var priorNext time.Time
	var revision int64
	var attempts int
	err = tx.QueryRow(r.Context(), "SELECT p.state,p.revision,s.attempts,m.sha256,m.state,s.state,s.error_code,s.next_attempt FROM sync_policies p JOIN sync_deliveries s ON s.record_id=p.record_id AND s.device_id=$1 JOIN records r ON r.id=p.record_id JOIN media m ON m.id=r.media_id WHERE p.project_id=$2 AND p.connection_id=$3 AND p.record_id=$4 FOR UPDATE OF p,s", d.ID, d.Project, d.Connection, in.Record).Scan(&policy, &revision, &attempts, &sha, &mediaState, &priorState, &priorCode, &priorNext)
	if err != nil || revision != in.Revision || policy != "active" {
		fail(out, 409, "同步项已处理，请刷新。")
		return
	}
	if in.MediaVerified && in.MediaSHA != sha && mediaState != "expired" {
		fail(out, 409, "照片校验尚未完成。")
		return
	}
	state, next := priorState, priorNext
	if in.Stage == "progress" {
		in.Code = priorCode
	}
	if in.Stage == "failed" {
		attempts++
		state, next = model.DeliveryRetry(attempts, in.Code, time.Now())
	}
	if in.Stage == "confirmed" {
		if !in.MediaVerified || !in.RecordWritten {
			fail(out, 409, "记录或照片尚未写入。")
			return
		}
		state = "confirmed"
		in.Code = ""
	}
	_, err = tx.Exec(r.Context(), "UPDATE sync_deliveries SET state=$3,attempts=$4,next_attempt=$5,error_code=$6,media_verified=media_verified OR $7,record_written=record_written OR $8,status_verified=status_verified OR $9,updated_at=NOW() WHERE device_id=$1 AND record_id=$2", d.ID, in.Record, state, attempts, next, in.Code, in.MediaVerified, in.RecordWritten, in.StatusVerified)
	if err == nil {
		err = deliveryEvent(r.Context(), tx, d, in.Record, false)
	}
	answer := map[string]any{"state": state, "attempts": attempts, "next_attempt": next, "revision": revision}
	result, _ = json.Marshal(answer)
	if err == nil {
		_, err = tx.Exec(r.Context(), "UPDATE sync_delivery_ops SET state='applied',result=$3::jsonb WHERE connection_id=$1 AND op_id=$2", d.Connection, in.Op, string(result))
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		fail(out, 503, "回执保存失败。")
		return
	}
	writeJSON(out, 200, answer)
}

func (w *Worker) deliveryAction(out http.ResponseWriter, r *http.Request) {
	d, err := w.device(r.Context(), r)
	if err != nil {
		fail(out, 401, "授权无效。")
		return
	}
	var in model.DeliveryAction
	if !readJSON(out, r, &in) || !in.Valid() || in.Connection != d.Connection {
		fail(out, 400, "处理操作无效。")
		return
	}
	raw, _ := json.Marshal(in)
	hash := model.Hash(in)
	if in.Action == "purge" {
		var priorHash, priorState string
		var priorResult []byte
		priorErr := w.DB.QueryRow(r.Context(), "SELECT o.request_hash,o.state,o.result FROM sync_delivery_ops o WHERE o.connection_id=$1 AND o.op_id=$2 AND o.actor=$3 AND (EXISTS(SELECT 1 FROM sync_policies p WHERE p.project_id=$4 AND p.connection_id=$1 AND p.record_id=o.record_id) OR EXISTS(SELECT 1 FROM sync_purges q WHERE q.project_id=$4 AND q.connection_id=$1 AND q.record_id=o.record_id))", d.Connection, in.Op, d.ID, d.Project).Scan(&priorHash, &priorState, &priorResult)
		if priorErr == nil {
			if priorHash != hash {
				fail(out, 409, "清理操作内容已变化。")
				return
			}
			if priorState != "queued" {
				var prior any
				_ = json.Unmarshal(priorResult, &prior)
				status := 200
				if priorState == "conflict" {
					status = 409
				}
				writeJSON(out, status, prior)
				return
			}
		} else if !errors.Is(priorErr, pgx.ErrNoRows) {
			fail(out, 503, "清理结果暂不可用。")
			return
		}
	}
	var stored string
	err = w.DB.QueryRow(r.Context(), "INSERT INTO sync_delivery_ops(connection_id,op_id,request_hash,actor,record_id,result,request) SELECT $1,$2,$3,$4,$5,'{}',$6::jsonb WHERE EXISTS(SELECT 1 FROM sync_policies WHERE project_id=$7 AND connection_id=$1 AND record_id=$5) ON CONFLICT(connection_id,op_id) DO UPDATE SET op_id=sync_delivery_ops.op_id RETURNING request_hash", d.Connection, in.Op, hash, d.ID, in.Record, string(raw), d.Project).Scan(&stored)
	if err != nil || hash != stored {
		fail(out, 409, "处理操作或来源已变化。")
		return
	}
	if err = w.processDeliveryAction(r.Context(), in.Connection, in.Op); err != nil {
		fail(out, 503, "操作已保存，请稍后刷新。")
		return
	}
	var state string
	var result []byte
	err = w.DB.QueryRow(r.Context(), "SELECT state,result FROM sync_delivery_ops WHERE connection_id=$1 AND op_id=$2", d.Connection, in.Op).Scan(&state, &result)
	if err != nil {
		fail(out, 503, "操作结果暂不可用。")
		return
	}
	var answer any
	_ = json.Unmarshal(result, &answer)
	status := 200
	if state == "conflict" {
		status = 409
	}
	writeJSON(out, status, answer)
}

func (w *Worker) processDeliveryAction(ctx context.Context, connection, op string) error {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var state, actor string
	err = tx.QueryRow(ctx, "SELECT request,state,actor FROM sync_delivery_ops WHERE connection_id=$1 AND op_id=$2 FOR UPDATE", connection, op).Scan(&raw, &state, &actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || state != "queued" {
		return err
	}
	var in model.DeliveryAction
	if json.Unmarshal(raw, &in) != nil || !in.Valid() {
		return errors.New("invalid persisted action")
	}
	var d device
	d.Connection = connection
	var revision int64
	var policy string
	err = tx.QueryRow(ctx, "SELECT project_id::text,revision,state FROM sync_policies WHERE connection_id=$1 AND record_id=$2 FOR UPDATE", connection, in.Record).Scan(&d.Project, &revision, &policy)
	missing := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !missing {
		return err
	}
	err = nil
	result := map[string]any{"state": "applied", "revision": revision + 1}
	state = "applied"
	if missing {
		state = "conflict"
		result = map[string]any{"code": "purged", "error": "同步记录已清理。"}
	} else if in.Revision != revision {
		state = "conflict"
		result = map[string]any{"code": "revision", "error": "同步项已变化，请刷新后处理。"}
	} else if in.Action == "purge" {
		var authorized bool
		if actor == "project_manager" {
			authorized = true
		} else {
			err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM devices WHERE id::text=$1 AND enabled AND project_id=$2 AND connection_id=$3)", actor, d.Project, connection).Scan(&authorized)
			if err != nil {
				return err
			}
		}
		if !authorized {
			state = "conflict"
			result = map[string]any{"code": "scope", "error": "当前设备无权清理这条记录。"}
		} else {
			var allowed bool
			err = tx.QueryRow(ctx, "SELECT NOT EXISTS(SELECT 1 FROM changes WHERE record_id=$1 AND (project_id<>$2 OR connection_id<>$3)) AND NOT EXISTS(SELECT 1 FROM sync_policies WHERE record_id=$1 AND connection_id<>$3)", in.Record, d.Project, connection).Scan(&allowed)
			if err == nil && !allowed {
				state = "conflict"
				result = map[string]any{"code": "shared_record", "error": "记录仍被其他来源使用，不能清理。"}
			} else if err == nil {
				result, err = w.queueArchivePurge(ctx, tx, d, in)
			}
		}
	} else {
		var purging bool
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sync_purges WHERE connection_id=$1 AND record_id=$2)", connection, in.Record).Scan(&purging)
		if err != nil {
			return err
		}
		if purging {
			state = "conflict"
			result = map[string]any{"code": "purging", "error": "记录已进入彻底清理，无法恢复。"}
		} else {
			policy := "active"
			if in.Action == "ignore" {
				policy = "ignored"
			}
			_, err = tx.Exec(ctx, "UPDATE sync_policies SET state=$3,revision=revision+1,historical_path=CASE WHEN $4='associate' THEN $5 ELSE historical_path END,historical_created=CASE WHEN $4='associate' THEN $6 ELSE historical_created END,reason=CASE WHEN $3='ignored' THEN 'user_ignored' WHEN $4 IN('restore','associate') THEN 'user_recovery' ELSE reason END,updated_at=NOW(),checked_at=CASE WHEN $4 IN('restore','associate') THEN NOW() ELSE checked_at END WHERE connection_id=$1 AND record_id=$2", connection, in.Record, policy, in.Action, in.Path, in.Created)
			if err == nil && policy == "active" {
				_, err = tx.Exec(ctx, "UPDATE sync_deliveries SET state='pending',attempts=0,next_attempt=NOW(),error_code='',record_written=CASE WHEN $4='associate' THEN false ELSE record_written END,status_verified=CASE WHEN $4='associate' THEN false ELSE status_verified END,updated_at=NOW() WHERE record_id=$1 AND device_id IN(SELECT id FROM devices WHERE connection_id=$2 AND project_id=$3)", in.Record, connection, d.Project, in.Action)
			}
			if err == nil {
				err = deliveryEvent(ctx, tx, d, in.Record, true)
			}
		}
	}
	answer, _ := json.Marshal(result)
	if err == nil {
		_, err = tx.Exec(ctx, "UPDATE sync_delivery_ops SET state=$3,result=$4::jsonb WHERE connection_id=$1 AND op_id=$2 AND actor=$5", connection, op, state, string(answer), actor)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *Worker) DeliveryTick(ctx context.Context) error {
	if err := w.control(ctx); err != nil {
		return err
	}
	rows, err := w.DB.Query(ctx, "SELECT connection_id,op_id FROM sync_delivery_ops WHERE state='queued' ORDER BY created_at LIMIT 20")
	if err != nil {
		return err
	}
	ops := [][2]string{}
	for rows.Next() {
		var conn, op string
		if err = rows.Scan(&conn, &op); err != nil {
			rows.Close()
			return err
		}
		ops = append(ops, [2]string{conn, op})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, op := range ops {
		if err = w.processDeliveryAction(ctx, op[0], op[1]); err != nil {
			return err
		}
	}
	return errors.Join(w.purgeArchivedRecords(ctx), w.classifyDeliveries(ctx))
}
