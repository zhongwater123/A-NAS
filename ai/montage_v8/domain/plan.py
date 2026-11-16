"""两步规划：拼第一步（大纲）和第二步（每个画面挑节拍）的请求，解析回答（第六版 cut.pick、cut.shots）。

这里只产出 (system, user, schema)，由 Planner 端口发给模型；模型是谁、怎么调用与这里无关。
"""

from .catalog import MARK, card_mark
from .music import music_note, slotted
from .options import Case, CutOptions
from .prompts import (
    CARD_MARKS,
    OPEN_CLOSE,
    ORDER_ASKS,
    PICK,
    PICK_MARKS,
    PICK_SYSTEM,
    SHOTS_MARKS,
    SHOTS_SYSTEM,
    STRUCTURE,
    STYLE_RULES,
)

SHORTEST, CAP = 2.0, 6.0
# 2026-11-16（用户）：同一个视频连着最多 RUN 个镜头，其中中景、近景、特写连着最多 SMALL_RUN 个；带人标精彩度时，
# 没有精彩节拍的视频最多 RUN_PLAIN 个。按视频数，不按卡片：一次滑车是好几张卡片。
RUN, RUN_PLAIN, SMALL_RUN = 3, 2, 2


class PromptDrift(ValueError):
    """要替换的提示词原文不在了：改提示词时没有同步改开关的替换。"""


def clock(t: float) -> str:
    return f"{int(t // 60)}:{int(t % 60):02d}"


def _replace(text: str, pairs, what: str) -> str:
    for a, b in pairs:
        if a not in text:
            raise PromptDrift(f"{what}: the prompt no longer says {a}")
        text = text.replace(a, b)
    return text


def units(card: dict, cap: float = CAP) -> list[tuple[int, float, float]]:
    """每个节拍剪出来的样子：(节拍号, 起点, 秒数)。从关键帧起（第一个从卡片开头），至少 SHORTEST，延续到下一个
    节拍，至多 cap。"""
    beats, out = card["answer"]["beats"], []
    for k, b in enumerate(beats):
        start = card["start"] if k == 0 else card["times"][k]
        if card["end"] - start < SHORTEST:          # 卡片最后一个节拍：往前提
            start = max(card["start"], card["end"] - SHORTEST)
        length = min(cap, max(SHORTEST, b["end"] - start), card["end"] - start)
        out.append((k + 1, start, length))
    return out


def pick_request(case: Case, opts: CutOptions, cards: list[dict], m: dict) -> tuple[str, str, dict]:
    """第一步：(system, user, schema)。cards：目录里的卡片，按拍摄时间；m：音乐（music.from_structure）。"""
    target = round(m["seconds"])
    many = len({c["video"] for c in cards}) > 1
    # 目录写每张卡片的过程（依次发生的事）：长卡片的概括只留最醒目的部分，丢了连接画面的那些步骤。
    lines = []
    for c in cards:
        where = f"视频{int(c['video'])} " if many else ""
        lines.append(f"{c['id']} {where}{clock(c['start'])} {c['end'] - c['start']:.0f}秒 | {c['answer'].get('scene', '')} | "
                     + " → ".join(c["answer"]["process"])
                     + (f" | {card_mark(opts.labels, c['id'], len(c['answer']['beats']))}" if opts.labels else ""))
    parts = len(m["parts"])
    user = PICK.format(instruction=opts.instruction(case), total_min=sum(c["end"] - c["start"] for c in cards) / 60, n=len(cards),
                       where="所在视频 " if many else "", catalog="\n".join(lines), music=music_note(m),
                       structure=STRUCTURE if case.style == "旅行" else "", open_close=OPEN_CLOSE, target=target,
                       parts_rule=f"{parts} 个部分（和音乐的 {parts} 段一一对应）")
    if opts.free_order:
        user = _replace(user, ORDER_ASKS, "free_order")
    if opts.labels:
        where = "所在视频 " if many else ""
        user = _replace(user, ((CARD_MARKS[0].format(where=where), CARD_MARKS[1].format(where=where)), PICK_MARKS), "labels")
    ids = [c["id"] for c in cards]
    schema = {"type": "object", "required": ["design", "opening", "ending", "unmet", "parts"], "properties": {
        "design": {"type": "string"}, "opening": {"type": "string"}, "ending": {"type": "string"},
        "unmet": {"type": "array", "items": {"type": "string"}},
        "parts": {"type": "array", "minItems": parts, "maxItems": parts, "items": {
            "type": "object", "required": ["name", "summary", "link", "scenes"], "properties": {
                "name": {"type": "string"}, "summary": {"type": "string"}, "link": {"type": "string"},
                "scenes": {"type": "array", "minItems": 1, "maxItems": max(len(x["slots"]) for x in m["parts"]), "items": {
                    "type": "object", "required": ["query", "cards"], "properties": {
                        "query": {"type": "string"},
                        "cards": {"type": "array", "minItems": 1, "maxItems": 3,
                                  "items": {"type": "string", "enum": ids}}}}}}}}}}
    return PICK_SYSTEM, user, schema


