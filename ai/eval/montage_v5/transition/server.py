"""Serve the transition labelling site and keep its marks: the site's files (with byte ranges, so the clips
seek in Safari too), GET /labels and POST /labels (the whole marks file, replaced through a temporary file,
the one before kept as .bak).

  python3 transition/server.py SITE/ [--port 8769]      then open http://127.0.0.1:8769/
"""
import argparse
import json
import os
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

MOST = 1 << 20


class Site(SimpleHTTPRequestHandler):
    labels: Path

    def reply(self, code, body=b"{}"):
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.split("?")[0] == "/labels":
            self.reply(200, self.labels.read_bytes() if self.labels.exists() else b"{}")
            return
        rng = self.headers.get("Range", "")
        path = Path(self.translate_path(self.path))
        if not rng.startswith("bytes=") or not path.is_file():
            super().do_GET()
            return
        size = path.stat().st_size
        first, _, last = rng[6:].partition("-")
        start = int(first) if first else max(0, size - int(last))
        end = min(int(last), size - 1) if first and last else size - 1
        with path.open("rb") as f:
            f.seek(start)
            body = f.read(end - start + 1)
        self.send_response(206)
        self.send_header("Content-Type", self.guess_type(str(path)))
        self.send_header("Accept-Ranges", "bytes")
        self.send_header("Content-Range", f"bytes {start}-{end}/{size}")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path != "/labels":
            self.reply(404)
            return
        n = int(self.headers.get("Content-Length", 0))
        if n > MOST:
            self.reply(413)
            return
        body = self.rfile.read(n)
        json.loads(body)
        if self.labels.exists():
            self.labels.replace(self.labels.with_suffix(".json.bak"))
        tmp = self.labels.with_suffix(".tmp")
        tmp.write_bytes(body)
        os.replace(tmp, self.labels)
        self.reply(200)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("site", type=Path)
    ap.add_argument("--port", type=int, default=8769)
    args = ap.parse_args()
    Site.labels = args.site / "labels.json"
    ThreadingHTTPServer(("127.0.0.1", args.port), partial(Site, directory=str(args.site))).serve_forever()


if __name__ == "__main__":
    main()
