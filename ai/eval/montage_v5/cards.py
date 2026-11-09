"""#416 / #392: every card of cases 01 (detective film), 02 (Shanghai vlog) and 04 (Shenzhen surfing),
cut and described the way settled on 2026-11-08:

cards     2 s windows alone (no FFmpeg cuts): a window under CARD 0.86 to the card's mean starts the
          next card unless one of the next LOOK 2 windows is that alike again (A B A stays one card);
          a card past LONG 120 s is cut at its least alike neighbouring windows, SIDE 20 s a side;
key frames the card's first window, then each window under STILL 0.9 to the mean of the run so far
          (today's card rule, no look-ahead); a run going GAP 12 s without one gets, every 12 s, the
          window least like the key frames taken; at least 3 (else 3 spread evenly), at most 20
          (the most alike neighbouring pair loses its later frame);
described by Qwen3.5-4B (tag_beats.describe_groups, one beat a key frame: shot size, what is seen;
          summary, scene, indoor/outdoor, time of day, mood, landmark, tags, highlight read by its
          log-probabilities, issues), each frame 512x288 labelled with its number and second.

  python3 cards.py URL      (writes cards-<case>.json and 关键帧卡片.html under paths.CARDS; resumes)"""
import base64
import io
import math
import subprocess
import sys
import time
from pathlib import Path
from types import SimpleNamespace

import numpy as np
from PIL import Image

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))  # ai/, for montage
import tag_beats
from paths import B_DIR, CARDS as HERE, read_json, write_json

from montage import moments

CARD, LOOK, LONG, SIDE = 0.86, 2, 120.0, 20.0
STILL, GAP, FEWEST, MOST = moments.STILL, 12.0, 3, 20
CASES = [("01", "case01", "film", "侦探片"), ("02", "shanghai", "travel-vlog", "上海 vlog"),
         ("04", "case04", "travel-vlog", "深圳冲浪")]
VIDEOS = Path.home() / "A-NAS-datasets" / "videos"
SECTIONS = {"简介": "intro", "看片重点": "focus", "精彩度标准": "rubric", "成片结构": "structure", "解说风格": "narration"}


def profile(key):
    lines = (B_DIR / f"{key}.prompt.md").read_text("utf-8").splitlines()
    found, current = {}, None
    for line in lines[1:]:
        if line.startswith("## "):
            current = found.setdefault(SECTIONS[line[3:].strip()], [])
        elif current is not None:
            current.append(line)
    return SimpleNamespace(key=key, name=lines[0][2:].strip(), **{k: "\n".join(v).strip() for k, v in found.items()})


def runs(ix, members, tau, look):
    """Index._pieces with look-ahead: a window under tau to the run's mean joins it when one of the
    next look windows is tau alike to that mean; bridged windows do not move the mean."""
    out, mean, i = [[members[0]]], ix.frames[members[0]].copy(), 1
    while i < len(members):
        w = members[i]
        def alike(j, mean=mean):
            return float(ix.frames[j] @ mean) / np.linalg.norm(mean) >= tau

        if len(out[-1]) < moments.PIECE_MIN or alike(w):
            out[-1].append(w)
            mean += ix.frames[w]
            i += 1
            continue
        back = next((n for n in range(1, look + 1) if i + n < len(members) and alike(members[i + n])), None)
        if back:
            out[-1].extend(members[i:i + back + 1])
            mean += ix.frames[members[i + back]]
            i += back + 1
            continue
        out.append([w])
        mean = ix.frames[w].copy()
        i += 1
    return out


def split(ix, w):
    t = lambda i: ix.rows[i][1]  # noqa: E731
    if ix.rows[w[-1]][2] - t(w[0]) <= LONG:
        return [w]
    cand = [k for k in range(1, len(w)) if t(w[k]) - t(w[0]) >= SIDE and ix.rows[w[-1]][2] - t(w[k]) >= SIDE]
    k = min(cand, key=lambda k: float(ix.frames[w[k - 1]] @ ix.frames[w[k]]))
    return split(ix, w[:k]) + split(ix, w[k:])


def card_windows(ix):
    by_video = {}
    for i, (video, _s, _e) in enumerate(ix.rows):
        if not ix.flat[i]:
            by_video.setdefault(video, []).append(i)
    out = []
    for video in sorted(by_video):
        members = sorted(by_video[video], key=lambda i: ix.rows[i][1])
        for r in runs(ix, members, CARD, LOOK):
            out += split(ix, r)
    return out


def keyframes(ix, w):
    """The windows to describe a card from (see the module's doc)."""
    picked = []
    for run in runs(ix, w, STILL, 0) if len(w) > 1 else [w]:
        picked.append(run[0])
        t0 = ix.rows[run[0]][1]
        for k in range(1, math.ceil((ix.rows[run[-1]][2] - t0) / GAP)):
            chunk = [i for i in run if t0 + k * GAP <= ix.rows[i][1] < t0 + (k + 1) * GAP]
            if chunk:
                picked.append(min(chunk, key=lambda i: max(float(ix.frames[i] @ ix.frames[p]) for p in picked)))
    picked = sorted(set(picked), key=lambda i: ix.rows[i][1])
    while len(picked) > MOST:
        k = max(range(1, len(picked)), key=lambda k: float(ix.frames[picked[k - 1]] @ ix.frames[picked[k]]))
        del picked[k]
    return picked


