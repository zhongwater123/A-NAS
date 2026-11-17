"""A cut log page: for each cut of a set, the film with the song's parts under it, step one's outline, every shot
in film order (its key frame, the beat, the seconds taken from the source, what step one asked for and what step
two meant it to show) and both steps' whole input. Written beside the cuts (paths.CUTS), so its videos and key
frames load from there; the cards are read from paths.CARDS (ANAS_MONTAGE_CARDS) for the cards' spans.

  python tools/log_page.py OUT.html CASE "TITLE" [OTHER:]TAG=NAME ...
  e.g. python tools/log_page.py 精简描述成片.html pj "精简描述：品杰 × Symphony" -symphony-slim-1=第 1 次

A run written OTHER:TAG is a cut of another case, so one page can set two sets of cards side by side; that case's
cards are looked for in paths.CARDS and then in each folder of LOG_CARDS (separated like PATH).
"""
import html
import json
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))  # montage_v5
from paths import CARDS, CUTS, read_json

esc = html.escape
COL = {"安静": "#7aa6c2", "中等": "#c9a227", "高潮": "#d1495b"}
ZH = {"intro": "前奏", "verse": "主歌", "chorus": "副歌", "bridge": "桥段", "outro": "尾奏", "inst": "间奏", "solo": "独奏",
      "break": "间歇", "build": "铺垫"}
SHORT = 1.5     # a shot under this many seconds is marked


def rel(p):
    return os.path.relpath(p, CUTS).replace(os.sep, "/")


def player(cut, music):
    total = music["seconds"]
    parts = music["parts"]
    count = [sum(1 for c in cut["clips"] if c["part"] == n) for n in range(1, len(parts) + 1)]
    secs = [[p["start"], p["end"], ZH.get(p.get("label", ""), p.get("label", "")), p["level"]] for p in parts]
    segs = "".join(f"<div class=seg style='left:{p['start'] / total * 100:.2f}%;width:{(p['end'] - p['start']) / total * 100:.2f}%;"
                   f"background:{COL[p['level']]}'><span>{esc(s[2])} · {n} 个镜头</span></div>"
                   for p, s, n in zip(parts, secs, count, strict=True))
    t, ticks = 0.0, []
    for c in cut["clips"][:-1]:
        t += (c["end"] - c["start"]) * c.get("slow", 1.0)
        ticks.append(f"<i style='left:{t / total * 100:.2f}%'></i>")
    video = Path(cut["cut"]).name
    return (f"<div class=player data-secs='{esc(json.dumps(secs, ensure_ascii=False))}' data-total='{total}'>"
            f"<div class=screen><video controls muted preload=metadata src='{esc(video)}'></video><div class=badge>{esc(secs[0][2])}</div></div>"
            f"<div class=music title='点一下跳到这里'>{segs}{''.join(ticks)}<b class=head></b></div></div>")


def outline(pick):
    a = pick["answer"]
    items = []
    for p in a["parts"]:
        scenes = "；".join(f"{esc(s['query'])}（{esc('、'.join(s['cards']))}）" for s in p["scenes"])
        items.append(f"<li><b>{esc(p['name'])}</b><br>{esc(p['summary'])}<br><span class=dim>衔接：{esc(p['link'])}</span>"
                     f"<br><span class=dim>画面：{scenes}</span></li>")
    unmet = "".join(f"<li>{esc(u)}</li>" for u in a.get("unmet", []))
    return (f"<h3>第一步大纲</h3><p>{esc(a['design'])}</p><p><b>开头为什么这样选：</b>{esc(a.get('opening', ''))}</p>"
            f"<p><b>结尾为什么这样选：</b>{esc(a.get('ending', ''))}</p><ol class=outline>{''.join(items)}</ol>"
            + (f"<p><b>第一步说素材里没有的：</b></p><ul>{unmet}</ul>" if unmet else ""))


def source_of(folder):
    """For joined footage (joined-parts.json beside the cards), where a moment of a joined video is in its files."""
    parts = read_json(folder / "joined-parts.json") if (folder / "joined-parts.json").exists() else {}

    def where(video, t):
        inside = [q for q in parts.get(video, []) if q["at"] <= t]
        return f"；{inside[-1]['file']} 的 {t - inside[-1]['at']:.1f} 秒" if inside else ""
    return where


