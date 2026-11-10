"""Vendors the parts of dsh-pet the companion reuses: the pet card (src/client/pet/card,
createPetCard and the runtime that wires it), the self-playing animation chain
(src/client/pet/animation), dragging, throwing, walking and following a position
(src/client/pet/motion), the physics they share (src/shared/physics), the host's work status
store, the bridge that publishes it and the page state it writes, the host's decisions on the
commands VNAS sends with the lock, sleep and playback stores they use (FILES), and the plain
modules those import. Nothing that talks to DeepSeek Harness, a model, a voice or the network
is copied: what the card reaches of the balance, the menu, todos, settings, chat and voice, and
what the host's decisions read from disk, is cut by patches (CUT_IMPORTS). The script stops if an import reaches outside the plain modules,
or if a vendored file imports a package at run time (type-only imports, which the build
erases, are fine).

Run on the development machine, with a clean dsh-pet checkout at the pinned commit and its
HEVC animations for Safari (the assets-mov release, MOV_ZIP below), in a Python that has the
development tools of ai/requirements-dev.txt installed (json5 reads dsh-pet's config.jsonc):
    python3 scripts/vendor-dsh-pet.py <dsh-pet checkout> web/src/companion/dsh-pet <assets-mov zip>
Without the zip, --mov-from <animations.sha256> takes the HEVC animations' lines from that
manifest instead, unchecked; scripts/check-dsh-pet-vendor.sh uses it to vendor again and compare
when only the checkout is at hand.

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

import json5

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
# For VNAS's commands, the host's decision layers and the stores behind them, so that what the
# page accepts and writes is what dsh-pet's routes would: /anim, /position and /throw, /broadcast
# and /bubble/clear, the sleep and the lock (with its gate), and the playback receipts.
FILES = ["host/work-status.ts", "host/plugin/work-status-bridge.ts", "host/state.ts", "client/pet/css.ts",
         "host/anim.ts", "host/motion-cue.ts", "host/broadcast.ts", "host/sleep.ts", "host/lock.ts",
         "host/playback/status.ts", "host/playback/store.ts", "host/playback/parse.ts"]
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
    # 0021: the configuration on disk; the page's one pet is resolved from animations.json.
    "host/anim.ts": {"./config.ts"},
    "host/motion-cue.ts": {"./config.ts"},
    # 0021 and 0023: the configuration on disk, and the meme pictures on disk.
    "host/broadcast.ts": {"./config.ts", "./memes.ts"},
}
STATEMENT = re.compile(r"""(?m)^\s*(?:import|export)\s+(type\s+)?[^;]*?\bfrom\s*(['"])([^'"]+)\2""")

USAGE = "usage: vendor-dsh-pet.py <dsh-pet checkout> <target> <assets-mov zip> | --mov-from <animations.sha256>"
if len(sys.argv) == 4:
    mov_zip, mov_from = Path(sys.argv[3]), None
elif len(sys.argv) == 5 and sys.argv[3] == "--mov-from":
    mov_zip, mov_from = None, Path(sys.argv[4])
else:
    sys.exit(USAGE)
upstream, target = Path(sys.argv[1]), Path(sys.argv[2])
source = upstream / "dsh-pet" / "src"
patches = target.with_name("dsh-pet-patches")


def git(*args: str) -> str:
    return subprocess.run(["git", "-C", str(upstream), *args],  # noqa: S607  a development tool, git from PATH
                          capture_output=True, text=True, check=True).stdout


commit = git("rev-parse", "HEAD").strip()
if commit != PINNED:
    sys.exit(f"dsh-pet checkout is at {commit[:12]}, expected the pinned {PINNED[:12]}")
# The pinned commit is what is copied, not the checkout: a change, a new file, or an ignored
# file where the script reads would be copied with it as if it were dsh-pet's.
changed = git("status", "--porcelain", "--untracked-files=all")
changed += git("status", "--porcelain", "--untracked-files=all", "--ignored=matching", "--",
               "dsh-pet/src", "dsh-pet/assets/config.jsonc", "dsh-pet/assets/webm", "LICENSE")
if changed:
    sys.exit(f"dsh-pet checkout differs from {PINNED[:12]}; vendor from a clean checkout:\n{changed}")
if mov_zip and hashlib.sha256(mov_zip.read_bytes()).hexdigest() != MOV_ZIP[1]:
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


# The animations the companion plays and how often, from the commit's own configuration, with the
# work status bubble's lines the card picks from. The balance and the meal game are dsh-pet
# features A-NAS does not have; sleep and the speech animations (events.whisper, which the card
# plays when it speaks and without which it shows no bubble) are what VNAS controls.
# JSON with comments and trailing commas, read with json5 rather than by hand (AGENTS.md).
config = json5.loads((upstream / "dsh-pet" / "assets" / "config.jsonc").read_text(encoding="utf-8"))
animations = {key: value for key, value in config["animations"].items() if key != "baifan"}
animations["events"] = {key: config["animations"]["events"][key] for key in ("whisper", "workStatus")}
(target / "animations.json").write_text(
    json.dumps({"animations": animations, "animationWeights": config["animationWeights"], "physics": config["physics"],
                "workStatusTexts": config["workStatusTexts"]},
               ensure_ascii=False, indent=2) + "\n", encoding="utf-8", newline="\n")

hashes = {path.name: hashlib.sha256(path.read_bytes()).hexdigest()
          for path in (upstream / "dsh-pet" / "assets" / "webm").glob("*.webm")}
if mov_zip:
    with zipfile.ZipFile(mov_zip) as archive:
        hashes |= {entry.filename: hashlib.sha256(archive.read(entry)).hexdigest()
                   for entry in archive.infolist() if not entry.is_dir()}
else:
    for line in mov_from.read_text(encoding="utf-8").splitlines():
        hash_, _, name = line.partition("  ")
        if name.endswith(".mov"):
            if not re.fullmatch(r"[0-9a-f]{64}", hash_):
                sys.exit(f"malformed line in {mov_from}: {line!r}")
            hashes[name] = hash_
lines = []
for name in sorted(hashes):
    if not re.fullmatch(r"[^./\\\x00-\x1f\x7f][^/\\\x00-\x1f\x7f]*\.(webm|mov)", name) or name != name.strip():
        sys.exit(f"unexpected animation file name: {name!r}")
    lines.append(f"{hashes[name]}  {name}\n")
manifest = "".join(lines).encode()
(target / "animations.sha256").write_bytes(manifest)
pool = hashlib.sha256(manifest).hexdigest()[:16]
shutil.copyfile(upstream / "LICENSE", target / "LICENSE")
# Where the copied files live upstream, for the look at upstream changes the README asks for: their
# directories, a little wider than the files themselves.
folders = {path.relative_to(source.resolve()).parent for path in files}
watched = sorted((Path("dsh-pet/src") / folder).as_posix() for folder in folders
                 if not any(other in folder.parents for other in folders))
(target / "README.md").write_text(
    "# Vendored dsh-pet pet card, animation and motion\n\n"
    f"Copied from [PC2005-cloud/dsh-pet](https://github.com/PC2005-cloud/dsh-pet) commit `{commit}` "
    "by `scripts/vendor-dsh-pet.py`. The code is MIT, see `LICENSE`, which the Web UI's build ships as "
    "`assets/third-party-licenses.txt` (`make web-build`). Do not edit the copied files by hand: "
    "add a patch to `../dsh-pet-patches/` or change the script, and vendor again. "
    "This README is written by the script too.\n\n"
    "The animations this code plays are not in Git and are not MIT. The character is the community-made "
    "DeepSeek 大肥鱼, not an official DeepSeek character: the original 溟月 is by "
    "[@上善无形](https://space.bilibili.com/4456176) and the maid design by "
    "[@ZipZipPipe](https://space.bilibili.com/4168597), licensed "
    "[CC BY-NC-SA 4.0](https://creativecommons.org/licenses/by-nc-sa/4.0/) with no commercial use in any form; "
    "dsh-pet's animations, prompts and source videos may not be used commercially. Anything showing the "
    "character credits <https://github.com/PC2005-cloud/dsh-pet>, and A-NAS must replace it before any "
    "commercial release.\n\n"
    "Patches applied: " + (", ".join(f"`{name}`" for name in applied) or "none") + ".\n\n"
    f"`animations.sha256` lists the {len(lines)} animation files of this commit and of `{MOV_ZIP[0]}` "
    f"(SHA-256 `{MOV_ZIP[1]}`); installed, they are companion animation pool `{pool}`, the pool "
    "`scripts/install-media-pool.sh` pins. Upstream has since replaced that release asset: the pinned zip "
    "comes from a kept copy, and the script refuses any other.\n\n"
    "## No updates\n\n"
    "1. A-NAS does not follow upstream. A new dsh-pet release or commit sets nothing off.\n"
    "2. A device never updates it. The code is compiled into the Web desktop that anas-api serves; the "
    "animations are a pool an administrator installs with `scripts/install-media-pool.sh`; the NAS fetches "
    "nothing of dsh-pet from the network.\n"
    "3. Upgrading is a deliberate dependency upgrade, only when upstream fixes a security problem or a bug "
    "A-NAS needs in the copied files, when a new character or new animations are wanted, or when the "
    "companion's next version is planned.\n"
    "4. Dependabot does not see vendored code, so no security alert comes on its own. Before each milestone, "
    "see whether upstream changed what is copied (an empty output means no):\n\n"
    "   ```bash\n"
    f"   git -C <dsh-pet checkout> fetch origin HEAD && git -C <dsh-pet checkout> diff --stat {commit[:12]}..FETCH_HEAD -- \\\n"
    "     " + " ".join(watched) + " \\\n"
    "     dsh-pet/assets/config.jsonc dsh-pet/assets/webm LICENSE\n"
    "   ```\n\n"
    "## Upgrading\n\n"
    "1. Set `PINNED` in `scripts/vendor-dsh-pet.py` to the new commit, and `MOV_ZIP` when the HEVC "
    "animations changed too; keep a copy of that zip, since upstream may replace it.\n"
    "2. Check the commit out in a clean dsh-pet checkout and run the script; adapt the patches in "
    "`../dsh-pet-patches/` one by one until they apply and the script passes.\n"
    "3. The new `animations.sha256` is a new pool: set `companion_pool` in `scripts/install-media-pool.sh` "
    "to the pool the script prints, and update the install runbook.\n"
    "4. Run `make vendor-check DSH_PET=<checkout> DSH_PET_MOV_ZIP=<zip>` and `make check`, then try the "
    "companion in a browser, Safari included.\n"
    "5. Stage and install the new pool on the NAS (`scripts/deploy-media-pool.ps1 -Kind companion`, then "
    "`install-media-pool.sh companion`), and have the upgrade reviewed as a dependency upgrade.\n",
    encoding="utf-8", newline="\n")
if not mov_zip:
    print(f"HEVC animations not checked: their lines come from {mov_from}, not from the assets-mov zip")
print(f"vendored {len(files)} files from {commit[:12]} with {len(applied)} patches; animation pool {pool}")
