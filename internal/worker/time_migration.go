package worker
import("context";"encoding/json";"errors";"time";"github.com/Self-Command/paca-plugin-task-checkin/internal/model")
func sameTime(a,b *time.Time)bool{return a==nil&&b==nil||a!=nil&&b!=nil&&a.Equal(*b)}
func (w *Worker) migrateRuleTimes(ctx context.Context,project string,task model.Task,rule model.Rule,revision int)(model.Task,error){
 if rule.Start==nil&&rule.Due==nil&&rule.StartMinutes==nil&&rule.DueMinutes==nil{return task,nil}
 var frozen bool
 if err:=w.DB.QueryRow(ctx,"SELECT EXISTS(SELECT 1 FROM instances WHERE project_id=$1 AND task_id=$2 AND state IN('active','frozen','conflict') AND freeze_at<=clock_timestamp())",project,task.ID).Scan(&frozen);err!=nil{return task,err}
 if frozen{return task,errors.New("已有打卡窗口已开放，专属时间需核对；请先取消原实例。")}
 times:=model.TimesOf(task)
 for _,entry:=range []struct{kind string;legacy *time.Time;value *model.TimeValue}{{"start",rule.Start,&times.Start},{"due",rule.Due,&times.Due}}{
 if entry.legacy==nil{continue}
 current:=model.Precise(task,entry.kind)
 if current!=nil&&!current.Equal(*entry.legacy){return task,errors.New("原打卡时间与任务时间不一致，请核对后重新确认。")}
 *entry.value=model.TimeValue{Precision:"instant",Value:entry.legacy.Format(time.RFC3339Nano)}
 }
 if rule.StartMinutes!=nil{times.StartMinutes=*rule.StartMinutes};if rule.DueMinutes!=nil{times.DueMinutes=*rule.DueMinutes}
 patch,err:=model.TimePatch(task,times);if err!=nil{return task,err}
 var fresh model.Task
 if err=w.call(ctx,"GET","/projects/"+project+"/tasks/"+task.ID,nil,&fresh);err!=nil{return task,err}
 if model.Hash(fresh)!=model.Hash(task){return task,errors.New("任务正在被修改，请刷新后重新确认。")}
 if err=w.call(ctx,"PATCH","/projects/"+project+"/tasks/"+task.ID,patch,&fresh);err!=nil{return task,err}
 var verified model.Task
 if err=w.call(ctx,"GET","/projects/"+project+"/tasks/"+task.ID,nil,&verified);err!=nil{return task,err}
 if !sameTime(model.Precise(verified,"start"),model.Precise(fresh,"start"))||!sameTime(model.Precise(verified,"due"),model.Precise(fresh,"due")){return task,errors.New("任务时间保存后发生变化，请核对。")}
 rule.Start=nil;rule.Due=nil;rule.StartMinutes=nil;rule.DueMinutes=nil;raw,_:=json.Marshal(rule)
 tag,err:=w.DB.Exec(ctx,"UPDATE task_rules SET config=$3::jsonb,base_fingerprint=$4 WHERE project_id=$1 AND task_id=$2 AND revision=$5",project,task.ID,string(raw),model.TaskFingerprint(verified),revision)
 if err!=nil{return task,err};if tag.RowsAffected()!=1{return task,errors.New("打卡设置发生变化，请重新核对。")}
 return verified,nil
}
