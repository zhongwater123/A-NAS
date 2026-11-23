#!/usr/bin/python3
"""Run the optional phone AP. systemd owns both daemons and runs cleanup."""

import http.client
import ipaddress
import json
import os
import re
import secrets
import shutil
import socket
import subprocess
import sys
import time
from pathlib import Path

RUNTIME = Path("/run/anas-phone-hotspot")
NET = Path("/sys/class/net")
SUBNET = ipaddress.IPv4Network("192.168.173.0/24")
ADDRESS = SUBNET[1]
TABLE = "anas_phone_hotspot"
TOOLS = ("ip", "iw", "nft", "hostapd", "hostapd_cli", "dnsmasq")


class HotspotError(Exception):
    """A startup or runtime failure safe to show to the administrator."""


def command(tool, *args, data=None, check=True):
    return subprocess.run(
        [f"/usr/sbin/{tool}", *args], input=data, capture_output=True,
        text=True, timeout=5, check=check,
    )


def write(name, text):
    # Status and connection are read by the Host Agent during start/stop.
    temp = RUNTIME / f".{name}"
    temp.write_text(text, encoding="utf-8")
    temp.replace(RUNTIME / name)


def defaults():
    return json.loads(command("ip", "-j", "-4", "route", "show", "default").stdout)


def wired_default(routes):
    if not routes:
        return False
    name = min(routes, key=lambda route: route.get("metric", 0)).get("dev")
    if not name or name == "lo":
        return False
    interface = NET / name
    return (
        (interface / "device").exists()
        and not (interface / "phy80211").exists()
        and not (interface / "wireless").exists()
        and read(interface / "type") == "1"
    )


def overlaps(routes):
    return any(
        SUBNET.overlaps(ipaddress.IPv4Network(dst, strict=False))
        for route in routes if (dst := route.get("dst", "default")) != "default"
    )


def read(path):
    return path.read_text(encoding="ascii").strip()


def idle_radio():
    for interface in sorted(NET.iterdir()):
        if not re.fullmatch(r"[A-Za-z0-9_.-]{1,15}", interface.name):
            continue
        phy = interface / "phy80211"
        if not phy.exists() or (interface / "master").exists():
            continue
        try:
            # Only the tested dedicated USB adapter is selected automatically.
            # Never take over another chipset or a radio already in use.
            device = (interface / "device").resolve(strict=True).parent
            if (read(device / "idVendor"), read(device / "idProduct")) != ("0e8d", "7612"):
                continue
            if int(read(interface / "flags"), 16) & 1:
                continue
            if any(
                peer != interface and (peer / "phy80211").resolve() == phy.resolve()
                for peer in NET.iterdir()
            ):
                continue
            details = command("iw", "dev", interface.name, "info").stdout
            if not re.search(r"^\s*type managed$", details, re.MULTILINE):
                continue
            modes = command("iw", "phy", phy.resolve().name, "info").stdout
            if not re.search(r"^\s*\* AP$", modes, re.MULTILINE):
                continue
            addresses = json.loads(command("ip", "-j", "addr", "show", "dev", interface.name).stdout)
            if not addresses or addresses[0].get("addr_info"):
                continue
            mac = read(interface / "address").replace(":", "")
            if re.fullmatch(r"[0-9a-f]{12}", mac):
                return interface, read(interface / "ifindex"), f"A-NAS-{mac[-6:].upper()}"
        except (FileNotFoundError, subprocess.CalledProcessError):
            # A USB radio may disappear during enumeration.
            continue
    raise HotspotError("no_idle_radio")


def same_link(name, index):
    try:
        return read(NET / name / "ifindex") == index
    except FileNotFoundError:
        return False


def firewall(index):
    return f"""
table inet {TABLE} {{
  chain input {{
    type filter hook input priority -5; policy accept;
    iif {index} ip protocol udp udp sport 68 udp dport 67 accept
    iif {index} ip saddr {SUBNET} ip daddr {ADDRESS} tcp dport 80 accept
    iif {index} drop
  }}
  chain forward {{
    type filter hook forward priority -5; policy accept;
    iif {index} drop
    oif {index} drop
  }}
}}
"""


def configure(name, ssid, password):
    write("hostapd.conf", f"""interface={name}
driver=nl80211
ctrl_interface={RUNTIME}/ctrl
ssid={ssid}
hw_mode=g
channel=6
ieee80211n=1
wmm_enabled=1
ap_isolate=1
wpa=2
wpa_key_mgmt=WPA-PSK
rsn_pairwise=CCMP
wpa_passphrase={password}
""")
    write("dnsmasq.conf", f"""port=0
interface={name}
except-interface=lo
bind-interfaces
dhcp-range={SUBNET[20]},{SUBNET[79]},255.255.255.0,1h
dhcp-option=3
dhcp-option=6
leasefile-ro
pid-file={RUNTIME}/dnsmasq.pid
""")


def start(name, *args):
    # No daemon is launched in the background outside this unit's cgroup.
    return subprocess.Popen([f"/usr/sbin/{name}", *args])


