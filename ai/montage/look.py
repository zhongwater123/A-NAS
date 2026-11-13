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
# Asked to write only what every frame shows, it left out whatever changed within a card: the
# second place of a walk, the man who came in. 方案 B's tagging asks the other way round (#416).
DESCRIBE = ("用一句中文描述上面几张图（同一段视频按时间先后的几帧）：在哪里，有什么，人或物在做什么；"
            "是夜晚就写夜晚。画面如果有变化（换了地方、主体或镜头方向），要概括全部内容，不要只写其中一张。"
            "认不准的建筑就说建筑，不要编地名，不超过 40 个字。直接写描述，不要复述题目。")

# What to look at in a drama film, after DESCRIBE (#416, from 方案 B's tagging), for a member who
# picks 剧情 for the cut: blind on 40 cards it won 8 to 1 on the detective film, while a line for
# travel vlogs lost 3 to 4 on Shanghai and was dropped. Not wired to the style yet.
FOCUS = {
    "剧情": "这是剧情片：写清人物的衣着、动作和表情，和谁在一起，手里或画面里的关键物件（枪、手电、电话、照片、文件），场景是什么样的光线。",
}

# The place alone, for telling a card's scene apart from the people in it (#350): a film shot
# in one look, men in suits in the dark, reads alike in pictures and in captions, while an
# office, a car and a beach are different places.
SCENE = ("上面是同一段视频按时间先后的几帧。只描述场景，不写人物在做什么、什么表情：在什么地方（室内还是室外、什么场所），"
         "有哪些环境和陈设，什么时间、天气和光线。认不准的地方就说是什么样的建筑或房间，不要编地名，不超过 40 个字。直接写描述。")

# Text over the picture marks a title, credits or a caption card, which a montage leaves out
# unless asked (#350); a place description seldom mentions it. Asked straight whether a frame
# has such text, a 2B model said no to a cast list; it reads the text well. Told what it read,
# it tells text laid over the picture from a sign or a clock it was filmed on (OVERLAY), and
# credits from a line of dialogue or a screen in the story (KIND); asked only the kind, it
# called a police sign a logo. TITLES are the kinds a montage leaves out.
READ = "把画面上能看到的文字原样写出来，只写文字；没有文字就回答“无”。"
READ_CHARS = 40
OVERLAY = ("画面上有文字「{}」。这些字是后期叠加在画面上的（片名、字幕、演职员表、标志），"
           "还是拍到的实物上的（招牌、门牌、钟表、屏幕、纸张）？只回答“叠加”或“实物”。")
TITLES = ("片名", "演职员表", "出品或资助说明", "品牌标志", "时间地点说明")
KINDS = (*TITLES, "台词", "仪器屏幕或界面上的字", "招牌或实物上的字")
KIND = "画面上有文字「{}」。它是哪一种：" + "、".join(KINDS) + "？只回答一种。"

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
# A part of the film for the planner's catalog, summed up from its cards' lines (#398): what
# happens in it in order, the same said once, every different thing kept. Asked without a
# penalty on repeats, a 2B model said one sentence over and over for case-01's dark rooms.
PART = ("下面是一部影片里连续一段时间的 {n} 个镜头，按原片先后排列，每行一个：光线、画面描述、最突出的东西。\n"
        "{lines}\n\n"
        "写这一段的概述，给剪辑规划用：按先后写出现的地点、人物、物体、动作和事件，意思相同的合并成一条，意思不同的都要留下，"
        "尤其是只出现一次的特别画面；只写镜头里有的，不要编，不要加括号注释。不超过 {chars} 个字，直接写概述。")
# What a 2B copies from its input or its habits into a summary all the same: highlight notes,
# card ids, and "these three pictures show".
COPIED = re.compile(r"[（(](?:亮点[:：][^）)]*|\d+-\d+-\d+[^）)]*)[）)]|[，,]?\s*亮点(?:为|是|[:：])[^，。；,;]*|三张(?:连续)?(?:图|画面)(?:展示|显示)?(?:了)?")
PART_REPEAT = 1.15


