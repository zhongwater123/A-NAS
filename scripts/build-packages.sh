#!/usr/bin/env bash
# Builds the product's Debian packages into OUT at VERSION, the Debian version
# scripts/release-version.sh gives (the Makefile's VERSION), from this tree and
# the programs make build-binaries leaves in BUILD (ADR 0034):
#
# - a-nas: the programs, the AI Worker's code and the AI assistant's scripts
#   and Hermes policy (ADR 0024) under /usr/lib/a-nas, the systemd units with the links that turn them on, the accounts
#   (sysusers.d), directories (tmpfiles.d), a-nas-generator, anas-site.service,
#   a-nas-site and a-nas-check. It depends on the content packages it links
#   to (scripts/lib/content.sh) and on a-nas-host at its own version.
# - a-nas-host and a-nas-console, which declare what the product needs from
#   Debian (ADR 0031); a-nas-host also needs Docker and Caddy at least at the
#   versions deploy/upstream/packages.json pins (ADR 0015), so installing a
#   release upgrades them to the ones its system test ran, and rolling back
#   leaves them;
# - a-nas-assistant-trial, empty, at the version of a-nas, which it depends on:
#   the assistant's trial is part of a-nas now, and a device that had the trial
#   updates to this package before apt removes it (ADR 0034).
# - a-nas-montage-music, with --montage-music only: the montage center's music
#   analysis (docs/architecture/montage-music.md) at the version of a-nas,
#   which it depends on with its runtime's and models' content packages.
#   While allin1's licence is pending no release builds it.
# - a-nas-docker-policy: the address-pool check and Docker's drop-ins, which
#   stay installed when the product rolls back, as Docker itself does.
# - a-nas-hotspot-radio: optional MediaTek radio dependencies, built for
#   the same target architecture as the rest of the deployment.
#
# OUT then holds this version's packages alone: those of an earlier build go,
# so that the system test and make release find the ones just built.
#
# Files go only to /usr, and the result does not depend on the package's
# scripts, so the same files make the product's system extension
# (docs/research/sysupdate-image-verification.md#第-6-项产品层做成-sysext-时软件包的约束).
#
# Usage: scripts/build-packages.sh [--montage-music] OUT VERSION [BUILD]
set -euo pipefail

montage_music=false
if [[ "${1:-}" == --montage-music ]]; then
  montage_music=true
  shift
fi
if [[ $# -lt 2 || $# -gt 3 ]]; then
  echo "usage: $0 [--montage-music] OUT VERSION [BUILD]" >&2
  exit 2
fi
mkdir -p "$1"
out=$(cd "$1" && pwd)
version=$2
repo=$(cd "$(dirname "$0")/.." && pwd)
build=${3:-$repo/build}
target=${ANAS_TARGET_ARCH:-$(dpkg --print-architecture)}
product_arch=$(sed -n 's/^Architecture: //p' "$repo/deploy/debian/a-nas/DEBIAN/control")
[[ "$target" == "$product_arch" ]] ||
  { echo "the product build supports $product_arch here; build the optional radio package separately for $target" >&2; exit 2; }
# A Debian upstream version without a revision (scripts/release-version.sh).
if [[ ! "$version" =~ ^[0-9][A-Za-z0-9.+~]*$ ]]; then
  echo "not a Debian version: $version (see scripts/release-version.sh)" >&2
  exit 2
fi
for binary in anas-api anas-host-agent anas-container-agent; do
  [[ -x "$build/$binary" ]] || { echo "missing $build/$binary: run make build-binaries" >&2; exit 2; }
done
for package in a-nas a-nas-host a-nas-console a-nas-assistant-trial a-nas-montage-music a-nas-docker-policy a-nas-hotspot-radio; do
  rm -f -- "$out/${package}"_*.deb
done
# shellcheck source=lib/content.sh
. "$repo/scripts/lib/content.sh"
# shellcheck source=lib/assistant-units.sh
. "$repo/scripts/lib/assistant-units.sh"

# The same commit builds the same packages.
SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git -C "$repo" log -1 --format=%ct 2>/dev/null || echo 0)}
export SOURCE_DATE_EPOCH
bash "$repo/scripts/build-hotspot-radio-package.sh" "$out" "$version"
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

root=$work/a-nas-docker-policy
install -d "$root/DEBIAN"
control a-nas-docker-policy '' >"$root/DEBIAN/control"
chmod 0644 "$root/DEBIAN/control"
install -m 0755 "$repo/deploy/debian/a-nas-docker-policy/DEBIAN/postinst" "$root/DEBIAN/"
install -D -m 0755 "$repo/deploy/docker/check_address_pool.py" "$root/usr/lib/a-nas-docker-policy/check-address-pool"
install -D -m 0644 "$repo/deploy/systemd/docker-address-pool.conf" \
  "$root/usr/lib/systemd/system/docker.service.d/a-nas-docker-policy.conf"
