"""A card described by the local Qwen3.5-4B (llama-server with its vision projector): the key frames numbered
with their seconds into the card; the answer one beat a key frame (shot size, what is seen), the 1-2 beats
that stand out, the process (what happens, in order), a summary, and the card-wide fields (scene, indoor or
outdoor, time of day, mood, landmark, tags, issues). The highlight score is read from the log-probabilities
of the digit written (G-Eval), not the integer itself. Helpers and the travel-vlog profile are 方案 B's
(tag_split.py and travel-vlog.prompt.md there).
"""
import base64
import json
import math
import re
import time
from pathlib import Path
from types import SimpleNamespace

import requests

TIMEOUT_SEC = 600
HIGHLIGHT_DIGITS = (1, 2, 3, 4, 5)
TOP_LOGPROBS = 10
_HIGHLIGHT_KEY = re.compile(r'"highlight"\s*:\s*$')
# Re-asked with a stronger repeat penalty when the model loops or the answer fails the schema.
REPEAT_PENALTIES = (1.1, 1.2, 1.3)
SHOT_SIZES = ["远景", "全景", "中景", "近景", "特写"]
TIMES_OF_DAY = ["白天", "黄昏", "夜晚", "不确定"]
PLACES = ["室内", "室外", "看不出"]
ISSUES = ["模糊", "过暗", "过曝", "抖动", "遮挡", "无明显问题"]
SYSTEM_PROMPT = (
    "你是视频剪辑助理，负责给素材片段打标签。只根据画面内容回答，"
    "不要猜测画面里看不到的信息。所有文字用简体中文。"
)
# 方案 B's travel-vlog profile: what to look at and how to score the highlight.
TRAVEL = SimpleNamespace(
    key="travel-vlog", name="旅行 vlog",
    focus=("- 地点：每个画面在哪里，能确认的城市、景点、地标写进 landmark，\n  认不出就留空。\n"
           "- 旅程感：出发、在路上、到达、交通工具、行李、地图和路牌。\n"
           "- 人物：出镜的人在做什么，表情和互动。\n"
           "- 亮点：景点的标志性画面、夜景、特别的体验。\n"
           "- 镜头质量：稳不稳、曝光是否正常、有没有被挡。"),
    rubric=("- 5：这段旅程最值得记住的画面。标志性景点的好镜头、壮观的夜景或风景、\n  人物最开心的瞬间。\n"
            "- 4：有地点特色的好镜头。一看就知道在哪，主体清楚、画面稳定。\n"
            "- 3：普通可用。交代了地点或过程，但不出彩，适合做过渡。\n"
            "- 2：平淡或有瑕疵。看不出在哪、主体不清、晃动或过暗。\n"
            "- 1：不可用。严重模糊、黑屏或被遮挡。"))


class TagError(RuntimeError):
    """The answer does not satisfy the schema."""


def image_part(path):
    data = base64.b64encode(Path(path).read_bytes()).decode("ascii")
    return {"type": "image_url", "image_url": {"url": f"data:image/jpeg;base64,{data}"}}


def highlight_score(top):
    """{"highlight_p": mean, "highlight_probs": {digit: p}} from the top log-probabilities where the
    highlight digit is written, renormalised over HIGHLIGHT_DIGITS."""
    probs = dict.fromkeys(HIGHLIGHT_DIGITS, 0.0)
    for alt in top:
        token = alt["token"].strip()
        if token.isdigit() and int(token) in probs:
            probs[int(token)] += math.exp(alt["logprob"])
    total = sum(probs.values())
    if total <= 0:
        raise TagError(f"no highlight digit among the top tokens: {top}")
    shares = {d: p / total for d, p in probs.items()}
    return {"highlight_p": round(sum(d * p for d, p in shares.items()), 3),
            "highlight_probs": {str(d): round(p, 4) for d, p in shares.items()}}


def _highlight_top(tokens):
    """The top log-probabilities of the digit written after "highlight": in the answer's token stream."""
    text = ""
    for t in tokens:
        if t["token"].strip().isdigit() and _HIGHLIGHT_KEY.search(text):
            return t["top_logprobs"]
        text += t["token"]
    raise TagError("the answer has no highlight digit to read probabilities from")


