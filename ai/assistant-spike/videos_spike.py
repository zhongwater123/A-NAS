"""Video moments for the assistant in the prototype sandbox (#322).

Stands in for what the photo service will offer once videos have vectors:
it holds the index of a set of test videos (eval.video_moments index), embeds
the model's probes with the AI Worker and answers on its own assistant
socket, which any member of a-nas-users may use, known by the caller's UID.

  POST /search   {"probes": [...], "limit": n,   shot cards (montage.moments),
                  "videos": [...]}                only in those videos if given
  POST /montage  {"segments": [...]}             the cut as video/mp4, or the
                                                 plan's problems with 422

It runs as the photo service's identity, the only one that reaches the AI
Worker; the assistant never reads the videos or their vectors. It runs with
ai/ (mounted at /spike-ai) on PYTHONPATH.
"""

import argparse
import base64
import grp
import json
import os
import pwd
import shutil
import socket
import socketserver
import struct
import tempfile
from http.server import BaseHTTPRequestHandler

import numpy as np

from montage import moments, render

PROBES_MAX = 8
PROBE_CHARS = 200
BODY_MAX = 1 << 16
MEMBERS = "a-nas-users"


def embed_query(ai_socket: str, text: str) -> np.ndarray:
    """One embed_query on the AI Worker (anas_ai.protocol): a length-prefixed JSON frame each way."""
    with socket.socket(socket.AF_UNIX) as conn:
        conn.settimeout(120)
        conn.connect(ai_socket)
        payload = json.dumps({"op": "embed_query", "text": text}).encode()
        conn.sendall(struct.pack(">I", len(payload)) + payload)
        data = b""
        while len(data) < 4 or len(data) < 4 + struct.unpack(">I", data[:4])[0]:
            more = conn.recv(1 << 16)
            if not more:
                raise OSError("the AI Worker hung up")
            data += more
    answer = json.loads(data[4:])
    if not answer.get("ok"):
        raise OSError(answer.get("error", {}).get("message", "the AI Worker failed"))
    return np.frombuffer(base64.b64decode(answer["vector"]), dtype="<f4")


def member(uid: int) -> bool:
    try:
        user = pwd.getpwuid(uid)
        group = grp.getgrnam(MEMBERS)
    except KeyError:
        return False
    return user.pw_gid == group.gr_gid or user.pw_name in group.gr_mem


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True

    def __init__(self, path, index, videos, ai_socket, work):
        if os.path.exists(path):
            os.remove(path)
        super().__init__(path, Handler)
        os.chmod(path, 0o666)  # noqa: S103  members connect as themselves; the UID decides
        self.index, self.videos, self.ai_socket, self.work = index, videos, ai_socket, work


class Handler(BaseHTTPRequestHandler):
    server: Server

    def address_string(self):
        return "assistant"

    def do_POST(self):
        creds = self.connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, struct.calcsize("3i"))
        if not member(struct.unpack("3i", creds)[1]):
            return self.reply(403, {"error": "只有家庭成员能用"})
        length = int(self.headers.get("Content-Length") or 0)
        if not 0 < length <= BODY_MAX:
            return self.reply(400, {"error": "请求体为空或太大"})
        try:
            body = json.loads(self.rfile.read(length))
        except ValueError:
            return self.reply(400, {"error": "请求体不是 JSON"})
        if self.path == "/search":
            return self.search(body)
        if self.path == "/montage":
            return self.montage(body)
        return self.reply(404, {"error": "没有这个接口"})

    def search(self, body):
        probes, limit, videos = body.get("probes"), body.get("limit", 20), body.get("videos")
        if (not isinstance(probes, list) or not 1 <= len(probes) <= PROBES_MAX
                or not all(isinstance(p, str) and 1 <= len(p.strip()) <= PROBE_CHARS for p in probes)):
            return self.reply(400, {"error": f"probes 要有 1–{PROBES_MAX} 条，每条 1–{PROBE_CHARS} 个字符"})
        if isinstance(limit, bool) or not isinstance(limit, int) or not 1 <= limit <= moments.CARDS_MAX:
            return self.reply(400, {"error": f"limit 要在 1–{moments.CARDS_MAX} 之间"})
        if videos is not None and (not isinstance(videos, list) or not videos
                                   or not all(v in self.server.index.videos for v in videos)):
            return self.reply(400, {"error": "videos 要是检索结果里出现过的视频编号列表"})
        try:
            vectors = {p: embed_query(self.server.ai_socket, p) for p in dict.fromkeys(probes)}
        except OSError as error:
            return self.reply(503, {"error": f"设备上的 AI 暂时不可用：{error}"})
        return self.reply(200, self.server.index.search(vectors, limit, videos))

    def montage(self, body):
        segments = body.get("segments")
        if not isinstance(segments, list):
            return self.reply(400, {"error": "segments 要是列表"})
        try:
            clips = self.server.index.check(segments)
        except moments.PlanError as error:
            return self.reply(422, {"error": "剪辑计划不合规则", "problems": error.problems})
        files = {v: os.path.join(self.server.videos, meta["file"]) for v, meta in self.server.index.videos.items()}
        work = tempfile.mkdtemp(dir=self.server.work)
        try:
            path = render.render_clips([(files[v], start, end) for v, start, end in clips], work)
            with open(path, "rb") as file:
                data = file.read()
        except render.RenderError as error:
            return self.reply(500, {"error": str(error)})
        finally:
            shutil.rmtree(work, ignore_errors=True)
        self.send_response(200)
        self.send_header("Content-Type", "video/mp4")
        self.send_header("Content-Length", str(len(data)))
        self.send_header("X-Seconds", f"{sum(end - start for _v, start, end in clips):.2f}")
        self.end_headers()
        self.wfile.write(data)
        return None

    def reply(self, status, data):
        raw = json.dumps(data, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--socket", required=True)
    parser.add_argument("--index", required=True, help="the model's directory with index.json and frames.npz")
    parser.add_argument("--videos", required=True, help="the folder with the set's video files")
    parser.add_argument("--ai-socket", required=True)
    parser.add_argument("--work", required=True, help="a private folder for renders in progress")
    args = parser.parse_args()
    index = moments.Index.load(os.path.join(args.index, "index.json"), os.path.join(args.index, "frames.npz"))
    with Server(args.socket, index, args.videos, args.ai_socket, args.work) as server:
        print(f"videos-ready {len(index.cards)} cards", flush=True)
        server.serve_forever()


if __name__ == "__main__":
    main()
