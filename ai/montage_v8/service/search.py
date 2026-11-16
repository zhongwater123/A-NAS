"""Search the prototype's beats by what a picture shows: a text query against the beats' window vectors.

EmbeddingGemma 2 puts text and images in one space; the video index already holds each 2 s window's
key-frame vector, and build.py groups them by beat in beats.npz. A query is embedded with A-NAS's own
MediaPipeProvider and its retrieval prompt, and a beat scores as its best window.

Runs in the anas-ai-dev image, where MediaPipe is installed (docs/research/video-montage-v7.md):

  GET /search?q=雪山上的直升机   → {"model": ..., "seconds": ..., "scores": {"pj-046/2": 0.41, ...}}

It also keeps the page's back-office log, one JSON line per confirmed film (--records):

  POST /record {"action": "直接确认", "film": "place-1", ...}   → {"ok": true, "time": ...}
  GET  /records                                               → {"records": [newest first]}

and says how alike two beats' pictures are (the mean of their windows), for the shot that covers a deleted one:

  GET /likeness?of=pj-009/2&to=pj-017/1&to=pj-001/1         → {"scores": {"pj-017/1": 0.81, ...}}

2026-11-16 (第八版): it also embeds pictures, for indexing uploaded videos (adapters/embedder.py); without
--beats it serves only that:

  POST /embed  (a JPEG)                                       → {"model": ..., "vector": [...]}
  GET  /model                                                 → {"model": ...}
"""
import argparse
import json
import threading
import time
from datetime import UTC, datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse

import numpy as np

from anas_ai import providers


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", required=True)
    parser.add_argument("--manifest", required=True)
    parser.add_argument("--beats", help="the page's beats.npz; without it only /embed is served")
    parser.add_argument("--weight-cache")
    parser.add_argument("--port", type=int, default=8766)
    parser.add_argument("--records", help="JSONL file the confirmations are appended to")
    args = parser.parse_args()
    records = Path(args.records) if args.records else None
    writing = threading.Lock()

    if args.beats:
        with np.load(args.beats) as data:
            keys, owner, vectors = data["keys"].tolist(), data["owner"], data["vectors"]
            built_with = str(data["model"])
    else:
        keys, owner, vectors, built_with = [], np.zeros(0, dtype=np.int32), np.zeros((0, 1), dtype=np.float32), None
    started = time.monotonic()
    provider = providers.MediaPipeProvider(args.model, args.manifest, cache_dir=args.weight_cache)
    model = threading.Lock()                      # one call into the model at a time: requests come on many threads
    if built_with and provider.model != built_with:
        raise SystemExit(f"the beats' vectors come from {built_with}, the model here is {provider.model}")
    print(f"{provider.model} loaded in {time.monotonic() - started:.1f} s; {len(keys)} beats", flush=True)
    cache = {}
    # a beat's picture as one vector: the mean of its windows, for picture-to-picture likeness
    pictures = np.zeros((len(keys), vectors.shape[1]), dtype=np.float32)
    np.add.at(pictures, owner, vectors)
    if len(keys):
        pictures /= np.linalg.norm(pictures, axis=1, keepdims=True)
    row = {k: n for n, k in enumerate(keys)}

    def scores(query):
        if query not in cache:
            with model:
                q = np.asarray(provider.embed_query(query), dtype=np.float32)
            best = np.full(len(keys), -1.0, dtype=np.float32)
            np.maximum.at(best, owner, vectors @ q)
            cache[query] = {k: round(float(s), 4) for k, s in zip(keys, best, strict=True)}
        return cache[query]

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            if urlparse(self.path).path == "/embed":     # a JPEG in, its vector out: 第八版 indexes uploaded videos with it
                size = int(self.headers.get("Content-Length") or 0)
                if not 0 < size <= 8 << 20:
                    return self.reply(413, {"error": "one JPEG up to 8 MiB"})
                image = self.rfile.read(size)
                try:
                    with model:
                        vector = np.asarray(provider.embed_image(image), dtype=np.float32)
                except Exception as error:  # noqa: BLE001  a picture the model cannot read is the caller's to report
                    return self.reply(400, {"error": f"could not embed: {error}"})
                return self.reply(200, {"model": provider.model, "vector": [round(float(x), 7) for x in vector]})
            if urlparse(self.path).path == "/model":
                return self.reply(200, {"model": provider.model})
            if urlparse(self.path).path != "/record" or records is None:
                return self.reply(404, {"error": "POST /record, with --records set"})
            size = int(self.headers.get("Content-Length") or 0)
            if not 0 < size <= 65536:
                return self.reply(413, {"error": "a record is one JSON object up to 64 KiB"})
            try:
                entry = json.loads(self.rfile.read(size))
            except ValueError:
                entry = None
            if not isinstance(entry, dict):
                return self.reply(400, {"error": "a record is one JSON object"})
            entry["time"] = datetime.now(UTC).isoformat(timespec="seconds")
            with writing, records.open("a", encoding="utf-8") as out:
                out.write(json.dumps(entry, ensure_ascii=False) + "\n")
            self.reply(200, {"ok": True, "time": entry["time"]})

        def do_GET(self):
            url = urlparse(self.path)
            if url.path == "/likeness":                   # ?of=pj-009/2&to=pj-017/1&to=...: how alike the pictures are
                asked = parse_qs(url.query)
                of, to = asked.get("of", [""])[0], asked.get("to", [])
                if of not in row or not all(t in row for t in to):
                    return self.reply(404, {"error": "unknown beat"})
                return self.reply(200, {"scores": {t: round(float(pictures[row[of]] @ pictures[row[t]]), 4) for t in to}})
            if url.path == "/model":
                return self.reply(200, {"model": provider.model})
            if url.path == "/records":
                lines = records.read_text(encoding="utf-8").splitlines() if records and records.exists() else []
                return self.reply(200, {"records": [json.loads(line) for line in reversed(lines[-500:])]})
            query = parse_qs(url.query).get("q", [""])[0].strip()
            if url.path != "/search" or not query:
                return self.reply(404, {"error": "GET /search?q=<text>"})
            began = time.monotonic()
            found = scores(query)
            self.reply(200, {"model": provider.model, "seconds": round(time.monotonic() - began, 3), "scores": found})

        def reply(self, status, body):
            data = json.dumps(body, ensure_ascii=False).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(data)))
            self.send_header("Access-Control-Allow-Origin", "*")   # the page is served from another local port
            self.end_headers()
            self.wfile.write(data)

        def log_message(self, fmt, *rest):
            pass

    # all interfaces inside the container; docker publishes the port on 127.0.0.1 only
    ThreadingHTTPServer(("0.0.0.0", args.port), Handler).serve_forever()  # noqa: S104


if __name__ == "__main__":
    main()
