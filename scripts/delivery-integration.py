"""Run within the Action official-host fixture, using its private SQL and API helpers."""
import uuid

schema = 'plugin_data_com_selfcommand_task_checkin.'
conn = scope['connection_id']
sql = f"INSERT INTO {schema}changes(project_id,connection_id,instance_id,record_id,revision,payload) SELECT i.project_id,'{conn}',i.id,r.id,i.status_revision,jsonb_build_object('record_id',r.id,'instance_id',i.id,'task_id',i.task_id,'revision',i.status_revision) FROM {schema}records r JOIN {schema}instances i ON i.id=r.instance_id;"
cmd('docker','exec','paca-ci-db','psql','-U','postgres','-d','paca','-v','ON_ERROR_STOP=1','-c',sql)
device_one = {**paired,'X-Sync-Device':str(uuid.uuid4())}
device_two = {**paired,'X-Sync-Device':str(uuid.uuid4())}
def deliveries(headers, after=0):return native('GET',f'/checkin-api/v1/sync/deliveries?after={after}',headers=headers)
feed = deliveries(device_one)
assert len(feed['items']) == 2
row = feed['items'][0]
peer = deliveries(device_two)
assert len(peer['items']) == 2 and all(r['state']=='pending' for r in peer['items'])
action_body = {'connection_id':conn,'record_id':row['record_id'],'op_id':str(uuid.uuid4()),'base_revision':row['revision'],'action':'ignore'}
answer = native('POST','/checkin-api/v1/sync/deliveries/actions',action_body,headers=device_one)
assert answer['state']=='applied'
assert native('POST','/checkin-api/v1/sync/deliveries/actions',action_body,headers=device_one)==answer
native('POST','/checkin-api/v1/sync/deliveries/actions',{**action_body,'action':'restore'},409,headers=device_one)
changed = deliveries(device_two,peer['next_cursor'])
assert any(r['record_id']==row['record_id'] and r['policy']=='ignored' for r in changed['items'])
restore = {**action_body,'op_id':str(uuid.uuid4()),'action':'restore','base_revision':answer['revision']}
restored = native('POST','/checkin-api/v1/sync/deliveries/actions',restore,headers=device_one)
assert restored['revision']==3
native('POST','/checkin-api/v1/sync/deliveries/actions',{**restore,'op_id':str(uuid.uuid4())},409,headers=device_one)
for attempt in range(10):
    report = {'record_id':row['record_id'],'op_id':str(uuid.uuid4()),'base_revision':3,'stage':'failed','error_code':'transfer_unavailable','media_sha256':'','media_verified':False,'record_written':False,'status_verified':False}
    response = native('POST','/checkin-api/v1/sync/deliveries/report',report,headers=device_one)
    assert response['attempts']==attempt+1
    assert native('POST','/checkin-api/v1/sync/deliveries/report',report,headers=device_one)==response
assert response['state']=='needs_action'
assert all(r['state']=='pending' for r in deliveries(device_two)['items'])
assert len(request('GET',path+'/records')['items'])==2
request('POST',path+'/sync-delivery-actions',{**restore,'op_id':str(uuid.uuid4()),'action':'retry','base_revision':3},202)
for _ in range(30):
    rows = deliveries(device_one)['items']
    if any(r['record_id']==row['record_id'] and r['revision']==4 and r['attempts']==0 for r in rows):break
    time.sleep(.5)
else:raise AssertionError('manager action did not persist or execute')
native('GET','/checkin-api/v1/sync/deliveries?after=0',expected=401)
(ROOT/'verification/delivery-report.json').write_text(json.dumps({'shared_policy_across_devices':True,'independent_device_progress':True,'action_idempotency':True,'stale_revision_rejected':True,'bounded_retry':True,'failed_report_idempotency':True,'history_preserved':True,'manager_actions_durable':True},indent=2))
