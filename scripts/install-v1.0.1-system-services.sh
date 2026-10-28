#!/usr/bin/env bash
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
if [[ $# -ne 3 ]]; then
  echo "usage: $0 SOURCE_RELEASE RELEASE_ID SMB_INTERFACE" >&2
  exit 2
fi

source_release=$(readlink -f -- "$1")
release_id=$2
smb_interface=$3
case "$release_id" in
  *[!A-Za-z0-9._-]*|'') echo "RELEASE_ID contains unsupported characters" >&2; exit 2 ;;
esac
case "$smb_interface" in
  *[!A-Za-z0-9_.:-]*|'') echo "SMB_INTERFACE contains unsupported characters" >&2; exit 2 ;;
esac
if [[ ! -d "$source_release" ]]; then
  echo "source release is not a directory: $source_release" >&2
  exit 2
fi
for binary in anas-api anas-host-agent install-screensavers.sh install-ffmpeg-units.sh; do
  if [[ ! -f "$source_release/$binary" || ! -x "$source_release/$binary" ]]; then
    echo "missing executable: $source_release/$binary" >&2
    exit 2
  fi
done
for unit in anas-api-system.service anas-host-agent-system.service anas-photos-system.service anas-shares-system.service anas-ffmpeg-system.socket anas-ffmpeg-system@.service anas-ffmpeg-system.slice; do
  if [[ ! -f "$source_release/$unit" ]]; then
    echo "missing system unit: $source_release/$unit" >&2
    exit 2
  fi
done
if [[ ! -f "$source_release/a-nas-chromium-policy.json" ]]; then
  echo "missing Chromium policy: $source_release/a-nas-chromium-policy.json" >&2
  exit 2
fi
if [[ ! -f "$source_release/Caddyfile" ]]; then
  echo "missing LAN Web entry configuration: $source_release/Caddyfile" >&2
  exit 2
fi
# The local console runs as an account of its own from the installed release
# (issue #174); enabling it stays a runbook step (operate-local-kiosk.md).
for file in kiosk-launcher anas-kiosk@.service a-nas-kiosk.pam a-nas-kiosk.sysusers; do
  if [[ ! -f "$source_release/$file" ]]; then
    echo "missing local console file: $source_release/$file" >&2
    exit 2
  fi
done
# Local AI is part of every release (ADR 0016): the Worker's code, its units,
# and manifests that pin its models and Python runtime by content. Each model
# has a role: its manifest is ai/ROLE.json and the release links its file as
# ai/ROLE.EXTENSION, where the Worker's unit looks for it.
ai_roles=(model:litertlm detector:onnx face-detector:onnx face-embedder:onnx animal-embedder:onnx)
for file in anas-system.slice anas-ai-system.socket anas-ai-system.service ai/anas_ai/worker.py ai/runtime.json; do
  if [[ ! -f "$source_release/$file" ]]; then
    echo "missing local AI file: $source_release/$file" >&2
    exit 2
  fi
done
for role in "${ai_roles[@]}"; do
  if [[ ! -f "$source_release/ai/${role%%:*}.json" ]]; then
    echo "missing local AI file: $source_release/ai/${role%%:*}.json" >&2
    exit 2
  fi
done
# The vector index (ADR 0028): the Qdrant release its manifest pins by
# content, the settings every instance shares, and the scripts its unit runs.
for file in anas-vectors-system@.service anas-vectors-proxy-system@.socket anas-vectors-proxy-system@.service \
  anas-vectors-system.slice vectors/qdrant.json vectors/config.yaml vectors/start.sh vectors/wait-ready.sh; do
  if [[ ! -f "$source_release/$file" ]]; then
    echo "missing vector index file: $source_release/$file" >&2
    exit 2
  fi
done
# Media and file previews run Debian's FFmpeg / libheif in the worker sandbox.
for command in btrfs mkfs.btrfs wipefs parted partprobe udevadm smartctl smbpasswd testparm smbcontrol setfacl getfacl python3 systemd-sysusers ffmpeg ffprobe heif-thumbnailer; do
  if ! command -v "$command" >/dev/null; then
    echo "missing required host command: $command" >&2
    exit 3
  fi