def shots_table(cut, shots, music, cards, where):
    parts = music["parts"]
    planned = shots["answer"]["parts"]
    rows, t, last = [], 0.0, None
    for c in cut["clips"]:
        length = (c["end"] - c["start"]) * c.get("slow", 1.0)
        if c["part"] != last:
            last = c["part"]
            p = parts[c["part"] - 1]
            rows.append(f"<tr class=part><td colspan=6 style='border-left:4px solid {COL[p['level']]}'>段 {c['part']} · "
                        f"{esc(c['partName'])} · {esc(p['level'])}（{esc(p.get('label', ''))}）</td></tr>")
        card = cards[c["card"]]
        pic = planned[c["part"] - 1]["pictures"][c["picture"] - 1]
        slow = f"，放慢到 {c['slow']:.2f} 倍" if c.get("slow", 1) != 1 else ""
        warn = " class=warn" if length < SHORT else ""
        rows.append(
            f"<tr><td class=t>{t:.1f}–{t + length:.1f}<br><span class=dim>槽 {c['slot'][0]:.1f}–{c['slot'][1]:.1f}</span></td>"
            f"<td><img src='{esc(rel(c['frame']))}' loading=lazy alt=''></td>"
            f"<td><b>{esc(c['beat'])}</b> {esc(c['shotSize'])}<br>{esc(c['what'])}<br>"
            f"<span class=dim>原片 {c['start']:.1f}–{c['end']:.1f} 秒（卡片 {card['start']:.0f}–{card['end']:.0f}）{esc(where(c['video'], c['start']))}</span></td>"
            f"<td{warn}>{length:.1f} 秒{slow}</td><td>{esc(c['query'])}</td>"
            f"<td class=dim>{esc(pic['shows'])}<br>衔接：{esc(pic['link'])}<br>第二步排的节拍：{esc('、'.join(pic['beats']))}</td></tr>")
        t += length
    unmet = "".join(f"<li>{esc(u)}</li>" for u in shots["answer"].get("unmet", []))
    return ("<h3>逐个镜头（按成片时间）</h3><div class=wrap><table><tr><th>成片时间</th><th>关键帧</th><th>用的节拍</th><th>长度</th>"
            f"<th>第一步要的画面</th><th>第二步：要呈现 / 衔接 / 排的节拍</th></tr>{''.join(rows)}</table></div>"
            + (f"<p><b>第二步说没找到的：</b></p><ul>{unmet}</ul>" if unmet else ""))


def cards_of(case):
    for folder in [CARDS, *map(Path, filter(None, os.environ.get("LOG_CARDS", "").split(os.pathsep)))]:
        if (folder / f"cards-{case}.json").exists():
            return folder, read_json(folder / f"cards-{case}.json")
    raise SystemExit(f"cards-{case}.json is in neither {CARDS} nor LOG_CARDS")


def section(case, tag, name):
    folder, cards = cards_of(case)
    cut = read_json(CUTS / f"cut-{case}{tag}.json")
    pick = read_json(CUTS / f"pick-{case}{tag}.json")
    shots = read_json(CUTS / f"shots-{case}{tag}.json")
    music = read_json(CUTS / cut["music"])
    c1, c2 = pick["cost"], shots["cost"]
    head = (f"成片 {cut['seconds']:.1f} 秒，{len(cut['clips'])} 个镜头；音乐取 {music['excerpt'][0]:.1f}–{music['excerpt'][1]:.1f} 秒，"
            f"{music['tempo']:.0f} BPM，一小节 {music['bar']:.2f} 秒。第一步 {c1['seconds']:.0f} 秒 ${c1['usd']:.2f}，"
            f"第二步 {c2['seconds']:.0f} 秒 ${c2['usd']:.2f}。描述：{esc(folder.name)}/{esc(pick.get('descriptions', ''))}"
            + ("；只用横拍" if pick.get("wide") else "") + ("；带人工精彩度" if pick.get("labels") else "") + "。")
    raw = f"{pick['system']}\n\n{pick['user']}\n\n———— 第二步 ————\n\n{shots['system']}\n\n{shots['user']}"
    return (f"<section><h2>{esc(name)}</h2><p class=dim>{head}</p>{player(cut, music)}{outline(pick)}"
            f"{shots_table(cut, shots, music, cards, source_of(folder))}<details><summary>第一步、第二步的完整输入</summary><pre>{esc(raw)}</pre></details></section>")


