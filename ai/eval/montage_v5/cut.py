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
          part's link, without highlight marks (MARK_HIGHLIGHTS).
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
import subprocess
import sys
import time
from pathlib import Path

import requests

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))  # ai/, for montage
from paths import B_DIR, CARDS, CUTS as HERE, read_json, write_json

from montage import look, moments, render
from montage.styles import STYLES

VIDEOS = Path.home() / "A-NAS-datasets" / "videos"
CASES = {"01": ("case01", "剪成一条悬疑感的电影预告片，保留关键对白，不剧透结局", 60, "预告片", "侦探片预告"),
         "02": ("shanghai", "按时间顺序讲完博主在上海的这一天，保留说话的关键片段", 90, "旅行", "上海vlog"),
         "04": ("case04", "把两集合成一条深圳旅行故事，以冲浪为高潮", 60, "旅行", "深圳冲浪")}
# A frame is 报幕 when a title (look.TITLES) is laid over a plain picture, one level over at least
# PLAIN of it (credits on black: 0.84-0.99), or when the title is the film's name (片名) over
# anything ("Meridian" over a street, 0.4). A shop sign read as a logo (全家, 0.10-0.15) and a
# vlog's subtitles read as 时间地点说明 (0.04-0.25) are over busy pictures. Two of three frames make
# a card 报幕.
PLAIN = 0.6
PER_CARD = 5.0      # about one card a 5 s of the cut (方案 B)
PER_SHOT = 3.0      # about one shot a 3 s
WIDEN = 3.0         # a clip widened to whole sentences runs at most this much past its share


# Each run of a case is a take: CUT_TAG ("-1", "-2") names its files, and its seed varies the plans.
import os  # noqa: E402

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
    cards = [c for c in read_json(CARDS / f"cards-{case}.json").values() if "answer" in c]
    return cards


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


def usable_cards(case):
    marks = read_json(HERE / f"titles-{case}.json")
    return [c for c in load(case) if not marks[c["id"]]["title"]
            and not any(a - 0.01 <= c["start"] < b - 0.01 for a, b in LEFT_OUT.get((case, c["video"]), []))]


# Step one is 方案 B's outline request (instruct.REQUEST_PROMPT) on our cards, the join rules first so
# that the plan is made with them: design -> unmet -> parts with weights and the pictures each wants;
# no materials list (the parts name their cards, and a list kept cards it missed out of the parts); no pace (the program does not cut by it) and no tags (标签盲测); the category's usual structure
# (B's film / travel-vlog profiles) as a reference; B's review rules on joins moved into the outline.
PICK_SYSTEM = "你是剪辑规划，按用户的剪辑要求写一份成片大纲。画面由程序按大纲剪出，带原声，不加配乐和字幕。"
PICK = """用户的剪辑要求：
「{instruction}」

下面是 {total_min:.1f} 分钟原始素材切成的 {n} 张卡片，按拍摄时间排列。卡片按画面变化切分，一张卡片是同一处、同一件事的一段连续画面；{what}由本机小模型看关键帧写成，可能看错。报幕、片头片尾字幕、黑场和纯文字画面已经去掉了。
每行：卡片 id {where}起始时间 时长 | 场景 | {what}{what_note}

{catalog}
{position}{structure}
衔接（规划时照着安排）：
{open_close}
- 同一部分里的几个画面要注意场景之间的衔接，前后有一定的叙事性或画面连续性，画面变化的时候要加入衔接画面；
- 换场景时用远景、空镜或一个动作的开始和结束来过渡；同一场景里景别可以由远到近推进，运动方向前后一致；
- 不要用片头片尾字幕、黑场或纯文字画面。

{task}
1. design：用 2–3 句话写成片的设计：开头、中间、结尾分别用哪些卡片，为什么这样安排；开场怎么交代背景（时间、地点、人物），段落之间怎么过渡；开头、中间、结尾按拍摄时间先后安排；写完对照用户的要求检查内容和先后顺序；
2. unmet：要求里提到、但卡片里找不到的内容，都找得到就留空；找不到的内容不要拿别的卡片冒充；
3. parts：照 design 分成 {parts_rule}{pictures_rule}，按拍摄时间先后排列，每部分里的画面也按拍摄时间先后排列：
   - name 是这一部分的标题，summary 用一句话说明这部分讲什么；
   - link 写这一部分和上一部分怎么衔接：写清上一部分最后一个画面和这一部分第一个画面靠什么接上（同一个人、同一样东西、同一个动作或声音、到达或进入一个地方），不要只写“直接切入”；{first_link}
   - weight 是这一部分的分量（1–10 的整数）；
   - scenes 是这一部分要的 1–4 个画面：query 用一个短语说明要的画面（如“雨夜车窗上的雨滴”“江边远眺陆家嘴”），写看得见的主体、动作和光线，不写地名（概括里的地名可能认错），所有部分的 query 互不重复；cards 写这个画面取自哪几张卡片（1–3 张）。
只输出 JSON。"""

