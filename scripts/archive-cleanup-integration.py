"""Real Paca selection UI, worker, SQL and private S3; Actions fixture only."""
import uuid

prefix='plugin_data_com_selfcommand_task_checkin.'
connection=scope['connection_id']
cleanup_pair=request('POST',path+'/pairing',{'name':'Cleanup acceptance','connection_id':connection},201)
paired={'Authorization':'Bearer '+cleanup_pair['token']}
def scalar(query):
    return subprocess.check_output(['docker','exec','paca-ci-db','psql','-U','postgres','-d','paca','-At','-v','ON_ERROR_STOP=1','-c',query],text=True).strip()
def execute(query):
    cmd('docker','exec','paca-ci-db','psql','-U','postgres','-d','paca','-v','ON_ERROR_STOP=1','-c',query)

original_media=json.loads(scalar("SELECT row_to_json(x) FROM (SELECT object_key,sha256,bytes FROM "+prefix+"media WHERE state='committed' LIMIT 1) x"))
photo_bytes=s3.get_object(Bucket='checkin-private',Key=original_media['object_key'])['Body'].read()
def fixture(title,policy='active'):
    task=request('POST',f'/projects/{project["id"]}/tasks',{'title':title},201)['data']
    instance,media,record=[str(uuid.uuid4()) for _ in range(3)]
    object_key=f'checkin/{project["id"]}/{instance}/{media}.jpg'
    source={'connection_id':connection,'source_ref':'cleanup:'+record,'path':'Tasks/'+title+'.md','snapshot':{'title':title,'dateCreated':'2026-10-10T00:00:00Z'}}
    s3.put_object(Bucket='checkin-private',Key=object_key,Body=photo_bytes,ContentType='image/jpeg')
    source_sql=json.dumps(source,ensure_ascii=False).replace("'","''")
    execute(f"INSERT INTO {prefix}instances(id,project_id,task_id,revision,fingerprint,title,source,start_minutes,due_minutes,freeze_at,state) VALUES('{instance}','{project['id']}','{task['id']}',1,'cleanup-{record}','{title}','{source_sql}',10,10,NOW(),'cancelled'); INSERT INTO {prefix}media(id,instance_id,kind,object_key,sha256,bytes,mime,state) VALUES('{media}','{instance}','due','{object_key}','{original_media['sha256']}',{len(photo_bytes)},'image/jpeg','committed'); INSERT INTO {prefix}records(id,instance_id,kind,media_id,submitted_at,status,op_id) VALUES('{record}','{instance}','due','{media}',NOW(),'synced','cleanup-fixture'); INSERT INTO {prefix}sync_policies(project_id,connection_id,record_id,state,reason) VALUES('{project['id']}','{connection}','{record}','{policy}','user_recovery'); INSERT INTO {prefix}changes(project_id,connection_id,instance_id,record_id,revision,payload) VALUES('{project['id']}','{connection}','{instance}','{record}',1,'{{}}');")
    return {'task':task['id'],'instance':instance,'media':media,'record':record,'object':object_key,'title':title}

def removed(item):
    assert scalar(f"SELECT count(*) FROM {prefix}records WHERE id='{item['record']}'")=='0'
    assert scalar(f"SELECT count(*) FROM {prefix}media WHERE id='{item['media']}'")=='0'
    assert scalar(f"SELECT state FROM {prefix}sync_purges WHERE record_id='{item['record']}'")=='complete'
    try:s3.head_object(Bucket='checkin-private',Key=item['object'])
    except Exception as error:assert error.response['ResponseMetadata']['HTTPStatusCode']==404
    else:raise AssertionError('Server photo was not deleted')
    assert request('GET',f'/projects/{project["id"]}/tasks/{item["task"]}')['data']['title']==item['title']

