#!/usr/bin/env bash
# Runs as root inside the disposable systemd container (anas-systemd:trixie): two members
# and their spaces made the way the Host Agent makes them (assistant-layout.sh spaces), the
# Hermes runtime, then the assistant itself (assistant-install.sh) and the probe's files.
# ai/assistant-spike/system-test.sh does the same on the real installer's accounts.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
# scripts/build-system-test-image.sh now includes them; an image built before needs them.
command -v socat >/dev/null && command -v rg >/dev/null && command -v tini >/dev/null ||
  { apt-get update -qq && apt-get install -y -qq socat ripgrep tini >/dev/null; }
mkdir -p /opt/a-nas/assistant-runtimes
tar -C /opt/a-nas/assistant-runtimes -xzf /spike-out/runtime.tar.gz

# Accounts as the Host Agent creates them (ADR 0008): the fixed groups, then each account
# with its own same-named group and a-nas-users.
groupadd -g 20000 a-nas-users
groupadd -g 20001 a-nas-admins
for u in alice:20100 bob:20101; do
  name=${u%%:*} id=${u##*:}
  groupadd -g "$id" "$name"
  useradd -u "$id" -g "$id" -G a-nas-users -M -d /nonexistent -s /usr/sbin/nologin "$name"
done

d=/srv/a-nas/data
install -d -m 0755 /srv/a-nas "$d"
bash /spike/assistant-layout.sh spaces "$d" alice bob
install -d -o root -g root -m 0700 "$d/.a-nas-snapshots/snap-1"
echo snapshot-copy > "$d/.a-nas-snapshots/snap-1/secret.txt"

product=()
if [[ -n "${SPIKE_PRODUCT_UID:-}" ]]; then
  # The UI prototype's product service runs on the host as this UID.
  useradd -u "$SPIKE_PRODUCT_UID" -M -d /nonexistent -s /usr/sbin/nologin anas-product
  product=(PRODUCT_USER=anas-product)
fi
if [[ -x /spike-photos/photos-spike ]]; then
  # The photo service as its own identity (ADR 0011) with its assistant socket (#113),
  # seeded with photos for alice, bob and the shared library. Its socket directories are
  # private to it; the assistant's unit binds the socket itself into the sandbox.
  useradd -r -u 31000 -U -M -d /nonexistent -s /usr/sbin/nologin a-nas-photos
  install -d -o a-nas-photos -g a-nas-photos -m 0700 /var/lib/photos-spike /run/a-nas-photos /run/a-nas-photos-assistant
  systemd-run --unit photos-spike --quiet -p User=a-nas-photos -p Group=a-nas-photos /spike-photos/photos-spike     /var/lib/photos-spike /run/a-nas-photos/photos.sock /run/a-nas-photos-assistant/assistant.sock
  for _ in $(seq 1 240); do
    journalctl -u photos-spike -o cat --no-pager | grep -q photos-ready && break
    systemctl is-active -q photos-spike || { journalctl -u photos-spike -o cat --no-pager | tail -5; exit 1; }
    sleep 1
  done
fi
env "${product[@]}" bash /spike/assistant-install.sh alice bob
bash /spike/seed.sh "$d"
echo setup-done
