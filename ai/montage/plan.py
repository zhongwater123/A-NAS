"""What the planning model is given to plan a montage, and how its plan becomes a cut (#350).

One request carries all the model needs, and nothing else: a system prompt of the rules, and a
message of the instruction, the cut's seconds, its style and the catalog of cards (moments.Index
catalog with scenes and the seconds each card can run, without the cards text is laid over).
The model answers with one JSON object, a story line and the segments, each a card with its
scene, what it shows, its pace and why it was chosen; the program sets every segment's seconds
from that (moments.Index.fill). A plan the video service refuses goes back with its problems, in
the same conversation, for the whole plan again.
"""

import json
import re

from montage import moments
from montage.styles import STYLES

_PACES = "，".join(f"{name} {low:g}–{high:g} 秒" for name, (low, _aim, high) in moments.PACES.items())

SYSTEM = (
    "你是剪辑规划。按成员的指令，从素材卡片里选段、排顺序，交一份剪辑计划；画面由程序按计划剪出，带原声，不加配乐和字幕，末尾淡出到黑。\n"
    "素材目录每行一张卡片：“卡片 id 场景N 可剪X秒 光线 描述（亮点）毛病”。卡片 id 是“视频-镜头序号-段”，序号相邻的多半是同一场戏；"
    "描述由本机小模型看三帧写成，可能看错。scenes 写每个场景是什么地方、有几张卡片，“可接”是色调明暗和地点都相近、可以直接切过去的场景。\n"
    "先写故事线：按指令要的效果，把成片的段落按播放顺序列出来，每段一句内容和对应的场景、卡片。只放讲得进故事线的内容。\n"
    "排段的规则：\n"
    "1. 每段写 card、scene、shows、pace、reason。scene 是这张卡片在目录里的场景号，照目录写；"
    "shows 用一句话写这段要呈现的、画面里看得见的东西，不写地名、年代、情绪，程序按它在卡片里定入出点；"
    "reason 是选镜理由：为什么选这张卡片，它在故事线里起什么作用。\n"
    f"2. pace 是节奏：{_PACES}。慢用来交代地点、铺垫情绪、收尾，中用来交代人物和情节，快用来冲突、打斗、追逐，可以几段连着快切。"
    f"最后一段是收尾，用慢或{moments.ENDING_PACE}，写成快程序会改成{moments.ENDING_PACE}。"
    "程序按 pace 给每段分配长短，再一起缩放到目标时长；卡片可剪的秒数不够这段 pace 的下限时，程序把它改成放得下的快一档。\n"
    f"3. 同一场景的段连在一起，离开一个场景后不再回去；换场景只能换到上一个场景“可接”里的场景。一个场景最多 {moments.SCENE_SEGMENTS} 段，"
    f"快节奏的段另算，最多 {moments.FAST_SEGMENTS} 段。过渡场景 1–2 段就够，不必每个场景都用满。\n"
    f"4. 各段卡片的“可剪”秒数加起来（超过这段 pace 上限的按上限算）至少要到目标时长的 {moments.MARGIN:g} 倍，程序才有余量定每段的长短。\n"
    "只输出一个 JSON 对象，不要别的文字："
    '{"story": "故事线", "segments": [{"card": "卡片 id", "scene": 场景号, "shows": "画面", "pace": "慢|中|快", "reason": "选镜理由"}]}。'
    "计划不合规则会被退回并说明原因，照着改好后再输出完整的新计划。"
)


FIELDS = ("card", "scene", "shows", "pace", "reason")


def message(index: moments.Index, instruction: str, seconds: float, style: str | None) -> str:
    """The request's one message: the instruction, the seconds, the style's rules and the catalog."""
    catalog = index.catalog(times=False, scenes=True, lengths=True, titles=False)
    lines = [f"指令：{instruction}", f"目标时长：约 {seconds:g} 秒"]
    if style:
        lines.append(f"体裁：{style}。剪法：{STYLES[style]}")
    lines.append("场景：")
    lines += [f"场景{n}：{name}" for n, name in catalog.get("scenes", {}).items()]
    for video, entry in catalog["videos"].items():
        lines.append(f"视频 {video}（{entry['seconds']:g} 秒）的卡片：")
        lines += entry["cards"]
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


def misplaced(plan: dict, index: moments.Index) -> list[str]:
    """The segments whose scene is not their card's scene in the catalog (the model misread it)."""
    return [f"第 {n} 段 {s.get('card')} 写的是场景{s.get('scene')}，目录里是场景{index.scene_of.get(s.get('card'))}"
            for n, s in enumerate(plan["segments"], 1)
            if str(s.get("scene")) != str(index.scene_of.get(s.get("card")))]


def request(plan: dict, seconds: float, style: str | None) -> dict:
    """The video service's /montage body for the plan: each segment located by its shows,
    its seconds set to add up to seconds by its pace, its ends kept off cuts, no frames looked at."""
    segments = [{k: s.get(k) for k in FIELDS} | {"probe": s.get("shows", "")}
                for s in plan["segments"]]
    return {"segments": segments, "skipped": [], "span": "probe", "seconds": seconds, "style": style,
            "margin": True, "edges": True, "look": False, "scenes": True}
