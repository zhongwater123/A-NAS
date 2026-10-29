"""Montages for #322 on the pilot set: the baseline, rules alone and a model's plan.

The instructions, their hand-given topic and condition and the seconds that fit
each are in video_montage.json. Every version is scored the same way from its
segments (video, start, end) against the pilot's shot index (video_moments
index):

  onTopic     seconds inside the instruction's onTopic, over the cut's length
  crossShot   seconds a segment shows of a shot it touches for under a second: the
              leftover of the shot before a cut, or a flash of the next
  flat        seconds in black or one-colour windows
  shotShare   the largest share of the cut from one shot
  reversals   times a segment starts before the one played before it from the same video
  length      the cut over the target

Steps:

  rules   (needs the embedding model)  embeds the probes, plans with
          montage.moments rules alone and renders each cut; also keeps the
          instructions' vectors for the random draw.
  score   reads the baseline (video_moments montage), the rule plans, the model's
          plans (anas_mcp saves one beside each cut; copy it to
          <cache>/montage-model/<id>.json) and draws random plans from the cards a
          search for the instruction returns, then prints the table and writes
          <cache>/montage-scores.json.
"""

import argparse
import json
import os
import random

import numpy as np

from eval import datasets, video_moments as vm
from eval.embed import MANIFEST
from montage import moments, render

INSTRUCTIONS = os.path.join(os.path.dirname(os.path.abspath(__file__)), "video_montage.json")
DRAWS = 200
SEED = 322
# Each random segment is as long as a still one the rules would take.
RANDOM_PIECE = 4.0
METRICS = ("onTopic", "crossShot", "flat", "shotShare", "reversals", "length")


def load_instructions(path):
    with open(path, encoding="utf-8") as file:
        return json.load(file)["instructions"]


def load_index(directory):
    return moments.Index.load(os.path.join(directory, "index.json"), os.path.join(directory, "frames.npz"))


def overlap(a, b, c, d):
    return max(0.0, min(b, d) - max(a, c))


def measure(index, clips, instruction):
    total = sum(end - start for _video, start, end in clips)
    if not total:
        return {"seconds": 0.0, **dict.fromkeys(METRICS)}
    on = sum(overlap(start, end, a, b) for video, start, end in clips
             for v, a, b in instruction["onTopic"] if v == video)
    cross, flat, by_shot, reversals, last = 0.0, 0.0, {}, 0, {}
    for video, start, end in clips:
        for n, (a, b) in enumerate(index.videos[video]["shots"]):
            seen = overlap(start, end, a, b)
            cross += seen if 0 < seen < 1 else 0
            if seen:
                by_shot[(video, n)] = by_shot.get((video, n), 0.0) + seen
        flat += sum(overlap(start, end, s, e) for i, (v, s, e) in enumerate(index.rows) if v == video and index.flat[i])
        reversals += video in last and start < last[video]
        last[video] = start
    return {"seconds": round(total, 2), "onTopic": round(on / total, 3), "crossShot": round(cross, 2),
            "flat": round(flat, 2), "shotShare": round(max(by_shot.values()) / total, 3),
            "reversals": reversals, "length": round(total / instruction["seconds"], 2)}


def clips_of(index, segments):
    cards = {c["id"]: c for c in index.cards}
    return [(cards[s["card"]]["video"], float(s["start"]), float(s["end"])) for s in segments]


def random_plan(index, found, seconds, rng):
    """Cards picked at random from what a search returns, RANDOM_PIECE of each anywhere in it."""
    clips, left = [], seconds
    for card in rng.sample(found, len(found)):
        if left < moments.SEGMENT_MIN:
            break
        lo, hi = card["start"], card["end"]
        piece = min(RANDOM_PIECE, left, hi - lo)
        if piece < moments.SEGMENT_MIN:
            continue
        start = rng.uniform(lo, hi - piece)
        clips.append((card["video"], start, start + piece))
        left -= piece
    return clips


