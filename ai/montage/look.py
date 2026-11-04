"""A local vision model looks at the frames of a cut (#322, third version).

The planning model reads text only. Here a small vision model on this machine
(Qwen3-VL behind llama.cpp's OpenAI-compatible llama-server on the development
machine; the AI Worker would hold it in the product) does the looking:

  describe  one sentence on a few frames of a card (KEYFRAMES, spread over
            it), handed to the planning model with the card, so that it knows
            what the picture shows; one frame alone misread a cathedral as a castle
  review    before a cut is rendered, frames every STEP seconds of each
            segment are asked one by one whether they show what the segment
            should; the longest run that does becomes the segment's in and out
            points, and a segment with no run of SEGMENT_MIN is sent back

Frames are asked one at a time: a 2B model judging one picture is steadier
than one comparing a dozen. Only text leaves this module; the frames go to the
local model alone. Standard library only, besides FFmpeg on PATH.
"""

import base64
import json
import os
import re
import subprocess
import tempfile
import urllib.request
from concurrent.futures import ThreadPoolExecutor

# Seconds between the frames a segment is reviewed on, and their short side.
STEP = 0.5
EDGE = 360
# A frame that does not fit, between two that do, is taken as the model's slip.
GAP = 1
# A segment fits when its run covers at least this share of its frames.
FITS = 0.8
SEGMENT_MIN = 1.5
TIMEOUT = 120
WORKERS = 4
# Frames a card is described from.
KEYFRAMES = 3

# Opening with a statement ("these are frames of one clip") had a 2B model repeat it as
# its answer for 59 of 396 cards; asked directly, it answers.
DESCRIBE = ("用一句中文描述上面几张图（同一段视频按时间先后的几帧）：在哪里，有什么，人或物在做什么；"
            "是夜晚就写夜晚。只写几帧里都看得到的，认不准的建筑就说建筑，不要编地名，不超过 40 个字。"
            "直接写描述，不要复述题目。")

# The place alone, for telling a card's scene apart from the people in it (#350): a film shot
# in one look, men in suits in the dark, reads alike in pictures and in captions, while an
# office, a car and a beach are different places.
SCENE = ("上面是同一段视频按时间先后的几帧。只描述场景，不写人物在做什么、什么表情：在什么地方（室内还是室外、什么场所），"
         "有哪些环境和陈设，什么时间、天气和光线。认不准的地方就说是什么样的建筑或房间，不要编地名，不超过 40 个字。直接写描述。")

# Two more facts of a card a 2B model gets right on the same frames; shot size, camera
# motion, landmarks and looks it does not (docs/research/video-moment-retrieval.md).
LIGHTS = ("晴天", "阴天", "逆光", "黄昏", "夜晚", "室内")
LIGHT = "上面是同一段视频的几帧。光线是哪一种：" + "、".join(LIGHTS) + "？只回答一个词。"
HIGHLIGHT = "上面是同一段视频的几帧。画面里最吸引人的一样东西是什么？不超过 8 个字，直接回答。"
HIGHLIGHT_CHARS = 12

# The answer comes first: asked to describe first, or to fill a JSON verdict, a 2B
# model says yes far more often (8 and 11 of 14 hard frames right, 13 of 14 this way).
FIT = "这是一段视频里的一帧。问题：画面里有「{shows}」吗？请仔细看，回答“有”或“没有”，再用一句话说明画面里实际是什么。"

# An experiment (#326): the cards a search recalls are judged one by one on their key frames
# before the planner sees them, and only those that pass go on. The answer comes first, as in
# FIT; the score asks how clearly and how well the frames show it, and a card passes when it
# is there and scores SCREEN_PASS or more (or gave no score).
SCREEN = ("上面是同一段视频按时间先后的几帧。问题：画面里有「{shows}」吗？请仔细看，先回答“有”或“没有”；"
          "再给 0–10 分，10 分是清楚、好看地拍到了它；最后用一句话说明画面里实际是什么。格式：有，8 分，……")
SCREEN_PASS = 5
SCORE = re.compile(r"(\d{1,2})\s*分")


class VisionError(Exception):
    pass


