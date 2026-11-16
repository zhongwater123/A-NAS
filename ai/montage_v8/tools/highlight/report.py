"""How the beats the user marked are used in each cut, and a page to watch the cuts with their shots coloured
by those marks. Written beside the cuts (the profile's cuts folder), so its videos and key frames load from there.

  python3 -m montage_v8.tools.highlight.report LABELS OUT.html "TITLE" GROUP:TAG=NAME ...
  e.g. python3 -m montage_v8.tools.highlight.report labels-pj.json 精彩度对比.html "精彩度试验：品杰 × Symphony" \\
           基线:-symphony-place-3=place-3 基线:-symphony-place-4=place-4 加精彩度:-symphony-label-1=label-1

Usage counts a beat as used when a shot of the cut is taken at its key frame (the clips' "beat"). A shot is
the planner's when step two listed its beat for that picture, the program's fill otherwise. Shares are of the
cut's seconds; the chorus is the song's parts at level 高潮.
"""
import html
import json
import os
import sys
from collections import Counter

from montage_v8.tools import CUTS, read_json

esc = html.escape
MARKS = ("3", "2", "1", "x")
NAME = {"3": "精彩", "2": "可用", "1": "平淡", "x": "有问题", "": "未标"}


def rel(p):
    return os.path.relpath(p, CUTS).replace(os.sep, "/")


def usage(labels, tag):
    cut = read_json(CUTS / f"cut-pj{tag}.json")
    shots = read_json(CUTS / f"shots-pj{tag}.json")
    music = read_json(CUTS / cut["music"])
    listed = {(p, k): set(pic["beats"]) for p, part in enumerate(shots["raw"]["parts"], 1)
              for k, pic in enumerate(part["pictures"], 1)}
    seconds, chorus, used, planned = Counter(), Counter(), set(), 0
    for c in cut["clips"]:
        mark = labels.get(c["beat"], "")
        length = (c["end"] - c["start"]) * c.get("slow", 1.0)
        seconds[mark] += length
        if music["parts"][c["part"] - 1]["level"] == "高潮":
            chorus[mark] += length
        used.add(c["beat"])
        planned += c["beat"] in listed[(c["part"], c["picture"])]
    # Out of the beats this plan could choose: those of the cards in its catalog (CUT_WIDE leaves out the upright).
    pick = read_json(CUTS / f"pick-pj{tag}.json")
    catalog = {line.split(" ", 1)[0] for line in pick["user"].splitlines() if line.startswith("pj-")}
    total = {v: sum(1 for b, m in labels.items() if m == v and b.split("/")[0] in catalog) for v in MARKS}
    hit = Counter(labels.get(b, "") for b in used)
    return {"tag": tag, "shots": len(cut["clips"]), "planned": planned, "seconds": round(cut["seconds"], 1),
            "share": {v: seconds[v] / sum(seconds.values()) for v in MARKS},
            "chorus": {v: chorus[v] / sum(chorus.values()) for v in MARKS} if chorus else {},
            "used": {v: hit[v] for v in MARKS}, "of": total,
            "cost": round(sum((read_json(CUTS / f"{s}-pj{tag}.json")["cost"].get("usd") or 0) for s in ("pick", "shots")), 2)}


def pct(x):
    return f"{x * 100:.0f}%"


def table(rows, groups):
    head = ("<tr><th>剪法</th><th>镜头</th><th>规划选的</th><th>精彩占成片</th><th>平淡占成片</th><th>有问题占成片</th>"
            "<th>副歌里精彩</th><th>精彩节拍用了</th><th>平淡节拍用了</th><th>有问题节拍用了</th></tr>")
    body = []
    for group in groups:
        mine = [r for r in rows if r["group"] == group]
        body.extend(line(r["name"], [r], group) for r in mine)
        if len(mine) > 1:
            body.append(line(f"{group}平均", mine, group, mean=True))
    return f"<table>{head}{''.join(body)}</table>"


