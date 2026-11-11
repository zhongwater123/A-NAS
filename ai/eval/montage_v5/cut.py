"""第六版剪辑：把一组旅行素材剪成一条配乐成片。

  python3 cut.py CASE ...            (CUT_MUSIC=music-<歌名>.json 必填；每一步的结果按 CASE 和 CUT_TAG 存在
                                      paths.CUTS，已有的步骤直接复用)

  卡片    cards.py 切好、本机 Qwen3.5-4B 写好描述的卡片（cards-<case>.json），quality.py 量过画质。
  过滤    误拍（excluded.json）、模糊、抖动、遮挡、夜景失焦的卡片不进目录（poor），人工复核保留的除外。
  第一步  Claude 读卡片目录和音乐段落，写成片大纲：设计、开头和结尾的理由、和音乐段一一对应的部分，
          每部分要哪些画面、取自哪几张卡片（pick）。
  第二步  Claude 给每个画面按镜头数挑节拍（shots）。
  剪辑    每个镜头卡在小节线上，从节拍的关键帧取一个槽的长度；卡片不够长时，从拍摄时间相近、画面最像
          的卡片补；配上歌曲的选段渲染（cut_to_music）。

开关：CUT_ORDER=free 不要求拍摄时间顺序，并要求同一地点的画面连续排列；CUT_TAG 给每次剪辑的文件命名；
CUT_QUALITY=0 不按画质过滤；CUT_CLAUDE、CUT_CLAUDE_MODEL 指定 Claude Code 命令行和模型。
"""

import json
import math
import os
import statistics
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))  # ai/, for montage.render
import held
import windows
from paths import CARDS, CUTS as HERE, INDEXES, VIDEOS, read_json, write_json

from montage import render

# case: (index key, the user's request, target seconds, style, name in file names)
CASES = {"ne": ("newzealand", "按拍摄时间顺序剪一条新西兰旅行 vlog，围绕旅途中的人物展开，场景之间衔接自然", 90, "旅行", "新西兰"),
         "ch": ("chuanxi", "按拍摄时间顺序剪一条川西旅行 vlog，围绕旅途中的人物展开，场景之间衔接自然", 90, "旅行", "川西"),
         "pj": ("pinjie", "按拍摄时间顺序剪一条新西兰旅行 vlog，围绕旅途中的人物展开，场景之间衔接自然", 90, "旅行", "新西兰（品杰手机）")}

# Each run of a case is a take: CUT_TAG ("-1", "-2") names its files.
TAG = os.environ.get("CUT_TAG", "")
# The cut in shooting order, or (CUT_ORDER=free) in no order asked for: the request and step 1 lose their
# shooting-order asks (ORDER_ASKS). Both are made, for the user to choose (2026-11-11: free order chose
# shots that fit the music's mood better on 品杰 and 川西; 新西兰 cut back and forth between places).
FREE_ORDER = os.environ.get("CUT_ORDER") == "free"
if FREE_ORDER:
    CASES = {k: (v[0], v[1].replace("按拍摄时间顺序", ""), *v[2:]) for k, v in CASES.items()}
ORDER_ASKS = (("开头、中间、结尾按拍摄时间先后安排；", ""), ("检查内容和先后顺序", "检查内容"),
              ("，按拍摄时间先后排列，每部分里的画面也按拍摄时间先后排列：", "："),
              # 2026-11-11 (the user's wording, its example too): free order cut 新西兰 back and forth between
              # places, its helicopter flight broken by roads, sheep and lakes.
              ("- 每部分的画面个数不超过", "- 同一地点的画面连续排列：切到别的地点后，不再切回之前用过的地点。比如开头是雪山，"
               "中间切到别的地方，之后又切回雪山，画面就不连贯了；\n- 每部分的画面个数不超过"))

# Both steps on Claude, through the Claude Code CLI with no tools, settings or MCP servers: it sees the
# prompt alone. (The local 4B, planning 152-187 cards, took 5 of 6 New Zealand parts from the first 46.)
CLAUDE = os.environ.get("CUT_CLAUDE", "claude")
CLAUDE_MODEL = os.environ.get("CUT_CLAUDE_MODEL", "claude-opus-5-5")


def ask_claude(system, user, schema):
    """(answer, cost): the structured answer, and the seconds, tokens and US dollars it took."""
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


