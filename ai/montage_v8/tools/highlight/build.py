"""Build the beat labelling page's site: every beat of 品杰's usable cards, for the user to mark how striking it is.

The beats are those step two chooses from (the catalog, montage_v8.tools.usable_cards, ids "<card>/<beat>" as in the shots files), in
shooting order, each with its key frame (upright as ffmpeg wrote it), its range in the source video and the
4B description. The 4B highlight score and its highlight marks are left out, so they do not lead the marks.

The site is a folder outside the repository (SITE, ANAS_MONTAGE_HIGHLIGHT): data.js is written there, and
links there point at this folder's page, the key frames and the source videos. The marks are kept in the
same folder (labels-pj.json) by server.py.

  python3 -m montage_v8.tools.highlight.build
"""
import json
import os
from pathlib import Path

from PIL import Image

from montage_v8.tools import CARDS, ROOT, VIDEOS, rotation, usable_cards

HERE = Path(__file__).resolve().parent

SITE = Path(os.environ.get("ANAS_MONTAGE_HIGHLIGHT", ROOT / "剪辑对比" / "精彩度标注（2026-11-13）"))
CASE = "pj"
SOURCES = VIDEOS / "phone-newzealand-2026-09"
FRAMES = CARDS / "frames" / CASE


def link(name, target):
    path = SITE / name
    if path.is_symlink() or path.exists():
        path.unlink()
    path.symlink_to(target)


def cards():
    """In shooting order (the dolphins' card, put back on review, comes last in the cards file)."""
    out = []
    flipped = rotation(CASE)
    for card in sorted(usable_cards(CASE), key=lambda c: (c["video"], c["start"])):
        answer = card["answer"]
        width, height = Image.open(card["frames"][0]).size
        source = next(SOURCES.glob(f"{card['video']}.*"))           # most are .mov, a few .mp4
        beats = []
        for n, beat in enumerate(answer["beats"], 1):
            frame = Path(card["frames"][beat["frames"][0] - 1])
            beats.append({"id": f"{card['id']}/{n}", "size": beat["shot_size"], "what": beat["what"],
                          "start": round(beat["start"], 2), "end": round(beat["end"], 2),
                          "frame": "frames/" + str(frame.relative_to(FRAMES))})
        out.append({"id": card["id"], "file": f"sources/{source.name}", "start": round(card["start"], 2),
                    "end": round(card["end"], 2), "upright": height > width,
                    # Tagged upside down over upright pixels: the browser plays it upside down, the page turns it back.
                    "flip": source.name in flipped, "scene": answer.get("scene", ""),
                    "process": answer.get("process", []), "beats": beats})
    return out


def main():
    SITE.mkdir(parents=True, exist_ok=True)
    link("index.html", HERE / "index.html")
    link("frames", FRAMES)
    link("sources", SOURCES)
    data = {"case": CASE, "footage": "新西兰（品杰手机）", "cards": cards()}
    text = "window.LABEL = " + json.dumps(data, ensure_ascii=False, separators=(",", ":")) + ";\n"
    (SITE / "data.js").write_text(text, encoding="utf-8")
    print(f"{len(data['cards'])} cards, {sum(len(c['beats']) for c in data['cards'])} beats, "
          f"{len(text) // 1024} KiB -> {SITE}")


if __name__ == "__main__":
    main()
