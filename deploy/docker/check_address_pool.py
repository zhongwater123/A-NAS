#!/usr/bin/python3
"""Refuse Docker's implicit networks until the site supplies its address pool."""

import ipaddress
import json
import sys
from pathlib import Path


def configured(path: Path) -> bool:
    """Both the default bridge and new networks must have explicit addresses.

    Docker checks the rest of daemon.json itself. This checks the additional
    A-NAS requirement in ADR 0013; a valid Docker config such as {} is unsafe
    here. The site, rather than this check, confirms that the ranges are free.
    """
    try:
        config = json.loads(path.read_text(encoding="utf-8"))
        if not isinstance(config["bip"], str) or "/" not in config["bip"]:
            return False
        bridge = ipaddress.IPv4Interface(config["bip"])
        pools = config["default-address-pools"]
        if not isinstance(pools, list) or not pools:
            return False
        has_ipv4 = False
        for pool in pools:
            if not isinstance(pool["base"], str) or "/" not in pool["base"]:
                return False
            network = ipaddress.ip_network(pool["base"])
            size = pool["size"]
            if type(size) is not int or not network.prefixlen <= size <= network.max_prefixlen:
                return False
            has_ipv4 |= network.version == bridge.version
        return has_ipv4
    except (OSError, ValueError, TypeError, KeyError):
        return False


if __name__ == "__main__":
    if not configured(Path("/etc/docker/daemon.json")):
        print(
            "Docker waits for bip and default-address-pools in /etc/docker/daemon.json "
            "(docs/runbooks/install-container-agent.md).",
            file=sys.stderr,
        )
        sys.exit(1)
