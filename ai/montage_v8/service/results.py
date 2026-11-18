"""把剪好的版本整理成网页（web/index.html）挑选页和精修页读的数据，和第七版 data.js 里的 films、cards、song 同一个形状。

文件经网页服务的 /media/ 路径给浏览器：关键帧在 /media/cards/，成片在 /media/cuts/，原片在 /media/videos/，
歌在 /media/songs/<歌名>。
"""

import os
import re
from pathlib import Path
from urllib.parse import quote

from ..adapters.store import read_json

LEVELS = {"安静": "quiet", "中等": "mid", "高潮": "peak"}     # 和第七版网页的段落颜色对应

SENTENCE = re.compile(r"^(.+?[。！？])")


def url(prefix: str, path, root) -> str:
    return f"media/{prefix}/" + quote(os.path.relpath(str(path), str(root)).replace(os.sep, "/"))


def plain(text: str) -> str:
    """去掉规划里引用的卡片编号：（017→001）、(pj-009)、ne-087：。"""
    text = re.sub(r"[（(](?:[a-z]{2}-)?\d{3}(?:\s*[→/、,，]\s*(?:[a-z]{2}-)?\d{3})*[）)]", "", text)
    return re.sub(r"[a-z]{2}-\d{3}[：:]?", "", text).strip()


def card_entries(store, case, cards: list[dict]) -> dict:
    ix = store.windows(case.index)
    out = {}
    for card in cards:
        answer = card["answer"]
        beats = []
        for n, beat in enumerate(answer["beats"], 1):
            first = beat["frames"][0] - 1
            beats.append({"n": n, "size": beat["shot_size"], "what": beat["what"], "start": round(beat["start"], 2),
                          "end": round(beat["end"], 2), "key": round(card["times"][first], 2),
                          "frame": url("cards", card["frames"][first], store.cards)})
        out[card["id"]] = {"id": card["id"], "video": card["video"], "file": url("videos", store.source(ix, card["video"]), store.videos),
                           "start": round(card["start"], 2), "end": round(card["end"], 2), "upright": store.upright(card),
                           "scene": answer.get("scene", ""), "summary": answer.get("summary", ""),
                           "process": answer.get("process", []), "tags": [], "mood": "", "beats": beats}
    return out


def film_entry(store, key: str, tag: str) -> dict:
    cut = read_json(store.cut_file("cut", key, tag))
    pick = read_json(store.cut_file("pick", key, tag))
    answer = pick["answer"]
    design = plain(answer["design"])
    first = SENTENCE.match(design)
    slots = [{"slot": c["slot"], "part": c["part"], "card": c["card"], "beat": c["beats"][0], "start": round(c["start"], 2),
              "end": round(c["end"], 2), "query": c["query"], "shows": c["shows"], "link": c["link"]} for c in cut["clips"]]
    return {"tag": tag, "file": url("cuts", cut["cut"], store.cuts), "seconds": cut["seconds"],
            "pitch": first.group(1) if first else design[:60], "design": design, "opening": plain(answer["opening"]),
            "ending": plain(answer["ending"]), "parts": [{"name": p["name"], "summary": p["summary"]} for p in answer["parts"]],
            "slots": slots, "cost": round(sum((read_json(store.cut_file(s, key, tag))["cost"].get("usd") or 0)
                                              for s in ("pick", "shots")), 2)}


def song_entry(m: dict, stem: str) -> dict:
    artist, _, title = Path(m["song"]).stem.partition("-")
    return {"id": stem, "title": title or artist, "artist": artist.replace(",", " / ") if title else "",
            "file": "media/songs/" + quote(stem), "excerpt": m["excerpt"], "bar": m["bar"],
            "parts": [{"start": p["start"], "end": p["end"], "label": p["label"], "level": LEVELS.get(p["level"], "mid")}
                      for p in m["parts"]]}


def build(montage, case, request, tags: list[str]) -> dict:
    """任务完成后的结果：每版成片、这次目录里全部卡片（精修页要换镜头）、歌。"""
    store = montage.store
    m, _ = montage.music(case, request.song)
    return {"case": case.key, "films": [film_entry(store, case.key, t) for t in tags],
            "cards": card_entries(store, case, montage.usable(case, request.options(""))), "song": song_entry(m, request.song)}
