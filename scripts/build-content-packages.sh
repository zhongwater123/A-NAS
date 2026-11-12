#!/usr/bin/env bash
# Builds the content packages a release depends on (scripts/lib/content.sh,
# ADR 0034) into OUT: the AI Worker's models and runtime, web search's
# runtime, Qdrant and llama.cpp. A package already in OUT is kept, so content
# that did not change is built once.
#
# CONTENT holds the files as the release content store lays them out:
#   models/<sha256>/<file>        each model its manifest under deploy/models pins
#   ai-runtimes/<id>.tar.gz       scripts/build-ai-runtime.sh's tarball
#   search-runtimes/<id>.tar.gz   scripts/build-search-runtime.sh's tarball
#   qdrant/<sha256>/<file>        scripts/fetch-qdrant.sh's tarball
#   llama/<sha256>/<file>         scripts/fetch-llama.sh's tarball
#   assistant-runtimes/<sha256>/runtime.tar.gz
#                                 the assistant trial's Hermes runtime
#   upstream/<sha256>/<file>      Docker and Caddy, which scripts/release.sh
#                                 fetches there (not content packages)
# Models, Qdrant, llama.cpp and the Hermes runtime are checked against their
# pins.
#
# With --stand-in, the models and the two runtimes are stand-ins under the
# same names, for the system test: small random files, and virtual
# environments around Debian's Python, where web search's holds the system
# test's stand-in for SearXNG. Qdrant and llama.cpp are the real ones from
# CONTENT; the assistant trial's runtime is left out. Stand-ins go to a
# directory of their own, never to a repository.
#
# Usage: scripts/build-content-packages.sh [--stand-in] OUT CONTENT
set -euo pipefail

stand_in=false
if [[ "${1:-}" == --stand-in ]]; then
  stand_in=true
  shift
fi
if [[ $# -ne 2 ]]; then
  echo "usage: $0 [--stand-in] OUT CONTENT" >&2
  exit 2
fi
mkdir -p "$1"
out=$(cd "$1" && pwd)
content=$(cd "$2" && pwd)
repo=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=lib/content.sh
. "$repo/scripts/lib/content.sh"

SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git -C "$repo" log -1 --format=%ct 2>/dev/null || echo 0)}
export SOURCE_DATE_EPOCH
# The packages' trees are as large as their content, one at a time: they are
# made in /var/tmp, not in /tmp, which may be a small tmpfs, and on a POSIX
# file system, whose modes dpkg-deb records (a Windows drive in WSL shows
# every file as 0777).
work=$(mktemp -d "${ANAS_CONTENT_WORK:-/var/tmp}/a-nas-content.XXXXXX")
trap 'rm -rf -- "$work"' EXIT

# pinned FILE MANIFEST: FILE is the one MANIFEST pins by size and SHA-256.
pinned() {
  [[ -f "$1" && "$(stat -c %s "$1")" == "$(pin_field "$2" sizeBytes)" ]] &&
    printf '%s  %s\n' "$(pin_field "$2" sha256)" "$1" | sha256sum --check --quiet - >/dev/null 2>&1
}

