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
for binary in anas-api anas-host-agent install-screensavers.sh; do
  if [[ ! -f "$source_release/$binary" || ! -x "$source_release/$binary" ]]; then
    echo "missing executable: $source_release/$binary" >&2
    exit 2
  fi
done
for unit in anas-api-system.service anas-host-agent-system.service anas-photos-system.service; do
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
for command in btrfs mkfs.btrfs wipefs parted partprobe udevadm smartctl smbpasswd testparm smbcontrol setfacl getfacl; do
  if ! command -v "$command" >/dev/null; then
    echo "missing required host command: $command" >&2
    exit 3
  fi
done
# The LAN Web entry is optional: installing Caddy enables it (ADR 0012).
# Validate before changing anything, so a bad configuration stops the upgrade.
lan_entry=false
if command -v caddy >/dev/null; then
  caddy validate --adapter caddyfile --config "$source_release/Caddyfile" >/dev/null 2>&1 ||
    { echo "LAN Web entry configuration does not validate: $source_release/Caddyfile" >&2; exit 2; }
  lan_entry=true
fi

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
install -d -o root -g root -m 0755 /opt/a-nas/releases
install -d -o a-nas -g a-nas -m 0700 /var/lib/a-nas
install -d -o root -g a-nas -m 0750 /var/lib/a-nas/screensavers
install -d -o root -g a-nas -m 0750 /srv/a-nas /srv/a-nas/data
install -d -o root -g a-nas -m 0750 /etc/a-nas
install -d -o root -g root -m 0755 /etc/samba
install -d -o root -g root -m 0755 /etc/chromium /etc/chromium/policies /etc/chromium/policies/managed
install -d -o root -g root -m 0755 /var/lib/samba /run/samba

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

printf '%s\n' \
  'ANAS_HTTP_ADDR=127.0.0.1:8080' \
  'ANAS_HOSTSTATE_MODE=agent' \
  'ANAS_HOST_AGENT_SOCKET=/run/a-nas/host-agent.sock' \
  'ANAS_FILE_BROKER_SOCKET=/run/a-nas/file-broker.sock' \
  'ANAS_PHOTOS_SOCKET=/run/a-nas-photos/photos.sock' \
  'ANAS_STATE_DIR=/var/lib/a-nas' \
  'ANAS_SCREENSAVER_DIRECTORY=/var/lib/a-nas/screensavers/current' \
  'ANAS_DATA_MOUNT=/srv/a-nas/data' > /etc/a-nas/anas-api.env
# Container management is an optional root-installed capability. Preserve it
# across system-service upgrades only when the typed agent socket is present;
# never grant the Product Service direct access to docker.sock.
if [[ -S /run/a-nas-container/agent.sock ]]; then
  printf 'ANAS_CONTAINERS_MODE=agent\n' >> /etc/a-nas/anas-api.env
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
  'ANAS_DATA_MOUNT=/srv/a-nas/data' \
  "ANAS_SMB_INTERFACE=$smb_interface" > /etc/a-nas/host-agent.env
chown root:root /etc/a-nas/host-agent.env
chmod 0600 /etc/a-nas/host-agent.env

install -o root -g root -m 0644 "$source_release/anas-host-agent-system.service" /etc/systemd/system/anas-host-agent.service
install -o root -g root -m 0644 "$source_release/anas-api-system.service" /etc/systemd/system/anas-api.service
install -o root -g root -m 0644 "$source_release/anas-photos-system.service" /etc/systemd/system/anas-photos.service
install -o root -g root -m 0644 "$source_release/a-nas-chromium-policy.json" /etc/chromium/policies/managed/a-nas.json
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
systemctl enable anas-host-agent.service anas-photos.service anas-api.service
restarted_at=$(date '+%Y-%m-%d %H:%M:%S')
systemctl restart anas-host-agent.service anas-photos.service anas-api.service
if systemctl is-active --quiet anas-kiosk@tty1.service; then
  systemctl restart anas-kiosk@tty1.service
fi
if [[ "$lan_entry" == true ]]; then
  systemctl enable caddy.service
  systemctl reload-or-restart caddy.service
fi

systemctl --no-pager --full status anas-host-agent.service anas-photos.service anas-api.service

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
