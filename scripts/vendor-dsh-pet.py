"""Vendors the parts of dsh-pet the companion reuses: the self-playing animation
chain (src/client/pet/animation), dragging, throwing and walking (src/client/pet/motion),
the physics they share (src/shared/physics), and the plain modules those import.
Nothing that talks to DeepSeek Harness, a model, a voice or the network is copied;
the script stops if an import reaches outside the plain modules.

Run on the development machine, with a dsh-pet checkout at the pinned commit and its HEVC
animations for Safari (the assets-mov release, MOV_ZIP below):
    python3 scripts/vendor-dsh-pet.py <dsh-pet checkout> web/src/companion/dsh-pet <assets-mov zip>

Imports lose their .ts suffix (A-NAS does not import with extensions). Changes A-NAS
needs are patches in web/src/companion/dsh-pet-patches/, applied in name order.
The animations stay out of Git; animations.sha256 pins the ones the commit ships, which
install-companion-animations.sh installs (scripts/deploy-companion-animations.ps1 stages them).
"""
import hashlib
import json
import re
import shutil
import subprocess
import sys
import zipfile
from pathlib import Path

PINNED = "10f6c868b8110c897af33d6137a3d85e84b665ba"
# Safari draws no alpha in VP9; dsh-pet publishes the same animations as HEVC with alpha in one
# release asset, which GitHub digests. It lacks only the meal game's, which A-NAS does not play.
MOV_ZIP = ("https://github.com/PC2005-cloud/dsh-pet/releases/download/assets-mov/dsh-pet-mov-assets-mov.zip",
           "654e42f14b647a1e59a4c6f05694273dcf23edb063a5e49b14fc05a1fad3b467")
ROOTS = ["client/pet/animation", "client/pet/motion", "shared/physics"]
# Moves the pet where the Harness host tells it to; A-NAS has no such host.
SKIP = {"client/pet/motion/position-follow.ts"}
# Code reached through imports must stay in these plain, browser-only modules.
ALLOWED = ("client/pet/", "shared/")
DENIED = ("shared/chat/", "shared/balance/", "shared/todo/", "shared/menu/", "client/pet/card/")
IMPORT = re.compile(r"""((?:from|import)\s*\(?\s*)(['"])(\.{1,2}/[^'"]+)\2""")

upstream, target, mov_zip = Path(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3])
source = upstream / "dsh-pet" / "src"
patches = target.with_name("dsh-pet-patches")
commit = subprocess.run(["git", "-C", str(upstream), "rev-parse", "HEAD"],  # noqa: S607  a development tool, git from PATH
                        capture_output=True, text=True, check=True).stdout.strip()
if commit != PINNED:
    sys.exit(f"dsh-pet checkout is at {commit[:12]}, expected the pinned {PINNED[:12]}")
if hashlib.sha256(mov_zip.read_bytes()).hexdigest() != MOV_ZIP[1]:
    sys.exit(f"{mov_zip} is not the pinned {MOV_ZIP[0]}")


def imports(path: Path) -> list[Path]:
    return [(path.parent / m.group(3)).resolve() for m in IMPORT.finditer(path.read_text(encoding="utf-8"))]


todo = [f for root in ROOTS for f in sorted((source / root).glob("*.ts"))
        if not f.name.endswith(".test.ts") and f.relative_to(source).as_posix() not in SKIP]
files: set[Path] = set()
while todo:
    path = todo.pop().resolve()
    if path in files:
        continue
    rel = path.relative_to(source.resolve()).as_posix()
    if not rel.startswith(ALLOWED) or rel.startswith(DENIED):
        sys.exit(f"{rel} is outside the plain modules the companion may vendor")
    files.add(path)
    todo.extend(imports(path))

if target.exists():
    shutil.rmtree(target)
header = f"// Vendored from PC2005-cloud/dsh-pet {commit[:12]} (MIT, see LICENSE).\n"
for path in sorted(files):
    rel = path.relative_to(source.resolve())
    dest = target / rel
    dest.parent.mkdir(parents=True, exist_ok=True)
    text = IMPORT.sub(lambda m: m.group(1) + m.group(2) + re.sub(r"\.ts$", "", m.group(3)) + m.group(2),
                      path.read_text(encoding="utf-8"))
    dest.write_text(header + text, encoding="utf-8", newline="\n")
