"""One test case of the A/B comparison (#341, #343), cut by one command (#350):

    python -m montage.case /case/case-01.json /out

The case (#342) names its sources with their SHA-256, beside it, and asks for a
target length and an instruction. The command cuts one montage of about that
length with the sources' own sound and writes, in /out:

    A-<id>.mp4     the cut
    plan.json      its segments, as montage.moments checks them
    pipeline.json  the time of every step in #343's categories, the outside
                   calls and the memory peak (montage.timing); written also
                   when a step fails, with the reason

Steps, each timed:

    sources  读取与解码  each source checked against its SHA-256; its length, size and sound
    decode   读取与解码  one decode of each source: a frame every WINDOW seconds for the
                         embedding model and the shot cuts (FFmpeg's scene score)
    embed    理解        EmbeddingGemma vectors of the frames and of the instruction
    labels   理解        the photo labels nearest each frame, for the plan's reasons
    cards    切分        shots cut where their picture changes (montage.moments)
    plan     选择与编排  the rule plan for the instruction (montage.moments.Index.plan)
    fill     选择与编排  more cards, best first, while the cut is short of the target
    render   渲染        the cut, with sound (montage.render)

This version plans by rule alone, the fallback when the planning model off the
host cannot be reached; the vision model and the planning model come next.
"""

import argparse
import hashlib
import itertools
import json
import math
import os
import shutil
import subprocess
import sys
import tempfile

import numpy as np
from PIL import Image

from montage import moments, render, timing

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
EMBEDDER = "/models/embeddinggemma-2-740m.litertlm"
MANIFEST = os.path.join(REPO, "deploy", "models", "embeddinggemma-2-740m.json")
LABELS = os.path.join(REPO, "internal", "photos", "labels", "v1.json")
PROVIDER = "A"

# The montage index of #326: a window every 2 s, its frame in the middle, rendered like
# the photo service's AI input (512 px long edge, JPEG 82).
WINDOW = 2
EDGE = 512
QUALITY = 82
# A cut is a frame whose scene score passes SCENE; one closer than SHOT_MIN to the last is a
# flash. A frame whose grey levels spread less than FLAT is black or one colour.
SCENE = 0.25
SHOT_MIN = 1.0
FLAT = 8.0
TAGS = 3
TIMEOUT = 3600


class CaseError(Exception):
    pass


def read_case(path: str) -> dict:
    """The case, its sources' paths beside it; CaseError says what is missing."""
    with open(path, encoding="utf-8") as file:
        case = json.load(file)
    request = case.get("request") or {}
    target = request.get("target_seconds")
    if not isinstance(case.get("id"), str) or not case["id"]:
        raise CaseError("case.json 缺少 id")
    if not isinstance(target, (int, float)) or isinstance(target, bool) or target <= 0:
        raise CaseError("request.target_seconds 要是正的秒数")
    if not case.get("sources"):
        raise CaseError("sources 是空的")
    here = os.path.dirname(os.path.abspath(path))
    sources = []
    for n, source in enumerate(case["sources"], 1):
        name = source.get("file", "")
        if not name or os.path.basename(name) != name:
            raise CaseError(f"第 {n} 个素材的 file 要是 case.json 旁边的文件名：{name!r}")
        sources.append({"id": f"{n:02d}", "file": name, "path": os.path.join(here, name),
                        "sha256": source.get("sha256", "")})
    return {"id": case["id"], "target": float(target), "instruction": str(request.get("instruction") or ""),
            "format": request.get("format") or {}, "sources": sources}


