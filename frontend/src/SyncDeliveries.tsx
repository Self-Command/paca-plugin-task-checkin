import {useCallback,useEffect,useRef,useState} from "react";
import {Button} from "./components/ui/button";
import {Card,CardHeader,CardTitle,CardContent} from "./components/ui/card";
import {Select,SelectTrigger,SelectValue,SelectContent,SelectItem} from "./components/ui/select";
import {api} from "./api";

type Device={name:string;state:string;attempts:number;error_code:string;media_verified:boolean;record_written:boolean;status_verified:boolean};
type Binding={path:string;note_created:string;title:string};
type Row={record_id:string;connection_id:string;title:string;kind:string;submitted_at:string;policy:string;reason:string;revision:number;paca_status:string;devices:Device[];photo_bytes?:number;cleanup_error?:string};
type Confirmation={action:"ignore"|"purge";items:Row[]};
const stages:Record<string,string>={pending:"等待同步",retry_wait:"等待重试",needs_action:"需要处理",confirmed:"回写完成",ignored:"已忽略",archived_deleted:"已归档",purging:"正在清理"};
const errors:Record<string,string>={transfer_unavailable:"照片传输中断",unavailable:"服务暂不可用",photo:"照片校验未完成",missing:"任务笔记需要关联",source_missing:"任务来源需要关联",block:"打卡区块有修改",status:"任务状态有冲突",token:"授权已失效"};
const kind=(row:Row)=>row.kind==="start"?"开始打卡":"结束打卡";
function status(row:Row):string{
 if(row.policy!=="active")return stages[row.policy]??"等待同步";
 if(row.reason==="source_missing"||row.devices.some(d=>d.state==="needs_action"))return "需要处理";
 if(row.devices.length&&row.devices.every(d=>d.state==="confirmed"))return "回写完成";
 return row.devices.some(d=>d.state==="retry_wait")?"等待重试":"等待同步";
}

