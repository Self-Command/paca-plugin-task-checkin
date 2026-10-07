package model

import (
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "errors"
 "regexp"
 "time"
 _ "time/tzdata"
)
var UUID=regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
type Config struct {
 Enabled bool `json:"enabled"`
 Timezone string `json:"timezone"`
 StartMinutes int `json:"start_minutes"`
 DueMinutes int `json:"due_minutes"`
 RetentionDays int `json:"retention_days"`
 ProgressStatus string `json:"progress_status"`
 DoneStatus string `json:"done_status"`
 ArchiveStatus string `json:"archive_status"`
}
func Default()Config{return Config{Timezone:"Asia/Shanghai",StartMinutes:10,DueMinutes:10,RetentionDays:90}}
func(c Config)Valid()bool{
 if _,err:=time.LoadLocation(c.Timezone);err!=nil{return false}
 if c.StartMinutes<1||c.StartMinutes>1440||c.DueMinutes<1||c.DueMinutes>1440||c.RetentionDays<1||c.RetentionDays>3650{return false}
 if c.Enabled && (!UUID.MatchString(c.ProgressStatus)||!UUID.MatchString(c.DoneStatus)||!UUID.MatchString(c.ArchiveStatus)){return false}
 return true
}
type Rule struct{
 Enabled bool `json:"enabled"`
 Start *time.Time `json:"start"`
 Due *time.Time `json:"due"`
 StartMinutes *int `json:"start_minutes"`
 DueMinutes *int `json:"due_minutes"`
 BaseFingerprint string `json:"base_fingerprint"`
}
type Task struct{
 ID string `json:"id"`
 Project string `json:"project_id"`
 Title string `json:"title"`
 Status string `json:"status_id"`
 Custom map[string]any `json:"custom_fields"`
}
type Instance struct{
 ID string `json:"id"`
 Project string `json:"project_id"`
 Task string `json:"task_id"`
 Revision int `json:"revision"`
 Title string `json:"title"`
 Fingerprint string `json:"fingerprint"`
 Source json.RawMessage `json:"source"`
 Start *time.Time `json:"start"`
 Due *time.Time `json:"due"`
 StartMinutes int `json:"start_minutes"`
 DueMinutes int `json:"due_minutes"`
 Freeze time.Time `json:"freeze_at"`
 State string `json:"state"`
 StatusRevision int `json:"status_revision"`
}
func(i Instance)Window(kind string)(time.Time,time.Time,error){
 target,minutes:=i.Start,i.StartMinutes
 if kind=="due"{target,minutes=i.Due,i.DueMinutes}else if kind!="start"{return time.Time{},time.Time{},errors.New("invalid card")}
 if target==nil{return time.Time{},time.Time{},errors.New("card time is not configured")}
 return target.Add(-time.Duration(minutes)*time.Minute),*target,nil
}
func(i Instance)CanSubmit(kind string,now time.Time)error{
 if i.State!="active"&&i.State!="frozen"{return errors.New("instance cancelled or schedule conflict")}
 open,close,err:=i.Window(kind);if err!=nil{return err}
 if now.Before(open){return errors.New("window is not open")}
 if !now.Before(close){return errors.New("window has expired")}
 return nil
}
func Hash(v any)string{b,_:=json.Marshal(v);h:=sha256.Sum256(b);return hex.EncodeToString(h[:])}
func DesiredStatus(kind string,hasEnd bool,c Config)string{
 if kind=="due"||hasEnd{return c.DoneStatus};return c.ProgressStatus
}
func Precise(t Task,kind string)*time.Time{
 m,_:=t.Custom["_integration_state_v1"].(map[string]any)
 if m[kind+"_precision"]!="instant"{return nil}
 raw,_:=m[kind+"_instant"].(string);instant,err:=time.Parse(time.RFC3339Nano,raw);if err!=nil{return nil}
 utc:=instant.UTC();return &utc
}