def shots_request(case: Case, opts: CutOptions, picked: dict, cards: dict, m: dict) -> dict:
    """第二步的请求：{system, user, schema, slots, seconds, pictures}。picked：第一步的回答；cards：{id: 卡片}。
    命令行版 Claude 的严格 schema 不支持 prefixItems，所以每段、每个画面是一个具名字段（段落1、画面1……），
    每个画面只能从自己的卡片里挑；shots_answer 再转回列表。"""
    target = round(m["seconds"])
    slots = slotted(m, picked["parts"])
    seconds = [[round(sum(b - a for a, b in pic), 2) for pic in part] for part in slots]
    lines, part_items = [], []
    for n, (p, secs) in enumerate(zip(picked["parts"], seconds, strict=True), 1):
        lines.append(f"段落{n}「{p['name']}」：{p['summary']}")
        lines.append(f"  衔接：{p['link']}")
        items = []
        for k, (sc, sec) in enumerate(zip(p["scenes"], secs, strict=True), 1):
            shots_here = len(slots[n - 1][k - 1])
            lines.append(f"  画面{k}：{sc['query']}（约 {sec:.0f} 秒，{shots_here} 个镜头）")
            ids = []
            for cid in dict.fromkeys(sc["cards"]):
                if cid not in cards:
                    continue
                c = cards[cid]
                a = c["answer"]
                lines.append(f"    卡片 {cid}（视频{int(c['video'])} {clock(c['start'])}–{clock(c['end'])}，{a.get('scene', '')}）：{a['summary']}")
                for (j, _start, length), b in zip(units(c, CAP), a["beats"], strict=True):
                    ids.append(f"{cid}/{j}")
                    mark = MARK.get(opts.labels.get(f"{cid}/{j}", ""), "")
                    lines.append(f"      {cid}/{j} {b['shot_size']} {length:.0f}秒 {b['what']}" + (f"【{mark}】" if mark else ""))
            need = min(len(ids), shots_here + 1)
            items.append({"type": "object", "required": ["beats", "shows", "link"], "properties": {
                "beats": {"type": "array", "minItems": max(1, need), "maxItems": max(1, min(len(ids), need + 4)),
                          "items": {"type": "string", "enum": ids}},
                "shows": {"type": "string"}, "link": {"type": "string"}}})
        part_items.append(items)
    rules = f"\n剪法：{STYLE_RULES[case.style]}" if STYLE_RULES.get(case.style) else ""
    user = (f"指令：{opts.instruction(case)}\n成片时长：约 {target} 秒\n体裁：{case.style}{rules}\n大纲：{picked['design']}\n"
            "段落和画面：\n" + "\n".join(lines))
    system = SHOTS_SYSTEM.format(target=target, run=RUN if not opts.labels else f"{RUN} 个（标了精彩的视频；其他视频 {RUN_PLAIN} 个）")
    if opts.labels:
        system = _replace(system, (SHOTS_MARKS,), "labels")
    schema = {"type": "object", "required": ["parts", "unmet"], "properties": {
        "unmet": {"type": "array", "items": {"type": "string"}},
        "parts": {"type": "object", "required": [f"段落{n}" for n in range(1, len(part_items) + 1)], "properties": {
            f"段落{n}": {"type": "object", "required": [f"画面{k}" for k in range(1, len(items) + 1)],
                        "properties": {f"画面{k}": item for k, item in enumerate(items, 1)}}
            for n, items in enumerate(part_items, 1)}}}}
    return {"system": system, "user": user, "schema": schema, "slots": slots, "seconds": seconds,
            "pictures": [len(items) for items in part_items]}


def shots_answer(answer: dict, pictures: list[int], picked: dict) -> tuple[dict, dict]:
    """第二步的回答转回列表：(raw, answer)，和第六版 shots-*.json 里的两个字段相同。"""
    raw = {"unmet": answer["unmet"],
           "parts": [{"pictures": [answer["parts"][f"段落{n}"][f"画面{k}"] for k in range(1, count + 1)]}
                     for n, count in enumerate(pictures, 1)]}
    return raw, {"parts": [{"name": p["name"], "summary": p["summary"], "pictures": r["pictures"]}
                           for p, r in zip(picked["parts"], raw["parts"], strict=True)],
                 "unmet": raw.get("unmet", [])}
