package worker

import (
 "context"
 "crypto/hmac"
 "crypto/sha256"
 "encoding/hex"
 "fmt"
 "net/http"
 "github.com/Self-Command/paca-plugin-task-checkin/internal/model"
)

type delegatedDeviceKey struct{}
// Private container-network capability bridge. Never mounted under public /internal/.
func (w *Worker) pairingInfo(out http.ResponseWriter,r *http.Request){
 if w.ActionSecret==""||!hmac.Equal([]byte(r.Header.Get("Authorization")),[]byte("Bearer "+w.ActionSecret)){fail(out,401,"internal authentication required");return}
 if err:=w.control(r.Context());err!=nil{fail(out,503,"plugin unavailable");return}
 var input struct{Token string `json:"token"`};if !readJSON(out,r,&input){return};if len(input.Token)!=64{fail(out,401,"valid paired credential required");return}
 var d device;err:=w.DB.QueryRow(r.Context(),"SELECT id::text,project_id::text,connection_id FROM devices WHERE token_hash=$1 AND enabled",tokenHash(input.Token)).Scan(&d.ID,&d.Project,&d.Connection);if err!=nil{fail(out,401,"valid paired credential required");return}
 writeJSON(out,200,map[string]any{"project_id":d.Project,"connection_id":d.Connection})
}
func (w *Worker) delegated(handler http.HandlerFunc)http.HandlerFunc{return func(out http.ResponseWriter,r *http.Request){
 if w.ActionSecret==""||!hmac.Equal([]byte(r.Header.Get("Authorization")),[]byte("Bearer "+w.ActionSecret)){fail(out,401,"internal authentication required");return}
 project,connection,client:=r.Header.Get("X-Sync-Project"),r.Header.Get("X-Sync-Connection"),r.Header.Get("X-Sync-Device")
 if !model.UUID.MatchString(project)||!model.UUID.MatchString(connection)||!model.UUID.MatchString(client){fail(out,400,"valid scoped identities required");return}
 sum:=sha256.Sum256([]byte(connection+"\n"+client));sum[6]=(sum[6]&15)|80;sum[8]=(sum[8]&63)|128;id:=fmt.Sprintf("%x-%x-%x-%x-%x",sum[:4],sum[4:6],sum[6:8],sum[8:10],sum[10:16])
 if err:=w.control(r.Context());err!=nil{fail(out,503,"plugin unavailable");return}
 _,err:=w.DB.Exec(r.Context(),"INSERT INTO devices(id,project_id,connection_id,name,token_hash) VALUES($1,$2,$3,'任务同步',$4) ON CONFLICT(id) DO NOTHING",id,project,connection,"service:"+hex.EncodeToString(sum[:]));if err!=nil{fail(out,503,"delegation unavailable");return}
 handler(out,r.WithContext(context.WithValue(r.Context(),delegatedDeviceKey{},device{ID:id,Project:project,Connection:connection})))
}}
