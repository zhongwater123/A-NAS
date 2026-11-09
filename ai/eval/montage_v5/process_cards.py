"""#416 (2026-11-09): one video's cards described again with the process field (tag_beats.describe_v2,
process=True), same key frames as cards-<case>.json, which is left alone. Writes proc-<case>.json under
paths.CARDS; cut.py's step 1 reads it when it is there. Resumes.

  python3 process_cards.py URL 01 02"""
import sys
import time

import cards as run
import tag_beats
from paths import CARDS as HERE, CUTS, read_json, write_json

KIND = {"01": "film", "02": "travel-vlog", "04": "travel-vlog"}
url = sys.argv[1]
for case in sys.argv[2:]:
    cards = read_json(HERE / f"cards-{case}.json")
    titles = read_json(CUTS / f"titles-{case}.json")
    prof = run.profile(KIND[case])
    path = HERE / f"proc-{case}.json"
    done = read_json(path) if path.exists() else {}
    for cid, c in cards.items():
        if cid in done or not c.get("answer") or titles[cid]["title"]:
            continue
        entry = {k: c[k] for k in ("id", "video", "start", "end", "windows", "times", "frames")}
        t0 = time.perf_counter()
        try:
            answer, score, seconds, errors = tag_beats.describe_v2(
                c["frames"], [t - c["start"] for t in c["times"]], list(range(1, len(c["frames"]) + 1)),
                c["end"] - c["start"], url, prof, process=True)
            for b, a_, b_ in zip(answer["beats"], [c["start"], *c["times"][1:]], [*c["times"][1:], c["end"]], strict=True):
                b["start"], b["end"] = round(a_, 2), round(b_, 2)
            entry |= {"answer": answer, "score": score, "seconds": round(seconds, 1), "errors": errors}
        except Exception as error:  # noqa: BLE001
            entry |= {"error": str(error), "seconds": round(time.perf_counter() - t0, 1)}
        done[cid] = entry
        write_json(done, path)
        print(case, cid, len(c["frames"]), entry.get("answer", {}).get("process") or entry.get("error"), flush=True)
    print("CASE-DONE", case, flush=True)