def line(name, rs, group, mean=False):
    def avg(f):
        return sum(f(r) for r in rs) / len(rs)

    def used(v):
        u, of = avg(lambda r: r["used"][v]), rs[0]["of"][v]
        return f"{u:.1f} / {of}（{pct(u / of)}）" if mean else f"{u:.0f} / {of}（{pct(u / of)}）"

    cls = " class=mean" if mean else ""
    return (f"<tr{cls}><td><span class=grp>{esc(group)}</span> {esc(name)}</td><td>{avg(lambda r: r['shots']):.0f}</td>"
            f"<td>{avg(lambda r: r['planned']):.0f}</td><td>{pct(avg(lambda r: r['share']['3']))}</td>"
            f"<td>{pct(avg(lambda r: r['share']['1']))}</td><td>{pct(avg(lambda r: r['share']['x']))}</td>"
            f"<td>{pct(avg(lambda r: r['chorus'].get('3', 0)))}</td><td>{used('3')}</td><td>{used('1')}</td><td>{used('x')}</td></tr>")


def film(labels, r, n):
    cut = read_json(CUTS / f"cut-pj{r['tag']}.json")
    pick = read_json(CUTS / f"pick-pj{r['tag']}.json")["answer"]
    music = read_json(CUTS / cut["music"])
    strip, t = [], 0.0
    for c in cut["clips"]:
        a = t
        t += (c["end"] - c["start"]) * c.get("slow", 1.0)
        mark = labels.get(c["beat"], "")
        hot = music["parts"][c["part"] - 1]["level"] == "高潮"
        strip.append(f"<button class='shot m{mark or 'n'}{' hot' if hot else ''}' data-t='{a:.2f}' data-e='{t:.2f}' "
                     f"title='{a:.1f} 秒 · {esc(c['beat'])} · {NAME[mark]} · {esc(c['what'])}'>"
                     f"<img src='{esc(rel(c['frame']))}' loading=lazy alt=''><i></i></button>")
    parts = "".join(f"<li><b>{esc(x['name'])}</b>：{esc(x['summary'])}</li>" for x in pick["parts"])
    return f"""<article><header><h2><span class=grp>{esc(r['group'])}</span> {esc(r['name'])}</h2>
<p class=dim>{r['shots']} 个镜头 · 精彩占 {pct(r['share']['3'])} · 副歌里精彩 {pct(r['chorus'].get('3', 0))} · 规划 {r['cost']} 美元</p></header>
<video controls preload=metadata playsinline src='{esc(rel(cut['cut']))}'></video>
<div class=strip>{''.join(strip)}</div>
<details><summary>第一步大纲</summary><p>{esc(pick['design'])}</p><p><b>开头：</b>{esc(pick.get('opening', ''))}</p>
<p><b>结尾：</b>{esc(pick.get('ending', ''))}</p><ol>{parts}</ol></details></article>"""


NOTE = os.environ.get("REPORT_NOTE", "")    # a sentence on how these cuts were made, after the page's first line


