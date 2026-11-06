"""Vendors the parts of dsh-pet the companion reuses: the self-playing animation
chain (src/client/pet/animation), dragging, throwing and walking (src/client/pet/motion),
the physics they share (src/shared/physics), and the plain modules those import.
Nothing that talks to DeepSeek Harness, a model, a voice or the network is copied;
the script stops if an import reaches outside the plain modules.

Run on the development machine, with a dsh-pet checkout at the pinned commit:
    python3 scripts/vendor-dsh-pet.py <dsh-pet checkout> web/src/companion/dsh-pet

Imports lose their .ts suffix (A-NAS does not import with extensions). Changes A-NAS
needs are patches in web/src/companion/dsh-pet-patches/, applied in name order.
"""
import re
import shutil
import subprocess
import sys
from pathlib import Path

PINNED = "10f6c868b8110c897af33d6137a3d85e84b665ba"
ROOTS = ["client/pet/animation", "client/pet/motion", "shared/physics"]
# Moves the pet where the Harness host tells it to; A-NAS has no such host.
SKIP = {"client/pet/motion/position-follow.ts"}
# Code reached through imports must stay in these plain, browser-only modules.
ALLOWED = ("client/pet/", "shared/")
DENIED = ("shared/chat/", "shared/balance/", "shared/todo/", "shared/menu/", "client/pet/card/")
IMPORT = re.compile(r"""((?:from|import)\s*\(?\s*)(['"])(\.{1,2}/[^'"]+)\2""")

upstream, target = Path(sys.argv[1]), Path(sys.argv[2])
source = upstream / "dsh-pet" / "src"
patches = target.with_name("dsh-pet-patches")
commit = subprocess.run(["git", "-C", str(upstream), "rev-parse", "HEAD"],  # noqa: S607  a development tool, git from PATH
                        capture_output=True, text=True, check=True).stdout.strip()
if commit != PINNED:
    sys.exit(f"dsh-pet checkout is at {commit[:12]}, expected the pinned {PINNED[:12]}")


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
    "Patches applied: " + (", ".join(f"`{name}`" for name in applied) or "none") + ".\n",
    encoding="utf-8", newline="\n")
print(f"vendored {len(files)} files from {commit[:12]} with {len(applied)} patches")