def index_dir(case):
    """The case's window index (index.json, frames.npz), built from its videos beforehand."""
    return next((INDEXES / f"video-moments-{CASES[case][0]}").glob("embeddinggemma*"))


def load(case):
    """The case's described cards (cards.py)."""
    return [c for c in read_json(CARDS / f"cards-{case}.json").values() if "answer" in c]


def described(case):
    """The description file load() reads; each take's pick and shots files name it."""
    return f"cards-{case}.json"


def reused(path, case):
    """A take's pick or shots file, kept only when it was made from the description load() reads now."""
    data = read_json(path)
    if data.get("descriptions") != described(case):
        sys.exit(f"{path.name} 不是按 {described(case)} 规划的：换一个 CUT_TAG 重剪")
    return data


def clock(t):
    return f"{int(t // 60)}:{int(t % 60):02d}"


# Mis-shots the user marked (a finger over the lens, black): (case, video) -> [(from, to)] seconds left out.
RAW = {case: key for case, (key, *_rest) in CASES.items()}
LEFT_OUT = {}
for _case, _key in RAW.items():
    for _x in read_json(CARDS / "excluded.json").get(_key, []) if (CARDS / "excluded.json").exists() else []:
        LEFT_OUT.setdefault((_case, _x["video"]), []).append((float(_x["from"]), float(_x["to"])))


# The user's own footage is left without its blurred, shaking or blocked cards
# (quality.py; 画面质量核对.html shows them, 画面质量标注.json holds the user's marks on 36 of them). Blur
# alone does not tell: the user kept still sunsets and dusk at 8-10.8 and left out what blurred while the
# camera moved (shake 2-4.4), so blur counts over BLUR_MOST only with shake over BLUR_MOVING. Shake marks
# from 3 to 5.5 px went both ways; over SHAKE_MOST all were left out. Black (nothing to measure) and a
# blocked share of BLOCKED_MOST or more go too. On the marks: none of the 22 kept is left out, 10 of the
# 14 are; the 4 missed are bad in part of the card only (a selfie swung to the sky or a jacket, a face
# filling the frame, a soft foreground). CUT_QUALITY=0 keeps them all.
BLUR_MOST, BLUR_MOVING, SHAKE_MOST, BLOCKED_MOST = 8.0, 1.0, 5.6, 0.25
# A night sky out of focus (quality.points, 2026-11-11): most key frames dark and their median sharp points
# per megapixel under POINTS_LEAST (ne-070's soft stars 22-27; in focus at night 150 and up).
POINTS_LEAST = 60
QUALITY = os.environ.get("CUT_QUALITY", "1") != "0"


def out_of_focus(points):
    """Most key frames dark (quality.points gives them a number) and few sharp points on them."""
    dark = [p for p in points if p is not None]
    return len(dark) * 2 > len(points) and statistics.median(dark) < POINTS_LEAST


def poor(case):
    """The cards quality.py would leave out: {card: [reasons]}, but those the user kept on review (reviewed.json:
    2026-11-11, 品杰's dolphins off the bow, a calm sea the blocked rule takes for something at the lens)."""
    path = CARDS / f"quality-{case}.json"
    if not QUALITY or not path.exists():
        return {}
    kept = set(read_json(CARDS / "reviewed.json").get(RAW[case], [])) if (CARDS / "reviewed.json").exists() else set()
    out = {}
    for cid, q in read_json(path).items():
        if cid in kept:
            continue
        why = [w for w, bad in (("黑帧", math.isnan(q["blur"])), ("模糊", q["blur"] > BLUR_MOST and q["shake"] > BLUR_MOVING),
                                ("抖动", q["shake"] > SHAKE_MOST), ("遮挡", q["blocked"] >= BLOCKED_MOST),
                                ("失焦", out_of_focus(q.get("points", [])))) if bad]
        if why:
            out[cid] = why
    return out


def usable_cards(case):
    bad = poor(case)
    return [c for c in load(case) if c["id"] not in bad
            and not any(a - 0.01 <= c["start"] < b - 0.01 for a, b in LEFT_OUT.get((case, c["video"]), []))]


