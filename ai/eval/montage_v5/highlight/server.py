"""Keep the labelling page's marks: GET /labels returns them, POST /labels replaces them.

The page is served by a static server that forwards what is not a file here (http-server --proxy), the same
way as 第七版's search.py. The marks file is replaced whole on each save, through a temporary file in the
same folder, and the one before is kept as <name>.bak.

  python3 server.py --labels /site/labels-pj.json --port 8768
"""
import argparse
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

MOST = 1 << 20      # bytes: 432 beats' marks are a few KiB


def handler(path):
    class Labels(BaseHTTPRequestHandler):
        def reply(self, code, body=b"{}"):
            self.send_response(code)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            if self.path != "/labels":
                return self.reply(404)
            return self.reply(200, path.read_bytes() if path.exists() else b"{}")

        def do_POST(self):
            if self.path != "/labels":
                return self.reply(404)
            size = int(self.headers.get("Content-Length", 0))
            if not 0 < size <= MOST:
                return self.reply(413)
            try:
                data = json.loads(self.rfile.read(size))
            except ValueError:
                return self.reply(400)
            if not isinstance(data, dict) or not isinstance(data.get("labels"), dict):
                return self.reply(400)
            tmp = path.with_name(path.name + ".tmp")
            tmp.write_text(json.dumps(data, ensure_ascii=False, indent=1), encoding="utf-8")
            if path.exists():
                os.replace(path, path.with_name(path.name + ".bak"))
            os.replace(tmp, path)
            return self.reply(200)

    return Labels


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--labels", type=Path, required=True)
    ap.add_argument("--host", default="0.0.0.0")  # noqa: S104 (inside a container; the port is published on 127.0.0.1)
    ap.add_argument("--port", type=int, default=8768)
    args = ap.parse_args()
    ThreadingHTTPServer((args.host, args.port), handler(args.labels)).serve_forever()


if __name__ == "__main__":
    main()
