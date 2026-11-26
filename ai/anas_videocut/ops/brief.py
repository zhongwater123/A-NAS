"""What the member's VNAS reads to plan a film (docs/architecture/montage-center.md, 规划): the eighth version's two
requests split into rules, data and the exact schema. The rules are the fixed template in prompts/; the data and the
schema come back from the montage tools' briefs, which call this through the compute Worker's plan_brief.

The data is what pick_request and shots_request put in their text, as fields: the catalog lines, the music's parts,
the outline's pictures with their beats. The schemas are theirs, unchanged. The domain stays as the eighth version
wrote it; this module only rearranges what it builds.
"""

import statistics
from pathlib import PurePath

from ..algo import plan as planrules
from ..algo.catalog import MARK, card_mark
from ..algo.options import Case, CutOptions
from ..algo.prompts import CARD_MARKS

# The template the data and the schema go with (prompts/<TEMPLATE>.md); a recut's (#596) starts from a version's plan.
TEMPLATE = "plan-v2"
RECUT_TEMPLATE = "plan-v3"
# What a member who asked for nothing gets: the eighth version's cases each wrote their own.
DEFAULT_INSTRUCTION = "按拍摄时间顺序，把这些素材剪成一个有开头、中间和结尾的短片"


def case_of(request):
    """The case and options of a job: no style of its own, the member's ask after the default instruction."""
    target = float(request["music"]["seconds"])
    case = Case(key="job", index="job", instruction=DEFAULT_INSTRUCTION, style="", name="job", target=target)
    return case, CutOptions(free_order=not request.get("chronological", True), ask=request.get("ask", ""))


def numbered(cards):
    """The cards with their videos numbered 1, 2… in shooting order, as the eighth version's catalogs name them."""
    numbers = {}
    for c in cards:
        numbers.setdefault(c["video"], str(len(numbers) + 1))
    return [{**c, "video": numbers[c["video"]]} for c in cards]


def music_parts(m):
    """The music's parts as the first step reads them (music.music_note's data, without its rules)."""
    return {"song": PurePath(m["song"]).stem, "seconds": round(m["seconds"]),
            "parts": [{"part": n, "from": round(p["start"]), "to": round(p["end"]), "level": p["level"],
                       "shots": len(p["slots"]), "shotSeconds": round(statistics.median([b - a for a, b in p["slots"]]), 1)}
                      for n, p in enumerate(m["parts"], 1)]}


def recut(request, data):
    """A recut's brief also carries the member's sentence and the plan it starts from: base holds the version's
    outline and, for each part and picture, the beats it was cut with, what it shows and how it links."""
    if not request.get("change"):
        return data, TEMPLATE
    base = request.get("base") or {}
    outline = base.get("outline") or {}
    shots = [[{"beats": pic.get("beats", []), "shows": pic.get("shows", ""), "link": pic.get("link", "")}
              for pic in part.get("pictures", [])] for part in base.get("shots") or []]
    return {**data, "change": request["change"], "base": {"outline": outline, "shots": shots}}, RECUT_TEMPLATE


def pick_brief(request):
    """The first step: {data, schema}. request: cards (the catalog, in shooting order), music, ask, chronological."""
    case, opts = case_of(request)
    cards, m = numbered(request["cards"]), request["music"]
    many = len({c["video"] for c in cards}) > 1
    _system, _user, schema = planrules.pick_request(case, opts, cards, m)
    lines = []
    for c in cards:
        where = f"视频{int(c['video'])} " if many else ""
        lines.append(f"{c['id']} {where}{planrules.clock(c['start'])} {c['end'] - c['start']:.0f}秒 | {planrules.place(c['answer'])} | "
                     + " → ".join(c["answer"]["process"])
                     + (f" | {card_mark(opts.labels, c['id'], len(c['answer']['beats']))}" if opts.labels else ""))
    data = {"instruction": opts.instruction(case), "minutes": round(sum(c["end"] - c["start"] for c in cards) / 60, 1),
            "cardCount": len(cards), "lineFormat": CARD_MARKS[0].format(where="所在视频 " if many else ""),
            "catalog": lines, "music": music_parts(m), "order": "free" if opts.free_order else "shooting",
            "partCount": len(m["parts"]), "targetSeconds": round(m["seconds"])}
    data, template = recut(request, data)
    return {"data": data, "schema": schema, "template": template}


def shots_brief(request):
    """The second step: {data, schema, slots, pictures}. request: picked (the outline taken), cards (the catalog, in
    shooting order, so the videos are numbered as in the first step), music, ask, chronological. slots and pictures stay with the montage center: the fill reads the slots, and the
    answer is turned back with the pictures (shots_answer)."""
    case, opts = case_of(request)
    m, picked = request["music"], request["picked"]
    cards = {c["id"]: c for c in numbered(request["cards"])}
    built = planrules.shots_request(case, opts, picked, cards, m)
    slots = built["slots"]
    parts = []
    for n, (p, secs) in enumerate(zip(picked["parts"], built["seconds"], strict=True), 1):
        pictures = []
        for k, (sc, sec) in enumerate(zip(p["scenes"], secs, strict=True), 1):
            choices = []
            for cid in dict.fromkeys(sc["cards"]):
                if cid not in cards:
                    continue
                c = cards[cid]
                a = c["answer"]
                beats = []
                for (j, _start, length), b in zip(planrules.units(c, planrules.CAP), a["beats"], strict=True):
                    mark = MARK.get(opts.labels.get(f"{cid}/{j}", ""), "")
                    beats.append({"beat": f"{cid}/{j}", "shotSize": b["shot_size"], "seconds": round(length),
                                  "what": b["what"], **({"mark": mark} if mark else {})})
                choices.append({"card": cid, "video": int(c["video"]),
                                "from": planrules.clock(c["start"]), "to": planrules.clock(c["end"]),
                                "scene": planrules.place(a), "summary": a["summary"], "beats": beats})
            pictures.append({"picture": k, "query": sc["query"], "seconds": round(sec), "shots": len(slots[n - 1][k - 1]),
                             "cards": choices})
        parts.append({"part": n, "name": p["name"], "summary": p["summary"], "link": p["link"], "pictures": pictures})
    data = {"instruction": opts.instruction(case), "targetSeconds": round(m["seconds"]), "design": picked["design"],
            "parts": parts}
    data, template = recut(request, data)
    return {"data": data, "schema": built["schema"], "template": template, "slots": built["slots"],
            "pictures": built["pictures"]}
