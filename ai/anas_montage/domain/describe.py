"""卡片描述的请求和回答（第六版 tag_beats.describe_v2）：先是机位，然后一张关键帧一个节拍（景别、画面），过程、概括和场景。

只产出请求的内容（文字和图片按顺序）与 JSON Schema；由 Describer 端口交给视觉模型（llama.cpp、RKLLM……）。
"""

from .prompts import CAMERAS, GROUPED, PROCESS, PROMPT, SHOT_SIZES, SUMMARY_CHARS, SYSTEM_PROMPT, TRAVEL

SYSTEM = SYSTEM_PROMPT


class DescribeError(RuntimeError):
    """回答不符合 schema。"""


def schema(count: int, process: bool = True) -> dict:
    def short(n):
        return {"type": "string", "minLength": 1, "maxLength": n}

    beats = {"type": "array", "minItems": count, "maxItems": count, "items": {"type": "object", "properties": {
        "shot_size": {"type": "string", "enum": SHOT_SIZES}, "what": short(45)}, "required": ["shot_size", "what"]}}
    steps = {"process": {"type": "array", "minItems": 1, "maxItems": count, "items": {"type": "string", "maxLength": 20}}} if process else {}
    props = {"camera": {"type": "string", "enum": CAMERAS}, "beats": beats, **steps, "summary": short(SUMMARY_CHARS + 20),
             "scene": short(20)}
    return {"type": "object", "properties": props, "required": list(props)}


def request(frames: list[str], offsets: list[float], groups: list[int], duration: float, profile: dict | None = None,
            process: bool = True) -> tuple[list, dict, list]:
    """(content, schema, spans)。content 是按顺序的 ("text", 文字) 和 ("image", 关键帧路径)；spans 是每个节拍的
    关键帧范围（从 1 数）。groups：每张图属于第几组（一组一个节拍）。"""
    profile = profile or TRAVEL
    count = max(groups)
    spans = [[k for k, g in enumerate(groups, 1) if g == n] for n in range(1, count + 1)]
    layout = "；".join(f"第 {n} 组：第 {s[0]}–{s[-1]} 张" if len(s) > 1 else f"第 {n} 组：第 {s[0]} 张"
                      for n, s in enumerate(spans, 1))
    text = PROMPT.format(count=len(frames), duration=duration, process=PROCESS if process else "",
                         before="process" if process else "beats", chars=SUMMARY_CHARS, name=profile["name"], focus=profile["focus"])
    text += "\n" + GROUPED.format(groups=count, layout=layout)
    content = [("text", text)]
    for k, (f, s) in enumerate(zip(frames, offsets, strict=True), 1):
        content += [("text", f"第 {k} 张（第 {groups[k - 1]} 组，第 {s:.0f} 秒）"), ("image", str(f))]
    return content, schema(count, process), spans


def check(answer: dict, spans: list) -> dict:
    """节拍数对得上就给每个节拍记上它的关键帧范围；对不上抛 DescribeError（端口会换参数重问）。"""
    if len(answer["beats"]) != len(spans):
        raise DescribeError(f"{len(answer['beats'])} beats for {len(spans)} groups")
    for n, b in enumerate(answer["beats"]):
        b["frames"] = [spans[n][0], spans[n][-1]]
    return answer