def grab(case, video, path, t):
    out = HERE / "frames" / case / video / f"{round(t * 1000):07d}.jpg"
    if not out.exists():
        out.parent.mkdir(parents=True, exist_ok=True)
        # A seek within a frame of the file's end gives none (a DJI clip, 0.02 s before it): back off.
        for at in (t, t - 0.5, t - 1.0):
            done = subprocess.run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-ss", f"{max(0.0, at):.3f}", "-i", str(path),  # noqa: S607
                                   "-frames:v", "1", "-vf", "scale=512:-2", "-q:v", "3", "-y", str(out)], check=False)
            if done.returncode == 0 and out.exists():
                break
        else:
            raise RuntimeError(f"no frame at {t:.3f} s of {path}")
    return out


def describe_case(case, key, kind, url):
    d = next((Path.home() / "A-NAS-datasets" / "cache" / f"video-moments-{key}").glob("embeddinggemma*"))
    ix = moments.Index.load(str(d / "index.json"), str(d / "frames.npz"))
    path = HERE / f"cards-{case}.json"
    done = read_json(path) if path.exists() else {}
    prof = profile(kind)
    found = card_windows(ix)
    for n, w in enumerate(found, 1):
        video, start, end = ix.rows[w[0]][0], ix.rows[w[0]][1], ix.rows[w[-1]][2]
        cid = f"{case}-{n:03d}"
        if cid in done:
            continue
        kf = keyframes(ix, w)
        if len(kf) >= FEWEST:
            times = [max(start, (ix.rows[i][1] + ix.rows[i][2]) / 2) for i in kf]
        else:
            kf, times = [], [start + (end - start) * (k + 0.5) / FEWEST for k in range(FEWEST)]
        files = [grab(case, video, VIDEOS / ix.videos[video]["file"], t) for t in times]
        entry = {"id": cid, "video": video, "start": start, "end": end, "windows": len(w), "times": times,
                 "frames": [str(f) for f in files]}
        t0 = time.perf_counter()
        try:
            answer, score, seconds, errors = tag_beats.describe_v2(
                [str(f) for f in files], [t - start for t in times], list(range(1, len(files) + 1)), end - start, url, prof)
            ends = [*times[1:], end]
            for b, a_, b_ in zip(answer["beats"], [start, *times[1:]], ends, strict=True):
                b["start"], b["end"] = round(a_, 2), round(b_, 2)
            # Two beats said alike over frames that are not (window vectors under STILL): flagged.
            vecs = [ix.frames[i] for i in kf] if kf else None
            flags = []
            whats = [b["what"].strip("。. ") for b in answer["beats"]]
            flags = [[i + 1, j + 1] for i in range(len(whats)) for j in range(i + 1, len(whats))
                     if whats[i] == whats[j] and (vecs is None or float(vecs[i] @ vecs[j]) < STILL)]
            entry |= {"answer": answer, "score": score, "seconds": round(seconds, 1), "errors": errors, "repeats": flags}
        except Exception as error:  # noqa: BLE001  kept as the card's result
            entry |= {"error": str(error), "seconds": round(time.perf_counter() - t0, 1)}
        done[cid] = entry
        write_json(done, path)
        print(cid, f"{end - start:.0f}s", len(files), "帧", entry["seconds"], "秒",
              entry.get("answer", {}).get("summary", entry.get("error")), flush=True)
        if n % 5 == 0:
            page()
    page()


def thumb(p):
    im = Image.open(p).convert("RGB")
    im.thumbnail((128, 128))
    b = io.BytesIO()
    im.save(b, "JPEG", quality=58)
    return "data:image/jpeg;base64," + base64.b64encode(b.getvalue()).decode()


