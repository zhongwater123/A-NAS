#!/usr/bin/env bash
set -euo pipefail

# Install worker units from a staged release, including the pre-rename layout.
# The caller stops clients first, switches /opt/a-nas/current afterwards, then
# starts the selected socket and clients. This never touches the data volume.
if [[ $EUID -ne 0 || $# -ne 1 ]]; then
  echo "usage (as root): $0 STAGED_RELEASE" >&2
  exit 2
fi
stage=$(readlink -f -- "$1")
worker=ffmpeg
if [[ ! -f "$stage/anas-ffmpeg-system.socket" ]]; then worker=media; fi
for suffix in .socket @.service .slice; do
  if [[ ! -f "$stage/anas-$worker-system$suffix" ]]; then
    echo "missing worker unit: $stage/anas-$worker-system$suffix" >&2
    exit 2
  fi
done

# Stop activation before accepted connections, so a caller cannot recreate an
# old worker that holds a different admission budget during the switch.
for name in media ffmpeg; do
  if [[ $(systemctl show -p LoadState --value "anas-$name.socket") != not-found ]]; then
    systemctl disable --now "anas-$name.socket"
  fi
  systemctl stop "anas-$name@*.service"
  if [[ $(systemctl show -p LoadState --value "system-anas$name.slice") != not-found ]]; then
    systemctl stop "system-anas$name.slice"
  fi
done
other=media
if [[ $worker == media ]]; then other=ffmpeg; fi
rm -f -- "/etc/systemd/system/anas-$other.socket" "/etc/systemd/system/anas-$other@.service" \
  "/etc/systemd/system/system-anas$other.slice" "/etc/tmpfiles.d/a-nas-$other.conf"
rm -f -- /run/a-nas-media/media.sock /run/a-nas-ffmpeg/ffmpeg.sock
install -o root -g root -m 0644 "$stage/anas-$worker-system.socket" "/etc/systemd/system/anas-$worker.socket"
install -o root -g root -m 0644 "$stage/anas-$worker-system@.service" "/etc/systemd/system/anas-$worker@.service"
install -o root -g root -m 0644 "$stage/anas-$worker-system.slice" "/etc/systemd/system/system-anas$worker.slice"

# All clients of a release share these same eight read-only flock slots.
install -d -o root -g root -m 0755 /etc/tmpfiles.d
{
  printf 'd /run/a-nas-%s 0755 root root -\n' "$worker"
  printf 'd /run/a-nas-%s/slots 0750 root a-nas-photos -\n' "$worker"
  for slot in 0 1 2 3 4 5 6 7; do
    printf 'f /run/a-nas-%s/slots/%s 0440 root a-nas-photos -\n' "$worker" "$slot"
  done
} > "/etc/tmpfiles.d/a-nas-$worker.conf"
chmod 0644 "/etc/tmpfiles.d/a-nas-$worker.conf"
systemd-tmpfiles --create "/etc/tmpfiles.d/a-nas-$worker.conf"
if [[ -f /etc/a-nas/anas-api.env ]]; then
  sed -i '/^ANAS_\(MEDIA\|FFMPEG\)_\(SOCKET\|SLOTS\)=/d' /etc/a-nas/anas-api.env
  printf 'ANAS_%s_SOCKET=/run/a-nas-%s/%s.sock\nANAS_%s_SLOTS=/run/a-nas-%s/slots\n' \
    "${worker^^}" "$worker" "$worker" "${worker^^}" "$worker" >> /etc/a-nas/anas-api.env
fi
systemctl daemon-reload
echo "installed anas-$worker.socket; activate it after switching the release"
