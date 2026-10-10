"""Action-only deployment check against a real Nginx with an unwritable temp path."""
import gzip
import hashlib
import http.server
import json
import os
import pathlib
import ssl
import subprocess
import tempfile
import threading
import time
import socket
import http.client
import concurrent.futures
import urllib.request

root = pathlib.Path(__file__).resolve().parent.parent
script = (b"/* self-hosted check-in application */\n" + b"var checkin=true;\n" * 65536)
stylesheet = b"body { color: #171717; background: #fff; }\n" * 8192
photo = bytes(range(256)) * 40960


class Upstream(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        content, kind = script, "text/javascript; charset=utf-8"
        if self.path.endswith(".css"):
            content, kind = stylesheet, "text/css; charset=utf-8"
        elif self.path.startswith("/checkin-api/"):
            content, kind = b'{"ok":true}', "application/json"
        elif self.path.startswith("/task-sync/v1/checkin/media/"):
            content, kind = photo, "image/jpeg"
        elif self.path.startswith("/task-sync/"):
            content, kind = b'{"items":[' + b'"task",' * 32768 + b'"end"]}', "application/json"
        self.send_response(200)
        self.send_header("Content-Type", kind)
        self.send_header("Content-Length", str(len(content)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        mode=self.headers.get('X-Test-Transfer','complete')
        try:
            if mode=='truncated':self.wfile.write(content[:65536]);self.close_connection=True
            elif mode=='paused':
                self.wfile.write(content[:4096]);self.wfile.flush();time.sleep(1);self.wfile.write(content[4096:])
            elif mode=='slow':
                for offset in range(0,len(content),65536):self.wfile.write(content[offset:offset+65536]);self.wfile.flush();time.sleep(.02)
            else:self.wfile.write(content)
        except (BrokenPipeError,ConnectionResetError):pass

    def log_message(self, *_):
        pass


upstream = http.server.ThreadingHTTPServer(("127.0.0.1", 18789), Upstream)
threading.Thread(target=upstream.serve_forever, daemon=True).start()
sync_upstream = http.server.ThreadingHTTPServer(("127.0.0.1", 18790), Upstream)
threading.Thread(target=sync_upstream.serve_forever, daemon=True).start()
report = {"source_sha": os.environ.get("GITHUB_SHA"), "checks": []}
with tempfile.TemporaryDirectory(prefix="checkin-nginx-") as temporary:
    prefix = pathlib.Path(temporary)
    prefix.chmod(0o755)
    blocked = prefix / "blocked-proxy-temp"
    blocked.mkdir(mode=0o700)
    subprocess.run(["sudo", "chown", "root:root", str(blocked)], check=True)
    config = prefix / "nginx.conf"
    subprocess.run(['openssl','req','-x509','-nodes','-newkey','rsa:2048','-days','1','-subj','/CN=localhost','-keyout',str(prefix/'tls.key'),'-out',str(prefix/'tls.crt')],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    tls=ssl._create_unverified_context()
    sync_config = (root / "deploy/nginx-task-sync.conf").read_text().replace("/var/log/nginx/paca-sync.access.log", str(prefix / "sync-access.log")).replace("/var/log/nginx/paca-sync.error.log", str(prefix / "sync-error.log"))
    (prefix / "sync.conf").write_text(sync_config)
    config.write_text(
        f"user www-data;\nworker_processes 1;\npid {prefix}/nginx.pid;\n"
        f"error_log {prefix}/error.log info;\nevents {{ worker_connections 128; }}\n"
        f"http {{ access_log off; proxy_temp_path {blocked}; include {root}/deploy/nginx-sync-log.conf; "
        f"server {{ listen 127.0.0.1:18089 ssl; ssl_certificate {prefix}/tls.crt; ssl_certificate_key {prefix}/tls.key; include {root}/deploy/nginx-checkin.conf; include {prefix}/sync.conf; }} }}\n"
    )
    subprocess.run(["sudo", "nginx", "-t", "-c", str(config)], check=True)
    subprocess.run(["sudo", "nginx", "-c", str(config)], check=True)
    try:
        time.sleep(0.5)
        for path, expected in [
            ("/checkin/assets/checkin-example.js", script),
            ("/checkin/assets/checkin-example.css", stylesheet),
            ("/checkin-api/v1/session", b'{"ok":true}'),
            ("/task-sync/v1/checkin/media/00000000-0000-4000-8000-000000000001", photo),
            ("/task-sync/v1/changes", b'{"items":[' + b'"task",' * 32768 + b'"end"]}'),
        ]:
            for encoding in ("identity", "gzip"):
                req = urllib.request.Request(
                    "https://127.0.0.1:18089" + path,
                    headers={"Accept-Encoding": encoding},
                )
                with urllib.request.urlopen(req, timeout=60,context=tls) as response:
                    received = bytearray()
                    while chunk := response.read(8192):
                        received.extend(chunk)
                        time.sleep(0.004 if len(expected) < 2000000 else 0.001)
                    compressed = response.headers.get("Content-Encoding") == "gzip"
                    decoded = gzip.decompress(received) if compressed else received
                    assert decoded == expected, f"truncated response: {path}"
                    assert response.headers.get("Cache-Control") == "no-store"
                    if path.startswith("/checkin/assets/") and encoding == "gzip":
                        assert compressed and len(received) < len(expected) / 2
                        assert "Accept-Encoding" in response.headers.get("Vary", "")
                    if path.startswith("/checkin-api/"):
                        assert not compressed
                    report["checks"].append({
                        "path": path, "encoding": encoding, "bytes": len(received),
                        "decoded_sha256": hashlib.sha256(decoded).hexdigest(), "ok": True,
                    })
        def media(mode,number,timeout=30):
            request=urllib.request.Request('https://127.0.0.1:18089/task-sync/v1/checkin/media/00000000-0000-4000-8000-'+str(number).zfill(12),headers={'X-Test-Transfer':mode})
            return urllib.request.urlopen(request,timeout=timeout,context=tls)
        with media('truncated',2) as response:
            try:response.read();raise AssertionError('truncated upstream was accepted')
            except http.client.IncompleteRead as error:assert len(error.partial)==65536
        with media('paused',3,.15) as response:
            try:response.read();raise AssertionError('body timeout was accepted')
            except (TimeoutError,socket.timeout):pass
        with media('slow',4) as response:assert len(response.read(4096))==4096
        def slow_download():
            with media('slow',5) as response:return response.read()
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
            download=pool.submit(slow_download);time.sleep(.2)
            subprocess.run(['sudo','nginx','-s','reload','-c',str(config)],check=True)
            assert download.result(timeout=30)==photo
        time.sleep(1)
        report.update({'https_10mib_complete':True,'truncated_upstream_rejected':True,'body_timeout_not_success':True,'client_cancel_does_not_stop_service':True,'reload_preserves_inflight_photo':True})
        errors = (prefix / "error.log").read_text()
        assert "Permission denied" not in errors and "[crit]" not in errors
        assert not subprocess.check_output(["sudo", "find", str(blocked), "-mindepth", "1", "-print"])
        sync_lines = (prefix / "sync-access.log").read_text().splitlines()
        assert len(sync_lines) >= 8
        for line in sync_lines:
            entry = json.loads(line)
            if entry['uri'].endswith('000000000001') or entry['uri'].endswith('/changes') or entry['uri'].endswith('000000000005'):
                assert entry["status"] == 200 and int(entry["upstream_bytes"]) == entry["bytes"]
            assert "Authorization" not in line and "?" not in entry["uri"]
        report["unwritable_temp_without_disk_writes"] = True
    finally:
        subprocess.run(["sudo", "nginx", "-s", "quit", "-c", str(config)], check=True)
        upstream.shutdown()
        sync_upstream.shutdown()
        subprocess.run(["sudo", "chown", "-R", f"{os.getuid()}:{os.getgid()}", str(prefix)], check=True)
verification = root / "verification"
verification.mkdir(exist_ok=True)
(verification / "nginx-report.json").write_text(json.dumps(report, indent=2))
print(json.dumps(report))
