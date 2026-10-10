"""Official host + native worker + actual private RustFS; only executed by Actions."""
import base64,datetime,threading
from playwright.sync_api import sync_playwright,expect
import boto3
cmd('docker','run','-d','--name','paca-ci-db-forward','--network','paca-ci','-p','127.0.0.1:15432:5432','alpine/socat','tcp-listen:5432,fork,reuseaddr','tcp-connect:paca-ci-db:5432')
cmd('docker','run','-d','--name','paca-ci-storage','--network','paca-ci','-p','127.0.0.1:19000:9000','--tmpfs','/data/rustfs0:mode=777','-e','RUSTFS_ACCESS_KEY=ci-access-key','-e','RUSTFS_SECRET_KEY=ci-secret-key','-e','RUSTFS_VOLUMES=/data/rustfs0','rustfs/rustfs:1.0.0-rc.6')
s3=boto3.client('s3',endpoint_url='http://127.0.0.1:19000',aws_access_key_id='ci-access-key',aws_secret_access_key='ci-secret-key',region_name='us-east-1')
for _ in range(60):
    try:s3.create_bucket(Bucket='checkin-private');break
    except Exception:time.sleep(1)
else:raise RuntimeError('private S3 unavailable')
key=request('POST','/users/me/api-keys',{'name':'Isolated check-in worker'},201)['data']['key']
secret_dir=ROOT/'ci-secrets';secret_dir.mkdir(mode=0o700,exist_ok=True)
values={'api-key':key,'worker-secret':worker_secret,'grant-secret':secrets.token_hex(32),'action-secret':secrets.token_hex(32),'storage-access':'ci-access-key','storage-secret':'ci-secret-key'}
for name,value in values.items():
    f=secret_dir/name;f.write_text(value);f.chmod(0o600)
from http.server import ThreadingHTTPServer,BaseHTTPRequestHandler
faults={'fail_status':1,'drop_status':1}
source_fault={}
source_link_fault={}
class FaultProxy(BaseHTTPRequestHandler):
    def log_message(self,*args):pass
    def forward(self):
        body=self.rfile.read(int(self.headers.get('Content-Length','0'))) or None
        if self.path.endswith('/source-link') and source_link_fault:
            payload=json.dumps(source_link_fault).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(payload)));self.end_headers();self.wfile.write(payload);return
        if self.path.endswith('/source-status') and source_fault:
            status=source_fault.get('http',200);payload=json.dumps({k:v for k,v in source_fault.items() if k not in ('http','core_http')}).encode()
            self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(payload)));self.end_headers();self.wfile.write(payload);return
        if self.command=='GET' and '/tasks/' in self.path and not '/plugins/' in self.path and source_fault.get('core_http'):
            payload=b'{"error":"isolated core lookup fixture"}';self.send_response(source_fault['core_http']);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(payload)));self.end_headers();self.wfile.write(payload);return
        status_patch=self.command=='PATCH'  and '/tasks/' in self.path and 'status_id' in json.loads(body or b'{}')
        if status_patch and faults['fail_status']:
            faults['fail_status']-=1;self.send_response(503);self.end_headers();self.wfile.write(b'{"error":"injected CI status outage"}');return
        req=urllib.request.Request('http://localhost:18080'+self.path,data=body,method=self.command,headers={k:v for k,v in self.headers.items() if k.lower() not in ('host','content-length','connection')})
        try:
            with urllib.request.urlopen(req,timeout=20) as result:status,payload=result.status,result.read()
        except urllib.error.HTTPError as error:status,payload=error.code,error.read()
        if status_patch and faults['drop_status']:
            faults['drop_status']-=1;self.close_connection=True;return
        self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(payload)));self.end_headers();self.wfile.write(payload)
    do_GET=forward;do_POST=forward;do_PUT=forward;do_PATCH=forward;do_DELETE=forward
