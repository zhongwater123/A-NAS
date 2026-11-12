#!/usr/bin/env bash
# Fetches the Docker and Caddy packages that deploy/upstream/packages.json
# pins (ADR 0015) into DIR/<sha256>/, and prints their paths. A-NAS's signed
# repository carries these files (scripts/release.sh), and the system test
# installs them (scripts/system-test.sh). Each is checked against its pinned
# size and SHA-256; one already in DIR, as a CI cache or the release content
# store keeps it, is reused once it passes the same check.
#
# Usage: scripts/fetch-upstream-packages.sh [DIR]
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
lock=$repo/deploy/upstream/packages.json
mkdir -p "${1:-$repo/build/upstream}"
out=$(cd "${1:-$repo/build/upstream}" && pwd)
# pinned FILE SIZE SHA256: FILE has this size and SHA-256.
pinned() {
  [[ -f "$1" && "$(stat -c %s "$1")" == "$2" ]] &&
    printf '%s  %s\n' "$3" "$1" | sha256sum --check --quiet - >/dev/null 2>&1
}

jq -r '.packages[] | [.source, .file, .sizeBytes, .sha256] | @tsv' "$lock" |
  while IFS=$'\t' read -r source file size sha; do
    uri=$(jq -er --arg source "$source" '.sources[$source].uri' "$lock")
    name=${file##*/}
    if [[ ! "$sha" =~ ^[0-9a-f]{64}$ || ! "$size" =~ ^[0-9]+$ || "$uri" != https://* ||
      ! "$name" =~ ^[A-Za-z0-9][A-Za-z0-9.+~_-]*\.deb$ || "$file" == *..* ]]; then
      echo "invalid entry in $lock: $file" >&2
      exit 2
    fi
    target=$out/$sha/$name
    if ! pinned "$target" "$size" "$sha"; then
      install -d "$out/$sha"
      curl --fail --location --silent --show-error --proto '=https' --retry 3 --output "$target.partial" "$uri/$file"
      if ! pinned "$target.partial" "$size" "$sha"; then
        rm -f -- "$target.partial"
        echo "the downloaded package does not match $lock: $uri/$file" >&2
        exit 4
      fi
      mv -f -- "$target.partial" "$target"
    fi
    echo "$target"
  done