def main():
    labels_path, out, title = sys.argv[1:4]
    labels = read_json(CUTS / labels_path)["labels"]
    runs = []
    for arg in sys.argv[4:]:
        group, rest = arg.split(":", 1)
        tag, name = rest.split("=", 1)
        runs.append(usage(labels, tag) | {"group": group, "name": name})
    groups = list(dict.fromkeys(r["group"] for r in runs))
    count = Counter(labels.values())
    write = CUTS / out
    stats = write.with_suffix(".json")
    stats.write_text(json.dumps(runs, ensure_ascii=False, indent=1), encoding="utf-8")
    legend = "".join(f"<span><i class=m{v}></i>{NAME[v]} {count[v]}</span>" for v in MARKS)
    page = f"""<!doctype html><html lang=zh-CN><head><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1">
<title>{esc(title)}</title><style>
:root{{--bg:#f7f6f3;--card:#fff;--ink:#1f1f1f;--dim:#6b6b6b;--line:#e2e0db;--acc:#9a4b1c;--m3:#e0a106;--m2:#3b82f6;--m1:#9ca3af;--mx:#dc2626;--mn:transparent}}
@media (prefers-color-scheme:dark){{:root:not([data-theme=light]){{--bg:#18181a;--card:#222225;--ink:#ececec;--dim:#9c9c9c;--line:#38383c;--acc:#e3a072;--m1:#6b7280}}}}
:root[data-theme=dark]{{--bg:#18181a;--card:#222225;--ink:#ececec;--dim:#9c9c9c;--line:#38383c;--acc:#e3a072;--m1:#6b7280}}
*{{box-sizing:border-box}} body{{background:var(--bg);color:var(--ink);font:14px/1.55 -apple-system,"PingFang SC",sans-serif;margin:0;padding:0 16px 48px}}
h1{{font-size:20px;margin:18px 0 4px}} h2{{font-size:15px;margin:0}} .dim{{color:var(--dim);font-size:12px;margin:2px 0 8px}}
.grp{{font-size:12px;color:var(--acc);font-weight:600}}
.wrap{{overflow-x:auto;margin:12px 0}} table{{border-collapse:collapse;font-size:13px;min-width:820px;background:var(--card)}}
th,td{{border:1px solid var(--line);padding:5px 9px;text-align:right;white-space:nowrap}} th:first-child,td:first-child{{text-align:left}}
th{{font-weight:600;color:var(--dim)}} tr.mean td{{font-weight:600;background:color-mix(in srgb,var(--acc) 8%,transparent)}}
.legend{{display:flex;gap:14px;flex-wrap:wrap;color:var(--dim);font-size:12px}} .legend i{{display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:4px;vertical-align:-1px}}
.legend .m3{{background:var(--m3)}} .legend .m2{{background:var(--m2)}} .legend .m1{{background:var(--m1)}} .legend .mx{{background:var(--mx)}}
.notes{{color:var(--dim);font-size:12px;max-width:900px}} .notes li{{margin:2px 0}}
.grid{{display:grid;grid-template-columns:repeat(auto-fill,minmax(min(100%,520px),1fr));gap:20px;margin-top:16px}}
article{{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:12px}}
video{{width:100%;aspect-ratio:16/9;background:#000;border-radius:6px;display:block}}
.strip{{display:flex;flex-wrap:wrap;gap:3px;margin-top:8px}}
.shot{{position:relative;width:62px;height:35px;padding:0;border:0;border-radius:3px;overflow:hidden;background:#000;cursor:pointer;outline:2px solid transparent}}
.shot img{{width:100%;height:100%;object-fit:cover;display:block}} .shot i{{position:absolute;left:0;right:0;bottom:0;height:5px}}
.shot.m3 i{{background:var(--m3)}} .shot.m2 i{{background:var(--m2)}} .shot.m1 i{{background:var(--m1)}} .shot.mx i{{background:var(--mx)}}
.shot.hot{{box-shadow:inset 0 3px 0 #d1495b}} .shot.now{{outline-color:var(--ink)}}
details{{margin-top:8px;font-size:13px}} summary{{cursor:pointer;color:var(--dim)}}
</style></head><body>
<h1>{esc(title)}</h1>
<p class=dim>每个节拍的精彩度由人标注（{sum(count.values())} 个节拍）。{esc(NOTE)}缩略图下沿的颜色是这个镜头所用节拍的标注，上沿红线表示在副歌里；点缩略图跳到那个镜头。</p>
<div class=legend>{legend}<span><i style="background:#d1495b"></i>副歌</span></div>
<div class=wrap>{table(runs, groups)}</div>
<ul class=notes><li>“精彩占成片”“平淡占成片”按成片秒数算；“副歌里精彩”只算副歌（音乐的高潮段）。</li>
<li>“精彩节拍用了”：成片里有镜头取自这个节拍的关键帧，就算用了一次；分母是这次规划的目录里标为这一档的节拍数。</li>
<li>“规划选的”：第二步给这个画面列过的节拍；其余镜头是卡片不够长时程序补位用的。</li></ul>
<div class=grid>{''.join(film(labels, r, n) for n, r in enumerate(runs))}</div>
<script>
for (const art of document.querySelectorAll("article")) {{
  const v = art.querySelector("video"), shots = [...art.querySelectorAll(".shot")];
  shots.forEach(s => s.onclick = () => {{ v.currentTime = +s.dataset.t + 0.05; v.play(); }});
  v.ontimeupdate = () => shots.forEach(s => s.classList.toggle("now", v.currentTime >= +s.dataset.t && v.currentTime < +s.dataset.e));
  v.onplay = () => document.querySelectorAll("video").forEach(o => {{ if (o !== v) o.pause(); }});
}}
</script></body></html>"""
    write.write_text(page, encoding="utf-8")
    print(write, stats)
    for r in runs:
        print(r["group"], r["name"], r["shots"], "镜头", "精彩占", pct(r["share"]["3"]), "副歌精彩", pct(r["chorus"].get("3", 0)),
              "用了 精彩", r["used"]["3"], "平淡", r["used"]["1"], "有问题", r["used"]["x"], "规划选的", r["planned"])


if __name__ == "__main__":
    main()
