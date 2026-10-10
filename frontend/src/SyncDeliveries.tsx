import {useEffect,useState} from "react";
import {Button} from "./components/ui/button";
import {Card,CardHeader,CardTitle,CardDescription,CardContent} from "./components/ui/card";
import {Select,SelectTrigger,SelectValue,SelectContent,SelectItem} from "./components/ui/select";
import {api} from "./api";

type Device={name:string;state:string;attempts:number;error_code:string;media_verified:boolean;record_written:boolean;status_verified:boolean};
type Binding={path:string;note_created:string;title:string};
type Row={record_id:string;connection_id:string;title:string;kind:string;submitted_at:string;policy:string;reason:string;revision:number;paca_status:string;devices:Device[];photo_bytes?:number;cleanup_error?:string};
type Confirmation={action:"ignore"|"purge";items:Row[];hasMore?:boolean};
const stages:Record<string,string>={pending:"等待同步",retry_wait:"等待重试",needs_action:"需要处理",confirmed:"回写完成",ignored:"已忽略",archived_deleted:"任务已删除，已归档",purging:"正在彻底清理"};
const errors:Record<string,string>={transfer_unavailable:"照片传输中断",unavailable:"服务暂不可用",photo:"照片校验未完成",missing:"任务笔记需要关联",source_missing:"任务来源需要关联",block:"打卡区块有修改",status:"任务状态有冲突",token:"授权已失效"};

