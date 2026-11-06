"""Vendors the parts of dsh-pet the companion reuses: the pet card (src/client/pet/card,
createPetCard and the runtime that wires it), the self-playing animation chain
(src/client/pet/animation), dragging, throwing, walking and following a position
(src/client/pet/motion), the physics they share (src/shared/physics), the host's work status
store, the bridge that publishes it and the page state it writes (FILES), and the plain
modules those import. Nothing that talks to DeepSeek Harness, a model, a voice or the network
is copied: what the card reaches of the balance, the menu, todos, settings, chat and voice is
cut by patches (CUT_IMPORTS). The script stops if an import reaches outside the plain modules,
or if a vendored file imports a package at run time (type-only imports, which the build
erases, are fine).

Run on the development machine, with a dsh-pet checkout at the pinned commit and its HEVC
animations for Safari (the assets-mov release, MOV_ZIP below):
    python3 scripts/vendor-dsh-pet.py <dsh-pet checkout> web/src/companion/dsh-pet <assets-mov zip>

Imports lose their .ts suffix (A-NAS does not import with extensions). Changes A-NAS
needs are patches in web/src/companion/dsh-pet-patches/, applied in name order.
The animations stay out of Git; animations.sha256 pins the ones the commit ships, which
install-media-pool.sh companion installs (scripts/deploy-media-pool.ps1 -Kind companion stages them).
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
ROOTS = ["client/pet/card", "client/pet/animation", "client/pet/motion", "shared/physics"]
# Single host modules the page runs as dsh-pet's host would: WorkStatusStore and pickDisplayed
# (self-contained), the bridge that publishes them (a working->result flip within 5 s merged,
# nothing published when the shown status is unchanged) and PollStateStore, the GET /state
# store the card reads. And the card's styles, which dsh-pet's page entry injects, not the card.
FILES = ["host/work-status.ts", "host/plugin/work-status-bridge.ts", "host/state.ts", "client/pet/css.ts"]
# The card's right-click menu, chat dialog, hotkeys and the chat handles they register: A-NAS
# has none of them (the patched runtime keeps the hover tooltip the menu entry also placed).
SKIP = {"client/pet/card/entry.ts", "client/pet/card/open-chat.ts", "client/pet/card/registry.ts",
        "client/pet/card/shortcut.ts"}
# Code reached through imports must stay in these plain, browser-only modules: the card's own
# directory and the shared modules, plus the three page modules it uses (the speech bubble, and
# the pose and progress readers it registers).
ALLOWED = ("client/pet/", "shared/", "client/bubble.ts", "client/pose.ts", "client/progress.ts", *FILES)
# DeepSeek Harness features the card can reach but A-NAS does not have: the balance, the menu
# and what its actions open (todos, settings, chat), and voice, which runs a TTS service.
DENIED = ("shared/chat/", "shared/balance/", "shared/todo/", "shared/menu/", "shared/menu.ts", "shared/voice.ts",
          "client/pet/menu-actions.ts", *SKIP)
IMPORT = re.compile(r"""((?:from|import)\s*\(?\s*)(['"])(\.{1,2}/[^'"]+)\2""")
# Imports a patch removes, by vendored file: not followed when collecting files, and checked to
# be gone once the patches are applied (a patch that stopped applying would leave them).
CUT_IMPORTS = {
    "host/plugin/work-status-bridge.ts": {"./setup.ts"},
    # 0004: the balance.
    "client/bubble.ts": {"../shared/balance/index.ts"},
    "shared/state.ts": {"./balance/index.ts"},
    # 0004 and 0005: the balance and voice.
    "client/pet/card/cue-effects.ts": {"../../../shared/balance/index.ts", "../../../shared/voice.ts"},
    "client/pet/card/bubble-slots.ts": {"../../../shared/balance/index.ts", "../../../shared/voice.ts"},
    # 0004 and 0006: the balance and the menu.
    "client/pet/card/props.ts": {"../../../shared/balance/index.ts", "../../../shared/menu.ts"},
    # 0006: todos, settings, the menu's actions and the menu entry.
    "client/pet/card/runtime.ts": {"../../todo-bubble.ts", "../../settings.ts", "../menu-actions.ts", "./entry.ts"},
    # 0008: the menu's styles.
    "client/pet/css.ts": {"../../shared/menu.ts"},
    # 0010: the score popup.
    "client/pet/motion/drag.ts": {"../../../shared/score-popup.ts"},
}
STATEMENT = re.compile(r"""(?m)^\s*(?:import|export)\s+(type\s+)?[^;]*?\bfrom\s*(['"])([^'"]+)\2""")

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
    cut = CUT_IMPORTS.get(path.resolve().relative_to(source.resolve()).as_posix(), set())
    return [(path.parent / m.group(3)).resolve() for m in IMPORT.finditer(path.read_text(encoding="utf-8"))
            if m.group(3) not in cut]


todo = [f for root in ROOTS for f in sorted((source / root).glob("*.ts"))
        if not f.name.endswith(".test.ts") and f.relative_to(source).as_posix() not in SKIP]
todo += [source / f for f in FILES]
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
for rel, specs in CUT_IMPORTS.items():
    text = (target / rel).read_text(encoding="utf-8")
    left = [spec for spec in specs if re.sub(r"\.ts$", "", spec) in text]
    if left:
        sys.exit(f"{rel} still imports {left}: a patch should have removed it")
for path in sorted(target.rglob("*.ts")):
    for m in STATEMENT.finditer(path.read_text(encoding="utf-8")):
        if not m.group(3).startswith(".") and not m.group(1):
            sys.exit(f"{path.relative_to(target)} imports {m.group(3)} at run time")


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


# The animations the companion plays and how often, from the commit's own configuration, with the
# work status bubble's lines the card picks from. The balance and the meal game are dsh-pet
# features A-NAS does not have; sleep and the speech animations (events.whisper, which the card
# plays when it speaks and without which it shows no bubble) are what VNAS controls.
config = jsonc((upstream / "dsh-pet" / "assets" / "config.jsonc").read_text(encoding="utf-8"))
animations = {key: value for key, value in config["animations"].items() if key != "baifan"}
animations["events"] = {key: config["animations"]["events"][key] for key in ("whisper", "workStatus")}
(target / "animations.json").write_text(
    json.dumps({"animations": animations, "animationWeights": config["animationWeights"], "physics": config["physics"],
                "workStatusTexts": config["workStatusTexts"]},
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
    "# Vendored dsh-pet pet card, animation and motion\n\n"
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
