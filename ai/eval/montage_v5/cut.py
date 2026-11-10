"""剪法25 (#405, from #416): keyframe cards cut by a two-step plan of the local Qwen3.5-4B.

  cards   (cards.py) cards cut by picture alone, each described from its key frames as beats (shot
          size, what is seen), 1-2 highlights, a process (process_cards.py: the steps one after
          another, no more than its beats) and a summary.
  titles  4B reads three key frames of each card; a card with a title laid over two of them over a
          plain picture, or the film's name, is a 报幕 card and is left out; a vlog's opening teaser
          is cut by fixed seconds (LEFT_OUT) as a material problem, not something to train for.
  pick    4B sees each card's process (its summary where there is none; no tags, no highlight) with
          the join rules and the category's structure, and writes 方案 B's outline: design, unmet,
          target/15 s parts or more and no more than a picture a 6 s of the target in all, each part
          with a link to the one before, a weight and 1-4 pictures (from which cards), parts and
          pictures in shooting order. The order is the model's own.
  shots   the program gives each part seconds by its weight and shares them evenly among its
          pictures (budget, 方案 B's way); 4B ranks each picture's beats from its cards, told the
          part's link, without highlight marks.
  cut     each picture filled from its ranked beats, each beat its own length from its key frame,
          SHORTEST to CAPS[style], a picture at most PICTURE_MOST (filled); leftovers carried on; a
          card's beats that meet joined; an end inside speech moves to the nearest pause; rendered
          with sound.

  python3 cut.py URL [case ...]     (resumes: each step's file is kept per case, under paths.CUTS)
"""

import difflib
import itertools
import json
import math
import os
import statistics
import subprocess
import sys
import time
from pathlib import Path

import requests

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))  # ai/, for montage
import held
from paths import B_DIR, CARDS, CUTS as HERE, V5, read_json, write_json

from montage import look, moments, render
from montage.styles import STYLES

VIDEOS = Path.home() / "A-NAS-datasets" / "videos"
CASES = {"01": ("case01", "剪成一条悬疑感的电影预告片，保留关键对白，不剧透结局", 60, "预告片", "侦探片预告"),
         "02": ("shanghai", "按时间顺序讲完博主在上海的这一天，保留说话的关键片段", 90, "旅行", "上海vlog"),
         "04": ("case04", "把两集合成一条深圳旅行故事，以冲浪为高潮", 60, "旅行", "深圳冲浪"),
         "ne": ("newzealand", "按拍摄时间顺序剪一条新西兰旅行 vlog，围绕旅途中的人物展开，场景之间衔接自然", 90, "旅行", "新西兰"),
         "ch": ("chuanxi", "按拍摄时间顺序剪一条川西旅行 vlog，围绕旅途中的人物展开，场景之间衔接自然", 90, "旅行", "川西")}
# The user's own camera footage (第五版): its cards and exclusions live in paths.V5, and straight from the
# camera it carries no title, credits or film name, so no card is asked about 报幕.
RAW = {"ne": "newzealand", "ch": "chuanxi"}
# A frame is 报幕 when a title (look.TITLES) is laid over a plain picture, one level over at least
# PLAIN of it (credits on black: 0.84-0.99), or when the title is the film's name (片名) over
# anything ("Meridian" over a street, 0.4). A shop sign read as a logo (全家, 0.10-0.15) and a
# vlog's subtitles read as 时间地点说明 (0.04-0.25) are over busy pictures. Two of three frames make
# a card 报幕.
PLAIN = 0.6
WIDEN = 3.0         # a clip widened to whole sentences runs at most this much past its share


# Each run of a case is a take: CUT_TAG ("-1", "-2") names its files, and its seed varies the plans.
TAG = os.environ.get("CUT_TAG", "")
SEED = int(os.environ.get("CUT_SEED", "0")) or None


def chat(url, system, user, schema, name, max_tokens=3000):
    body = {"model": "local", "temperature": 0.2, "max_tokens": max_tokens, **({"seed": SEED} if SEED else {}),
            "chat_template_kwargs": {"enable_thinking": False},
            "messages": [{"role": "system", "content": system}, {"role": "user", "content": user}],
            "response_format": {"type": "json_schema", "json_schema": {"name": name, "schema": schema}}}
    t0 = time.perf_counter()
    r = requests.post(f"{url}/v1/chat/completions", json=body, timeout=1800)
    r.raise_for_status()
    out = r.json()
    usage = out.get("usage", {})
    return json.loads(out["choices"][0]["message"]["content"]), {
        "seconds": round(time.perf_counter() - t0, 1), "input": usage.get("prompt_tokens"),
        "output": usage.get("completion_tokens")}


# Step one on a cloud model (2026-11-09, CUT_PLANNER=claude): planning may leave the host (#341 lets each
# plan declare its outside calls). With 152-187 cards, the 4B wrote 5 of 6 New Zealand parts from the first
# 46 and none from the next 90. The call goes through the Claude Code CLI (as eval/video_planner.py does),
# with no tools, settings or MCP servers: it sees the prompt alone. CUT_CLAUDE names the CLI (one recent
# enough for the model), CUT_CLAUDE_MODEL the model. Step two stays on the local 4B.
PLANNER = os.environ.get("CUT_PLANNER", "local")
# Step two on the cloud model too (2026-11-09, CUT_STEP2=claude): faster than the 4B, which also listed a
# card's beats in their own order for every picture of the second plan to Forever.
STEP2 = os.environ.get("CUT_STEP2", "local")
CLAUDE = os.environ.get("CUT_CLAUDE", "claude")
CLAUDE_MODEL = os.environ.get("CUT_CLAUDE_MODEL", "claude-opus-5-5")


def ask_claude(system, user, schema):
    """chat() on the cloud model: (answer, cost), the cost with its US dollars."""
    t0 = time.perf_counter()
    run = subprocess.run([CLAUDE, "-p", "--model", CLAUDE_MODEL, "--output-format", "json", "--tools", "",
                          "--setting-sources", "", "--strict-mcp-config", "--no-session-persistence",
                          "--disable-slash-commands", "--system-prompt", system,
                          "--json-schema", json.dumps(schema, ensure_ascii=False)],
                         input=user, capture_output=True, text=True, timeout=1800, cwd=HERE, check=False)
    out = json.loads(run.stdout or "{}")
    if run.returncode or out.get("is_error") or "structured_output" not in out:
        raise RuntimeError(f"{CLAUDE_MODEL}: {out.get('result') or run.stderr[-500:]}")
    usage = out.get("usage", {})
    return out["structured_output"], {
        "seconds": round(time.perf_counter() - t0, 1), "model": CLAUDE_MODEL, "usd": out.get("total_cost_usd"),
        "input": sum(usage.get(k) or 0 for k in ("input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens")),
        "output": usage.get("output_tokens")}


def ask_picture(url, prompt, jpeg, tokens):
    import base64
    content = [{"type": "image_url", "image_url": {"url": "data:image/jpeg;base64," + base64.b64encode(jpeg).decode()}},
               {"type": "text", "text": prompt}]
    body = {"model": "local", "temperature": 0, "max_tokens": tokens, "chat_template_kwargs": {"enable_thinking": False},
            "messages": [{"role": "user", "content": content}]}
    r = requests.post(f"{url}/v1/chat/completions", json=body, timeout=300)
    r.raise_for_status()
    return r.json()["choices"][0]["message"]["content"].strip()


def title_of(url, jpeg):
    """look.Vision.text for one frame, on the 4B: the kind and words of a title over it, or ""."""
    read = " ".join(ask_picture(url, look.READ, jpeg, look.READ_CHARS).split())[:look.READ_CHARS]
    if not read or read.startswith(("无", "没有")):
        return ""
    if not ask_picture(url, look.OVERLAY.format(read), jpeg, 6).startswith("叠加"):
        return ""
    kind = look.kind_of(ask_picture(url, look.KIND.format(read), jpeg, 10))
    return f"{kind}：{read}" if kind in look.TITLES else ""


def load(case):
    """The case's cards, each with one description for both steps: proc-<case>.json's (beats, process,
    scene, summary; same key frames) where it has one, else cards-<case>.json's. Until 2026-11-09 step 1
    read the process from proc and step 2 the beats from cards, two separate readings of the same frames."""
    proc_path = cards_dir(case) / f"proc-{case}.json"
    proc = read_json(proc_path) if proc_path.exists() else {}
    cards = [c for c in read_json(cards_dir(case) / f"cards-{case}.json").values() if "answer" in c]
    return [c | {"answer": proc[c["id"]]["answer"]} if "answer" in proc.get(c["id"], {}) else c for c in cards]


def described(case):
    """The description file load() reads for the case; a take's pick and shots files name it."""
    return f"proc-{case}.json" if (cards_dir(case) / f"proc-{case}.json").exists() else f"cards-{case}.json"


def cards_dir(case):
    return V5 if case in RAW else CARDS


def reused(path, case):
    """A take's pick or shots file, kept only when it was made from the description load() reads now:
    a take of before 2026-11-09 (step 2 on cards-<case>.json) cut again would pair its plan with
    beats it never saw."""
    data = read_json(path)
    if data.get("descriptions") != described(case):
        sys.exit(f"{path.name} 不是按 {described(case)} 规划的：换一个 CUT_TAG 重剪")
    return data


