"""#416: a card described as beats (节拍) by a local vision model: the frames numbered with their
seconds into the card, the answer a summary, the beats in time order (which frames, shot size,
what is seen), and the card-wide fields. Highlight read by its log-probabilities (方案 B's way).
Helpers (image_part, highlight_score, ...) are b/tag_split.py's."""
import importlib.util
import json
import time
from pathlib import Path

import requests
from paths import B_DIR as B

spec = importlib.util.spec_from_file_location("tag_split", B / "tag_split.py")
ts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ts)

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
            "shot_size": {"type": "string", "enum": ts.SHOT_SIZES},
            "what": short(45)}, "required": ["frames", "shot_size", "what"]}},
        "scene": short(20), "place": {"type": "string", "enum": ts.PLACES},
        "time_of_day": {"type": "string", "enum": ts.TIMES_OF_DAY}, "mood": short(20),
        "landmark": {"type": "string", "maxLength": 20},
        "tags": {"type": "array", "items": {"type": "string", "maxLength": 12}, "minItems": 3, "maxItems": 8},
        "highlight": {"type": "integer", "minimum": 1, "maximum": 5},
        "issues": {"type": "array", "items": {"type": "string", "enum": ts.ISSUES}}},
        "required": ["summary", "beats", "scene", "place", "time_of_day", "mood", "landmark", "tags", "highlight", "issues"]}


def describe(frames, offsets, duration, url, profile):
    """(answer, highlight score, seconds, errors): frames are image paths, offsets their seconds into the card."""
    content = [{"type": "text", "text": PROMPT.format(count=len(frames), duration=duration, name=profile.name,
                                                      focus=profile.focus, rubric=profile.rubric)}]
    for k, (f, s) in enumerate(zip(frames, offsets, strict=True), 1):
        content += [{"type": "text", "text": f"第 {k} 张（第 {s:.0f} 秒）"}, ts.image_part(Path(f))]
    errors, total = [], 0.0
    for penalty in ts.REPEAT_PENALTIES:
        body = {"messages": [{"role": "system", "content": ts.SYSTEM_PROMPT}, {"role": "user", "content": content}],
                "temperature": 0.2, "repeat_penalty": penalty, "max_tokens": 1500, "logprobs": True,
                "top_logprobs": ts.TOP_LOGPROBS, "chat_template_kwargs": {"enable_thinking": False},
                "response_format": {"type": "json_schema", "json_schema": {"name": "card_beats", "schema": schema(len(frames))}}}
        t0 = time.perf_counter()
        resp = requests.post(f"{url}/v1/chat/completions", json=body, timeout=ts.TIMEOUT_SEC)
        total += time.perf_counter() - t0
        resp.raise_for_status()
        choice = resp.json()["choices"][0]
        try:
            answer = json.loads(choice["message"]["content"])
            score = ts.highlight_score(ts._highlight_top(choice["logprobs"]["content"]))
            return answer, score, total, errors
        except (ValueError, ts.TagError) as error:
            errors.append(f"repeat_penalty={penalty}: {error}")
    raise ts.TagError("; ".join(errors))


GROUPED = (
    "这些图已经由程序按画面分成 {groups} 组，同一组的画面相近，组按时间先后排列：{layout}。"
    "beats 按组写，每组正好一项，顺序和组一致：shot_size 是这一组的景别，"
    "what 是这一组看得见的主体和动作（不超过 30 字，画面里的关键人物和物件都要写到，只有一张的组往往是一闪而过的插入画面，也要写清）。"
)