proxy=ThreadingHTTPServer(('127.0.0.1',18180),FaultProxy);threading.Thread(target=proxy.serve_forever,daemon=True).start()
worker_env={**os.environ,'PACA_API_URL':'http://127.0.0.1:18180','PUBLIC_URL':'http://127.0.0.1:18082','DATABASE_URL':'postgres://postgres:ci-only-password@127.0.0.1:15432/paca?sslmode=disable','CHECKIN_TEST_MODE':'true','LISTEN_ADDR':'127.0.0.1:18082','CHECKIN_BUCKET':'checkin-private','CHECKIN_S3_ENDPOINT':'http://127.0.0.1:19000','CHECKIN_WEB_DIR':str(ROOT/'frontend/web-dist')}
for env_name,file_name in [('PACA_API_KEY','api-key'),('WORKER_SECRET','worker-secret'),('GRANT_SECRET','grant-secret'),('ACTION_SECRET','action-secret'),('STORAGE_ACCESS_KEY','storage-access'),('STORAGE_SECRET_KEY','storage-secret')]:worker_env[env_name+'_FILE']=str(secret_dir/file_name)
# The compatibility job builds the web output again from the same checkout.
subprocess.run(['bun','install','--frozen-lockfile'],cwd=ROOT/'frontend',check=True)
subprocess.run(['bun','run','build'],cwd=ROOT/'frontend',check=True)
log=(ROOT/'worker-test.log').open('w')
worker=subprocess.Popen(['/tmp/checkin-worker'],env=worker_env,stdout=log,stderr=subprocess.STDOUT)
def native(method,path,body=None,expected=200,headers=None,opener=None):
    data=body if isinstance(body,bytes) else json.dumps(body).encode() if body is not None else None
    req=urllib.request.Request('http://127.0.0.1:18082'+path,data=data,method=method,headers={'Content-Type':'application/json','Origin':'http://127.0.0.1:18082',**(headers or {})})
    try:
        with (opener or urllib.request.build_opener()).open(req,timeout=20) as response:status,payload=response.status,response.read()
    except urllib.error.HTTPError as error:status,payload=error.code,error.read()
    if status!=expected:raise AssertionError(f'{method} {path}: expected {expected}, got {status}: {payload[:400]!r}')
    return json.loads(payload) if payload else {}
for _ in range(60):
    try:native('GET','/healthz');break
    except Exception:
        if worker.poll() is not None:raise RuntimeError((ROOT/'worker-test.log').read_text())
        time.sleep(1)
