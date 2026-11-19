"""Describes a montage card from its key frames (docs/architecture/photo-ai.md, 剪辑中心的分析).

The prompts, the genre and the schema are the prototype's, taken unchanged
from commit 19f2d6db (ai/montage_v8/domain/prompts.py and describe.py) with
one sentence added: not to copy the numbers the frames show. Their version is
TEMPLATE; any change to them is another template, and so another analysis.
The caller sends frames, never a prompt.

The reader's context holds the prompt and the answer. A card with more key
frames than leave the answer its MAX_TOKENS is described from some of them
(fitting, numbered; the reader counts, anas_ai.providers.LlamaReader.describe).
"""

import itertools
import json
import re
import sys

TEMPLATE = "montage-card/v1"
# The genre is fixed: the analysis is shared across projects and members.
GENRE = "TRAVEL"
FEWEST_FRAMES, MOST_FRAMES = 3, 20
# The answer's token budget, as the prototype's (adapters/vision.py).
MAX_TOKENS = 1800
TEMPERATURE = 0.2
# An answer that repeats itself or does not fit the schema is asked again
# with a stronger repeat penalty: the prototype's three tries.
REPEAT_PENALTIES = (1.1, 1.2, 1.3)

# --- The prototype's prompt data (ai/montage_v8/domain/prompts.py at 19f2d6db), unchanged. ---

# 卡片描述（本机视觉模型）。
SHOT_SIZES = ["远景", "全景", "中景", "近景", "特写"]
SYSTEM_PROMPT = (
    "你是视频剪辑助理，负责描述素材片段。只根据画面内容回答，"
    "不要猜测画面里看不到的信息。所有文字用简体中文。"
)
# 方案 B's travel-vlog profile: what to look at.
TRAVEL = {
    "key": "travel-vlog", "name": "旅行 vlog",
    "focus": ("- 地点：每个画面在哪里。\n"
           "- 旅程感：出发、在路上、到达、交通工具、行李、地图和路牌。\n"
           "- 人物：出镜的人在做什么，表情和互动。\n"
           "- 亮点：景点的标志性画面、夜景、特别的体验。")}


SUMMARY_CHARS = 80   # a vlog card holds more than a film's: 40 characters left it out

# #416 (2026-11-08 evening): the summary written last. Asked first, it was written before the frames
# were gone through and left out what only some of them show: 01-013's puppet, 01-014's woman in white
# (order_exp.py). process (#416, 2026-11-09): a short ordered list of what happens, between the beats and
# the summary.
# camera (2026-11-16): where the camera is, asked first. Without the old prompt's indoor-or-outdoor question
# ("车厢是室内"), the 4B wrote boats out of the cruise and whale-watch cards and took pj-038's fjord, filmed
# from a boat at the water's level, for a drone; the plan then put it among the flight's shots. Asked from what
# is seen (three wordings tried on 12 cards, three runs each): every boat card 船上, the helicopter's 飞机或直升机里
# (they had been drones), no drone written anywhere; but a lake seen from the shore is still taken for a boat
# (5 of 6), its summary then a boat trip. Data: 剪辑对比/描述随机性（2026-11-16）/. 室内 (2026-11-16, Insta360):
# a breakfast shot through a restaurant's window had been 地面.
CAMERAS = ["地面", "室内", "车里", "船上", "飞机或直升机里", "无人机航拍", "水里", "看不出"]
PROMPT = (
    "下面 {count} 张图按时间先后取自同一段视频（共 {duration:.0f} 秒），每张图前标了编号和它在这段里的秒数。"
    "请输出 JSON：\n"
    "camera：先判断这一段是在哪里拍的：地面（室外）、室内（房间、餐厅、商店里）、车里、船上、飞机或直升机里、无人机航拍、水里，看不出写看不出。"
    "只按画面里看得见的东西判断：看得见车窗、车头、座椅是车里，在房间、餐厅、商店里隔着窗户拍是室内；看得见舷窗、窗框、玻璃反光或桨叶的影子是飞机或直升机里；"
    "看得见船舷、船头、甲板、栏杆或同船的人是船上；看得见岸、路、栈桥或站在地上的脚下是地面；"
    "只看到水面、山和天空，看不到这些的写看不出，不要猜；看不出时 summary 和 beats 也不要写在船上或航拍；\n"
    "beats：见下面的分组说明；\n"
    "{process}"
    "summary：写完 {before} 再写，按 beats 一句话概括这一段，不超过 {chars} 字，beats 里的关键人物和物件要写到；\n"
    # 2026-11-16: with camera asked first, the 4B copied it into scene (地面, 车里) for 51 of 品杰's 116 cards
    # and 25 of Insta360's 33.
    "scene 是这一段所在的地方，写具体的地点或环境（如街道、餐厅里、教堂前、湖边、峡湾），不要写机位（地面、车里、船上）。"
    "片头片尾字幕、制作人员名单、只有文字或黑屏的画面，what 要写明是字幕、文字或黑屏。\n"
    "这类素材是「{name}」。看片时注意：\n{focus}\n"
)
PROCESS = ("process：写完 beats 再写，按 beats 的先后，用短语写这一段里依次发生的事，"
           "短语个数不超过节拍数，每个短语不超过 10 个字，连着几个节拍是同一件事就合成一个短语；\n")