def page():
    import html
    esc = html.escape
    tabs, panes, rows = [], [], []
    for case, _key, _kind, name in CASES:
        path = HERE / f"cards-{case}.json"
        if not path.exists():
            continue
        cards = list(read_json(path).values())
        ok = [c for c in cards if "answer" in c]
        beats = sum(len(c["answer"]["beats"]) for c in ok)
        secs = [c["seconds"] for c in ok]
        rows.append(f"<tr><td>{name}</td><td>{len(cards)}</td><td>{beats}</td><td>{sum(secs) / 60:.1f} 分钟</td>"
                    f"<td>{(sorted(secs)[len(secs) // 2] if secs else 0):.0f} 秒</td><td>{sum(bool(c.get('repeats')) for c in ok)}</td>"
                    f"<td>{len(cards) - len(ok)}</td></tr>")
        body = []
        for c in cards:
            head = f"<b>{c['id']}</b> 视频{c['video']} {c['start']:.0f}–{c['end']:.0f} 秒（{c['end'] - c['start']:.0f} 秒），{len(c['frames'])} 帧，用时 {c['seconds']:.0f} 秒"
            if "answer" not in c:
                body.append(f"<div class=card>{head}<p class=bad>失败：{esc(c['error'][:200])}</p></div>")
                continue
            a = c["answer"]
            beat_rows = "".join(
                f"<div class=beat><img src='{thumb(f)}'><div><b>{n}. {'★亮点 ' if n in a.get('highlights', []) else ''}{esc(b['shot_size'])}</b>：{esc(b['what'])}"
                f"<div class=dim>{b['start'] - c['start']:.0f}–{b['end'] - c['start']:.0f} 秒</div></div></div>"
                for n, (b, f) in enumerate(zip(a["beats"], c["frames"], strict=True), 1))
            rep = f"<p class=bad>重复：{c['repeats']}</p>" if c.get("repeats") else ""
            meta = (f"{esc(a['scene'])} · {esc(a['place'])} · {esc(a['time_of_day'])} · {esc(a['mood'])} · 地标：{esc(a['landmark'] or '—')} · "
                    f"标签：{esc('、'.join(a['tags']))} · 精彩度 {a['highlight']}（加权 {c['score']['highlight_p']}）· {esc('、'.join(a['issues']))}")
            body.append(f"<div class=card>{head}<p class=sum>{esc(a['summary'])}</p><p class=dim>{meta}</p>{rep}<div class=beats>{beat_rows}</div></div>")
        tabs.append(f"<button onclick=\"show('c{case}')\" id=b-c{case}>{name}（{len(cards)}）</button>")
        panes.append(f"<section id=c{case} hidden>{''.join(body)}</section>")
    html_ = f"""<!doctype html><meta charset=utf-8><meta name=viewport content='width=device-width,initial-scale=1'><title>关键帧卡片</title>
<style>:root{{--bg:#f6f5f2;--card:#fff;--ink:#222;--dim:#777;--line:#ddd;--acc:#a0522d;--bad:#c0392b}}@media (prefers-color-scheme:dark){{:root{{--bg:#1b1b1d;--card:#252528;--ink:#e8e8e8;--dim:#9a9a9a;--line:#3a3a3e;--acc:#e0a070;--bad:#ff7b6b}}}}
body{{background:var(--bg);color:var(--ink);font:14px/1.55 -apple-system,'PingFang SC',sans-serif;margin:0;padding:20px 16px}}main{{max-width:1200px;margin:0 auto}}
table{{border-collapse:collapse;background:var(--card)}}td,th{{border:1px solid var(--line);padding:4px 10px;text-align:left}}
.card{{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:10px 12px;margin:10px 0}}.sum{{margin:4px 0;padding:4px 8px;border-left:3px solid var(--acc)}}
.dim{{color:var(--dim);font-size:12px}}.bad{{color:var(--bad)}}.beats{{display:grid;grid-template-columns:repeat(auto-fill,minmax(300px,1fr));gap:4px 14px}}
.beat{{display:flex;gap:8px;align-items:flex-start;border-top:1px dashed var(--line);padding:4px 0}}.beat img{{width:110px;border-radius:3px;flex:none}}
nav button{{font-size:14px;padding:5px 12px;margin:10px 6px 4px 0}}nav button.on{{font-weight:700}}</style>
<main><h1>关键帧卡片：侦探片、上海、深圳冲浪</h1>
<p class=dim>更新于 {time.strftime('%H:%M')}（还在跑时每 5 张卡片刷新一次）。卡片：只按画面切，0.86，往后看 2 个窗口，超过 2 分钟拆开。关键帧：和当前组的平均画面相似度低于 0.9 取一帧，同一组 12 秒没有新帧就补一张最不像的，至少 3 张、最多 20 张；一张关键帧一个节拍。Qwen3.5-4B，这台 Mac。</p>
<table><tr><th>视频</th><th>卡片</th><th>节拍</th><th>4B 总用时</th><th>每张中位</th><th>有重复的卡片</th><th>失败</th></tr>{''.join(rows)}</table>
<nav>{''.join(tabs)}</nav>{''.join(panes)}
<script>function show(k){{document.querySelectorAll('section').forEach(s=>s.hidden=s.id!==k);document.querySelectorAll('nav button').forEach(b=>b.classList.toggle('on',b.id==='b-'+k))}}const f=document.querySelector('section');if(f)show(f.id)</script></main>"""
    (HERE / "关键帧卡片.html").write_text(html_, encoding="utf-8")


if __name__ == "__main__":
    url = sys.argv[1]
    only = sys.argv[2:]
    for case, key, kind, _name in CASES:
        if not only or case in only:
            describe_case(case, key, kind, url)
    print("ALL-DONE", flush=True)
