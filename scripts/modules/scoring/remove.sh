#!/usr/bin/env bash
# Removes the photo scoring module's program, units, manifest and account
# (ADR 0037). It leaves the module's store, /srv/a-nas/data/module-scoring,
# and the Shared folder 照片打分 with the photos: export first, then delete the
# store by docs/runbooks/install-and-remove-photo-scoring.md. Run as root.
set -euo pipefail

[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 2; }

for unit in anas-module-scoring.service anas-module-scoring.socket; do
  systemctl disable --now "$unit" 2>/dev/null || true
done
rm -f /etc/systemd/system/anas-module-scoring.service /etc/systemd/system/anas-module-scoring.socket
rm -f /etc/a-nas/modules/scoring.json
rmdir /etc/a-nas/modules 2>/dev/null || true
rm -rf /opt/a-nas/modules/scoring
rmdir /opt/a-nas/modules 2>/dev/null || true
systemctl daemon-reload
systemctl reset-failed anas-module-scoring.service anas-module-scoring.socket 2>/dev/null || true
rm -f /run/a-nas-modules/scoring.sock
rmdir /run/a-nas-modules 2>/dev/null || true
if getent passwd a-nas-scoring >/dev/null; then
  userdel a-nas-scoring
fi
if getent group a-nas-scoring >/dev/null; then
  groupdel a-nas-scoring
fi
echo "photo scoring is removed; its store /srv/a-nas/data/module-scoring is kept until you delete it by the runbook"
