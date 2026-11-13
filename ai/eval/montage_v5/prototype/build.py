"""Build the editing prototype's site (第七版) from the real 第六版 records of 品杰's footage.

Three films of the same footage and song (pj × Symphony, place-1/3/4): each music slot keeps the card,
beat and key frame the planner chose. Every described card goes in, with its beats, so the page can
offer a card's other beats and search the rest.

The site is a folder outside the repository (SITE, ANAS_MONTAGE_PROTO): data.js and beats.npz are written
there, and links there point at this folder's page and vendor code, the key frames, the rendered films, the
songs and the source videos; nothing is copied. hero.mp4 and hero.jpg, the home page's background, are cut
by hand into the same folder (docs/research/video-montage-v7.md).

beats.npz is for search.py: the EmbeddingGemma 2 vectors of the video index's 2 s windows that each
beat covers (flat, single-colour windows left out), so a text query can be scored against every beat.

  python3 build.py
"""
import json
import os
import re
import subprocess
import sys
from datetime import datetime, timedelta
from pathlib import Path

import numpy as np
from PIL import Image

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))  # montage_v5
from paths import CARDS as CARD_DIR, CUTS, INDEXES, ROOT, VIDEOS  # noqa: E402

SITE = Path(os.environ.get("ANAS_MONTAGE_PROTO", ROOT / "剪辑对比" / "剪辑原型（2026-11-11）"))
CARDS = CARD_DIR / "cards-pj.json"
SOURCES = VIDEOS / "phone-newzealand-2026-09"
INDEX = next((INDEXES / "video-moments-pinjie").glob("embeddinggemma*"))
NAS_FRAMES = CARD_DIR / "frames"
FILMS = ("place-1", "place-3", "place-4")
# One sentence per film for the choose page: written here by hand from each plan's design, standing in for a
# field the planner would be asked for (the design runs to a paragraph and names cards).
PITCH = {
    "place-1": "跟着人走：从出发到湖边小镇、峡湾、小飞机，高潮飞越雪山，在平静的海面收尾。",
    "place-3": "先亮雪山大景再带出人物，高潮是航拍雪山云海，最后落在沿路走远的背影上。",
    "place-4": "中段多了森林登顶，高潮加入峡湾和鲸群，结尾一路驶离，落在走远的背影上。",
}
LEVELS = {"安静": "quiet", "中等": "mid", "高潮": "peak"}