def describe_groups(frames, offsets, groups, duration, url, profile):
    """As describe, but the program has grouped the frames (groups: each frame's group from 1) and the
    model writes one beat a group: its shot size and what is seen."""
    count = max(groups)
    spans = [[k for k, g in enumerate(groups, 1) if g == n] for n in range(1, count + 1)]
    layout = "；".join(f"第 {n} 组：第 {s[0]}–{s[-1]} 张" if len(s) > 1 else f"第 {n} 组：第 {s[0]} 张"
                      for n, s in enumerate(spans, 1))
    text = PROMPT.format(count=len(frames), duration=duration, name=profile.name, focus=profile.focus, rubric=profile.rubric)
    text = text.replace(
        "beats：按时间顺序把这一段分成几个节拍：主体、景别或地点明显变了就分开，同样的画面合成一个；"
        "每个节拍写 frames（起止图片编号，如 [3, 5]，只有一张就写 [4, 4]）、shot_size（景别）、"
        "what（看得见的主体和动作，不超过 30 字，画面里的关键人物和物件都要写到）；",
        "beats：见下面的分组说明；") + "\n" + GROUPED.format(groups=count, layout=layout)
    content = [{"type": "text", "text": text}]
    for k, (f, s) in enumerate(zip(frames, offsets, strict=True), 1):
        content += [{"type": "text", "text": f"第 {k} 张（第 {groups[k - 1]} 组，第 {s:.0f} 秒）"}, ts.image_part(Path(f))]
    sch = schema(len(frames))
    beat = sch["properties"]["beats"]
    beat["minItems"] = beat["maxItems"] = count
    beat["items"]["properties"].pop("frames")
    beat["items"]["required"] = ["shot_size", "what"]
    errors, total = [], 0.0
    for penalty in ts.REPEAT_PENALTIES:
        body = {"messages": [{"role": "system", "content": ts.SYSTEM_PROMPT}, {"role": "user", "content": content}],
                "temperature": 0.2, "repeat_penalty": penalty, "max_tokens": 1800, "logprobs": True,
                "top_logprobs": ts.TOP_LOGPROBS, "chat_template_kwargs": {"enable_thinking": False},
                "response_format": {"type": "json_schema", "json_schema": {"name": "card_beats", "schema": sch}}}
        t0 = time.perf_counter()
        resp = requests.post(f"{url}/v1/chat/completions", json=body, timeout=ts.TIMEOUT_SEC)
        total += time.perf_counter() - t0
        resp.raise_for_status()
        choice = resp.json()["choices"][0]
        try:
            answer = json.loads(choice["message"]["content"])
            for n, b in enumerate(answer["beats"]):
                b["frames"] = [spans[n][0], spans[n][-1]]
            score = ts.highlight_score(ts._highlight_top(choice["logprobs"]["content"]))
            return answer, score, total, errors
        except (ValueError, IndexError, ts.TagError) as error:
            errors.append(f"repeat_penalty={penalty}: {error}")
    raise ts.TagError("; ".join(errors))


# #416 (2026-11-08 evening): the summary written last. Asked first, it was written before the frames
# were gone through and left out what only some of them show: 01-013's puppet, 01-014's woman in white
# (order_exp.py). The beats now come first, then the 1-2 beats that stand out (highlights, chosen side
# by side in the same call: each frame rated alone came out 4 of 5 almost every time), then the summary.
SUMMARY_FIRST = "summary：一句话概括这一段，不超过 40 字；\n"
HIGHLIGHTS_AFTER = ("highlights：写完 beats 再写，挑出这一段里最有记忆点、最值得剪进成片的 {most} 个节拍"
                    "（特别的人物、道具、表情或光影），写组号；\n"
                    "summary：写完 highlights 再写，按 beats 一句话概括这一段，不超过 40 字，beats 里的关键人物和物件要写到；\n")


SUMMARY_CHARS = {"travel-vlog": 80}   # a vlog card holds more than a film's: 40 characters left it out


PROCESS_AFTER = ("process：写完 highlights 再写，按 beats 的先后，用短语写这一段里依次发生的事，"
                 "短语个数不超过节拍数，每个短语不超过 10 个字，连着几个节拍是同一件事就合成一个短语；\n")


def describe_v2(frames, offsets, groups, duration, url, profile, process=False):
    """As describe_groups, with the beats written first, then highlights (group numbers), then the summary.
    process=True (#416, 2026-11-09): a short ordered list of what happens, between highlights and summary."""
    count = max(groups)
    chars = SUMMARY_CHARS.get(getattr(profile, "key", ""), 40)
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
        content += [{"type": "text", "text": f"第 {k} 张（第 {groups[k - 1]} 组，第 {s:.0f} 秒）"}, ts.image_part(Path(f))]
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
    for penalty in ts.REPEAT_PENALTIES:
        body = {"messages": [{"role": "system", "content": ts.SYSTEM_PROMPT}, {"role": "user", "content": content}],
                "temperature": 0.2, "repeat_penalty": penalty, "max_tokens": 1800, "logprobs": True,
                "top_logprobs": ts.TOP_LOGPROBS, "chat_template_kwargs": {"enable_thinking": False},
                "response_format": {"type": "json_schema", "json_schema": {"name": "card_beats", "schema": sch}}}
        t0 = time.perf_counter()
        resp = requests.post(f"{url}/v1/chat/completions", json=body, timeout=ts.TIMEOUT_SEC)
        total += time.perf_counter() - t0
        resp.raise_for_status()
        choice = resp.json()["choices"][0]
        try:
            answer = json.loads(choice["message"]["content"])
            for n, b in enumerate(answer["beats"]):
                b["frames"] = [spans[n][0], spans[n][-1]]
            answer["highlights"] = sorted({h for h in answer["highlights"] if 1 <= h <= count})
            score = ts.highlight_score(ts._highlight_top(choice["logprobs"]["content"]))
            return answer, score, total, errors
        except (ValueError, IndexError, ts.TagError) as error:
            errors.append(f"repeat_penalty={penalty}: {error}")
    raise ts.TagError("; ".join(errors))
