"""上海卡片.html (or another case): every card of one video in the order the 4B wrote it
(关键帧节拍 → 亮点 → 过程 → 概括 → 其余字段; cut.load's description), with what the two 剪法25 takes did with it.

  python3 case_page.py 02
"""

import html
import os
import sys

import cut as plan
from paths import CARDS as HERE, CUTS as CUT, read_json

NAMES = {"01": "侦探片", "02": "上海vlog", "04": "深圳冲浪"}
esc = html.escape


def clock(t):
    return f"{int(t // 60)}:{t % 60:04.1f}"


def main(case):
    cards = {c["id"]: c for c in plan.load(case)}     # the description both steps read
    titles = read_json(CUT / f"titles-{case}.json")
    takes = {}
    for n in [int(x) for x in os.environ.get("TAKES", "1,2").split(",")]:
        pick = read_json(CUT / f"pick-{case}-{n}.json")["answer"]
        cut = read_json(CUT / f"cut-{case}-{n}.json")
        outlined = {cid for p in pick["parts"] for s in p["scenes"] for cid in s["cards"]}
        used = {}
        for c in cut["clips"]:
            cid, ks = c["beat"].split("/")
            used.setdefault(cid, []).extend(int(k) for k in ks.split("+"))
        takes[n] = (outlined, used)
    rows, body = [], []
    for c in cards.values():
        cid, a = c["id"], c.get("answer")
        if not a:
            continue
        marks = []
        if titles[cid]["title"]:
            marks.append("<span class='tag bad'>报幕，规划看不到</span>")
        for n, (outlined, used) in takes.items():
            if cid in used:
                marks.append(f"<span class='tag on'>第{n}次 用了节拍 {'、'.join(map(str, sorted(set(used[cid]))))}</span>")
            elif cid in outlined:
                marks.append(f"<span class='tag'>第{n}次 大纲选了</span>")
        hl = a.get("highlights", [])
        beats = []
        for k, b in enumerate(a["beats"], 1):
            f = c["frames"][k - 1]
            src = os.path.relpath(f, HERE).replace(os.sep, "/")
            mark = "<b class=star>★亮点</b> " if k in hl else ""
            beats.append(f"<li class=beat><img src='{esc(src)}' loading=lazy><div><b>{k}.</b> {mark}<span class=dim>{esc(b['shot_size'])} · "
                         f"{clock(b['start'])}–{clock(b['end'])}</span><br>{esc(b['what'])}</div></li>")
        line = f"{cid} {clock(c['start'])} {c['end'] - c['start']:.0f}秒 | {a.get('scene', '')} | {' → '.join(a.get('process', [])) or a['summary']}"
        rows.append(f"<tr class='{'used' if any(cid in u for _o, u in takes.values()) else ''}'><td><a href='#{cid}'>{cid}</a></td>"
                    f"<td>{clock(c['start'])}</td><td>{c['end'] - c['start']:.0f} 秒</td><td>{len(a['beats'])}</td>"
                    f"<td>{esc(a.get('scene', ''))}</td><td>{esc(a['summary'])}</td><td>{''.join(marks)}</td></tr>")
        meta = (f"场景：{esc(a['scene'])} · 地点：{esc(a['place'])} · {esc(a['time_of_day'])} · 情绪：{esc(a['mood'])} · "
                f"地标：{esc(a['landmark'] or '—')} · 标签：{esc('、'.join(a['tags']))} · 精彩度 {a['highlight']} · 问题：{esc('、'.join(a['issues']))}")
        body.append(f"<article class=card id='{cid}'><h3>{cid}<span class=dim>　{clock(c['start'])}–{clock(c['end'])}，"
                    f"{c['end'] - c['start']:.0f} 秒，{len(a['beats'])} 个关键帧</span> {''.join(marks)}</h3>"
                    f"<h4>① 逐个关键帧</h4><ol class=beats>{''.join(beats)}</ol>"
                    f"<h4>② 亮点</h4><p>{'、'.join(f'第 {k} 个' for k in hl) or '—'}</p>"
                    f"<h4>③ 过程</h4><p>{esc(' → '.join(a.get('process', [])) or '—')}</p>"
                    f"<h4>④ 概括</h4><p class=sum>{esc(a['summary'])}</p>"
                    f"<p class=dim>{meta}</p>"
                    f"<details><summary class=dim>规划第一步看到的目录行</summary><code>{esc(line)}</code></details></article>")
    name = NAMES[case]
    page = f"""<!doctype html><html lang=zh><head><meta charset=utf-8><meta name=viewport content='width=device-width,initial-scale=1'>
<title>{name}卡片</title><style>
:root{{--bg:#f6f5f2;--card:#fff;--ink:#222;--dim:#777;--line:#ddd;--acc:#a0522d;--on:#1a7f37;--bad:#c0392b;--hl:#b8860b}}
@media (prefers-color-scheme:dark){{:root{{--bg:#1b1b1d;--card:#252528;--ink:#e8e8e8;--dim:#9a9a9a;--line:#3a3a3e;--acc:#e0a070;--on:#4ac26b;--bad:#ff7b6b;--hl:#f0c040}}}}
body{{background:var(--bg);color:var(--ink);font:14px/1.6 -apple-system,'PingFang SC',sans-serif;margin:0;padding:20px 16px}}
main{{max-width:1150px;margin:0 auto}} .dim{{color:var(--dim);font-size:12px}}
.wrap{{overflow-x:auto}} table{{border-collapse:collapse;background:var(--card);width:100%;font-size:13px}}
td,th{{border-bottom:1px solid var(--line);padding:4px 8px;text-align:left;vertical-align:top}} tr.used td:first-child{{border-left:3px solid var(--on)}}
.tag{{display:inline-block;font-size:12px;font-weight:400;border:1px solid var(--line);border-radius:10px;padding:0 7px;margin:1px 3px;white-space:nowrap}}
.tag.on{{color:var(--on);border-color:var(--on)}} .tag.bad{{color:var(--bad);border-color:var(--bad)}}
.card{{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:10px 14px;margin:14px 0;scroll-margin-top:10px}}
.card h3{{margin:0 0 4px;font-size:16px}} .card h4{{margin:8px 0 2px;font-size:13px;color:var(--acc)}}
.beats{{list-style:none;padding:0;margin:0;display:grid;grid-template-columns:repeat(auto-fill,minmax(320px,1fr));gap:6px 14px}}
.beat{{display:flex;gap:8px;align-items:flex-start}} .beat img{{width:128px;border-radius:4px;flex:none}}
.star{{color:var(--hl)}} .sum{{margin:2px 0;padding:4px 8px;border-left:3px solid var(--acc)}} code{{white-space:pre-wrap;font-size:12px}}
label{{margin-right:14px}}
</style></head><body><main>
<h1>{name}：{len(rows)} 张卡片（最新描述）</h1>
<p class=dim>每张卡片一次 Qwen3.5-4B 调用，按这个顺序写：先逐个关键帧写一个节拍，再挑 1–2 个亮点（3 张以内只挑 1 个），再按先后写过程，最后写概括（旅行不超过 80 字）和其余字段。
标记：绿色是剪法25 两次成片实际用了哪些节拍；“大纲选了”是第一步选了但成片没用上；红色是报幕卡片，规划前就过滤了。</p>
<p><label><input type=checkbox id=only> 只看两次成片用到的卡片</label></p>
<div class=wrap><table><tr><th>卡片</th><th>开始</th><th>时长</th><th>节拍</th><th>场景</th><th>概括</th><th>两次成片</th></tr>{''.join(rows)}</table></div>
{''.join(body)}
<script>document.getElementById('only').onchange=e=>{{const on=e.target.checked;
document.querySelectorAll('tr').forEach(r=>{{if(r.querySelector('td'))r.hidden=on&&!r.classList.contains('used')}});
document.querySelectorAll('.card').forEach(c=>c.hidden=on&&!c.querySelector('.tag.on'))}}</script>
</main></body></html>"""
    out = HERE / f"{name}卡片.html"
    out.write_text(page, encoding="utf-8")
    print(out)


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "02")