def titles(case, url):
    path = HERE / f"titles-{case}.json"
    done = read_json(path) if path.exists() else {}
    cards = {c["id"]: c for c in load(case)}
    for i, entry in done.items():
        entry["plain"] = [plainness(cards[i]["frames"][k]) for k in entry["frames"]]
        entry["title"] = is_report(entry["said"], entry["plain"])
    for card in load(case):
        if card["id"] in done:
            continue
        if case in RAW:
            done[card["id"]] = {"frames": [], "said": [], "plain": [], "title": False, "seconds": 0}
            continue
        n = len(card["frames"])
        picks = sorted({0, n // 2, n - 1})
        t0 = time.perf_counter()
        said = [title_of(url, Path(card["frames"][k]).read_bytes()) for k in picks]
        plain = [plainness(card["frames"][k]) for k in picks]
        done[card["id"]] = {"frames": picks, "said": said, "plain": plain, "title": is_report(said, plain),
                            "seconds": round(time.perf_counter() - t0, 1)}
        if done[card["id"]]["title"]:
            print(case, card["id"], "报幕", said, flush=True)
        write_json(done, path)
    write_json(done, path)
    return done


def plainness(path):
    """The share of a frame within 12 grey levels of its commonest level."""
    import numpy as np
    from PIL import Image
    a = np.asarray(Image.open(path).convert("L").resize((128, 72)), dtype=float)
    top = np.histogram(a, bins=256, range=(0, 256))[0].argmax()
    return round(float(((a >= top - 12) & (a <= top + 12)).mean()), 2)


def is_report(said, plain):
    """Two of the three frames carry a title over a plain picture, or the film's name."""
    return sum(1 for s, p in zip(said, plain, strict=True)
               if s and (p >= PLAIN or s.split("：", 1)[0] == "片名")) >= 2


def clock(t):
    return f"{int(t // 60)}:{int(t % 60):02d}"


# A vlog's opening teaser (快剪的行程预告: other days and cities) is a fact of the material: its seconds
# are left out (start, end per video). 上海: from her self-introduction on the Bund (02-003, "I'm Ginny,
# and I'm in Shanghai right now"; until 2026-11-09 from the airport, 02-008, so the introduction was
# never in the catalog), less the teaser shots inside it (02-004 to 02-006: 南京东路, 苏州, 杭州; both
# plans of 2026-11-09 took 苏州 and 杭州 into a Shanghai vlog); 冲浪: episode 1 at the drive to 深圳
# (04-014), episode 2 at the beach after the recap (04-124).
LEFT_OUT = {("02", "01"): [(0.0, 14.0), (26.0, 38.0)], ("04", "01"): [(0.0, 82.0)], ("04", "02"): [(0.0, 84.0)]}
# 第五版's mis-shots (a finger over the lens, black), as the user marked them in excluded.json.
for _case, _key in RAW.items():
    for _x in read_json(V5 / "excluded.json").get(_key, []):
        LEFT_OUT.setdefault((_case, _x["video"]), []).append((float(_x["from"]), float(_x["to"])))


# 第六版 (2026-11-09): the user's own footage is left without its blurred, shaking or blocked cards
# (quality.py; 画面质量核对.html shows them, 画面质量标注.json holds the user's marks on 36 of them). Blur
# alone does not tell: the user kept still sunsets and dusk at 8-10.8 and left out what blurred while the
# camera moved (shake 2-4.4), so blur counts over BLUR_MOST only with shake over BLUR_MOVING. Shake marks
# from 3 to 5.5 px went both ways; over SHAKE_MOST all were left out. Black (nothing to measure) and a
# blocked share of BLOCKED_MOST or more go too. On the marks: none of the 22 kept is left out, 10 of the
# 14 are; the 4 missed are bad in part of the card only (a selfie swung to the sky or a jacket, a face
# filling the frame, a soft foreground). CUT_QUALITY=0 keeps them all.
BLUR_MOST, BLUR_MOVING, SHAKE_MOST, BLOCKED_MOST = 8.0, 1.0, 5.6, 0.25
QUALITY = os.environ.get("CUT_QUALITY", "1") != "0"


def poor(case):
    """The cards quality.py would leave out: {card: [reasons]}."""
    path = cards_dir(case) / f"quality-{case}.json"
    if case not in RAW or not QUALITY or not path.exists():
        return {}
    out = {}
    for cid, q in read_json(path).items():
        why = [w for w, bad in (("黑帧", math.isnan(q["blur"])), ("模糊", q["blur"] > BLUR_MOST and q["shake"] > BLUR_MOVING),
                                ("抖动", q["shake"] > SHAKE_MOST), ("遮挡", q["blocked"] >= BLOCKED_MOST)) if bad]
        if why:
            out[cid] = why
    return out


def usable_cards(case):
    marks = read_json(HERE / f"titles-{case}.json")
    bad = poor(case)
    return [c for c in load(case) if not marks[c["id"]]["title"] and c["id"] not in bad
            and not any(a - 0.01 <= c["start"] < b - 0.01 for a, b in LEFT_OUT.get((case, c["video"]), []))]


# Step one is 方案 B's outline request (instruct.REQUEST_PROMPT) on our cards, the join rules first so
# that the plan is made with them: design -> unmet -> parts with weights and the pictures each wants;
# no materials list (the parts name their cards, and a list kept cards it missed out of the parts); no pace (the program does not cut by it) and no tags (标签盲测); the category's usual structure
# (B's film / travel-vlog profiles) as a reference; B's review rules on joins moved into the outline.
PICK_SYSTEM = "你是剪辑规划，按用户的剪辑要求写一份成片大纲。画面由程序按大纲剪出，带原声，不加配乐和字幕。"
PICK = """用户的剪辑要求：
「{instruction}」

下面是 {total_min:.1f} 分钟原始素材切成的 {n} 张卡片，按拍摄时间排列。卡片按画面变化切分，一张卡片是同一处、同一件事的一段连续画面；{what}由本机小模型看关键帧写成，可能看错。报幕、片头片尾字幕、黑场和纯文字画面已经去掉了。
每行：卡片 id {where}起始时间 时长 | 场景 | {what}{what_note}{said_note}

{catalog}
{position}{structure}
衔接（规划时照着安排）：
{open_close}
- 同一部分里的几个画面要注意场景之间的衔接，前后有一定的叙事性或画面连续性，画面变化的时候要加入衔接画面；
- 换场景时用远景、空镜或一个动作的开始和结束来过渡；同一场景里景别可以由远到近推进，运动方向前后一致；
- 不要用片头片尾字幕、黑场或纯文字画面。

{task}
1. design：用 2–3 句话写成片的设计：开头、中间、结尾分别用哪些卡片，为什么这样安排；开场怎么交代背景（时间、地点、人物），段落之间怎么过渡；开头、中间、结尾按拍摄时间先后安排；写完对照用户的要求检查内容和先后顺序；
2. opening：写开头第一个画面用哪张卡片、为什么选它；
3. ending：写结尾最后一个画面用哪张卡片、为什么选它、属于哪一种收尾；
4. unmet：要求里提到、但卡片里找不到的内容，都找得到就留空；找不到的内容不要拿别的卡片冒充；
5. parts：照 design 分成 {parts_rule}{pictures_rule}，按拍摄时间先后排列，每部分里的画面也按拍摄时间先后排列：
   - name 是这一部分的标题，summary 用一句话说明这部分讲什么；
   - link 写这一部分和上一部分怎么衔接：写清上一部分最后一个画面和这一部分第一个画面靠什么接上（同一个人、同一样东西、同一个动作或声音、到达或进入一个地方），不要只写“直接切入”；{first_link}
   - weight 是这一部分的分量（1–10 的整数）；
   - scenes 是这一部分要的 1–4 个画面：query 用一个短语说明要的画面（如“雨夜车窗上的雨滴”“江边远眺陆家嘴”），写看得见的主体、动作和光线，不写地名（概括里的地名可能认错），所有部分的 query 互不重复；cards 写这个画面取自哪几张卡片（1–3 张），一张卡片只放进一个画面。
只输出 JSON。"""

# The category's usual structure, offered as a reference (the instruction still decides): B's words.
STRUCTURE = """
这批素材属于「{name}」，这类成片常用的结构是：{structure}
设计时可以参考，但以用户的要求为准。
"""
PROFILES = {"预告片": "film", "旅行": "travel-vlog"}


def structure_of(style):
    """B's profile for the style: its name and 成片结构, from the copy of B's prompt files."""
    key = PROFILES.get(style)
    if not key:
        return ""
    lines = (B_DIR / f"{key}.prompt.md").read_text("utf-8").splitlines()
    name = lines[0].lstrip("# ").strip()
    body, on = [], False
    for line in lines:
        if line.startswith("## "):
            on = line[3:].strip() == "成片结构"
            continue
        if on and line.strip():
            body.append(line.strip())
    text = "".join(body)
    # The film's cold open is left out: told to open on a gripping picture or a line, the 4B opened
    # in the car or the office and never set up the time and place (#416).
    text = text.replace("冷开场：一个抓人的画面或一句对白 → ", "")
    # Nor the closing on suspense without the ending (the puppet was left out of the plans), nor the
    # licence to leave the story's order (a plan of a trailer turned into a flashback unasked).
    text = text.replace(" → 收在一个悬念画面或一句话上，不交代结局", "").replace("不必按剧情时间顺序；", "")
    return STRUCTURE.format(name=name, structure=text)


SHOTS = """你是剪辑师。大纲定好了段落和每个段落要的画面，程序定好了每个画面约几秒。你给每个画面挑节拍；程序按你排的顺序，从每个节拍开头取 {shortest:g}–{cap:g} 秒，取到凑满这个画面的秒数为止。成片约 {target:g} 秒，带原声，不加配乐和字幕。
下面每个画面列出它可以取自的卡片（id、时间、场景、概括）和节拍（节拍 id、景别、能取几秒、画面{said_note}）。
每个画面写：
- beats：从它的卡片里挑节拍，最合适的排前面，多列一两个备用；一个节拍只用一次；
- shows：一句话写这个画面要呈现什么；
- link：它和上一个画面怎么接，第一个写“开场”；写的衔接要能在你挑的节拍里看到。
挑节拍时，按优先级从高到低：
- 画面后面的描述只是大纲给的方向，大纲并没有深入了解画面细节，不用死守；你要自己保证画面内部的叙事性或画面连贯性，这一条优先级最高；
- 这几张卡片里如果有更合适、更精彩的节拍，按需挑选，这一条优先级其次；
- 相邻镜头景别要有变化，同一主体不要用相同景别接着出现；新地方先远景或全景，再到近处和细节；换地点、换时间用动作、方向、相似的形状或空镜过渡，这一条优先级最后。
unmet：大纲要的、节拍里找不到的画面，没有就写空数组。"""

# 方案 B's way of filling a part (instruct.outline_parts, outline.pick_segments), on our beats: a part's
# seconds by its weight; a picture's seconds the part's shared evenly among its pictures (what one
# cannot hold goes to the others); a picture filled from its beats in the planner's order, each beat
# its own length from its key frame, at least SHORTEST (one shorter runs on into the next beat), at
# most CAP; a beat is taken while it brings the picture closer to its seconds, and what a picture
# leaves over or short goes on to the next, so no short leftover becomes a shot of its own.
SAME_TEXT = 0.85   # two beats whose descriptions match this closely show the same thing
SHORTEST = 2.0
CAPS = {"预告片": 4.0, "旅行": 6.0}     # B's film profile: shots 1.5-4 s; travel at B's 适中, 6 s
CAP = 6.0
# At least a part a PART_SECONDS of the cut (90 s: 6, 60 s: 4): with 3 parts for 90 s, the Shanghai
# outline gave a part 47 s, and the crab-noodle picture 35.7 s of one card. A part of about 15 s tells
# what happened at one place. And as in B, where a part is about 15 s and no shot over its pace's cap,
# no picture runs longer than PICTURE_MOST, whatever the style; what it cannot take goes on.
PART_SECONDS = 15.0
PICTURE_MOST = 8.0
PICTURE_SECONDS = 6.0
# A catalog of more than CHUNK_LINES cards is planned in blocks of about equal size, one after another
# in shooting order, each given the target's seconds in proportion to its cards (2026-11-09): read as
# one 81-line catalog, the Shanghai vlog's middle third (lunch, 城隍庙, 豫园, 7.7 of 19.6 minutes) gave
# 3-5 of 18-24 pictures in each of three plans, and step 2 over 24 pictures listed every card's first
# three beats. Tried at 30 (CUT_CHUNK=30): 3 blocks of 27 Shanghai cards, 30 s each; each block still
# planned from a run of its own cards (the Bund by day and the night market were left out) and the first
# block, with no picture cap, gave 12 pictures to its 30 s. Off by default (0: one block).
CHUNK_LINES = int(os.environ.get("CUT_CHUNK", "0"))
# The ending (2026-11-09): what montage and travel editing close on, the user's choice of it, landscapes and
# cities apart; asked for, not ruled out (Shanghai closes on the Bund by night, crowded, and should). The
# Forever cut had ended on a museum's bronze tree.
ENDING = ("结尾的最后一个画面要静下来，节奏放缓，有结束的感觉："
          "素材以自然风景为主时，可以用远去的背影或交通工具、拉远或升高的大景、黄昏落日或星空、几乎不动的风景空镜、人物回望或静坐望远；"
          "素材以城市为主时，可以用夜景和灯火（街道、天际线、江边）、人流或车流里慢慢走远的背影、从高处俯瞰城市、地标建筑的全景；"
          "两种都可以呼应开头")
OPEN_CLOSE = f"- 开场先交代地点、时代和人物，不要跳切；{ENDING}；"
# Trial (2026-11-09, CUT_SPEECH=1): what is said, from the index's transcript (speech.json, word by word
# with times), beside each card in step one and each beat in step two. 方案 B's review showed every
# candidate's sentences, and its Shanghai story came from them (the 1930 restaurant, the Lamborghini's
# owner); our planner never saw what anyone said. Lines are cut at SAID_CARD / SAID_BEAT characters.
SPEECH = os.environ.get("CUT_SPEECH") == "1"
SAID_CARD, SAID_BEAT = 60, 40
_said = {}


def said(card_id, video, a, b, most):
    """What is said between a and b s of the video (words whose middle falls in), as one line."""
    case = card_id.split("-")[0]
    if case not in _said:
        key = CASES[case][0]
        d = next((Path.home() / "A-NAS-datasets" / "cache" / f"video-moments-{key}").glob("embeddinggemma*"))
        _said[case] = read_json(d / "speech.json")["videos"]
    # Words joined as moments.Index.lines joins them: a Shanghai subtitle's pieces carry no leading
    # space ("guysWelcome to my channelI'm Jini" when joined as they come).
    text = ""
    for s, e, w in _said[case].get(video, []):
        if not a <= (s + e) / 2 < b:
            continue
        word = w.strip()
        if text and word and (w[:1].isspace() or (text[-1].isascii() and text[-1].isalnum()
                                                   and word[0].isascii() and word[0].isalnum())):
            text += " "
        text += word
    return text if len(text) <= most else text[:most] + "…"


# Free planning (2026-11-09, CUT_FREE=1): step one is given no length, no number of parts and no cap on
# pictures; each part says its own seconds instead of a weight, and the cut is as long as they add up to.
FREE = os.environ.get("CUT_FREE") == "1"
FREE_WEIGHT = "   - weight 是这一部分的分量（1–10 的整数）；"
FREE_SECONDS = "   - seconds 是这一部分在成片里的秒数（整数），全片多长由你按素材和要求定；"
FREE_SCENES = ("要的 1–4 个画面：", "要的画面（个数由你定）：")


# 第六版 (2026-11-09, CUT_MUSIC=music-<song>.json under HERE, by music.py): the montage is cut to a song, CutClaw's
# way. The song's parts are the cut's parts, its bar-line slots each picture's shots, and the cut carries the
# song instead of its own sound. Step one is told each part's seconds, level and slots, and to take wide and
# full shots first where the music peaks.
MUSIC = os.environ.get("CUT_MUSIC", "")
MUSIC_SYSTEM = "你是剪辑规划，按用户的剪辑要求写一份成片大纲。画面由程序按大纲剪出，配上给定的音乐，不用原声，不加字幕。"
MUSIC_SCENES = ("要的 1–4 个画面：", "要的画面（个数不超过这一段能放的镜头数）：")


def song():
    return read_json(HERE / MUSIC) if MUSIC else None


def music_note(m):
    """The song's parts as step one reads them."""
    lines = [f"成片配乐：{Path(m['song']).stem}，取其中 {m['seconds']:.0f} 秒，按音乐分成 {len(m['parts'])} 段，"
             "成片的部分和音乐段一一对应；镜头由程序卡在音乐的小节上切："]
    for n, part in enumerate(m["parts"], 1):
        sl = [b - a for a, b in part["slots"]]
        lines.append(f"第 {n} 段 {part['start']:.0f}–{part['end']:.0f} 秒，{part['level']}，能放 {len(sl)} 个镜头（每个约 {statistics.median(sl):.1f} 秒）")
    # 2026-11-09: the first plan to Forever crossed five places in its 25 s chorus and, told to join scene
    # changes, put a coach on the road between the swing and the rafting into it.
    # 2026-11-09 (the user): the chorus opens on its first beat with a wide shot and is wide and grand
    # throughout. (A rule that it needs no person and holds few of them was dropped the next day: the user.)
    # 2026-11-09 (the user): build and release, the cut onto the chorus's first beat is the cut's biggest.
    # "A person moving forward" asked for a kind of picture some material has none of; the picture before
    # the chorus is described by its state instead (not yet there, not yet seen, not yet open), in the six
    # kinds the user chose from common montage practice.
    lines += ["- 进入高潮段前的最后一个画面处在“还没”的状态：人正要到达某处但还没到；镜头在往前冲或往上升；"
              "画面被挡住、被框住、很窄；人在准备或期待；动作在加速或悬在半空；从暗到亮、从小到大的前一刻。"
              "高潮段的第一个画面紧接着放全片最开阔、最宏大的全景或远景，作为释放；",
              "- 高潮段的第一个画面必须是全景或远景；高潮段全部用全景、远景和宏大的场面；",
              # 2026-11-09: both choruses were mountains end to end, with aurora, waterfalls, rivers on the
              # grassland and lakes in the same stretch of the catalog.
              "- 高潮段的宏大场面要多样，同一类景物的画面不超过三个；",
              "- 音乐段落的交界就是换场景的地方，过渡画面放在交界处；高潮段只放最精彩的画面，不放过渡画面；",
              "- 每部分的画面个数不超过这一段能放的镜头数。"]
    return "\n" + "\n".join(lines) + "\n"


def slotted(m, parts):
    """Each picture's slots: a part's slots shared among its pictures in order, as evenly as they go (the
    earlier pictures take the odd ones); a picture past the part's slots gets none."""
    out = []
    for mp, part in zip(m["parts"], parts, strict=True):
        each, extra = divmod(len(mp["slots"]), len(part["scenes"]))
        got, at = [], 0
        for k in range(len(part["scenes"])):
            take = each + (1 if k < extra else 0)
            got.append(mp["slots"][at:at + take])
            at += take
        out.append(got)
    return out


def target_of(case, picked):
    """The cut's seconds: the song's excerpt, or what a freely planned outline's parts add up to, else the
    case's target."""
    if MUSIC:
        return round(song()["seconds"])
    parts = picked["parts"]
    return sum(p["seconds"] for p in parts) if parts and all("seconds" in p for p in parts) else CASES[case][2]


def fewest_parts(target):
    return max(2, round(target / PART_SECONDS))


def share(total, capacity, weights=None):
    """total split by weights (evenly by default), none above its capacity; what one cannot hold goes to
    the others in the same proportions (方案 B's outline.share)."""
    w = weights or [1.0] * len(capacity)
    out, left, open_ = [0.0] * len(capacity), total, set(range(len(capacity)))
    while open_:
        unit = left / sum(w[j] for j in open_)
        full = [j for j in open_ if capacity[j] < w[j] * unit]
        if not full:
            for j in open_:
                out[j] = w[j] * unit
            break
        for j in full:
            out[j] = capacity[j]
            left -= capacity[j]
            open_.remove(j)
    return out


def blocks(n, size):
    """n catalog lines in ceil(n / size) blocks of about equal size: (first, last + 1) of each."""
    k = max(1, math.ceil(n / size)) if size else 1
    cuts = [round(n * b / k) for b in range(k + 1)]
    return list(itertools.pairwise(cuts))


def pick(case, url):
    path = HERE / f"pick-{case}{TAG}.json"
    if path.exists():
        return reused(path, case)
    _key, instruction, target, style, _name = CASES[case]
    if MUSIC:
        target = round(song()["seconds"])
    cards = usable_cards(case)
    many = len({c["video"] for c in cards}) > 1
    # #416 (2026-11-09): where the cards were described again with a process (proc-<case>.json,
    # 关键帧卡片/process_cards.py), the catalog carries it instead of the summary: a long card's summary
    # kept its most striking part and dropped the steps on the way (the car pulling up at the cliff,
    # the torch in the cave), which is what joins one picture to the next.
    has_process = all("process" in c["answer"] for c in cards)
    what, what_note = ("过程", "（这张卡片里依次发生的事）") if has_process else ("概括", "")
    lines = []
    for c in cards:
        a = c["answer"]
        text = " → ".join(a["process"]) if has_process else a["summary"]
        where = f"视频{int(c['video'])} " if many else ""
        # No tags (标签盲测 4.0 against 2.7) and no highlight: it ranked the street and road cards that
        # set up the place and join scenes (3.1-3.2) under the story's (3.6-4.7), and they were left out.
        line = f"{c['id']} {where}{clock(c['start'])} {c['end'] - c['start']:.0f}秒 | {a.get('scene', '')} | {text}"
        words = said(c["id"], c["video"], c["start"], c["end"], SAID_CARD) if SPEECH else ""
        lines.append(line + (f" | 台词：「{words}」" if words else ""))
    spans = blocks(len(cards), CHUNK_LINES)
    k = len(spans)
    users, answers, costs, info, before = [], [], [], [], None
    for b, (lo, hi) in enumerate(spans, 1):
        seconds = target * (hi - lo) / len(cards)
        if k == 1:
            position, open_close = "", OPEN_CLOSE
            task, first_link = f"请按用户的要求，从这些卡片里设计一个约 {target} 秒的成片，按顺序写：", "第一部分写开场怎么进入；"
            if FREE:
                task = "请按用户的要求，从这些卡片里设计成片，时长、分几部分、用多少画面都由你按素材和要求决定，按顺序写："
        else:
            position = (f"\n素材较长，成片按拍摄时间先后分成 {k} 块分别规划，上面是第 {b} 块的卡片（全部 {len(cards)} 张里的第 "
                        f"{lo + 1}–{hi} 张，{clock(cards[lo]['start'])}–{clock(cards[hi - 1]['end'])}）。"
                        f"这一块在成片里约 {seconds:.0f} 秒，全片约 {target} 秒。\n")
            if before:
                last = before["parts"][-1]
                position += (f"上一块的设计：{before['design']}\n上一块的最后一部分「{last['name']}」：{last['summary']}；"
                             f"它的最后一个画面：{last['scenes'][-1]['query']}。\n")
            if b == 1:
                open_close = "- 这一块是成片的开头：开场先交代地点、时代和人物，不要跳切；这一块的结尾要能接到下一块，不用收束；"
            elif b < k:
                open_close = "- 这一块在成片中间：第一部分接上上一块的最后一个画面，不要重新开场；结尾要能接到下一块，不用收束；"
            else:
                open_close = f"- 这一块是成片的结尾：第一部分接上上一块的最后一个画面，不要重新开场；{ENDING}；"
            task = f"请按用户的要求，从这些卡片里设计成片里约 {seconds:.0f} 秒的这一块，按顺序写："
            first_link = "第一部分写开场怎么进入；" if b == 1 else "第一部分写怎么接上上一块的最后一个画面；"
        answer, cost, user = plan_block(url, instruction, style, cards[lo:hi], lines[lo:hi], many, what, what_note,
                                        seconds if k > 1 else target, position, open_close, task, first_link)
        users.append(user)
        answers.append(answer)
        costs.append(cost)
        info.append({"cards": [lo + 1, hi], "seconds": round(seconds, 1), "parts": len(answer["parts"]),
                     "design": answer["design"]})
        before = answer
    if k == 1:
        merged, cost, user = answers[0], costs[0], users[0]
    else:
        merged = {"design": "\n".join(f"第 {n} 块：{a['design']}" for n, a in enumerate(answers, 1)),
                  "opening": answers[0].get("opening", ""), "ending": answers[-1].get("ending", ""),
                  "unmet": [u for a in answers for u in a["unmet"]],
                  "parts": [part for a in answers for part in a["parts"]], "blocks": info}
        cost = {key: round(sum(c[key] or 0 for c in costs), 1) for key in ("seconds", "input", "output")} | {"calls": k}
        user = "\n\n".join(f"========== 第 {n} 块 ==========\n{u}" for n, u in enumerate(users, 1))
    # The order is the model's own (the design and the parts both told to follow shooting order); no
    # program sorting after it.
    out = {"system": MUSIC_SYSTEM if MUSIC else PICK_SYSTEM, "user": user, "answer": merged, "cost": cost,
           "descriptions": described(case), **({"music": MUSIC} if MUSIC else {})}
    write_json(out, path)
    print(case, "pick", cost, "分块", [b["cards"] for b in info], "每块段数", [b["parts"] for b in info], flush=True)
    return out


def plan_block(url, instruction, style, cards, lines, many, what, what_note, target, position, open_close, task,
               first_link):
    """Step one on one block of the catalog (all of it when it is short): (answer, cost, user)."""
    max_parts = max(2, min(8, int(target // 10)))     # B's part_limits: parts of 10 s at least, 2-8
    min_parts = fewest_parts(target)
    # The pictures of all parts together are held to one a PICTURE_SECONDS of the target (2026-11-09):
    # the Shanghai vlog's 6 parts of 4 pictures left 3.7 s a picture, and step 2 listed every card's
    # first three beats as they came; told 15, the planner wrote 19 and step 2 chose its beats. Only
    # the prompt says so. Trials: CUT_PARTS fixes the number of parts, CUT_PICTURES the cap (0: none).
    if os.environ.get("CUT_PARTS"):
        min_parts = max_parts = int(os.environ["CUT_PARTS"])
    parts_rule = (f"{min_parts} 个部分" if min_parts == max_parts
                  else f"{min_parts}–{max_parts} 个部分（至少 {min_parts} 个，不能超过 {max_parts} 个）")
    pictures = int(os.environ.get("CUT_PICTURES", round(target / PICTURE_SECONDS)))
    pictures_rule = f"，所有部分的画面加起来不超过 {pictures} 个" if pictures else ""
    if FREE:
        parts_rule, pictures_rule = "若干部分（分几部分由你定）", ""
    m = song()
    if m:
        parts_rule, pictures_rule = f"{len(m['parts'])} 个部分（和音乐的 {len(m['parts'])} 段一一对应）", ""
        position += music_note(m)
    total = sum(c["end"] - c["start"] for c in cards) / 60
    user = PICK.format(instruction=instruction, total_min=total, n=len(cards), where="所在视频 " if many else "", what=what, what_note=what_note,
                       said_note=" | 台词（这段时间里说的话，听写而来，可能有错）" if SPEECH else "",
                       catalog="\n".join(lines), structure=structure_of(style), parts_rule=parts_rule, pictures_rule=pictures_rule, position=position, open_close=open_close,
                       task=task, first_link=first_link)
    if FREE:
        user = user.replace(FREE_WEIGHT, FREE_SECONDS).replace(*FREE_SCENES)
    if m:
        user = user.replace(FREE_WEIGHT + "\n", "").replace(*MUSIC_SCENES)
    ids = [c["id"] for c in cards]
    # 2026-11-09 (the user): the opening and the ending each with a reason, written before the parts.
    schema = {"type": "object", "required": ["design", "opening", "ending", "unmet", "parts"], "properties": {
        "design": {"type": "string"}, "opening": {"type": "string"}, "ending": {"type": "string"},
        "unmet": {"type": "array", "items": {"type": "string"}},
        "parts": {"type": "array", "minItems": min_parts, "maxItems": max_parts, "items": {
            "type": "object", "required": ["name", "summary", "link", "weight", "scenes"], "properties": {
                "name": {"type": "string"}, "summary": {"type": "string"}, "link": {"type": "string"},
                "weight": {"type": "integer", "enum": list(range(1, 11))},
                "scenes": {"type": "array", "minItems": 1, "maxItems": 4, "items": {
                    "type": "object", "required": ["query", "cards"], "properties": {
                        "query": {"type": "string"},
                        "cards": {"type": "array", "minItems": 1, "maxItems": 3,
                                  "items": {"type": "string", "enum": ids}}}}}}}}}}
    if FREE:
        part = schema["properties"]["parts"]
        del part["minItems"], part["maxItems"], part["items"]["properties"]["scenes"]["maxItems"]
        part["items"]["properties"].pop("weight")
        part["items"]["properties"]["seconds"] = {"type": "integer", "minimum": 1}
        part["items"]["required"] = ["name", "summary", "link", "seconds", "scenes"]
    if m:
        part = schema["properties"]["parts"]
        part["minItems"] = part["maxItems"] = len(m["parts"])
        part["items"]["properties"]["scenes"]["maxItems"] = max(len(x["slots"]) for x in m["parts"])
        part["items"]["properties"].pop("weight")
        part["items"]["required"] = ["name", "summary", "link", "scenes"]
    system = MUSIC_SYSTEM if m else PICK_SYSTEM
    if PLANNER == "claude":
        answer, cost = ask_claude(system, user, schema)
    else:
        answer, cost = chat(url, system, user, schema, "outline")
    return answer, cost, user


def units(card, cap):
    """Each beat as it is cut: (beat number, start, seconds): from its key frame (the card's start for
    the first), its own length but at least SHORTEST, running on into the next beat, and at most cap."""
    beats, out = card["answer"]["beats"], []
    for k, b in enumerate(beats):
        start = card["start"] if k == 0 else card["times"][k]
        if card["end"] - start < SHORTEST:          # the card's last beat: start earlier
            start = max(card["start"], card["end"] - SHORTEST)
        length = min(cap, max(SHORTEST, b["end"] - start), card["end"] - start)
        out.append((k + 1, start, length))
    return out


def budget(picked, cards, target, cap):
    """Each part's seconds by its weight, each picture at least SHORTEST, and each picture's seconds
    (the part's shared evenly, none above what its cards can run): [[seconds, ...], ...]."""
    most = PICTURE_MOST
    room = [[min(most, sum(u[2] for cid in dict.fromkeys(sc["cards"]) if cid in cards for u in units(cards[cid], cap)))
             for sc in p["scenes"]] for p in picked["parts"]]
    floors = [SHORTEST * len(p["scenes"]) for p in picked["parts"]]
    spare = max(0.0, target - sum(floors))
    parts = share(spare, [max(0.0, sum(r) - f) for r, f in zip(room, floors, strict=True)],
                  [p.get("weight", p.get("seconds")) for p in picked["parts"]])
    return [[max(SHORTEST, x) for x in share(f + extra, r)] for f, extra, r in zip(floors, parts, room, strict=True)]


def shots(case, url):
    """Step two: for each picture of the outline, its beats in the planner's order of preference; the
    program then takes them to fill the picture's seconds. A plan made in blocks is ranked block by
    block, each call with that block's parts and seconds only."""
    path = HERE / f"shots-{case}{TAG}.json"
    if path.exists():
        return reused(path, case)
    _key, instruction, target, style, _name = CASES[case]
    cap = CAPS.get(style, CAP)
    picked = pick(case, url)["answer"]
    target = target_of(case, picked)
    cards = {c["id"]: c for c in usable_cards(case)}
    groups, at = [], 0
    for b in picked.get("blocks", []):
        groups.append((picked["parts"][at:at + b["parts"]], b["design"], b["seconds"]))
        at += b["parts"]
    if not groups:
        groups = [(picked["parts"], picked["design"], target)]
    seconds, raws, costs, users, system = [], [], [], [], ""
    for n, (parts, design, sec) in enumerate(groups, 1):
        extra = f"这是全片 {len(groups)} 块中的第 {n} 块，这一块约 {sec:.0f} 秒。\n" if len(groups) > 1 else ""
        slots = slotted(song(), parts) if MUSIC else None
        secs, raw, cost, system, user = rank_block(url, instruction, target, style, cap, parts, design, cards, sec,
                                                   extra, slots)
        seconds += secs
        raws.append(raw)
        costs.append(cost)
        users.append(user)
    if len(groups) == 1:
        raw, cost, user = raws[0], costs[0], users[0]
    else:
        raw = {"parts": [r for x in raws for r in x["parts"]], "unmet": [u for x in raws for u in x.get("unmet", [])]}
        cost = {key: round(sum(c[key] or 0 for c in costs), 1) for key in ("seconds", "input", "output")} | {"calls": len(groups)}
        user = "\n\n".join(f"========== 第 {n} 块 ==========\n{u}" for n, u in enumerate(users, 1))
    out = {"system": system, "user": user, "raw": raw, "seconds": seconds, "cap": cap, "cost": cost, "step2": STEP2,
           "descriptions": described(case), **({"slots": slotted(song(), picked["parts"]), "music": MUSIC} if MUSIC else {}),
           "answer": {"parts": [{"name": p["name"], "summary": p["summary"], "pictures": r["pictures"]}
                                for p, r in zip(picked["parts"], raw["parts"], strict=True)],
                      "unmet": raw.get("unmet", [])}}
    write_json(out, path)
    print(case, "shots", cost, "画面秒数", [[round(x, 1) for x in secs] for secs in seconds],
          "每个画面列的节拍数", [[len(pic["beats"]) for pic in r["pictures"]] for r in raw["parts"]], flush=True)
    return out


def rank_block(url, instruction, target, style, cap, parts, design, cards, seconds_of, extra, slots=None):
    """Step two on some parts (all of them for a plan in one block): (seconds, raw, cost, system, user).
    With slots (第六版), each picture's seconds are its music slots'."""
    seconds = ([[round(sum(b - a for a, b in pic), 2) for pic in part] for part in slots] if slots
               else budget({"parts": parts}, cards, seconds_of, cap))
    rules = f"\n剪法：{STYLES[style]}" if STYLES.get(style) else ""
    lines, part_items = [], []
    for n, (p, secs) in enumerate(zip(parts, seconds, strict=True), 1):
        lines.append(f"段落{n}「{p['name']}」：{p['summary']}")
        lines.append(f"  衔接：{p['link']}")
        items = []
        for k, (sc, sec) in enumerate(zip(p["scenes"], secs, strict=True), 1):
            shots_here = len(slots[n - 1][k - 1]) if slots else 0
            lines.append(f"  画面{k}：{sc['query']}（约 {sec:.0f} 秒" + (f"，{shots_here} 个镜头）" if slots else "）"))
            ids = []
            for cid in dict.fromkeys(sc["cards"]):
                if cid not in cards:
                    continue
                c = cards[cid]
                a = c["answer"]
                lines.append(f"    卡片 {cid}（{clock(c['start'])}–{clock(c['end'])}，{a.get('scene', '')}）：{a['summary']}")
                for (j, _start, length), b in zip(units(c, cap), a["beats"], strict=True):
                    ids.append(f"{cid}/{j}")
                    # #416 (2026-11-09): no 【亮点】 marks; two marked faces in the car came before the
                    # rear-view mirror the picture asked for, and the picture had room for two beats.
                    words = said(cid, c["video"], _start, _start + length, SAID_BEAT) if SPEECH else ""
                    lines.append(f"      {cid}/{j} {b['shot_size']} {length:.0f}秒 {b['what']}" + (f" 台词：「{words}」" if words else ""))
            need = min(len(ids), (shots_here if slots else math.ceil(sec / cap)) + 1)
            items.append({"type": "object", "required": ["beats", "shows", "link"], "properties": {
                "beats": {"type": "array", "minItems": max(1, need), "maxItems": max(1, min(len(ids), need + 4)),
                          "items": {"type": "string", "enum": ids}},
                "shows": {"type": "string"}, "link": {"type": "string"}}})
        part_items.append({"type": "object", "required": ["pictures"], "properties": {
            "pictures": {"type": "array", "minItems": len(items), "maxItems": len(items), "prefixItems": items}}})
    user = (f"指令：{instruction}\n成片时长：约 {target} 秒\n体裁：{style}{rules}\n大纲：{design}\n{extra}"
            "段落和画面：\n" + "\n".join(lines))
    schema = {"type": "object", "required": ["parts", "unmet"], "properties": {
        "parts": {"type": "array", "minItems": len(part_items), "maxItems": len(part_items), "prefixItems": part_items},
        "unmet": {"type": "array", "items": {"type": "string"}}}}
    system = SHOTS.format(target=target, shortest=SHORTEST, cap=cap, said_note="、台词（听写而来，可能有错）" if SPEECH else "")
    if slots:
        system = system.replace(f"程序按你排的顺序，从每个节拍开头取 {SHORTEST:g}–{cap:g} 秒，取到凑满这个画面的秒数为止。成片约 {target:g} 秒，带原声，不加配乐和字幕。",
                                f"程序按音乐把每个画面切成几个镜头，镜头卡在小节上；按你排的顺序，每个镜头用一个节拍，从节拍开头取。成片约 {target:g} 秒，配音乐，不用原声。"
                                "高潮段的画面只挑全景、远景的节拍，第一个画面的第一个节拍必须是全景或远景，挑最开阔的那个；"
                                # 2026-11-09: step one stays coarse; for "walks to the water, sits on a log, turns and
                                # smiles" given one 5 s shot, step two put beat 1 (the walk) first and the smile
                                # (beat 3) never came. Picking the moment is step two's work.
                                "画面只有一两个镜头时，放不下卡片里的整个过程：不要默认挑第一个节拍，要从这张卡片的全部节拍里挑出最关键、最精彩的那一刻排在第一。"
                                "高潮段前的最后一个画面，在符合这个画面内容的节拍里，挑最处在“还没”状态的：还没到、还没看见、还没打开、还没落地。")
    if STEP2 == "claude":
        # The CLI's strict schemas have no prefixItems: each part and picture becomes a named field (段落1,
        # 画面1, ...), so each picture keeps its own beats to choose from, and the answer is turned back.
        named = {"type": "object", "required": ["parts", "unmet"], "properties": {
            "unmet": schema["properties"]["unmet"],
            "parts": {"type": "object", "required": [f"段落{n}" for n in range(1, len(part_items) + 1)], "properties": {
                f"段落{n}": {"type": "object", "required": [f"画面{k}" for k in range(1, len(pi["properties"]["pictures"]["prefixItems"]) + 1)],
                            "properties": {f"画面{k}": item for k, item in enumerate(pi["properties"]["pictures"]["prefixItems"], 1)}}
                for n, pi in enumerate(part_items, 1)}}}}
        answer, cost = ask_claude(system, user, named)
        raw = {"unmet": answer["unmet"],
               "parts": [{"pictures": [answer["parts"][f"段落{n}"][f"画面{k}"]
                                       for k in range(1, len(pi["properties"]["pictures"]["prefixItems"]) + 1)]}
                         for n, pi in enumerate(part_items, 1)]}
    else:
        raw, cost = chat(url, system, user, schema, "shots", max_tokens=4000)
    return seconds, raw, cost, system, user


def filled(planned, picked, cards, cap, most):
    """The clips: each picture filled from its beats in order (a beat already used, or overlapping a
    clip taken, is passed over), a beat taken while it brings the picture closer to its seconds; what a
    picture leaves over or short carried to the next; a picture's clips in time order, and clips of one
    card that meet joined into one."""
    clips, carry, taken, seen_whats = [], 0.0, [], []
    for p, (part, step1) in enumerate(zip(planned["answer"]["parts"], picked["parts"], strict=True), 1):
        for k, (pic, sec) in enumerate(zip(part["pictures"], planned["seconds"][p - 1], strict=True), 1):
            # What earlier pictures left short is carried, but no picture over the longest a picture runs.
            # What one runs over is not taken from the next (2026-11-09): a picture's first beat is taken
            # whole (6 s against the 3.6 s of a Shanghai picture), and the 3 s it ran over left the next
            # picture, the car with the navigation screen that joins two parts, 0.9 s and no beat; held
            # to SHORTEST instead, the debt ran on and squeezed the last part. cut() levels the longest
            # clips down to the target instead.
            owed = sec + carry
            want, mine, total = min(owed, most), [], 0.0
            # The planner's beats first; when they run out short of the picture's seconds, the other
            # beats of the picture's cards, nearest the planner's first beat first (Shanghai's beats are
            # about 2 s, and two listed beats left a 4.8 s picture short, and the cut 11 s under).
            listed = [b for b in dict.fromkeys(pic["beats"]) if b.rsplit("/", 1)[0] in cards]
            pool = [f"{cid}/{n}" for cid in dict.fromkeys(b.rsplit("/", 1)[0] for b in listed)
                    for n in range(1, len(cards[cid]["answer"]["beats"]) + 1)]
            if listed:
                c0, j0 = listed[0].rsplit("/", 1)
                anchor = cards[c0]["times"][int(j0) - 1]
                pool.sort(key=lambda b: abs(cards[b.rsplit("/", 1)[0]]["times"][int(b.rsplit("/", 1)[1]) - 1] - anchor))
            for beat in listed + [b for b in pool if b not in listed]:
                if total >= want - SHORTEST / 2:
                    break
                cid, j = beat.rsplit("/", 1)
                if cid not in cards:
                    continue
                _n, start, length = units(cards[cid], cap)[int(j) - 1]
                end = start + length
                if any(v == cid and start < b - 0.05 and end > a + 0.05 for v, a, b in taken):
                    continue
                # A beat described the same as one already taken (01-012/2 and /4, a man looking up at the
                # sky twice in a row) repeats it: passed over.
                what = cards[cid]["answer"]["beats"][int(j) - 1]["what"]
                if any(difflib.SequenceMatcher(None, what, w).ratio() > SAME_TEXT for w in seen_whats):
                    continue
                if mine and total + length / 2 > want:
                    continue
                seen_whats.append(what)
                mine.append({"card": cid, "beats": [int(j)], "start": start, "end": end,
                             "key": cards[cid]["times"][int(j) - 1]})
                taken.append((cid, start, end))
                total += length
            carry = max(0.0, owed - total)
            mine.sort(key=lambda c: (c["card"], c["start"]))
            joined = []
            for c in mine:
                if joined and joined[-1]["card"] == c["card"] and c["start"] <= joined[-1]["end"] + 0.05:
                    joined[-1]["end"] = max(joined[-1]["end"], c["end"])
                    joined[-1]["beats"] += c["beats"]
                else:
                    joined.append(c)
            joined.sort(key=lambda c: (cards[c["card"]]["start"], c["start"]))
            clips.extend(c | {"part": p, "partName": part["name"], "picture": k, "query": step1["scenes"][k - 1]["query"],
                              "shows": pic["shows"], "link": pic["link"], "share": round(c["end"] - c["start"], 2)}
                         for c in joined)
    return clips


def cut(case):
    if MUSIC:
        return cut_to_music(case)
    key, instruction, target, style, name = CASES[case]
    cap = CAPS.get(style, CAP)
    d = next((Path.home() / "A-NAS-datasets" / "cache" / f"video-moments-{key}").glob("embeddinggemma*"))
    ix = moments.Index.load(str(d / "index.json"), str(d / "frames.npz"))
    cards = {c["id"]: c for c in load(case)}
    planned = reused(HERE / f"shots-{case}{TAG}.json", case)
    picked = reused(HERE / f"pick-{case}{TAG}.json", case)["answer"]
    # A freely planned cut is as long as the seconds step two handed out, not the outline's own sum: the
    # Shanghai outline asked 483 s of 28 pictures held to PICTURE_MOST, 218 s, and topping up toward 483
    # grew a shot to 22 s.
    target = sum(map(sum, planned["seconds"])) if target_of(case, picked) != CASES[case][2] else CASES[case][2]
    clips = filled(planned, picked, cards, cap, PICTURE_MOST)
    # Over the target: the longest clips cut down to one level first, none under SHORTEST (B's level).
    lengths = [c["end"] - c["start"] for c in clips]
    if sum(lengths) > target + 0.5:
        lo, hi = SHORTEST, max(lengths)
        for _ in range(50):
            mid = (lo + hi) / 2
            if sum(max(SHORTEST, min(x, mid)) if x > SHORTEST else x for x in lengths) > target:
                hi = mid
            else:
                lo = mid
        for c, x in zip(clips, lengths, strict=True):
            c["end"] = c["start"] + (max(SHORTEST, min(x, lo)) if x > SHORTEST else x)
            c["share"] = round(c["end"] - c["start"], 2)
    out_clips, taken = [], []
    for c in clips:
        card = cards[c["card"]]
        video = card["video"]
        words = sorted(ix.words.get(video, []))
        start, end = c["start"], c["end"]
        length = end - start
        mine = [(a, b) for v, a, b in taken if v == video]

        def free(a, b, mine=mine, card=card, length=length):
            return (card["start"] - 0.01 <= a < b <= card["end"] + 0.01 and SHORTEST - 0.01 <= b - a <= length + WIDEN
                    and all(b <= x or a >= y for x, y in mine))

        a = paused(start, words, (start - WIDEN, end - SHORTEST), lambda t, e=end: free(t, e))
        a = start if a is None else a
        b = paused(end, words, (a + SHORTEST, end + WIDEN), lambda t, u=a: free(u, t))
        b = end if b is None else b
        # Whole shots, none shorter than SHORTEST, by the index's cuts and the ones it missed.
        source = str(VIDEOS / ix.videos[video]["file"])
        # Scanned as far as topping up may reach, so that a grown end lands on a cut it can see.
        # (from before the card: a cut is seen only with half a second of frames either side of it)
        reach = (max(0.0, min(a, card["start"]) - 4), min(card["end"] + 4, b + 16))
        known = [x for x, _y in ix.videos[video]["shots"] if reach[0] - 1 < x < reach[1] + 1]
        cuts = known + [x for x in scene_cuts(source, *reach) if all(abs(x - k) > 0.3 for k in known)]
        a2, b2 = shot_safe(a, b, c["key"], cuts, card)
        if b2 - a2 >= FLASH:          # a clip the cuts would leave under a second keeps its span instead
            a, b = a2, b2
        c["_cuts"] = sorted(cuts)
        a, b = math.ceil(a * 100) / 100, math.floor(b * 100) / 100
        taken.append((video, a, b))
        k = c["beats"][0]
        beats = card["answer"]["beats"]
        out_clips.append(c | {"video": video, "start": a, "end": b, "beat": f"{c['card']}/" + "+".join(map(str, c["beats"])),
                              "weight": f"画面{c['picture']}", "frame": card["frames"][k - 1],
                              "shotSize": "、".join(dict.fromkeys(beats[j - 1]["shot_size"] for j in c["beats"])),
                              "what": "；".join(beats[j - 1]["what"] for j in c["beats"])})
    topped_up(out_clips, cards, target)
    whole_sentences(out_clips, cards, ix)
    for c in out_clips:
        cuts = c.pop("_cuts", [])
        words = sorted(ix.words.get(c["video"], []))
        c["start"], c["end"] = off_words(c["start"], c["end"], words, cuts)
    t0 = time.perf_counter()
    made = render.render_clips([(str(VIDEOS / ix.videos[c["video"]]["file"]), c["start"], c["end"]) for c in out_clips],
                               str(HERE), ending=True)
    final = HERE / f"{case}-{name}-剪法25{TAG}.mp4"
    Path(made).replace(final)
    out = {"case": case, "instruction": instruction, "target": target, "style": style, "cap": cap,
           "seconds": round(sum(c["end"] - c["start"] for c in out_clips), 2), "clips": out_clips,
           "plannedShots": sum(len(pic["beats"]) for part in planned["answer"]["parts"] for pic in part["pictures"]),
           "renderSeconds": round(time.perf_counter() - t0, 1), "cut": str(final)}
    write_json(out, HERE / f"cut-{case}{TAG}.json")
    L = [c["end"] - c["start"] for c in out_clips]
    print(case, "cut", out["seconds"], "秒", len(out_clips), "个镜头，最短", round(min(L), 2), "最长", round(max(L), 2), flush=True)


PIECE_LEAST = 1.5    # seconds: no shot of a music cut shorter, the slots' and the fills'
SLOW_MOST = 2.5      # times: the ending shot drawn out at most this much to fill its slot
# Sources tagged upside down over upright pixels (V5/rotation.json), turned back in every render of this
# module, narrative or music: set once here, not per cut, so no cut can miss them.
ROTATION = {k: set(v) for k, v in read_json(V5 / "rotation.json").items() if k != "about"} if (V5 / "rotation.json").exists() else {}
render.TURNED_BACK |= {str(f) for names in ROTATION.values() for n in names for f in VIDEOS.glob(f"*/{n}")}


def cut_to_music(case):
    """第六版: each picture's slots filled in its beats' order, one beat a slot from its key frame, the clip
    exactly the slot's frames (moved earlier in its card when the card ends first; a card too short leaves
    the rest of the slot to the next beat, but no piece under half a second); the song's excerpt for sound."""
    key, instruction, _target, style, name = CASES[case]
    m = song()
    d = next((Path.home() / "A-NAS-datasets" / "cache" / f"video-moments-{key}").glob("embeddinggemma*"))
    ix = moments.Index.load(str(d / "index.json"), str(d / "frames.npz"))
    cards = {c["id"]: c for c in load(case)}
    planned = reused(HERE / f"shots-{case}{TAG}.json", case)
    picked = reused(HERE / f"pick-{case}{TAG}.json", case)["answer"]
    fps = render.FPS
    spans = held.Spans(HERE / f"held-{case}.json")
    # A slot its picture's cards cannot fill (川西's opening stream, ch-001, is 2.3 s against a 3.2 s
    # slot) is filled on from the cards next to them in shooting order, the next first: a slot short of
    # its frames moves every later cut off the bar lines.
    catalog = [c["id"] for c in usable_cards(case)]

    def neighbours(ids):
        at = sorted(catalog.index(i) for i in ids if i in catalog)
        if not at:
            return []
        out = []
        for d in range(1, len(catalog)):
            for k in (at[-1] + d, at[0] - d):
                if 0 <= k < len(catalog) and catalog[k] not in out and catalog[k] not in ids:
                    out.append(catalog[k])
            if len(out) >= 6:
                break
        return [f"{cid}/{n}" for cid in out for n in range(1, len(cards[cid]["answer"]["beats"]) + 1)]

    clips, taken, short = [], [], []
    many = len(planned["answer"]["parts"])
    for p, (part, step1, slots_p) in enumerate(zip(planned["answer"]["parts"], picked["parts"], planned["slots"], strict=True), 1):
        level = m["parts"][p - 1]["level"]
        for k, (pic, slots) in enumerate(zip(part["pictures"], slots_p, strict=True), 1):
            # The ending picture's chosen beat closes the cut: its slots are filled from the last, so that beat
            # takes the final slot, and what fills the rest of that slot goes before it (新西兰 fortnight-6
            # ended on two neighbours after its starry-sky ending, ne-154, held 2 s of a 5 s slot).
            ending = p == many and k == len(part["pictures"])
            mine = len(clips)
            listed = [b for b in dict.fromkeys(pic["beats"]) if b.rsplit("/", 1)[0] in cards]
            pool = [f"{cid}/{n}" for cid in dict.fromkeys(b.rsplit("/", 1)[0] for b in listed)
                    for n in range(1, len(cards[cid]["answer"]["beats"]) + 1)]
            if listed:
                c0, j0 = listed[0].rsplit("/", 1)
                anchor = cards[c0]["times"][int(j0) - 1]
                pool.sort(key=lambda b: abs(cards[b.rsplit("/", 1)[0]]["times"][int(b.rsplit("/", 1)[1]) - 1] - anchor))
            order = listed + [b for b in pool if b not in listed]
            order += neighbours(list(dict.fromkeys(b.rsplit("/", 1)[0] for b in order)))
            for a, b in reversed(slots) if ending else slots:
                need, first = round(b * fps) - round(a * fps), True
                for beat in order:
                    if need <= 0:
                        break
                    cid, j = beat.rsplit("/", 1)
                    c = cards[cid]
                    # The key frame (what the description saw) a third into the shot, not at its start.
                    seen = c["times"][int(j) - 1]
                    if any(v == cid and x - 0.01 <= seen < y - 0.01 for v, x, y in taken):
                        continue
                    # Only the stretch around the key frame that nothing passes in front of (held.py).
                    file = str(VIDEOS / ix.videos[c["video"]]["file"])
                    lo, hi = spans.get(beat, file, seen, max(c["start"], seen - need / fps), min(c["end"], seen + need / fps),
                                       file in render.TURNED_BACK)
                    start = max(lo, seen - need / fps / 3)
                    start = max([start] + [y for v, _x, y in taken if v == cid and y <= seen + 0.01])
                    room = min([hi] + [x for v, x, _y in taken if v == cid and x > start])
                    frames = min(need, int((room - start) * fps))
                    slow = 1.0
                    if frames < need and first:      # one beat for the whole slot: start earlier in the card
                        earliest = max([lo] + [y for v, _x, y in taken if v == cid and y <= start + 0.01])
                        if room - earliest >= need / fps:
                            start, frames = room - need / fps, need
                        else:
                            start, frames = earliest, min(need, int((room - earliest) * fps))
                    shown = frames
                    if ending and first and a == slots[-1][0] and 0 < frames < need:
                        # The ending's own beat, too short for the cut's last slot: drawn out (a still or a far
                        # view hardly shows it), not followed by its neighbours (ne-154's 2 s of a starry sky in a
                        # 5 s slot came after a coach and a helicopter).
                        shown = min(need, int(frames * SLOW_MOST))
                        slow = shown / frames
                    # No piece under PIECE_LEAST (a 0.3 s flash of the next card): a piece that would leave less
                    # than that gives some of its own seconds to the piece after it.
                    least = round(PIECE_LEAST * fps)
                    if shown < need and need - shown < least and shown - (least - (need - shown)) >= least:
                        shown -= least - (need - shown)
                        frames = min(frames, shown)
                        slow = shown / frames
                    if shown < min(need, least):
                        continue
                    end = start + frames / fps
                    clips.append({"card": cid, "beats": [int(j)], "start": start, "end": end, "video": c["video"],
                                  "key": c["times"][int(j) - 1], "part": p, "partName": part["name"], "picture": k,
                                  "level": level, "slot": [a, b], "query": step1["scenes"][k - 1]["query"],
                                  "shows": pic["shows"], "link": pic["link"], "share": round(shown / fps, 3)}
                                 | ({"slow": round(slow, 3)} if slow != 1 else {}))
                    taken.append((cid, start, end))
                    need -= shown
                    first = False
                if need > 0:
                    short.append((p, k, a, b, need))
            if ending and len(clips) > mine:
                last = clips[mine]       # the final slot's first piece: the chosen beat; its fills in shooting order
                rest = sorted(clips[mine + 1:], key=lambda c: (c["slot"][0], catalog.index(c["card"]) if c["slot"] == last["slot"] and c["card"] in catalog else 0))
                clips[mine:] = [*rest, last]
    for c in clips:
        beats = cards[c["card"]]["answer"]["beats"]
        c |= {"beat": f"{c['card']}/{c['beats'][0]}", "frame": cards[c["card"]]["frames"][c["beats"][0] - 1],
              "shotSize": beats[c["beats"][0] - 1]["shot_size"], "what": beats[c["beats"][0] - 1]["what"],
              "weight": f"画面{c['picture']}"}
    t0 = time.perf_counter()
    made = render.render_clips([(str(VIDEOS / ix.videos[c["video"]]["file"]), c["start"], c["end"], c.get("slow", 1.0))
                                for c in clips], str(HERE), ending=True)
    seconds = sum((c["end"] - c["start"]) * c.get("slow", 1.0) for c in clips)
    final = HERE / f"{case}-{name}-第六版{TAG}.mp4"
    start = m["excerpt"][0]
    subprocess.run(["ffmpeg", "-hide_banner", "-v", "error", "-y", "-i", made, "-ss", f"{start:.3f}", "-t", f"{seconds:.3f}",  # noqa: S607
                    "-i", m["song"], "-map", "0:v", "-map", "1:a", "-c:v", "copy", "-c:a", "aac", "-b:a", "192k",
                    "-af", f"afade=t=in:d=0.3,afade=t=out:st={max(0.0, seconds - 2.5):.3f}:d=2.5", "-shortest", str(final)],
                   check=True)
    Path(made).unlink()
    out = {"case": case, "instruction": instruction, "target": round(m["seconds"]), "style": style, "music": MUSIC,
           "seconds": round(seconds, 2), "clips": clips, "short": short,
           "plannedShots": sum(len(s) for part in planned["slots"] for s in part),
           "renderSeconds": round(time.perf_counter() - t0, 1), "cut": str(final)}
    write_json(out, HERE / f"cut-{case}{TAG}.json")
    print(case, "cut", out["seconds"], "秒", len(clips), "个镜头，槽", out["plannedShots"], "个，没填满", short, flush=True)


# Cuts the index missed: FFmpeg's scene score finds none in the film's black-backed stretch (437-719 s)
# and scored a plain cut 0.24 against its 0.25 (361.14). Frames at CUT_FPS, grey and divided by their
# own brightness so that a cut between two dark shots shows; a change counts as a cut when the 0.5 s
# either side differ as much (CUT_STEP), so rain and lightning flicker does not (238.7-239.6 s).
CUT_FPS, CUT_SIZE, CUT_STEP, CUT_SIDE = 12, (64, 36), 0.35, 6
ISOLATE = 0.6    # a change with another within this is motion, not a cut (Shanghai: 0.25 apart; film cuts 0.84 apart)
FLASH = 1.0      # a piece of another shot shorter than this at a clip's end is a flash: cut off
_cut_cache = {}


def scene_cuts(file, a, b):
    """Cut times between a and b (seconds) in file, by the brightness-normalised frame difference."""
    import numpy as np
    key = (file, round(a, 1), round(b, 1))
    if key in _cut_cache:
        return _cut_cache[key]
    w, h = CUT_SIZE
    raw = subprocess.run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-ss", f"{max(0.0, a):.3f}", "-t", f"{b - a:.3f}",  # noqa: S607
                          "-i", file, "-vf", f"fps={CUT_FPS},scale={w}:{h},format=gray", "-f", "rawvideo", "-"],
                         capture_output=True, check=True).stdout
    f = np.frombuffer(raw, np.uint8).reshape(-1, h, w).astype(np.float32)
    out = []
    if len(f) > 2:
        n = f / (f.reshape(len(f), -1).mean(1)[:, None, None] + 4.0)
        d = np.abs(n[1:] - n[:-1]).reshape(len(f) - 1, -1).mean(1)
        for i, x in enumerate(d):
            j = i + 1
            if x < CUT_STEP or j - CUT_SIDE < 0 or j + CUT_SIDE > len(n):
                continue
            far = float(np.abs(n[j:j + CUT_SIDE].mean(0) - n[j - CUT_SIDE:j].mean(0)).mean())
            if far >= CUT_STEP:
                t = max(0.0, a) + j / CUT_FPS
                if out and t - out[-1][0] < 0.25:          # one cut seen over two frames
                    if x > out[-1][1]:
                        out[-1] = (t, float(x))
                    continue
                out.append((t, float(x)))
    # A real cut stands alone: changes that come in a run, under ISOLATE apart, are the camera moving
    # through a busy street (Shanghai 02-062: one every 0.25 s for 7 s), not cuts.
    times = [t for t, _x in out]
    _cut_cache[key] = [t for n, t in enumerate(times)
                       if all(abs(t - u) >= ISOLATE for m, u in enumerate(times) if m != n)]
    return _cut_cache[key]


WORD_REACH = 0.6   # an end inside a word moves to the word's edge when it is this near and no cut is crossed


def off_words(a, b, words, cuts):
    """a and b, each that falls inside a word moved to that word's nearer edge within WORD_REACH, without
    crossing a cut (the clean ends and topping up look at the picture only, and cut words in two)."""
    def crosses(t, x, is_start):
        """Whether moving an end from t to x takes in any of another shot: past a cut, or out across one
        the end sits on (a start moved earlier from a cut, an end moved later from one)."""
        lo, hi = min(t, x), max(t, x)
        outward = (x < t) if is_start else (x > t)
        return any((lo - 0.02 <= c <= hi + 0.02) if outward else (lo + 0.02 < c < hi - 0.02) for c in cuts)

    def moved(t, is_start):
        for w0, w1 in words:
            if w0 + 0.03 < t < w1 - 0.03:
                options = sorted([w1 + 0.02, w0 - 0.02], key=lambda x: abs(x - t))
                for x in options:
                    if abs(x - t) <= WORD_REACH and not crosses(t, x, is_start):
                        return x
                return t
        return t
    a2, b2 = moved(a, True), moved(b, False)
    return (math.floor(a2 * 100) / 100, math.ceil(b2 * 100) / 100) if b2 - a2 >= FLASH else (a, b)


SENTENCE_MORE = 4.0   # a clip may run this much past its share to take a sentence whole


def whole_sentences(clips, cards, ix):
    """Each end inside a sentence moved out of it (2026-11-09): out to take the sentence whole (方案 B's
    whole_sentences, moments.Index._whole), inside the card, clear of the other clips and at most
    SENTENCE_MORE past the clip's share; where that will not do, back to before the sentence when SHORTEST
    stays. Pauses of a second (paused) were too rare to find: in the free cuts 9 of the film's 64 ends and
    31 of Shanghai's 72 fell inside a sentence."""
    for c in clips:
        card = cards[c["card"]]
        others = [(o["start"], o["end"]) for o in clips if o is not c and o["video"] == c["video"]]

        def allowed(a, b, c=c, card=card, others=others):
            return (card["start"] - 0.01 <= a < b <= card["end"] + 0.01 and b - a <= c["share"] + SENTENCE_MORE
                    and all(b <= x or a >= y for x, y in others))

        a, b = ix._whole(c["video"], c["start"], c["end"], allowed)
        for run in ix.sentences(c["video"]):
            s0, s1 = run[0][0], run[-1][1]
            if s0 + 0.15 < b < s1 - 0.15 and s0 - a >= SHORTEST:
                b = s0 - 0.05
            if s0 + 0.15 < a < s1 - 0.15 and b - s1 >= SHORTEST:
                a = s1 + 0.05
        c["start"], c["end"] = a, b


def topped_up(clips, cards, target):
    """Clips the clean ends and pauses left short of their shares grown back at their ends, a whole shot
    at a time (to the next cut, inside the card, clear of the other clips), the shortest-changed first,
    until the cut reaches the target (#416: the surf vlog's 1-2 s shots left a 60 s cut at 49 s)."""
    def total():
        return sum(c["end"] - c["start"] for c in clips)

    for _round in range(40):
        if total() >= target - 0.5:
            return
        grown = False
        for c in sorted(clips, key=lambda c: (c["end"] - c["start"]) - c["share"]):
            need = target - total()
            if need < 0.5 or c["end"] - c["start"] >= c["share"] + 1.0:
                continue
            card = cards[c["card"]]
            others = [(o["start"], o["end"]) for o in clips if o is not c and o["video"] == c["video"]]
            after = [x for x in c["_cuts"] if x > c["end"] + 0.05] + [card["end"]]
            end = min(after)
            if end - c["end"] < FLASH and end < card["end"] - 0.05:
                later = [x for x in after if x > end + 0.05]
                end = min(later) if later else card["end"]
            limit = min(c["end"] + max(need, SHORTEST) + 1.0, card["end"])
            if end > limit:               # the next shot's start is out of reach: stop inside it only
                last_cut = max([x for x in c["_cuts"] if x <= limit] + [c["end"]])   # with SHORTEST of it
                end = limit if limit - last_cut >= SHORTEST else last_cut
            if end - c["end"] < FLASH or any(end > x and c["start"] < y for x, y in others if x >= c["end"] - 0.05):
                continue
            c["end"] = math.floor(end * 100) / 100
            grown = True
        if not grown:
            return


def shot_safe(a, b, key, cuts, card):
    """a-b with clean ends (#416): the cuts inside it are the film's own and stay; only its first and
    last pieces are looked at. An end piece under SHORTEST that holds the key frame grows to SHORTEST
    within its shot (three suspects for 1.4 s taken whole); another end piece under FLASH is cut off
    (a glimpse of the shot before), and one between FLASH and SHORTEST grows to SHORTEST when its shot
    allows (a face for 1.1 s after a mirror becomes 2 s), else is cut off."""
    lo_card, hi_card = card["start"], card["end"]
    cuts = sorted(c for c in cuts if lo_card < c < hi_card)   # a cut 0.04 s into a card is a frame of the shot before
    # A key frame just before a cut (a 2 s window's middle, 361.0 for a face from 361.17) is the next shot's.
    soon = [c for c in cuts if key < c <= key + 0.5]
    if soon:
        key = soon[0] + 0.01
    inner = [c for c in cuts if a + 0.01 < c < b - 0.01]
    if not inner:
        return a, b
    first, last = inner[0], inner[-1]
    shot_start = max([c for c in cuts if c <= a + 0.05], default=lo_card)
    shot_end = min([c for c in cuts if c >= b - 0.05], default=hi_card)
    if first - a < SHORTEST:
        if key < first and first - max(shot_start, first - SHORTEST) >= FLASH:
            a = max(shot_start, first - SHORTEST)
        elif first - a >= FLASH and first - shot_start >= SHORTEST:
            a = first - SHORTEST
        else:
            a = first
    if b - last < SHORTEST:
        if key >= last and min(shot_end, last + SHORTEST) - last >= FLASH:
            b = min(shot_end, last + SHORTEST)
        elif b - last >= FLASH and shot_end - last >= SHORTEST:
            b = last + SHORTEST
        else:
            b = last
    return max(lo_card, a), min(hi_card, b)


PAUSE_LONG = 1.0   # an end inside speech moves to a pause at least this long (#416: half sentences cut)


def paused(t, words, reach, ok):
    """t when it is not inside speech (in a word, or between two words less than PAUSE_LONG apart);
    else the middle of the nearest pause of PAUSE_LONG or more within reach that ok passes; None
    when there is none."""
    inside = any(a - 0.05 < t < b + 0.05 for a, b in words) or any(
        b <= t <= c and c - b < PAUSE_LONG for (_a, b), (c, _d) in itertools.pairwise(words))
    if not inside:
        return t
    gaps = [(b + c) / 2 for (_a, b), (c, _d) in itertools.pairwise(words) if c - b >= PAUSE_LONG]
    if words:
        gaps += [words[0][0] - PAUSE_LONG / 2, words[-1][1] + PAUSE_LONG / 2]
    near = sorted((g for g in gaps if reach[0] <= g <= reach[1] and ok(g)), key=lambda g: abs(g - t))
    return near[0] if near else None


def main():
    url = sys.argv[1]
    cases = sys.argv[2:] or list(CASES)
    for case in cases:
        titles(case, url)
    for case in cases:
        shots(case, url)
        cut(case)
    print("ALL-DONE", flush=True)


if __name__ == "__main__":
    main()