# package NAME ARCH DEPENDS DESCRIPTION COPYRIGHT [SCRIPTS]: builds the package
# from $work/NAME, the tree it installs. DEPENDS may go on with more fields of
# the control file, one per line. SCRIPTS is a directory of maintainer scripts
# the package runs.
package() {
  local name=$1 arch=$2 depends=$3 description=$4 copyright=$5 scripts=${6:-} root=$work/$1
  local deb=$out/${name}_1_$arch.deb
  install -d -m 0755 "$root/DEBIAN" "$root/usr/share/doc/$name"
  printf '%s\n' "$copyright" >"$root/usr/share/doc/$name/copyright"
  {
    echo "Package: $name"
    echo "Version: 1"
    echo "Architecture: $arch"
    echo "Maintainer: A-NAS <zhongwater123@users.noreply.github.com>"
    echo "Section: misc"
    echo "Priority: optional"
    [[ -z "$depends" ]] || echo "Depends: $depends"
    echo "Description: $description"
  } >"$root/DEBIAN/control"
  find "$root" -type d -exec chmod 0755 {} +
  chmod 0644 "$root/DEBIAN/control" "$root/usr/share/doc/$name/copyright"
  [[ -z "$scripts" ]] || install -m 0755 "$scripts"/* "$root/DEBIAN/"
  # Models and runtimes are mostly compressed already; zstd at its default
  # level costs little time for what it saves.
  dpkg-deb --root-owner-group -Zzstd --build "$root" "$deb.partial" >/dev/null
  mv -f -- "$deb.partial" "$deb"
  rm -rf -- "$root"
  echo "$name"
}
# have NAME ARCH: OUT has the package already, or the apt repository that
# ANAS_CONTENT_REPOSITORY names does (scripts/release.sh).
have() {
  [[ -f "$out/${1}_1_$2.deb" ]] ||
    [[ -n "${ANAS_CONTENT_REPOSITORY:-}" && -f "$ANAS_CONTENT_REPOSITORY/pool/main/a/$1/${1}_1_$2.deb" ]]
}

# Models (ADR 0016).
for role in "${ai_roles[@]}"; do
  manifest=$repo/deploy/models/${role##*:}.json
  name=$(model_package "$manifest")
  have "$name" all && continue
  sha=$(pin_field "$manifest" sha256)
  file=$(pin_field "$manifest" file)
  target=$work/$name/opt/a-nas/models/$sha
  install -d "$target"
  if "$stand_in"; then
    head -c 4096 /dev/urandom >"$target/$file"
  else
    source=$content/models/$sha/$file
    pinned "$source" "$manifest" || { echo "missing or not as pinned: $source ($manifest)" >&2; exit 2; }
    cp -- "$source" "$target/$file"
  fi
  chmod 0644 "$target/$file"
  package "$name" all '' "$(pin_field "$manifest" name) for A-NAS's local AI
 The model file deploy/models/${role##*:}.json pins by SHA-256 $sha,
 in /opt/a-nas/models/$sha.$("$stand_in" && printf '\n .\n A stand-in with random content for the system test.')" \
    "$(jq -r '"Source: \(.source // .weights // "see the manifest")\nLicense: \(.license // "see the manifest")"' "$manifest")"
done

# runtime NAME KIND ID STAND_IN_SCRIPT: a Python runtime, unpacked where the
# release links it from. Its scripts start with this path.
runtime() {
  local name=$1 kind=$2 id=$3 stand_in_script=$4 target
  have "$name" amd64 && return
  target=$work/$name/opt/a-nas/$kind/$id
  install -d "$target"
  if "$stand_in"; then
    install -d "$target/bin"
    ln -s /usr/bin/python3 "$target/bin/python"
    printf 'home = /usr/bin\ninclude-system-site-packages = false\nversion = %s\n' "$TARGET_PYTHON" >"$target/pyvenv.cfg"
    [[ -z "$stand_in_script" ]] || install -m 0644 "$stand_in_script" "$target/stand-in.py"
  else
    local tarball=$content/$kind/$id.tar.gz
    [[ -f "$tarball" ]] || { echo "missing $tarball: build it with scripts/build-${kind%-runtimes}-runtime.sh" >&2; exit 2; }
    tar -C "$target" --no-same-owner -xzf "$tarball"
  fi
  package "$name" amd64 "python3 (>= $TARGET_PYTHON~), python3 (<< ${TARGET_PYTHON%.*}.$((${TARGET_PYTHON#*.} + 1))~)" \
    "the Python runtime of A-NAS's ${kind%-runtimes}
 A virtual environment for Debian 13's Python $TARGET_PYTHON in
 /opt/a-nas/$kind/$id, built from the lock its ID names.$("$stand_in" && printf '\n .\n A stand-in around Debian'"'"'s Python for the system test.')" \
    "Built from the wheels the lock pins by hash; each package keeps its own license in the runtime."
}
runtime "a-nas-ai-runtime-$(ai_runtime_id "$repo")" ai-runtimes "$(ai_runtime_id "$repo")" ''
runtime "a-nas-search-runtime-$(search_runtime_id "$repo")" search-runtimes "$(search_runtime_id "$repo")" \
  "$repo/scripts/system-test-search.py"

# Qdrant (ADR 0028): its binary alone, out of the official tarball.
pin=$repo/deploy/qdrant/qdrant.json
name=$(qdrant_package "$repo")
if ! have "$name" amd64; then
  sha=$(pin_field "$pin" sha256)
  tarball=$content/qdrant/$sha/$(pin_field "$pin" file)
  pinned "$tarball" "$pin" || { echo "missing or not as pinned: $tarball: run scripts/fetch-qdrant.sh" >&2; exit 2; }
  install -d "$work/$name/opt/a-nas/qdrant/$sha" "$work/unpacked"
  tar -C "$work/unpacked" --no-same-owner -xzf "$tarball" "$(pin_field "$pin" binary)"
  install -m 0755 "$work/unpacked/$(pin_field "$pin" binary)" "$work/$name/opt/a-nas/qdrant/$sha/qdrant"
  rm -rf -- "$work/unpacked"
  package "$name" amd64 '' "Qdrant $(pin_field "$pin" version) for A-NAS's vector index
 The statically linked server deploy/qdrant/qdrant.json pins, in
 /opt/a-nas/qdrant/$sha." "Source: $(pin_field "$pin" source)
License: $(pin_field "$pin" license)"
fi

# llama.cpp: llama-server with its libraries and licence, as the pin names.
pin=$repo/deploy/llama/llama.json
name=$(llama_package "$repo")
if ! have "$name" amd64; then
  sha=$(pin_field "$pin" sha256)
  tarball=$content/llama/$sha/$(pin_field "$pin" file)
  pinned "$tarball" "$pin" || { echo "missing or not as pinned: $tarball: run scripts/fetch-llama.sh" >&2; exit 2; }
  directory=$(pin_field "$pin" directory)
  mapfile -t members < <(jq -r '.members[]' "$pin")
  target=$work/$name/opt/a-nas/llama/$sha
  install -d "$target"
  tar -C "$target" --no-same-owner --strip-components=1 -xzf "$tarball" --wildcards "${members[@]/#/$directory/}"
  chmod -R u=rwX,go=rX "$target"
  chmod 0755 "$target/$(pin_field "$pin" binary)"
  package "$name" amd64 'libc6, libgcc-s1, libstdc++6, libgomp1, libssl3t64' "llama.cpp $(pin_field "$pin" version) for A-NAS's text reader
 llama-server with its libraries, as deploy/llama/llama.json pins them,
 in /opt/a-nas/llama/$sha." "Source: $(pin_field "$pin" source)
License: $(pin_field "$pin" license)"
fi

# The assistant trial's Hermes runtime (ai/assistant-spike/runtime.json). Its
# scripts name /opt/a-nas/assistant-runtimes/spike, so one is installed at a
# time: each provides, conflicts with and replaces a-nas-assistant-runtime.
# Its prerm stops what runs on it before apt replaces it; the trial's postinst
# starts them again (deploy/debian/a-nas-assistant-runtime/DEBIAN/prerm).
pin=$repo/ai/assistant-spike/runtime.json
name=$(assistant_runtime_package "$repo")
if ! "$stand_in" && ! have "$name" amd64; then
  sha=$(pin_field "$pin" sha256)
  tarball=$content/assistant-runtimes/$sha/$(pin_field "$pin" file)
  if [[ ! -e "$tarball" ]]; then
    # A release needs it (scripts/release.sh); the system test does not.
    echo "note: no $tarball, so no $name" >&2
  elif ! pinned "$tarball" "$pin"; then
    echo "not as pinned: $tarball (ai/assistant-spike/runtime.json)" >&2
    exit 2
  else
    install -d "$work/$name/opt/a-nas/assistant-runtimes"
    tar -C "$work/$name/opt/a-nas/assistant-runtimes" --no-same-owner -xzf "$tarball"
    package "$name" amd64 "python3 (>= $TARGET_PYTHON~), python3 (<< ${TARGET_PYTHON%.*}.$((${TARGET_PYTHON#*.} + 1))~)
Provides: a-nas-assistant-runtime
Conflicts: a-nas-assistant-runtime
Replaces: a-nas-assistant-runtime" "the Hermes runtime of the A-NAS assistant trial
 The runtime ai/assistant-spike/runtime.json pins by SHA-256 $sha, in
 /opt/a-nas/assistant-runtimes/spike, for development testing only
 (docs/runbooks/try-ai-assistant-on-experimental-nas.md)." \
      "Hermes Agent (MIT) with the packages its build installs, each under its own license." \
      "$repo/deploy/debian/a-nas-assistant-runtime/DEBIAN"
  fi
fi
