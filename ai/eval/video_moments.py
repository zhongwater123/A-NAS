"""Finds the seconds of a video a Chinese query means, window by window.

Development only (docs/research/video-moment-retrieval.md, issue #287). The
baseline is Google AI Edge Gallery's Video Moments Finder: cut a video into
windows, sample a few frames in each, give each window a vector and rank the
windows by cosine to the query. MediaPipe's Python and C API (1.1.0) embed
one image at a time and have no counterpart of the Android embedContent that
encodes a window's frames together, so every frame gets its own vector and a
window is scored by their normalised mean or by their best frame (MaxSim).

Three steps, each resuming from its cache under $ANAS_DATASETS/cache/video-moments:

    python -m eval.video_moments frames      # FFmpeg; frames as thumbnail/v1
    python -m eval.video_moments embed --model /models/embeddinggemma-2-740m.litertlm
    python -m eval.video_moments evaluate    # NumPy only

A fourth cuts the baseline clip for one query out of the same vectors: the
best 2 s windows, merged like Gallery's results, in order of score until the
clip is long enough, with no editing plan at all.

    python -m eval.video_moments montage --model ... --query 新加坡的美食 --seconds 20

The videos are listed with their SHA-256 in video_moments.json and read from
$ANAS_DATASETS/videos. Another set (--set, before the step) keeps its own
cache, named after its file: video_moments_edinburgh.json caches under
video-moments-edinburgh; a set without queries only takes the frames and
embed steps, and its "layouts" limits the frames it samples. Frames are
decoded with an accurate seek, where Gallery takes the nearest key frame,
and rendered like the photo service's AI input (eval.embed.render: 512 px
long edge, JPEG 82).
"""

import argparse
import hashlib
import io
import itertools
import json
import os
import resource
import subprocess
import time

import numpy as np
from PIL import Image

from eval import datasets
from eval.embed import MANIFEST, Cache, render

SET = os.path.join(os.path.dirname(os.path.abspath(__file__)), "video_moments.json")
VIDEOS = os.path.join(datasets.ROOT, "videos")
CACHE = os.path.join(datasets.ROOT, "cache", "video-moments")
EDGE = 512

# name: (window seconds, frames per window). Gallery's presets are 2 s with 2
# frames (objects) and 4 s with 4 (actions); 2 s with 1 is a frame per window.
LAYOUTS = {"2s2f": (2, 2), "4s4f": (4, 4), "2s1f": (2, 1)}
METHODS = ("mean", "maxsim")
TOP_K = 5
# Gallery merges results whose gap is at most this into one time range.
MERGE_MS = 500
# A frame this close to the previous kept frame (mean absolute difference of
# 32 x 18 grey thumbnails, 0-255) reuses its vector in the skip simulation,
# run on the layout the pilot recommends.
STATIC_THRESHOLDS = (2, 4, 8, 12)
STATIC_LAYOUT = "2s1f"


def use_set(path):
    """Points SET and CACHE at another set of videos."""
    global SET, CACHE
    SET = os.path.abspath(path)
    name = os.path.splitext(os.path.basename(SET))[0].replace("_", "-")
    CACHE = os.path.join(datasets.ROOT, "cache", name)


def load_set():
    with open(SET, encoding="utf-8") as file:
        return json.load(file)


def set_layouts(data):
    return {name: LAYOUTS[name] for name in data.get("layouts", LAYOUTS)}


