#!/usr/bin/env bash
# Builds the product's Debian packages into OUT at VERSION, the Debian version
# scripts/release-version.sh gives (the Makefile's VERSION), from this tree and
# the programs make build-binaries leaves in BUILD (ADR 0034):
#
# - a-nas: the programs and the AI Worker's code under /usr/lib/a-nas, the
#   systemd units with the links that turn them on, the accounts
#   (sysusers.d), directories (tmpfiles.d), a-nas-generator, anas-site.service,
#   a-nas-site and a-nas-check. It depends on the content packages it links
#   to (scripts/lib/content.sh) and on a-nas-host at its own version.
# - a-nas-host and a-nas-console, which declare what the product needs from
#   Debian (ADR 0031); a-nas-host also needs Docker and Caddy at least at the
#   versions deploy/upstream/packages.json pins (ADR 0015), so installing a
#   release upgrades them to the ones its system test ran, and rolling back
#   leaves them;
# - a-nas-assistant-trial, the assistant's trial (ADR 0024), at the version of
#   a-nas, which it depends on with its Hermes runtime's content package.
#
# OUT then holds this version's packages alone: those of an earlier build go,
# so that the system test and make release find the ones just built.
#
# Files go only to /usr, and the result does not depend on the package's
# scripts, so the same files make the product's system extension
# (docs/research/sysupdate-image-verification.md#第-6-项产品层做成-sysext-时软件包的约束).
#
# Usage: scripts/build-packages.sh OUT VERSION [BUILD]
set -euo pipefail

if [[ $# -lt 2 || $# -gt 3 ]]; then
  echo "usage: $0 OUT VERSION [BUILD]" >&2
  exit 2
fi
mkdir -p "$1"
out=$(cd "$1" && pwd)
version=$2
repo=$(cd "$(dirname "$0")/.." && pwd)
build=${3:-$repo/build}
# A Debian upstream version without a revision (scripts/release-version.sh).
if [[ ! "$version" =~ ^[0-9][A-Za-z0-9.+~]*$ ]]; then
  echo "not a Debian version: $version (see scripts/release-version.sh)" >&2
  exit 2
fi
for binary in anas-api anas-host-agent anas-container-agent; do
  [[ -x "$build/$binary" ]] || { echo "missing $build/$binary: run make build-binaries" >&2; exit 2; }
done
for package in a-nas a-nas-host a-nas-console a-nas-assistant-trial; do
  rm -f -- "$out/${package}"_*.deb
done
# shellcheck source=lib/content.sh
. "$repo/scripts/lib/content.sh"

# The same commit builds the same packages.
SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git -C "$repo" log -1 --format=%ct 2>/dev/null || echo 0)}
export SOURCE_DATE_EPOCH
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

upstream=$(jq -r '.packages | map("\(.name) (>= \(.version))") | join(", ")' "$repo/deploy/upstream/packages.json")
# control PACKAGE CONTENT: the package's control file at this version.
control() {
  sed -e "s/@VERSION@/$version/g" -e "s/@CONTENT@/$2/g" -e "s/@UPSTREAM@/$upstream/g" "$repo/deploy/debian/$1/DEBIAN/control"
}

# build ROOT: the package in ROOT, named as Debian names it, printed.
build() {
  local root=$1 package arch deb
  package=$(sed -n 's/^Package: //p' "$root/DEBIAN/control")
  arch=$(sed -n 's/^Architecture: //p' "$root/DEBIAN/control")
  deb=$out/${package}_${version}_$arch.deb
  find "$root" -type d -exec chmod 0755 {} +
  dpkg-deb --root-owner-group -Zxz --build "$root" "$deb" >/dev/null
  echo "$package $version"
}

for package in a-nas-host a-nas-console; do
  install -d "$work/$package/DEBIAN"
  control "$package" '' >"$work/$package/DEBIAN/control"
  chmod 0644 "$work/$package/DEBIAN/control"
  build "$work/$package"
done

root=$work/a-nas
lib=$root/usr/lib/a-nas
units=$root/usr/lib/systemd/system
share=$root/usr/share/a-nas
install -d "$root/DEBIAN" "$lib/ai/anas_ai" "$lib/search" "$lib/vectors" "$lib/site" "$units" \
  "$root/usr/lib/systemd/system-generators" "$root/usr/lib/sysusers.d" "$root/usr/lib/tmpfiles.d" \
  "$root/usr/sbin" "$share/pam" "$root/usr/share/doc/a-nas"
control a-nas "$(content_packages "$repo" | paste -sd, - | sed 's/,/, /g')" >"$root/DEBIAN/control"
chmod 0644 "$root/DEBIAN/control"
install -m 0755 "$repo"/deploy/debian/a-nas/DEBIAN/{preinst,postinst,prerm,postrm} "$root/DEBIAN/"

# The programs, and the scripts their units and runbooks run.
install -m 0755 "$build/anas-api" "$build/anas-host-agent" "$build/anas-container-agent" \
  "$repo/scripts/install-media-pool.sh" "$repo/scripts/install-screensavers.sh" "$lib/"
install -m 0755 "$repo/scripts/run-kiosk.sh" "$lib/kiosk-launcher"

