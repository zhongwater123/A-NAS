"""剪法24.html (剪法25 takes): each case's cut, its 报幕 cards, outline and shots, beside the key frames."""

import html
import os

from paths import CARDS, CUTS as HERE, read_json

CASES = {"01": "侦探片预告", "02": "上海vlog", "04": "深圳冲浪"}
esc = html.escape


LABELS = {"materials": "素材", "card": "卡片", "content": "能用的内容", "design": "设计", "pace": "节奏",
          "unmet": "找不到的", "parts": "段落", "name": "名字", "summary": "概括", "weight": "分量",
          "scenes": "要的画面", "query": "画面", "cards": "卡片", "use": "用途", "shots": "镜头", "beat": "节拍",
          "shows": "要呈现", "link": "衔接", "pictures": "画面", "beats": "节拍（按优先顺序）"}


def shown(value):
    """A model's whole answer as nested lists with Chinese labels: every field and item, nothing dropped."""
    if isinstance(value, dict):
        rows = []
        for k, v in value.items():
            label = esc(LABELS.get(k, k))
            if isinstance(v, (dict, list)) and v and not all(isinstance(x, (str, int, float)) for x in (v if isinstance(v, list) else [0])):
                rows.append(f"<li><b>{label}</b>{shown(v)}</li>")
            elif isinstance(v, list):
                rows.append(f"<li><b>{label}：</b>{esc('、'.join(map(str, v))) if v else '（空）'}</li>")
            else:
                rows.append(f"<li><b>{label}：</b>{esc(str(v))}</li>")
        return "<ul>" + "".join(rows) + "</ul>"
    if isinstance(value, list):
        return "<ol>" + "".join(f"<li>{shown(v) if isinstance(v, (dict, list)) else esc(str(v))}</li>" for v in value) + "</ol>"
    return esc(str(value))


def rel(path):
    return os.path.relpath(path, HERE).replace(os.sep, "/")


def load(name):
    p = HERE / name
    return read_json(p) if p.exists() else None


