#!/usr/bin/env python3
"""Read-only check of photo OCR on the experimental NAS (step 6 acceptance).

Opens the photo catalog read-only and prints counts only: no text, names or
values read from photos. Run as root; writes /var/tmp/a-nas-ocr-check.txt.
"""

import collections
import contextlib
import datetime
import itertools
import json
import os
import re
import sqlite3
import statistics
import sys

CATALOG = sys.argv[1] if len(sys.argv) > 1 else "/srv/a-nas/data/photos/catalog.db"
# Where the read-only SSH account can read it; the end says how it is created.
OUT = sys.argv[2] if len(sys.argv) > 2 else "/var/tmp/a-nas-ocr-check.txt"  # noqa: S108
AI_SLICE = "/sys/fs/cgroup/anas.slice/anas-ai.slice"
# Asset names that say a photo is a screenshot; only their count is printed.
SCREENSHOT = re.compile(r"screenshot|screen[ _]shot|截屏|截图|屏幕快照|snipaste", re.I)

lines = []


def say(*parts):
    lines.append(" ".join(str(part) for part in parts))


def when(value):
    # Go writes nanoseconds; Python reads microseconds.
    return datetime.datetime.fromisoformat(re.sub(r"(\.\d{6})\d*Z$", r"\1+00:00", value))


def share(part, whole):
    return f"{part}/{whole} ({100 * part / whole:.1f}%)" if whole else f"{part}/0"


db = sqlite3.connect(f"file:{CATALOG}?mode=ro", uri=True)
q = db.execute

say("photo OCR check", datetime.datetime.now().astimezone().isoformat(timespec="seconds"))
say("catalog", CATALOG)

say("\n== libraries (kind, reads text, people)")
for kind, texts, people in q("SELECT kind, texts, people FROM libraries ORDER BY kind"):
    say(f"  {kind}: texts={texts} people={people}")

# Every content object a live photo shows, with what local AI did to it.
objects = {}
for object_id, media_type, video, texts_on in q("""
SELECT o.id, o.media_type, EXISTS (SELECT 1 FROM videos v WHERE v.object_id = o.id),
  EXISTS (SELECT 1 FROM assets ta JOIN libraries tl ON tl.id = ta.library_id
          WHERE ta.object_id = o.id AND ta.trashed_at IS NULL AND tl.texts = 1)
FROM objects o WHERE EXISTS (SELECT 1 FROM assets a WHERE a.object_id = o.id AND a.trashed_at IS NULL)"""):
    objects[object_id] = {"type": media_type, "video": bool(video), "texts_on": bool(texts_on), "screenshot": False}
for object_id, name in q("SELECT object_id, name FROM assets WHERE trashed_at IS NULL"):
    if object_id in objects and SCREENSHOT.search(name):
        objects[object_id]["screenshot"] = True
embedded = {row[0] for row in q("SELECT object_id FROM embeddings WHERE derivation = 'embedding/v1'")}
jobs = {
    row[0]: (row[1], row[2], row[3])
    for row in q("SELECT object_id, state, error_class, attempts FROM jobs WHERE derivation = 'ocr/v1'")
}
scanned = {row[0] for row in q("SELECT object_id FROM scans WHERE derivation = 'ocr/v1'")}

photos = {k: v for k, v in objects.items() if not v["video"]}
say("\n== photos local AI looks at (live content objects, not videos)")
say("  photos:", len(photos), " in a library that reads text:", sum(v["texts_on"] for v in photos.values()))
say("  with a vector:", share(len(embedded & photos.keys()), len(photos)))
routed = {k for k in photos if k in jobs or k in scanned}
say("  sent to have their text read:", share(len(routed), len(photos)))
state = collections.Counter()
for k in routed:
    job = jobs.get(k)
    state["read" if job is None else ("failed" if job[0] == "failed" else "queued")] += 1