def rules(args):
    from anas_ai import providers

    instructions = load_instructions(args.instructions)
    os.makedirs(args.weight_cache, exist_ok=True)
    provider = providers.MediaPipeProvider(args.model, MANIFEST, cache_dir=args.weight_cache)
    texts = sorted({t for i in instructions for t in [i["text"], *i["topic"], *i.get("condition", [])]})
    vectors = {t: np.asarray(provider.embed_query(t), dtype=np.float32) for t in texts}
    directory = os.path.join(vm.CACHE, provider.model)
    provider.close()
    np.savez(os.path.join(directory, "montage-probes.npz"), keys=np.array(texts), vectors=np.stack(list(vectors.values())))
    index = load_index(directory)
    out = os.path.join(directory, "montage-rules")
    os.makedirs(out, exist_ok=True)
    for instruction in instructions:
        videos = None
        if instruction.get("condition"):
            found = index.search({t: vectors[t] for t in instruction["condition"]}, moments.CARDS_MAX)["cards"]
            videos = sorted({c["video"] for c in found if c["score"] >= moments.THRESHOLD})
        segments = index.plan({t: vectors[t] for t in instruction["topic"]}, instruction["seconds"], videos)
        plan = {"videos": videos, "segments": segments}
        if segments:
            clips = index.check(segments)
            files = {v: os.path.join(vm.VIDEOS, meta["file"]) for v, meta in index.videos.items()}
            cut = render.render_clips([(files[v], s, e) for v, s, e in clips], out)
            os.replace(cut, os.path.join(out, f"{instruction['id']}.mp4"))
        with open(os.path.join(out, f"{instruction['id']}.json"), "w", encoding="utf-8") as file:
            json.dump(plan, file, ensure_ascii=False, indent=2)
        print(instruction["id"], videos, [(s["card"], s["start"], s["end"]) for s in segments], flush=True)


def score(args):
    index = load_index(args.cache)
    with np.load(os.path.join(args.cache, "montage-probes.npz")) as data:
        vectors = dict(zip(data["keys"].tolist(), data["vectors"], strict=True))
    rng = random.Random(SEED)  # noqa: S311  a repeatable draw, not a secret
    out = {}
    for instruction in load_instructions(args.instructions):
        row = {}
        baseline = os.path.join(vm.CACHE, "montage", instruction["id"], f"{instruction['id']}.json")
        if os.path.exists(baseline):
            with open(baseline, encoding="utf-8") as file:
                parts = json.load(file)["parts"]
            row["baseline"] = measure(index, [(p["video"], p["start"], p["end"]) for p in parts], instruction)
        for version in ("rules", "model"):
            path = os.path.join(args.cache, f"montage-{version}", f"{instruction['id']}.json")
            if os.path.exists(path):
                with open(path, encoding="utf-8") as file:
                    row[version] = measure(index, clips_of(index, json.load(file)["segments"]), instruction)
        found = index.search({instruction["text"]: vectors[instruction["text"]]}, moments.CARDS_MAX)["cards"]
        draws = [measure(index, random_plan(index, found, instruction["seconds"], rng), instruction)
                 for _ in range(DRAWS)]
        row["random"] = {k: round(float(np.mean([d[k] for d in draws])), 3) for k in ("seconds", *METRICS)}
        row["random"]["onTopicRange"] = [min(d["onTopic"] for d in draws), max(d["onTopic"] for d in draws)]
        out[instruction["id"]] = row
    with open(os.path.join(args.cache, "montage-scores.json"), "w", encoding="utf-8") as file:
        json.dump(out, file, ensure_ascii=False, indent=2)
    print(f"{'instruction':<12}{'version':<10}{'seconds':>8}" + "".join(f"{m:>11}" for m in METRICS))
    for name, row in out.items():
        for version, m in row.items():
            print(f"{name:<12}{version:<10}{m['seconds']!s:>8}" + "".join(f"{m[k]!s:>11}" for k in METRICS))


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--set", default=vm.SET, help="the videos, as video_moments --set")
    parser.add_argument("--instructions", default=INSTRUCTIONS, help="the instructions for that set")
    steps = parser.add_subparsers(dest="step", required=True)
    step = steps.add_parser("rules", help="plan and cut each instruction by rule alone")
    step.add_argument("--model", required=True)
    step.add_argument("--weight-cache", default=os.path.join(datasets.ROOT, "cache", "xnnpack"))
    step = steps.add_parser("score", help="score every version against the instructions")
    step.add_argument("--cache", required=True, help="the model's directory under the cache, with index.json")
    args = parser.parse_args()
    vm.use_set(args.set)
    if args.step == "rules":
        rules(args)
    else:
        score(args)


if __name__ == "__main__":
    main()