def ap_enabled(name):
    status = command("hostapd_cli", "-p", str(RUNTIME / "ctrl"), "-i", name, "status", check=False)
    return status.returncode == 0 and "state=ENABLED" in status.stdout.splitlines()


def check_web():
    connection = http.client.HTTPConnection(str(ADDRESS), timeout=4)
    try:
        connection.request("GET", "/healthz")
        if connection.getresponse().status != 200:
            raise HotspotError("web_unavailable")
    except (OSError, http.client.HTTPException) as error:
        raise HotspotError("web_unavailable") from error
    finally:
        connection.close()


def ready():
    address = os.environ["NOTIFY_SOCKET"]
    if address.startswith("@"):
        address = "\0" + address[1:]
    with socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM) as notify:
        notify.sendto(b"READY=1", address)


def serve():
    (RUNTIME / "reason").unlink(missing_ok=True)
    if not all(os.access(f"/usr/sbin/{tool}", os.X_OK) for tool in TOOLS):
        raise HotspotError("dependencies_missing")
    cleanup()
    wired = defaults()
    if not wired_default(wired):
        raise HotspotError("no_wired_uplink")
    if overlaps(json.loads(command("ip", "-j", "-4", "route", "show", "table", "all").stdout)):
        raise HotspotError("subnet_conflict")
    radio, index, ssid = idle_radio()
    name = radio.name
    password = secrets.token_hex(10)
    configure(name, ssid, password)
    if command("dnsmasq", "--test", "--conf-file=" + str(RUNTIME / "dnsmasq.conf"), check=False).returncode:
        raise HotspotError("dhcp_failed")
    # Mark ownership before mutation so ExecStopPost can recover partial startup.
    tables = command("nft", "list", "tables").stdout.splitlines()
    if f"table inet {TABLE}" in tables:
        raise HotspotError("service_failed")
    write("firewall", "owned\n")
    command("nft", "-f", "-", data=firewall(index))
    if not same_link(name, index) or int(read(radio / "flags"), 16) & 1:
        raise HotspotError("no_idle_radio")
    write("interface.json", json.dumps([name, index]))
    command("ip", "-4", "addr", "add", f"{ADDRESS}/{SUBNET.prefixlen}", "dev", name)
    command("ip", "link", "set", "dev", name, "up")
    ap = start("hostapd", str(RUNTIME / "hostapd.conf"))
    for _ in range(12):
        if ap.poll() is not None:
            raise HotspotError("ap_failed")
        if ap_enabled(name):
            break
        time.sleep(1)
    else:
        raise HotspotError("ap_failed")
    dhcp = start("dnsmasq", "--keep-in-foreground", "--conf-file=" + str(RUNTIME / "dnsmasq.conf"))
    time.sleep(1)
    if dhcp.poll() is not None:
        raise HotspotError("dhcp_failed")
    check_web()
    if defaults() != wired:
        raise HotspotError("no_wired_uplink")
    write("connection.json", json.dumps({"ssid": ssid, "password": password, "url": f"http://{ADDRESS}/"}))
    ready()
    while True:
        if ap.poll() is not None or not same_link(name, index) or not ap_enabled(name):
            raise HotspotError("ap_failed")
        if dhcp.poll() is not None:
            raise HotspotError("dhcp_failed")
        time.sleep(2)


def cleanup():
    for name in ("connection.json", "hostapd.conf", "dnsmasq.conf", "dnsmasq.pid", ".connection.json", ".hostapd.conf"):
        (RUNTIME / name).unlink(missing_ok=True)
    shutil.rmtree(RUNTIME / "ctrl", ignore_errors=True)
    interface = RUNTIME / "interface.json"
    if interface.exists():
        name, index = json.loads(interface.read_text(encoding="utf-8"))
        if same_link(name, index):
            command("ip", "link", "set", "dev", name, "down")
            addresses = json.loads(command("ip", "-j", "-4", "addr", "show", "dev", name).stdout)
            if any(item.get("local") == str(ADDRESS) for link in addresses for item in link.get("addr_info", [])):
                command("ip", "-4", "addr", "del", f"{ADDRESS}/{SUBNET.prefixlen}", "dev", name)
            command("iw", "dev", name, "set", "type", "managed")
        interface.unlink()
    if (RUNTIME / "firewall").exists():
        if f"table inet {TABLE}" in command("nft", "list", "tables").stdout.splitlines():
            command("nft", "delete", "table", "inet", TABLE)
        (RUNTIME / "firewall").unlink()


if __name__ == "__main__":
    if sys.argv[1:] == ["cleanup"]:
        cleanup()
    else:
        try:
            serve()
        except HotspotError as error:
            write("reason", f"{error}\n")
            print(f"Phone hotspot: {error}", file=sys.stderr)
            sys.exit(1)
        except (OSError, ValueError, subprocess.SubprocessError) as error:
            # Keep tool output and generated configurations out of web-visible status.
            print(f"Phone hotspot command failed: {error}", file=sys.stderr)
            sys.exit(1)
