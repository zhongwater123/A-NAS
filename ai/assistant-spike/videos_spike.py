"""Video moments for the assistant in the prototype sandbox (#322).

Stands in for what the photo service will offer once videos have vectors:
it holds the index of a set of test videos (eval.video_moments index), embeds
the model's probes with the AI Worker and answers on its own assistant
socket, which any member of a-nas-users may use, known by the caller's UID.

  POST /search   {"probes": [...], "limit": n,   shot cards (montage.moments), or
                  "each": n,                      every probe's own best n
                  "pictures": true,               an experiment (#326): each card's three
                                                  frames side by side as JPEG in place of
                                                  its caption, for a planner that sees
                  "screen": true}                 another (#326, needs --vision): every probe
                                                  recalls RECALL cards, the vision model
                                                  judges each on its key frames, and each
                                                  is then how many that pass a probe keeps
                  "videos": [...],                only in those videos if given,
                  "lights": [...],                only cards of these lights (look.LIGHTS),
                  "words": [...],                 only cards whose caption or highlight holds one,
                  "when": [{"is", "not"}]}        only over windows closer to is than to not
  POST /catalog  {"videos": [...],               every card as a line of seconds, light,
                  "times": false,                 caption and highlight, to write probes from;
                  "scenes": true,                 without times, no seconds; with scenes, each
                  "lengths": true}                card's scene and a table of scenes; with
                                                  lengths, the seconds each card can run (#350)
  POST /montage  {"segments": [...],             the cut as video/mp4, or the
                  "skipped": [{card, why}],       (cards left out, checked when given)
                  "span": "probe" | "frame",      an experiment (#350): no start or end, but
                                                  each segment's probe; the seconds grow from
                                                  the window it matched (moments.Index.span),
                  "seconds": n, "style": s,       or, with seconds, add up to them, frames
                                                  every half second setting the ends
                                                  (moments.Index.fill)
                  "edges": true,                  and a cut at either end trims it (EDGE)
                  "look": false,                  no review by the vision model
                  "scenes": true,                 a scene's segments together, at most 3
                  "margin": true}                 cards able to run 1.1 times the seconds
                                                 plan's problems with 422

With --vision, the URL of a local vision model (montage.look), the cards carry
what it said of their key frames (captions.json in the index) and a plan is
looked at before it is cut: every segment says in shows what it is to show,
and frames every half second are asked whether they do. A segment with no
run of 1.5 s that does is sent back with what the frames showed (422); the
others are trimmed to their run. What was seen goes back in X-Review.

It runs as the photo service's identity, the only one that reaches the AI
Worker; the assistant never reads the videos or their vectors. It runs with
ai/ (mounted at /spike-ai) on PYTHONPATH.
"""

import argparse
import base64
import grp
import json
import math
import os
import pwd
import shutil
import socket
import socketserver
import struct
import subprocess
import tempfile
from concurrent.futures import ThreadPoolExecutor
from http.server import BaseHTTPRequestHandler

import numpy as np

from montage import look, moments, render

PROBES_MAX = 8
WORDS_MAX = 8
WORD_CHARS = 20
WHEN_MAX = 4
PROBE_CHARS = 200
BODY_MAX = 1 << 16
# Each frame of a card's picture: this wide, and the JPEG quality FFmpeg's scale gives (2-31).
PICTURE_WIDTH = 480
PICTURE_QUALITY = 5
# Cards every probe recalls for the vision model to screen.
RECALL = 20
# An experiment (#350): the first and last EDGE seconds of each segment are scanned at the
# source's frame rate, and a cut scoring EDGE_CUT or more there (FFmpeg's scene score, which
# a dark film keeps low: 0.12 for a cut 0.25 misses) trims the segment to its side of it.
EDGE = 0.6
EDGE_CUT = 0.08
# A cut this close outside an end is scanned for too, and an end is kept this far from a cut:
# from 24 frames a second to 30, an end 3 ms before a cut took one frame of the next shot.
GUARD = 0.1
CLEAR = 0.05
MEMBERS = "a-nas-users"


def embed_query(ai_socket: str, text: str) -> np.ndarray:
    """One embed_query on the AI Worker (anas_ai.protocol): a length-prefixed JSON frame each way."""
    return _embedded(ai_socket, {"op": "embed_query", "text": text})