def main():
    tabs, bodies, rows = [], [], []
    cards_all = {}
    # The numbered takes (cut-02-3.json); the trials' takes have named tags (cut-02-两层节拍-1.json).
    takes = [(case, base, tag) for case, base in CASES.items()
             for tag in sorted((f.stem[len(f"cut-{case}"):] for f in HERE.glob(f"cut-{case}-*.json")
                                if f.stem[len(f"cut-{case}-"):].isdigit()), key=lambda t: int(t[1:]))]
    takes = takes or [(case, base, "") for case, base in CASES.items()]
    for case, base, tag in takes:
        name = base + (f" 第{tag[1:]}次" if tag else "")
        key = case + tag
        cut, pick, shots = (load(f"{k}-{case}{tag}.json") for k in ("cut", "pick", "shots"))
        titles = load(f"titles-{case}.json")
        cards = {c["id"]: c for c in read_json(CARDS / f"cards-{case}.json").values()}
        cards_all[case] = cards
        if not cut or not shots or not pick:
            continue
        dropped = [i for i, t in (titles or {}).items() if t["title"]]
        title_s = sum(t["seconds"] for t in titles.values())
        rows.append(f"<tr><td>{case} {esc(name)}</td><td>{len(cards)}</td><td>{len(dropped)}</td>"
                    f"<td>{len({c for p in pick['answer']['parts'] for sc in p['scenes'] for c in sc['cards']})}</td><td>{cut.get('plannedShots', len(cut['clips']))} → {len(cut['clips'])}</td>"
                    f"<td>{cut['target']} 秒</td><td>{cut['seconds']:.1f} 秒</td>"
                    f"<td>{title_s / 60:.1f} 分</td><td>{pick['cost']['seconds']:.0f} 秒</td><td>{shots['cost']['seconds']:.0f} 秒</td>"
                    f"<td>{cut['renderSeconds']:.0f} 秒</td></tr>")
        tabs.append(f'<button data-case="{key}">{case} {esc(name)}</button>')
        out = [f'<section id="c{key}"><h2>{case} {esc(name)}</h2>',
               f"<p class=muted>指令：{esc(cut['instruction'])}　体裁：{esc(cut['style'])}　目标 {cut['target']} 秒，成片 {cut['seconds']:.1f} 秒</p>",
               f'<video controls preload="metadata" src="{esc(rel(cut["cut"]))}"></video>']
        # 报幕
        out.append(f"<h3>报幕过滤（{len(dropped)} 张）</h3>")
        if dropped:
            out.append('<div class="titles">')
            for i in dropped:
                c = cards[i]
                said = [s for s in titles[i]["said"] if s]
                out.append(f'<figure><img src="{esc(rel(c["frames"][len(c["frames"]) // 2]))}"><figcaption><b>{i}</b> '
                           f'{c["start"]:.0f}–{c["end"]:.0f} 秒<br>{esc(" ｜ ".join(said))}</figcaption></figure>')
            out.append("</div>")
        else:
            out.append("<p class=muted>没有卡片被判为报幕。</p>")
        kept_text = [(i, t) for i, t in titles.items() if not t["title"] and any(t["said"])]
        if kept_text:
            out.append(f"<details><summary>读到叠加文字但保留的卡片（{len(kept_text)} 张：三帧里少于两帧有）</summary><ul>")
            out += [f"<li><b>{i}</b> {esc(' ｜ '.join(s or '—' for s in t['said']))}</li>" for i, t in kept_text]
            out.append("</ul></details>")
        # Outline
        a = pick["answer"]
        out.append(f"<h3>第一步：大纲（看卡片过程）</h3><p><b>设计：</b>{esc(a['design'])}</p>"
                   f"<p><b>找不到的：</b>{esc('；'.join(a['unmet']) or '无')}</p>")
        out.append("<p class=muted>下面是成片的播放顺序：段落和每段要的画面。</p><ol>")
        for p in a["parts"]:
            scenes = "；".join(f"{esc(sc['query'])}（{'、'.join(sc['cards'])}）" for sc in p["scenes"])
            link = f"<br><span class=muted>衔接：{esc(p['link'])}</span>" if p.get("link") else ""   # 剪法23-24 takes have none
            out.append(f"<li><b>{esc(p['name'])}</b>（分量 {p['weight']}）{esc(p['summary'])}{link}"
                       f"<br><span class=muted>要的画面：{scenes}</span></li>")
        out.append("</ol>")
        # Shots
        out.append("<h3>第二步：排镜头（看节拍）</h3>")
        part = None
        names = {i + 1: p for i, p in enumerate(shots["answer"]["parts"])}
        out.append("<table class=shots><tr><th>帧</th><th>节拍</th><th>景别</th><th>画面</th><th>节拍长</th><th>实取</th>"
                   "<th>原片时间</th><th>要呈现</th><th>节拍描述</th><th>衔接</th></tr>")
        for c in cut["clips"]:
            if c["part"] != part:
                part = c["part"]
                p = names[part]
                out.append(f'<tr class=part><td colspan=10>段落 {part}：{esc(p["name"])}　<span class=muted>{esc(p["summary"])}</span></td></tr>')
            got = c["end"] - c["start"]
            span = c["share"]
            warn = " class=warn" if abs(got - span) > 0.3 else ""
            vid = f"视频{int(c['video'])} " if case == "04" else ""
            out.append(f'<tr><td><img src="{esc(rel(c["frame"]))}"></td><td>{c["beat"]}</td><td>{esc(c["shotSize"])}</td>'
                       f"<td>{c['weight']}</td><td>{span:.1f}</td><td{warn}>{got:.1f}</td>"
                       f"<td>{vid}{c['start']:.1f}–{c['end']:.1f}</td><td>{esc(c['shows'])}</td><td>{esc(c['what'])}</td>"
                       f"<td>{esc(c['link'])}</td></tr>")
        out.append("</table>")
        out.append(f"<p><b>第二步找不到的：</b>{esc('；'.join(shots['answer']['unmet']) or '无')}</p>")
        out.append(f"<h3>第一步模型原始输出（整理排版，内容完整）</h3><div class=raw>{shown(pick['answer'])}</div>"
                   f"<h3>第二步模型原始输出（整理排版，内容完整）</h3><div class=raw>{shown(shots.get('raw', shots['answer']))}</div>")
        out.append(f"<details><summary>4B 的两次输入</summary><h4>第一步</h4><pre>{esc(pick['system'])}\n\n{esc(pick['user'])}</pre>"
                   f"<h4>第二步</h4><pre>{esc(shots['system'])}\n\n{esc(shots['user'])}</pre></details></section>")
        bodies.append("\n".join(out))
    page = f"""<!doctype html><html lang="zh"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>剪法25 成片</title><style>
:root{{--bg:#fff;--fg:#1d1d1f;--muted:#6e6e73;--line:#e5e5ea;--card:#f5f5f7;--accent:#0a66c2;--warn:#b25000}}
@media (prefers-color-scheme:dark){{:root{{--bg:#151516;--fg:#f2f2f2;--muted:#a1a1a6;--line:#333;--card:#1f1f21;--accent:#5aa9ff;--warn:#ffa24d}}}}
body{{background:var(--bg);color:var(--fg);font:15px/1.6 -apple-system,"PingFang SC",sans-serif;margin:0 auto;max-width:1280px;padding:16px}}
h1{{font-size:22px}} h2{{margin-top:8px}} .muted{{color:var(--muted)}}
table{{border-collapse:collapse;width:100%;font-size:13px}} td,th{{border-bottom:1px solid var(--line);padding:6px;text-align:left;vertical-align:top}}
.shots img{{width:150px;border-radius:4px}} tr.part td{{background:var(--card);font-weight:600}}
td.warn{{color:var(--warn);font-weight:600}} video{{width:100%;max-width:960px;background:#000;border-radius:6px}}
.tabs button{{font:inherit;padding:6px 14px;margin-right:6px;border:1px solid var(--line);background:var(--card);color:var(--fg);border-radius:6px;cursor:pointer}}
.tabs button.on{{border-color:var(--accent);color:var(--accent)}} section{{display:none}} section.on{{display:block}}
.titles{{display:flex;flex-wrap:wrap;gap:10px}} .titles figure{{margin:0;width:220px;font-size:12px}} .titles img{{width:100%;border-radius:4px}}
.raw ul{{list-style:none;padding-left:14px;margin:2px 0}} .raw ol{{padding-left:22px;margin:2px 0}} .raw li{{margin:2px 0}} .raw{{font-size:14px;background:var(--card);padding:8px 12px;border-radius:6px}}
pre{{white-space:pre-wrap;font-size:12px;background:var(--card);padding:10px;border-radius:6px}}
.wrap{{overflow-x:auto}}
</style></head><body>
<h1>剪法25：关键帧卡片 + B 的大纲 + 按画面填秒</h1>
<p class=muted><b>卡片描述</b>：4B 先逐个关键帧写节拍（景别、看得见的主体和动作），再在同一次调用里挑出 1–2 个亮点节拍，按先后写过程，最后写概括；两步规划用的是同一份描述。
<b>报幕</b>：叠加的片名、演职员表、出品或标志压在素净的底上，或叠加的是片名，三帧里两帧这样的卡片不进规划。
<b>第一步（大纲）</b>：4B 看卡片过程（没有标签、没有亮点），衔接规则和体裁结构在前，写设计、找不到的，以及段落（分量、衔接）和每段 1–4 个画面（取自哪些卡片）。
<b>第二步</b>：程序按段落分量分秒，段里画面平分；4B 给每个画面按优先顺序列节拍（描述只是方向，不标亮点）。
<b>取画面</b>：每个画面按顺序取节拍，每个节拍从关键帧起取自己的长度，2 秒到上限（剧情片 4 秒、旅行 6 秒），取到凑满为止，零头顺延；同一张卡片相邻的节拍接成一段；切点（索引里的，加上按亮度归一化后看出的、黑底画面里的）两边的画面都不短于 2 秒，不到 1 秒的碎片切掉，原片本来就短的镜头整段取；头尾挪到 1 秒以上的停顿。
每个视频的编号成片（-1、-2…）各用一个随机种子规划。</p>
<div class=wrap><table><tr><th>视频</th><th>卡片</th><th>报幕</th><th>挑中</th><th>计划镜头 → 成片段数</th><th>目标</th><th>成片</th><th>报幕过滤</th><th>第一步</th><th>第二步</th><th>出片</th></tr>
{''.join(rows)}</table></div>
<p class=tabs>{''.join(tabs)}</p>
{''.join(bodies)}
<script>
const bs=[...document.querySelectorAll('.tabs button')];
function show(c){{bs.forEach(b=>b.classList.toggle('on',b.dataset.case===c));document.querySelectorAll('section').forEach(s=>s.classList.toggle('on',s.id==='c'+c));try{{localStorage.setItem('cut24',c)}}catch(e){{}}}}
bs.forEach(b=>b.onclick=()=>show(b.dataset.case));
let c=null;try{{c=localStorage.getItem('cut24')}}catch(e){{}}
show(bs.some(b=>b.dataset.case===c)?c:bs[0].dataset.case);
</script></body></html>"""
    (HERE / "剪法24.html").write_text(page, "utf-8")
    print(HERE / "剪法24.html")


if __name__ == "__main__":
    main()