def windows(duration_ms, seconds, frames):
    """Gallery's windows: (start, end, frame times) in ms, no overlap.

    One frame sits in the middle; more are spread from the window's start to
    its end, both included, so neighbouring windows share a frame time.
    """
    out, start, length = [], 0, seconds * 1000
    while start < duration_ms:
        end = min(start + length, duration_ms)
        if frames == 1:
            times = [start + (end - start) // 2]
        else:
            times = [start + i * (end - start) // (frames - 1) for i in range(frames)]
        out.append((start, end, [min(max(t, 0), duration_ms) for t in times]))
        start += length
    return out


def cover_ms(duration_ms):
    """The album's poster time (internal/photos/video.go posterTime)."""
    return int(min(duration_ms / 10, 1000))


def frame_times(duration_ms, layouts=LAYOUTS):
    times = {cover_ms(duration_ms)}
    for seconds, frames in layouts.values():
        for _start, _end, sampled in windows(duration_ms, seconds, frames):
            times.update(sampled)
    return sorted(times)


def frame_path(video, ms):
    return os.path.join(CACHE, "frames", video, f"{ms:07d}.jpg")


def decode(path, ms, duration_ms):
    # The last frame starts before the end; a seek to the very end finds none.
    at = min(ms, max(duration_ms - 100, 0)) / 1000
    result = subprocess.run(
        ["ffmpeg", "-v", "error", "-ss", f"{at:.3f}", "-i", path, "-frames:v", "1",  # noqa: S607  FFmpeg from PATH on a development machine
         "-f", "image2pipe", "-vcodec", "png", "-"],
        capture_output=True, check=True)
    with Image.open(io.BytesIO(result.stdout)) as image:
        return render(image, EDGE)


def sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as file:
        for chunk in iter(lambda: file.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def extract_frames():
    data = load_set()
    layouts = set_layouts(data)
    timings = []
    for video, meta in sorted(data["videos"].items()):
        path = os.path.join(VIDEOS, meta["file"])
        if sha256(path) != meta["sha256"]:
            raise SystemExit(f"{path} does not match the SHA-256 in {SET}")
        duration_ms = round(meta["duration"] * 1000)
        todo = [ms for ms in frame_times(duration_ms, layouts) if not os.path.exists(frame_path(video, ms))]
        os.makedirs(os.path.dirname(frame_path(video, 0)), exist_ok=True)
        for ms in todo:
            started = time.perf_counter()
            data_bytes = decode(path, ms, duration_ms)
            timings.append(time.perf_counter() - started)
            partial = frame_path(video, ms) + ".partial"
            with open(partial, "wb") as file:
                file.write(data_bytes)
            os.replace(partial, frame_path(video, ms))
        print(f"{video}: {len(frame_times(duration_ms, layouts))} frames, {len(todo)} decoded", flush=True)
    if timings:
        print(f"decode: median {np.median(timings) * 1000:.0f} ms per frame", flush=True)


def embed(args):
    from anas_ai import providers

    started = time.monotonic()
    os.makedirs(args.weight_cache, exist_ok=True)
    provider = providers.MediaPipeProvider(args.model, MANIFEST, vision_tokens=args.tokens, cache_dir=args.weight_cache)
    print(f"model {provider.model} loaded in {time.monotonic() - started:.1f} s", flush=True)
    directory = os.path.join(CACHE, provider.model)
    os.makedirs(directory, exist_ok=True)
    data = load_set()
    layouts = set_layouts(data)

    queries = Cache(os.path.join(directory, "queries.npz"))
    todo = [q["text"] for q in data["queries"] if q["text"] not in queries.vectors]
    for text in todo:
        queries.vectors[text] = np.asarray(provider.embed_query(text), dtype=np.float32)
    if todo:
        queries.save()

    frames = Cache(os.path.join(directory, "frames.npz"))
    wall, cpu = [], []
    for video, meta in sorted(data["videos"].items()):
        duration_ms = round(meta["duration"] * 1000)
        todo = [ms for ms in frame_times(duration_ms, layouts) if f"{video}/{ms}" not in frames.vectors]
        for ms in todo:
            with open(frame_path(video, ms), "rb") as file:
                image = file.read()
            started, started_cpu = time.perf_counter(), time.process_time()
            vector = provider.embed_image(image)
            wall.append(time.perf_counter() - started)
            cpu.append(time.process_time() - started_cpu)
            frames.vectors[f"{video}/{ms}"] = np.asarray(vector, dtype=np.float32)
        frames.save()
        print(f"{video}: {len(todo)} frames embedded", flush=True)
    provider.close()
    peak = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1024
    if wall:
        cost = {"model": provider.model, "frames": len(wall), "cores": len(os.sched_getaffinity(0)),
                "wallMsMedian": float(np.median(wall) * 1000), "wallMsP90": float(np.percentile(wall, 90) * 1000),
                "cpuMsMedian": float(np.median(cpu) * 1000), "peakRssMiB": peak}
        with open(os.path.join(directory, "cost.json"), "w", encoding="utf-8") as file:
            json.dump(cost, file, indent=2)
        print(json.dumps(cost), flush=True)


# Evaluation


def unit(vectors):
    return vectors / np.linalg.norm(vectors, axis=-1, keepdims=True)


class Index:
    """Every window of every video under one layout, with its frame vectors."""

    def __init__(self, data, frames, layout, keep=None):
        seconds, count = LAYOUTS[layout]
        self.layout = layout
        self.interval_ms = seconds * 1000 / (count - 1) if count > 1 else seconds * 1000
        self.rows = []  # (video, start, end)
        stacks = []
        for video, meta in sorted(data["videos"].items()):
            for start, end, times in windows(round(meta["duration"] * 1000), seconds, count):
                self.rows.append((video, start, end))
                stacks.append([frames[f"{video}/{(keep or {}).get((video, ms), ms)}"] for ms in times])
        self.frames = np.stack(stacks)  # windows x frames x dims

    def scores(self, query, method):
        per_frame = self.frames @ query
        if method == "maxsim":
            return per_frame.max(axis=1)
        return unit(self.frames.mean(axis=1)) @ query


def overlap(a_start, a_end, b_start, b_end):
    return max(0, min(a_end, b_end) - max(a_start, b_start))


def merge(rows):
    """Gallery's merge of the top windows into time ranges, per video."""
    merged = []
    for video, start, end in sorted(rows):
        last = merged[-1] if merged else None
        if last and last[0] == video and start <= last[2] + MERGE_MS:
            merged[-1] = (video, last[1], max(last[2], end))
        else:
            merged.append((video, start, end))
    return merged


def judge(query, ranked, index):
    """Hits of one query given window indices ranked best first.

    A time point counts when a top window's centre falls in a labelled moment
    widened by Δ on both sides, Δ being 2 s or twice the frame interval,
    whichever is larger (#207), and strictly when it falls in the moment itself;
    a range counts by the tIoU of the merged top windows with a labelled moment.
    """
    moments = [(video, start * 1000, end * 1000) for video, start, end in query["moments"]]
    if not moments:
        return {}
    delta = max(2000, 2 * index.interval_ms)
    top = [index.rows[i] for i in ranked[:TOP_K]]

    def event(row, slack):
        video, start, end = row
        centre = (start + end) / 2
        for number, (m_video, m_start, m_end) in enumerate(moments):
            if m_video == video and m_start - slack <= centre <= m_end + slack:
                return number
        return None

    events = [event(row, delta) for row in top]
    strict = [event(row, 0) for row in top]
    hit_events = [e for e in events if e is not None]
    tiou = 0.0
    for video, start, end in merge(top):
        for m_video, m_start, m_end in moments:
            if m_video == video:
                inter = overlap(start, end, m_start, m_end)
                tiou = max(tiou, inter / (max(end, m_end) - min(start, m_start)))
    videos = {video for video, _start, _end in moments}
    return {
        "video@1": top[0][0] in videos,
        "point@1": events[0] is not None,
        "point@5": bool(hit_events),
        "strict@1": strict[0] is not None,
        "strict@5": any(e is not None for e in strict),
        "tiou0.5@5": tiou >= 0.5,
        "tiou0.3@5": tiou >= 0.3,
        "repeats": len(hit_events) - len(set(hit_events)),
        "hits": len(hit_events),
    }


def summarise(rows):
    out = {"n": len(rows)}
    if rows:
        for key in ("video@1", "point@1", "point@5", "strict@1", "strict@5", "tiou0.5@5", "tiou0.3@5"):
            out[key] = sum(row[key] for row in rows) / len(rows)
        hits = sum(row["hits"] for row in rows)
        out["repeatShare"] = sum(row["repeats"] for row in rows) / hits if hits else 0.0
    return out


def run(data, queries, index, method):
    results = []
    for query in data["queries"]:
        scores = index.scores(queries[query["text"]], method)
        ranked = np.argsort(-scores, kind="stable")
        row = judge(query, ranked, index)
        row.update(id=query["id"], category=query["category"], top=float(scores[ranked[0]]),
                   best=[[*index.rows[i], round(float(scores[i]), 4)] for i in ranked[:TOP_K]])
        results.append(row)
    return results


def report(results):
    answerable = [r for r in results if r["category"] != "nomatch"]
    out = {"all": summarise(answerable)}
    for category in ("regular", "short", "similar", "order"):
        out[category] = summarise([r for r in answerable if r["category"] == category])
    nomatch = sorted(r["top"] for r in results if r["category"] == "nomatch")
    # The lowest threshold that turns away at least 80 % of the queries with no
    # answer, on this same set: an in-sample bound, not a calibrated value.
    if nomatch:
        threshold = nomatch[int(np.ceil(0.8 * len(nomatch))) - 1] + 1e-6
        kept = [dict(r, **{"point@5": r["point@5"] and r["top"] >= threshold}) for r in answerable]
        out["reject"] = {"threshold": threshold, "nomatchTop1": nomatch,
                         "answerableTop1Min": min(r["top"] for r in answerable),
                         "point@5WithThreshold": summarise(kept)["point@5"]}
    return out


def by_duration(results, data):
    """point@5 by the length of the query's longest moment, for short events a mean may drown."""
    buckets = {"<=2s": [], "2-5s": [], "5-15s": [], ">15s": []}
    lengths = {q["id"]: max(e - s for _v, s, e in q["moments"]) for q in data["queries"] if q["moments"]}
    for r in results:
        if r["id"] in lengths:
            length = lengths[r["id"]]
            key = "<=2s" if length <= 2 else "2-5s" if length <= 5 else "5-15s" if length <= 15 else ">15s"
            buckets[key].append(r["point@5"])
    return {key: {"n": len(values), "point@5": sum(values) / len(values) if values else None}
            for key, values in buckets.items()}


def grey(path):
    with Image.open(path) as image:
        return np.asarray(image.convert("L").resize((32, 18)), dtype=np.float32)


def static_keep(data, threshold, layout):
    """Maps each frame of the layout to the earlier frame whose vector it would reuse."""
    keep, skipped, total = {}, 0, 0
    for video, meta in sorted(data["videos"].items()):
        last_ms, last = None, None
        sampled = windows(round(meta["duration"] * 1000), *LAYOUTS[layout])
        for ms in sorted({t for _start, _end, times in sampled for t in times}):
            current = grey(frame_path(video, ms))
            total += 1
            if last is not None and np.abs(current - last).mean() < threshold:
                keep[(video, ms)] = last_ms
                skipped += 1
            else:
                last_ms, last = ms, current
    return keep, skipped / total


def cover_hits(data, frames, queries):
    videos = sorted(data["videos"])
    covers = np.stack([frames[f"{v}/{cover_ms(round(data['videos'][v]['duration'] * 1000))}"] for v in videos])
    hits = []
    for query in data["queries"]:
        if query["moments"]:
            best = videos[int(np.argmax(covers @ queries[query["text"]]))]
            hits.append(best in {v for v, _s, _e in query["moments"]})
    return sum(hits) / len(hits)


def evaluate(args):
    data = load_set()
    directory = os.path.join(CACHE, args.cache)
    frames = Cache(os.path.join(directory, "frames.npz")).vectors
    queries = Cache(os.path.join(directory, "queries.npz")).vectors
    out = {"cache": args.cache, "coverVideo@1": cover_hits(data, frames, queries), "runs": {}}
    per_query = {}
    for layout in LAYOUTS:
        index = Index(data, frames, layout)
        for method in METHODS if LAYOUTS[layout][1] > 1 else ("mean",):
            name = f"{layout}-{method}" if LAYOUTS[layout][1] > 1 else layout
            results = run(data, queries, index, method)
            out["runs"][name] = dict(report(results), duration=by_duration(results, data), windows=len(index.rows))
            per_query[name] = results
    out["static"] = {}
    for threshold in STATIC_THRESHOLDS:
        keep, share = static_keep(data, threshold, STATIC_LAYOUT)
        index = Index(data, frames, STATIC_LAYOUT, keep)
        results = run(data, queries, index, "mean")
        answerable = [r for r in results if r["category"] != "nomatch"]
        out["static"][str(threshold)] = {"skippedFrames": share, **summarise(answerable)}
    os.makedirs(directory, exist_ok=True)
    with open(os.path.join(directory, "report.json"), "w", encoding="utf-8") as file:
        json.dump(out, file, ensure_ascii=False, indent=2)
    with open(os.path.join(directory, "queries.json"), "w", encoding="utf-8") as file:
        json.dump(per_query, file, ensure_ascii=False, indent=1)
    print_tables(out)


def print_tables(out):
    print(f"cover frame only: video@1 {out['coverVideo@1']:.2f}")
    keys = ("video@1", "point@1", "point@5", "strict@1", "strict@5", "tiou0.5@5", "tiou0.3@5", "repeatShare")
    print("| run | category | n | " + " | ".join(keys) + " |")
    for name, run_out in out["runs"].items():
        for category in ("all", "regular", "short", "similar", "order"):
            row = run_out[category]
            if row["n"]:
                print(f"| {name} | {category} | {row['n']} | " + " | ".join(f"{row[k]:.2f}" for k in keys) + " |")
    for name, run_out in out["runs"].items():
        reject = run_out["reject"]
        print(f"{name}: threshold {reject['threshold']:.4f}, answerable point@5 with it "
              f"{reject['point@5WithThreshold']:.2f} (without {run_out['all']['point@5']:.2f}); "
              f"by length {json.dumps(run_out['duration'])}")
    for threshold, row in out["static"].items():
        print(f"static skip < {threshold}: {row['skippedFrames']:.0%} of frames reuse a vector, "
              f"point@5 {row['point@5']:.2f}, tiou0.5@5 {row['tiou0.5@5']:.2f}")


MONTAGE_LAYOUT = "2s1f"
# Every part is brought to one size and rate before joining; the test videos
# without sound decide -an.
MONTAGE_FILTER = ("scale=1280:720:force_original_aspect_ratio=decrease,"
                  "pad=1280:720:(ow-iw)/2:(oh-ih)/2,fps=30,format=yuv420p")


def montage(args):
    from anas_ai import providers

    os.makedirs(args.weight_cache, exist_ok=True)
    provider = providers.MediaPipeProvider(args.model, MANIFEST, cache_dir=args.weight_cache)
    query = np.asarray(provider.embed_query(args.query), dtype=np.float32)
    provider.close()
    data = load_set()
    frames = Cache(os.path.join(CACHE, provider.model, "frames.npz")).vectors
    index = Index(data, frames, MONTAGE_LAYOUT)
    scores = index.scores(query, "mean")

    target, picked, segments = args.seconds * 1000, [], []
    for i in np.argsort(-scores, kind="stable"):
        if scores[i] < args.min_score:
            break
        picked.append(index.rows[i])
        segments = merge(picked)
        if sum(end - start for _video, start, end in segments) >= target:
            break
    best = {}
    for i, row in enumerate(index.rows):
        for segment in segments:
            if row[0] == segment[0] and segment[1] <= row[1] and row[2] <= segment[2]:
                best[segment] = max(best.get(segment, -1.0), float(scores[i]))
    segments.sort(key=lambda segment: -best[segment])

    directory = os.path.join(CACHE, "montage", args.name)
    os.makedirs(directory, exist_ok=True)
    parts, left = [], target
    for number, (video, start, end) in enumerate(segments):
        if left <= 0:
            break
        length = min(end - start, left)
        left -= length
        part = os.path.join(directory, f"part{number:02d}.mp4")
        subprocess.run(
            ["ffmpeg", "-v", "error", "-y", "-ss", f"{start / 1000:.3f}",  # noqa: S607  FFmpeg from PATH
             "-i", os.path.join(VIDEOS, data["videos"][video]["file"]), "-t", f"{length / 1000:.3f}",
             "-an", "-vf", MONTAGE_FILTER, "-c:v", "libx264", "-preset", "veryfast", "-crf", "20", part],
            check=True)
        parts.append({"video": video, "start": start / 1000, "end": (start + length) / 1000,
                      "score": round(best[(video, start, end)], 4), "file": os.path.basename(part)})
    # The concat filter, not the concat demuxer with -c copy: parts cut from
    # 720p and 1080p sources carry different H.264 parameter sets, and a copy
    # keeps only the first part's, so later parts decode wrongly.
    output = os.path.join(directory, f"{args.name}.mp4")
    inputs = [arg for part in parts for arg in ("-i", os.path.join(directory, part["file"]))]
    joined = "".join(f"[{i}:v]" for i in range(len(parts))) + f"concat=n={len(parts)}:v=1:a=0"
    subprocess.run(["ffmpeg", "-v", "error", "-y", *inputs, "-filter_complex", joined,  # noqa: S607  FFmpeg from PATH
                    "-c:v", "libx264", "-preset", "veryfast", "-crf", "20", output], check=True)
    with open(os.path.join(directory, f"{args.name}.json"), "w", encoding="utf-8") as file:
        json.dump({"query": args.query, "seconds": args.seconds, "model": provider.model, "parts": parts},
                  file, ensure_ascii=False, indent=2)
    print(output)
    for part in parts:
        print(f"  {part['video']} {part['start']:.1f}-{part['end']:.1f} s  score {part['score']}")


# The index for the montage tools (montage.moments): shots, flat frames and labels.
# A cut is a frame whose scene score (FFmpeg's select filter) exceeds SCENE;
# cuts closer than SHOT_MIN to the last are flashes and dropped. A frame whose
# grey levels spread less than FLAT is black or one colour.
SCENE = 0.25
SHOT_MIN = 1.0
FLAT = 8.0
TAGS_PER_WINDOW = 3
LABELS = os.path.join(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))),
                      "internal", "photos", "labels", "v1.json")


