"""A page that plays several cuts of one set side by side: each on its own, or all together from one control
bar over the song's parts. Written beside the cuts (paths.CUTS), so its videos and frames load from there.

  python tools/compare_page.py OUT.html CASE music-<song>.json "TITLE" TAG=LABEL ...
  e.g. python tools/compare_page.py 品杰对比.html pj music-Forester-Symphony.json "品杰 × Symphony" \\
           -symphony-place-1=同一地点连续 -symphony-2=按时间顺序
"""
import html
import json
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))  # montage_v5
from paths import CUTS, read_json

esc = html.escape
COL = {"安静": "#7aa6c2", "中等": "#c9a227", "高潮": "#d1495b"}
ZH = {"intro": "前奏", "verse": "主歌", "chorus": "副歌", "bridge": "桥段", "outro": "尾奏", "inst": "间奏", "solo": "独奏",
      "break": "间歇", "build": "铺垫"}


def rel(p):
    return os.path.relpath(p, CUTS).replace(os.sep, "/")


OUT, CASE, MUSIC, TITLE = sys.argv[1:5]
RUNS = [tuple(x.split("=", 1)) for x in sys.argv[5:]]
m = read_json(CUTS / MUSIC)
total = m["seconds"]
secs = [[p["start"], p["end"], ZH.get(p.get("label", ""), p.get("label", "")), p["level"]] for p in m["parts"]]
bars = "".join(f"<div class=seg style='left:{p['start'] / total * 100:.2f}%;width:{(p['end'] - p['start']) / total * 100:.2f}%;"
               f"background:{COL[p['level']]}'><span>{ZH.get(p.get('label', ''), p.get('label', ''))}</span></div>" for p in m["parts"])
