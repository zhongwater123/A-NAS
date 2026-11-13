#!/usr/bin/python3 -I
"""Reads /etc/a-nas/internet.env, which turns the internet entry on (ADR 0032).

Prints the entry's host, the relay's endpoint and WireGuard public key, and
the relay's and this NAS's tunnel addresses, one per line, or exits 1 with
the reason when a line is unknown or a value is not valid. It reads the file
without running it.
"""

import ipaddress
import re
import sys

PATH = sys.argv[1] if len(sys.argv) > 1 else "/etc/a-nas/internet.env"
KEYS = {
    "ANAS_INTERNET_HOST",
    "ANAS_INTERNET_RELAY_ENDPOINT",
    "ANAS_INTERNET_RELAY_PUBLIC_KEY",
    "ANAS_INTERNET_TUNNEL_PREFIX",
}


def fail(reason):
    print(f"{PATH}: {reason}", file=sys.stderr)
    sys.exit(1)


values = {}
with open(PATH, encoding="utf-8") as lines:
    for line in lines:
        line = line.rstrip("\n")
        if not line or line.startswith("#"):
            continue
        key, _, value = line.partition("=")
        if key not in KEYS:
            fail(f"unknown line: {key}")
        values[key] = value

host = values.get("ANAS_INTERNET_HOST", "")
# A domain in lower-case ASCII, as browsers send it; an internationalized name
# is written in its xn-- form.
if not re.fullmatch(r"([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?", host):
    fail("ANAS_INTERNET_HOST must be the entry's domain in lower case, such as nas.example.com")
key = values.get("ANAS_INTERNET_RELAY_PUBLIC_KEY", "")
if not re.fullmatch(r"[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=", key):
    fail("ANAS_INTERNET_RELAY_PUBLIC_KEY must be the relay's WireGuard public key")
endpoint = values.get("ANAS_INTERNET_RELAY_ENDPOINT", "")
# The tunnel's addresses come from a unique local /64: the relay is ::1, this
# NAS ::2.
try:
    prefix = ipaddress.IPv6Network(values.get("ANAS_INTERNET_TUNNEL_PREFIX", ""))
    address, colon, port = endpoint.rpartition(":")
    ipaddress.ip_address(address.removeprefix("[").removesuffix("]"))
    valid = (
        prefix.prefixlen == 64
        and prefix.subnet_of(ipaddress.IPv6Network("fd00::/8"))
        and colon
        and port.isdigit()
        and 1 <= int(port) <= 65535
    )
except ValueError:
    valid = False
if not valid:
    fail(
        "ANAS_INTERNET_TUNNEL_PREFIX must be a unique local /64 (fdXX:XXXX:XXXX:XXXX::/64) "
        "and ANAS_INTERNET_RELAY_ENDPOINT the relay's address:port"
    )
print(host, endpoint, key, prefix[1], prefix[2], sep="\n")