# Step one: 方案 B's outline request on our cards, the join rules first so that the plan is made with
# them; the category's usual structure (B's travel-vlog profile) as a reference; the song's parts.
PICK_SYSTEM = "你是剪辑规划，按用户的剪辑要求写一份成片大纲。画面由程序按大纲剪出，配上给定的音乐，不用原声，不加字幕。"
PICK = """用户的剪辑要求：
「{instruction}」

下面是 {total_min:.1f} 分钟原始素材切成的 {n} 张卡片，按拍摄时间排列。卡片按画面变化切分，一张卡片是同一处、同一件事的一段连续画面；过程由本机小模型看关键帧写成，可能看错。报幕、片头片尾字幕、黑场和纯文字画面已经去掉了。
每行：卡片 id {where}起始时间 时长 | 场景 | 过程（这张卡片里依次发生的事）

{catalog}
{music}{structure}
衔接（规划时照着安排）：
{open_close}
- 同一部分里的几个画面要注意场景之间的衔接，前后有一定的叙事性或画面连续性，画面变化的时候要加入衔接画面；
- 换场景时用远景、空镜或一个动作的开始和结束来过渡；同一场景里景别可以由远到近推进，运动方向前后一致；
- 不要用片头片尾字幕、黑场或纯文字画面。

请按用户的要求，从这些卡片里设计一个约 {target} 秒的成片，按顺序写：
1. design：用 2–3 句话写成片的设计：开头、中间、结尾分别用哪些卡片，为什么这样安排；开场怎么交代背景（时间、地点、人物），段落之间怎么过渡；开头、中间、结尾按拍摄时间先后安排；写完对照用户的要求检查内容和先后顺序；
2. opening：写开头第一个画面用哪张卡片、为什么选它；
3. ending：写结尾最后一个画面用哪张卡片、为什么选它、属于哪一种收尾；
4. unmet：要求里提到、但卡片里找不到的内容，都找得到就留空；找不到的内容不要拿别的卡片冒充；
5. parts：照 design 分成 {parts_rule}，按拍摄时间先后排列，每部分里的画面也按拍摄时间先后排列：
   - name 是这一部分的标题，summary 用一句话说明这部分讲什么；
   - link 写这一部分和上一部分怎么衔接：写清上一部分最后一个画面和这一部分第一个画面靠什么接上（同一个人、同一样东西、同一个动作或声音、到达或进入一个地方），不要只写“直接切入”；第一部分写开场怎么进入；
   - scenes 是这一部分要的画面（个数不超过这一段能放的镜头数）：query 用一个短语说明要的画面（如“雨夜车窗上的雨滴”“江边远眺陆家嘴”），写看得见的主体、动作和光线，不写地名（概括里的地名可能认错），所有部分的 query 互不重复；cards 写这个画面取自哪几张卡片（1–3 张），一张卡片只放进一个画面。
只输出 JSON。"""
# B's travel-vlog profile, its 成片结构, offered as a reference (the request still decides).
STRUCTURE = """
这批素材属于「旅行 vlog」，这类成片常用的结构是：开场最抓人的一个画面 → 出发与在路上 → 按地点依次展开，每地一两个最好的镜头 → 最高潮的地点或体验 → 收尾（回望、夜景或离开）。
设计时可以参考，但以用户的要求为准。
"""
# The ending (2026-11-09): what montage and travel editing close on, the user's choice of it, landscapes and
# cities apart; asked for, not ruled out. The Forever cut had ended on a museum's bronze tree.
ENDING = ("结尾的最后一个画面要静下来，节奏放缓，有结束的感觉："
          "素材以自然风景为主时，可以用远去的背影或交通工具、拉远或升高的大景、黄昏落日或星空、几乎不动的风景空镜、人物回望或静坐望远；"
          "素材以城市为主时，可以用夜景和灯火（街道、天际线、江边）、人流或车流里慢慢走远的背影、从高处俯瞰城市、地标建筑的全景；"
          "两种都可以呼应开头")
OPEN_CLOSE = f"- 开场先交代地点、时代和人物，不要跳切；{ENDING}；"