def sha256(path: str) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as file:
        for chunk in iter(lambda: file.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def probe(path: str) -> dict:
    """The source's length, its picture's width and height as shown (rotation applied), and whether it has sound."""
    out = subprocess.run(["ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", path],  # noqa: S607  FFmpeg from PATH
                         capture_output=True, text=True, timeout=120, check=False)
    if out.returncode != 0:
        raise CaseError(f"读不了 {os.path.basename(path)}：{out.stderr.strip()[-200:]}")
    data = json.loads(out.stdout)
    video = next((s for s in data["streams"] if s["codec_type"] == "video"), None)
    if video is None:
        raise CaseError(f"{os.path.basename(path)} 没有画面")
    rotation = next((int(side.get("rotation", 0)) for side in video.get("side_data_list", []) if "rotation" in side),
                    int(video.get("tags", {}).get("rotate", 0)))
    width, height = video["width"], video["height"]
    if rotation % 180:
        width, height = height, width
    return {"duration": float(data["format"]["duration"]), "width": width, "height": height,
            "sound": any(s["codec_type"] == "audio" for s in data["streams"])}


def frame_size(width: int, height: int) -> tuple[int, int]:
    """eval.embed.render's size: the long side EDGE, the aspect kept."""
    if width <= EDGE and height <= EDGE:
        return width, height
    if width > height:
        return EDGE, max(1, int(EDGE / (width / height)))
    return max(1, int(EDGE * (width / height))), EDGE


def windows(duration: float) -> list[tuple[float, float, int]]:
    """(start, end, frame ms) of each window, its frame in the middle, as eval.video_moments.windows."""
    total, length, out, start = round(duration * 1000), WINDOW * 1000, [], 0
    while start < total:
        end = min(start + length, total)
        out.append((start / 1000, end / 1000, start + (end - start) // 2))
        start += length
    return out


def scene_cuts(log: str) -> list[float]:
    """The cut times in showinfo's log, flashes dropped."""
    times = []
    for line in log.splitlines():
        if "pts_time:" in line:
            t = float(line.split("pts_time:", 1)[1].split()[0])
            if t - (times[-1] if times else 0) >= SHOT_MIN:
                times.append(t)
    return times


def decode(path: str, meta: dict, folder: str) -> tuple[dict[int, str], list[float]]:
    """One pass over the source: each window's frame as a JPEG in folder, by frame ms, and the shot cuts.

    The picture is scaled first, so that the scene score and the frames come from the
    same small picture; the frames are taken WINDOW apart from the middle of the first window.
    """
    width, height = frame_size(meta["width"], meta["height"])
    planned = windows(meta["duration"])
    os.makedirs(folder, exist_ok=True)
    graph = (f"[0:v]scale={width}:{height}:flags=lanczos,split[s][f];"
             f"[s]select='gt(scene,{SCENE})',showinfo,nullsink;"
             f"[f]trim=start={WINDOW / 2:g},setpts=PTS-STARTPTS,fps=1/{WINDOW},format=rgb24[out]")
    size = width * height * 3
    frames = {}
    with tempfile.TemporaryFile("w+", encoding="utf-8") as log:
        process = subprocess.Popen(
            ["ffmpeg", "-nostdin", "-hide_banner", "-nostats", "-i", path, "-filter_complex", graph,  # noqa: S607  FFmpeg from PATH
             "-map", "[out]", "-f", "rawvideo", "-"], stdout=subprocess.PIPE, stderr=log)
        k = 0
        while chunk := process.stdout.read(size):
            if len(chunk) < size or k >= len(planned):
                break
            start, end, ms = planned[k]
            if end - start >= WINDOW:  # a short last window takes its own middle below
                frames[ms] = _save(np.frombuffer(chunk, np.uint8).reshape(height, width, 3), folder, ms)
            k += 1
        process.stdout.close()
        if process.wait(timeout=TIMEOUT) != 0:
            log.seek(0)
            raise CaseError(f"解码 {os.path.basename(path)} 失败：{log.read().strip()[-200:]}")
        log.seek(0)
        cuts = scene_cuts(log.read())
    for _start, _end, ms in planned:
        if ms not in frames:
            frames[ms] = _save(_seek(path, ms / 1000, meta["duration"], width, height), folder, ms)
    return frames, cuts


def _seek(path: str, at: float, duration: float, width: int, height: int) -> np.ndarray:
    # The last frame starts before the end; a seek to the very end finds none.
    at = min(at, max(duration - 0.1, 0))
    out = subprocess.run(["ffmpeg", "-nostdin", "-v", "error", "-ss", f"{at:.3f}", "-i", path, "-frames:v", "1",  # noqa: S607
                          "-vf", f"scale={width}:{height}:flags=lanczos,format=rgb24", "-f", "rawvideo", "-"],
                         capture_output=True, timeout=120, check=True).stdout
    if len(out) < width * height * 3:
        return np.zeros((height, width, 3), np.uint8)
    return np.frombuffer(out[:width * height * 3], np.uint8).reshape(height, width, 3)


def _save(rgb: np.ndarray, folder: str, ms: int) -> str:
    path = os.path.join(folder, f"{ms:08d}.jpg")
    Image.fromarray(rgb).save(path, format="JPEG", quality=QUALITY)
    return path


def is_flat(path: str) -> bool:
    with Image.open(path) as image:
        return float(np.asarray(image.convert("L"), dtype=np.float32).std()) < FLAT


def fill(index: moments.Index, segments: list[dict], vector: np.ndarray, target: float, shows: str) -> list[dict]:
    """The segments, with more while the cut is short of target: the unused cards whose
    best window scores highest, one from each group not yet shown, each segment at most
    CLIP_MAX around its best window and inside its card, over no flat window."""
    cards = {c["id"]: c for c in index.cards}
    scores = index.frames @ vector
    groups = {cards[s["card"]]["group"] for s in segments}
    left = target - sum(s["end"] - s["start"] for s in segments)
    out = list(segments)

    def best(card):
        return max(card["windows"], key=lambda i: scores[i])

    for card in sorted((c for c in index.cards if c["group"] not in groups), key=lambda c: -scores[best(c)]):
        if left < moments.SEGMENT_MIN or len(out) >= moments.SEGMENTS_MAX:
            break
        if card["group"] in groups:
            continue
        length = min(moments.CLIP_MAX, left, card["end"] - card["start"])
        if length < moments.SEGMENT_MIN:
            continue
        window = best(card)
        middle = (index.rows[window][1] + index.rows[window][2]) / 2
        start = min(max(card["start"], middle - length / 2), card["end"] - length)
        start, end = math.ceil(start * 100) / 100, math.floor((start + length) * 100) / 100
        if end - start < moments.SEGMENT_MIN or any(
                index.flat[i] and index.rows[i][1] < end and start < index.rows[i][2] for i in index._shot_windows(card)):
            continue
        out.append({"card": card["id"], "start": start, "end": end, "shows": shows,
                    "reason": f"规则：补足时长，分数 {float(scores[window]):.3f}"})
        groups.add(card["group"])
        left -= end - start
    return sorted(out, key=lambda s: (cards[s["card"]]["video"], s["start"]))


def cut(case: dict, out: str, args, run: timing.Run) -> dict:
    """Runs the steps of one case and returns what plan.json holds."""
    work = args.work
    with run.step("sources", "读取与解码") as step:
        for source in case["sources"]:
            if not os.path.exists(source["path"]):
                raise CaseError(f"找不到素材 {source['file']}")
            if source["sha256"] and sha256(source["path"]) != source["sha256"].lower():
                raise CaseError(f"{source['file']} 和 case.json 里的 sha256 不符")
            source.update(probe(source["path"]))
        step["videoSeconds"] = round(sum(s["duration"] for s in case["sources"]), 1)
    with run.step("decode", "读取与解码") as step:
        decoded = {s["id"]: decode(s["path"], s, os.path.join(work, "frames", s["id"])) for s in case["sources"]}
        step["frames"] = sum(len(f) for f, _c in decoded.values())
        step["shots"] = sum(len(c) + 1 for _f, c in decoded.values())

    from anas_ai import providers

    os.makedirs(args.weight_cache, exist_ok=True)
    with run.step("embed", "理解") as step:
        provider = providers.MediaPipeProvider(args.model, args.manifest, cache_dir=args.weight_cache)
        try:
            vectors = {}
            for video, (frames, _cuts) in decoded.items():
                for ms, path in frames.items():
                    with open(path, "rb") as file:
                        vectors[f"{video}/{ms}"] = np.asarray(provider.embed_image(file.read()), dtype=np.float32)
            query = np.asarray(provider.embed_query(case["instruction"] or "精彩的画面"), dtype=np.float32)
            step["frames"] = len(vectors)
        except BaseException:
            provider.close()
            raise
    with run.step("labels", "理解") as step:
        try:
            names, labels = _labels(provider, args.labels)
        finally:
            provider.close()
        step["labels"] = len(names)
    with run.step("cards", "切分") as step:
        index_data = {"model": provider.model, "layout": "2s1f", "scene": SCENE, "videos": {}}
        for source in case["sources"]:
            frames, cuts = decoded[source["id"]]
            bounds = [0.0, *cuts, source["duration"]]
            rows = []
            for start, end, ms in windows(source["duration"]):
                nearest = np.argsort(-(labels @ vectors[f"{source['id']}/{ms}"]))[:TAGS] if len(names) else []
                rows.append({"start": start, "end": end, "frame": ms, "flat": is_flat(frames[ms]),
                             "tags": [names[i] for i in nearest]})
            index_data["videos"][source["id"]] = {"file": source["file"], "duration": source["duration"],
                                                  "shots": [list(pair) for pair in itertools.pairwise(bounds)],
                                                  "windows": rows}
        index = moments.Index(index_data, vectors)
        step["cards"] = len(index.cards)
    shows = case["instruction"] or "精彩的画面"
    with run.step("plan", "选择与编排") as step:
        segments = index.plan({shows: query}, case["target"])
        step["segments"] = len(segments)
    with run.step("fill", "选择与编排") as step:
        segments = fill(index, segments, query, case["target"], shows)
        step["segments"] = len(segments)
    with run.step("render", "渲染") as step:
        clips = index.check(segments)
        files = {s["id"]: s["path"] for s in case["sources"]}
        made = render.render_clips([(files[v], start, end) for v, start, end in clips], work)
        final = os.path.join(out, f"{PROVIDER}-{case['id']}.mp4")
        # The work folder is in the container and /out a mount: a rename cannot cross them.
        shutil.move(made, final)
        step["cutSeconds"] = round(sum(e - s for _v, s, e in clips), 2)
    return {"case": case["id"], "instruction": case["instruction"], "target": case["target"],
            "seconds": round(sum(e - s for _v, s, e in clips), 2), "planner": "rules",
            "videos": {s["id"]: s["file"] for s in case["sources"]}, "segments": segments,
            "cut": os.path.basename(final)}


def _labels(provider, path: str) -> tuple[list[str], np.ndarray]:
    """The photo labels' names and vectors (a label is the mean of its name and synonyms), or none without the file."""
    if not path or not os.path.exists(path):
        return [], np.zeros((0, 1), np.float32)
    with open(path, encoding="utf-8") as file:
        vocabulary = json.load(file)["labels"]
    rows = []
    for label in vocabulary:
        mean = np.mean([provider.embed_query(w) for w in [label["name"], *label.get("synonyms", [])]], axis=0)
        rows.append(mean / np.linalg.norm(mean))
    return [label["name"] for label in vocabulary], np.stack(rows).astype(np.float32)


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("case", help="case-XX.json, its sources beside it")
    parser.add_argument("out", help="the folder the cut, plan.json and pipeline.json go to")
    parser.add_argument("--model", default=os.environ.get("ANAS_MONTAGE_EMBEDDER", EMBEDDER))
    parser.add_argument("--manifest", default=MANIFEST)
    parser.add_argument("--labels", default=LABELS, help="the photo labels; empty for none")
    parser.add_argument("--weight-cache", default=os.environ.get("ANAS_MONTAGE_WEIGHT_CACHE"),
                        help="where MediaPipe keeps repacked weights; a folder in the work folder by default")
    parser.add_argument("--keep-work", action="store_true", help="keep the frames and other work files")
    args = parser.parse_args(argv)
    run = timing.Run()
    os.makedirs(args.out, exist_ok=True)
    args.work = tempfile.mkdtemp(prefix="montage-")
    args.weight_cache = args.weight_cache or os.path.join(args.work, "xnnpack")
    about = {"case": None, "provider": PROVIDER, "result": "失败"}
    try:
        case = read_case(args.case)
        about["case"] = case["id"]
        plan = cut(case, args.out, args, run)
        with open(os.path.join(args.out, "plan.json"), "w", encoding="utf-8") as file:
            json.dump(plan, file, ensure_ascii=False, indent=2)
        about |= {"result": "成功", "cut": plan["cut"], "cutSeconds": plan["seconds"], "target": case["target"],
                  "segments": len(plan["segments"]), "planner": plan["planner"]}
        return 0
    except (CaseError, moments.PlanError, render.RenderError, OSError, ValueError,
            subprocess.SubprocessError) as error:
        about["reason"] = f"{type(error).__name__}: {error}"
        print(f"出片失败：{error}", file=sys.stderr)
        return 1
    finally:
        summary = run.write(os.path.join(args.out, "pipeline.json"), about)
        print(json.dumps({"result": about["result"], **timing.report(summary)}, ensure_ascii=False), flush=True)
        if not args.keep_work:
            shutil.rmtree(args.work, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