install -D -m 0644 "$repo/deploy/systemd/docker-socket-address-pool.conf" \
  "$root/usr/lib/systemd/system/docker.socket.d/a-nas-docker-policy.conf"
build "$root"

root=$work/a-nas
lib=$root/usr/lib/a-nas
units=$root/usr/lib/systemd/system
share=$root/usr/share/a-nas
install -d "$root/DEBIAN" "$lib/ai/anas_ai" "$lib/ai/qv_head" "$lib/ai/anas_videocut/ops" "$lib/ai/anas_videocut/algo" "$lib/ai/anas_videocut/prompts" "$lib/assistant" "$lib/search" "$lib/vectors" "$lib/site" "$units" \
  "$root/usr/lib/systemd/system-generators" "$root/usr/lib/sysusers.d" "$root/usr/lib/tmpfiles.d" \
  "$root/usr/sbin" "$share/pam" "$root/usr/share/doc/a-nas"
control a-nas "$(content_packages "$repo" | paste -sd, - | sed 's/,/, /g')" >"$root/DEBIAN/control"
chmod 0644 "$root/DEBIAN/control"
install -m 0755 "$repo"/deploy/debian/a-nas/DEBIAN/{preinst,postinst,prerm,postrm} "$root/DEBIAN/"
# A device that runs the image gets the product as a system extension, which
# dpkg does not install: the copies run before and after the extension is
# switched (scripts/build-image.sh).
install -d "$lib/release"
install -m 0755 "$repo"/deploy/debian/a-nas/DEBIAN/{preinst,postinst} "$lib/release/"

# The programs, and the scripts their units and runbooks run.
install -m 0755 "$build/anas-api" "$build/anas-host-agent" "$build/anas-container-agent" \
  "$repo/scripts/install-media-pool.sh" "$repo/scripts/install-screensavers.sh" "$lib/"
install -m 0755 "$repo/scripts/run-kiosk.sh" "$lib/kiosk-launcher"
install -m 0755 "$repo/deploy/network/phone-hotspot.py" "$lib/phone-hotspot"

