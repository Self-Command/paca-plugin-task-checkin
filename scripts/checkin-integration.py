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
worker_env={**os.environ,'PACA_API_URL':'http://localhost:18080','PUBLIC_URL':'http://127.0.0.1:18082','DATABASE_URL':'postgres://postgres:ci-only-password@127.0.0.1:15432/paca?sslmode=disable','CHECKIN_TEST_MODE':'true','LISTEN_ADDR':'127.0.0.1:18082','CHECKIN_BUCKET':'checkin-private','CHECKIN_S3_ENDPOINT':'http://127.0.0.1:19000','CHECKIN_WEB_DIR':str(ROOT/'frontend/web-dist')}
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
task=request('POST',f'/projects/{project["id"]}/tasks',{'title':'独立双卡验收'},201)['data']
task_path=path+f'/tasks/{task["id"]}'
rule={'enabled':True,'start':start,'due':due}
request('PUT',task_path+'/checkin',{'config':rule,'revision':0})
request('PUT',task_path+'/checkin',{'config':rule,'revision':1})
request('PUT',task_path+'/checkin',{'config':rule,'revision':1},409)
def action(kind,target):return native('POST','/internal/v1/action',{'project_id':project['id'],'task_id':task['id'],'kind':kind,'target':target},headers=auth)
end_action=action('due',due)
start_action=action('start',start)
assert end_action['instance_id']==start_action['instance_id']
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
    assert not page.url.split('#')[-1].startswith('token=')
    page.locator('input[type=file]').last.set_input_files({'name':'check.png','mimeType':'image/png','buffer':png})
    with page.expect_request(lambda r:r.url.endswith('/checkin-api/v1/submit') and r.method=='POST') as submitted_request:
        page.get_by_role('button',name='确认打卡',exact=True).click()
    expect(page.get_by_role('status').filter(has_text='打卡成功')).to_be_visible(timeout=30000)
    (ROOT/'verification').mkdir(exist_ok=True);page.screenshot(path=str(ROOT/'verification/checkin-mobile.png'),full_page=True)
    # End succeeds first; start is still independently authorized after Paca is done.
    for _ in range(12):
        current=request('GET',f'/projects/{project["id"]}/tasks/{task["id"]}')['data']
        if current['status_id']==done:break
        time.sleep(1)
    assert current['status_id']==done,'durable outbox did not update Paca'
    context2=browser.new_context();page2=context2.new_page();page2.goto(start_action['metadata']['action_url'],wait_until='networkidle')
    expect(page2.get_by_role('heading',name='独立双卡验收')).to_be_visible()
    page2.locator('input[type=file]').last.set_input_files({'name':'start.png','mimeType':'image/png','buffer':png})
    page2.get_by_role('button',name='确认打卡',exact=True).click()
    expect(page2.get_by_role('status').filter(has_text='打卡成功')).to_be_visible(timeout=30000)
    time.sleep(6)
    assert request('GET',f'/projects/{project["id"]}/tasks/{task["id"]}')['data']['status_id']==done,'late start downgraded finished task'
    # An acknowledged or lost response retries the exact operation, never another record.
    replay=context.request.post('http://127.0.0.1:18082/checkin-api/v1/submit',headers={'Origin':'http://127.0.0.1:18082'},data=submitted_request.value.post_data_json)
    assert replay.status==200 and replay.json()['duplicate']
    # Original photo is private and cannot be fetched without its card session.
    record=context.request.get('http://127.0.0.1:18082/checkin-api/v1/session').json()['record']
    native('GET','/checkin-api/v1/photo/'+record['media_id'],expected=401)
    # Same operation returns original record; replacing a successful photo conflicts.
    dup=context.request.post('http://127.0.0.1:18082/checkin-api/v1/submit',headers={'Origin':'http://127.0.0.1:18082'},data={'media_id':record['media_id'],'revision':1,'note':'','op_id':'different-operation'})
    assert dup.status==409
    request('POST',task_path+'/cancel',{})
    assert context.request.get('http://127.0.0.1:18082/checkin-api/v1/session').status==410
    context.close();context2.close();browser.close()
# Independent expired fixture: upload can complete but final submit crosses the deadline.
late=request('POST',f'/projects/{project["id"]}/tasks',{'title':'截止验收'},201)['data']
close=(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(seconds=8)).isoformat()
request('PUT',path+f'/tasks/{late["id"]}/checkin',{'config':{'enabled':True,'due':close},'revision':0})
entry=native('POST','/internal/v1/action',{'project_id':project['id'],'task_id':late['id'],'kind':'due','target':close},headers=auth)
token=entry['metadata']['action_url'].split('#token=')[1]
cookies=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
native('POST','/checkin-api/v1/exchange',{'token':token},opener=cookies)
photo=native('POST','/checkin-api/v1/photos',png,201,opener=cookies,headers={'Content-Type':'image/png'})
time.sleep(max(0,(datetime.datetime.fromisoformat(close)-datetime.datetime.now(datetime.timezone.utc)).total_seconds())+.1)
native('POST','/checkin-api/v1/submit',{'media_id':photo['media_id'],'revision':1,'op_id':'cross-cutoff-test','note':''},410,opener=cookies)
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
worker.terminate();worker.wait(timeout=10);log.close()
(ROOT/'verification/checkin-report.json').write_text(json.dumps({'source_sha':os.environ['GITHUB_SHA'],'passwordless_fragment_exchange':True,'mobile_browser_upload':True,'independent_cards_end_first':True,'no_status_downgrade':True,'private_media':True,'cancel_revokes_session':True,'upload_cross_deadline_rejected':True,'plugin_disable_pauses_worker':True,'real_device':False},indent=2))

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
