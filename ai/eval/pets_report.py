"""Measures pet detection and individual clustering from eval.pets' cache.

Development only (docs/architecture/photo-people.md#评估与验收). Each photo
is labelled with one animal; the best detected box of that photo stands for
it, as the photo service would find it. Per embedder, eval.people.cluster
runs over each household at every threshold, min_core and rule; a setting
must keep every set at precision >= --precision on its own (a stranger
pulled into a family pet, or two family pets merged, counts against it;
in the cat set every person formed is a merge of different cats), and the
one with the best recall on the worst dog set wins.

    python -m eval.pets_report --sets mpdd,dogfacenet,oxford-cats --report /datasets/runs/people/report-pets.json
"""

import argparse
import collections
import json
import os

import numpy as np

from eval import datasets, people, pet_sets
from eval.pets import Cache
from eval.people_report import describe, run, sweep


def collect(set_name, cache):
    """Found counts and, per embedder, each household's occurrences."""
    counts = collections.Counter()
    occurrences = collections.defaultdict(lambda: collections.defaultdict(list))
    for photo in pet_sets.SETS[set_name]():
        entry = cache.photos.get(photo.key)
        if entry is None:
            continue
        counts["photos"] += 1
        boxes = entry["boxes"]
        if not len(boxes):
            continue
        counts["found"] += 1
        best = int(np.argmax(boxes[:, 1]))
        label = photo.faces[0]
        for embedder, vectors in entry["vectors"].items():
            occurrences[embedder][photo.library].append(
                people.Occurrence(f"{photo.key}#{best}", photo.key, label.identity, vectors[best], label.group))
    return counts, occurrences


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--sets", default="mpdd,dogfacenet,oxford-cats")
    parser.add_argument("--precision", type=float, default=0.99)
    parser.add_argument("--cores", default="2,3")
    parser.add_argument("--assign", default="nearest,centroid")
    parser.add_argument("--thresholds", default="0.50,0.98,0.02")
    parser.add_argument("--report", required=True)
    args = parser.parse_args()
    first, last, step = (float(value) for value in args.thresholds.split(","))
    thresholds = np.round(np.arange(first, last + step / 2, step), 3)

    report = {"precisionFloor": args.precision, "sets": {}, "embedders": {}}
    pools = collections.defaultdict(dict)  # embedder -> "<set>:calibration" -> households
    for set_name in args.sets.split(","):
        cache = Cache(os.path.join(datasets.ROOT, "cache", "pets", f"{set_name}.npz"))
        counts, occurrences = collect(set_name, cache)
        report["sets"][set_name] = dict(counts)
        print(f"{set_name}: {dict(counts)}", flush=True)
        for embedder, households in occurrences.items():
            pools[embedder][f"{set_name}:calibration"] = households
    for embedder, by_set in sorted(pools.items()):
        print(f"{embedder}: sweeping {len(thresholds)} thresholds over {', '.join(sorted(by_set))}", flush=True)
        results = sweep(by_set, thresholds, [int(c) for c in args.cores.split(",")], args.assign.split(","))
        # Recall only means something where a household has family pets.
        dog_sets = {pool for pool in by_set if not pool.startswith("oxford-cats")}
        for row in results:
            row["worst"] = min((row["calibration"][pool]["recall"] for pool in dog_sets), default=0)
        passing = [row for row in results if all(found and found["precision"] >= args.precision
                                                 for found in row["calibration"].values())]
        chosen = max(passing, key=lambda row: (round(row["worst"], 3), row["threshold"]), default=None)
        report["embedders"][embedder] = {"chosen": chosen, "sweep": results}
        if chosen:
            print(f"{embedder}: threshold {chosen['threshold']} minCore {chosen['minCore']} {chosen['assign']}", flush=True)
            for pool, found in sorted(chosen["calibration"].items()):
                print(f"  {pool}: {describe(found)}", flush=True)
        else:
            print(f"{embedder}: no setting keeps every set at precision {args.precision}", flush=True)
    os.makedirs(os.path.dirname(args.report), exist_ok=True)
    with open(args.report, "w", encoding="utf-8") as file:
        json.dump(report, file, ensure_ascii=False, indent=1)


if __name__ == "__main__":
    main()