def embed_image(ai_socket: str, jpeg: bytes) -> np.ndarray:
    """One embed_image on the AI Worker: the image travels as a descriptor beside the frame."""
    fd = os.memfd_create("frame")
    try:
        os.write(fd, jpeg)
        os.lseek(fd, 0, os.SEEK_SET)
        return _embedded(ai_socket, {"op": "embed_image"}, fd)
    finally:
        os.close(fd)


def _embedded(ai_socket: str, request: dict, fd: int | None = None) -> np.ndarray:
    with socket.socket(socket.AF_UNIX) as conn:
        conn.settimeout(120)
        conn.connect(ai_socket)
        payload = json.dumps(request).encode()
        frame = struct.pack(">I", len(payload)) + payload
        if fd is None:
            conn.sendall(frame)
        else:
            socket.send_fds(conn, [frame], [fd])
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


def edges(path: str, start, end):
    """start and end, each moved inside a cut found within EDGE inside it or GUARD outside it,
    at the source's frame rate, and kept CLEAR of it."""
    if not all(isinstance(t, (int, float)) and not isinstance(t, bool) for t in (start, end)) or end - start <= 2 * EDGE:
        return start, end
    at = max(0.0, start - GUARD)
    cuts = [at + t for t in scene_cuts(path, at, start + EDGE - at)]
    if cuts:
        start = max(start, math.ceil((max(cuts) + CLEAR) * 100) / 100)
    cuts = [end - EDGE + t for t in scene_cuts(path, end - EDGE, EDGE + GUARD)]
    if cuts:
        end = min(end, math.floor((min(cuts) - CLEAR) * 100) / 100)
    return start, end


def scene_cuts(path: str, at: float, seconds: float) -> list[float]:
    """The seconds after at, within seconds, of frames FFmpeg scores EDGE_CUT or more as a new scene."""
    log = subprocess.run(
        ["ffmpeg", "-nostdin", "-v", "info", "-ss", f"{at:.3f}", "-i", path, "-t", f"{seconds:.3f}",  # noqa: S607
         "-vf", "scale=640:-2,select='gte(scene,0)',metadata=print:key=lavfi.scene_score", "-an", "-f", "null", "-"],
        capture_output=True, text=True, timeout=120, check=False).stderr
    times, out = [], []
    for line in log.splitlines():
        if "pts_time:" in line:
            times.append(float(line.split("pts_time:")[1].split()[0]))
        # The first frame has nothing before it to differ from.
        elif "lavfi.scene_score=" in line and len(times) > 1 and float(line.split("=")[1]) >= EDGE_CUT:
            out.append(times[-1])
    return out