else:raise RuntimeError('native worker readiness failed')
auth={'Authorization':'Bearer '+values['action-secret']}
now=datetime.datetime.now(datetime.timezone.utc)
start=(now+datetime.timedelta(minutes=3)).isoformat()
due=(now+datetime.timedelta(minutes=4)).isoformat()
task=request('POST',f'/projects/{project["id"]}/tasks',{'title':'独立双卡验收','importance':75,'tags':['学习','每日记录'],'description':[{'type':'paragraph','content':[{'type':'text','text':'阅读第五章，整理三个要点。'}],'children':[]}]},201)['data']
task_path=path+f'/tasks/{task["id"]}'
rule={'enabled':True,'start':start,'due':due}
request('PUT',task_path+'/checkin',{'config':rule,'revision':0})
request('PUT',task_path+'/checkin',{'config':rule,'revision':1})
request('PUT',task_path+'/checkin',{'config':rule,'revision':1},409)
def action(kind,target):return native('POST','/internal/v1/action',{'project_id':project['id'],'task_id':task['id'],'kind':kind,'target':target},headers=auth)
# Reproduce AI zero-minute metadata bypass: reject before any instance/grant exists.
zero=request('POST',f'/projects/{project["id"]}/tasks',{'title':'零分钟打卡拒绝','start_date':start,'custom_fields':{'_integration_state_v1':{'version':2,'source':'paca','timezone':'Asia/Shanghai','start_precision':'instant','start_instant':start,'start_core_date':start[:10],'reminder_start_minutes':0}}},201)['data']
native('POST','/internal/v1/action',{'project_id':project['id'],'task_id':zero['id'],'kind':'start','target':start},expected=409,headers=auth)
rows=subprocess.check_output(['docker','exec','paca-ci-db','psql','-U','postgres','-d','paca','-Atc',"SELECT count(*) FROM plugin_data_com_selfcommand_task_checkin.instances WHERE task_id='"+zero['id']+"'"],text=True).strip()
assert rows=='0','invalid zero lead created a check-in instance'
request('DELETE',f'/projects/{project["id"]}/tasks/{zero["id"]}')
end_action=action('due',due)
start_action=action('start',start)
card_headers={'X-Checkin-Instance':end_action['instance_id'],'X-Checkin-Kind':'due'}
assert end_action['instance_id']==start_action['instance_id']
card=json.loads(end_action['metadata']['task_card']);assert card['priority']=='高' and card['content']=='阅读第五章，整理三个要点。'
assert action('due',due)['metadata']==end_action['metadata'],'retry changed authorization snapshot'
import struct,zlib
def chunk(kind,data):return struct.pack('!I',len(data))+kind+data+struct.pack('!I',zlib.crc32(kind+data)&0xffffffff)
png=b'\x89PNG\r\n\x1a\n'+chunk(b'IHDR',struct.pack('!IIBBBBB',1,1,8,2,0,0,0))+chunk(b'IDAT',zlib.compress(b'\x00\xff\x00\x00'))+chunk(b'IEND',b'')
# Browser navigates the passwordless fragment link and uses the real UI upload/submit.
with sync_playwright() as pw:
    browser=pw.chromium.launch()
    context=browser.new_context(viewport={'width':390,'height':844})
    page=context.new_page();page.on('pageerror',lambda error:print('browser error:',error));page.goto(end_action['metadata']['action_url'],wait_until='networkidle')
    (ROOT/'verification').mkdir(exist_ok=True);page.screenshot(path=str(ROOT/'verification/checkin-initial.png'),full_page=True)
    expect(page.get_by_role('heading',name='独立双卡验收')).to_be_visible()
    expect(page.get_by_text('任务内容',exact=True)).to_be_visible()
    expect(page.get_by_text('阅读第五章，整理三个要点。',exact=True)).to_be_visible()
    assert 'UUID' not in page.locator('main').inner_text()
    assert not page.url.split('#')[-1].startswith('token=')
    page.locator('input[type=file]').last.set_input_files({'name':'check.png','mimeType':'image/png','buffer':png})
    with page.expect_request(lambda r:r.url.endswith('/checkin-api/v1/submit') and r.method=='POST') as submitted_request:
        page.get_by_role('button',name='确认打卡',exact=True).click()
    expect(page.get_by_role('status').filter(has_text='打卡成功')).to_be_visible(timeout=30000)
    (ROOT/'verification').mkdir(exist_ok=True);page.screenshot(path=str(ROOT/'verification/checkin-mobile.png'),full_page=True)
    # A failed status PATCH preserves the successful card. Accelerate only the isolated
    # retry timer; the real worker still dispatches the durable outbox via official API.
    time.sleep(6)
    assert faults['fail_status']==0,'status failure fixture was not exercised'
    cmd('docker','exec','paca-ci-db','psql','-U','postgres','-d','paca','-c',"UPDATE plugin_data_com_selfcommand_task_checkin.outbox SET next_attempt=NOW() WHERE state='pending'")
    # End succeeds first; start is still independently authorized after Paca is done.
    for _ in range(12):
        current=request('GET',f'/projects/{project["id"]}/tasks/{task["id"]}')['data']
        if current['status_id']==done:break
        time.sleep(1)
    assert current['status_id']==done,'durable outbox did not update Paca'
    page2=context.new_page();page2.goto(start_action['metadata']['action_url'],wait_until='networkidle')
    expect(page2.get_by_role('heading',name='独立双卡验收')).to_be_visible()
    page2.locator('input[type=file]').last.set_input_files({'name':'start.png','mimeType':'image/png','buffer':png})
    page2.get_by_role('button',name='确认打卡',exact=True).click()
    expect(page2.get_by_role('status').filter(has_text='打卡成功')).to_be_visible(timeout=30000)
    time.sleep(6)
    assert request('GET',f'/projects/{project["id"]}/tasks/{task["id"]}')['data']['status_id']==done,'late start downgraded finished task'
    # An acknowledged or lost response retries the exact operation, never another record.
    replay=context.request.post('http://127.0.0.1:18082/checkin-api/v1/submit',headers={'Origin':'http://127.0.0.1:18082',**card_headers},data=submitted_request.value.post_data_json)
    assert replay.status==200 and replay.json()['duplicate']
    # Original photo is private and cannot be fetched without its card session.
    record=context.request.get('http://127.0.0.1:18082/checkin-api/v1/session',headers=card_headers).json()['record']
    native('GET','/checkin-api/v1/photo/'+record['media_id'],expected=401)
    # Same operation returns original record; replacing a successful photo conflicts.
    dup=context.request.post('http://127.0.0.1:18082/checkin-api/v1/submit',headers={'Origin':'http://127.0.0.1:18082',**card_headers},data={'media_id':record['media_id'],'revision':1,'note':'','op_id':'different-operation'})
    assert dup.status==409
    request('POST',task_path+'/cancel',{})
    assert context.request.get('http://127.0.0.1:18082/checkin-api/v1/session',headers=card_headers).status==410
    context.close();browser.close()