single=fixture('清理单条待处理')
batch=[fixture('清理批量待处理'),fixture('清理批量已忽略','ignored'),fixture('清理批量已归档','archived_deleted')]
peer_headers={**paired,'X-Sync-Device':str(uuid.uuid4())}
peer_before=native('GET','/checkin-api/v1/sync/deliveries?after=0',headers=peer_headers)
page.get_by_role('button',name='刷新记录',exact=True).click()
select=page.get_by_role('button',name='选择记录：清理单条待处理 · 结束打卡',exact=True)
expect(select).to_be_visible()
assert select.bounding_box()['height']>=44
select.click()
expect(select).to_have_attribute('aria-pressed','true')
page.get_by_role('button',name='彻底清理所选记录 (1)',exact=True).click()
expect(page.get_by_text('清理单条待处理 · 结束打卡',exact=False).last).to_be_visible()
page.get_by_role('button',name='取消',exact=True).last.click()
assert scalar(f"SELECT count(*) FROM {prefix}records WHERE id='{single['record']}'")=='1'
page.get_by_role('button',name='彻底清理所选记录 (1)',exact=True).click()
page.get_by_role('button',name='确认永久删除',exact=True).click()
expect(select).to_have_count(0,timeout=30000)
removed(single)

for item in batch:
    selected=page.get_by_role('button',name='选择记录：'+item['title']+' · 结束打卡',exact=True)
    selected.click()
    expect(selected).to_have_attribute('aria-pressed','true')
# Selection remains stable after the previous manager-queue refresh timer fires.
page.wait_for_timeout(6500)
for item in batch:
    expect(page.get_by_role('button',name='选择记录：'+item['title']+' · 结束打卡',exact=True)).to_have_attribute('aria-pressed','true')
page.get_by_role('button',name='彻底清理所选记录 (3)',exact=True).click()
page.get_by_role('button',name='确认永久删除',exact=True).click()
for item in batch:
    expect(page.get_by_role('button',name='选择记录：'+item['title']+' · 结束打卡',exact=True)).to_have_count(0,timeout=30000)
    removed(item)

direct=fixture('配对设备直接清理','ignored')
body={'connection_id':connection,'record_id':direct['record'],'op_id':str(uuid.uuid4()),'base_revision':1,'action':'purge'}
answer=native('POST','/checkin-api/v1/sync/deliveries/actions',body,headers=peer_headers)
assert answer['state']=='purging'
assert native('POST','/checkin-api/v1/sync/deliveries/actions',body,headers=peer_headers)==answer
for _ in range(60):
    if scalar(f"SELECT state FROM {prefix}sync_purges WHERE record_id='{direct['record']}'")=='complete':break
    time.sleep(.5)
else:raise AssertionError('Device purge did not finish')
removed(direct)
assert native('POST','/checkin-api/v1/sync/deliveries/actions',body,headers=peer_headers)['state']=='purged'
offline=native('GET',f'/checkin-api/v1/sync/deliveries?after={peer_before["next_cursor"]}',headers=peer_headers)
assert {x['record'] for x in [single,*batch,direct]}.issubset({x['record_id'] for x in offline['items'] if x['policy']=='purged'})
native('POST','/checkin-api/v1/sync/deliveries/actions',{**body,'connection_id':str(uuid.uuid4()),'op_id':str(uuid.uuid4())},400,headers=peer_headers)
native('POST','/checkin-api/v1/sync/deliveries/actions',body,401)

page.get_by_role('button',name='全选记录',exact=True).click()
assert all(value=='true' for value in page.get_by_role('button',name='选择记录：',exact=False).evaluate_all('(elements)=>elements.map(e=>e.getAttribute("aria-pressed"))'))
page.get_by_role('button',name='取消选择',exact=True).click()
page.screenshot(path=str(ROOT/'verification/archive-cleanup-selection.png'),full_page=True)
(ROOT/'verification/archive-cleanup-report.json').write_text(json.dumps({'source_sha':os.environ['GITHUB_SHA'],'real_paca_single_and_batch_ui':True,'selection_target_at_least_44px':True,'active_ignored_archived_selectable':True,'selection_survives_refresh':True,'cancel_preserves_record':True,'record_and_private_photo_removed':True,'ordinary_tasks_preserved':True,'paired_device_direct_purge':True,'request_idempotency':True,'offline_device_tombstones':True,'cross_connection_and_unauthenticated_denied':True},indent=2))