STYLE = """:root{--bg:#f7f6f3;--card:#fff;--ink:#1f1f1f;--dim:#6b6b6b;--line:#e2e0db;--acc:#9a4b1c;--bad:#b42318}
@media (prefers-color-scheme:dark){:root:not([data-theme=light]){--bg:#18181a;--card:#222225;--ink:#ececec;--dim:#9c9c9c;--line:#38383c;--acc:#e3a072;--bad:#ff8a7a}}
:root[data-theme=dark]{--bg:#18181a;--card:#222225;--ink:#ececec;--dim:#9c9c9c;--line:#38383c;--acc:#e3a072;--bad:#ff8a7a}
*{box-sizing:border-box} body{background:var(--bg);color:var(--ink);font:15px/1.6 -apple-system,"PingFang SC",sans-serif;margin:0;padding:24px 16px 48px}
main{max-width:1200px;margin:0 auto} h1{font-size:24px} h2{font-size:20px;margin-top:36px;padding-top:16px;border-top:1px solid var(--line)} h3{font-size:15px;color:var(--acc)}
.dim{color:var(--dim);font-size:13px} video{width:100%;max-width:960px;background:#000;border-radius:6px;aspect-ratio:16/9}
.music{position:relative;height:40px;background:var(--card);border:1px solid var(--line);border-radius:6px;overflow:hidden}
.seg{position:absolute;top:0;bottom:0;opacity:.85} .seg span{position:absolute;left:6px;top:10px;font-size:12px;color:#fff;white-space:nowrap}
.music i{position:absolute;top:0;bottom:0;width:1px;background:rgba(255,255,255,.8)}
.player{max-width:960px} .screen{position:relative;border-radius:8px;outline:4px solid transparent;transition:outline-color .15s} .screen video{display:block;max-width:none}
.screen.chorus{outline-color:#d1495b} .badge{position:absolute;left:12px;top:12px;background:rgba(0,0,0,.65);color:#fff;font-size:15px;font-weight:600;padding:4px 12px;border-radius:999px;pointer-events:none}
.screen.chorus .badge{background:#d1495b} .music{margin-top:8px;cursor:pointer} .music b.head{position:absolute;top:-2px;bottom:-2px;width:3px;background:#fff;box-shadow:0 0 0 1px #000;left:0}
.outline li{margin:6px 0;font-size:14px} .wrap{overflow-x:auto} table{border-collapse:collapse;width:100%;font-size:13px;background:var(--card)}
td,th{border-bottom:1px solid var(--line);padding:6px;text-align:left;vertical-align:top} td img{width:150px;border-radius:4px} td.t{white-space:nowrap}
td.warn{color:var(--bad);font-weight:700} tr.part td{font-weight:600;background:var(--bg)} pre{white-space:pre-wrap;font-size:12px;background:var(--card);padding:10px;border-radius:6px}
nav a{margin-right:14px}"""

SCRIPT = """document.querySelectorAll('.player').forEach(pl=>{
  const v=pl.querySelector('video'),scr=pl.querySelector('.screen'),badge=pl.querySelector('.badge'),bar=pl.querySelector('.music'),head=pl.querySelector('.head');
  const secs=JSON.parse(pl.dataset.secs),total=+pl.dataset.total,chorus=secs.find(s=>s[3]==='高潮');
  function draw(){const t=v.currentTime;head.style.left=Math.min(100,t/total*100)+'%';
    const s=secs.find(s=>t>=s[0]&&t<s[1])||secs[secs.length-1];const inC=s[3]==='高潮';scr.classList.toggle('chorus',inC);
    let txt=s[2]+(inC?'（高潮）':'');if(chorus&&t<chorus[0]){const left=chorus[0]-t;txt+=left<=8?'　'+left.toFixed(1)+' 秒后进副歌':''}badge.textContent=txt;
    if(!v.paused)requestAnimationFrame(draw)}
  ['play','seeked','timeupdate','loadedmetadata'].forEach(e=>v.addEventListener(e,draw));
  bar.addEventListener('click',e=>{const r=bar.getBoundingClientRect();v.currentTime=(e.clientX-r.left)/r.width*total;draw()});
});"""


def main():
    out, case, title = sys.argv[1:4]
    runs = [(*(t.split(":", 1) if ":" in t else (case, t)), name) for t, name in (x.split("=", 1) for x in sys.argv[4:])]
    note = os.environ.get("LOG_NOTE", "")
    nav = "".join(f"<a href='#run{n}'>{esc(name)}</a>" for n, (*_, name) in enumerate(runs, 1))
    body = "".join(section(*run).replace("<section>", f"<section id=run{n}>", 1) for n, run in enumerate(runs, 1))
    page = (f"<!doctype html><html lang=zh><head><meta charset=utf-8><meta name=viewport content='width=device-width,initial-scale=1'>"
            f"<title>{esc(title)}</title><style>{STYLE}</style></head><body><main><h1>{esc(title)}</h1>"
            f"<p class=dim><b>视频默认静音。</b>画面左上角显示当前段落，进副歌前 8 秒开始倒数，副歌期间画面外框变红；下面的色条是音乐段落，"
            f"白线是播放位置，竖细线是镜头切点，点色条可以跳转。不到 {SHORT} 秒的镜头标红。{esc(note)}</p><nav>{nav}</nav>"
            f"{body}<script>{SCRIPT}</script></main></body></html>")
    (CUTS / out).write_text(page, encoding="utf-8")
    print(CUTS / out)


if __name__ == "__main__":
    main()
