#!/usr/bin/env bash
# Assemble ARM wheels with native CPython 3.13; execute/import them separately
# under ARM emulation before treating the result as a validated runtime.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
export ANAS_TARGET_ARCH=arm64
# shellcheck source=lib/content.sh
. "$repo/scripts/lib/content.sh"
# shellcheck source=lib/python-runtime.sh
. "$repo/scripts/lib/python-runtime.sh"
require_uv "$repo"
kind=${1:-}
case "$kind" in
  ai) id=$(ai_runtime_id "$repo"); lock=$(target_file "$repo" ai/requirements.txt) ;;
  search) id=$(search_runtime_id "$repo"); lock=$(target_file "$repo" deploy/search/requirements.txt) ;;
  *) echo "usage: $0 ai|search" >&2; exit 2 ;;
esac
python=${ANAS_BUILD_PYTHON:-python3.13}
[[ "$("$python" -c 'import sys; print("%d.%d" % sys.version_info[:2])')" == "$TARGET_PYTHON" ]] || {
  echo "ARM runtime assembly needs native CPython $TARGET_PYTHON" >&2; exit 2;
}
out=$repo/build/$kind-runtime
mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
runtime=$work/runtime
"$python" -m venv "$runtime"
options=()
if [[ "$kind" == ai ]]; then options=(--excludes "$repo/ai/requirements.excludes"); fi
uv pip install --python "$runtime/bin/python" --prefix "$runtime" --python-version 3.13 \
  --python-platform aarch64-manylinux_2_40 --require-hashes --only-binary :all: \
  --compile-bytecode "${options[@]}" -r "$lock"
check_status=0
uv pip check --python "$runtime/bin/python" --python-version 3.13 \
  --python-platform aarch64-manylinux_2_40 > "$work/dependencies.log" 2>&1 || check_status=$?
cat "$work/dependencies.log"
if [[ "$check_status" != 0 ]]; then
  [[ "$kind" == ai && "$check_status" == 1 ]] || exit "$check_status"
  # uv checks target wheel tags. Its only accepted incompatibilities are
  # MediaPipe's explicitly replaced dependencies, not arbitrary missing libs.
  "$runtime/bin/python" - "$work/dependencies.log" "$repo/ai/requirements.excludes" <<'PY'
import pathlib
import re
import sys

from packaging.requirements import Requirement
from packaging.utils import canonicalize_name

report, policy = map(pathlib.Path, sys.argv[1:])
allowed = {canonicalize_name(line.split('#', 1)[0].strip()) for line in policy.read_text().splitlines() if line.split('#', 1)[0].strip()}
accepted = 0
expected = None
for line in report.read_text().splitlines():
    if line.startswith('Using Python ') or re.fullmatch(r'Checked \d+ packages? in .+', line):
        continue
    count = re.fullmatch(r'Found (\d+) incompatibilit(?:y|ies)', line)
    if count:
        expected = int(count.group(1))
        continue
    missing = re.fullmatch(r"The package `mediapipe` requires `([^`]+)`, but it's not installed", line)
    if not missing or canonicalize_name(Requirement(missing.group(1)).name) not in allowed:
        raise SystemExit('Unexpected target dependency incompatibility: ' + line)
    accepted += 1
if not accepted or accepted != expected:
    raise SystemExit('Dependency report did not match the explicit replacement policy')
PY
fi
if [[ "$kind" == search ]]; then
  pin=$repo/deploy/search/searxng.json
  sha=$(pin_field "$pin" sha256)
  archive=$repo/build/search-source/$sha/searxng.tar.gz
  mkdir -p "$(dirname "$archive")"
  if [[ ! -f "$archive" ]]; then curl --fail --location --proto '=https' -o "$archive" "$(pin_field "$pin" source)"; fi
  printf '%s  %s\n' "$sha" "$archive" | sha256sum -c -
  [[ "$(stat -c %s "$archive")" == "$(pin_field "$pin" sizeBytes)" ]] || exit 2
  commit=$(pin_field "$pin" commit)
  version=$(pin_field "$pin" version)
  mkdir -p "$runtime/lib/searxng" "$runtime/share/searxng"
  tar -C "$runtime/lib/searxng" --strip-components=1 --no-same-owner -xzf "$archive" "searxng-$commit/searx"
  jq -rn --arg VERSION_STRING "$version" --arg VERSION_TAG "$version" --arg DOCKER_TAG "${version/+/-}" \
    --arg GIT_URL https://github.com/searxng/searxng --arg GIT_BRANCH master \
    '$ARGS.named | to_entries[] | "\(.key) = \(.value | @json)"' > "$runtime/lib/searxng/searx/version_frozen.py"
  "$runtime/bin/python" -m compileall -q "$runtime/lib/searxng"
  cp "$archive" "$runtime/share/searxng/searxng-$commit.tar.gz"
  tar -xzOf "$archive" "searxng-$commit/LICENSE" > "$runtime/share/searxng/LICENSE"
fi
"$runtime/bin/python" -m pip uninstall --quiet --yes pip
"$python" - "$runtime" "/opt/a-nas/$kind-runtimes/$id" <<'PY'
import pathlib
import sys

root, installed = sys.argv[1:]
for path in (pathlib.Path(root) / 'bin').iterdir():
    if path.is_symlink() or not path.is_file():
        continue
    try:
        text = path.read_text()
    except UnicodeDecodeError:
        continue
    path.write_text(text.replace(root, installed))
PY
while IFS= read -r -d '' library; do require_target_elf "$library"; done < <(find "$runtime" -type f \( -name '*.so' -o -name '*.so.*' \) -print0)
# No build-machine interpreter path is shipped. The device supplies Python.
rm -f "$runtime/bin/python" "$runtime/bin/python3" "$runtime/bin/python3.13"
ln -s /usr/bin/python3.13 "$runtime/bin/python"
ln -s python "$runtime/bin/python3"
ln -s python "$runtime/bin/python3.13"
printf 'home = /usr/bin\ninclude-system-site-packages = false\nversion = 3.13\n' > "$runtime/pyvenv.cfg"
mkdir -p "$runtime/share"
printf '{"os":"linux","architecture":"arm64","python":"3.13","abi":"cp313"}\n' > "$runtime/share/target.json"
tar -C "$runtime" --owner=0 --group=0 --numeric-owner -czf "$out/$id.tar.gz.partial" .
mv "$out/$id.tar.gz.partial" "$out/$id.tar.gz"
write_runtime_manifest "$out/$id.tar.gz" "$id" "$TARGET_PYTHON" "$repo/build/$kind-runtime-arm64.json"