# The AI Worker (ADR 0016): its code and the aesthetic scorer's, each model
# role's manifest and a link to the role's file in its content package, the
# runtime and llama.cpp.
install -m 0644 "$repo"/ai/anas_ai/*.py "$lib/ai/anas_ai/"
install -m 0644 "$repo"/ai/qv_head/*.py "$lib/ai/qv_head/"
# The montage center's compute Worker runs in the same runtime (ADR 0038):
# worker.py, its operations (ops/), the pure computation (algo/) and the
# planning templates (prompts/).
install -m 0644 "$repo"/ai/anas_videocut/*.py "$lib/ai/anas_videocut/"
install -m 0644 "$repo"/ai/anas_videocut/ops/*.py "$lib/ai/anas_videocut/ops/"
install -m 0644 "$repo"/ai/anas_videocut/algo/*.py "$lib/ai/anas_videocut/algo/"
install -m 0644 "$repo"/ai/anas_videocut/prompts/*.md "$lib/ai/anas_videocut/prompts/"
for role in "${ai_roles[@]}" "${aesthetic_role[@]}"; do
  name=${role%%:*} rest=${role#*:}
  manifest=$repo/deploy/models/${rest#*:}.json
  install -m 0644 "$manifest" "$lib/ai/$name.json"
  ln -s "$(model_link "$manifest")" "$lib/ai/$name.${rest%%:*}"
done
ln -s "/opt/a-nas/ai-runtimes/$(ai_runtime_id "$repo")" "$lib/ai/runtime"
install -m 0644 "$repo/deploy/llama/llama.json" "$lib/ai/"
ln -s "/opt/a-nas/llama/$(pin_field "$repo/deploy/llama/llama.json" sha256)" "$lib/ai/llama"

# The AI assistant (ADR 0024): the scripts its units run, the MCP servers and
# the gateway, Hermes's managed policy and the system skills, the files its
# sandbox binds over Hermes's home, and a link to the Hermes runtime. The Host
# Agent writes each member's own settings (/var/lib/a-nas-host/assistant).
assistant=$repo/ai/assistant
install -d "$lib/assistant/managed" "$lib/assistant/empty"
for file in "$assistant"/*.sh "$assistant"/*.py; do
  install -m 0755 "$file" "$lib/assistant/"
done
install -m 0644 "$assistant/member-skill.md" "$lib/assistant/"
install -m 0644 "$assistant/managed-config.yaml" "$lib/assistant/managed/config.yaml"
# The planning instance's policy adds the montage center's planning server,
# which no other unit starts, and reviews every planning
# (docs/architecture/montage-planning-session.md); the fixed prompt templates
# the planner fills go beside it.
install -d "$lib/assistant/managed-plan" "$lib/assistant/videocut-prompts"
derive_plan_policy "$assistant/managed-config.yaml" "$assistant/montage-mcp.yaml" "$lib/assistant/managed-plan/config.yaml"
install -m 0644 "$repo"/ai/anas_videocut/prompts/*.md "$lib/assistant/videocut-prompts/"
install -m 0644 /dev/null "$lib/assistant/empty.env"
# Members' sandboxes read the skills, whatever the builder's umask.
cp -r "$assistant/skills" "$lib/assistant/skills"
find "$lib/assistant/skills" -type d -exec chmod 0755 {} +
find "$lib/assistant/skills" -type f -exec chmod 0644 {} +
ln -s "/opt/a-nas/assistant-runtimes/spike" "$lib/assistant/runtime"

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
# The assistant's scheduled tasks, its curator and its planning instance, made
# from its sandbox.
derive_assistant_units "$units"
wants() {
  install -d "$units/$1.wants"
  ln -s "../${3:-$2}" "$units/$1.wants/$2"
}
for unit in anas-host-agent anas-api anas-photos anas-shares anas-family anas-videocut anas-workspace anas-search anas-site \
  anas-assistant-gateway anas-assistant-cron-waker anas-videocut-planner; do
  wants multi-user.target "$unit.service"
done
wants sockets.target anas-ai.socket
wants sockets.target anas-photos-grants.socket
wants sockets.target anas-videocut-compute.socket
wants sockets.target anas-videocut-assistant.socket
wants sockets.target anas-ffmpeg.socket
wants sockets.target anas-photos-assistant.socket
wants sockets.target anas-companion-assistant.socket
wants sockets.target anas-vectors-proxy@a-nas-photos.socket anas-vectors-proxy@.socket
wants paths.target anas-search-dns.path
wants paths.target anas-update.path
wants timers.target anas-update.timer
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

# The assistant's trial, now part of a-nas: an empty package at this version,
# so that a device with the trial installed updates it (its scripts there
# allow it) and a-nas's postinst takes the trial over; apt removes it with the
# next release that does not list it (internal/systemupdate).
root=$work/a-nas-assistant-trial
install -d "$root/DEBIAN"
control a-nas-assistant-trial '' >"$root/DEBIAN/control"
chmod 0644 "$root/DEBIAN/control"
build "$root"

# The montage center's music analysis (docs/architecture/montage-music.md):
# the Worker's code, its units, each model's manifest and links to the
# content packages of its models and runtime.
if "$montage_music"; then
  root=$work/a-nas-montage-music
  lib=$root/usr/lib/a-nas/montage-music
  units=$root/usr/lib/systemd/system
  install -d "$root/DEBIAN" "$lib/anas_montage_music" "$units/sockets.target.wants" "$root/usr/share/doc/a-nas-montage-music"
  control a-nas-montage-music "$(montage_music_packages "$repo" | paste -sd, - | sed 's/,/, /g')" >"$root/DEBIAN/control"
  chmod 0644 "$root/DEBIAN/control"
  install -m 0755 "$repo"/deploy/debian/a-nas-montage-music/DEBIAN/{postinst,prerm} "$root/DEBIAN/"
  install -m 0644 "$repo"/ai/anas_montage_music/*.py "$lib/anas_montage_music/"
  for role in "${montage_music_roles[@]}"; do
    manifest=$repo/deploy/models/${role#*:}.json
    install -m 0644 "$manifest" "$lib/${role%%:*}.json"
    ln -s "/opt/a-nas/models/$(pin_field "$manifest" sha256)" "$lib/${role%%:*}"
  done
  ln -s "/opt/a-nas/montage-music-runtimes/$(montage_music_runtime_id "$repo")" "$lib/runtime"
  install -m 0644 "$repo"/deploy/montage-music/anas-videocut-music.{socket,service} "$units/"
  ln -s ../anas-videocut-music.socket "$units/sockets.target.wants/anas-videocut-music.socket"
  printf '%s\n' 'A-NAS: https://github.com/zhongwater123/A-NAS' \
    "allin1's and Demucs' weights and the runtime keep their own licenses; see their content packages." \
    >"$root/usr/share/doc/a-nas-montage-music/copyright"
  chmod 0644 "$root/usr/share/doc/a-nas-montage-music/copyright"
  build "$root"
fi
