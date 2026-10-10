import hashlib, hmac, http.cookiejar, json, os, pathlib, secrets, subprocess, time, urllib.request, urllib.error

ROOT=pathlib.Path(__file__).resolve().parent.parent
source_manifest=json.loads((ROOT/'plugin.json').read_text())
manifest=json.loads((ROOT/f'release/wasm/{source_manifest["id"]}/plugin.json').read_text())
plugin_id=manifest['id']
password=secrets.token_urlsafe(24)
new_password=secrets.token_urlsafe(24)

def cmd(*args):
    subprocess.run(args,check=True,stdout=subprocess.DEVNULL)

cmd('docker','network','create','paca-ci')
cmd('docker','run','-d','--name','paca-ci-db','--network','paca-ci','-e','POSTGRES_PASSWORD=ci-only-password','-e','POSTGRES_DB=paca','postgres:16-alpine')
cmd('docker','run','-d','--name','paca-ci-cache','--network','paca-ci','valkey/valkey:8-alpine')
for _ in range(50):
    if subprocess.run(['docker','exec','paca-ci-db','pg_isready','-U','postgres'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0: break
    time.sleep(1)
env={'DATABASE_URL':'postgres://postgres:ci-only-password@paca-ci-db:5432/paca?sslmode=disable','REDIS_URL':'redis://paca-ci-cache:6379','JWT_SECRET':secrets.token_hex(32),'ADMIN_USERNAME':'admin','ADMIN_PASSWORD':password,'ENCRYPTION_KEY':secrets.token_hex(32),'PUBLIC_URL':'http://localhost:18080','COOKIE_SECURE':'false','PLUGINS_WASM_DIR':'/plugins/wasm','PLUGINS_FRONTEND_DIR':'/plugins/frontend','STORAGE_PROVIDER':'s3','STORAGE_ENDPOINT':'http://paca-ci-storage:9000','STORAGE_ACCESS_KEY_ID':'ci-access-key','STORAGE_SECRET_ACCESS_KEY':'ci-secret-key','AI_AGENT_INTERNAL_KEY':secrets.token_hex(32),'STORAGE_BUCKET':'paca','STORAGE_REGION':'us-east-1'}
args=['docker','run','-d','--name','paca-ci-api','--network','paca-ci','--network-alias','api','-p','127.0.0.1:18080:8080','-v',f'{ROOT}/release:/plugins']
for k,v in env.items(): args+=['-e',f'{k}={v}']
args+=['pacaai/paca-api@sha256:42b36fcb167f39bf07c04464b6d71ea49b1f7a8a745a2e9391623e76c5a17ad9']
cmd(*args)
jar=http.cookiejar.CookieJar()
opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
base='http://localhost:18080/api/v1'
def request(method,path,data=None,expected=200,headers=None):
    body=json.dumps(data).encode() if data is not None else None
    req=urllib.request.Request(base+path,data=body,method=method,headers={'Content-Type':'application/json',**(headers or {})})
    try:
        with opener.open(req,timeout=20) as r: status,payload=r.status,r.read()
    except urllib.error.HTTPError as e: status,payload=e.code,e.read()
    if status!=expected: raise RuntimeError(f'{method} {path}: {status}: {payload.decode()}')
    return json.loads(payload) if payload else {}
for _ in range(90):
    try:
        request('POST','/auth/login',{'username':'admin','password':password})
        break
    except (OSError,RuntimeError): time.sleep(2)
else: raise RuntimeError('official API did not become ready')
request('PATCH','/users/me/password',{'current_password':password,'new_password':new_password},204)
request('POST','/auth/login',{'username':'admin','password':new_password})
installed=request('POST','/admin/plugins',{'name':plugin_id,'version':manifest['version'],'manifest':manifest,'enabled':True},201)['data']
assert request('GET',f'/plugins/{plugin_id}/health')['schema_version']==2
worker_secret=request('POST',f'/plugins/{plugin_id}/admin/worker-credential',{},201)['secret']
project=request('POST','/projects',{'name':'Check-in acceptance','task_id_prefix':'CHECK'},201)['data']
statuses=request('GET',f'/projects/{project["id"]}/task-statuses')['data']['items']
progress=next(s['id'] for s in statuses if s['category'] not in ('todo','backlog','done'))
done=next(s['id'] for s in statuses if s['category']=='done')
archive=request('POST',f'/projects/{project["id"]}/task-statuses',{'name':'Archive','category':'done','position':99},201)['data']['id']
path=f'/plugins/{plugin_id}/projects/{project["id"]}'
cfg={'enabled':True,'timezone':'Asia/Shanghai','start_minutes':10,'due_minutes':10,'retention_days':90,'progress_status':progress,'done_status':done,'archive_status':archive}
request('PUT',path+'/settings',{'config':cfg,'revision':0})
request('PUT',path+'/settings',{'config':cfg,'revision':1})
request('PUT',path+'/settings',{'config':cfg,'revision':1},409)
assert request('GET',path+'/settings')['revision']==2
pair=request('POST',path+'/pairing',{'name':'test vault','connection_id':'11111111-1111-4111-8111-111111111111'},201)
assert len(pair['token'])==64
request('DELETE',path+'/pairing/'+pair['id'])

# Retain revoked records: reloading must never reuse their primary keys or tokens.
pair_ids={pair['id']};pair_tokens={pair['token']}
for phase in range(3):
    for number in range(4):
        extra=request('POST',path+'/pairing',{'name':f'配对重载检查 {phase}-{number}','connection_id':'11111111-1111-4111-8111-111111111111'},201)
        assert len(extra['token'])==64 and extra['id'] not in pair_ids and extra['token'] not in pair_tokens
        pair_ids.add(extra['id']);pair_tokens.add(extra['token'])
        request('DELETE',path+'/pairing/'+extra['id'])
    request('PATCH',f'/admin/plugins/{installed["id"]}',{'manifest':manifest,'version':manifest['version'],'enabled':True})
    assert request('GET',f'/plugins/{plugin_id}/health')['schema_version']==2
# Persist DB and restart the official host: RNG state must not restart a sequence.
cmd('docker','restart','paca-ci-api')
for _ in range(45):
    try:
        request('GET',f'/plugins/{plugin_id}/health');break
    except (OSError,RuntimeError):time.sleep(1)
else:raise RuntimeError('host did not recover')
extra=request('POST',path+'/pairing',{'name':'重启后配对','connection_id':'11111111-1111-4111-8111-111111111111'},201)
assert extra['id'] not in pair_ids and extra['token'] not in pair_tokens
request('DELETE',path+'/pairing/'+extra['id'])

request('PATCH',f'/admin/plugins/{installed["id"]}',{'enabled':False})
request('GET',f'/plugins/{plugin_id}/health',expected=404)
request('PATCH',f'/admin/plugins/{installed["id"]}',{'enabled':True})
# Actual native worker and private S3, browser upload and exact-deadline integration.
exec((ROOT/'scripts/checkin-integration.py').read_text(),globals())
verification=ROOT/'verification';verification.mkdir(exist_ok=True)
(verification/'host-report.json').write_text(json.dumps({'source_sha':os.environ['GITHUB_SHA'],'official_core':'v0.18.6','schema':2,'settings_cas':True,'pairing_revocation':True,'pairing_entropy_survives_reload_and_restart':True,'independent_plugin_enable_disable':True,'native_photo_and_browser':True},indent=2))