# Step two. Beats are listed with what they can run: from the key frame, at least SHORTEST, at most CAP.
SHOTS_SYSTEM = """你是剪辑师。大纲定好了段落和每个段落要的画面，程序定好了每个画面约几秒。你给每个画面挑节拍；程序按音乐把每个画面切成几个镜头，镜头卡在小节上；按你排的顺序，每个镜头用一个节拍，从节拍开头取。成片约 {target:g} 秒，配音乐，不用原声。高潮段的画面只挑全景、远景的节拍，第一个画面的第一个节拍必须是全景或远景，挑最开阔的那个；画面只有一两个镜头时，放不下卡片里的整个过程：不要默认挑第一个节拍，要从这张卡片的全部节拍里挑出最关键、最精彩的那一刻排在第一。高潮段前的最后一个画面，在符合这个画面内容的节拍里，挑最处在“还没”状态的：还没到、还没看见、还没打开、还没落地。
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
STYLE_RULES = {"旅行": "按时间或路线走；开头用一个能交代地点的全景；中间每处地方挑最有代表性的画面，远景、人和细节穿插；"
                      "结尾落在一个安静、有余味的画面上，如日落、远景、离开的背影。"}
SHORTEST, CAP = 2.0, 6.0

# The montage is cut to a song, CutClaw's way (CUT_MUSIC=music-<song>.json under HERE, by music.py): the
# song's parts are the cut's parts, its bar-line slots each picture's shots, and the cut carries the song
# instead of its own sound. Step one is told each part's seconds, level and slots.
MUSIC = os.environ.get("CUT_MUSIC", "")


def song():
    if not MUSIC:
        sys.exit("CUT_MUSIC=music-<歌名>.json：先用 structure.py 和 music.py 分析歌曲")
    return read_json(HERE / MUSIC)


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




def pick(case):
    """Step one: the outline, one part to each of the song's parts."""
    path = HERE / f"pick-{case}{TAG}.json"
    if path.exists():
        return reused(path, case)
    _key, instruction, _target, style, _name = CASES[case]
    m = song()
    target = round(m["seconds"])
    cards = usable_cards(case)
    many = len({c["video"] for c in cards}) > 1
    # The catalog carries each card's process, the steps one after another: a long card's summary kept its
    # most striking part and dropped the steps on the way, which is what joins one picture to the next. No
    # tags (标签盲测 4.0 against 2.7) and no highlight: it ranked the cards that set up the place and join
    # scenes under the story's, and they were left out.
    lines = []
    for c in cards:
        where = f"视频{int(c['video'])} " if many else ""
        lines.append(f"{c['id']} {where}{clock(c['start'])} {c['end'] - c['start']:.0f}秒 | {c['answer'].get('scene', '')} | "
                     + " → ".join(c["answer"]["process"]))
    many_parts = len(m["parts"])
    user = PICK.format(instruction=instruction, total_min=sum(c["end"] - c["start"] for c in cards) / 60, n=len(cards),
                       where="所在视频 " if many else "", catalog="\n".join(lines), music=music_note(m),
                       structure=STRUCTURE if style == "旅行" else "", open_close=OPEN_CLOSE, target=target,
                       parts_rule=f"{many_parts} 个部分（和音乐的 {many_parts} 段一一对应）")
    if FREE_ORDER:
        for a, b in ORDER_ASKS:
            if a not in user:
                raise ValueError(f"CUT_ORDER: the prompt no longer says {a}")
            user = user.replace(a, b)
    ids = [c["id"] for c in cards]
    # 2026-11-09 (the user): the opening and the ending each with a reason, written before the parts.
    schema = {"type": "object", "required": ["design", "opening", "ending", "unmet", "parts"], "properties": {
        "design": {"type": "string"}, "opening": {"type": "string"}, "ending": {"type": "string"},
        "unmet": {"type": "array", "items": {"type": "string"}},
        "parts": {"type": "array", "minItems": many_parts, "maxItems": many_parts, "items": {
            "type": "object", "required": ["name", "summary", "link", "scenes"], "properties": {
                "name": {"type": "string"}, "summary": {"type": "string"}, "link": {"type": "string"},
                "scenes": {"type": "array", "minItems": 1, "maxItems": max(len(x["slots"]) for x in m["parts"]), "items": {
                    "type": "object", "required": ["query", "cards"], "properties": {
                        "query": {"type": "string"},
                        "cards": {"type": "array", "minItems": 1, "maxItems": 3,
                                  "items": {"type": "string", "enum": ids}}}}}}}}}}
    answer, cost = ask_claude(PICK_SYSTEM, user, schema)
    # The order is the model's own; no program sorting after it.
    out = {"system": PICK_SYSTEM, "user": user, "answer": answer, "cost": cost, "descriptions": described(case), "music": MUSIC}
    write_json(out, path)
    print(case, "pick", cost, "段数", len(answer["parts"]), flush=True)
    return out