# One beat a key frame. 2026-11-10 (景别调研): the five shot sizes defined, and shot_size and what read from
# that one frame alone, not with a person seen in another frame.
GROUPED = (
    "这些图是程序在画面变化处取的关键帧，共 {groups} 张，每张是一个节拍，按时间先后排列：{layout}。"
    "beats 按图写，每张图正好一项，顺序和图一致。"
    "shot_size 只看这一张图判断，看画面的主角是环境还是人或物：远景是环境是主角，人很小或没有人（大片风景、城市天际线）；"
    "全景是人和环境都能看得到，人在环境里活动，背后的山、湖、街道仍占画面大部分；中景是人或物成了主角，环境退成背景；"
    "近景是人的上半身或一件物品占满大部分画面；特写是脸、手或物件的局部填满画面。"
    "what 只写这一张图里看得见的主体和动作，不超过 30 字。")

# --- What montage-card/v1 adds. ---

# Decision 11 of docs/decisions/2026-11-17-montage-center-first-version.md:
# the description reaches a member's assistant, so it does not copy numbers.
# The photo library masks the numbers its rules know before storing it; this
# sentence is all that keeps out plates and short ticket numbers.
NO_NUMBERS = "画面里的证件号、票号、车牌等号码不要转写，写成“号码”即可。"
CARD_PROMPT = PROMPT.replace("what 要写明是字幕、文字或黑屏。\n", "what 要写明是字幕、文字或黑屏。" + NO_NUMBERS + "\n")
assert CARD_PROMPT != PROMPT  # noqa: S101  the sentence must have found its place
PROFILES = {"TRAVEL": TRAVEL}


class InvalidAnswer(ValueError):
    """The answer is not JSON, or does not fit the schema."""


def schema(count, process=True):
    """The answer's JSON Schema for count beats (the prototype's describe.schema)."""

    def short(n):
        return {"type": "string", "minLength": 1, "maxLength": n}

    beats = {"type": "array", "minItems": count, "maxItems": count, "items": {"type": "object", "properties": {
        "shot_size": {"type": "string", "enum": SHOT_SIZES}, "what": short(45)}, "required": ["shot_size", "what"]}}
    steps = {"process": {"type": "array", "minItems": 1, "maxItems": count,
                         "items": {"type": "string", "maxLength": 20}}} if process else {}
    props = {"camera": {"type": "string", "enum": CAMERAS}, "beats": beats, **steps,
             "summary": short(SUMMARY_CHARS + 20), "scene": short(20)}
    return {"type": "object", "properties": props, "required": list(props)}


def request(offsets, groups, duration, process=True):
    """The prompt as (content, schema, spans).

    content holds ("text", text) and ("image", index) in order; spans are
    each beat's frames, counted from 1. The prototype's describe.request, with
    the frames given by their index among the request's descriptors instead
    of by path.
    """
    profile = PROFILES[GENRE]
    count = max(groups)
    spans = [[k for k, g in enumerate(groups, 1) if g == n] for n in range(1, count + 1)]
    layout = "；".join(f"第 {n} 组：第 {s[0]}–{s[-1]} 张" if len(s) > 1 else f"第 {n} 组：第 {s[0]} 张"
                      for n, s in enumerate(spans, 1))
    text = CARD_PROMPT.format(count=len(offsets), duration=duration, process=PROCESS if process else "",
                              before="process" if process else "beats", chars=SUMMARY_CHARS, name=profile["name"],
                              focus=profile["focus"])
    text += "\n" + GROUPED.format(groups=count, layout=layout)
    content = [("text", text)]
    for k, seconds in enumerate(offsets, 1):
        content += [("text", f"第 {k} 张（第 {groups[k - 1]} 组，第 {seconds:.0f} 秒）"), ("image", k - 1)]
    return content, schema(count, process), spans