done
# Each vector index is reached through systemd's socket proxy.
if [[ ! -x /usr/lib/systemd/systemd-socket-proxyd ]]; then
  echo "missing required host command: /usr/lib/systemd/systemd-socket-proxyd" >&2
  exit 3
fi
# MediaPipe links these even on the CPU (Debian packages libegl1 and libgles2).
for library in libEGL.so.1 libGLESv2.so.2; do
  if ! ldconfig -p | grep -Fq "$library ("; then
    echo "missing required host library: $library" >&2
    exit 3
  fi
done

# manifest_field FILE KEY: one field of a JSON manifest.
manifest_field() { python3 -c 'import json, sys; print(json.load(open(sys.argv[1], encoding="utf-8"))[sys.argv[2]])' "$1" "$2"; }
declare -A model_sha model_size model_file model_path
for role in "${ai_roles[@]}"; do
  name=${role%%:*}
  model_sha[$name]=$(manifest_field "$source_release/ai/$name.json" sha256)
  model_size[$name]=$(manifest_field "$source_release/ai/$name.json" sizeBytes)
  model_file[$name]=$(manifest_field "$source_release/ai/$name.json" file)
  if [[ ! "${model_sha[$name]}" =~ ^[0-9a-f]{64}$ || ! "${model_size[$name]}" =~ ^[0-9]+$ ||
    ! "${model_file[$name]}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
    echo "invalid $name manifest: $source_release/ai/$name.json" >&2
    exit 2
  fi
done
runtime_id=$(manifest_field "$source_release/ai/runtime.json" id)
runtime_sha=$(manifest_field "$source_release/ai/runtime.json" sha256)
runtime_python=$(manifest_field "$source_release/ai/runtime.json" python)
if [[ ! "$runtime_id" =~ ^[0-9a-f]{16}$ || ! "$runtime_sha" =~ ^[0-9a-f]{64}$ ]]; then
  echo "invalid AI runtime manifest: $source_release/ai/runtime.json" >&2
  exit 2
fi
system_python=$(python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])')
if [[ "$system_python" != "$runtime_python" ]]; then
  echo "the AI runtime needs Python $runtime_python; this system has $system_python" >&2
  exit 3
fi
# Each model and runtime is kept once by content and linked from every release
# that pins it, so an upgrade or rollback copies neither again.
for role in "${ai_roles[@]}"; do
  name=${role%%:*}
  model_path[$name]=/opt/a-nas/models/${model_sha[$name]}/${model_file[$name]}
  if [[ ! -f "${model_path[$name]}" && ! -f "$source_release/ai/$name.${role#*:}" ]]; then
    echo "missing $name: neither ${model_path[$name]} nor $source_release/ai/$name.${role#*:}" >&2
    exit 2
  fi
done
runtime_path=/opt/a-nas/ai-runtimes/$runtime_id
if [[ ! -d "$runtime_path" && ! -f "$source_release/ai/runtime.tar.gz" ]]; then
  echo "missing AI runtime: neither $runtime_path nor $source_release/ai/runtime.tar.gz" >&2
  exit 2
fi
# Qdrant, like the models, is kept once by content: the official tarball is
# checked, and only its binary is kept, under the tarball's SHA-256.
qdrant_sha=$(manifest_field "$source_release/vectors/qdrant.json" sha256)
qdrant_size=$(manifest_field "$source_release/vectors/qdrant.json" sizeBytes)
qdrant_binary=$(manifest_field "$source_release/vectors/qdrant.json" binary)
if [[ ! "$qdrant_sha" =~ ^[0-9a-f]{64}$ || ! "$qdrant_size" =~ ^[0-9]+$ ||
  ! "$qdrant_binary" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
  echo "invalid Qdrant manifest: $source_release/vectors/qdrant.json" >&2
  exit 2
fi
qdrant_path=/opt/a-nas/qdrant/$qdrant_sha/qdrant
if [[ ! -f "$qdrant_path" && ! -f "$source_release/vectors/qdrant.tar.gz" ]]; then
  echo "missing Qdrant: neither $qdrant_path nor $source_release/vectors/qdrant.tar.gz" >&2
  exit 2
fi
# The LAN Web entry is optional: installing Caddy enables it (ADR 0012).
# Validate before changing anything, so a bad configuration stops the upgrade.
lan_entry=false
if command -v caddy >/dev/null; then
  caddy validate --adapter caddyfile --config "$source_release/Caddyfile" >/dev/null 2>&1 ||
    { echo "LAN Web entry configuration does not validate: $source_release/Caddyfile" >&2; exit 2; }
  lan_entry=true
fi
# The Host Agent moves each of its records out of /var/lib/a-nas when it
# starts, but only a regular file with one link that root owns (issue #176):
# any other record would be unusable after the restart. Stop before changing
# anything; the provisioning runbook says how to check such a file.
for record in space-registry identity-registry viewing-grants app-identities volume-operations; do
  legacy=/var/lib/a-nas/$record.json
  [[ -e "$legacy" || -L "$legacy" ]] || continue
  [[ -e "/var/lib/a-nas-host/$record.json" || -L "/var/lib/a-nas-host/$record.json" ]] && continue
  details=$(stat -c '%u %h %F' -- "$legacy")
  case "$details" in
    '0 1 regular file' | '0 1 regular empty file') ;;
    *)
      echo "$legacy is not a regular file with one link owned by root ($details), so the Host Agent would not move it; check it as the provisioning runbook describes, then run the installer again" >&2
      exit 3
      ;;
  esac
done

getent group a-nas >/dev/null || groupadd --system a-nas
if ! id a-nas >/dev/null 2>&1; then
  useradd --system --gid a-nas --home-dir /var/lib/a-nas --shell /usr/sbin/nologin a-nas
fi
# The photo service owns the photos subvolume under a fixed UID/GID, so a
# reinstalled system disk owns the same photos again (ADR 0011). A-NAS never
# adopts an account or ID it did not create.
photos_id=31000
if getent group a-nas-photos >/dev/null; then
  [[ "$(getent group a-nas-photos | cut -d: -f3)" == "$photos_id" ]] || { echo "group a-nas-photos exists with another GID" >&2; exit 3; }
elif getent group "$photos_id" >/dev/null; then
  echo "GID $photos_id is taken by another group" >&2; exit 3
else
  groupadd --system --gid "$photos_id" a-nas-photos
fi
if getent passwd a-nas-photos >/dev/null; then
  [[ "$(id -u a-nas-photos):$(id -g a-nas-photos)" == "$photos_id:$photos_id" ]] || { echo "user a-nas-photos exists with another UID or GID" >&2; exit 3; }
elif getent passwd "$photos_id" >/dev/null; then
  echo "UID $photos_id is taken by another user" >&2; exit 3
else
  useradd --system --uid "$photos_id" --gid "$photos_id" --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin a-nas-photos
fi
shares_id=31001
if getent group a-nas-shares >/dev/null; then
  [[ "$(getent group a-nas-shares | cut -d: -f3)" == "$shares_id" ]] || { echo "group a-nas-shares exists with another GID" >&2; exit 3; }
elif getent group "$shares_id" >/dev/null; then
  echo "GID $shares_id is taken by another group" >&2; exit 3
else
  groupadd --system --gid "$shares_id" a-nas-shares
fi
if getent passwd a-nas-shares >/dev/null; then
  [[ "$(id -u a-nas-shares):$(id -g a-nas-shares)" == "$shares_id:$shares_id" ]] || { echo "user a-nas-shares exists with another UID or GID" >&2; exit 3; }
elif getent passwd "$shares_id" >/dev/null; then
  echo "UID $shares_id is taken by another user" >&2; exit 3
else
  useradd --system --uid "$shares_id" --gid "$shares_id" --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin a-nas-shares
fi
# The local console's account owns no data, so its ID may differ between
# installs; it must not be able to log in.
install -d -o root -g root -m 0755 /etc/sysusers.d
install -o root -g root -m 0644 "$source_release/a-nas-kiosk.sysusers" /etc/sysusers.d/a-nas-kiosk.conf
systemd-sysusers /etc/sysusers.d/a-nas-kiosk.conf
if [[ "$(getent passwd a-nas-kiosk | cut -d: -f6-7)" != /nonexistent:/usr/sbin/nologin ]]; then
  echo "user a-nas-kiosk exists with a home or shell A-NAS did not give it" >&2
  exit 3
fi
# setgid makes the Product-owned bridge socket inherit the shares-only group.
install -d -o root -g root -m 0755 /etc/tmpfiles.d
printf 'd /run/a-nas-share-files 2750 a-nas a-nas-shares -\n' > /etc/tmpfiles.d/a-nas-shares.conf
chmod 0644 /etc/tmpfiles.d/a-nas-shares.conf
systemd-tmpfiles --create /etc/tmpfiles.d/a-nas-shares.conf
install -d -o root -g root -m 0755 /opt/a-nas/releases
install -d -o a-nas -g a-nas -m 0700 /var/lib/a-nas
install -d -o root -g a-nas -m 0750 /var/lib/a-nas/screensavers
# The Host Agent's records (its StateDirectory); it moves those of older
# releases out of /var/lib/a-nas when it starts (issue #176).
install -d -o root -g root -m 0700 /var/lib/a-nas-host
install -d -o root -g a-nas -m 0750 /srv/a-nas /srv/a-nas/data
install -d -o root -g a-nas -m 0750 /etc/a-nas
install -d -o root -g root -m 0755 /etc/samba
install -d -o root -g root -m 0755 /etc/chromium /etc/chromium/policies /etc/chromium/policies/managed
install -d -o root -g root -m 0755 /var/lib/samba /run/samba
install -d -o root -g root -m 0755 /opt/a-nas/models /opt/a-nas/ai-runtimes /opt/a-nas/qdrant

# Staged content is copied before it is checked, so the staging account cannot
# change it after the check.
incoming=
trap '[[ -z "$incoming" ]] || rm -rf -- "$incoming" "$incoming.tar.gz"' EXIT
for role in "${ai_roles[@]}"; do
  name=${role%%:*}
  [[ -f "${model_path[$name]}" ]] && continue
  staged="$source_release/ai/$name.${role#*:}"
  incoming=$(mktemp -d /opt/a-nas/models/.incoming-XXXXXX)
  cp -- "$staged" "$incoming/${model_file[$name]}"
  if [[ "$(stat -c %s "$incoming/${model_file[$name]}")" != "${model_size[$name]}" ]] ||
    ! printf '%s  %s\n' "${model_sha[$name]}" "$incoming/${model_file[$name]}" | sha256sum --check --quiet - >/dev/null 2>&1; then
    echo "the staged $name does not match its manifest: $staged" >&2
    exit 4
  fi
  chmod 0644 "$incoming/${model_file[$name]}"
  chmod 0755 "$incoming"
  mv -T -- "$incoming" "/opt/a-nas/models/${model_sha[$name]}"
  incoming=
done
if [[ ! -d "$runtime_path" ]]; then
  incoming=$(mktemp -d /opt/a-nas/ai-runtimes/.incoming-XXXXXX)
  cp -- "$source_release/ai/runtime.tar.gz" "$incoming.tar.gz"
  if ! printf '%s  %s\n' "$runtime_sha" "$incoming.tar.gz" | sha256sum --check --quiet - >/dev/null 2>&1; then
    echo "the staged AI runtime does not match its manifest: $source_release/ai/runtime.tar.gz" >&2
    exit 4
  fi
  tar -C "$incoming" --no-same-owner -xzf "$incoming.tar.gz"
  rm -f -- "$incoming.tar.gz"
  chmod 0755 "$incoming"
  mv -T -- "$incoming" "$runtime_path"
  incoming=
fi
if [[ ! -f "$qdrant_path" ]]; then
  incoming=$(mktemp -d /opt/a-nas/qdrant/.incoming-XXXXXX)
  cp -- "$source_release/vectors/qdrant.tar.gz" "$incoming.tar.gz"
  if [[ "$(stat -c %s "$incoming.tar.gz")" != "$qdrant_size" ]] ||
    ! printf '%s  %s\n' "$qdrant_sha" "$incoming.tar.gz" | sha256sum --check --quiet - >/dev/null 2>&1; then
    echo "the staged Qdrant does not match its manifest: $source_release/vectors/qdrant.tar.gz" >&2
    exit 4
  fi
  install -d -m 0700 "$incoming/unpacked"
  tar -C "$incoming/unpacked" --no-same-owner -xzf "$incoming.tar.gz" "$qdrant_binary"
  rm -f -- "$incoming.tar.gz"
  install -o root -g root -m 0755 "$incoming/unpacked/$qdrant_binary" "$incoming/qdrant"
  rm -rf -- "$incoming/unpacked"
  chmod 0755 "$incoming"
  mv -T -- "$incoming" "/opt/a-nas/qdrant/$qdrant_sha"
  incoming=
fi
trap - EXIT

target="/opt/a-nas/releases/$release_id"
if [[ -e "$target" ]]; then
  echo "target release already exists: $target" >&2
  exit 4
fi
temporary="/opt/a-nas/releases/.install-$release_id-$$"
trap 'rm -rf -- "$temporary"' EXIT
install -d -o root -g root -m 0755 "$temporary"
install -o root -g root -m 0755 "$source_release/anas-api" "$temporary/anas-api"
install -o root -g root -m 0755 "$source_release/anas-host-agent" "$temporary/anas-host-agent"
install -o root -g root -m 0755 "$source_release/install-screensavers.sh" "$temporary/install-screensavers.sh"
install -o root -g root -m 0755 "$source_release/install-ffmpeg-units.sh" "$temporary/install-ffmpeg-units.sh"
install -o root -g root -m 0755 "$source_release/kiosk-launcher" "$temporary/kiosk-launcher"
install -d -o root -g root -m 0755 "$temporary/ai" "$temporary/ai/anas_ai"
install -o root -g root -m 0644 "$source_release"/ai/anas_ai/*.py "$temporary/ai/anas_ai/"
install -o root -g root -m 0644 "$source_release/ai/runtime.json" "$temporary/ai/"
for role in "${ai_roles[@]}"; do
  name=${role%%:*}
  install -o root -g root -m 0644 "$source_release/ai/$name.json" "$temporary/ai/"
  ln -s -- "${model_path[$name]}" "$temporary/ai/$name.${role#*:}"
done
ln -s -- "$runtime_path" "$temporary/ai/runtime"
install -d -o root -g root -m 0755 "$temporary/vectors"
install -o root -g root -m 0644 "$source_release/vectors/qdrant.json" "$source_release/vectors/config.yaml" "$temporary/vectors/"
install -o root -g root -m 0755 "$source_release/vectors/start.sh" "$source_release/vectors/wait-ready.sh" "$temporary/vectors/"
ln -s -- "$qdrant_path" "$temporary/vectors/qdrant"
mv -- "$temporary" "$target"
trap - EXIT

# Screen saver videos are long-lived media, not part of a release (ADR 0014):
# install-screensavers.sh keeps them under /var/lib/a-nas/screensavers, where
# "current" names the pool the Product Service plays whatever the release.
if [[ -d "$source_release/screensavers" ]]; then
  echo "ignoring the release's screen saver videos: install them with install-screensavers.sh" >&2
fi
# Installers before issue #49 left each pool in a directory named after its
# release and no "current". Adopt the pool installed last, which they played
# unless a release without videos followed.
if [[ ! -e /var/lib/a-nas/screensavers/current && ! -L /var/lib/a-nas/screensavers/current ]]; then
  legacy_pool=$(find /var/lib/a-nas/screensavers -mindepth 1 -maxdepth 1 -type d -name '[A-Za-z0-9_-]*' \
    ! -name objects ! -name pools -printf '%T@ %p\n' | sort -n | tail -n 1 | cut -d ' ' -f 2-)
  if [[ -n "$legacy_pool" ]]; then
    "$target/install-screensavers.sh" --import "$legacy_pool" ||
      echo "could not adopt the screen saver pool in $legacy_pool; the local console keeps its wallpaper" >&2
  fi
fi

# The administrator terminal stays off until the device's operator turns it on
# (docs/specs/web-terminal.md). This file is rewritten on every release, which
# used to drop that choice, so carry it over.
terminal_enabled=
if grep -qx 'ANAS_TERMINAL=enabled' /etc/a-nas/anas-api.env 2>/dev/null; then
  terminal_enabled=1
fi
printf '%s\n' \
  'ANAS_HTTP_ADDR=127.0.0.1:8080' \
  'ANAS_HOSTSTATE_MODE=agent' \
  'ANAS_HOST_AGENT_SOCKET=/run/a-nas/host-agent.sock' \
  'ANAS_FILE_BROKER_SOCKET=/run/a-nas/file-broker.sock' \
  'ANAS_PHOTOS_SOCKET=/run/a-nas-photos/photos.sock' \
  'ANAS_FFMPEG_SOCKET=/run/a-nas-ffmpeg/ffmpeg.sock' \
  'ANAS_STATE_DIR=/var/lib/a-nas' \
  'ANAS_SCREENSAVER_DIRECTORY=/var/lib/a-nas/screensavers/current' \
  'ANAS_DATA_MOUNT=/srv/a-nas/data' > /etc/a-nas/anas-api.env
# Container management is an optional root-installed capability. Preserve it
# across system-service upgrades only when the typed agent socket is present;
# never grant the Product Service direct access to docker.sock.
if [[ -S /run/a-nas-container/agent.sock ]]; then
  printf 'ANAS_CONTAINERS_MODE=agent\n' >> /etc/a-nas/anas-api.env
fi
if [[ -n "$terminal_enabled" ]]; then
  printf 'ANAS_TERMINAL=enabled\n' >> /etc/a-nas/anas-api.env
fi
chown root:a-nas /etc/a-nas/anas-api.env
chmod 0640 /etc/a-nas/anas-api.env

printf '%s\n' \
  'ANAS_HOST_AGENT_SOCKET=/run/a-nas/host-agent.sock' \
  'ANAS_FILE_BROKER_SOCKET=/run/a-nas/file-broker.sock' \
  'ANAS_STATE_DIR=/var/lib/a-nas' \
  'ANAS_HOST_AGENT_GROUP=a-nas' \
  'ANAS_PHOTO_SESSION_SOCKET=/run/a-nas-sessions/photos.sock' \
  'ANAS_PHOTO_SESSION_GROUP=a-nas-photos' \
  'ANAS_SHARE_SESSION_SOCKET=/run/a-nas-share-sessions/shares.sock' \
  'ANAS_SHARE_SESSION_GROUP=a-nas-shares' \
  'ANAS_DATA_MOUNT=/srv/a-nas/data' \
  "ANAS_SMB_INTERFACE=$smb_interface" > /etc/a-nas/host-agent.env
chown root:root /etc/a-nas/host-agent.env
chmod 0600 /etc/a-nas/host-agent.env

install -o root -g root -m 0644 "$source_release/anas-host-agent-system.service" /etc/systemd/system/anas-host-agent.service
install -o root -g root -m 0644 "$source_release/anas-api-system.service" /etc/systemd/system/anas-api.service
install -o root -g root -m 0644 "$source_release/anas-photos-system.service" /etc/systemd/system/anas-photos.service
install -o root -g root -m 0644 "$source_release/anas-shares-system.service" /etc/systemd/system/anas-shares.service
# Background work gives way to the device's other work (anas.slice).
install -o root -g root -m 0644 "$source_release/anas-system.slice" /etc/systemd/system/anas.slice
install -o root -g root -m 0644 "$source_release/anas-ai-system.socket" /etc/systemd/system/anas-ai.socket
install -o root -g root -m 0644 "$source_release/anas-ai-system.service" /etc/systemd/system/anas-ai.service
# The AI Worker may use every core but two, which stay free for the user's next
# request (anas-ai.service); the cap follows this machine's cores.
cores=$(nproc)
install -d -o root -g root -m 0755 /etc/systemd/system/anas-ai.service.d
printf '# Written by the A-NAS installer for %s cores: all but two.\n[Service]\nCPUQuota=%s%%\n' \
  "$cores" "$((cores > 2 ? (cores - 2) * 100 : 100))" > /etc/systemd/system/anas-ai.service.d/cpu-reserve.conf
chmod 0644 /etc/systemd/system/anas-ai.service.d/cpu-reserve.conf
# One vector index instance per owning service, reached only through its
# proxy socket (ADR 0028).
install -o root -g root -m 0644 "$source_release/anas-vectors-system.slice" /etc/systemd/system/anas-vectors.slice
install -o root -g root -m 0644 "$source_release/anas-vectors-system@.service" /etc/systemd/system/anas-vectors@.service
install -o root -g root -m 0644 "$source_release/anas-vectors-proxy-system@.socket" /etc/systemd/system/anas-vectors-proxy@.socket
install -o root -g root -m 0644 "$source_release/anas-vectors-proxy-system@.service" /etc/systemd/system/anas-vectors-proxy@.service
# A renamed worker must not coexist with old clients using a separate slot
# directory. Stop clients before replacing units; restart after the switch.
for service in anas-api anas-photos anas-shares; do
  if [[ $(systemctl show -p LoadState --value "$service.service") != not-found ]]; then
    systemctl stop "$service.service"
  fi
done
"$target/install-ffmpeg-units.sh" "$source_release"
install -o root -g root -m 0644 "$source_release/a-nas-chromium-policy.json" /etc/chromium/policies/managed/a-nas.json
# The local console's unit, enabled or not, starts this release's launcher.
install -o root -g root -m 0644 "$source_release/anas-kiosk@.service" /etc/systemd/system/anas-kiosk@.service
install -o root -g root -m 0644 "$source_release/a-nas-kiosk.pam" /etc/pam.d/a-nas-kiosk
if [[ "$lan_entry" == true ]]; then
  # Keep the administrator's own configuration the first time A-NAS replaces it.
  if [[ -f /etc/caddy/Caddyfile && ! -e /etc/caddy/Caddyfile.before-a-nas ]] &&
    ! grep -Fqx '# Managed by A-NAS.' /etc/caddy/Caddyfile; then
    cp -p /etc/caddy/Caddyfile /etc/caddy/Caddyfile.before-a-nas
  fi
  install -d -o root -g root -m 0755 /etc/caddy
  install -o root -g root -m 0644 "$source_release/Caddyfile" /etc/caddy/Caddyfile
fi
ln -sfn -- "$target" /opt/a-nas/current
systemctl daemon-reload
systemctl enable --now smbd.service
# A running Worker belongs to the previous release; the next connection starts
# this release's.
systemctl stop anas-ai.service
systemctl enable anas-ai.socket
systemctl restart anas-ai.socket
# Clients reconnect to this release's worker and its shared admission slots.
systemctl enable anas-ffmpeg.socket
systemctl restart anas-ffmpeg.socket
# A running vector index belongs to the previous release; the photo service's
# next connection starts this release's.
systemctl stop 'anas-vectors-proxy@*.service' 'anas-vectors@*.service'
systemctl enable anas-vectors-proxy@a-nas-photos.socket
systemctl restart anas-vectors-proxy@a-nas-photos.socket
systemctl enable anas-host-agent.service anas-photos.service anas-api.service anas-shares.service
restarted_at=$(date '+%Y-%m-%d %H:%M:%S')
systemctl restart anas-host-agent.service anas-photos.service anas-api.service anas-shares.service
if systemctl is-active --quiet anas-kiosk@tty1.service; then
  systemctl restart anas-kiosk@tty1.service
fi
if [[ "$lan_entry" == true ]]; then
  systemctl enable caddy.service
  systemctl reload-or-restart caddy.service
fi

systemctl --no-pager --full status anas-host-agent.service anas-photos.service anas-api.service anas-shares.service anas-ai.socket anas-ffmpeg.socket anas-vectors-proxy@a-nas-photos.socket

# Web files and the terminal run as the signed-in user, so the Host Agent must
# keep CAP_SETUID and CAP_SETGID in its sandbox and prove the switch at startup
# (issue #38). A release that cannot is installed but must be rolled back.
identity_switch=
for _ in $(seq 1 30); do
  identity_switch=$(journalctl -u anas-host-agent.service --since "$restarted_at" -o cat --no-pager |
    grep -Eo 'file broker (identity switch verified|cannot switch to user identities)' | tail -n 1 || true)
  [[ -n "$identity_switch" ]] && break
  sleep 1
done
host_agent_pid=$(systemctl show -p MainPID --value anas-host-agent.service)
cap_eff=$(awk '/^CapEff:/ {print $2}' "/proc/$host_agent_pid/status" 2>/dev/null || true)
if [[ "$identity_switch" != "file broker identity switch verified" || -z "$cap_eff" ]] || (((16#$cap_eff >> 6 & 3) != 3)); then
  echo "anas-host-agent cannot start file workers as users (CapEff=${cap_eff:-unknown}; ${identity_switch:-no identity probe result}); roll back this release" >&2
  exit 5
fi
echo "Open the local A-NAS console to create the first administrator with an account and password."