tiles = []
for n, (tag, name) in enumerate(RUNS):
    k = read_json(CUTS / f"cut-{CASE}{tag}.json")
    pick = read_json(CUTS / f"pick-{CASE}{tag}.json")
    strip, t = [], 0.0
    for c in k["clips"]:
        a = t
        t += (c["end"] - c["start"]) * c.get("slow", 1.0)
        hot = m["parts"][c["part"] - 1]["level"] == "高潮"
        strip.append(f"<img src='{esc(rel(c['frame']))}' data-t='{a:.2f}' data-e='{t:.2f}' class='{'hot' if hot else ''}' "
                     f"loading=lazy alt='' title='{a:.1f} 秒 · {esc(c['what'])}'>")
    parts = "".join(f"<li><b>{esc(x['name'])}</b>：{esc(x['summary'])}</li>" for x in pick["answer"]["parts"])
    tiles.append(f"""<article><h2>{esc(name)}</h2><p class=dim>{len(k['clips'])} 个镜头 · {esc(k['cut'].rsplit('/', 1)[-1])}</p>
<div class=screen><video controls {'' if n == 0 else 'muted '}preload=auto playsinline src='{esc(rel(k['cut']))}'></video><div class=badge></div></div>
<div class=strip>{''.join(strip)}</div>
<details><summary>第一步大纲</summary><p>{esc(pick['answer']['design'])}</p><p><b>结尾：</b>{esc(pick['answer'].get('ending', ''))}</p><ol>{parts}</ol></details></article>""")
page = f"""<!doctype html><html lang=zh><head><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1"><title>{esc(TITLE)}</title><style>
:root{{--bg:#f7f6f3;--card:#fff;--ink:#1f1f1f;--dim:#6b6b6b;--line:#e2e0db;--acc:#9a4b1c}}
@media (prefers-color-scheme:dark){{:root:not([data-theme=light]){{--bg:#18181a;--card:#222225;--ink:#ececec;--dim:#9c9c9c;--line:#38383c;--acc:#e3a072}}}}
:root[data-theme=dark]{{--bg:#18181a;--card:#222225;--ink:#ececec;--dim:#9c9c9c;--line:#38383c;--acc:#e3a072}}
*{{box-sizing:border-box}} body{{background:var(--bg);color:var(--ink);font:14px/1.55 -apple-system,"PingFang SC",sans-serif;margin:0;padding:0 16px 48px}}
h1{{font-size:20px;margin:16px 0 4px}} h2{{font-size:15px;margin:0;color:var(--acc)}} .dim{{color:var(--dim);font-size:12px;margin:2px 0 6px}}
.bar{{position:sticky;top:0;z-index:5;background:var(--bg);padding:10px 0;border-bottom:1px solid var(--line)}}
.ctl{{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-bottom:8px}} button,select{{font:inherit;padding:4px 14px;border-radius:6px;border:1px solid var(--line);background:var(--card);color:var(--ink);cursor:pointer}}
.music{{position:relative;height:30px;border-radius:6px;overflow:hidden;cursor:pointer;background:var(--card)}} .seg{{position:absolute;top:0;bottom:0;opacity:.85}}
.seg span{{position:absolute;left:6px;top:6px;font-size:12px;color:#fff;white-space:nowrap}} .head{{position:absolute;top:0;bottom:0;width:3px;background:#fff;box-shadow:0 0 0 1px #000;left:0}}
.grid{{display:grid;grid-template-columns:repeat(auto-fill,minmax(min(100%,520px),1fr));gap:20px;margin-top:16px}}
article{{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:12px;min-width:0}}
.screen{{position:relative;border-radius:6px;outline:3px solid transparent}} .screen.chorus{{outline-color:#d1495b}}
video{{display:block;width:100%;aspect-ratio:16/9;background:#000;border-radius:6px}}
.badge{{position:absolute;left:8px;top:8px;background:rgba(0,0,0,.65);color:#fff;font-size:12px;padding:2px 10px;border-radius:999px;pointer-events:none}} .screen.chorus .badge{{background:#d1495b}}
.strip{{display:flex;gap:2px;overflow-x:auto;margin-top:8px;padding-bottom:4px}} .strip img{{height:42px;border-radius:2px;cursor:pointer;border-bottom:3px solid transparent;opacity:.75}}
.strip img.hot{{border-bottom-color:#d1495b}} .strip img.now{{opacity:1;outline:2px solid var(--acc)}}
details{{margin-top:8px;font-size:13px}} details ol{{padding-left:20px}}
</style></head><body><div class=bar><h1>{esc(TITLE)}：{len(RUNS)} 条成片同步对比</h1>
<div class=ctl><button id=play>▶ 全部播放</button><span id=time class=dim>0.0 / {total:.1f} 秒</span>
<label class=dim>声音来自 <select id=sound>{''.join(f'<option value={i}>{esc(n)}</option>' for i, (_t, n) in enumerate(RUNS))}</select></label>
<span class=dim>每个视频可以单独播放（播哪条就听哪条的声音）；顶部按钮让全部一起播。点色条全部一起跳，点缩略图只跳那一条；缩略图下面红线是副歌镜头</span></div>
<div class=music id=music>{bars}<b class=head id=head></b></div></div>
<div class=grid>{''.join(tiles)}</div>
<script>
const secs={json.dumps(secs, ensure_ascii=False)},total={total};
const vids=[...document.querySelectorAll('video')],btn=document.getElementById('play'),sound=document.getElementById('sound');
let master=vids[0],sync=false,ours=0;          // ours: events we cause ourselves, not the viewer's
function quiet(f){{ours++;try{{f()}}finally{{setTimeout(()=>ours--,300)}}}}
function voice(i){{master=vids[i];sound.value=i;vids.forEach((v,j)=>v.muted=j!=i)}}
function look(v){{const t=v.currentTime,art=v.closest('article'),sc=art.querySelector('.screen');
  const s=secs.find(s=>t>=s[0]&&t<s[1])||secs[secs.length-1];sc.classList.toggle('chorus',s[3]==='高潮');sc.querySelector('.badge').textContent=s[2];
  art.querySelectorAll('.strip img').forEach(i=>i.classList.toggle('now',t>=+i.dataset.t&&t<+i.dataset.e))}}
function tick(){{vids.forEach(look);const t=master.currentTime;
  document.getElementById('head').style.left=Math.min(100,t/total*100)+'%';document.getElementById('time').textContent=t.toFixed(1)+' / '+total.toFixed(1)+' 秒';
  if(sync&&!master.paused)vids.forEach(v=>{{if(v!==master&&Math.abs(v.currentTime-t)>0.2)quiet(()=>v.currentTime=t)}});
  if(vids.some(v=>!v.paused))requestAnimationFrame(tick)}}
function stopSync(){{sync=false;btn.textContent='▶ 全部播放'}}
btn.onclick=()=>{{if(!sync){{const t=master.currentTime;sync=true;quiet(()=>vids.forEach(v=>{{v.currentTime=t;v.play()}}));btn.textContent='❚❚ 全部暂停';tick()}}
  else{{quiet(()=>vids.forEach(v=>v.pause()));stopSync()}}}};
sound.onchange=e=>voice(+e.target.value);
document.getElementById('music').onclick=e=>{{const r=e.currentTarget.getBoundingClientRect(),t=(e.clientX-r.left)/r.width*total;quiet(()=>vids.forEach(v=>v.currentTime=t));tick()}};
vids.forEach((v,i)=>{{
  v.addEventListener('play',()=>{{if(ours)return;if(sync)stopSync();voice(i);quiet(()=>vids.forEach(o=>{{if(o!==v)o.pause()}}));tick()}});
  v.addEventListener('pause',()=>{{if(!ours&&sync){{quiet(()=>vids.forEach(o=>o.pause()));stopSync()}}}});
  ['seeked','timeupdate','loadedmetadata'].forEach(e=>v.addEventListener(e,()=>{{look(v);if(v===master)tick()}}));
  v.addEventListener('ended',()=>{{if(sync)stopSync()}});
  v.closest('article').querySelectorAll('.strip img').forEach(img=>img.onclick=()=>{{v.currentTime=+img.dataset.t;look(v)}})}});
</script></body></html>"""
out = CUTS / OUT
out.write_text(page, encoding="utf-8")
print(out)