def shots(case):
    """Step two: for each picture of the outline, its beats in Claude's order of preference, as many as
    the picture's slots and one or two to spare."""
    path = HERE / f"shots-{case}{TAG}.json"
    if path.exists():
        return reused(path, case)
    _key, instruction, _target, style, _name = CASES[case]
    m = song()
    target = round(m["seconds"])
    picked = pick(case)["answer"]
    cards = {c["id"]: c for c in usable_cards(case)}
    slots = slotted(m, picked["parts"])
    seconds = [[round(sum(b - a for a, b in pic), 2) for pic in part] for part in slots]
    lines, part_items = [], []
    for n, (p, secs) in enumerate(zip(picked["parts"], seconds, strict=True), 1):
        lines.append(f"段落{n}「{p['name']}」：{p['summary']}")
        lines.append(f"  衔接：{p['link']}")
        items = []
        for k, (sc, sec) in enumerate(zip(p["scenes"], secs, strict=True), 1):
            shots_here = len(slots[n - 1][k - 1])
            lines.append(f"  画面{k}：{sc['query']}（约 {sec:.0f} 秒，{shots_here} 个镜头）")
            ids = []
            for cid in dict.fromkeys(sc["cards"]):
                if cid not in cards:
                    continue
                c = cards[cid]
                a = c["answer"]
                lines.append(f"    卡片 {cid}（{clock(c['start'])}–{clock(c['end'])}，{a.get('scene', '')}）：{a['summary']}")
                # #416 (2026-11-09): no 【亮点】 marks; two marked faces in the car came before the rear-view
                # mirror the picture asked for.
                for (j, _start, length), b in zip(units(c, CAP), a["beats"], strict=True):
                    ids.append(f"{cid}/{j}")
                    lines.append(f"      {cid}/{j} {b['shot_size']} {length:.0f}秒 {b['what']}")
            need = min(len(ids), shots_here + 1)
            items.append({"type": "object", "required": ["beats", "shows", "link"], "properties": {
                "beats": {"type": "array", "minItems": max(1, need), "maxItems": max(1, min(len(ids), need + 4)),
                          "items": {"type": "string", "enum": ids}},
                "shows": {"type": "string"}, "link": {"type": "string"}}})
        part_items.append(items)
    rules = f"\n剪法：{STYLE_RULES[style]}" if STYLE_RULES.get(style) else ""
    user = (f"指令：{instruction}\n成片时长：约 {target} 秒\n体裁：{style}{rules}\n大纲：{picked['design']}\n"
            "段落和画面：\n" + "\n".join(lines))
    system = SHOTS_SYSTEM.format(target=target)
    # The CLI's strict schemas have no prefixItems: each part and picture becomes a named field (段落1,
    # 画面1, ...), so each picture keeps its own beats to choose from, and the answer is turned back.
    named = {"type": "object", "required": ["parts", "unmet"], "properties": {
        "unmet": {"type": "array", "items": {"type": "string"}},
        "parts": {"type": "object", "required": [f"段落{n}" for n in range(1, len(part_items) + 1)], "properties": {
            f"段落{n}": {"type": "object", "required": [f"画面{k}" for k in range(1, len(items) + 1)],
                        "properties": {f"画面{k}": item for k, item in enumerate(items, 1)}}
            for n, items in enumerate(part_items, 1)}}}}
    answer, cost = ask_claude(system, user, named)
    raw = {"unmet": answer["unmet"],
           "parts": [{"pictures": [answer["parts"][f"段落{n}"][f"画面{k}"] for k in range(1, len(items) + 1)]}
                     for n, items in enumerate(part_items, 1)]}
    out = {"system": system, "user": user, "raw": raw, "seconds": seconds, "cap": CAP, "cost": cost, "step2": "claude",
           "descriptions": described(case), "slots": slots, "music": MUSIC,
           "answer": {"parts": [{"name": p["name"], "summary": p["summary"], "pictures": r["pictures"]}
                                for p, r in zip(picked["parts"], raw["parts"], strict=True)],
                      "unmet": raw.get("unmet", [])}}
    write_json(out, path)
    print(case, "shots", cost, "画面秒数", [[round(x, 1) for x in secs] for secs in seconds],
          "每个画面列的节拍数", [[len(pic["beats"]) for pic in r["pictures"]] for r in raw["parts"]], flush=True)
    return out


