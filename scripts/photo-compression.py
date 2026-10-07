"""Production web build, real browser decoding and exact XHR bytes; Actions only."""
import base64, datetime, hashlib, json, os, pathlib, struct, threading
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from playwright.sync_api import sync_playwright, expect

ROOT = pathlib.Path(__file__).resolve().parent.parent
class Static(SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs): super().__init__(*args, directory=str(ROOT/'frontend/web-dist'), **kwargs)
    def log_message(self, *args): pass
    def do_GET(self):
        self.path = self.path.removeprefix('/checkin')
        if self.path.startswith('/assets/'): return super().do_GET()
        self.path = '/checkin.html'; return super().do_GET()
server = ThreadingHTTPServer(('127.0.0.1', 0), Static)
threading.Thread(target=server.serve_forever, daemon=True).start()
origin = 'http://127.0.0.1:' + str(server.server_port)
report = {'source_sha':os.environ['GITHUB_SHA'], 'real_device':False}
with sync_playwright() as pw:
    browser = pw.chromium.launch()
    context = browser.new_context(viewport={'width':390, 'height':844})
    generator = context.new_page(); generator.goto(origin+'/checkin/test/start')
    fixtures = generator.evaluate("""async () => {
      async function photo(width,height,mime,noise=false,transparent=false) {
        const c=document.createElement('canvas');c.width=width;c.height=height;const x=c.getContext('2d');
        if(noise){const im=x.createImageData(width,height);let n=1234567;for(let p=0;p<im.data.length;p+=4){n=(Math.imul(n,1664525)+1013904223)|0;im.data[p]=n&255;im.data[p+1]=(n>>>8)&255;im.data[p+2]=(n>>>16)&255;im.data[p+3]=255;}x.putImageData(im,0,0);}
        else{x.fillStyle='red';x.fillRect(0,0,width/2,height);x.fillStyle='blue';x.fillRect(width/2,0,width/2,height);}
        if(transparent)x.clearRect(0,0,100,100);
        return c.toDataURL(mime,0.99).split(',')[1];
      }
      return {large:await photo(3072,2048,'image/jpeg',true),orientation:await photo(80,40,'image/jpeg'),transparent:await photo(1600,1200,'image/png',true,true),webp:await photo(1200,800,'image/webp'),small:await photo(16,16,'image/png')};
    }""")
    generator.close()
    def fixture(name,mime): return {'name':name+'.'+mime.split('/')[1], 'mimeType':mime, 'buffer':base64.b64decode(fixtures[name])}
    def open_page():
        page=context.new_page(); captured=[]; current={'record':None}
        now=datetime.datetime.now(datetime.timezone.utc)
        view={'task':{'title':'照片上传验收','content':'测试实际上传文件','start':None,'due':None,'priority':'普通','status':'待完成','tags':[],'source':'任务','timezone':'Asia/Shanghai'},'timezone':'Asia/Shanghai','instance_id':'compression','revision':1,'kind':'start','state':'active','server_time':now.isoformat(),'opens_at':(now-datetime.timedelta(minutes=1)).isoformat(),'closes_at':(now+datetime.timedelta(minutes=10)).isoformat()}
        def api(route):
            req=route.request
            if req.url.endswith('/photos'):
                captured.append({'data':req.post_data_buffer,'type':req.headers.get('content-type')})
                route.fulfill(status=201,json={'media_id':'test-media'})
            elif req.url.endswith('/submit'):
                current['record']={'id':'record','submitted_at':now.isoformat(),'status':'synced','note':''}
                route.fulfill(json={'record_id':'record'})
            else: route.fulfill(json={**view, 'record':current['record']})
        page.route('**/checkin-api/v1/**',api)
        page.goto(origin+'/checkin/compression/start',wait_until='networkidle')
        expect(page.get_by_role('heading',name='照片上传验收',exact=True)).to_be_visible()
        return page,captured
    def pick(page,file):
        page.locator('input[type=file]').last.set_input_files(file)
        expect(page.get_by_role('button',name='确认打卡',exact=True)).to_be_enabled(timeout=30000)
        return page.locator('img[alt="待提交照片预览"]').evaluate("async i => {await i.decode();const c=document.createElement('canvas');c.width=i.naturalWidth;c.height=i.naturalHeight;const x=c.getContext('2d');x.drawImage(i,0,0);return {width:c.width,height:c.height,corner:[...x.getImageData(4,4,1,1).data]};}")
    def send(page,captured):
        page.get_by_role('button',name='确认打卡',exact=True).click()
        expect(page.get_by_role('status').filter(has_text='打卡成功')).to_be_visible()
        assert len(captured)==1 and len(captured[0]['data'])<=1024*1024
        return captured[0]
    page,captured=open_page(); original=fixture('large','image/jpeg'); dimensions=pick(page,original); sent=send(page,captured)
    assert len(original['buffer'])>1024*1024 and len(sent['data'])<len(original['buffer']) and max(dimensions['width'],dimensions['height'])<=2048
    assert sent['type']=='image/jpeg' and hashlib.sha256(sent['data']).digest()!=hashlib.sha256(original['buffer']).digest()
    report['exact_xhr_compressed_bytes']={'original_bytes':len(original['buffer']),'upload_bytes':len(sent['data']),'width':dimensions['width'],'height':dimensions['height']};page.close()
    # APP1 / TIFF orientation=6. The uploaded JPEG must be physically portrait.
    tiff=b'II'+struct.pack('<HIH',42,8,1)+struct.pack('<HHI',274,3,1)+struct.pack('<H',6)+b'\0\0'+struct.pack('<I',0)
    exif=b'Exif\0\0'+tiff; jpeg=base64.b64decode(fixtures['orientation']); rotated=jpeg[:2]+b'\xff\xe1'+struct.pack('>H',len(exif)+2)+exif+jpeg[2:]
    page,captured=open_page(); dims=pick(page,{'name':'portrait.jpg','mimeType':'image/jpeg','buffer':rotated});sent=send(page,captured)
    assert (dims['width'],dims['height'])==(40,80) and b'Exif' not in sent['data'];report['exif_orientation_normalized']=True;page.close()
    page,captured=open_page();dims=pick(page,fixture('transparent','image/png'));sent=send(page,captured)
    assert sent['type']=='image/jpeg' and min(dims['corner'][:3])>240;report['transparent_white_background']=True;page.close()
    for name,mime in [('webp','image/webp'),('small','image/png')]:
        page,captured=open_page();original=fixture(name,mime);dims=pick(page,original);send(page,captured)
        if name=='small':assert max(dims['width'],dims['height'])==16
        report[name]=True;page.close()
    page,captured=open_page();page.add_init_script('window.createImageBitmap=undefined');page.reload(wait_until='networkidle');pick(page,fixture('small','image/png'));send(page,captured);report['webview_image_fallback']=True;page.close()
    page,captured=open_page();page.locator('input[type=file]').last.set_input_files({'name':'bad.jpg','mimeType':'image/jpeg','buffer':b'not an image'})
    expect(page.get_by_role('alert')).to_contain_text('照片格式无效');expect(page.get_by_role('button',name='确认打卡',exact=True)).to_be_disabled();assert captured==[];report['corrupt_rejected']=True
    oversized=b'\x89PNG\r\n\x1a\n'+struct.pack('>I',13)+b'IHDR'+struct.pack('>II',5001,5000)+b'\x08\x06\x00\x00\x00'
    page.locator('input[type=file]').last.set_input_files({'name':'pixels.png','mimeType':'image/png','buffer':oversized})
    expect(page.get_by_role('alert')).to_contain_text('2500 万');assert captured==[];report['pixel_limit_before_decode']=True
    pick(page,fixture('large','image/jpeg'));page.locator('input[type=file]').last.set_input_files({'name':'last.png','mimeType':'image/png','buffer':base64.b64decode(fixtures['small'])});expect(page.get_by_role('button',name='确认打卡',exact=True)).to_be_enabled();dims=page.locator('img').evaluate('i=>({width:i.naturalWidth,height:i.naturalHeight})');assert dims['width']==16;send(page,captured);report['reselection_uses_latest']=True
    page.screenshot(path=str(ROOT/'verification/photo-compression-mobile.png'),full_page=True);page.close();context.close();browser.close()
server.shutdown()
(ROOT/'verification/photo-compression-report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2))
