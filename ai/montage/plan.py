"""What the planning model is given to plan a montage, and how its plan becomes a cut (#350).

One request carries all the model needs, and nothing else: a system prompt of the rules, and a
message of the instruction, the cut's seconds, its style and the catalog of cards (moments.Index
catalog with the seconds each card can run, without the cards text is laid over), in the film's
parts (moments.PARTS, #398): a part of few cards lists them, a part of many gives the summary the
local model wrote of them. The model answers with one JSON object, a story line and the segments,
each a card, or a part whose card the program picks by what the segment shows (moments.Index.pick),
with what it shows and why it was chosen; the program paces each segment by how much its
card's picture changes (moments.Index.pace_of) and sets its seconds (moments.Index.fill). A plan the video service refuses goes back with its problems, in
the same conversation, for the whole plan again.
"""

import json
import re

from montage import moments
from montage.styles import STYLES

_PACES = "，".join(f"{name}约 {seconds:g} 秒" for name, seconds in moments.PACES.items())

SYSTEM = (
    "你是剪辑规划。按成员的指令，从素材卡片里选段、排顺序，交一份剪辑计划；画面由程序按计划剪出，带原声，不加配乐和字幕，末尾淡出到黑。\n"
    "素材目录按原片时间分成片段：每个片段先一行写它在原片里的时间、有几张卡片、各节奏几张。"
    "卡片少的片段，下面每行一张卡片：“卡片 id 可剪X秒 节奏Y 光线 描述（亮点）毛病”，卡片 id 是“视频-镜头序号-段”；"
    "卡片多的片段只给一段概述，是本机小模型按这些卡片写的。"
    f"节奏是程序按画面变化幅度定的：画面几乎不动的慢，变化大的快，其余中；程序按节奏定每段长短（{_PACES}，只是参考）。"
    "描述由本机小模型看三帧写成，可能看错。\n"
    "先写故事线：按指令要的效果，把成片的段落按播放顺序列出来，每段一句内容和对应的卡片。只放讲得进故事线的内容。\n"
    "排段的规则：\n"
    "1. 每段写 card 或 part，再写 shows、reason。用目录里列出的卡片，就写 card（卡片 id）；"
    "用只给了概述的片段，就写 part（片段号），程序按 shows 在这个片段里找最合适的卡片。"
    "shows 用一句话写这段要呈现的、画面里看得见的东西，不写地名、年代、情绪，程序按它找画面、定入出点；"
    "reason 是选镜理由：为什么选这张卡片，它在故事线里起什么作用。\n"
    "2. 每段长短由程序定，不用写。\n"
    "只输出一个 JSON 对象，不要别的文字："
    '{"story": "故事线", "segments": [{"card": "卡片 id", "part": 片段号, "shows": "画面", "reason": "选镜理由"}]}，'
    "card 和 part 写一个即可。"
    "计划不合规则会被退回并说明原因，照着改好后再输出完整的新计划。"
)


FIELDS = ("card", "part", "shows", "reason")


def _clock(seconds: float) -> str:
    return f"{int(seconds // 60)}:{int(seconds % 60):02d}"


def message(index: moments.Index, instruction: str, seconds: float, style: str | None) -> str:
    """The request's one message: the instruction, the seconds, the style's rules and the catalog
    in the film's parts (index.parts, one a video without them), each part's line and then its
    cards' lines, or its summary when they run past moments.PART_CHARS."""
    catalog = index.catalog(times=False, lengths=True, titles=False, paces=True)
    lines = [f"指令：{instruction}", f"目标时长：约 {seconds:g} 秒"]
    if style:
        lines.append(f"体裁：{style}" + (f"。剪法：{STYLES[style]}" if STYLES.get(style) else ""))
    lines.append("素材：" + "，".join(f"视频 {v}（{e['seconds']:g} 秒）" for v, e in catalog["videos"].items()))
    listed = {line.split(" ", 1)[0]: line for entry in catalog["videos"].values() for line in entry["cards"]}
    cards = {c["id"]: c for c in index.cards}
    parts = index.parts or [{"video": v, "cards": [c["id"] for c in index.cards if c["video"] == v], "summary": ""}
                            for v in index.videos]
    for n, part in enumerate(parts, 1):
        # Cards no segment can be cut from (fewer than SEGMENT_MIN usable seconds) are left out.
        ids = [i for i in part["cards"] if i in listed and index.usable(cards[i]) >= moments.SEGMENT_MIN]
        if not ids:
            continue
        paces = [index.pace_of(i) for i in ids]
        counted = "、".join(f"{p} {paces.count(p)}" for p in moments.PACES if paces.count(p))
        where = f"视频 {part['video']} " if len(index.videos) > 1 else ""
        lines.append(f"片段{n}（{where}{_clock(cards[part['cards'][0]]['start'])}–{_clock(cards[part['cards'][-1]]['end'])}，"
                     f"{len(ids)} 张卡片{'：' + counted if counted else ''}）")
        body = [listed[i] for i in ids]
        if part.get("summary") and sum(len(line) for line in body) > moments.PART_CHARS:
            lines.append("概述：" + part["summary"])
        else:
            lines += body
    return "\n".join(lines)


def refused(problems: list[str]) -> str:
    """The message that sends a refused plan back."""
    return "计划被退回：\n" + "\n".join(f"- {p}" for p in problems) + "\n改好后输出完整的新计划，只输出 JSON。"


def parsed(text: str) -> dict:
    """The plan in the model's answer: its one JSON object, fenced or not."""
    found = re.search(r"\{.*\}", text, re.S)
    if not found:
        raise ValueError("回答里没有 JSON 对象")
    plan = json.loads(found.group(0))
    if not isinstance(plan.get("segments"), list) or not plan["segments"]:
        raise ValueError("计划里没有 segments")
    return plan


def request(plan: dict, seconds: float, style: str | None) -> dict:
    """The video service's /montage body for the plan: each segment located by its shows,
    its seconds set to add up to seconds by its pace, its ends kept off cuts, no frames looked at."""
    segments = [{k: s[k] for k in FIELDS if s.get(k) is not None} | {"probe": s.get("shows", "")}
                for s in plan["segments"]]
    return {"segments": segments, "skipped": [], "span": "probe", "seconds": seconds, "style": style,
            "margin": True, "edges": True, "look": False, "pace": "picture"}