# Independent expired fixture: upload can complete but final submit crosses the deadline.
late=request('POST',f'/projects/{project["id"]}/tasks',{'title':'截止验收'},201)['data']
close=(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(seconds=8)).isoformat()
request('PUT',path+f'/tasks/{late["id"]}/checkin',{'config':{'enabled':True,'due':close},'revision':0})
entry=native('POST','/internal/v1/action',{'project_id':project['id'],'task_id':late['id'],'kind':'due','target':close},headers=auth)
token=entry['metadata']['action_url'].split('#token=')[1]
cookies=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
native('POST','/checkin-api/v1/exchange',{'token':token},opener=cookies)
late_headers={'X-Checkin-Instance':entry['instance_id'],'X-Checkin-Kind':'due'}
photo=native('POST','/checkin-api/v1/photos',png,201,opener=cookies,headers={'Content-Type':'image/png',**late_headers})
time.sleep(max(0,(datetime.datetime.fromisoformat(close)-datetime.datetime.now(datetime.timezone.utc)).total_seconds())+.1)
native('POST','/checkin-api/v1/submit',{'media_id':photo['media_id'],'revision':1,'op_id':'cross-cutoff-test','note':''},410,opener=cookies,headers=late_headers)
# Invalid payloads and cross-origin browser writes fail closed.
native('POST','/checkin-api/v1/exchange',{'token':token},403,headers={'Origin':'https://untrusted.invalid'})
request('PATCH',f'/admin/plugins/{installed["id"]}',{'enabled':False})
native('GET','/healthz',expected=503)
request('PATCH',f'/admin/plugins/{installed["id"]}',{'enabled':True})
worker.terminate();worker.wait(timeout=10)
worker=subprocess.Popen(['/tmp/checkin-worker'],env=worker_env,stdout=log,stderr=subprocess.STDOUT)
for _ in range(30):
    try:native('GET','/healthz');break
    except Exception:time.sleep(.5)