# The AI Worker (ADR 0016): its code, each model role's manifest and a link
# to the role's file in its content package, the runtime and llama.cpp.
install -m 0644 "$repo"/ai/anas_ai/*.py "$lib/ai/anas_ai/"
for role in "${ai_roles[@]}"; do
  name=${role%%:*} rest=${role#*:}
  manifest=$repo/deploy/models/${rest#*:}.json
  install -m 0644 "$manifest" "$lib/ai/$name.json"
  ln -s "/opt/a-nas/models/$(pin_field "$manifest" sha256)/$(pin_field "$manifest" file)" "$lib/ai/$name.${rest%%:*}"
done
ln -s "/opt/a-nas/ai-runtimes/$(ai_runtime_id "$repo")" "$lib/ai/runtime"
install -m 0644 "$repo/deploy/llama/llama.json" "$lib/ai/"
ln -s "/opt/a-nas/llama/$(pin_field "$repo/deploy/llama/llama.json" sha256)" "$lib/ai/llama"

# Web search (ADR 0030) and the vector index (ADR 0028).
install -m 0644 "$repo"/deploy/search/{settings.yml,searxng.json,anas_search.py} "$lib/search/"
install -m 0755 "$repo/deploy/search/allow-dns.sh" "$lib/search/"
ln -s "/opt/a-nas/search-runtimes/$(search_runtime_id "$repo")" "$lib/search/runtime"
install -m 0644 "$repo"/deploy/qdrant/{qdrant.json,config.yaml} "$lib/vectors/"
install -m 0755 "$repo"/deploy/qdrant/{start.sh,wait-ready.sh} "$lib/vectors/"
ln -s "/opt/a-nas/qdrant/$(pin_field "$repo/deploy/qdrant/qdrant.json" sha256)/qdrant" "$lib/vectors/qdrant"

# The units, and the links that turn on what every A-NAS runs. The local
# console, the container agent and the internet entry are turned on by their
# runbooks or by a-nas-generator.
install -m 0644 "$repo"/deploy/systemd/system/* "$units/"
wants() {
  install -d "$units/$1.wants"
  ln -s "../${3:-$2}" "$units/$1.wants/$2"
}
for unit in anas-host-agent anas-api anas-photos anas-shares anas-family anas-search anas-site; do
  wants multi-user.target "$unit.service"
done
wants sockets.target anas-ai.socket
wants sockets.target anas-ffmpeg.socket
wants sockets.target anas-vectors-proxy@a-nas-photos.socket anas-vectors-proxy@.socket
wants paths.target anas-search-dns.path
# Docker, which a-nas-host depends on, waits for its address pool (ADR 0013).
for unit in docker.service docker.socket; do
  install -D -m 0644 "$repo/deploy/systemd/docker-address-pool.conf" "$units/$unit.d/a-nas-address-pool.conf"
done

# What follows the machine: its settings, and the health check.
install -m 0755 "$repo/deploy/site/a-nas-generator.sh" "$root/usr/lib/systemd/system-generators/a-nas-generator"
install -m 0755 "$repo/deploy/site/apply.sh" "$lib/site/apply"
install -m 0755 "$repo/deploy/site/internet-entry.py" "$lib/site/internet-entry"
install -m 0755 "$repo/deploy/site/a-nas-site.sh" "$root/usr/sbin/a-nas-site"
install -m 0755 "$repo/deploy/site/a-nas-check.sh" "$root/usr/sbin/a-nas-check"
install -m 0644 "$repo"/deploy/sysusers.d/*.conf "$root/usr/lib/sysusers.d/"
install -m 0644 "$repo/deploy/tmpfiles.d/a-nas.conf" "$root/usr/lib/tmpfiles.d/"
install -m 0644 "$repo/deploy/site/site.conf" "$share/site.conf.example"
install -m 0644 "$repo/deploy/caddy/Caddyfile" "$repo/deploy/caddy/Caddyfile.internet" \
  "$repo/deploy/internet/anas-internet.nft" "$share/"
install -m 0644 "$repo/deploy/chromium/policies/managed/a-nas.json" "$share/chromium-policy.json"
install -m 0644 "$repo/deploy/pam/a-nas-kiosk" "$share/pam/"
printf '%s\n' 'A-NAS: https://github.com/zhongwater123/A-NAS' \
  'Each bundled component keeps its own license; see the content packages.' >"$root/usr/share/doc/a-nas/copyright"
chmod 0644 "$root/usr/share/doc/a-nas/copyright"
build "$root"

# The assistant trial (ADR 0024), at this version of a-nas: the spike's
# scripts, which its nas-trial.sh sets up and updates, and its Hermes runtime
# as a content package.
root=$work/a-nas-assistant-trial
install -d "$root/DEBIAN" "$root/opt/a-nas/assistant-trial"
control a-nas-assistant-trial "$(assistant_runtime_package "$repo")" >"$root/DEBIAN/control"
chmod 0644 "$root/DEBIAN/control"
install -m 0755 "$repo"/deploy/debian/a-nas-assistant-trial/DEBIAN/{postinst,prerm} "$root/DEBIAN/"
cp -r "$repo/ai/assistant-spike" "$root/opt/a-nas/assistant-trial/spike"
find "$root/opt/a-nas/assistant-trial/spike" -name __pycache__ -prune -exec rm -rf {} +
# Scripts run as root: root's alone to change, and executable as they start.
while IFS= read -r -d '' file; do
  if [[ "$(head -c 2 "$file")" == '#!' ]]; then chmod 0755 "$file"; else chmod 0644 "$file"; fi
done < <(find "$root/opt/a-nas/assistant-trial/spike" -type f -print0)
build "$root"
