#!/usr/bin/env bash
# Runs as root inside the disposable systemd container (anas-systemd:trixie): two members
# and their spaces made the way the Host Agent makes them (assistant-layout.sh spaces), the
# Hermes runtime, then the assistant itself (assistant-install.sh) and the probe's files.
# ai/assistant-spike/system-test.sh does the same on the real installer's accounts.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq && apt-get install -y -qq socat ripgrep tini >/dev/null
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
env "${product[@]}" bash /spike/assistant-install.sh alice bob
bash /spike/seed.sh "$d"
echo setup-done
