"""Action-only deployment check against a real Nginx with an unwritable temp path."""
import gzip
import hashlib
import http.server
import json
import os
import pathlib
import subprocess
import tempfile
import threading
import time
import urllib.request

root = pathlib.Path(__file__).resolve().parent.parent
script = (b"/* self-hosted check-in application */\n" + b"var checkin=true;\n" * 65536)
stylesheet = b"body { color: #171717; background: #fff; }\n" * 8192


class Upstream(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        content, kind = script, "text/javascript; charset=utf-8"
        if self.path.endswith(".css"):
            content, kind = stylesheet, "text/css; charset=utf-8"
        elif self.path.startswith("/checkin-api/"):
            content, kind = b'{"ok":true}', "application/json"
        self.send_response(200)
        self.send_header("Content-Type", kind)
        self.send_header("Content-Length", str(len(content)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(content)

    def log_message(self, *_):
        pass


upstream = http.server.ThreadingHTTPServer(("127.0.0.1", 18789), Upstream)
threading.Thread(target=upstream.serve_forever, daemon=True).start()
report = {"source_sha": os.environ.get("GITHUB_SHA"), "checks": []}
with tempfile.TemporaryDirectory(prefix="checkin-nginx-") as temporary:
    prefix = pathlib.Path(temporary)
    prefix.chmod(0o755)
    blocked = prefix / "blocked-proxy-temp"
    blocked.mkdir(mode=0o700)
    subprocess.run(["sudo", "chown", "root:root", str(blocked)], check=True)
    config = prefix / "nginx.conf"
    config.write_text(
        f"user www-data;\nworker_processes 1;\npid {prefix}/nginx.pid;\n"
        f"error_log {prefix}/error.log info;\nevents {{ worker_connections 128; }}\n"
        f"http {{ access_log off; proxy_temp_path {blocked}; "
        f"server {{ listen 127.0.0.1:18089; include {root}/deploy/nginx-checkin.conf; }} }}\n"
    )
    subprocess.run(["sudo", "nginx", "-t", "-c", str(config)], check=True)
    subprocess.run(["sudo", "nginx", "-c", str(config)], check=True)
    try:
        time.sleep(0.5)
        for path, expected in [
            ("/checkin/assets/checkin-example.js", script),
            ("/checkin/assets/checkin-example.css", stylesheet),
            ("/checkin-api/v1/session", b'{"ok":true}'),
        ]:
            for encoding in ("identity", "gzip"):
                req = urllib.request.Request(
                    "http://127.0.0.1:18089" + path,
                    headers={"Accept-Encoding": encoding},
                )
                with urllib.request.urlopen(req, timeout=15) as response:
                    received = bytearray()
                    while chunk := response.read(8192):
                        received.extend(chunk)
                        time.sleep(0.004)
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
        errors = (prefix / "error.log").read_text()
        assert "Permission denied" not in errors and "[crit]" not in errors
        assert not subprocess.check_output(["sudo", "find", str(blocked), "-mindepth", "1", "-print"])
        report["unwritable_temp_without_disk_writes"] = True
    finally:
        subprocess.run(["sudo", "nginx", "-s", "quit", "-c", str(config)], check=True)
        upstream.shutdown()
        subprocess.run(["sudo", "chown", "-R", f"{os.getuid()}:{os.getgid()}", str(prefix)], check=True)
verification = root / "verification"
verification.mkdir(exist_ok=True)
(verification / "nginx-report.json").write_text(json.dumps(report, indent=2))
print(json.dumps(report))