# The category's usual structure, offered as a reference (the instruction still decides): B's words.
STRUCTURE = """
这批素材属于「{name}」，这类成片常用的结构是：{structure}
设计时可以参考，但以用户的要求为准。
"""
PROFILES = {"预告片": "film", "旅行": "travel-vlog"}
PACES = ("整段", "适中", "快")


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
下面每个画面列出它可以取自的卡片（id、时间、场景、概括）和节拍（节拍 id、景别、能取几秒、画面）。
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
MARK_HIGHLIGHTS = False
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
OPEN_CLOSE = "- 开场先交代地点、时代和人物，不要跳切；结尾要有收束感；"


def fewest_parts(target):
    return max(2, round(target / PART_SECONDS))


def longest_picture(_target):
    return PICTURE_MOST


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
        return read_json(path)
    _key, instruction, target, style, _name = CASES[case]
    cards = usable_cards(case)
    many = len({c["video"] for c in cards}) > 1
    # #416 (2026-11-09): where the cards were described again with a process (proc-<case>.json,
    # 关键帧卡片/process_cards.py), the catalog carries it instead of the summary: a long card's summary
    # kept its most striking part and dropped the steps on the way (the car pulling up at the cliff,
    # the torch in the cave), which is what joins one picture to the next.
    proc_path = CARDS / f"proc-{case}.json"
    proc = read_json(proc_path) if proc_path.exists() else {}
    what, what_note = ("过程", "（这张卡片里依次发生的事）") if proc else ("概括", "")
    lines = []
    for c in cards:
        a = c["answer"]
        text = " → ".join(proc[c["id"]]["answer"]["process"]) if "answer" in proc.get(c["id"], {}) else a["summary"]
        where = f"视频{int(c['video'])} " if many else ""
        # No tags (标签盲测 4.0 against 2.7) and no highlight: it ranked the street and road cards that
        # set up the place and join scenes (3.1-3.2) under the story's (3.6-4.7), and they were left out.
        lines.append(f"{c['id']} {where}{clock(c['start'])} {c['end'] - c['start']:.0f}秒 | {a.get('scene', '')} | {text}")
    spans = blocks(len(cards), CHUNK_LINES)
    k = len(spans)
    users, answers, costs, info, before = [], [], [], [], None
    for b, (lo, hi) in enumerate(spans, 1):
        seconds = target * (hi - lo) / len(cards)
        if k == 1:
            position, open_close = "", OPEN_CLOSE
            task, first_link = f"请按用户的要求，从这些卡片里设计一个约 {target} 秒的成片，按顺序写：", "第一部分写开场怎么进入；"
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
                open_close = "- 这一块是成片的结尾：第一部分接上上一块的最后一个画面，不要重新开场；结尾要有收束感；"
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
                  "unmet": [u for a in answers for u in a["unmet"]],
                  "parts": [part for a in answers for part in a["parts"]], "blocks": info}
        cost = {key: round(sum(c[key] or 0 for c in costs), 1) for key in ("seconds", "input", "output")} | {"calls": k}
        user = "\n\n".join(f"========== 第 {n} 块 ==========\n{u}" for n, u in enumerate(users, 1))
    # The order is the model's own (the design and the parts both told to follow shooting order); no
    # program sorting after it (in_order is kept for comparison only).
    out = {"system": PICK_SYSTEM, "user": user, "answer": merged, "cost": cost}
    write_json(out, path)
    print(case, "pick", cost, "分块", [b["cards"] for b in info], "每块段数", [b["parts"] for b in info], flush=True)
    return out


