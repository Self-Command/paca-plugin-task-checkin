package main

import (
	"encoding/json"
	"fmt"

	plugin "github.com/Paca-AI/plugin-sdk-go"
	"github.com/Self-Command/paca-plugin-task-checkin/internal/model"
)

func (p *integrationPlugin) syncDeliveries(req *plugin.Request, res *plugin.Response) {
	rows, err := p.db.Query("SELECT p.record_id::text,p.connection_id,CASE WHEN q.record_id IS NOT NULL THEN 'purging' ELSE p.state END,p.revision,p.reason,i.title,r.kind,r.submitted_at::text,r.status,COALESCE((SELECT json_agg(json_build_object('name',d.name,'state',s.state,'attempts',s.attempts,'error_code',s.error_code,'media_verified',s.media_verified,'record_written',s.record_written,'status_verified',s.status_verified))::text FROM sync_deliveries s JOIN devices d ON d.id=s.device_id WHERE s.record_id=p.record_id AND d.connection_id=p.connection_id AND d.project_id=p.project_id),'[]'),m.bytes,COALESCE(q.last_error,'') FROM sync_policies p JOIN records r ON r.id=p.record_id JOIN instances i ON i.id=r.instance_id JOIN media m ON m.id=r.media_id LEFT JOIN sync_purges q ON q.record_id=p.record_id AND q.connection_id=p.connection_id WHERE p.project_id=$1 ORDER BY r.submitted_at DESC LIMIT 200", req.PathParam("projectId"))
	if err != nil {
		res.Error(503, "同步处理状态暂不可用。")
		return
	}
	items := []any{}
	for _, r := range rows.Rows {
		var devices any
		_ = json.Unmarshal([]byte(fmt.Sprint(r[9])), &devices)
		items = append(items, map[string]any{"record_id": r[0], "connection_id": r[1], "policy": r[2], "revision": r[3], "reason": r[4], "title": r[5], "kind": r[6], "submitted_at": r[7], "paca_status": r[8], "devices": devices, "photo_bytes": r[10], "cleanup_error": r[11]})
	}
	res.JSON(200, map[string]any{"items": items})
}

func (p *integrationPlugin) archivedSyncRecords(req *plugin.Request, res *plugin.Response) {
	rows, err := p.db.Query("SELECT p.record_id::text,p.connection_id,p.revision,i.title,r.kind,r.submitted_at::text,m.bytes FROM sync_policies p JOIN records r ON r.id=p.record_id JOIN instances i ON i.id=r.instance_id JOIN media m ON m.id=r.media_id WHERE p.project_id=$1 AND p.state='archived_deleted' AND NOT EXISTS(SELECT 1 FROM sync_purges q WHERE q.connection_id=p.connection_id AND q.record_id=p.record_id) ORDER BY r.submitted_at LIMIT 501", req.PathParam("projectId"))
	if err != nil {
		res.Error(503, "归档记录暂时无法读取。")
		return
	}
	items := []any{}
	for n, r := range rows.Rows {
		if n == 500 {
			break
		}
		items = append(items, map[string]any{"record_id": r[0], "connection_id": r[1], "revision": r[2], "title": r[3], "kind": r[4], "submitted_at": r[5], "photo_bytes": r[6], "policy": "archived_deleted", "devices": []any{}})
	}
	res.JSON(200, map[string]any{"items": items, "has_more": len(rows.Rows) > 500})
}

func (p *integrationPlugin) deliveryAction(req *plugin.Request, res *plugin.Response) {
	in, err := plugin.JSONBody[model.DeliveryAction](req)
	if err != nil || !in.Valid() {
		res.Error(400, "处理操作无效。")
		return
	}
	raw, _ := json.Marshal(in)
	hash := model.Hash(in)
	if in.Action == "purge" {
		prior, readErr := p.db.Query("SELECT o.request_hash,o.state FROM sync_delivery_ops o WHERE o.connection_id=$1 AND o.op_id=$2 AND (EXISTS(SELECT 1 FROM sync_policies p WHERE p.project_id=$3 AND p.connection_id=$1 AND p.record_id=o.record_id) OR EXISTS(SELECT 1 FROM sync_purges q WHERE q.project_id=$3 AND q.connection_id=$1 AND q.record_id=o.record_id))", in.Connection, in.Op, req.PathParam("projectId"))
		if readErr != nil {
			res.Error(503, "清理操作暂不可用。")
			return
		}
		if len(prior.Rows) == 1 {
			if fmt.Sprint(prior.Rows[0][0]) != hash {
				res.Error(409, "清理操作内容已变化。")
				return
			}
			res.JSON(202, map[string]any{"op_id": in.Op, "state": prior.Rows[0][1]})
			return
		}
		eligible, readErr := p.db.Query("SELECT p.record_id FROM sync_policies p WHERE p.project_id=$1 AND p.connection_id=$2 AND p.record_id=$3 AND p.revision=$4 AND p.state='archived_deleted' AND NOT EXISTS(SELECT 1 FROM changes c WHERE c.record_id=p.record_id AND (c.project_id<>p.project_id OR c.connection_id<>p.connection_id)) AND NOT EXISTS(SELECT 1 FROM sync_policies other WHERE other.record_id=p.record_id AND other.connection_id<>p.connection_id)", req.PathParam("projectId"), in.Connection, in.Record, in.Revision)
		if readErr != nil {
			res.Error(503, "归档记录暂不可用。")
			return
		}
		if len(eligible.Rows) != 1 {
			res.Error(409, "只能清理未被其他来源使用的归档记录，请刷新后核对。")
			return
		}
	}
	rows, err := p.db.Query("INSERT INTO sync_delivery_ops(connection_id,op_id,request_hash,actor,record_id,result,request) SELECT $1,$2,$3,'project_manager',$4,'{}',$5::jsonb WHERE EXISTS(SELECT 1 FROM sync_policies WHERE project_id=$6 AND connection_id=$1 AND record_id=$4) ON CONFLICT(connection_id,op_id) DO UPDATE SET op_id=sync_delivery_ops.op_id RETURNING request_hash,state,result::text", in.Connection, in.Op, hash, in.Record, string(raw), req.PathParam("projectId"))
	if err != nil {
		res.Error(503, "处理操作暂时无法保存。")
		return
	}
	if len(rows.Rows) != 1 || fmt.Sprint(rows.Rows[0][0]) != hash {
		res.Error(409, "操作内容或来源已变化，请刷新。")
		return
	}
	res.JSON(202, map[string]any{"op_id": in.Op, "state": rows.Rows[0][1]})
}
