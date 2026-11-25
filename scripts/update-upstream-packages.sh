#!/usr/bin/env bash
# Updates deploy/upstream/packages.json to the newest Docker and Caddy
# packages that their own apt repositories publish for Debian 13 (ADR 0015).
# A-NAS's signed repository carries copies of exactly these files, which
# scripts/fetch-upstream-packages.sh fetches and checks by SHA-256, so a
# release installs the Docker and Caddy that its system test ran.
#
# apt in IMAGE reads each repository's index, whose signature it checks
# against deploy/upstream/<source>.asc. Each key must have the fingerprint
# that packages.json pins: a new key is a change to review on its own. The
# index gives each package's version, file, size, SHA-256 and the packages
# it depends on, which scripts/build-system-test-image.sh installs from
# Debian. Review the diff, then run the gates and make release
# (docs/runbooks/deploy-a-nas-packages.md#更新-docker-与-caddy).
#
# Usage: scripts/update-upstream-packages.sh
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
image=${IMAGE:-anas-systemd:trixie}
lock=$repo/deploy/upstream/packages.json
incoming=$(mktemp "$lock.XXXXXX")
trap 'rm -f -- "$incoming"' EXIT

# shellcheck disable=SC2016 # the script expands its variables in the image
docker run --rm -v "$repo/deploy/upstream:/upstream:ro" "$image" bash -c '
  set -euo pipefail
  lock=/upstream/packages.json
  # apt reads these repositories alone.
  rm -f /etc/apt/sources.list /etc/apt/sources.list.d/* /etc/apt/preferences.d/*
  for source in $(jq -r ".sources | keys[]" "$lock"); do
    field() { jq -er --arg source "$source" --arg key "$1" ".sources[\$source][\$key]" "$lock"; }
    key=/upstream/$source.asc
    fingerprint=$(gpg --batch --show-keys --with-colons "$key" 2>/dev/null | awk -F: "\$1 == \"fpr\" { print \$10; exit }")
    if [[ "$fingerprint" != "$(field fingerprint)" ]]; then
      echo "$key has fingerprint $fingerprint, not the one packages.json pins" >&2
      exit 3
    fi
    printf "Types: deb\nURIs: %s\nSuites: %s\nComponents: %s\nArchitectures: amd64\nSigned-By: %s\n" \
      "$(field uri)" "$(field suite)" "$(field component)" "$key" >"/etc/apt/sources.list.d/$source.sources"
  done
  apt-get update -qq -o APT::Update::Error-Mode=any >/dev/null
  # Each package at the version apt would install, from its own repository.
  jq -c ".packages[]" "$lock" | while read -r package; do
    name=$(jq -r .name <<<"$package")
    source=$(jq -r .source <<<"$package")
    uri=$(jq -er --arg source "$source" ".sources[\$source].uri" "$lock")
    record=$(apt-cache show --no-all-versions "$name")
    value() { sed -n "s/^$1: //p" <<<"$record" | head -n 1; }
    [[ "$(apt-cache policy "$name" | sed -n "/^  Candidate:/,\$p")" == *"$uri"* ]] ||
      { echo "$name does not come from $uri" >&2; exit 3; }
    depends=$(value Pre-Depends)
    [[ -z "$(value Depends)" ]] || depends=${depends:+$depends, }$(value Depends)
    jq -n --arg name "$name" --arg source "$source" --arg version "$(value Version)" --arg file "$(value Filename)" \
      --argjson size "$(value Size)" --arg sha256 "$(value SHA256)" --arg depends "$depends" \
      "{name: \$name, source: \$source, version: \$version, file: \$file, sizeBytes: \$size, sha256: \$sha256, depends: \$depends}"
  done | jq -s --slurpfile lock "$lock" "\$lock[0] + {packages: .}"
' >"$incoming"
jq -e '.packages | length > 0' "$incoming" >/dev/null
mv -f -- "$incoming" "$lock"
trap - EXIT
jq -r '.packages[] | "\(.name) \(.version)"' "$lock"