def cuts(path):
    found = subprocess.run(
        ["ffmpeg", "-hide_banner", "-nostats", "-i", path, "-an", "-vf", f"select='gt(scene,{SCENE})',showinfo",  # noqa: S607  FFmpeg from PATH
         "-f", "null", "-"], capture_output=True, text=True, check=True).stderr
    times = []
    for line in found.splitlines():
        if "pts_time:" in line:
            t = float(line.split("pts_time:", 1)[1].split()[0])
            if t - (times[-1] if times else 0) >= SHOT_MIN:
                times.append(t)
    return times


def build_index(args):
    from anas_ai import providers

    data = load_set()
    os.makedirs(args.weight_cache, exist_ok=True)
    provider = providers.MediaPipeProvider(args.model, MANIFEST, cache_dir=args.weight_cache)
    with open(LABELS, encoding="utf-8") as file:
        vocabulary = json.load(file)["labels"]
    rows = []
    for label in vocabulary:
        mean = np.mean([provider.embed_query(word) for word in [label["name"], *label.get("synonyms", [])]], axis=0)
        rows.append(mean / np.linalg.norm(mean))
    labels = np.stack(rows).astype(np.float32)
    provider.close()
    directory = os.path.join(CACHE, provider.model)
    frames = Cache(os.path.join(directory, "frames.npz")).vectors
    out = {"model": provider.model, "layout": MONTAGE_LAYOUT, "scene": SCENE, "videos": {}}
    for video, meta in sorted(data["videos"].items()):
        duration = meta["duration"]
        bounds = [0.0, *cuts(os.path.join(VIDEOS, meta["file"])), duration]
        windows_out = []
        for start, end, (frame,) in windows(round(duration * 1000), *LAYOUTS[MONTAGE_LAYOUT]):
            with Image.open(frame_path(video, frame)) as image:
                flat = float(np.asarray(image.convert("L"), dtype=np.float32).std()) < FLAT
            nearest = np.argsort(-(labels @ frames[f"{video}/{frame}"]))[:TAGS_PER_WINDOW]
            windows_out.append({"start": start / 1000, "end": end / 1000, "frame": frame, "flat": flat,
                                "tags": [vocabulary[i]["name"] for i in nearest]})
        out["videos"][video] = {"file": meta["file"], "duration": duration,
                                "shots": [list(pair) for pair in itertools.pairwise(bounds)], "windows": windows_out}
        print(f"{video}: {len(bounds) - 1} shots, {sum(w['flat'] for w in windows_out)} flat windows", flush=True)
    with open(os.path.join(directory, "index.json"), "w", encoding="utf-8") as file:
        json.dump(out, file, ensure_ascii=False)