# A model going round in circles repeats a whole clause; a summary that names one thing three
# times ("机械巨兽" in case-08's battle) repeats fewer characters than this.
LOOP_CHARS = 16


def looping(text: str) -> bool:
    """A model stuck repeating itself: some LOOP_CHARS characters come back three times or more."""
    return any(text.count(text[i:i + LOOP_CHARS]) >= 3 for i in range(max(len(text) - LOOP_CHARS, 0)))


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

    def ask(self, prompt: str, jpeg: bytes | list[bytes], max_tokens: int = 120, repeat_penalty: float | None = None) -> str:
        """The model's answer to prompt about one picture or several (none: text alone), in order."""
        jpegs = jpeg if isinstance(jpeg, list) else [jpeg]
        content = [{"type": "image_url", "image_url": {"url": "data:image/jpeg;base64," + base64.b64encode(j).decode()}}
                   for j in jpegs] + [{"type": "text", "text": prompt}]
        body = {"model": self.model, "temperature": 0, "max_tokens": max_tokens,
                "messages": [{"role": "user", "content": content}]}
        if repeat_penalty is not None:
            body["repeat_penalty"] = repeat_penalty
        # The URL is the operator's and checked to be HTTP above.
        request = urllib.request.Request(self.url, json.dumps(body).encode(), {"Content-Type": "application/json"})  # noqa: S310
        try:
            with urllib.request.urlopen(request, timeout=TIMEOUT) as response:  # noqa: S310
                answer = json.load(response)
        except (OSError, ValueError) as error:
            raise VisionError(f"本机视觉模型不可用：{error}") from None
        return answer["choices"][0]["message"]["content"].strip()

    def summary(self, lines: list[str], chars: int) -> str:
        """A part's card lines summed up in at most about chars characters (PART), or "" when the
        model went round in circles (looping), so that the part keeps its lines."""
        text = " ".join(self.ask(PART.format(n=len(lines), lines="\n".join(lines), chars=chars), [],
                                 int(chars * 1.6), repeat_penalty=PART_REPEAT).split())
        if looping(text):
            return ""
        text = COPIED.sub("", text)
        # Past chars by a fifth, cut at the last full stop before it: a 2B runs long.
        if len(text) > chars * 1.2:
            cut = max(text.rfind(p, 0, int(chars * 1.2)) for p in "。；")
            text = text[:cut + 1] if cut > chars // 2 else text[:int(chars * 1.2)]
        return text

    def describe(self, jpegs: list[bytes], kind: str | None = None) -> str:
        """A card's description; with kind (剧情), what to look at in that kind of footage (FOCUS)."""
        prompt = DESCRIBE + ("\n" + FOCUS[kind] if kind else "")
        return self.ask(prompt, jpegs, 80).splitlines()[0].strip()

    def scene(self, jpegs: list[bytes]) -> str:
        return self.ask(SCENE, jpegs, 80).splitlines()[0].strip()

    def text(self, jpegs: list[bytes]) -> str:
        """The kind and words of the first of TITLES laid over any of the frames ("片名：…"), or ""."""
        for jpeg in jpegs:
            read = " ".join(self.ask(READ, jpeg, READ_CHARS).split())[:READ_CHARS]
            if not read or read.startswith(("无", "没有")) or not self.ask(OVERLAY.format(read), jpeg, 6).startswith("叠加"):
                continue
            kind = kind_of(self.ask(KIND.format(read), jpeg, 10))
            if kind in TITLES:
                return f"{kind}：{read}"
        return ""

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


def kind_of(answer: str) -> str:
    """The one of KINDS an answer to KIND names, in part or whole, or ""."""
    answer = answer.strip(" 。.“”\"")
    return next((k for k in KINDS if answer and (k in answer or answer in k)), "")


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