PIECE_LEAST = 1.5    # seconds: no shot of a music cut shorter, the slots' and the fills'
NEAR = 6            # cards either side in shooting order a short slot may be filled from
SLOW_MOST = 2.5      # times: the ending shot drawn out at most this much to fill its slot
# Sources tagged upside down over upright pixels (rotation.json beside the cards), turned back in every render of this
# module, narrative or music: set once here, not per cut, so no cut can miss them.
ROTATION = {k: set(v) for k, v in read_json(CARDS / "rotation.json").items() if k != "about"} if (CARDS / "rotation.json").exists() else {}
render.TURNED_BACK |= {str(f) for names in ROTATION.values() for n in names for f in VIDEOS.glob(f"*/{n}")}


def cut_to_music(case):
    """第六版: each picture's slots filled in its beats' order, one beat a slot from its key frame, the clip
    exactly the slot's frames (moved earlier in its card when the card ends first; a card too short leaves
    the rest of the slot to the next beat, but no piece under half a second); the song's excerpt for sound."""
    _key, instruction, _target, style, name = CASES[case]
    m = song()
    d = index_dir(case)
    ix = windows.Index.load(str(d / "index.json"), str(d / "frames.npz"))
    cards = {c["id"]: c for c in load(case)}
    planned = reused(HERE / f"shots-{case}{TAG}.json", case)
    picked = reused(HERE / f"pick-{case}{TAG}.json", case)["answer"]
    fps = render.FPS
    spans = held.Spans(HERE / f"held-{case}.json")
    # A slot its picture's cards cannot fill (川西's opening stream, ch-001, is 2.3 s against a 3.2 s
    # slot) is filled on from the cards shot near them, NEAR either side of each in shooting order, the
    # one whose picture is most like theirs first: a slot short of its frames moves every later cut off
    # the bar lines. (2026-11-11, the user: the nearest alone put a starry night after a helicopter, ne-154
    # after ne-153, and a window and a rotor after an aurora; likeness alone, 0.73-0.79 for those and 0.76
    # for fills that were right, would reach other places.)
    catalog = [c["id"] for c in usable_cards(case)]
    seen_as = {}

    def picture(cid):
        """The card's mean frame vector, over the index windows its span touches."""
        if cid not in seen_as:
            import numpy as np

            c = cards[cid]
            rows = [i for i, (v, a, b) in enumerate(ix.rows) if v == c["video"] and a < c["end"] and b > c["start"]]
            v = ix.frames[rows].mean(axis=0)
            seen_as[cid] = v / np.linalg.norm(v)
        return seen_as[cid]

    def neighbours(ids):
        at = [catalog.index(i) for i in ids if i in catalog]
        if not at:
            return []
        near = sorted({catalog[k] for i in at for k in range(max(0, i - NEAR), min(len(catalog), i + NEAR + 1))} - set(ids))
        like = sum(picture(i) for i in ids if i in cards)
        near.sort(key=lambda cid: -float(picture(cid) @ like))
        return [f"{cid}/{n}" for cid in near for n in range(1, len(cards[cid]["answer"]["beats"]) + 1)]

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
            # Then the other cards step 1 named for this picture, and only then the cards shot next to them
            # (新西兰 fortnight-free-1: step 2 listed ne-153 alone, and its neighbour in shooting order, the
            # starry-sky ne-154, filled the slot where step 1 had also named ne-101, in the helicopter).
            named = [f"{cid}/{n}" for cid in step1["scenes"][k - 1]["cards"] if cid in cards
                     for n in range(1, len(cards[cid]["answer"]["beats"]) + 1)]
            order += [b for b in named if b not in order]
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




def main():
    cases = sys.argv[1:] or list(CASES)
    for case in cases:
        shots(case)
        cut_to_music(case)
    print("ALL-DONE", flush=True)


if __name__ == "__main__":
    main()
