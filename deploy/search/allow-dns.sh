#!/usr/bin/env bash
# Lets the web search service reach the DNS servers /etc/resolv.conf names
# (ADR 0030). anas-search-dns.service runs this as root before the service
# starts and whenever the file changes: the service's unit denies private and
# reserved ranges, and DHCP may hand out DNS servers in them. The list is set
# at runtime, so it lasts until the next boot, when this runs again.
#
# Usage: allow-dns.sh [RESOLV_CONF]
set -euo pipefail

servers=()
while read -r keyword address _; do
  [[ "$keyword" == nameserver ]] || continue
  # An IPv6 server may carry its interface, as in fe80::1%enp2s0.
  address=${address%%\%*}
  if [[ "$address" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ || "$address" =~ ^[0-9A-Fa-f:]+$ ]]; then
    servers+=("$address")
  fi
done < "${1:-/etc/resolv.conf}"
# The empty assignment drops the servers allowed before.
systemctl set-property --runtime anas-search.service IPAddressAllow= "IPAddressAllow=${servers[*]}"
