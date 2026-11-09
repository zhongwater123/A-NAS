#!/usr/bin/env bash
# Stages a release bundle on the NAS as the staging account. deploy-dev.ps1
# -BundleDirectory writes the bundle on the development machine; a deploy
# machine that only copies files brings it here and runs this script, which
# stages exactly what deploy-dev.ps1 uploads over SSH and then runs the same
# remote-activate-release.sh with stage-only
# (docs/runbooks/stage-a-release-bundle.md).
#
# Usage: stage.sh [--missing] [CONTENT_DIR]
#   CONTENT_DIR  where the bundle's content files are, laid out as
#                deploy-dev.ps1 wrote them (default: the bundle's ../content).
#                A content file the NAS already holds is not needed there.
#   --missing    list the content files that are neither on the NAS nor in
#                CONTENT_DIR, one path per line, and change nothing.
#
# Check SHA256SUMS against the hash the developer handed over before running
# this script: the script checks the bundle against SHA256SUMS, not SHA256SUMS
# itself.
set -euo pipefail

bundle=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
list_missing=false
if [[ "${1:-}" == --missing ]]; then
  list_missing=true
  shift
fi
[[ $# -le 1 ]] || { echo "usage: stage.sh [--missing] [CONTENT_DIR]" >&2; exit 2; }
content=${1:-$bundle/../content}
staging=$HOME/apps/a-nas

cd "$bundle"
sha256sum --check --strict --quiet SHA256SUMS

version='' product_version='' mode='' api_sha='' agent_sha=''
contents=() releases=() extras=() roots=()
name_pattern='^[A-Za-z0-9][A-Za-z0-9._@-]*$'
while read -r kind first second third; do
  case "$kind" in
    version) version=$first ;;
    product-version) product_version=$first ;;
    mode) mode=$first ;;
    api-sha256) api_sha=$first ;;
    host-agent-sha256) agent_sha=$first ;;
    content)
      # A directory below the staging area, such as models/<sha256>.
      [[ "$first" =~ ^[A-Za-z0-9-]+(/[0-9a-f]{64})?$ && "$second" =~ $name_pattern && "$third" =~ ^[0-9a-f]{64}$ ]] ||
        { echo "invalid content line: $first $second $third" >&2; exit 2; }
      contents+=("$first $second $third")
      ;;
    release | extra | root)
      [[ "$first" =~ $name_pattern && -z "$second" ]] || { echo "invalid $kind line: $first" >&2; exit 2; }
      case "$kind" in
        release) releases+=("$first") ;;
        extra) extras+=("$first") ;;
        root) roots+=("$first") ;;
      esac
      ;;
    *) echo "unknown line in bundle.txt: $kind" >&2; exit 2 ;;
  esac
done < bundle.txt
[[ "$version" =~ ^[0-9a-f]{7,40}$ ]] || { echo "bundle.txt names no release" >&2; exit 2; }

# Content travels once per content, as deploy-dev.ps1's uploads do: what the
# NAS already holds is kept, the rest comes from CONTENT_DIR and is checked
# before it takes the name the release links to.
missing=()
for entry in "${contents[@]}"; do
  read -r directory file sha <<<"$entry"
  target=$staging/$directory/$file
  if printf '%s  %s\n' "$sha" "$target" | sha256sum --check --status - 2>/dev/null; then
    continue
  fi
  if [[ ! -f "$content/$directory/$file" ]]; then
    missing+=("$directory/$file")
    continue
  fi
  "$list_missing" && continue
  install -d -m 0750 "$staging/$directory"
  cp -- "$content/$directory/$file" "$target.incoming"
  if ! printf '%s  %s\n' "$sha" "$target.incoming" | sha256sum --check --quiet -; then
    rm -f -- "$target.incoming"
    echo "content does not match the bundle: $directory/$file" >&2
    exit 1
  fi
  mv -f -- "$target.incoming" "$target"
done
if "$list_missing"; then
  ((${#missing[@]} == 0)) || printf '%s\n' "${missing[@]}"
  exit 0
fi
if ((${#missing[@]} > 0)); then
  echo "content missing from $content and from the NAS:" >&2
  printf '  %s\n' "${missing[@]}" >&2
  exit 1
fi

release=$staging/releases/$version
install -d -m 0750 "$release"
for name in "${releases[@]}"; do
  install -m 0644 -- "release/$name" "$release/$name"
done
chmod 0700 "$release/activate.incoming"
"$release/activate.incoming" "$version" "$api_sha" "$agent_sha" "$mode" "$product_version" stage-only

# Files the release carries as they are, such as the container agent, and the
# root scripts, which root installs from the staging account's home after
# checking their hashes.
for name in "${extras[@]}"; do
  [[ ! -e "$release/$name" ]] || { echo "extra file would replace a staged one: $name" >&2; exit 1; }
  install -m 0644 -- "extra/$name" "$release/$name"
done
for name in "${roots[@]}"; do
  install -m 0644 -- "root/$name" "$HOME/$name"
done
echo "Staged A-NAS $product_version ($version) in $release."
