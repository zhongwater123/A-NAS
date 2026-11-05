#!/usr/bin/env bash
# Sets up the edge relay of the A-NAS internet entry (ADR 0032) on a Debian or
# Ubuntu server that the company firewall maps a public IP to. The relay holds
# no certificate and no data: the NAS dials it over WireGuard, and HAProxy
# passes public HTTPS for the A-NAS domain through to the NAS untouched, with
# a PROXY header in front. Plain HTTP is answered here with a redirect.
#
# Usage, as root (docs/runbooks/enable-internet-access.md):
#   setup-edge.sh TUNNEL_PREFIX                        closed: tunnel and firewall, no peer, HAProxy off
#   setup-edge.sh TUNNEL_PREFIX DOMAIN NAS_PUBLIC_KEY  open: the NAS as the tunnel's peer, HAProxy on
#
# TUNNEL_PREFIX is the unique local /64 the NAS's /etc/a-nas/internet.env
# names; the relay is its ::1 and the NAS its ::2. Running it again converges
# to the same state, keeping the relay's key. It prints the relay's public
# key, which the NAS's internet.env needs.
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
if [[ $# -ne 1 && $# -ne 3 ]]; then
  echo "usage: setup-edge.sh TUNNEL_PREFIX [DOMAIN NAS_PUBLIC_KEY]" >&2
  exit 2
fi
prefix=$1
domain=${2:-}
nas_key=${3:-}
if ! tunnel=$(python3 -I - "$prefix" <<'PY'
import ipaddress
import sys

try:
    prefix = ipaddress.IPv6Network(sys.argv[1])
except ValueError:
    sys.exit(1)
if prefix.prefixlen != 64 or not prefix.subnet_of(ipaddress.IPv6Network("fd00::/8")):
    sys.exit(1)
print(prefix[1], prefix[2])
PY
); then
  echo "TUNNEL_PREFIX must be a unique local /64 such as fd78:1295:a3fe:1::/64" >&2
  exit 2
fi
read -r relay_address nas_address <<<"$tunnel"
if [[ -n "$domain" ]]; then
  if [[ ! "$domain" =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$ ]]; then
    echo "DOMAIN must be the A-NAS domain in lower case, such as nas.example.com" >&2
    exit 2
  fi
  if [[ ! "$nas_key" =~ ^[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$ ]]; then
    echo "NAS_PUBLIC_KEY must be the key the NAS's installer printed" >&2
    exit 2
  fi
fi

export DEBIAN_FRONTEND=noninteractive
if ! command -v wg >/dev/null || ! command -v haproxy >/dev/null || ! command -v nft >/dev/null; then
  # A source this server cannot reach only warns; the ones it can still serve.
  apt-get update -o Acquire::http::Timeout=10 >/dev/null 2>&1 || true
  apt-get install -y --no-install-recommends wireguard-tools haproxy nftables >/dev/null
fi
# HAProxy, which starts with its package, is off until the NAS is the peer.
if [[ -z "$domain" ]]; then
  systemctl disable --now haproxy.service >/dev/null 2>&1
fi

# Public addresses reach only TCP 80 and 443. Company addresses (the bastion,
# monitoring agents, the NAS's tunnel) are left alone, so nothing else that
# runs here is cut off; only this table is replaced.
install -d -m 0755 /etc/a-nas-edge
cat > /etc/a-nas-edge/firewall.nft <<'NFT'
# Managed by A-NAS: the edge relay (ADR 0032). Public addresses may reach
# only TCP 80 and 443; company addresses are left alone.
table inet a_nas_edge
delete table inet a_nas_edge
table inet a_nas_edge {
	set internal4 {
		type ipv4_addr
		flags interval
		elements = { 10.0.0.0/8, 127.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 }
	}
	chain input {
		type filter hook input priority filter; policy accept;
		ct state established,related accept
		iif "lo" accept
		ip saddr @internal4 accept
		ip6 saddr { ::1, fe80::/10, fc00::/7 } accept
		tcp dport { 80, 443 } accept
		counter drop
	}
}
NFT
cat > /etc/systemd/system/a-nas-edge-firewall.service <<'UNIT'
[Unit]
Description=A-NAS edge relay: firewall table
DefaultDependencies=no
Before=network-pre.target
Wants=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f /etc/a-nas-edge/firewall.nft
ExecStop=/usr/sbin/nft delete table inet a_nas_edge

[Install]
WantedBy=multi-user.target
UNIT
nft --check --file /etc/a-nas-edge/firewall.nft
systemctl daemon-reload
systemctl enable a-nas-edge-firewall.service >/dev/null 2>&1
systemctl restart a-nas-edge-firewall.service

# The tunnel. The relay's key is made once and kept.
install -d -m 0700 /etc/wireguard
if [[ ! -s /etc/wireguard/wg-anas.key ]]; then
  (umask 077 && wg genkey > /etc/wireguard/wg-anas.key)
fi
{
  echo "# Managed by A-NAS: the internet entry's tunnel (ADR 0032). The NAS dials in."
  echo '[Interface]'
  echo "Address = $relay_address/64"
  echo 'ListenPort = 51820'
  echo 'PostUp = wg set %i private-key /etc/wireguard/%i.key'
  if [[ -n "$nas_key" ]]; then
    echo
    echo '[Peer]'
    echo "PublicKey = $nas_key"
    echo "AllowedIPs = $nas_address/128"
  fi
} > /etc/wireguard/wg-anas.conf.incoming
chmod 0600 /etc/wireguard/wg-anas.conf.incoming
if cmp -s /etc/wireguard/wg-anas.conf.incoming /etc/wireguard/wg-anas.conf; then
  rm -f /etc/wireguard/wg-anas.conf.incoming
else
  mv -f /etc/wireguard/wg-anas.conf.incoming /etc/wireguard/wg-anas.conf
  systemctl enable wg-quick@wg-anas.service >/dev/null 2>&1
  systemctl restart wg-quick@wg-anas.service
fi
systemctl enable --now wg-quick@wg-anas.service >/dev/null 2>&1

if [[ -n "$domain" ]]; then
  # Keep the package's own configuration the first time it is replaced.
  if [[ -f /etc/haproxy/haproxy.cfg && ! -e /etc/haproxy/haproxy.cfg.before-a-nas ]] &&
    ! grep -Fqx '# Managed by A-NAS.' /etc/haproxy/haproxy.cfg; then
    cp -p /etc/haproxy/haproxy.cfg /etc/haproxy/haproxy.cfg.before-a-nas
  fi
  cat > /etc/haproxy/haproxy.cfg.incoming <<CFG
# Managed by A-NAS.
# The edge relay of the internet entry (ADR 0032), for $domain.
global
	log /dev/log local0
	chroot /var/lib/haproxy
	stats socket /run/haproxy/admin.sock mode 660 level admin
	user haproxy
	group haproxy
	daemon
	maxconn 4096

defaults
	log global
	option dontlognull
	timeout connect 5s
	timeout client 1h
	timeout server 1h
	timeout client-fin 30s
	timeout server-fin 30s

# Public HTTPS passes through untouched to the NAS, which holds the
# certificate. Only the A-NAS domain is forwarded, each connection behind a
# PROXY header that carries the client's address; an address opening more
# than 60 connections in 10 seconds waits.
frontend public_https
	mode tcp
	bind :443
	option tcplog
	stick-table type ip size 100k expire 1m store conn_rate(10s)
	tcp-request connection track-sc0 src
	tcp-request connection reject if { sc0_conn_rate gt 60 }
	tcp-request inspect-delay 5s
	tcp-request content reject unless { req.ssl_hello_type 1 }
	tcp-request content reject unless { req.ssl_sni -i $domain }
	default_backend nas

backend nas
	mode tcp
	server nas [$nas_address]:443 send-proxy-v2

# Plain HTTP only points browsers at HTTPS: the NAS's port 80 is its LAN
# entry, so nothing is forwarded.
frontend public_http
	mode http
	bind :80
	option httplog
	timeout client 10s
	http-request redirect prefix https://$domain code 301
CFG
  haproxy -c -q -f /etc/haproxy/haproxy.cfg.incoming
  mv -f /etc/haproxy/haproxy.cfg.incoming /etc/haproxy/haproxy.cfg
  systemctl enable haproxy.service >/dev/null 2>&1
  systemctl reload-or-restart haproxy.service
fi

echo "edge relay: tunnel $relay_address, NAS $nas_address; HAProxy $(systemctl is-active haproxy.service || true)"
echo "relay public key: $(wg pubkey < /etc/wireguard/wg-anas.key)"
