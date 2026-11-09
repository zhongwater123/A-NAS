"""过程卡片.html: 侦探片 and 上海 cards described again with the process field (proc-<case>.json),
and how long the step-1 planning prompt of 剪法25 gets when each catalog line carries the process
instead of the summary (tokens counted by the 4B's own /tokenize).

  python3 proc_page.py URL"""
import html
import os
import sys

import cut
import requests
from paths import CARDS as HERE, read_json

esc = html.escape
NAMES = {"01": "侦探片", "02": "上海vlog"}


def prompt(case, text_of, what):
    """The step-1 user prompt as cut.pick builds it, each card's text from text_of(card)."""
    _key, instruction, target, style, _name = cut.CASES[case]
    cards = cut.usable_cards(case)
    many = len({c["video"] for c in cards}) > 1
    lines = []
    for c in cards:
        where = f"视频{int(c['video'])} " if many else ""
        lines.append(f"{c['id']} {where}{cut.clock(c['start'])} {c['end'] - c['start']:.0f}秒 | {c['answer'].get('scene', '')} | {text_of(c)}")
    return cut.PICK.format(instruction=instruction, total_min=sum(c["end"] - c["start"] for c in cards) / 60, n=len(cards),
                           where="所在视频 " if many else "", catalog="\n".join(lines), what=what,
                           what_note="（这张卡片里依次发生的事）" if what == "过程" else "", structure=cut.structure_of(style),
                           target=target, most=round(target / cut.PER_CARD), max_parts=max(2, min(8, int(target // 10))),
                           min_parts=cut.fewest_parts(target),
                           parts_rule=f"{cut.fewest_parts(target)}–{max(2, min(8, int(target // 10)))} 个部分"
                                      f"（至少 {cut.fewest_parts(target)} 个，不能超过 {max(2, min(8, int(target // 10)))} 个）",
                           pictures_rule=""), lines


def tokens(url, text):
    return len(requests.post(f"{url}/tokenize", json={"content": text}, timeout=60).json()["tokens"])


def main(url):
    tabs, sections, rows = [], [], []
    for case, name in NAMES.items():
        if not (HERE / f"proc-{case}.json").exists():
            continue
        old = {c["id"]: c for c in read_json(HERE / f"cards-{case}.json").values()}
        new = read_json(HERE / f"proc-{case}.json")
        def flow(c, new=new):
            return " → ".join(new[c["id"]]["answer"]["process"]) if "answer" in new.get(c["id"], {}) else c["answer"]["summary"]

        before, old_lines = prompt(case, lambda c: c["answer"]["summary"], "概括")
        after, new_lines = prompt(case, flow, "过程")
        tb, ta = tokens(url, cut.PICK_SYSTEM + before), tokens(url, cut.PICK_SYSTEM + after)
        steps = [len(e["answer"]["process"]) for e in new.values() if "answer" in e]
        beats = [len(e["answer"]["beats"]) for e in new.values() if "answer" in e]
        failed = [cid for cid, e in new.items() if "answer" not in e]
        rows.append(f"<tr><td>{name}</td><td>{len(old_lines)}</td><td>{len(before)} 字 / {tb}</td><td>{len(after)} 字 / {ta}</td>"
                    f"<td>{ta - tb:+d}（{(ta - tb) / tb:+.0%}）</td><td>{sum(steps) / len(steps):.1f}（节拍 {sum(beats) / len(beats):.1f}）</td>"
                    f"<td>{len(failed)}</td></tr>")
        cards = []
        for c in cut.usable_cards(case):
            cid, e = c["id"], new.get(c["id"], {})
            if "answer" not in e:
                cards.append(f"<article><h3>{cid}</h3><p class=bad>{esc(e.get('error', '没有结果'))}</p></article>")
                continue
            a = e["answer"]
            hl = a.get("highlights", [])
            strip = "".join(f"<figure><img src='{esc(os.path.relpath(f, HERE))}' loading=lazy><figcaption>{k}. {'★' if k in hl else ''}{esc(b['what'])}</figcaption></figure>"
                            for k, (f, b) in enumerate(zip(e["frames"], a["beats"], strict=True), 1))
            cards.append(f"<article id='{case}-{cid}'><h3>{cid}<span class=dim>　{cut.clock(c['start'])}，{c['end'] - c['start']:.0f} 秒，"
                         f"{len(a['beats'])} 个关键帧 → {len(a['process'])} 个短语</span></h3><div class=strip>{strip}</div>"
                         f"<table><tr><th>过程</th><td class=flow>{' → '.join(esc(s) for s in a['process'])}</td></tr>"
                         f"<tr><th>新概括</th><td>{esc(a['summary'])}</td></tr><tr><th>原概括</th><td class=dim>{esc(old[cid]['answer']['summary'])}</td></tr></table></article>")
        tabs.append(f"<button data-case='{case}'>{name}</button>")
        sections.append(f"<section id='c{case}'><h2>{name}</h2>"
                        f"<details><summary>第一步目录：原来（概括）</summary><pre>{esc(chr(10).join(old_lines))}</pre></details>"
                        f"<details open><summary>第一步目录：改成过程</summary><pre>{esc(chr(10).join(new_lines))}</pre></details>"
                        f"{''.join(cards)}</section>")
    page = f"""<!doctype html><html lang=zh><head><meta charset=utf-8><meta name=viewport content='width=device-width,initial-scale=1'>
<title>过程卡片</title><style>
:root{{--bg:#f6f5f2;--card:#fff;--ink:#222;--dim:#777;--line:#ddd;--acc:#a0522d;--bad:#c0392b}}
@media (prefers-color-scheme:dark){{:root{{--bg:#1b1b1d;--card:#252528;--ink:#e8e8e8;--dim:#9a9a9a;--line:#3a3a3e;--acc:#e0a070;--bad:#ff7b6b}}}}
body{{background:var(--bg);color:var(--ink);font:14px/1.6 -apple-system,'PingFang SC',sans-serif;margin:0;padding:20px 16px}}
main{{max-width:1150px;margin:0 auto}} .dim{{color:var(--dim);font-size:12px;font-weight:400}} .bad{{color:var(--bad)}}
.wrap{{overflow-x:auto}} table{{border-collapse:collapse;width:100%;font-size:13px;background:var(--card)}}
th,td{{border-bottom:1px solid var(--line);padding:4px 8px;text-align:left;vertical-align:top}} th{{white-space:nowrap;color:var(--acc)}}
article{{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:10px 14px;margin:14px 0;scroll-margin-top:10px}}
h3{{margin:0 0 6px;font-size:16px}} .strip{{display:flex;gap:6px;overflow-x:auto;padding-bottom:4px}}
figure{{margin:0;flex:none;width:140px;font-size:11px;color:var(--dim)}} figure img{{width:140px;border-radius:4px;display:block}}
pre{{white-space:pre-wrap;font-size:12px;background:var(--card);border:1px solid var(--line);border-radius:6px;padding:8px}}
.tabs button{{font:inherit;padding:5px 14px;margin-right:6px;border:1px solid var(--line);background:var(--card);color:var(--ink);border-radius:6px;cursor:pointer}}
.tabs button.on{{border-color:var(--acc);color:var(--acc)}} section{{display:none}} section.on{{display:block}}
</style></head><body><main>
<h1>卡片描述加“过程”：侦探片和上海</h1>
<p class=dim>同样的关键帧，Qwen3.5-4B 按新提示词重新描述一遍：逐帧写节拍 → 挑亮点 → 写过程（短语个数不超过节拍数，每个不超过 10 字）→ 写概括。
第一步规划的目录每行原来是“卡片 id 时间 时长 | 场景 | 概括”，改成把概括换成过程。下表是第一步整份输入（系统提示 + 用户提示）的长度，token 用 4B 自己的分词器数。报幕卡片和片头预告不进目录。<br>上海这次没有重跑，按侦探片的比例估算（每个关键帧 0.72 个短语、每个短语 8.8 字）：目录从约 3630 字的概括变成约 2200 字的过程，第一步输入从 4598 token 降到约 3800 token，少约 18%。上海的概括本来就长（旅行片允许 80 字），短卡片的过程只有两三个短语。</p>
<div class=wrap><table><tr><th>视频</th><th>目录行数</th><th>原来（概括）</th><th>改成过程</th><th>多出 token</th><th>平均短语数</th><th>失败</th></tr>{''.join(rows)}</table></div>
<p class=tabs>{''.join(tabs)}</p>{''.join(sections)}
<script>const bs=[...document.querySelectorAll('.tabs button')];
function show(c){{bs.forEach(b=>b.classList.toggle('on',b.dataset.case===c));document.querySelectorAll('section').forEach(s=>s.classList.toggle('on',s.id==='c'+c))}}
bs.forEach(b=>b.onclick=()=>show(b.dataset.case));show('01');</script>
</main></body></html>"""
    (HERE / "过程卡片.html").write_text(page, encoding="utf-8")
    print(HERE / "过程卡片.html")


if __name__ == "__main__":
    main(sys.argv[1])
