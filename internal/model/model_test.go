package model

import("testing";"time")
func TestExactDeadlineAndIndependentCards(t *testing.T){
 target:=time.Date(2026,10,7,9,0,0,0,time.UTC);end:=target.Add(time.Hour)
 i:=Instance{Start:&target,Due:&end,StartMinutes:10,DueMinutes:10,State:"active"}
 for _,c:=range []struct{at time.Time;ok bool}{{target.Add(-10*time.Minute-time.Nanosecond),false},{target.Add(-10*time.Minute),true},{target.Add(-time.Nanosecond),true},{target,false},{target.Add(time.Nanosecond),false}}{
  if (i.CanSubmit("start",c.at)==nil)!=c.ok{t.Fatal(c)}
 }
 if i.CanSubmit("due",end.Add(-time.Minute))!=nil{t.Fatal("end incorrectly requires start")}
 cfg:=Config{ProgressStatus:"progress",DoneStatus:"done"}
 if DesiredStatus("start",true,cfg)!="done"{t.Fatal("late start downgraded completed task")}
 i.State="cancelled";if i.CanSubmit("start",target.Add(-time.Minute))==nil{t.Fatal("cancelled accepted")}
}
func TestDateOnlyIsNeverMidnightReminder(t *testing.T){
 task:=Task{Custom:map[string]any{"_integration_state_v1":map[string]any{"start_precision":"day","start_instant":"2026-10-07T00:00:00Z"}}}
 if Precise(task,"start")!=nil{t.Fatal("date-only got a window")}
 cfg:=Default();cfg.Enabled=true;if cfg.Valid(){t.Fatal("enabled without project states")}
}
