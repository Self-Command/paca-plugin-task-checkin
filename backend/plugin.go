package main

import(
 "crypto/rand"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "fmt"
 plugin "github.com/Paca-AI/plugin-sdk-go"
 "github.com/Self-Command/paca-plugin-task-checkin/internal/buildinfo"
 "github.com/Self-Command/paca-plugin-task-checkin/internal/model"
)
const pluginID="com.selfcommand.task-checkin"
const pluginVersion=buildinfo.Version
type integrationPlugin struct{db *plugin.DB;cfg *plugin.Config}
func(p *integrationPlugin)Init(ctx *plugin.Context)error{
 p.db=ctx.DB();p.cfg=ctx.Config()
 ctx.Route("GET","/health",p.health);ctx.Route("GET","/worker/control",p.workerControl)
 ctx.Route("POST","/admin/worker-credential",p.rotateWorkerCredential)
 ctx.Route("GET","/projects/:projectId/settings",p.settings);ctx.Route("PUT","/projects/:projectId/settings",p.saveSettings)
 ctx.Route("GET","/projects/:projectId/tasks/:taskId/checkin",p.taskSettings);ctx.Route("PUT","/projects/:projectId/tasks/:taskId/checkin",p.saveTask)
 ctx.Route("POST","/projects/:projectId/tasks/:taskId/cancel",p.cancel)
 ctx.Route("GET","/projects/:projectId/records",p.records)
 ctx.Route("POST","/projects/:projectId/pairing",p.pair);ctx.Route("DELETE","/projects/:projectId/pairing/:id",p.revoke)
 for _,topic:=range []string{"task.created","task.updated","task.deleted"}{ctx.On(topic,p.dirty)}
 return nil
}
func(p *integrationPlugin)Shutdown(){}
func(p *integrationPlugin)health(req *plugin.Request,res *plugin.Response){
 rows,err:=p.db.Query("SELECT version FROM plugin_metadata WHERE id=1")
 if err!=nil||len(rows.Rows)!=1{res.Error(503,"migration unavailable");return}
 res.JSON(200,map[string]any{"id":pluginID,"version":pluginVersion,"source_sha":buildinfo.SourceSHA,"schema_version":rows.Rows[0][0]})
}
func(p *integrationPlugin)settings(req *plugin.Request,res *plugin.Response){
 rows,err:=p.db.Query("SELECT config::text,revision,last_error FROM project_settings WHERE project_id=$1",req.PathParam("projectId"))
 if err!=nil{res.Error(503,"settings unavailable");return}
 if len(rows.Rows)==0{res.JSON(200,map[string]any{"config":model.Default(),"revision":0});return}
 var cfg model.Config;_ =json.Unmarshal([]byte(fmt.Sprint(rows.Rows[0][0])),&cfg)
 res.JSON(200,map[string]any{"config":cfg,"revision":rows.Rows[0][1],"last_error":rows.Rows[0][2]})
}
func(p *integrationPlugin)saveSettings(req *plugin.Request,res *plugin.Response){
 body,err:=plugin.JSONBody[struct{Config model.Config `json:"config"`;Revision int `json:"revision"`}](req)
 if err!=nil||!body.Config.Valid(){res.Error(400,"valid timezone, lead minutes (1-1440), retention and project status IDs required");return}
 raw,_:=json.Marshal(body.Config)
 result,err:=p.db.Query("WITH updated AS (UPDATE project_settings SET config=$2::jsonb,revision=revision+1,needs_reconcile=TRUE WHERE project_id=$1 AND revision=$3 RETURNING project_id), inserted AS (INSERT INTO project_settings(project_id,config) SELECT $1,$2::jsonb WHERE $3=0 ON CONFLICT DO NOTHING RETURNING project_id) SELECT project_id FROM updated UNION ALL SELECT project_id FROM inserted",req.PathParam("projectId"),string(raw),body.Revision)
 if err!=nil{res.Error(503,"save failed");return};if len(result.Rows)!=1{res.Error(409,"settings changed; reload");return}
 res.JSON(200,map[string]any{"revision":body.Revision+1})
}
func(p *integrationPlugin)taskSettings(req *plugin.Request,res *plugin.Response){
 rows,err:=p.db.Query("SELECT config::text,revision FROM task_rules WHERE project_id=$1 AND task_id=$2",req.PathParam("projectId"),req.PathParam("taskId"))
 if err!=nil{res.Error(503,"task rule unavailable");return}
 var rule any=nil;var revision any=0
 if len(rows.Rows)>0{_ =json.Unmarshal([]byte(fmt.Sprint(rows.Rows[0][0])),&rule);revision=rows.Rows[0][1]}
 instances,err:=p.db.Query("SELECT id::text,revision,state,last_error,start_at::text,due_at::text FROM instances WHERE project_id=$1 AND task_id=$2 ORDER BY revision DESC LIMIT 1",req.PathParam("projectId"),req.PathParam("taskId"))
 if err!=nil{res.Error(503,"instance unavailable");return}
 var current any=nil
 if len(instances.Rows)>0{r:=instances.Rows[0];current=map[string]any{"id":r[0],"revision":r[1],"state":r[2],"last_error":r[3],"start":r[4],"due":r[5]}}
 res.JSON(200,map[string]any{"config":rule,"revision":revision,"instance":current})
}
func(p *integrationPlugin)saveTask(req *plugin.Request,res *plugin.Response){
 body,err:=plugin.JSONBody[struct{Config model.Rule `json:"config"`;Revision int `json:"revision"`}](req)
 if err!=nil{res.Error(400,"invalid rule");return}
 for _,v:=range []*int{body.Config.StartMinutes,body.Config.DueMinutes}{if v!=nil&&(*v<1||*v>1440){res.Error(400,"lead minutes must be 1-1440");return}}
 if body.Config.Start!=nil&&body.Config.Due!=nil&&body.Config.Due.Before(*body.Config.Start){res.Error(400,"end before start");return}
 frozen,err:=p.db.Query("SELECT id FROM instances WHERE project_id=$1 AND task_id=$2 AND state IN('active','frozen','conflict') AND freeze_at<=clock_timestamp()",req.PathParam("projectId"),req.PathParam("taskId"))
 if err!=nil{res.Error(503,"window check unavailable");return};if len(frozen.Rows)>0{res.Error(409,"window opened; cancel before scheduling another instance");return}
 raw,_:=json.Marshal(body.Config)
 result,err:=p.db.Query("WITH updated AS (UPDATE task_rules SET config=$3::jsonb,revision=revision+1,base_fingerprint='' WHERE project_id=$1 AND task_id=$2 AND revision=$4 RETURNING task_id), inserted AS (INSERT INTO task_rules(project_id,task_id,config) SELECT $1,$2,$3::jsonb WHERE $4=0 ON CONFLICT DO NOTHING RETURNING task_id) SELECT task_id FROM updated UNION ALL SELECT task_id FROM inserted",req.PathParam("projectId"),req.PathParam("taskId"),string(raw),body.Revision)
 if err!=nil{res.Error(503,"rule save failed");return};if len(result.Rows)!=1{res.Error(409,"rule changed; reload");return}
 _,_=p.db.Exec("UPDATE project_settings SET needs_reconcile=TRUE WHERE project_id=$1",req.PathParam("projectId"))
 res.JSON(200,map[string]any{"revision":body.Revision+1})
}
func(p *integrationPlugin)cancel(req *plugin.Request,res *plugin.Response){
 _,err:=p.db.Exec("WITH optout AS (INSERT INTO task_rules(project_id,task_id,config) VALUES($1,$2,'{\"enabled\":false}') ON CONFLICT(project_id,task_id) DO UPDATE SET config=jsonb_set(task_rules.config,'{enabled}','false'),revision=task_rules.revision+1 RETURNING task_id) UPDATE instances SET state='cancelled' WHERE project_id=$1 AND task_id IN(SELECT task_id FROM optout) AND state IN('active','frozen','conflict')",req.PathParam("projectId"),req.PathParam("taskId"))
 if err!=nil{res.Error(503,"cancel failed");return};res.JSON(200,map[string]any{"cancelled":true})
}
func(p *integrationPlugin)records(req *plugin.Request,res *plugin.Response){
 rows,err:=p.db.Query("SELECT r.id::text,i.task_id::text,i.title,r.kind,r.submitted_at::text,r.status FROM records r JOIN instances i ON i.id=r.instance_id WHERE i.project_id=$1 ORDER BY r.submitted_at DESC LIMIT 100",req.PathParam("projectId"))
 if err!=nil{res.Error(503,"records unavailable");return}
 items:=[]any{};for _,r:=range rows.Rows{items=append(items,map[string]any{"id":r[0],"task_id":r[1],"title":r[2],"kind":r[3],"submitted_at":r[4],"status":r[5]})};res.JSON(200,map[string]any{"items":items})
}
func randomID()(string,error){b:=make([]byte,16);if _,err:=rand.Read(b);err!=nil{return"",err};b[6]=b[6]&15|64;b[8]=b[8]&63|128;return fmt.Sprintf("%x-%x-%x-%x-%x",b[:4],b[4:6],b[6:8],b[8:10],b[10:]),nil}
func(p *integrationPlugin)pair(req *plugin.Request,res *plugin.Response){
 body,err:=plugin.JSONBody[struct{Name string `json:"name"`;Connection string `json:"connection_id"`}](req)
 if err!=nil||body.Name==""||len(body.Name)>120||!model.UUID.MatchString(body.Connection){res.Error(400,"device name and source connection UUID required");return}
 id,err:=randomID();if err!=nil{res.Error(503,"randomness unavailable");return}
 token:=make([]byte,32);if _,err=rand.Read(token);err!=nil{res.Error(503,"randomness unavailable");return}
 plain:=hex.EncodeToString(token);hash:=sha256.Sum256([]byte(plain))
 _,err=p.db.Exec("INSERT INTO devices(id,project_id,connection_id,name,token_hash) VALUES($1,$2,$3,$4,$5)",id,req.PathParam("projectId"),body.Connection,body.Name,hex.EncodeToString(hash[:]))
 if err!=nil{res.Error(503,"pairing failed");return};res.JSON(201,map[string]any{"id":id,"token":plain,"warning":"Save once in Obsidian; this token is revocable and never stored in notes."})
}
func(p *integrationPlugin)revoke(req *plugin.Request,res *plugin.Response){
 _,err:=p.db.Exec("UPDATE devices SET enabled=FALSE WHERE id=$1 AND project_id=$2",req.PathParam("id"),req.PathParam("projectId"));if err!=nil{res.Error(503,"revoke failed");return};res.JSON(200,map[string]any{"revoked":true})
}
func(p *integrationPlugin)dirty(evt *plugin.Event){_,_=p.db.Exec("UPDATE project_settings SET needs_reconcile=TRUE")}
