"""Vendors the parts of Hermes Desktop's chat layer the A-NAS assistant window
reuses: the transcript model (lib/chat-messages) with the small libraries it
needs, the assistant-ui runtime adapter, the generated gateway types, and the
JSON-RPC client Desktop and the web dashboard share (apps/shared).

Run on the development machine:
    python3 ai/assistant-spike/vendor-hermes-ui.py <hermes-agent checkout> web/src/assistant/hermes

Imports are rewritten to relative paths; the few modules that would pull in
the Desktop application (media, generated images, connector tools, the
@hermes/shared barrel) are replaced by the local shims in shims/.
"""
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

upstream, target = Path(sys.argv[1]), Path(sys.argv[2])
desktop, shared = upstream / "apps/desktop/src", upstream / "apps/shared/src"
commit = subprocess.run(["git", "-C", str(upstream), "rev-parse", "HEAD"], capture_output=True, text=True, check=True).stdout.strip()

COPY = [
    *[f"lib/chat-messages/{f}" for f in sorted(os.listdir(desktop / "lib/chat-messages")) if not f.endswith(".test.ts")],
    "lib/incremental-external-store-runtime.ts",
    "lib/text.ts",
    "lib/todos.ts",
    "lib/error-surface.ts",
    "lib/embedded-images.ts",
    "lib/tool-result-metadata.ts",
    "components/assistant-ui/reference-kinds.ts",
    "types/hermes.ts",
]
SHARED = ["gateway-contract.generated.ts", "skill-scaffold.ts", "gateway-events.ts", "json-rpc-channel.ts",
          "json-rpc-gateway.ts", "reconnect-backoff.ts"]
SHIMS = Path(__file__).with_name("hermes-ui-shims")

if target.exists():
    shutil.rmtree(target)
header = f"// Vendored from NousResearch/hermes-agent {commit[:12]} (MIT, see LICENSE).\n"


def rewrite(source: str, here: Path) -> str:
    def relative(spec: str) -> str:
        if spec.startswith("@/"):
            dest = target / spec[2:]
        elif spec == "@hermes/shared":
            dest = target / "shared"
        elif spec.startswith("@hermes/shared/"):
            dest = target / "shared" / spec[len("@hermes/shared/"):]
        else:
            return spec
        rel = os.path.relpath(dest, here.parent).replace(os.sep, "/")
        return rel if rel.startswith(".") else "./" + rel
    return re.sub(r"(from |import\()'([^']+)'", lambda m: f"{m.group(1)}'{relative(m.group(2))}'", source)


for rel in COPY:
    dest = target / rel
    dest.parent.mkdir(parents=True, exist_ok=True)
    dest.write_text(header + rewrite((desktop / rel).read_text(encoding="utf-8"), dest), encoding="utf-8", newline="\n")
for name in SHARED:
    dest = target / "shared" / name
    dest.parent.mkdir(parents=True, exist_ok=True)
    dest.write_text(header + (shared / name).read_text(encoding="utf-8"), encoding="utf-8", newline="\n")
for shim in SHIMS.rglob("*.ts"):
    dest = target / shim.relative_to(SHIMS)
    dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(shim, dest)
shutil.copyfile(upstream / "LICENSE", target / "LICENSE")
(target / "README.md").write_text(
    "# Vendored Hermes Desktop chat layer\n\n"
    f"Copied from [NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent) commit `{commit}` "
    "by `ai/assistant-spike/vendor-hermes-ui.py`; MIT, see `LICENSE`. Do not edit the copied files by hand: "
    "change the shims or the script and vendor again.\n",
    encoding="utf-8", newline="\n")
print(f"vendored {len(COPY) + len(SHARED)} files and {len(list(SHIMS.rglob('*.ts')))} shims from {commit[:12]}")
