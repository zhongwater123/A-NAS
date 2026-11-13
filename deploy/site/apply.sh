#!/usr/bin/env bash
# anas-site.service (ADR 0034): applies this machine's settings where other
# programs read their own configuration, at every boot and after each install.
# What systemd reads comes from a-nas-generator instead.
#
# - SearXNG's secret, made once: it signs web search's thumbnail addresses,
#   and the Product Service reads it to sign them as well (ADR 0030);
# - the fan sensor drivers that find a chip (docs/specs/device-temperatures.md);
# - the LAN Web entry's Caddy site (ADR 0012), and the
#   internet entry's HTTPS site in it, when /etc/a-nas/internet.env turns the
#   entry on (ADR 0032), with the entry's tunnel.
#
# A part that fails leaves the others done and the service failed, which
# a-nas-check reports; a configuration that does not validate is not written.
set -uo pipefail

share=${ANAS_SHARE:-/usr/share/a-nas}
internet_env=${ANAS_INTERNET_ENV:-/etc/a-nas/internet.env}
failed=0
fail() {
  echo "$*" >&2
  failed=1
}
# replace FILE MODE: moves FILE.incoming over FILE unless they are equal, and
# reports whether it did.
replace() {
  if cmp -s -- "$1.incoming" "$1"; then
    rm -f -- "$1.incoming"
    return 1
  fi
  chmod "$2" "$1.incoming"
  mv -f -- "$1.incoming" "$1"
}

install -d -o root -g a-nas -m 0750 /etc/a-nas
if ! grep -Eqx 'SEARXNG_SECRET=[0-9a-f]{64}' /etc/a-nas/search.env 2>/dev/null; then
  (umask 077 && printf 'SEARXNG_SECRET=%s\n' "$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')" >/etc/a-nas/search.env.incoming)
  mv -f -- /etc/a-nas/search.env.incoming /etc/a-nas/search.env
fi
chown root:a-nas /etc/a-nas/search.env
chmod 0640 /etc/a-nas/search.env

# Loading a driver only reads the chip and changes no fan setting. The
# in-kernel drivers of the common chips find no device on a board without one,
# and no driver is forced onto a chip it does not know.
for module in nct6775 it87; do
  modprobe "$module" 2>/dev/null || true
done

# The internet entry, when internet.env turns it on and is valid.
internet=false
if [[ -e "$internet_env" ]]; then
  if ! entry=$(/usr/lib/a-nas/site/internet-entry "$internet_env"); then
    fail "the internet entry stays off until $internet_env is valid (docs/runbooks/enable-internet-access.md)"
  elif ! command -v caddy >/dev/null || ! caddy list-modules 2>/dev/null | grep -x 'caddy.listeners.proxy_protocol' >/dev/null; then
    # grep reads the whole list: stopping at the match would leave caddy
    # writing to a closed pipe.
    fail "the internet entry needs Caddy 2.7 or newer, the one A-NAS's repository carries (deploy/upstream/packages.json)"
  elif ! command -v wg >/dev/null || ! command -v wg-quick >/dev/null || ! command -v nft >/dev/null; then
    fail "the internet entry needs WireGuard and nftables, which a-nas-host depends on: apt-get install --no-install-recommends a-nas-host"
  else
    { read -r internet_host; read -r relay_endpoint; read -r relay_key; read -r relay_address; read -r tunnel_address; } <<<"$entry"
    internet=true
  fi
fi

