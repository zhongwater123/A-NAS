#!/bin/bash
# systemd generator of the a-nas package (ADR 0034): the systemd settings that
# follow this machine rather than the release. systemd runs it at boot and on
# every daemon-reload, so they never depend on a package script.
#
# - smbd waits for the SMB interface's IPv4 address
#   (docs/investigations/2026-11-04-smbd-starts-before-dhcp-address.md);
# - the AI Worker's CPU and memory caps follow this machine's cores and
#   memory;
# - /etc/a-nas/internet.env turns the internet entry on (ADR 0032): its
#   firewall table and tunnel start at boot, and the Product Service listens
#   for it.
#
# It writes only into the directory systemd gives it, reads site.conf without
# running it, and leaves out a setting whose value is not valid.
set -u

out=${1:?usage: a-nas-generator NORMAL_DIR [EARLY_DIR LATE_DIR]}
site=${ANAS_SITE_CONF:-/etc/a-nas/site.conf}
internet=${ANAS_INTERNET_ENV:-/etc/a-nas/internet.env}
units=${ANAS_UNIT_DIR:-/usr/lib/systemd/system}

# site_value KEY: the last value of KEY in site.conf.
site_value() {
  [[ -r "$site" ]] || return 0
  sed -n "s/^$1=//p" "$site" | tail -n 1
}

smb_interface=$(site_value ANAS_SMB_INTERFACE)
if [[ "$smb_interface" =~ ^[A-Za-z0-9_.:-]+$ ]]; then
  # smbd listens only on the addresses its interfaces have when it starts
  # (bind interfaces only), and at boot a DHCP address can arrive after
  # smbd's turn. So smbd waits up to 20 s for the interface's IPv4 address;
  # without one, the start fails and systemd tries again 5 s later, until it
  # has one. Units ordered after smbd, the Host Agent among them, start after
  # the first try. Restart= also brings smbd back after a crash.
  mkdir -p "$out/smbd.service.d"
  cat >"$out/smbd.service.d/a-nas-lan-address.conf" <<EOF
# Written by a-nas-generator for the SMB interface $smb_interface (site.conf).
[Service]
ExecStartPre=/bin/sh -c 'timeout 20 sh -c "until ip -4 -o address show dev $smb_interface scope global 2>/dev/null | grep -q inet; do sleep 1; done" || { echo "$smb_interface has no IPv4 address after 20 s; smbd waits for one because it listens only on the addresses present when it starts" >&2; exit 1; }'
Restart=on-failure
RestartSec=5s
EOF
fi

# The AI Worker may use every core but two, which stay free for the user's
# next request. Its memory has no budget: background work loads a model only
# while 1 GiB stays free (anas-ai.service). The cap, all of this machine's
# memory but 2.5 GiB and at least 1 GiB, only stops a runaway.
cores=$(nproc 2>/dev/null || echo 1)
memory_kib=$(awk '$1 == "MemTotal:" { print $2 }' /proc/meminfo 2>/dev/null)
mkdir -p "$out/anas-ai.service.d"
{
  echo "# Written by a-nas-generator for $cores cores and ${memory_kib:-unknown} KiB of memory."
  echo '[Service]'
  echo "CPUQuota=$((cores > 2 ? (cores - 2) * 100 : 100))%"
  if [[ "$memory_kib" =~ ^[0-9]+$ ]]; then
    echo "MemoryMax=$((memory_kib > 3670016 ? memory_kib - 2621440 : 1048576))K"
  fi
} >"$out/anas-ai.service.d/a-nas-reserve.conf"

# The internet entry, when internet.env is valid; anas-site.service writes
# Caddy's site and the tunnel's configuration from the same file.
if [[ -e "$internet" ]] && entry=$(/usr/lib/a-nas/site/internet-entry "$internet" 2>/dev/null); then
  host=$(sed -n 1p <<<"$entry")
  mkdir -p "$out/anas-api.service.d" "$out/multi-user.target.wants"
  # The Product Service's own listener for the entry, which Caddy's HTTPS
  # site proxies to.
  printf '# Written by a-nas-generator from %s.\n[Service]\nEnvironment=ANAS_INTERNET_HTTP_ADDR=127.0.0.1:8081\nEnvironment=ANAS_INTERNET_HOST=%s\n' \
    "$internet" "$host" >"$out/anas-api.service.d/a-nas-internet.conf"
  ln -sf "$units/anas-internet-firewall.service" "$out/multi-user.target.wants/anas-internet-firewall.service"
  if [[ -e "$units/wg-quick@.service" ]]; then
    ln -sf "$units/wg-quick@.service" "$out/multi-user.target.wants/wg-quick@wg-anas.service"
  fi
fi
exit 0
