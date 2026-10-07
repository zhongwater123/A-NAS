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
for binary in anas-api anas-host-agent; do
  if [[ ! -f "$source_release/$binary" || ! -x "$source_release/$binary" ]]; then
    echo "missing executable: $source_release/$binary" >&2
    exit 2
  fi
done
for unit in anas-api-system.service anas-host-agent-system.service; do
  if [[ ! -f "$source_release/$unit" ]]; then
    echo "missing system unit: $source_release/$unit" >&2
    exit 2
  fi
done
if [[ ! -f "$source_release/a-nas-chromium-policy.json" ]]; then
  echo "missing Chromium policy: $source_release/a-nas-chromium-policy.json" >&2
  exit 2
fi
for command in btrfs mkfs.btrfs wipefs parted partprobe udevadm smartctl smbpasswd testparm smbcontrol; do
  if ! command -v "$command" >/dev/null; then
    echo "missing required host command: $command" >&2
    exit 3
  fi
done

getent group a-nas >/dev/null || groupadd --system a-nas
getent group a-nas-members >/dev/null || groupadd --system a-nas-members
if ! id a-nas >/dev/null 2>&1; then
  useradd --system --gid a-nas --home-dir /var/lib/a-nas --shell /usr/sbin/nologin a-nas
fi
usermod --append --groups a-nas-members a-nas
install -d -o root -g root -m 0755 /opt/a-nas/releases
install -d -o a-nas -g a-nas -m 0700 /var/lib/a-nas
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
mv -- "$temporary" "$target"
trap - EXIT

printf '%s\n' \
  'ANAS_HTTP_ADDR=127.0.0.1:8080' \
  'ANAS_HOSTSTATE_MODE=agent' \
  'ANAS_HOST_AGENT_SOCKET=/run/a-nas/host-agent.sock' \
  'ANAS_STATE_DIR=/var/lib/a-nas' \
  'ANAS_DATA_MOUNT=/srv/a-nas/data' > /etc/a-nas/anas-api.env
chown root:a-nas /etc/a-nas/anas-api.env
chmod 0640 /etc/a-nas/anas-api.env

printf '%s\n' \
  'ANAS_HOST_AGENT_SOCKET=/run/a-nas/host-agent.sock' \
  'ANAS_HOST_AGENT_GROUP=a-nas' \
  'ANAS_DATA_MOUNT=/srv/a-nas/data' \
  "ANAS_SMB_INTERFACE=$smb_interface" > /etc/a-nas/host-agent.env
chown root:root /etc/a-nas/host-agent.env
chmod 0600 /etc/a-nas/host-agent.env

install -o root -g root -m 0644 "$source_release/anas-host-agent-system.service" /etc/systemd/system/anas-host-agent.service
install -o root -g root -m 0644 "$source_release/anas-api-system.service" /etc/systemd/system/anas-api.service
install -o root -g root -m 0644 "$source_release/a-nas-chromium-policy.json" /etc/chromium/policies/managed/a-nas.json
ln -sfn -- "$target" /opt/a-nas/current
systemctl daemon-reload
systemctl enable --now smbd.service
systemctl enable anas-host-agent.service anas-api.service
systemctl restart anas-host-agent.service anas-api.service
if systemctl is-active --quiet anas-kiosk@tty1.service; then
  systemctl restart anas-kiosk@tty1.service
fi

systemctl --no-pager --full status anas-host-agent.service anas-api.service
echo "Open the local A-NAS console to create the first administrator with an account and password."
