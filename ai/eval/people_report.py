"""Measures face detection, verification and clustering from eval.faces' cache.

Development only (docs/architecture/photo-people.md#评估与验收). For each
set it matches the cached faces to the labelled ones, then reports:

- detection: how many labelled faces were found, by face size in the AI input;
- verification: per embedder, the similarity that lets through at most one in
  10^3, 10^4 and 10^5 different-person pairs, and how many same-person pairs
  pass it;
- clustering: per embedder, eval.people.cluster over each library at every
  threshold and min_core in the sweep. The libraries of the --calibrate sets
  are split in two halves by a hash of their name: the calibration half
  chooses the setting with the best recall at precision >= --precision, and
  the test half reports it, as does every other set in full (Gallagher's
  family album is the one that matters most).

    python -m eval.people_report --detector face_detection_yunet_2026may.onnx-dfull-s0.5 \\
        --sets lfw,fgnet,gallagher --calibrate lfw,fgnet --report /datasets/runs/people/report.json
"""

import argparse
import collections
import hashlib
import json
import math
import os

import numpy as np
from PIL import Image

from eval import datasets, face_sets, people
from eval.faces import AI_INPUT_EDGE, Cache

SIZE_BUCKETS = (24, 48, 96)
FARS = (1e-3, 1e-4, 1e-5)
MIN_IOU = 0.4


def original_size(path):
    """Width and height as the photo is shown (EXIF orientations 5-8 swap them)."""
    with Image.open(path) as image:
        width, height = image.size
        orientation = image.getexif().get(0x0112, 1)
    return (height, width) if orientation in (5, 6, 7, 8) else (width, height)


class Sizes:
    """Each photo's shown size, kept beside the face caches: reading 13,000
    headers over the development machine's shared drive takes minutes."""

    def __init__(self, set_name):
        self.path = os.path.join(datasets.ROOT, "cache", "faces", f"sizes-{set_name}.json")
        self.sizes = {}
        if os.path.exists(self.path):
            with open(self.path, encoding="utf-8") as file:
                self.sizes = {key: tuple(value) for key, value in json.load(file).items()}
        self.changed = False

    def of(self, photo):
        if photo.key not in self.sizes:
            self.sizes[photo.key] = original_size(photo.path)
            self.changed = True
        return self.sizes[photo.key]

    def save(self):
        if self.changed:
            with open(self.path + ".partial", "w", encoding="utf-8") as file:
                json.dump(self.sizes, file)
            os.replace(self.path + ".partial", self.path)


def iou(a, b):
    x0, y0 = max(a[0], b[0]), max(a[1], b[1])
    x1, y1 = min(a[2], b[2]), min(a[3], b[3])
    inter = max(0.0, x1 - x0) * max(0.0, y1 - y0)
    union = (a[2] - a[0]) * (a[3] - a[1]) + (b[2] - b[0]) * (b[3] - b[1]) - inter
    return inter / union if union > 0 else 0.0


def match(photo, faces, scale, size):
    """Pairs each labelled face with the index of the found face that is it, or None."""
    boxes = [(f[0], f[1], f[0] + f[2], f[1] + f[3]) for f in faces]
    taken, pairs = set(), []
    for label in photo.faces:
        best = None
        if label.eyes is not None:
            (lx, ly), (rx, ry) = label.eyes
            mx, my = (lx + rx) / 2 * scale, (ly + ry) / 2 * scale
            reach = max(4.0, 0.5 * ((lx - rx) ** 2 + (ly - ry) ** 2) ** 0.5 * scale)
            # YuNet's first two landmarks are the eyes.
            near = [(((f[4] + f[6]) / 2 - mx) ** 2 + ((f[5] + f[7]) / 2 - my) ** 2, i)
                    for i, f in enumerate(faces) if i not in taken]
            distance, index = min(near, default=(math.inf, None))
            best = index if distance <= reach ** 2 else None
        elif label.box is None:
            cx, cy = size[0] * scale / 2, size[1] * scale / 2
            inside = [i for i, b in enumerate(boxes) if b[0] <= cx <= b[2] and b[1] <= cy <= b[3] and i not in taken]
            best = max(inside, key=lambda i: faces[i][14], default=None)
        else:
            target = [value * scale for value in label.box]
            scored = [(iou(target, b), i) for i, b in enumerate(boxes) if i not in taken]
            overlap, index = max(scored, default=(0.0, None))
            best = index if overlap >= MIN_IOU else None
        if best is not None:
            taken.add(best)
        pairs.append((label, best))
    return pairs