def describe(args):
    """One sentence from the local vision model on a few frames of each card (montage.look),
    written to captions.json beside index.json; cards already described from the same
    frames are kept."""
    from montage import look, moments

    index = moments.Index.load(os.path.join(args.cache, "index.json"), os.path.join(args.cache, "frames.npz"))
    path = os.path.join(args.cache, moments.CAPTIONS)
    said = {}
    if os.path.exists(path):
        with open(path, encoding="utf-8") as file:
            said = json.load(file)["cards"]
    vision = look.Vision(args.vision)
    def chosen(card):
        return [index.frame_ms[i] for i in index.keyframes(card, look.KEYFRAMES)]

    def kept(card):
        return [said.get(card["id"], {}).get(k) for k in ("start", "end", "frames")] == [card["start"], card["end"], chosen(card)]

    todo = [c for c in index.cards if not kept(c) or not all(k in said[c["id"]] for k in ("light", "highlight"))]
    began = time.monotonic()
    for n, card in enumerate(todo, 1):
        frames = chosen(card)
        jpegs = []
        for frame in frames:
            with open(frame_path(card["video"], frame), "rb") as file:
                jpegs.append(file.read())
        caption = said[card["id"]]["caption"] if kept(card) else vision.describe(jpegs)
        said[card["id"]] = {"start": card["start"], "end": card["end"], "frames": frames, "caption": caption,
                            "light": vision.light(jpegs), "highlight": vision.highlight(jpegs)}
        if n % 20 == 0 or n == len(todo):
            with open(path, "w", encoding="utf-8") as file:
                json.dump({"vision": args.name, "cards": said}, file, ensure_ascii=False, indent=1)
            print(f"{n}/{len(todo)} {(time.monotonic() - began) / n:.2f} s each  {card['id']}: {caption}", flush=True)