assert len(request('GET',path+'/records')['items'])==2,'restart lost or duplicated a card'
# Pairing is restricted to its declared source connection; unrelated media is denied.
pairing=request('POST',path+'/pairing',{'name':'scoped device','connection_id':'11111111-1111-4111-8111-111111111111'},201)
paired={'Authorization':'Bearer '+pairing['token']}
scope=native('POST','/internal/v1/pairing-info',{'token':pairing['token']},headers=auth)
assert scope=={'project_id':project['id'],'connection_id':'11111111-1111-4111-8111-111111111111'}
native('POST','/internal/v1/pairing-info',{'token':pairing['token']},expected=401,headers=paired)
delegated={**auth,'X-Sync-Project':project['id'],'X-Sync-Connection':scope['connection_id'],'X-Sync-Device':'22222222-2222-4222-8222-222222222222'}
assert native('GET','/internal/v1/sync/changes?after=0',headers=delegated)['items']==[]
native('GET','/internal/v1/sync/media/'+record['media_id'],expected=404,headers=delegated)
native('GET','/internal/v1/sync/info',expected=401,headers=paired)
assert native('GET','/checkin-api/v1/sync/changes?after=0',headers=paired)['items']==[]
native('GET','/checkin-api/v1/sync/media/'+record['media_id'],expected=404,headers=paired)
exec((ROOT/'scripts/delivery-integration.py').read_text(),globals())
request('DELETE',path+'/pairing/'+pairing['id'])
native('GET','/checkin-api/v1/sync/changes?after=0',expected=401,headers=paired)
native('POST','/internal/v1/pairing-info',{'token':pairing['token']},expected=401,headers=auth)
worker.terminate();worker.wait(timeout=10);log.close();proxy.shutdown()
assert faults=={'fail_status':0,'drop_status':0},'outbox response-loss fixture was not exercised'
(ROOT/'verification/checkin-report.json').write_text(json.dumps({'zero_lead_metadata_rejected_before_instance':True,'source_sha':os.environ['GITHUB_SHA'],'passwordless_fragment_exchange':True,'mobile_browser_upload':True,'independent_cards_end_first':True,'no_status_downgrade':True,'private_media':True,'shared_browser_card_isolation':True,'cancel_revokes_session':True,'upload_cross_deadline_rejected':True,'plugin_disable_pauses_worker':True,'status_outage_and_response_loss':faults=={'fail_status':0,'drop_status':0},'real_device':False},indent=2))

# Load the independent extension inside the unchanged official Paca web app.
(ROOT/'ci.Caddyfile').write_text(':80 {\n handle /api/* {\n reverse_proxy paca-ci-api:8080\n }\n handle_path /plugins/* {\n root * /var/www/plugins\n file_server\n }\n handle {\n reverse_proxy paca-ci-web:3000\n }\n}\n')
cmd('docker','run','-d','--name','paca-ci-web','--network','paca-ci','pacaai/paca-web@sha256:c65dc2fa6384be8bbafdda9a220d525d54c730f63f2a0e0b4c167c7bb9452995')
cmd('docker','run','-d','--name','paca-ci-caddy','--network','paca-ci','-p','127.0.0.1:18081:80','-v',f'{ROOT}/ci.Caddyfile:/etc/caddy/Caddyfile:ro','-v',f'{ROOT}/release/frontend:/var/www/plugins:ro','caddy:2-alpine')
with sync_playwright() as pw:
    browser=pw.chromium.launch();context=browser.new_context(viewport={'width':1280,'height':900})
    response=context.request.post('http://127.0.0.1:18081/api/v1/auth/login',data={'username':'admin','password':new_password});assert response.ok
    page=context.new_page();page.goto(f'http://127.0.0.1:18081/projects/{project["id"]}/settings/',wait_until='domcontentloaded')
    page.get_by_role('button',name=manifest['displayName'],exact=True).last.click(timeout=45000)
    expect(page.get_by_label('时区',exact=True)).to_have_value('Asia/Shanghai',timeout=30000)
    page.get_by_role('button',name='保存设置',exact=True).click()
    expect(page.get_by_role('status').filter(has_text='设置已保存')).to_be_visible()
    page.screenshot(path=str(ROOT/'verification/paca-settings.png'),full_page=True)
    context.close();browser.close()