def member(uid: int) -> bool:
    try:
        user = pwd.getpwuid(uid)
        group = grp.getgrnam(MEMBERS)
    except KeyError:
        return False
    return user.pw_gid == group.gr_gid or user.pw_name in group.gr_mem


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True

    def __init__(self, path, index, videos, ai_socket, work, vision=None):
        if os.path.exists(path):
            os.remove(path)
        super().__init__(path, Handler)
        os.chmod(path, 0o666)  # noqa: S103  members connect as themselves; the UID decides
        self.index, self.videos, self.ai_socket, self.work = index, videos, ai_socket, work
        # What the vision model saw, by (video, start, end, shows): a plan sent again after a
        # refusal is looked at only where it changed.
        self.vision, self.seen = vision, {}
        self.pictures, self.keyframes, self.verdicts = {}, {}, {}
        # The probes' vectors, by text, for segments whose seconds are set from them, and the
        # frames embedded to set them, by (video, seconds).
        self.probes, self.frames = {}, {}


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
        if self.path == "/catalog":
            videos = body.get("videos")
            if videos is not None and (not isinstance(videos, list) or not videos
                                       or not all(v in self.server.index.videos for v in videos)):
                return self.reply(400, {"error": "videos 要是库里的视频编号列表"})
            return self.reply(200, self.server.index.catalog(videos, times=body.get("times") is not False,
                                                             scenes=body.get("scenes") is True,
                                                             lengths=body.get("lengths") is True))
        if self.path == "/montage":
            return self.montage(body)
        return self.reply(404, {"error": "没有这个接口"})

    def search(self, body):
        probes, limit, videos = body.get("probes"), body.get("limit", 20), body.get("videos")
        each = body.get("each")
        when = body.get("when", [])
        lights, words = body.get("lights"), body.get("words")
        if lights is not None and (not isinstance(lights, list) or not lights
                                   or not all(light in look.LIGHTS for light in lights)):
            return self.reply(400, {"error": f"lights 要是 {'、'.join(look.LIGHTS)} 里的几个"})
        if words is not None and (not isinstance(words, list) or not 1 <= len(words) <= WORDS_MAX
                                  or not all(isinstance(w, str) and 1 <= len(w.strip()) <= WORD_CHARS for w in words)):
            return self.reply(400, {"error": f"words 要有 1–{WORDS_MAX} 个词，每个 1–{WORD_CHARS} 个字"})
        if (not isinstance(probes, list) or not 1 <= len(probes) <= PROBES_MAX
                or not all(isinstance(p, str) and 1 <= len(p.strip()) <= PROBE_CHARS for p in probes)):
            return self.reply(400, {"error": f"probes 要有 1–{PROBES_MAX} 条，每条 1–{PROBE_CHARS} 个字符"})
        if isinstance(limit, bool) or not isinstance(limit, int) or not 1 <= limit <= moments.CARDS_MAX:
            return self.reply(400, {"error": f"limit 要在 1–{moments.CARDS_MAX} 之间"})
        if each is not None and (isinstance(each, bool) or not isinstance(each, int)
                                 or not 1 <= each * len(probes) <= moments.CARDS_MAX):
            return self.reply(400, {"error": f"each 乘以 probes 的条数要在 1–{moments.CARDS_MAX} 之间"})
        if videos is not None and (not isinstance(videos, list) or not videos
                                   or not all(v in self.server.index.videos for v in videos)):
            return self.reply(400, {"error": "videos 要是检索结果里出现过的视频编号列表"})
        if (not isinstance(when, list) or len(when) > WHEN_MAX
                or not all(isinstance(w, dict) and all(isinstance(w.get(k), str)
                                                       and 1 <= len(w[k].strip()) <= PROBE_CHARS for k in ("is", "not"))
                           for w in when)):
            return self.reply(400, {"error": f"when 最多 {WHEN_MAX} 条，每条是 {{is, not}} 两句短描述"})
        try:
            texts = dict.fromkeys([*probes, *(w[k] for w in when for k in ("is", "not"))])
            vectors = {t: embed_query(self.server.ai_socket, t) for t in texts}
        except OSError as error:
            return self.reply(503, {"error": f"设备上的 AI 暂时不可用：{error}"})
        pairs = [(vectors[w["is"]], vectors[w["not"]]) for w in when]
        screen = body.get("screen") is True
        if screen and self.server.vision is None:
            return self.reply(400, {"error": "没有本机视觉模型，不能筛卡片"})
        found = self.server.index.search({p: vectors[p] for p in probes}, limit, videos, pairs,
                                         RECALL if screen else each, lights, [w.strip() for w in words] if words else None)
        if screen:
            asks = list(dict.fromkeys((i, probe) for probe, ids in found["each"].items() for i in ids))
            cards = {card["id"]: card for card in found["cards"]}
            try:
                with ThreadPoolExecutor(look.WORKERS) as pool:
                    said = list(pool.map(lambda ask: self.verdict(cards[ask[0]], ask[1]), asks))
            except look.VisionError as error:
                return self.reply(503, {"error": str(error)})
            found = moments.screened(found, dict(zip(asks, said, strict=True)), each or 5)
        if body.get("pictures") is True:
            with ThreadPoolExecutor(4) as pool:
                pictures = list(pool.map(self.picture, found["cards"]))
            for card, picture in zip(found["cards"], pictures, strict=True):
                card.pop("caption", None)
                card["picture"] = base64.b64encode(picture).decode()
        return self.reply(200, found)

    def verdict(self, card, probe):
        """What the vision model said of the card's key frames against probe (look.screened)."""
        key = (card["id"], probe)
        if key not in self.server.verdicts:
            if card["id"] not in self.server.keyframes:
                video, seconds = self.seconds(card)
                self.server.keyframes[card["id"]] = [subprocess.run(
                    ["ffmpeg", "-nostdin", "-v", "error", "-ss", f"{t:.3f}", "-i", video,  # noqa: S607
                     "-frames:v", "1", "-vf", f"scale=-2:{look.EDGE}", "-q:v", "4", "-f", "image2", "-c:v", "mjpeg", "-"],
                    check=True, capture_output=True, timeout=120).stdout for t in seconds]
            self.server.verdicts[key] = self.server.vision.screen(self.server.keyframes[card["id"]], probe)
        return self.server.verdicts[key]

    def seconds(self, card):
        """The card's video file and the seconds of its key frames, the ones its caption is written from."""
        index = self.server.index
        full = next(c for c in index.cards if c["id"] == card["id"])
        return (os.path.join(self.server.videos, index.videos[card["video"]]["file"]),
                [index.frame_ms[i] / 1000 for i in index.keyframes(full, look.KEYFRAMES)])

    def picture(self, card):
        """The card's three key frames side by side."""
        if card["id"] not in self.server.pictures:
            video, seconds = self.seconds(card)
            inputs = [a for t in seconds for a in ("-ss", f"{t:.3f}", "-i", video)]
            scaled = ";".join(f"[{k}:v]scale={PICTURE_WIDTH}:-2[f{k}]" for k in range(len(seconds)))
            joined = "".join(f"[f{k}]" for k in range(len(seconds)))
            graph = scaled + (f";{joined}hstack=inputs={len(seconds)}[out]" if len(seconds) > 1 else ";[f0]null[out]")
            self.server.pictures[card["id"]] = subprocess.run(
                ["ffmpeg", "-nostdin", "-v", "error", *inputs, "-filter_complex", graph, "-map", "[out]",  # noqa: S607
                 "-frames:v", "1", "-q:v", str(PICTURE_QUALITY), "-f", "image2", "-c:v", "mjpeg", "-"],
                check=True, capture_output=True, timeout=120).stdout
        return self.server.pictures[card["id"]]

    def montage(self, body):
        segments = body.get("segments")
        if not isinstance(segments, list):
            return self.reply(400, {"error": "segments 要是列表"})
        spans = body.get("span")
        if spans is not None:
            if spans not in moments.SPANS:
                return self.reply(400, {"error": f"span 要是 {'、'.join(moments.SPANS)} 之一"})
            target = body.get("seconds")
            if target is not None and (isinstance(target, bool) or not isinstance(target, (int, float))
                                       or not 5 <= target <= 600):
                return self.reply(400, {"error": "seconds 要是 5–600 的秒数"})
            # "pace": "picture": each segment takes its card's pace from how much its picture
            # changes (moments.Index.pace_of), not one the planner wrote (#350).
            if body.get("pace") == "picture":
                segments = [s | {"pace": self.server.index.pace_of(s.get("card")) or "中"} if isinstance(s, dict) else s
                            for s in segments]
            try:
                if target is None:
                    segments, problems = self.spanned(segments, spans)
                else:
                    segments, problems = self.filled(segments, spans, target, body.get("style"),
                                                     body.get("margin") is True)
            except OSError as error:
                return self.reply(503, {"error": f"设备上的 AI 暂时不可用：{error}"})
            if problems:
                return self.reply(422, {"error": "剪辑计划不合规则", "problems": problems})
        edged = []
        if body.get("edges") is True:
            files = {v: os.path.join(self.server.videos, meta["file"]) for v, meta in self.server.index.videos.items()}
            cards = {c["id"]: c["video"] for c in self.server.index.cards}
            for segment in segments:
                if isinstance(segment, dict) and segment.get("card") in cards:
                    start, end = edges(files[cards[segment["card"]]], segment.get("start"), segment.get("end"))
                    edged.append([start, end] != [segment.get("start"), segment.get("end")])
                    segment["start"], segment["end"] = start, end
        try:
            clips = self.server.index.check(segments, shows_needed=self.server.vision is not None,
                                            skipped=body.get("skipped"), scenes=body.get("scenes") is True,
                                            shortest=moments.TRIMMED_MIN if edged else moments.SEGMENT_MIN)
        except moments.PlanError as error:
            return self.reply(422, {"error": "剪辑计划不合规则", "problems": error.problems})
        files = {v: os.path.join(self.server.videos, meta["file"]) for v, meta in self.server.index.videos.items()}
        review = []
        # An experiment (#350): "look": false cuts the plan without the vision model's review.
        if self.server.vision is not None and body.get("look") is not False:
            try:
                review = [self.look(files[v], v, start, end, segment["shows"])
                          for (v, start, end), segment in zip(clips, segments, strict=True)]
            except look.VisionError as error:
                return self.reply(503, {"error": str(error)})
            refused = [f"第 {n} 段（{s['card']} 的 {s['start']:g}–{s['end']:g} 秒）看画面不是“{s['shows']}”："
                       f"{r['frames']} 帧里 {r['fit']} 帧符合，画面是{r['seen']}。换一张卡片或删掉这段；画面若符合指令的另一部分，可以改 shows 换个位置用"
                       for n, (s, r) in enumerate(zip(segments, review, strict=True), 1) if not r["usable"]]
            if refused:
                return self.reply(422, {"error": "出片前看了画面，有的段不切题", "problems": refused, "review": review})
            clips = [(v, r["start"], r["end"]) for (v, _s, _e), r in zip(clips, review, strict=True)]
        elif spans is not None:
            # Not looked at: each segment as cut, so that the plan kept beside the cut has its seconds.
            review = [{"start": start, "end": end, "frames": 0, "fit": 0, "seen": "", "usable": True}
                      for _v, start, end in clips]
        if review and spans is not None:
            review = [r | {"planned": [s["start"], s["end"]]} for s, r in zip(segments, review, strict=True)]
        if review and spans is not None:
            review = [r | {k: s[k] for k in ("pace", "asked") if s.get(k)} for s, r in zip(segments, review, strict=True)]
        if review and edged:
            review = [r | {"edged": e} for r, e in zip(review, edged, strict=True)]
        work = tempfile.mkdtemp(dir=self.server.work)
        try:
            path = render.render_clips([(files[v], start, end) for v, start, end in clips], work,
                                       ending=spans is not None)
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
        if review:
            self.send_header("X-Review", base64.b64encode(json.dumps(review, ensure_ascii=False).encode()).decode())
        self.end_headers()
        self.wfile.write(data)
        return None

    def spanned(self, segments, mode):
        """The segments with start and end set from the window their probe matched (Index.span),
        in order, none over another; and the problems of those it cannot set."""
        index, cards = self.server.index, {c["id"] for c in self.server.index.cards}
        out, problems, taken = [], [], []
        for n, segment in enumerate(segments, 1):
            card = segment.get("card") if isinstance(segment, dict) else None
            probe = segment.get("probe") if isinstance(segment, dict) else None
            if card not in cards or not isinstance(probe, str) or not 1 <= len(probe.strip()) <= PROBE_CHARS:
                problems.append(f"第 {n} 段要有目录或检索结果里的 card，和要找的画面（shows）")
                continue
            if probe not in self.server.probes:
                self.server.probes[probe] = embed_query(self.server.ai_socket, probe)
            span = index.span(card, self.server.probes[probe], mode, taken)
            if span is None:
                problems.append(f"第 {n} 段的 {card} 剪不出 1.5 秒：镜头太短、前面的段用了它，或只剩纯色画面")
                continue
            taken.append((card.split("-", 1)[0], *span))
            out.append(segment | {"start": span[0], "end": span[1]})
        return out, problems

    def filled(self, segments, mode, target, style, margin=False):
        """The segments with start and end set to add up to target (Index.fill): frames every
        half second, embedded as they are needed, set the ends; a trailer's last quarter is
        held to moments.TAIL_MIN-TAIL_MAX a segment; or, when the segments say their pace
        (moments.PACES), each is held to its pace's range and aims at its pace's seconds, all
        scaled to target. With margin, the cards must be able to run
        moments.MARGIN times target first. And the problems of those it cannot set."""
        index, cards = self.server.index, {c["id"] for c in self.server.index.cards}
        problems = [f"第 {n} 段要有目录或检索结果里的 card，和要找的画面（shows）" for n, s in enumerate(segments, 1)
                    if not isinstance(s, dict) or s.get("card") not in cards or not isinstance(s.get("probe"), str)
                    or not 1 <= len(s["probe"].strip()) <= PROBE_CHARS]
        if problems:
            return [], problems
        for segment in segments:
            if segment["probe"] not in self.server.probes:
                self.server.probes[segment["probe"]] = embed_query(self.server.ai_socket, segment["probe"])
        paces = [s.get("pace") for s in segments]
        shares = None
        if any(paces):
            wrong = [f"第 {n} 段的 pace 要是{'、'.join(moments.PACES)}之一" for n, p in enumerate(paces, 1)
                     if p not in moments.PACES]
            if wrong:
                return [], wrong
            segments = moments.settled(segments)
            paces = [s["pace"] for s in segments]
            mins, aims, caps = (list(column) for column in zip(*(moments.PACES[p] for p in paces), strict=True))
            if sum(caps) < target - 1 or sum(mins) > target + 1:
                return [], [f"按这些段的 pace，成片在 {sum(mins):g}–{sum(caps):g} 秒之间，目标是 {target:g} 秒："
                            + ("加几段，或换节奏更慢的段" if sum(caps) < target - 1 else "删几段，或换节奏更快的段")]
            # Each segment's share: its pace's aim, all scaled to the target and kept to the pace.
            scale = target / sum(aims)
            shares = [min(cap, max(low, aim * scale)) for low, aim, cap in zip(mins, aims, caps, strict=True)]
        else:
            tail = len(segments) - max(1, round(len(segments) / 4))
            trailer = [style == "预告片" and n >= tail for n in range(len(segments))]
            caps = [moments.TAIL_MAX if t else moments.CLIP_MAX for t in trailer]
            mins = [moments.TAIL_MIN if t else moments.SEGMENT_MIN for t in trailer]
        if margin:
            cards = {c["id"]: c for c in index.cards}
            most = sum(min(index.usable(cards[s["card"]]), cap) for s, cap in zip(segments, caps, strict=True))
            if most < moments.MARGIN * target:
                return [], [f"所选卡片最多只能剪 {most:g} 秒，要有目标 {target:g} 秒的 {moments.MARGIN:g} 倍"
                            f"（{moments.MARGIN * target:g} 秒）程序才有余量定每段的长短：再加几段，或换可剪更长的卡片"]
        spans, problems = index.fill([(s["card"], self.server.probes[s["probe"]]) for s in segments], mode, target,
                                     self.frame, caps, mins, shares, moments.SEGMENT_MIN if shares else None)
        out = [s | {"start": span[0], "end": span[1]} for s, span in zip(segments, spans, strict=True) if span]
        # A card too short for its segment's pace gives a faster pace instead of a refusal.
        for segment in out if shares else []:
            pace = moments.slowest(segment["end"] - segment["start"])
            if moments.PACES[pace][0] < moments.PACES[segment["pace"]][0]:
                segment |= {"asked": segment.get("asked", segment["pace"]), "pace": pace}
        return out, problems

    def frame(self, video, seconds):
        """The vector of the frame at seconds, rendered like the index's frames (512 px long edge)."""
        key = (video, round(seconds, 2))
        if key not in self.server.frames:
            path = os.path.join(self.server.videos, self.server.index.videos[video]["file"])
            jpeg = subprocess.run(
                ["ffmpeg", "-nostdin", "-v", "error", "-ss", f"{seconds:.3f}", "-i", path, "-frames:v", "1",  # noqa: S607
                 "-vf", "scale='if(gt(iw,ih),512,-2)':'if(gt(iw,ih),-2,512)':flags=lanczos", "-q:v", "3",
                 "-f", "image2", "-c:v", "mjpeg", "-"], check=True, capture_output=True, timeout=120).stdout
            self.server.frames[key] = embed_image(self.server.ai_socket, jpeg)
        return self.server.frames[key]

    def look(self, path, video, start, end, shows):
        key = (video, start, end, shows)
        if key not in self.server.seen:
            self.server.seen[key] = look.review(self.server.vision, path, start, end, shows)
        return self.server.seen[key]

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
    parser.add_argument("--vision", help="a local vision model's chat completions URL, to look before a cut")
    args = parser.parse_args()
    index = moments.Index.load(os.path.join(args.index, "index.json"), os.path.join(args.index, "frames.npz"))
    vision = look.Vision(args.vision) if args.vision else None
    with Server(args.socket, index, args.videos, args.ai_socket, args.work, vision) as server:
        print(f"videos-ready {len(index.cards)} cards, {len(index.captions)} captions, {len(index.pictures)} measured,"
              f" {'looking' if vision else 'not looking'} before a cut", flush=True)
        server.serve_forever()


if __name__ == "__main__":
    main()