def measure(args):
    """How the camera moves in each card and how its picture looks (montage.picture), written to
    pictures.json beside index.json; cards measured over the same seconds are kept."""
    from concurrent.futures import ProcessPoolExecutor

    from montage import moments, picture

    index = moments.Index.load(os.path.join(args.cache, "index.json"), os.path.join(args.cache, "frames.npz"))
    path = os.path.join(args.cache, moments.PICTURES)
    measured = {}
    if os.path.exists(path):
        with open(path, encoding="utf-8") as file:
            measured = json.load(file)["cards"]
    todo = [c for c in index.cards
            if [measured.get(c["id"], {}).get(k) for k in ("start", "end")] != [c["start"], c["end"]]]
    files = {video: os.path.join(VIDEOS, meta["file"]) for video, meta in index.videos.items()}
    shapes = {video: picture.size(file) for video, file in files.items()}
    began = time.monotonic()
    with ProcessPoolExecutor(args.workers) as pool:
        jobs = [(c, pool.submit(_measure, files[c["video"]], c["start"], c["end"], shapes[c["video"]])) for c in todo]
        for n, (card, job) in enumerate(jobs, 1):
            measured[card["id"]] = {"start": card["start"], "end": card["end"], **job.result()}
            if n % 50 == 0 or n == len(jobs):
                with open(path, "w", encoding="utf-8") as file:
                    json.dump({"step": picture.STEP, "cards": measured}, file, ensure_ascii=False, indent=1)
                print(f"{n}/{len(jobs)} {(time.monotonic() - began) / n:.2f} s each", flush=True)


