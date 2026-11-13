#!/usr/bin/env bash
# Installs or updates the photo scoring module (docs/specs/photo-scoring.md,
# ADR 0037) from a staged directory holding anas-scoring, its units and its
# manifest (make scoring-module). Run as root on a NAS whose A-NAS already has
# the module support (the a-nas-modules and a-nas-module-sessions groups).
# Runbook: docs/runbooks/install-and-remove-photo-scoring.md.
#
# Usage: install.sh STAGED_DIR
set -euo pipefail

staged=${1:?usage: install.sh STAGED_DIR}
[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 2; }
for file in anas-scoring anas-module-scoring.service anas-module-scoring.socket scoring.json; do
  [[ -f "$staged/$file" ]] || { echo "missing $staged/$file" >&2; exit 2; }
done
for group in a-nas-modules a-nas-module-sessions a-nas-users; do
  getent group "$group" >/dev/null || { echo "group $group is missing: install an A-NAS with module support first" >&2; exit 3; }
done
data=/srv/a-nas/data
[[ -s "$data/.a-nas-volume.json" ]] || { echo "the data volume is not mounted at $data" >&2; exit 3; }

# The module's account has a fixed ID in the module range (ADR 0037), so that
# its store keeps its owner across reinstalls.
id=31100
if getent group a-nas-scoring >/dev/null; then
  [[ "$(getent group a-nas-scoring | cut -d: -f3)" == "$id" ]] || { echo "group a-nas-scoring exists with another GID" >&2; exit 3; }
elif getent group "$id" >/dev/null; then
  echo "GID $id is taken by another group" >&2; exit 3
else
  groupadd --system --gid "$id" a-nas-scoring
fi
if getent passwd a-nas-scoring >/dev/null; then
  [[ "$(id -u a-nas-scoring):$(id -g a-nas-scoring)" == "$id:$id" ]] || { echo "user a-nas-scoring exists with another UID or GID" >&2; exit 3; }
elif getent passwd "$id" >/dev/null; then
  echo "UID $id is taken by another user" >&2; exit 3
else
  useradd --system --uid "$id" --gid "$id" --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin a-nas-scoring
fi

# The Shared folder the module reads. Created by root, it takes the Shared
# space's default ACL, so members can put photos in it.
photos="$data/spaces/shared/照片打分"
[[ -d "$data/spaces/shared" && ! -L "$data/spaces/shared" ]] || { echo "no Shared space at $data/spaces/shared" >&2; exit 3; }
if [[ -L "$photos" || (-e "$photos" && ! -d "$photos") ]]; then
  echo "$photos is not a folder" >&2; exit 3
fi
[[ -d "$photos" ]] || mkdir "$photos"

install -d -o root -g root -m 0755 /opt/a-nas/modules/scoring /etc/a-nas/modules
install -o root -g root -m 0755 "$staged/anas-scoring" /opt/a-nas/modules/scoring/anas-scoring.new
mv -f /opt/a-nas/modules/scoring/anas-scoring.new /opt/a-nas/modules/scoring/anas-scoring
install -o root -g root -m 0644 "$staged/anas-module-scoring.service" /etc/systemd/system/anas-module-scoring.service
install -o root -g root -m 0644 "$staged/anas-module-scoring.socket" /etc/systemd/system/anas-module-scoring.socket
install -o root -g root -m 0644 "$staged/scoring.json" /etc/a-nas/modules/scoring.json
systemctl daemon-reload

# The Host Agent makes the store within 30 seconds of the manifest.
store="$data/module-scoring"
for _ in $(seq 1 90); do
  [[ -d "$store" && "$(stat -c '%u %a' "$store")" == "$id 700" ]] && break
  sleep 1
done
[[ "$(stat -c '%u %a' "$store" 2>/dev/null)" == "$id 700" ]] || { echo "the Host Agent did not make $store; is anas-host-agent running?" >&2; exit 4; }

systemctl enable anas-module-scoring.socket anas-module-scoring.service
systemctl restart anas-module-scoring.socket
systemctl restart anas-module-scoring.service
for _ in $(seq 1 30); do
  systemctl is-active --quiet anas-module-scoring.service && [[ -S /run/a-nas-modules/scoring.sock ]] && break
  sleep 1
done
systemctl is-active --quiet anas-module-scoring.service || { journalctl -u anas-module-scoring --no-pager -n 30 >&2; exit 4; }
echo "photo scoring is installed and running"