# Caddy serves the LAN Web entry (ADR 0012); a-nas-host depends on it. Turning
# Caddy off (systemctl disable --now caddy) turns the entry off: its site here
# stays current, and nothing here starts Caddy.
if command -v caddy >/dev/null; then
  install -d -o root -g root -m 0755 /etc/caddy
  # Keep the administrator's own configuration the first time A-NAS replaces it.
  if [[ -f /etc/caddy/Caddyfile && ! -e /etc/caddy/Caddyfile.before-a-nas ]] &&
    ! grep -Fqx '# Managed by A-NAS.' /etc/caddy/Caddyfile; then
    cp -p /etc/caddy/Caddyfile /etc/caddy/Caddyfile.before-a-nas
  fi
  if "$internet"; then
    sed -e "s|@INTERNET_HOST@|$internet_host|g" -e "s|@TUNNEL_ADDRESS@|$tunnel_address|g" \
      -e "s|@RELAY_ADDRESS@|$relay_address|g" "$share/Caddyfile.internet" >/etc/caddy/Caddyfile.incoming
  else
    cp -- "$share/Caddyfile" /etc/caddy/Caddyfile.incoming
  fi
  if ! caddy validate --adapter caddyfile --config /etc/caddy/Caddyfile.incoming >/dev/null 2>&1; then
    rm -f -- /etc/caddy/Caddyfile.incoming
    fail "Caddy's A-NAS site does not validate; /etc/caddy/Caddyfile stays as it was"
  elif replace /etc/caddy/Caddyfile 0644; then
    # Caddy not yet started reads the new file when it starts. Caddy is
    # ordered after this service, so a job for it waits until this one ends:
    # --no-block queues it instead of waiting for it here.
    systemctl --no-block try-reload-or-restart caddy.service || fail "Caddy did not take its new site; see journalctl -u caddy"
  fi
fi

tunnel_changed=false
if "$internet"; then
  # Caddy binds the tunnel's address even while the tunnel is down, so it
  # starts, LAN entry included, whatever the tunnel does.
  sysctl --quiet -w net.ipv6.ip_nonlocal_bind=1 || fail "cannot let Caddy bind the tunnel's address"
  # The tunnel's key is made once and kept, also when the entry is turned off.
  install -d -o root -g root -m 0700 /etc/wireguard
  if [[ ! -s /etc/wireguard/wg-anas.key ]]; then
    (umask 077 && wg genkey >/etc/wireguard/wg-anas.key.incoming) &&
      mv -f -- /etc/wireguard/wg-anas.key.incoming /etc/wireguard/wg-anas.key
  fi
  (umask 077 && printf '%s\n' \
    "# Managed by A-NAS: the internet entry's tunnel to the edge relay (ADR 0032)," \
    "# written from $internet_env. This NAS dials the relay." \
    '[Interface]' \
    "Address = $tunnel_address/64" \
    'PostUp = wg set %i private-key /etc/wireguard/%i.key' \
    '' \
    '[Peer]' \
    "PublicKey = $relay_key" \
    "Endpoint = $relay_endpoint" \
    "AllowedIPs = $relay_address/128" \
    'PersistentKeepalive = 25' >/etc/wireguard/wg-anas.conf.incoming)
  if replace /etc/wireguard/wg-anas.conf 0600; then
    tunnel_changed=true
  fi
  if ! nft --check --file "$share/anas-internet.nft"; then
    fail "the internet entry's firewall table does not load: $share/anas-internet.nft"
  fi
  # A tunnel already up takes its new configuration now; at boot it starts
  # after this service.
  if "$tunnel_changed"; then
    systemctl --no-block try-restart wg-quick@wg-anas.service || fail "the internet entry's tunnel did not restart; see journalctl -u wg-quick@wg-anas"
  fi
  echo "internet entry: https://$internet_host through $relay_endpoint; this NAS's tunnel public key: $(wg pubkey </etc/wireguard/wg-anas.key)"
elif [[ -e /etc/wireguard/wg-anas.conf ]]; then
  # Turned off: undo what turning it on set up, keeping the key.
  systemctl --no-block stop wg-quick@wg-anas.service anas-internet-firewall.service 2>/dev/null || true
  rm -f -- /etc/wireguard/wg-anas.conf
  sysctl --quiet -w net.ipv6.ip_nonlocal_bind=0 || true
  echo "internet entry turned off; the tunnel key stays in /etc/wireguard/wg-anas.key"
fi
exit "$failed"