def _measure(video, start, end, shape):
    from montage import picture

    return picture.measure(picture.frames(video, start, end, shape))


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--set", default=SET, help="the videos and queries, with a cache of its own")
    steps = parser.add_subparsers(dest="step", required=True)
    steps.add_parser("frames", help="decode and render every frame the layouts sample")
    step = steps.add_parser("embed", help="embed the frames and the queries")
    step.add_argument("--model", required=True)
    step.add_argument("--tokens", type=int, default=70)
    step.add_argument("--weight-cache", default=os.path.join(datasets.ROOT, "cache", "xnnpack"))
    step = steps.add_parser("index", help="find the shots, flat frames and labels the montage tools read")
    step.add_argument("--model", required=True)
    step.add_argument("--weight-cache", default=os.path.join(datasets.ROOT, "cache", "xnnpack"))
    step = steps.add_parser("describe", help="caption each montage card's key frame with a local vision model")
    step.add_argument("--cache", required=True, help="the model's directory under the cache, with index.json")
    step.add_argument("--vision", default="http://127.0.0.1:8090/v1/chat/completions",
                      help="the local vision model's chat completions URL (llama-server)")
    step.add_argument("--name", default="Qwen3-VL-2B-Instruct Q8_0", help="the vision model, as recorded")
    step = steps.add_parser("measure", help="measure camera motion, sharpness and exposure of each montage card")
    step.add_argument("--cache", required=True, help="the model's directory under the cache, with index.json")
    step.add_argument("--workers", type=int, default=4)
    step = steps.add_parser("evaluate", help="score the layouts and methods")
    step.add_argument("--cache", required=True, help="the model's directory under the cache, as embed printed it")
    step = steps.add_parser("montage", help="cut the baseline clip for one query")
    step.add_argument("--model", required=True)
    step.add_argument("--weight-cache", default=os.path.join(datasets.ROOT, "cache", "xnnpack"))
    step.add_argument("--query", required=True)
    step.add_argument("--seconds", type=int, default=20)
    step.add_argument("--min-score", type=float, default=0.0, help="leave out windows scoring below this")
    step.add_argument("--name", required=True, help="the clip's folder and file name")
    args = parser.parse_args()
    use_set(args.set)
    if args.step == "frames":
        extract_frames()
    elif args.step == "embed":
        embed(args)
    elif args.step == "montage":
        montage(args)
    elif args.step == "index":
        build_index(args)
    elif args.step == "describe":
        describe(args)
    elif args.step == "measure":
        measure(args)
    else:
        evaluate(args)


if __name__ == "__main__":
    main()
