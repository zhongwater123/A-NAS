"""A card described by the local Qwen3.5-4B (llama-server with its vision projector): the key frames numbered
with their seconds into the card; the answer one beat a key frame (shot size, what is seen), the process (what
happens, in order), a summary and the scene, after where the camera is. Those are all the plan reads (cut.py). The travel-vlog profile
and the helpers are 方案 B's (tag_split.py and travel-vlog.prompt.md there).

2026-11-13 (the user): the fields nothing read are no longer asked for: mood, tags, landmark, indoor or
outdoor, time of day, issues, the 1-5 highlight score and the 1-2 highlight beats. Tags had made the plan
worse in the 2026-11-08 blind test and retrieval kept to the frame vectors (#419); the highlight score and
beats hardly agreed with the user's marks (docs/research/video-montage-highlight.md); issues said 无明显问题
for every DJI card (quality.py). Descriptions written before keep those fields; nothing reads them.
"""
import base64
import json
import time
from pathlib import Path
from types import SimpleNamespace

import requests

TIMEOUT_SEC = 600
# Re-asked with a stronger repeat penalty when the model loops or the answer fails the schema.
REPEAT_PENALTIES = (1.1, 1.2, 1.3)
SHOT_SIZES = ["远景", "全景", "中景", "近景", "特写"]
SYSTEM_PROMPT = (
    "你是视频剪辑助理，负责描述素材片段。只根据画面内容回答，"
    "不要猜测画面里看不到的信息。所有文字用简体中文。"
)
# 方案 B's travel-vlog profile: what to look at.
TRAVEL = SimpleNamespace(
    key="travel-vlog", name="旅行 vlog",
    focus=("- 地点：每个画面在哪里。\n"
           "- 旅程感：出发、在路上、到达、交通工具、行李、地图和路牌。\n"
           "- 人物：出镜的人在做什么，表情和互动。\n"
           "- 亮点：景点的标志性画面、夜景、特别的体验。"))


class TagError(RuntimeError):
    """The answer does not satisfy the schema."""


def image_part(path):
    data = base64.b64encode(Path(path).read_bytes()).decode("ascii")
    return {"type": "image_url", "image_url": {"url": f"data:image/jpeg;base64,{data}"}}


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
# (5 of 6), its summary then a boat trip. Data: 剪辑对比/描述随机性（2026-11-16）/.
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


def schema(count, process=True):
    short = lambda n: {"type": "string", "minLength": 1, "maxLength": n}  # noqa: E731
    beats = {"type": "array", "minItems": count, "maxItems": count, "items": {"type": "object", "properties": {
        "shot_size": {"type": "string", "enum": SHOT_SIZES}, "what": short(45)}, "required": ["shot_size", "what"]}}
    steps = {"process": {"type": "array", "minItems": 1, "maxItems": count, "items": {"type": "string", "maxLength": 20}}} if process else {}
    props = {"camera": {"type": "string", "enum": CAMERAS}, "beats": beats, **steps, "summary": short(SUMMARY_CHARS + 20), "scene": short(20)}
    return {"type": "object", "properties": props, "required": list(props)}


def describe_v2(frames, offsets, groups, duration, url, profile=None, process=True):
    """(answer, seconds, errors): one beat a group of frames (groups: each frame's group from 1; shot size,
    what is seen), written first, then the process, then the summary and the scene.
    profile: the kind of material (TRAVEL by default)."""
    profile = profile or TRAVEL
    count = max(groups)
    spans = [[k for k, g in enumerate(groups, 1) if g == n] for n in range(1, count + 1)]
    layout = "；".join(f"第 {n} 组：第 {s[0]}–{s[-1]} 张" if len(s) > 1 else f"第 {n} 组：第 {s[0]} 张"
                      for n, s in enumerate(spans, 1))
    text = PROMPT.format(count=len(frames), duration=duration, process=PROCESS if process else "",
                         before="process" if process else "beats", chars=SUMMARY_CHARS, name=profile.name, focus=profile.focus)
    text += "\n" + GROUPED.format(groups=count, layout=layout)
    content = [{"type": "text", "text": text}]
    for k, (f, s) in enumerate(zip(frames, offsets, strict=True), 1):
        content += [{"type": "text", "text": f"第 {k} 张（第 {groups[k - 1]} 组，第 {s:.0f} 秒）"}, image_part(Path(f))]
    sch = schema(count, process)
    errors, total = [], 0.0
    for penalty in REPEAT_PENALTIES:
        body = {"messages": [{"role": "system", "content": SYSTEM_PROMPT}, {"role": "user", "content": content}],
                "temperature": 0.2, "repeat_penalty": penalty, "max_tokens": 1800,
                "chat_template_kwargs": {"enable_thinking": False},
                "response_format": {"type": "json_schema", "json_schema": {"name": "card_beats", "schema": sch}}}
        t0 = time.perf_counter()
        resp = requests.post(f"{url}/v1/chat/completions", json=body, timeout=TIMEOUT_SEC)
        total += time.perf_counter() - t0
        resp.raise_for_status()
        choice = resp.json()["choices"][0]
        try:
            answer = json.loads(choice["message"]["content"])
            if len(answer["beats"]) != count:
                raise TagError(f"{len(answer['beats'])} beats for {count} groups")
            for n, b in enumerate(answer["beats"]):
                b["frames"] = [spans[n][0], spans[n][-1]]
            return answer, total, errors
        except (ValueError, KeyError, TagError) as error:
            errors.append(f"repeat_penalty={penalty}: {error}")
    raise TagError("; ".join(errors))