export default function SyncDeliveries({path}:{path:string}){
 const [rows,setRows]=useState<Row[]>([]),[selected,setSelected]=useState<Set<string>>(new Set()),[busy,setBusy]=useState(false),[message,setMessage]=useState("");
 const [confirmation,setConfirmation]=useState<Confirmation|null>(null);
 const [bindings,setBindings]=useState<Record<string,Binding[]>>({}),[target,setTarget]=useState<Record<string,string>>({});
 const load=async()=>{
  const items=(await api<{items:Row[]}>(path+"/sync-deliveries")).items;
  setRows(old=>JSON.stringify(old)===JSON.stringify(items)?old:items);
  setSelected(old=>new Set([...old].filter(id=>items.some(row=>row.record_id===id))));
 };
 useEffect(()=>{
  void load().catch(()=>setMessage("暂时无法加载同步记录，请稍后重试。"));
 },[path]);
 const cleaning=rows.some(row=>row.policy==='purging');
 useEffect(()=>{
  if(!cleaning)return;
  const timer=window.setInterval(()=>void load().catch(()=>{}),3000);
  return()=>window.clearInterval(timer);
 },[path,cleaning]);
 const associate=async(row:Row)=>{
  if(!bindings[row.connection_id]){
   try{
    const project=path.split("/projects/")[1];
    const reply=await api<{items:Binding[]}>("/api/v1/plugins/com.selfcommand.tasknotes-webhook/projects/"+project+"/connections/"+row.connection_id+"/note-bindings");
    setBindings(old=>({...old,[row.connection_id]:reply.items}));
    setMessage(reply.items.length?"请选择对应任务笔记。":"没有可关联的笔记，请在 Obsidian 中选择。");
   }catch{setMessage("暂时无法加载笔记，请在 Obsidian 中重新关联。")}
   return;
  }
  const binding=bindings[row.connection_id].find(b=>b.path===target[row.record_id]);
  if(!binding){setMessage("请选择对应的任务笔记。");return;}
  setBusy(true);
  try{
   await api(path+"/sync-delivery-actions",{connection_id:row.connection_id,record_id:row.record_id,op_id:crypto.randomUUID(),action:"associate",base_revision:row.revision,path:binding.path,note_created:binding.note_created});
   setMessage("关联请求已提交，只同步打卡记录和照片。");
   await load();
  }catch{setMessage("暂时无法保存关联，请刷新后重试。")}finally{setBusy(false)}
 };
 const act=async(action:string,items:Row[],confirmed=false)=>{
  if(!items.length)return;
  if((action==="ignore"||action==="purge")&&!confirmed){setConfirmation({action,items});return;}
  setConfirmation(null);setBusy(true);setMessage("");
  let submitted=0;
  try{
   for(const row of items){
    await api(path+"/sync-delivery-actions",{connection_id:row.connection_id,record_id:row.record_id,op_id:crypto.randomUUID(),action,base_revision:row.revision});
    submitted++;
   }
   setSelected(new Set());
   setMessage(action==="purge"?`已提交 ${submitted} 条清理请求，后台会删除记录和服务器照片。`:"处理请求已提交。");
   // Manager requests enter the worker queue. Refresh after it has run once.
   window.setTimeout(()=>void load().catch(()=>{}),6000);
   await load();
  }catch(error){setMessage(`已提交 ${submitted} 条；`+(error instanceof Error?error.message:"其余记录暂未处理，请刷新后重试。"))}finally{setBusy(false)}
 };
 const previewArchived=async()=>{
  setBusy(true);setMessage("");
  try{
   const reply=await api<{items:Row[];has_more:boolean}>(path+"/archived-sync-records");
   if(reply.items.length)setConfirmation({action:"purge",items:reply.items,hasMore:reply.has_more});
   else setMessage("没有可清理的归档记录。");
  }catch(error){setMessage(error instanceof Error?error.message:"暂时无法读取归档记录。")}finally{setBusy(false)}
 };
 const selectedActive=rows.filter(row=>selected.has(row.record_id)&&row.policy==="active");
 const selectable=rows.filter(row=>row.policy!=="purging");
 const selectedRows=selectable.filter(row=>selected.has(row.record_id));
 return <Card>
  <CardHeader><CardTitle>Obsidian 同步处理</CardTitle><CardDescription>查看照片和记录回写进度，处理失败记录。</CardDescription></CardHeader>
  <CardContent className="grid gap-3">
   <div className="flex flex-wrap gap-2">
    <Button variant="outline" disabled={busy} onClick={()=>void load().catch(()=>setMessage("暂时无法刷新，请稍后重试。"))}>刷新记录</Button>
    <Button variant="outline" disabled={busy||!selectedActive.length} onClick={()=>void act("ignore",selectedActive)}>忽略所选故障项</Button>
    <Button variant="outline" disabled={busy||!selectable.length} onClick={()=>setSelected(new Set(selectable.map(row=>row.record_id)))}>全选记录</Button>
    <Button variant="outline" disabled={busy||!selected.size} onClick={()=>setSelected(new Set())}>取消选择</Button>
    <Button variant="destructive" disabled={busy||!selectedRows.length} onClick={()=>void act("purge",selectedRows)}>彻底清理所选记录{selectedRows.length?` (${selectedRows.length})`:""}</Button>
    <Button variant="outline" disabled={busy} onClick={()=>void previewArchived()}>清理已归档记录</Button>
   </div>
   <p className="text-sm" role="status">已选择 {selectedRows.length} 条记录。点击下方任务标题区域即可选择。</p>
   {confirmation&&<Card>
    <CardHeader>
     <CardTitle>{confirmation.action==="purge"?"确认彻底清理打卡记录":"确认忽略同步记录"}</CardTitle>
     <CardDescription>{confirmation.action==="purge"?"永久删除以下打卡记录及服务器照片，无法恢复。已保存到 Obsidian 的笔记和附件保留。":"同一笔记库的设备会停止重试所选记录，任务、记录和照片保留，可恢复同步。"}</CardDescription>
    </CardHeader>
    <CardContent className="grid gap-3">
     <p>本次处理 {confirmation.items.length} 条记录{confirmation.action==="purge"?`，照片约 ${(confirmation.items.reduce((sum,row)=>sum+Number(row.photo_bytes??0),0)/1048576).toFixed(2)} MB`:""}。</p>
     <div className="max-h-64 overflow-y-auto">{confirmation.items.map(row=><p key={row.record_id}>{row.title} · {row.kind==="start"?"开始打卡":"结束打卡"} · {new Date(row.submitted_at).toLocaleString()}</p>)}</div>
     {confirmation.hasMore&&<p>还有更多归档记录，本次最多清理 500 条，完成后可继续清理。</p>}
     <div className="flex gap-2">
      <Button variant="outline" disabled={busy} onClick={()=>setConfirmation(null)}>取消</Button>
      <Button disabled={busy} variant={confirmation.action==="purge"?"destructive":"default"} onClick={()=>void act(confirmation.action,confirmation.items,true)}>{confirmation.action==="purge"?"确认永久删除":"确认忽略"}</Button>
     </div>
    </CardContent>
   </Card>}
   {rows.length?rows.map(row=><div className="grid gap-2 rounded-lg border p-3" key={row.record_id}>
    <button type="button" aria-pressed={selected.has(row.record_id)} aria-label={`选择记录：${row.title} · ${row.kind==="start"?"开始打卡":"结束打卡"}`} className="flex min-h-12 w-full cursor-pointer items-center gap-3 rounded-md border p-3 text-left aria-pressed:border-primary aria-pressed:bg-accent disabled:cursor-default disabled:opacity-60" disabled={busy||row.policy==="purging"} onClick={()=>setSelected(old=>{const next=new Set(old);if(next.has(row.record_id))next.delete(row.record_id);else next.add(row.record_id);return next})}>
     <span aria-hidden="true" className="flex size-6 shrink-0 items-center justify-center rounded border text-lg">{selected.has(row.record_id)?"✓":""}</span>
     <span className="flex-1">{row.title} · {row.kind==="start"?"开始打卡":"结束打卡"}</span>
     <span className="text-sm">{selected.has(row.record_id)?"已选":"选择"}</span>
    </button>
    <p className="text-sm text-muted-foreground">{new Date(row.submitted_at).toLocaleString()} · {row.paca_status==="synced"?"Paca 状态已更新":"Paca 状态更新中"}</p>
    {row.policy!=="active"?<p className="text-sm">{stages[row.policy]}</p>:row.devices.length?row.devices.map((d,i)=><div key={i} className="text-sm">
     <p>{d.name}：{stages[d.state]??"等待同步"}{d.error_code?" · "+(errors[d.error_code]??"需要处理同步记录"):""}</p>
     <p className="text-muted-foreground">照片：{d.media_verified?"已保存":"待保存"} · 记录：{d.record_written?"已写入":"待写入"} · 状态：{d.status_verified?"已回写":"尚未回写"}</p>
    </div>):<p className="text-sm text-muted-foreground">{row.reason==="source_missing"?"任务来源需要关联，请重新关联笔记或忽略此同步项。":"等待设备同步"}</p>}
    {row.cleanup_error&&<p className="text-sm" role="status">{row.cleanup_error}</p>}
    <div className="flex flex-wrap gap-2">
     {row.policy==="purging"?row.cleanup_error&&<Button size="sm" variant="outline" disabled={busy} onClick={()=>void act("purge",[row])}>重试清理</Button>:<>
      {row.policy==="active"?<>
       <Button size="sm" variant="outline" disabled={busy} onClick={()=>void act("retry",[row])}>重试同步</Button>
       <Button size="sm" variant="outline" disabled={busy} onClick={()=>void act("ignore",[row])}>忽略此同步项</Button>
      </>:<Button size="sm" variant="outline" disabled={busy} onClick={()=>void act("restore",[row])}>恢复同步</Button>}
      <Button variant="destructive" disabled={busy} onClick={()=>void act("purge",[row])}>彻底清理</Button>
      <Button size="sm" variant="outline" disabled={busy} onClick={()=>void associate(row)}>重新关联笔记</Button>
     </>}
    </div>
    {row.policy!=="purging"&&bindings[row.connection_id]?.length>0&&<Select value={target[row.record_id]??""} onValueChange={value=>setTarget(old=>({...old,[row.record_id]:value}))}>
     <SelectTrigger><SelectValue placeholder="选择对应任务笔记"/></SelectTrigger>
     <SelectContent>{bindings[row.connection_id].map(note=><SelectItem key={note.path} value={note.path}>{note.title} · {note.path}</SelectItem>)}</SelectContent>
    </Select>}
   </div>):<p className="text-sm text-muted-foreground">暂无同步记录</p>}
   {message&&<p className="text-sm" role="status">{message}</p>}
  </CardContent>
 </Card>;
}