class Vision:
    """A vision model on an OpenAI-compatible chat completions URL."""

    def __init__(self, url: str, model: str = "local"):
        if not url.startswith(("http://", "https://")):
            raise ValueError(f"not an HTTP URL: {url}")
        self.url, self.model = url, model

    def ask(self, prompt: str, jpeg: bytes | list[bytes], max_tokens: int = 120) -> str:
        """The model's answer to prompt about one picture or several, in order."""
        jpegs = jpeg if isinstance(jpeg, list) else [jpeg]
        content = [{"type": "image_url", "image_url": {"url": "data:image/jpeg;base64," + base64.b64encode(j).decode()}}
                   for j in jpegs] + [{"type": "text", "text": prompt}]
        body = {"model": self.model, "temperature": 0, "max_tokens": max_tokens,
                "messages": [{"role": "user", "content": content}]}
        # The URL is the operator's and checked to be HTTP above.
        request = urllib.request.Request(self.url, json.dumps(body).encode(), {"Content-Type": "application/json"})  # noqa: S310
        try:
            with urllib.request.urlopen(request, timeout=TIMEOUT) as response:  # noqa: S310
                answer = json.load(response)
        except (OSError, ValueError) as error:
            raise VisionError(f"本机视觉模型不可用：{error}") from None
        return answer["choices"][0]["message"]["content"].strip()

    def describe(self, jpegs: list[bytes]) -> str:
        return self.ask(DESCRIBE, jpegs, 80).splitlines()[0].strip()

    def scene(self, jpegs: list[bytes]) -> str:
        return self.ask(SCENE, jpegs, 80).splitlines()[0].strip()

    def light(self, jpegs: list[bytes]) -> str:
        """One of LIGHTS, or 不确定 when the answer is none of them."""
        text = self.ask(LIGHT, jpegs, 8)
        return next((name for name in LIGHTS if text.startswith(name)), "不确定")

    def highlight(self, jpegs: list[bytes]) -> str:
        return self.ask(HIGHLIGHT, jpegs, 20).splitlines()[0].strip(" 。.")[:HIGHLIGHT_CHARS]

    def fits(self, jpeg: bytes, shows: str) -> tuple[bool, str]:
        text = self.ask(FIT.format(shows=shows), jpeg, 60)
        seen = text.lstrip("有没，,。.：: \n")
        return text.startswith("有"), seen.splitlines()[0][:60] if seen else ""

    def screen(self, jpegs: list[bytes], shows: str) -> dict:
        return screened(self.ask(SCREEN.format(shows=shows), jpegs, 80))


def screened(text: str) -> dict:
    """The verdict in an answer to SCREEN: whether the frames show it, the score (None when
    the answer gave none), what they showed, and whether the card passes."""
    fits = text.startswith("有")
    score = SCORE.search(text)
    points = min(int(score.group(1)), 10) if score else None
    rest = text[score.end():] if score else text.lstrip("有没")
    seen = rest.lstrip("有没，,。.：: \n").splitlines()
    return {"fits": fits, "score": points, "seen": seen[0][:60] if seen else "",
            "passes": fits and (points is None or points >= SCREEN_PASS)}


def frames(video: str, start: float, end: float, step: float = STEP) -> list[tuple[float, bytes]]:
    """(second, JPEG) at the middle of every step of start-end, decoded with an accurate seek."""
    count = max(1, round((end - start) / step))
    step = (end - start) / count
    with tempfile.TemporaryDirectory() as work:
        subprocess.run(
            ["ffmpeg", "-nostdin", "-v", "error", "-ss", f"{start + step / 2:.3f}", "-i", video,  # noqa: S607  FFmpeg from PATH
             "-vf", f"fps=1/{step:.4f},scale=-2:{EDGE}", "-frames:v", str(count), "-q:v", "4",
             os.path.join(work, "%04d.jpg")],
            check=True, capture_output=True, timeout=TIMEOUT)
        names = sorted(os.listdir(work))
        out = []
        for k, name in enumerate(names):
            with open(os.path.join(work, name), "rb") as file:
                out.append((round(start + step * (k + 0.5), 3), file.read()))
    return out


def run(fit: list[bool]) -> tuple[int, int] | None:
    """The first and last index of the longest run of fitting frames, a GAP of misfits allowed inside."""
    best, a, last, misses = None, None, None, 0
    for k, ok in enumerate([*fit, False]):
        if ok:
            if a is None:
                a = k
            last, misses = k, 0
            continue
        if a is not None:
            misses += 1
            if misses > GAP or k == len(fit):
                if best is None or last - a > best[1] - best[0]:
                    best = (a, last)
                a, misses = None, 0
    return best


def review(vision: Vision, video: str, start: float, end: float, shows: str) -> dict:
    """What the frames of one segment show against shows: fits (bool), the in and out points
    of its longest fitting run (or None), how many frames fit and what the frames showed."""
    shots = frames(video, start, end)
    with ThreadPoolExecutor(WORKERS) as pool:
        said = list(pool.map(lambda tj: vision.fits(tj[1], shows), shots))
    fit = [ok for ok, _seen in said]
    span = run(fit)
    step = (end - start) / len(shots)
    out = {"frames": len(shots), "fit": sum(fit), "seen": _seen(said, fit), "start": None, "end": None}
    out["usable"] = out["fits"] = False
    if span:
        a, b = span
        out["start"] = round(max(start, shots[a][0] - step / 2), 2)
        out["end"] = round(min(end, shots[b][0] + step / 2), 2)
        out["usable"] = out["end"] - out["start"] >= SEGMENT_MIN
        out["fits"] = out["usable"] and b - a + 1 >= FITS * len(shots)
    return out


def _seen(said: list[tuple[bool, str]], fit: list[bool]) -> str:
    """What the frames showed: the most common answer among the misfits when most miss, else among the fits."""
    misses = [seen for (_ok, seen), ok in zip(said, fit, strict=True) if not ok and seen]
    hits = [seen for (_ok, seen), ok in zip(said, fit, strict=True) if ok and seen]
    pool = misses if len(misses) > len(hits) else hits or misses
    return max(pool, key=pool.count) if pool else ""