export default function SyncDeliveries({path}:{path:string}){
 const [rows,setRows]=useState<Row[]>([]),[selected,setSelected]=useState<Set<string>>(new Set()),[busy,setBusy]=useState(false),[message,setMessage]=useState("");
 const [confirmation,setConfirmation]=useState<Confirmation|null>(null),[tracking,setTracking]=useState(false);
 const [bindings,setBindings]=useState<Record<string,Binding[]>>({}),[target,setTarget]=useState<Record<string,string>>({});
 const dialog=useRef<HTMLDialogElement>(null),trackingIds=useRef<string[]>([]),partialFailure=useRef(false);
 const load=useCallback(async()=>{
  const items=(await api<{items:Row[]}>(path+"/sync-deliveries")).items;
  setRows(old=>JSON.stringify(old)===JSON.stringify(items)?old:items);
  setSelected(old=>new Set([...old].filter(id=>items.some(row=>row.record_id===id))));
  if(trackingIds.current.length&&!trackingIds.current.some(id=>items.some(row=>row.record_id===id))){
   trackingIds.current=[];setTracking(false);
   if(!partialFailure.current)setMessage("清理完成。");
  }
 },[path]);
 useEffect(()=>{void load().catch(()=>setMessage("暂时无法加载，请稍后刷新。"))},[load]);
 const cleaning=rows.some(row=>row.policy==="purging");
 useEffect(()=>{
  if(!cleaning&&!tracking)return;
  const timer=window.setInterval(()=>void load().catch(()=>{}),3000);
  return()=>window.clearInterval(timer);
 },[load,cleaning,tracking]);
 useEffect(()=>{
  if(confirmation&&!dialog.current?.open)dialog.current?.showModal();
  if(!confirmation&&dialog.current?.open)dialog.current.close();
 },[confirmation]);
 const associate=async(row:Row)=>{
  if(!bindings[row.connection_id]){
   try{
    const project=path.split("/projects/")[1];
    const reply=await api<{items:Binding[]}>("/api/v1/plugins/com.selfcommand.tasknotes-webhook/projects/"+project+"/connections/"+row.connection_id+"/note-bindings");
    setBindings(old=>({...old,[row.connection_id]:reply.items}));
    setMessage(reply.items.length?"请选择对应笔记。":"没有可关联的笔记，请在 Obsidian 中选择。");
   }catch{setMessage("暂时无法加载笔记，请在 Obsidian 中重新关联。")}
   return;
  }
  const binding=bindings[row.connection_id].find(b=>b.path===target[row.record_id]);
  if(!binding){setMessage("请选择对应笔记。");return;}
  setBusy(true);
  try{
   await api(path+"/sync-delivery-actions",{connection_id:row.connection_id,record_id:row.record_id,op_id:crypto.randomUUID(),action:"associate",base_revision:row.revision,path:binding.path,note_created:binding.note_created});
   setMessage("关联已提交。");await load();
  }catch{setMessage("关联未完成，请刷新后重试。")}finally{setBusy(false)}
 };
 const act=async(action:string,items:Row[],confirmed=false)=>{
  if(!items.length)return;
  if((action==="ignore"||action==="purge")&&!confirmed){setConfirmation({action,items});return;}
  setConfirmation(null);setBusy(true);setMessage("");partialFailure.current=false;
  let submitted=0;
  try{
   for(const row of items){
    await api(path+"/sync-delivery-actions",{connection_id:row.connection_id,record_id:row.record_id,op_id:crypto.randomUUID(),action,base_revision:row.revision});
    submitted++;
    if(action==="purge"){trackingIds.current.push(row.record_id);setTracking(true)}
   }
   setSelected(new Set());setMessage(action==="purge"?"正在清理 "+submitted+" 条记录…":"操作已提交。");
   if(action!=="purge")window.setTimeout(()=>void load().catch(()=>{}),6000);
   await load();
  }catch(error){partialFailure.current=true;setMessage("已提交 "+submitted+" 条；"+(error instanceof Error?error.message:"其余记录未处理，请重试。"))}finally{setBusy(false)}
 };
 const selectable=rows.filter(row=>row.policy!=="purging"),selectedRows=selectable.filter(row=>selected.has(row.record_id));
 const allSelected=selectable.length>0&&selectable.every(row=>selected.has(row.record_id));
 return <Card>
  <CardHeader><CardTitle>打卡同步记录</CardTitle></CardHeader>
  <CardContent className="grid gap-3">
   <div className="sync-toolbar">
    <Button variant="outline" disabled={busy||!selectable.length} onClick={()=>setSelected(allSelected?new Set():new Set(selectable.map(row=>row.record_id)))}>{allSelected?"取消选择":"全选记录"}</Button>
    <span className="sync-selection-count" aria-live="polite">{selectedRows.length?"已选 "+selectedRows.length+" 条":"共 "+rows.length+" 条"}</span>
    <div className="sync-toolbar-actions">
     {selectedRows.length>0&&<Button variant="destructive" aria-label={"彻底清理所选记录 ("+selectedRows.length+")"} disabled={busy} onClick={()=>void act("purge",selectedRows)}>清理所选 ({selectedRows.length})</Button>}
     <Button variant="ghost" aria-label="刷新记录" disabled={busy} onClick={()=>void load().catch(()=>setMessage("刷新未完成，请重试。"))}>刷新</Button>
    </div>
   </div>
   {message&&<p className="sync-feedback" role="status">{message}</p>}
   <div className="sync-record-list">
    {rows.length?rows.map(row=><div className="sync-record-card" data-selected={selected.has(row.record_id)} key={row.record_id}>
     <button type="button" aria-pressed={selected.has(row.record_id)} aria-label={"选择记录："+row.title+" · "+kind(row)} className="sync-record-select" disabled={busy||row.policy==="purging"} onClick={()=>setSelected(old=>{const next=new Set(old);if(next.has(row.record_id))next.delete(row.record_id);else next.add(row.record_id);return next})}>
      <span aria-hidden="true" className="sync-check">{selected.has(row.record_id)?"✓":""}</span>
      <span className="sync-record-heading"><strong>{row.title}</strong><span>{kind(row)} · {new Date(row.submitted_at).toLocaleString()}</span></span>
      <span className="sync-badge" data-status={status(row)}>{status(row)}</span>
     </button>
     <div className="sync-record-actions">
      {row.policy==="purging"?row.cleanup_error&&<Button variant="outline" disabled={busy} onClick={()=>void act("purge",[row])}>重试清理</Button>:<>
       {row.policy==="active"&&status(row)!=="回写完成"&&<><Button variant="outline" disabled={busy} onClick={()=>void act("retry",[row])}>重试</Button><Button variant="ghost" disabled={busy} onClick={()=>void act("ignore",[row])}>忽略</Button></>}
       {row.policy!=="active"&&<Button variant="outline" disabled={busy} onClick={()=>void act("restore",[row])}>恢复同步</Button>}
       <Button variant="outline" aria-label="彻底清理" disabled={busy} onClick={()=>void act("purge",[row])}>清理</Button>
      </>}
     </div>
     {row.cleanup_error&&<p className="sync-feedback" role="status">{row.cleanup_error}</p>}
     {row.policy!=="purging"&&<details className="sync-details">
      <summary>同步详情</summary>
      <div className="sync-device-list">
       {row.devices.length?row.devices.map((d,i)=><div key={i}><strong>{d.name}</strong><span>{stages[d.state]??"等待同步"}{d.error_code?" · "+(errors[d.error_code]??"需要处理"):""}</span><span>照片{d.media_verified?"已保存":"待保存"} · 记录{d.record_written?"已写入":"待写入"} · 状态{d.status_verified?"已回写":"待回写"}</span></div>):<p>等待设备同步</p>}
       <Button variant="outline" disabled={busy} onClick={()=>void associate(row)}>重新关联笔记</Button>
       {bindings[row.connection_id]?.length>0&&<Select value={target[row.record_id]??""} onValueChange={value=>setTarget(old=>({...old,[row.record_id]:value}))}><SelectTrigger><SelectValue placeholder="选择任务笔记"/></SelectTrigger><SelectContent>{bindings[row.connection_id].map(note=><SelectItem key={note.path} value={note.path}>{note.title} · {note.path}</SelectItem>)}</SelectContent></Select>}
      </div>
     </details>}
    </div>):<p className="sync-empty">暂无同步记录</p>}
   </div>
   <dialog ref={dialog} className="sync-confirm" onCancel={()=>setConfirmation(null)}>
    {confirmation&&<>
     <h3>{confirmation.action==="purge"?"清理 "+confirmation.items.length+" 条记录？":"忽略同步记录？"}</h3>
     <p>{confirmation.action==="purge"?"删除服务器记录及照片，保留任务和本地笔记、附件。此操作无法撤销。":"停止所选记录的同步，保留记录和照片，可恢复同步。"}</p>
     <div className="sync-confirm-list">{confirmation.items.map(row=><div key={row.record_id}><strong>{row.title} · {kind(row)}</strong><span>{new Date(row.submitted_at).toLocaleString()}</span></div>)}</div>
     <div className="sync-confirm-actions"><Button variant="outline" onClick={()=>setConfirmation(null)}>取消</Button><Button variant={confirmation.action==="purge"?"destructive":"default"} onClick={()=>void act(confirmation.action,confirmation.items,true)}>{confirmation.action==="purge"?"确认永久删除":"确认忽略"}</Button></div>
    </>}
   </dialog>
  </CardContent>
 </Card>;
}