def read(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def link(name, target):
    path = SITE / name
    if path.is_symlink() or path.exists():
        path.unlink()
    path.symlink_to(target)


def frame_url(path):
    return "frames/" + str(Path(path).relative_to(NAS_FRAMES / "pj"))


def cards():
    out = {}
    for cid, card in read(CARDS).items():
        answer = card.get("answer")
        if not answer:
            continue
        width, height = Image.open(card["frames"][0]).size
        beats = []
        for n, beat in enumerate(answer["beats"], 1):
            first = beat["frames"][0] - 1
            beats.append({"n": n, "size": beat["shot_size"], "what": beat["what"],
                          "start": round(beat["start"], 2), "end": round(beat["end"], 2),
                          "key": round(card["times"][first], 2), "frame": frame_url(card["frames"][first])})
        source = next(SOURCES.glob(f"{card['video']}.*"))           # most are .mov, a few .mp4
        out[cid] = {"id": cid, "video": card["video"], "file": f"sources/{source.name}", "start": round(card["start"], 2), "end": round(card["end"], 2),
                    "upright": height > width, "scene": answer.get("scene", ""), "summary": answer.get("summary", ""),
                    "process": answer.get("process", []), "tags": answer.get("tags", []),
                    "mood": answer.get("mood", ""), "beats": beats}
    return out


def plain(text):
    """The planner's prose without the card numbers it cites: (017→001), (057/058/079), (pj-009), pj-087："""
    text = re.sub(r"[（(](?:pj-)?\d{3}(?:\s*[→/、,，]\s*(?:pj-)?\d{3})*[）)]", "", text)
    return re.sub(r"pj-\d{3}[：:]?", "", text).strip()


def film(tag, index):
    cut = read(CUTS / f"cut-pj-symphony-{tag}.json")
    pick = read(CUTS / f"pick-pj-symphony-{tag}.json")["answer"]
    link(f"film-{index + 1}.mp4", cut["cut"])
    slots = [{"slot": clip["slot"], "part": clip["part"], "card": clip["card"], "beat": clip["beats"][0],
              "start": round(clip["start"], 2), "end": round(clip["end"], 2),
              "query": clip["query"], "shows": clip["shows"], "link": clip["link"]} for clip in cut["clips"]]
    return {"tag": tag, "file": f"film-{index + 1}.mp4", "seconds": cut["seconds"], "pitch": PITCH[tag], "design": plain(pick["design"]),
            "opening": plain(pick["opening"]), "ending": plain(pick["ending"]),
            "parts": [{"name": p["name"], "summary": p["summary"]} for p in pick["parts"]], "slots": slots}


def beat_vectors(cards):
    """Every beat's windows: those of its video overlapping the beat, or the nearest one when none does."""
    index = read(INDEX / "index.json")
    with np.load(INDEX / "frames.npz") as data:
        vectors = dict(zip(data["keys"].tolist(), data["vectors"], strict=True))
    keys, owner, rows = [], [], []
    for card in cards.values():
        windows = [w for w in index["videos"][card["video"]]["windows"] if not w["flat"]]
        for beat in card["beats"]:
            hit = [w for w in windows if w["start"] < beat["end"] and w["end"] > beat["start"]]
            hit = hit or [min(windows, key=lambda w, b=beat: abs((w["start"] + w["end"]) / 2 - b["key"]))]
            for w in hit:
                owner.append(len(keys))
                rows.append(vectors[f"{card['video']}/{w['frame']}"])
            keys.append(f"{card['id']}/{beat['n']}")
    np.savez(SITE / "beats.npz", keys=np.array(keys), owner=np.array(owner, dtype=np.int32),
             vectors=np.stack(rows).astype(np.float32), model=np.array(index["model"]))
    return len(keys), len(rows)


FOLDERS = (                                   # newest trip first, as a NAS lists them
    ("ne", "新西兰 · DJI", "dji-newzealand-2026-10", "video-moments-newzealand"),
    ("pj", "新西兰 · 品杰手机", "phone-newzealand-2026-09", "video-moments-pinjie"),
    ("ch", "川西 · DJI", "dji-chuanxi-2026-08", "video-moments-chuanxi"),
)


def shot_at(path):
    """Local shooting time: DJI names carry it, phone videos keep it in the QuickTime tags; None when neither has it."""
    stem = path.stem
    if stem.startswith("DJI_"):
        t = stem.split("_")[1]
        return f"{t[:4]}-{t[4:6]}-{t[6:8]}T{t[8:10]}:{t[10:12]}"
    tags = json.loads(subprocess.run(["ffprobe", "-v", "error", "-show_entries", "format_tags", "-of", "json", str(path)],  # noqa: S607
                                     capture_output=True, text=True).stdout).get("format", {}).get("tags", {})
    if "com.apple.quicktime.creationdate" in tags:
        return tags["com.apple.quicktime.creationdate"][:16]
    if "creation_time" in tags:                    # UTC; the phone folder was shot in New Zealand, UTC+13 then
        utc = datetime.fromisoformat(tags["creation_time"].replace("Z", "+00:00"))
        return (utc + timedelta(hours=13)).strftime("%Y-%m-%dT%H:%M")
    return None


def folders():
    """The footage already on the NAS, one entry per folder, each video in shooting order with its length,
    shooting time and a middle key frame for its thumbnail, so the upload page can list them to tick."""
    out = []
    for fid, name, folder, cache in FOLDERS:
        index = read(next((INDEXES / cache).glob("embeddinggemma*")) / "index.json")["videos"]
        videos = []
        for key, entry in sorted(index.items()):
            path = VIDEOS / entry["file"]
            shots = sorted((NAS_FRAMES / fid / key).glob("*.jpg"))
            thumb = shots[len(shots) // 2]
            width, height = Image.open(thumb).size
            stem = path.stem
            videos.append({"id": stem if fid == "pj" else key, "name": path.name, "seconds": round(entry["duration"], 1),
                           "at": shot_at(path), "upright": height > width,
                           "thumb": "nas-frames/" + str(thumb.relative_to(NAS_FRAMES))})
        out.append({"id": fid, "name": name, "path": f"/视频/{folder}", "videos": videos})
    return out


def library():
    """The songs whose structure has been analysed: the only ones a film can be cut to, so the only ones found."""
    (SITE / "songs").mkdir(exist_ok=True)
    songs = []
    for path in sorted(CUTS.glob("music-*.json")):
        if "." in path.stem:                       # music-X.旧切点.json and the like: older takes of the same song
            continue
        music = read(path)
        source = Path(music["song"])
        artist, title = source.stem.split("-", 1)
        slug = f"{len(songs) + 1}{source.suffix}"
        link(f"songs/{slug}", source)
        chorus = next(p for p in music["parts"] if p["level"] == "高潮")
        songs.append({"id": path.stem.removeprefix("music-"), "title": title, "artist": artist.replace(",", " / "),
                      "file": f"songs/{slug}", "tempo": music["tempo"], "seconds": music["seconds"],
                      "chorus": round(music["excerpt"][0] + chorus["start"], 1),
                      "shots": sum(len(p["slots"]) for p in music["parts"])})
    return songs


def main():
    music = read(CUTS / "music-Forester-Symphony.json")
    SITE.mkdir(parents=True, exist_ok=True)
    link("index.html", HERE / "index.html")
    link("vendor", HERE / "vendor")
    link("frames", NAS_FRAMES / "pj")
    link("sources", SOURCES)
    link("song.mp3", music["song"])
    link("nas-frames", NAS_FRAMES)
    nas = folders()
    data = {"footage": {"name": "新西兰（品杰手机）"}, "folders": nas,
            "song": {"title": "Symphony", "artist": "Forester", "file": "song.mp3", "excerpt": music["excerpt"],
                     "bar": music["bar"],
                     "parts": [{"start": p["start"], "end": p["end"], "label": p["label"],
                                "level": LEVELS.get(p["level"], "mid")} for p in music["parts"]]},
            "library": library(),
            "films": [film(tag, i) for i, tag in enumerate(FILMS)], "cards": cards()}
    text = "window.PROTO = " + json.dumps(data, ensure_ascii=False, separators=(",", ":")) + ";\n"
    (SITE / "data.js").write_text(text, encoding="utf-8")
    beats, windows = beat_vectors(data["cards"])
    print(f"{len(data['cards'])} cards, {len(data['films'])} films, {len(text) // 1024} KiB; "
          f"{beats} beats over {windows} windows")


if __name__ == "__main__":
    main()
