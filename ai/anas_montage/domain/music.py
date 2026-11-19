"""音乐是成片的时间线（CutClaw 的做法）：段落和强度、选段、按小节线切成的镜头槽（第六版 music.py、cut.py 的这部分）。

段落和重拍由 MusicAnalyzer 端口给出（开发机上是 allin1，板子上读预先算好的结果）；响度曲线也由端口给出。
"""

import itertools
import statistics
from pathlib import PurePath

import numpy as np

PHRASE = 4                  # 一个乐句几小节
# allin1 的段落名到成片强度：副歌是高潮；主歌、桥段、间奏、独奏是中等；前奏、尾奏、间歇是安静。
LEVELS = {"chorus": "高潮", "verse": "中等", "bridge": "中等", "inst": "中等", "solo": "中等", "build": "中等",
          "intro": "安静", "outro": "安静", "break": "安静", "start": "安静", "end": "安静"}
# allin1 叫副歌的段落，后面一段比它响 BUILD_RISE dB 以上，它是推向副歌的铺垫，后面那段才是副歌。
BUILD_RISE = 2.0


def sections(st: dict, rms: np.ndarray, times: np.ndarray) -> tuple[list[dict], float]:
    """整理过的段落和一小节的长度：短段落并进下一段，边界挪到重拍上，推向副歌的铺垫改名。选段从这些段落里取。"""
    bars = st["downbeats"]
    bar = float(np.median(np.diff(bars)))
    # 不到两小节的段落并进下一段。
    segs = []
    for g in (g for g in st["segments"] if g["label"] not in ("start", "end") and g["end"] - g["start"] > 1.0):
        if segs and segs[-1]["end"] - segs[-1]["start"] < 2 * bar:
            segs[-1] = {**g, "start": segs[-1]["start"]}
        else:
            segs.append(dict(g))
    # 段落边界挪到最近的重拍上。
    for g, nxt in itertools.pairwise(segs):
        t = min(bars, key=lambda x: abs(x - nxt["start"]))
        if abs(t - nxt["start"]) <= bar / 2:
            g["end"] = nxt["start"] = t

    def loudness(g):
        return 20 * float(np.log10(rms[(times >= g["start"]) & (times < g["end"])].mean() / rms.max()))

    for g, nxt in itertools.pairwise(segs):
        if g["label"] == "chorus" and loudness(nxt) - loudness(g) >= BUILD_RISE:
            g |= {"allin1": g["label"], "label": "build"}
            nxt |= {"allin1": nxt["label"], "label": "chorus"}
    return segs, bar


def parts(segs: list[dict], i: int, j: int, start: float, end: float, bars: list) -> list[dict]:
    """选段 start 到 end 落在第 i 到第 j 个段落上，各段在选段里的起止、强度和镜头槽。副歌每小节一个槽，其余每两小节。"""
    out = []
    for n, g in enumerate(segs[i:j + 1], i):
        a, b = max(g["start"], start), min(g["end"], end)
        if b - a < 1.0:
            continue
        level = LEVELS.get(g["label"], "中等")
        # 从段落开头数第 2、4、6 条小节线；选段最后一段从结尾往回数，奇数小节落在开头，成片最后一个镜头完整。
        inside = [t for t in bars if a + 0.5 < t < b - 0.5]
        lines = inside if level == "高潮" else inside[::-1][1::2][::-1] if n == j else inside[1::2]
        cuts = [a, *lines, b]
        out.append({"start": round(a - start, 2), "end": round(b - start, 2), "level": level, "label": g["label"],
                    **({"allin1": g["allin1"]} if "allin1" in g else {}),
                    "slots": [[round(x - start, 2), round(y - start, 2)] for x, y in itertools.pairwise(cuts) if y - x > 0.5]})
    return out


