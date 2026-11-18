"""What the planning model is given to plan a montage, and how its plan becomes a cut (#350).

One request carries all the model needs, and nothing else: a system prompt of the rules, and a
message of the instruction, the cut's seconds, its style and the catalog of cards (moments.Index
catalog with the seconds each card can run, without the cards text is laid over), in the film's
parts (moments.PARTS, #398): a part of few cards lists them, a part of many gives the summary the
local model wrote of them. The model answers with one JSON object, a story line and the segments,
each a card, or a part whose card the program picks by what the segment shows (moments.Index.pick),
with what it shows, its weight and why it was chosen; the program shares the target's seconds
out by the weights (moments.allocated, 剪法24), among a paragraph's shots by how much each card's
picture changes (moments.Index.pace_of), and sets the ends (moments.Index.fill). A plan the video service refuses goes back with its problems, in
the same conversation, for the whole plan again.
"""

import json
import re

from montage import moments
from montage.styles import STYLES

SYSTEM = (
    "你是剪辑规划。按成员的指令，从素材卡片里选段、排顺序，交一份剪辑计划；画面由程序按计划剪出，带原声，不加配乐和字幕，末尾淡出到黑。\n"
    "素材目录按原片时间分成片段：每个片段先一行写它在原片里的时间、有几张卡片。"
    "卡片少的片段，下面每行一张卡片：“卡片 id 可剪X秒 光线 描述（亮点）毛病”，卡片 id 是“视频-镜头序号-段”；"
    "卡片多的片段只给一段概述，是本机小模型按这些卡片写的。"
    "描述由本机小模型看三帧写成，可能看错。"
    "先写故事线：按指令要的效果，把成片按播放顺序分成几个段落，每个段落一句内容和对应的片段或卡片。只放讲得进故事线的内容。\n"
    "成片由段落组成。每个段落是原片同一时刻里连续的几个镜头：你指定从哪里开始，取几个镜头，"
    "程序从这里按原片顺序往后连续取，段落内部怎么接，沿用原片的剪辑；只有段落之间才换时刻、换地点，要想好段落之间怎么过渡。\n"
    "每个段落写：\n"
    "1. card 或 part：从哪里开始。用目录里列出的卡片，就写 card（卡片 id）；用只给了概述的片段，就写 part（片段号），"
    "程序按 shows 在这个片段里找起点。\n"
    f"2. shots：取几个连续镜头，1–{moments.SHOTS_MAX} 个，一般 2–4 个；连续快切的高潮可以多取；"
    "1 个就是单独插一个镜头，比如一张报纸。\n"
    f"3. weight：这个段落在成片里占多大分量，{moments.WEIGHT_MIN}–{moments.WEIGHT_MAX} 的整数，按故事需要定：要交代清楚、要停留的给大，"
    f"冲击、紧张、一闪而过的给小；程序先给每个镜头 {moments.SEGMENT_MIN:g} 秒，再按分量把成片剩下的时长分给各段落，"
    "段落里再按画面变化分给各个镜头。\n"
    "4. shows：一句话写这个段落要呈现的、画面里看得见的东西，不写地名、年代、情绪，程序按它找起点、定入出点。\n"
    "5. reason：这个段落在故事线里起什么作用。\n"
    "另外：相邻两个段落不要从同一个镜头里接着取，看起来像跳切；电影和夜景本来就暗，暗不算毛病；不要用片头片尾字幕、黑场或纯文字画面。\n"
    "按故事需要选，不按片段长短或卡片多少分配：很长的片段可能只用一个段落，很短的片段也可以用。\n"
    "只输出一个 JSON 对象，不要别的文字："
    '{"story": "故事线", "paragraphs": [{"card": "卡片 id", "part": 片段号, "shots": 镜头数, "weight": 分量, "shows": "画面", "reason": "作用"}]}，'
    "card 和 part 写一个即可。"
    "计划不合规则会被退回并说明原因，照着改好后再输出完整的新计划。"
)


FIELDS = ("card", "part", "shots", "weight", "shows", "reason")


def _clock(seconds: float) -> str:
    return f"{int(seconds // 60)}:{int(seconds % 60):02d}"


def message(index: moments.Index, instruction: str, seconds: float, style: str | None) -> str:
    """The request's one message: the instruction, the seconds, the style's rules and the catalog
    in the film's parts (index.parts, one a video without them), each part's line and then its
    cards' lines, or its summary when they run past moments.PART_CHARS."""
    catalog = index.catalog(times=False, lengths=True, titles=False)
    lines = [f"指令：{instruction}", f"目标时长：成片约 {seconds:g} 秒"]
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
        where = f"视频 {part['video']} " if len(index.videos) > 1 else ""
        lines.append(f"片段{n}（{where}{_clock(cards[part['cards'][0]]['start'])}–{_clock(cards[part['cards'][-1]]['end'])}，"
                     f"{len(ids)} 张卡片）")
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
    # A plan of paragraphs (剪法19) is sent as its segments, each one paragraph.
    if isinstance(plan.get("paragraphs"), list) and "segments" not in plan:
        plan["segments"] = plan["paragraphs"]
    if not isinstance(plan.get("segments"), list) or not plan["segments"]:
        raise ValueError("计划里没有 paragraphs")
    # A paragraph's seconds are the program's, from its weight (剪法24): each needs one.
    if plan["segments"] is plan.get("paragraphs"):
        wrong = [n for n, s in enumerate(plan["segments"], 1)
                 if not isinstance(s, dict) or not _whole(s.get("weight"), moments.WEIGHT_MIN, moments.WEIGHT_MAX)]
        if wrong:
            raise ValueError("、".join(f"第 {n} 个段落" for n in wrong)
                             + f"的 weight 要是 {moments.WEIGHT_MIN}–{moments.WEIGHT_MAX} 的整数")
    return plan


def _whole(value, low: int, high: int) -> bool:
    return isinstance(value, int) and not isinstance(value, bool) and low <= value <= high


def request(plan: dict, seconds: float, style: str | None) -> dict:
    """The video service's /montage body for the plan: each segment located by its shows; with
    weights (剪法24), each paragraph's seconds shared out of the target by them (moments.allocated),
    else its seconds set to add up to the target by its pace; its ends kept off cuts, no frames looked at."""
    segments = [{k: s[k] for k in FIELDS if s.get(k) is not None} | {"probe": s.get("shows", "")}
                for s in plan["segments"]]
    if segments and all(_whole(s.get("weight"), moments.WEIGHT_MIN, moments.WEIGHT_MAX) for s in segments):
        shots = [s["shots"] if _whole(s.get("shots"), 1, moments.SHOTS_MAX) else 1 for s in segments]
        for segment, share in zip(segments, moments.allocated([s["weight"] for s in segments], shots, seconds),
                                  strict=True):
            segment["seconds"] = share
    return {"segments": segments, "skipped": [], "span": "probe", "seconds": seconds, "style": style,
            "margin": False, "edges": True, "look": False, "pace": "picture"}