def parse(text, answer_schema, spans):
    """The answer in text, checked against answer_schema; InvalidAnswer otherwise.

    As the prototype's describe.check, each beat gets "frames", the first
    and last of its key frames, counted from 1.
    """
    fenced = re.search(r"```(?:json)?\s*(.*?)```", text, re.DOTALL)
    try:
        answer = json.loads(fenced.group(1) if fenced else text)
    except ValueError as error:
        raise InvalidAnswer(f"not JSON: {error}") from error
    check(answer, answer_schema)
    if len(answer["beats"]) != len(spans):
        raise InvalidAnswer(f"{len(answer['beats'])} beats for {len(spans)} groups")
    for n, beat in enumerate(answer["beats"]):
        beat["frames"] = [spans[n][0], spans[n][-1]]
    return answer


def check(value, sub, where="answer"):
    """Raises InvalidAnswer where value breaks sub: required fields, types, item counts, lengths and enums.

    The prototype checked all but the lengths; llama-server's grammar keeps
    to them too, but an answer is checked here whatever produced it.
    """
    kind = sub.get("type")
    if kind == "object":
        if not isinstance(value, dict):
            raise InvalidAnswer(f"{where} is not an object")
        for key in sub.get("required", []):
            if key not in value:
                raise InvalidAnswer(f"{where} lacks {key}")
        for key, inner in sub.get("properties", {}).items():
            if key in value:
                check(value[key], inner, f"{where}.{key}")
    elif kind == "array":
        if not isinstance(value, list):
            raise InvalidAnswer(f"{where} is not an array")
        if not sub.get("minItems", 0) <= len(value) <= sub.get("maxItems", len(value)):
            raise InvalidAnswer(f"{where} has {len(value)} items")
        for n, item in enumerate(value):
            check(item, sub.get("items", {}), f"{where}[{n}]")
    elif kind == "string":
        if not isinstance(value, str):
            raise InvalidAnswer(f"{where} is not a string")
        if not sub.get("minLength", 0) <= len(value) <= sub.get("maxLength", len(value)):
            raise InvalidAnswer(f"{where} has {len(value)} characters")
        if "enum" in sub and value not in sub["enum"]:
            raise InvalidAnswer(f"{where} is not one of {sub['enum']}")


def fitting(offsets, groups, most):
    """The layout of the frames a card is described from when only most fit, as (kept, offsets, groups).

    A card with more key frames than fit is described from most of them,
    spread evenly from its first to its last; kept are their places among the
    card's frames, counted from 0. A beat that keeps none of its frames is
    left out, and the beats after it move up. A card that fits is unchanged.

    The NAS names this fitting in a description's identity (fit= in
    internal/photos/montagedescribe.go, describeIdentitySuffix): a change to
    which frames a card is described from, here or in how LlamaReader.describe
    counts what fits, bumps fit= there, or cards that failed under the old
    fitting are never described again.
    """
    count = len(offsets)
    if count <= most:
        return list(range(count)), offsets, groups
    kept = [(2 * k * (count - 1) + most - 1) // (2 * (most - 1)) for k in range(most)]
    beats = sorted({groups[i] for i in kept})
    return kept, [offsets[i] for i in kept], [beats.index(groups[i]) + 1 for i in kept]


def numbered(answer, kept):
    """answer with each beat's "frames" counted among the card's key frames, of which kept were asked about."""
    for beat in answer["beats"]:
        beat["frames"] = [kept[k - 1] + 1 for k in beat["frames"]]
    return answer


def valid_layout(offsets, groups, duration):
    """Whether offsets, groups and duration describe 3 to 20 frames of a card in order.

    offsets are seconds from the card's start, rising; groups count the beats
    from 1, each frame in its predecessor's beat or the next; duration is the
    card's seconds, not before the last frame.
    """
    def number(value):
        return type(value) in (int, float) and -sys.float_info.max <= value <= sys.float_info.max

    if not isinstance(offsets, list) or not isinstance(groups, list) or not number(duration):
        return False
    if not FEWEST_FRAMES <= len(offsets) <= MOST_FRAMES or len(groups) != len(offsets):
        return False
    if not all(number(value) for value in offsets) or not all(isinstance(g, int) and not isinstance(g, bool) for g in groups):
        return False
    if offsets[0] < 0 or any(b < a for a, b in itertools.pairwise(offsets)) or duration < offsets[-1] or duration <= 0:
        return False
    return groups[0] == 1 and all(b - a in (0, 1) for a, b in itertools.pairwise(groups))