def from_structure(st: dict, rms: np.ndarray, times: np.ndarray, target: float) -> dict:
    """选段和镜头槽。st：allin1 的结果（song、bpm、downbeats、segments）；rms、times：响度曲线和它的时间。
    选段是包含第一个副歌的整段，最接近 target，其次最早；超出一个乐句以上时在重拍处截断。副歌每小节一个槽，其余每两小节。"""
    bars = st["downbeats"]
    segs, bar = sections(st, rms, times)
    first = next(k for k, g in enumerate(segs) if g["label"] == "chorus")
    options = [(i, j) for i in range(first + 1) for j in range(first, len(segs))]
    i, j = min(options, key=lambda o: (max(0.0, abs(segs[o[1]]["end"] - segs[o[0]]["start"] - target) - bar * PHRASE),
                                       segs[o[0]]["start"]))
    start, end = segs[i]["start"], segs[j]["end"]
    if end - start > target + bar * PHRASE:
        end = min((t for t in bars if t > start + target - bar), key=lambda t: abs(t - start - target))
    return {"song": st["song"], "tempo": st["bpm"], "bar": round(bar, 2), "structure": st["segments"],
            "excerpt": [round(start, 2), round(end, 2)], "seconds": round(end - start, 2),
            "parts": parts(segs, i, j, start, end, bars)}


def music_note(m: dict) -> str:
    """第一步读到的音乐段落和对各段的要求。"""
    lines = [f"成片配乐：{PurePath(m['song']).stem}，取其中 {m['seconds']:.0f} 秒，按音乐分成 {len(m['parts'])} 段，"
             "成片的部分和音乐段一一对应；镜头由程序卡在音乐的小节上切："]
    for n, part in enumerate(m["parts"], 1):
        sl = [b - a for a, b in part["slots"]]
        lines.append(f"第 {n} 段 {part['start']:.0f}–{part['end']:.0f} 秒，{part['level']}，能放 {len(sl)} 个镜头（每个约 {statistics.median(sl):.1f} 秒）")
    lines += ["- 进入高潮段前的最后一个画面处在“还没”的状态：人正要到达某处但还没到；镜头在往前冲或往上升；"
              "画面被挡住、被框住、很窄；人在准备或期待；动作在加速或悬在半空；从暗到亮、从小到大的前一刻。"
              "高潮段的第一个画面紧接着放全片最开阔、最宏大的全景或远景，作为释放；",
              "- 高潮段的第一个画面必须是全景或远景；高潮段全部用全景、远景和宏大的场面；",
              "- 高潮段的宏大场面要多样，同一类景物的画面不超过三个；",
              "- 音乐段落的交界就是换场景的地方，过渡画面放在交界处；高潮段只放最精彩的画面，不放过渡画面；",
              "- 每部分的画面个数不超过这一段能放的镜头数。"]
    return "\n" + "\n".join(lines) + "\n"


TAKE_MOST = 6.0     # 秒：高潮段一个镜头最长（和 plan.CAP 相同）


def slotted(m: dict, parts: list[dict]) -> list:
    """每个画面的槽：一段的槽按顺序尽量平均分给它的画面，多出来的给前面的画面；高潮段一个画面一个镜头（one_take）。"""
    out = []
    for mp, part in zip(m["parts"], parts, strict=True):
        each, extra = divmod(len(mp["slots"]), len(part["scenes"]))
        got, at = [], 0
        for k in range(len(part["scenes"])):
            take = each + (1 if k < extra else 0)
            got.append(one_take(mp["slots"][at:at + take], mp["level"]))
            at += take
        out.append(got)
    return out


def one_take(slots: list, level: str) -> list:
    """高潮段一个画面的几个槽连成一个镜头：同一段连续的原片跨过小节线，只在换画面处切；超过 TAKE_MOST 秒的画面在
    会超过的那条小节线上切开。（2026-11-17，用户：每小节一切时，潜水的副歌是 8 段 1.9 秒，每个画面是同一张卡片的
    两段，dv-020 先放 13 秒再跳回 2 秒，像抽帧；Insta360 拼接第 6 版一条大街放了 9 秒，加了上限。）"""
    if level != "高潮" or len(slots) < 2:
        return [list(s) for s in slots]
    out = [list(slots[0])]
    for a, b in slots[1:]:
        if b - out[-1][0] <= TAKE_MOST + 0.05:
            out[-1][1] = b
        else:
            out.append([a, b])
    return out