applied = []
for patch in sorted(patches.glob("*.patch")):
    subprocess.run(["git", "apply", "--directory", target.as_posix(), str(patch)],  # noqa: S607  our own patches
                   check=True, cwd=Path.cwd())
    applied.append(patch.name)
def jsonc(text: str) -> object:
    """Parses dsh-pet's config.jsonc: JSON with comments and trailing commas."""
    out, i, inside = [], 0, False
    while i < len(text):
        c = text[i]
        if inside:
            out.append(text[i:i + 2] if c == "\\" else c)
            i += 2 if c == "\\" else 1
            inside = c != '"'
        elif text.startswith("//", i):
            i = text.find("\n", i) if "\n" in text[i:] else len(text)
        elif text.startswith("/*", i):
            i = text.index("*/", i) + 2
        else:
            out.append(c)
            inside = c == '"'
            i += 1
    return json.loads(re.sub(r",(\s*[}\]])", r"\1", "".join(out)))


# The animations the companion plays and how often, from the commit's own configuration.
# Balance, chatter, sleep and the meal game are dsh-pet features A-NAS does not have.
config = jsonc((upstream / "dsh-pet" / "assets" / "config.jsonc").read_text(encoding="utf-8"))
animations = {key: value for key, value in config["animations"].items() if key not in ("sleep", "baifan")}
animations["events"] = {"workStatus": config["animations"]["events"]["workStatus"]}
(target / "animations.json").write_text(
    json.dumps({"animations": animations, "animationWeights": config["animationWeights"], "physics": config["physics"]},
               ensure_ascii=False, indent=2) + "\n", encoding="utf-8", newline="\n")

files_by_name = {path.name: path.read_bytes() for path in (upstream / "dsh-pet" / "assets" / "webm").glob("*.webm")}
with zipfile.ZipFile(mov_zip) as archive:
    files_by_name |= {entry.filename: archive.read(entry) for entry in archive.infolist() if not entry.is_dir()}
lines = []
for name in sorted(files_by_name):
    if not re.fullmatch(r"[^./\\\x00-\x1f\x7f][^/\\\x00-\x1f\x7f]*\.(webm|mov)", name) or name != name.strip():
        sys.exit(f"unexpected animation file name: {name!r}")
    lines.append(f"{hashlib.sha256(files_by_name[name]).hexdigest()}  {name}\n")
manifest = "".join(lines).encode()
(target / "animations.sha256").write_bytes(manifest)
pool = hashlib.sha256(manifest).hexdigest()[:16]
shutil.copyfile(upstream / "LICENSE", target / "LICENSE")
(target / "README.md").write_text(
    "# Vendored dsh-pet animation and motion\n\n"
    f"Copied from [PC2005-cloud/dsh-pet](https://github.com/PC2005-cloud/dsh-pet) commit `{commit}` "
    "by `scripts/vendor-dsh-pet.py`. The code is MIT, see `LICENSE`. Do not edit the copied files by hand: "
    "add a patch to `../dsh-pet-patches/` or change the script, and vendor again.\n\n"
    "The animations this code plays are not in Git and are not MIT. The character is the community-made "
    "DeepSeek 大肥鱼, not an official DeepSeek character: the original 溟月 is by "
    "[@上善无形](https://space.bilibili.com/4456176) and the maid design by "
    "[@ZipZipPipe](https://space.bilibili.com/4168597), licensed "
    "[CC BY-NC-SA 4.0](https://creativecommons.org/licenses/by-nc-sa/4.0/) with no commercial use in any form; "
    "dsh-pet's animations, prompts and source videos may not be used commercially. Anything showing the "
    "character credits <https://github.com/PC2005-cloud/dsh-pet>, and A-NAS must replace it before any "
    "commercial release.\n\n"
    "Patches applied: " + (", ".join(f"`{name}`" for name in applied) or "none") + ".\n\n"
    f"`animations.sha256` lists the {len(lines)} animation files of this commit and of `{MOV_ZIP[0]}`; "
    f"installed, they are companion animation pool `{pool}`.\n",
    encoding="utf-8", newline="\n")
print(f"vendored {len(files)} files from {commit[:12]} with {len(applied)} patches; animation pool {pool}")