say("  read:", state["read"], " queued:", state["queued"], " failed:", state["failed"])
running = sum(1 for k in routed if jobs.get(k, ("",))[0] == "running")
say("  of the queued, being read now:", running)
failures = collections.Counter((job[1] or "-", job[2]) for k, job in jobs.items() if job[0] == "failed" and k in photos)
for (error, attempts), count in failures.most_common():
    say(f"  failed: error_class={error} attempts={attempts}: {count}")
backlog = collections.Counter(
    row[0] for row in q("SELECT derivation FROM jobs WHERE derivation <> 'ocr/v1' AND state <> 'failed'")
)
say("  other queues waiting before reading text:", dict(backlog) or "none")

say("\n== which photos were sent (by media type; screenshots by file name)")
for media_type, count in collections.Counter(v["type"] for v in photos.values()).most_common():
    sent = sum(1 for k, v in photos.items() if v["type"] == media_type and k in routed)
    say(f"  {media_type}: sent {share(sent, count)}")
shots = [k for k, v in photos.items() if v["screenshot"]]
say(
    "  named like a screenshot: sent",
    share(sum(1 for k in shots if k in routed), len(shots)),
    " with a vector:",
    sum(1 for k in shots if k in embedded),
)

say("\n== readings")
readings = list(q("SELECT object_id, model, kind, finish, fields, checks, created_at FROM texts"))
live = [r for r in readings if r[0] in photos]
say("  readings:", len(readings), " of live photos:", len(live))
say("  models:", dict(collections.Counter(r[1] for r in live)))
say("  finish:", dict(collections.Counter(r[3] for r in live)))
kinds = collections.Counter(r[2] for r in live)
say("  kinds:", dict(kinds.most_common()))
fields_by_kind = collections.defaultdict(collections.Counter)
reasons = collections.Counter()
for _, _, kind, _, fields, checks, _ in live:
    values, results = json.loads(fields or "{}"), json.loads(checks or "{}")
    for name, value in values.items():
        tally = fields_by_kind[kind]
        tally["fields"] += 1
        if value:
            tally["read"] += 1
        check = results.get(name, {})
        if check.get("passed"):
            tally["passed"] += 1
        for reason in check.get("reasons", []):
            reasons[reason] += 1
for kind, tally in sorted(fields_by_kind.items()):
    say(f"  {kind}: fields {tally['fields']}, read {tally['read']}, passed {share(tally['passed'], tally['fields'])}")
say("  why not passed (a field can have several):", dict(reasons.most_common()))
say(
    "  corrections:",
    q("SELECT COUNT(*) FROM text_corrections").fetchone()[0],
    " changes:",
    q("SELECT COUNT(*) FROM text_changes").fetchone()[0],
)

say("\n== time between readings finishing (gaps under 15 min, i.e. while reading)")
times = sorted(when(r[6]) for r in readings)
gaps = [(b - a).total_seconds() for a, b in itertools.pairwise(times) if 0 < (b - a).total_seconds() < 900]
if gaps:
    gaps.sort()
    say(f"  gaps: {len(gaps)}, median {statistics.median(gaps):.0f} s, p90 {gaps[int(0.9 * (len(gaps) - 1))]:.0f} s")
per_day = collections.Counter(t.astimezone().date().isoformat() for t in times)
for day, count in sorted(per_day.items()):
    say(f"  {day}: {count} read")

say("\n== AI slice since boot")
try:
    with open("/proc/uptime") as uptime:
        say(f"  up {float(uptime.read().split()[0]) / 3600:.1f} h")
    for name in ("memory.max", "memory.peak", "memory.current"):
        with open(f"{AI_SLICE}/{name}") as value:
            say(f"  {name}: {value.read().strip()}")
    with open(f"{AI_SLICE}/memory.events") as events:
        say("  memory.events:", " ".join(events.read().split()))
except OSError as error:
    say("  unreadable:", error)

report = "\n".join(lines) + "\n"
# /var/tmp is writable by everyone, so root writes a fresh file: never one
# already there, nor a link someone left in its place.
with contextlib.suppress(FileNotFoundError):
    os.unlink(OUT)
with os.fdopen(os.open(OUT, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o644), "w") as out:
    out.write(report)
print(report)