def bucket(edge):
    for limit in SIZE_BUCKETS:
        if edge < limit:
            return f"<{limit}"
    return f">={SIZE_BUCKETS[-1]}"


def half(library):
    return "test" if int(hashlib.sha256(library.encode()).hexdigest()[:8], 16) % 2 else "calibration"


def collect(set_name, cache, min_score, min_edge):
    """Detection counts and, per embedder, the matched occurrences by library."""
    detection = collections.Counter()
    occurrences = collections.defaultdict(lambda: collections.defaultdict(list))
    sizes = Sizes(set_name)
    for photo in face_sets.SETS[set_name]():
        entry = cache.photos.get(photo.key)
        if entry is None:
            continue
        size = sizes.of(photo)
        scale = min(1.0, AI_INPUT_EDGE / max(size))
        faces = entry["faces"]
        keep = [i for i, f in enumerate(faces) if f[14] >= min_score and min(f[2], f[3]) >= min_edge]
        kept = faces[keep]
        for label, index in match(photo, kept, scale, size):
            edge = None
            if label.box is not None:
                edge = min(label.box[2] - label.box[0], label.box[3] - label.box[1]) * scale
            elif label.eyes is not None:
                # A face is about twice as wide as the distance between the eyes.
                (lx, ly), (rx, ry) = label.eyes
                edge = 2 * ((lx - rx) ** 2 + (ly - ry) ** 2) ** 0.5 * scale
            elif index is not None:
                edge = min(kept[index][2], kept[index][3])
            name = bucket(edge) if edge is not None else "unknown"
            detection[f"labelled {name}"] += 1
            if index is None:
                continue
            detection[f"found {name}"] += 1
            if label.identity is None:
                continue
            for embedder, vectors in entry["vectors"].items():
                vector = vectors[keep[index]]
                occurrences[embedder][photo.library or set_name].append(
                    people.Occurrence(f"{photo.key}#{keep[index]}", photo.key, label.identity, vector, label.group))
    sizes.save()
    return detection, occurrences


def run(libraries, setting):
    """Clusters every library with setting and returns the scores and group breakdowns."""
    scores, groups = [], collections.defaultdict(list)
    for found in libraries.values():
        person = people.cluster(found, setting["threshold"], setting["minCore"], setting["assign"])
        scores.append(people.score(found, person))
        for name, value in people.by_group(found, person).items():
            groups[name].append(value)
    return {"all": people.total(scores) if scores else None,
            "groups": {name: people.total(values) for name, values in sorted(groups.items())}}


def sweep(libraries, thresholds, cores, assigns):
    results = []
    for assign in assigns:
        for min_core in cores:
            for threshold in thresholds:
                setting = {"assign": assign, "minCore": min_core, "threshold": round(float(threshold), 4)}
                results.append({**setting, "calibration": run(libraries, setting)["all"]})
    return results


def choose(results, precision):
    """The setting with the best calibration recall at the precision floor."""
    passing = [r for r in results if r["calibration"] and r["calibration"]["precision"] >= precision]
    # Among equal recall, the stricter threshold: it leaves a margin.
    return max(passing, key=lambda r: (round(r["calibration"]["recall"], 3), r["threshold"]), default=None)