PROMPT = (
    "下面 {count} 张图按时间先后取自同一段视频（共 {duration:.0f} 秒），每张图前标了编号和它在这段里的秒数。"
    "请输出 JSON：\n"
    "summary：一句话概括这一段，不超过 40 字；\n"
    "beats：按时间顺序把这一段分成几个节拍：主体、景别或地点明显变了就分开，同样的画面合成一个；"
    "每个节拍写 frames（起止图片编号，如 [3, 5]，只有一张就写 [4, 4]）、shot_size（景别）、"
    "what（看得见的主体和动作，不超过 30 字，画面里的关键人物和物件都要写到）；\n"
    "scene 是地点；place 是室内还是室外：看得到天空、街道、山水草木是室外，四周是墙、天花板、家具或车厢是室内，"
    "看不出填看不出；time_of_day 是时段，看天空和光源判断：天空亮、有阳光是白天，太阳贴近地平线、天边发红发橙才是黄昏，"
    "天空黑、靠灯光照亮是夜晚，逆光不等于黄昏；室内看窗外的光判断，看不到窗外或看不到天空就填不确定；"
    "mood 是氛围；landmark 是画面里能确认的地标或地名，认不出就填空字符串；tags 是 3-8 个检索标签；"
    "highlight 是作为短视频素材的精彩程度 1-5；issues 是画面问题，没有问题填 无明显问题。"
    "片头片尾字幕、制作人员名单、只有文字或黑屏的画面，what 要写明是字幕、文字或黑屏。\n"
    "这类素材是「{name}」。看片时注意：\n{focus}\n"
    "最后按下面的标准给 highlight 打分：\n{rubric}\n"
    "一段素材里多数是 3 分，4 分是少数好镜头，5 分很少，平淡或有瑕疵的给 2 分；对照标准逐档判断，不要都给中间分。"
)


def schema(count):
    short = lambda n: {"type": "string", "minLength": 1, "maxLength": n}  # noqa: E731
    return {"type": "object", "properties": {
        "summary": short(60),
        "beats": {"type": "array", "minItems": 1, "maxItems": count, "items": {"type": "object", "properties": {
            "frames": {"type": "array", "items": {"type": "integer", "minimum": 1, "maximum": count}, "minItems": 2, "maxItems": 2},
            "shot_size": {"type": "string", "enum": SHOT_SIZES},
            "what": short(45)}, "required": ["frames", "shot_size", "what"]}},
        "scene": short(20), "place": {"type": "string", "enum": PLACES},
        "time_of_day": {"type": "string", "enum": TIMES_OF_DAY}, "mood": short(20),
        "landmark": {"type": "string", "maxLength": 20},
        "tags": {"type": "array", "items": {"type": "string", "maxLength": 12}, "minItems": 3, "maxItems": 8},
        "highlight": {"type": "integer", "minimum": 1, "maximum": 5},
        "issues": {"type": "array", "items": {"type": "string", "enum": ISSUES}}},
        "required": ["summary", "beats", "scene", "place", "time_of_day", "mood", "landmark", "tags", "highlight", "issues"]}


# One beat a key frame. 2026-11-10 (景别调研): the five shot sizes defined, and shot_size and what read from
# that one frame alone, not with a person seen in another frame.
GROUPED = (
    "这些图是程序在画面变化处取的关键帧，共 {groups} 张，每张是一个节拍，按时间先后排列：{layout}。"
    "beats 按图写，每张图正好一项，顺序和图一致。"
    "shot_size 只看这一张图判断，看画面的主角是环境还是人或物：远景是环境是主角，人很小或没有人（大片风景、城市天际线）；"
    "全景是人和环境都能看得到，人在环境里活动，背后的山、湖、街道仍占画面大部分；中景是人或物成了主角，环境退成背景；"
    "近景是人的上半身或一件物品占满大部分画面；特写是脸、手或物件的局部填满画面。"
    "what 只写这一张图里看得见的主体和动作，不超过 30 字。")


# #416 (2026-11-08 evening): the summary written last. Asked first, it was written before the frames
# were gone through and left out what only some of them show: 01-013's puppet, 01-014's woman in white
# (order_exp.py). The beats now come first, then the 1-2 beats that stand out (highlights, chosen side
# by side in the same call: each frame rated alone came out 4 of 5 almost every time), then the summary.
SUMMARY_FIRST = "summary：一句话概括这一段，不超过 40 字；\n"
HIGHLIGHTS_AFTER = ("highlights：写完 beats 再写，挑出这一段里最有记忆点、最值得剪进成片的 {most} 个节拍"
                    "（特别的人物、道具、表情或光影），写组号；\n"
                    "summary：写完 highlights 再写，按 beats 一句话概括这一段，不超过 40 字，beats 里的关键人物和物件要写到；\n")


SUMMARY_CHARS = 80   # a vlog card holds more than a film's: 40 characters left it out


