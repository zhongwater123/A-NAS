"""Builds the companion's sprite strips from a checkout of QCYTSN/dsh-dafeiyu.

Usage: python -I scripts/import-companion-art.py <dsh-dafeiyu checkout> <its commit>

The checkout's assets/pet frames are the whale-tail maid set converted from
PC2005-cloud/dsh-pet. dsh-dafeiyu calls them MIT, but dsh-pet's own README
licenses only its code as MIT: the art is non-commercial with attribution, and
the character is CC BY-NC-SA 4.0 (LICENSE_TERMS below; NOTICE.md). Nothing in
the checkout is run; only the manifest and frames are read. Every pose becomes
one horizontal WebP strip on a shared crop window, so switching poses never
moves the character. Needs Pillow. Writes web/src/companion/assets/dafeiyu-*.webp
and dafeiyu.source.json.
"""

import hashlib
import json
import sys
from pathlib import Path

from PIL import Image

ROOT = Path(__file__).resolve().parent.parent
OUT = ROOT / "web/src/companion/assets"

# The "## 许可" section of dsh-pet's README at the commit the frames come from,
# read by hand on 2026-11-05; its current README adds the character licence.
LICENSE_TERMS = {
    "readme": "https://github.com/PC2005-cloud/dsh-pet/blob/549fff2ea33c2acfb16612c76912c6137da4a758/README.md",
    "readmeSHA256": "adb591635dbb1d5bba9910a4379b7e0ee04c02b385af8c51ccbea50b5f0a7b2a",
    "code": "MIT",
    "art": "允许开源使用，禁止商用；衍生、改版、换皮作品在任何介绍、展示、分发处须附 https://github.com/PC2005-cloud/dsh-pet",
    "character": "社区二创 DeepSeek「大肥鱼」，非官方形象；dsh-pet 现行 README 标注 CC BY-NC-SA 4.0，不可以任何形式商业化",
}

# One window for every pose (x0, y0, x1, y1 on the 412x344 frames).
CROP = (56, 6, 376, 336)
SIZE = (224, 231)
QUALITY = 72
# The green-screen matte leaves alpha 1-8 black over most of the background,
# which shows as a grey box on light pages. Such pixels become transparent.
MATTE = 8
# PetAction -> (upstream clip, first frame, end frame, stride, seconds, loop).
# Loops keep the whole clip: it starts and ends at rest. One-shot poses skip
# the upstream lead-in so the action shows within the stage's reaction time.
POSES = {
    "idle": ("idle", 0, 240, 4, 10, True),
    "thinking": ("thinking", 0, 240, 4, 10, True),
    "working": ("working", 0, 240, 4, 10, True),
    "waiting": ("waiting", 0, 240, 4, 10, True),
    "happy": ("success", 48, 216, 4, 7, False),
    "concerned": ("error", 0, 118, 4, 5, False),
    "wave": ("poke", 60, 180, 5, 2, False),
}


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main(source, commit):
    manifest_path = source / "assets/pet-manifest.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    if manifest.get("characterId") != "whaletail-maid":
        sys.exit(f"{manifest_path}: expected the whale-tail maid set, found {manifest.get('characterId')!r}")
    poses = {}
    for pose, (name, first, end, stride, seconds, loop) in POSES.items():
        clip = manifest["clips"][name]
        frames = clip["frames"][first:end:stride]
        strip = Image.new("RGBA", (SIZE[0] * len(frames), SIZE[1]))
        for index, frame in enumerate(frames):
            image = Image.open(source / "assets/pet" / frame).convert("RGBA")
            image.putalpha(image.getchannel("A").point(lambda v: 0 if v <= MATTE else v))
            box = image.getchannel("A").getbbox()
            if box and not (box[0] >= CROP[0] and box[1] >= CROP[1] and box[2] <= CROP[2] and box[3] <= CROP[3]):
                sys.exit(f"{frame}: the character at {box} leaves the crop window {CROP}")
            strip.paste(image.crop(CROP).resize(SIZE, Image.Resampling.LANCZOS), (index * SIZE[0], 0))
        target = OUT / f"dafeiyu-{pose}.webp"
        strip.save(target, "WEBP", quality=QUALITY, method=6)
        poses[pose] = {
            "clip": name,
            "frames": len(frames),
            "seconds": seconds,
            "loop": loop,
            "sourceFrames": [first, end, stride],
            "sourceSHA256": clip["source"]["sha256"],
            "bytes": target.stat().st_size,
            "sha256": sha256(target),
        }
        print(f"{target.name}: {len(frames)} frames, {target.stat().st_size // 1024} KiB")
    record = {
        "repository": "https://github.com/QCYTSN/dsh-dafeiyu",
        "commit": commit,
        "manifestSHA256": sha256(manifest_path),
        "assetSource": manifest["assetSource"],
        "crop": list(CROP),
        "frameSize": list(SIZE),
        "webpQuality": QUALITY,
        "matteAlphaCutoff": MATTE,
        "poses": poses,
        "license": LICENSE_TERMS,
        "scope": "Non-commercial experiment only. Not MIT: dsh-pet's art is non-commercial with attribution and the character is CC BY-NC-SA 4.0. Do not ship in a commercial product without the rights holders' permission.",
    }
    (OUT / "dafeiyu.source.json").write_text(
        json.dumps(record, indent=2, ensure_ascii=False) + "\n", encoding="utf-8", newline="\n"
    )


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    main(Path(sys.argv[1]), sys.argv[2])