def operating_points(occurrences):
    same, different = people.verification(occurrences)
    points = {}
    for far in FARS:
        threshold = people.threshold_for_far(different, far)
        points[f"{far:g}"] = {"threshold": round(threshold, 4),
                              "tar": float(np.mean(same >= threshold)) if len(same) else None}
    return {"samePairs": len(same), "differentPairs": len(different), "points": points}, same, different


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--detector", required=True, help="the cache directory's name under cache/faces")
    parser.add_argument("--sets", default="lfw")
    parser.add_argument("--calibrate", default="lfw", help="sets whose calibration halves choose the setting")
    parser.add_argument("--min-score", type=float, default=0.8)
    parser.add_argument("--min-edge", type=float, default=0, help="ignore faces narrower than this in AI-input pixels")
    parser.add_argument("--precision", type=float, default=0.99)
    parser.add_argument("--cores", default="2,3,4")
    parser.add_argument("--assign", default="nearest,majority,centroid")
    parser.add_argument("--report", required=True)
    args = parser.parse_args()
    calibrate = set(args.calibrate.split(","))

    report = {"detector": args.detector, "minScore": args.min_score, "minEdge": args.min_edge,
              "precisionFloor": args.precision, "calibrate": sorted(calibrate), "sets": {}, "embedders": {}}
    # embedder -> pool ("calibration", "test" or a held-out set) -> library -> occurrences
    pools = collections.defaultdict(lambda: collections.defaultdict(dict))
    for set_name in args.sets.split(","):
        cache = Cache(os.path.join(datasets.ROOT, "cache", "faces", args.detector, f"{set_name}.npz"))
        detection, occurrences = collect(set_name, cache, args.min_score, args.min_edge)
        found = {"photos": len(cache.photos), "detection": dict(sorted(detection.items())), "verification": {}}
        for embedder, libraries in sorted(occurrences.items()):
            found["verification"][embedder] = operating_points([o for f in libraries.values() for o in f])[0]
            for library, members in libraries.items():
                pool = half(library) if set_name in calibrate else set_name
                pools[embedder][pool][f"{set_name}/{library}"] = members
        report["sets"][set_name] = found
        print(f"{set_name}: {found['photos']} photos, detection {found['detection']}", flush=True)

    for embedder, by_pool in sorted(pools.items()):
        calibration = by_pool.get("calibration", {})
        everything = [o for f in calibration.values() for o in f]
        verification, same, different = operating_points(everything)
        low = float(np.quantile(different, 0.5)) if len(different) else 0.0
        high = float(np.quantile(same, 0.95)) if len(same) else 1.0
        thresholds = np.round(np.arange(max(0.0, low), min(1.0, high), 0.02), 2)
        print(f"{embedder}: sweeping {len(thresholds)} thresholds over {len(calibration)} libraries, "
              f"{len(everything)} occurrences", flush=True)
        results = sweep(calibration, thresholds, [int(c) for c in args.cores.split(",")], args.assign.split(","))
        chosen = choose(results, args.precision)
        entry = {"calibrationVerification": verification, "chosen": chosen, "sweep": results, "results": {}}
        if chosen:
            setting = {key: chosen[key] for key in ("assign", "minCore", "threshold")}
            for pool, libraries in sorted(by_pool.items()):
                if pool != "calibration":
                    entry["results"][pool] = run(libraries, setting)
            print(f"{embedder}: threshold {setting['threshold']} minCore {setting['minCore']} {setting['assign']}; "
                  f"calibration P {chosen['calibration']['precision']:.4f} R {chosen['calibration']['recall']:.3f}", flush=True)
            for pool, result in entry["results"].items():
                found = result["all"] or {}
                print(f"  {pool}: P {found.get('precision', 0):.4f} R {found.get('recall', 0):.3f} "
                      f"B3 P {found.get('bcubedPrecision', 0):.4f} R {found.get('bcubedRecall', 0):.3f} "
                      f"coverage {found.get('coverage', 0):.3f} people {found.get('people')} "
                      f"identities {found.get('identities')} mixed {found.get('mixedPeople')}", flush=True)
                for name, group in result["groups"].items():
                    print(f"    {name}: P {group['precision']:.4f} R {group['recall']:.3f} "
                          f"coverage {group['coverage']:.3f}", flush=True)
        else:
            print(f"{embedder}: no setting reaches precision {args.precision}", flush=True)
        report["embedders"][embedder] = entry
    os.makedirs(os.path.dirname(args.report), exist_ok=True)
    with open(args.report, "w", encoding="utf-8") as file:
        json.dump(report, file, ensure_ascii=False, indent=1)


if __name__ == "__main__":
    main()