def plan_block(url, instruction, style, cards, lines, many, what, what_note, target, position, open_close, task,
               first_link):
    """Step one on one block of the catalog (all of it when it is short): (answer, cost, user)."""
    most = round(target / PER_CARD)
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
    total = sum(c["end"] - c["start"] for c in cards) / 60
    user = PICK.format(instruction=instruction, total_min=total, n=len(cards), where="所在视频 " if many else "", what=what, what_note=what_note,
                       catalog="\n".join(lines), structure=structure_of(style), target=target, most=most,
                       parts_rule=parts_rule, pictures_rule=pictures_rule, position=position, open_close=open_close,
                       task=task, first_link=first_link)
    ids = [c["id"] for c in cards]
    schema = {"type": "object", "required": ["design", "unmet", "parts"], "properties": {
        "design": {"type": "string"},
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
    answer, cost = chat(url, PICK_SYSTEM, user, schema, "outline")
    return answer, cost, user


def reversed_put(items, span):
    """items with each pair put the wrong way round swapped: one before another although all of its
    cards come after all of the other's. Pictures and parts whose times overlap keep the model's order."""
    items = list(items)
    moved = True
    while moved:
        moved = False
        for i in range(len(items) - 1):
            if min(span(items[i])) > max(span(items[i + 1])):
                items[i], items[i + 1] = items[i + 1], items[i]
                moved = True
    return items


def in_order(answer, at):
    """The outline in shooting order where the model put it plainly backwards (#416: 侦探片 had the
    puppet in the cave before the sky and the beach, and the cliff after them), and as the model wrote it
    where times overlap (上海's last part: 南京路 17:18, then the Bund 16:14 with 陆家嘴 19:06, to end on
    the night view; sorting by the earliest card put 南京路 last)."""
    times = lambda sc: [at[cid] for cid in sc["cards"]]     # noqa: E731
    for part in answer["parts"]:
        part["scenes"] = reversed_put(part["scenes"], times)
    answer["parts"] = reversed_put(answer["parts"], lambda part: [t for sc in part["scenes"] for t in times(sc)])
    return answer


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
    most = longest_picture(target)
    room = [[min(most, sum(u[2] for cid in dict.fromkeys(sc["cards"]) if cid in cards for u in units(cards[cid], cap)))
             for sc in p["scenes"]] for p in picked["parts"]]
    floors = [SHORTEST * len(p["scenes"]) for p in picked["parts"]]
    spare = max(0.0, target - sum(floors))
    parts = share(spare, [max(0.0, sum(r) - f) for r, f in zip(room, floors, strict=True)],
                  [p["weight"] for p in picked["parts"]])
    return [[max(SHORTEST, x) for x in share(f + extra, r)] for f, extra, r in zip(floors, parts, room, strict=True)]


HIGHLIGHT = """上面是同一张卡片里按时间排的 {n} 个画面（第 1 张到第 {n} 张）。挑出其中最有记忆点、最值得剪进{style}的 {most} 张：特别的人物、道具、表情或光影。只看画面本身，按编号回答。"""


def highlights(case, url, card_ids, style):
    """The beats of each card that stand out: marked by the description itself (tag_beats.describe_v2,
    chosen side by side among the card's key frames, after the beats and before the summary)."""
    cards = {c["id"]: c for c in load(case)}
    return {cid: cards[cid]["answer"].get("highlights", []) for cid in card_ids if cid in cards}


def shots(case, url):
    """Step two: for each picture of the outline, its beats in the planner's order of preference; the
    program then takes them to fill the picture's seconds. A plan made in blocks is ranked block by
    block, each call with that block's parts and seconds only."""
    path = HERE / f"shots-{case}{TAG}.json"
    if path.exists():
        return read_json(path)
    _key, instruction, target, style, _name = CASES[case]
    cap = CAPS.get(style, CAP)
    picked = pick(case, url)["answer"]
    cards = {c["id"]: c for c in usable_cards(case)}
    marks = highlights(case, url, list(dict.fromkeys(c for p in picked["parts"] for sc in p["scenes"]
                                                     for c in sc["cards"] if c in cards)), style)
    groups, at = [], 0
    for b in picked.get("blocks", []):
        groups.append((picked["parts"][at:at + b["parts"]], b["design"], b["seconds"]))
        at += b["parts"]
    if not groups:
        groups = [(picked["parts"], picked["design"], target)]
    seconds, raws, costs, users, system = [], [], [], [], ""
    for n, (parts, design, sec) in enumerate(groups, 1):
        extra = f"这是全片 {len(groups)} 块中的第 {n} 块，这一块约 {sec:.0f} 秒。\n" if len(groups) > 1 else ""
        secs, raw, cost, system, user = rank_block(url, instruction, target, style, cap, parts, design, cards, marks,
                                                   sec, extra)
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
    out = {"system": system, "user": user, "raw": raw, "seconds": seconds, "cap": cap, "cost": cost,
           "answer": {"parts": [{"name": p["name"], "summary": p["summary"], "pictures": r["pictures"]}
                                for p, r in zip(picked["parts"], raw["parts"], strict=True)],
                      "unmet": raw.get("unmet", [])}}
    write_json(out, path)
    print(case, "shots", cost, "画面秒数", [[round(x, 1) for x in secs] for secs in seconds],
          "每个画面列的节拍数", [[len(pic["beats"]) for pic in r["pictures"]] for r in raw["parts"]], flush=True)
    return out


def rank_block(url, instruction, target, style, cap, parts, design, cards, marks, seconds_of, extra):
    """Step two on some parts (all of them for a plan in one block): (seconds, raw, cost, system, user)."""
    seconds = budget({"parts": parts}, cards, seconds_of, cap)
    rules = f"\n剪法：{STYLES[style]}" if STYLES.get(style) else ""
    lines, part_items = [], []
    for n, (p, secs) in enumerate(zip(parts, seconds, strict=True), 1):
        lines.append(f"段落{n}「{p['name']}」：{p['summary']}")
        lines.append(f"  衔接：{p['link']}")
        items = []
        for k, (sc, sec) in enumerate(zip(p["scenes"], secs, strict=True), 1):
            lines.append(f"  画面{k}：{sc['query']}（约 {sec:.0f} 秒）")
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
                    star = "【亮点】" if MARK_HIGHLIGHTS and j in marks.get(cid, []) else ""
                    lines.append(f"      {cid}/{j} {b['shot_size']} {length:.0f}秒 {star}{b['what']}")
            need = min(len(ids), math.ceil(sec / cap) + 1)
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
    system = SHOTS.format(target=target, shortest=SHORTEST, cap=cap)
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
    key, instruction, target, style, name = CASES[case]
    cap = CAPS.get(style, CAP)
    d = next((Path.home() / "A-NAS-datasets" / "cache" / f"video-moments-{key}").glob("embeddinggemma*"))
    ix = moments.Index.load(str(d / "index.json"), str(d / "frames.npz"))
    cards = {c["id"]: c for c in load(case)}
    planned = read_json(HERE / f"shots-{case}{TAG}.json")
    picked = read_json(HERE / f"pick-{case}{TAG}.json")["answer"]
    clips = filled(planned, picked, cards, cap, longest_picture(target))
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
    for c in out_clips:
        cuts = c.pop("_cuts", [])
        words = sorted(ix.words.get(c["video"], []))
        c["start"], c["end"] = off_words(c["start"], c["end"], words, cuts)
    t0 = time.perf_counter()
    made = render.render_clips([(str(VIDEOS / ix.videos[c["video"]]["file"]), c["start"], c["end"]) for c in out_clips],
                               str(HERE), ending=True)
    final = HERE / f"{case}-{name}-剪法25{TAG.replace('-', '-第') + '次' if TAG else ''}.mp4"
    Path(made).replace(final)
    out = {"case": case, "instruction": instruction, "target": target, "style": style, "cap": cap,
           "seconds": round(sum(c["end"] - c["start"] for c in out_clips), 2), "clips": out_clips,
           "plannedShots": sum(len(pic["beats"]) for part in planned["answer"]["parts"] for pic in part["pictures"]),
           "renderSeconds": round(time.perf_counter() - t0, 1), "cut": str(final)}
    write_json(out, HERE / f"cut-{case}{TAG}.json")
    L = [c["end"] - c["start"] for c in out_clips]
    print(case, "cut", out["seconds"], "秒", len(out_clips), "个镜头，最短", round(min(L), 2), "最长", round(max(L), 2), flush=True)


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