PROCESS_AFTER = ("process：写完 highlights 再写，按 beats 的先后，用短语写这一段里依次发生的事，"
                 "短语个数不超过节拍数，每个短语不超过 10 个字，连着几个节拍是同一件事就合成一个短语；\n")


def describe_v2(frames, offsets, groups, duration, url, profile=None, process=True):
    """One beat a group of frames (groups: each frame's group from 1; shot size, what is seen), written
    first, then highlights (group numbers), then the summary.
    process (#416, 2026-11-09): a short ordered list of what happens, between highlights and summary.
    profile: the kind of material (TRAVEL by default)."""
    profile = profile or TRAVEL
    count = max(groups)
    chars = SUMMARY_CHARS
    most = 1 if count <= 3 else 2
    spans = [[k for k, g in enumerate(groups, 1) if g == n] for n in range(1, count + 1)]
    layout = "；".join(f"第 {n} 组：第 {s[0]}–{s[-1]} 张" if len(s) > 1 else f"第 {n} 组：第 {s[0]} 张"
                      for n, s in enumerate(spans, 1))
    text = PROMPT.format(count=len(frames), duration=duration, name=profile.name, focus=profile.focus, rubric=profile.rubric)
    text = text.replace(SUMMARY_FIRST, "")
    text = text.replace(
        "beats：按时间顺序把这一段分成几个节拍：主体、景别或地点明显变了就分开，同样的画面合成一个；"
        "每个节拍写 frames（起止图片编号，如 [3, 5]，只有一张就写 [4, 4]）、shot_size（景别）、"
        "what（看得见的主体和动作，不超过 30 字，画面里的关键人物和物件都要写到）；",
        "beats：见下面的分组说明；\n" + HIGHLIGHTS_AFTER.format(most="1" if most == 1 else "1–2"))
    if process:
        text = text.replace("summary：写完 highlights 再写", PROCESS_AFTER + "summary：写完 process 再写")
    text += "\n" + GROUPED.format(groups=count, layout=layout)
    text = text.replace("一句话概括这一段，不超过 40 字", f"一句话概括这一段，不超过 {chars} 字")
    content = [{"type": "text", "text": text}]
    for k, (f, s) in enumerate(zip(frames, offsets, strict=True), 1):
        content += [{"type": "text", "text": f"第 {k} 张（第 {groups[k - 1]} 组，第 {s:.0f} 秒）"}, image_part(Path(f))]
    sch = schema(len(frames))
    sch["properties"]["summary"]["maxLength"] = chars + 20
    beat = sch["properties"]["beats"]
    beat["minItems"] = beat["maxItems"] = count
    beat["items"]["properties"].pop("frames")
    beat["items"]["required"] = ["shot_size", "what"]
    props = sch["properties"]
    highlights = {"type": "array", "minItems": 1, "maxItems": most, "items": {"type": "integer", "minimum": 1, "maximum": count}}
    steps = {"process": {"type": "array", "minItems": 1, "maxItems": count, "items": {"type": "string", "maxLength": 20}}} if process else {}
    sch["properties"] = {"beats": props["beats"], "highlights": highlights, **steps, "summary": props["summary"],
                         **{k: v for k, v in props.items() if k not in ("beats", "summary")}}
    sch["required"] = list(sch["properties"])
    errors, total = [], 0.0
    for penalty in REPEAT_PENALTIES:
        body = {"messages": [{"role": "system", "content": SYSTEM_PROMPT}, {"role": "user", "content": content}],
                "temperature": 0.2, "repeat_penalty": penalty, "max_tokens": 1800, "logprobs": True,
                "top_logprobs": TOP_LOGPROBS, "chat_template_kwargs": {"enable_thinking": False},
                "response_format": {"type": "json_schema", "json_schema": {"name": "card_beats", "schema": sch}}}
        t0 = time.perf_counter()
        resp = requests.post(f"{url}/v1/chat/completions", json=body, timeout=TIMEOUT_SEC)
        total += time.perf_counter() - t0
        resp.raise_for_status()
        choice = resp.json()["choices"][0]
        try:
            answer = json.loads(choice["message"]["content"])
            for n, b in enumerate(answer["beats"]):
                b["frames"] = [spans[n][0], spans[n][-1]]
            answer["highlights"] = sorted({h for h in answer["highlights"] if 1 <= h <= count})
            score = highlight_score(_highlight_top(choice["logprobs"]["content"]))
            return answer, score, total, errors
        except (ValueError, IndexError, TagError) as error:
            errors.append(f"repeat_penalty={penalty}: {error}")
    raise TagError("; ".join(errors))
