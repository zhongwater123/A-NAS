#!/usr/bin/python3
"""Serve the phone hotspot in a private network namespace, without a host route."""

import http.client
import ipaddress
import json
import os
import pwd
import re
import secrets
import shutil
import socket
import ssl
import subprocess
import sys
import time
from pathlib import Path

RUNTIME = Path("/run/anas-phone-hotspot")
TLS = Path("/etc/a-nas/phone-hotspot")
NET = Path("/sys/class/net")
SUBNET = ipaddress.IPv4Network("192.168.173.0/24")
ADDRESS = SUBNET[1]
HTTPS_PORT = 8443
TOOLS = {
    "ip": "/usr/sbin/ip",
    "iw": "/usr/sbin/iw",
    "hostapd": "/usr/sbin/hostapd",
    "hostapd_cli": "/usr/sbin/hostapd_cli",
    "dnsmasq": "/usr/sbin/dnsmasq",
    "caddy": "/usr/bin/caddy",
    "socat": "/usr/bin/socat",
    "unshare": "/usr/bin/unshare",
    "nsenter": "/usr/bin/nsenter",
    "setpriv": "/usr/bin/setpriv",
    "sleep": "/usr/bin/sleep",
    "python": "/usr/bin/python3",
}


class HotspotError(Exception):
    """A failure code safe to show to the administrator."""


def arguments(tool, *args, namespace=None):
    argv = [TOOLS[tool], *args]
    if namespace is not None:
        argv = [TOOLS["nsenter"], f"--net=/proc/{namespace}/ns/net", "--", *argv]
    return argv


def command(tool, *args, namespace=None, check=True):
    return subprocess.run(
        arguments(tool, *args, namespace=namespace), capture_output=True, text=True, timeout=5, check=check
    )


def start(tool, *args, namespace=None, env=None):
    # systemd stops the entire cgroup before ExecStopPost.
    return subprocess.Popen(arguments(tool, *args, namespace=namespace), env=env)


def read(path):
    return path.read_text(encoding="ascii").strip()


def write(name, text):
    # The Host Agent observes these files while the service starts and stops.
    temp = RUNTIME / f".{name}"
    temp.write_text(text, encoding="utf-8")
    temp.replace(RUNTIME / name)


def ethernet(interface, visited=frozenset()):
    if interface in visited or (interface / "phy80211").exists() or (interface / "wireless").exists():
        return False
    try:
        if read(interface / "type") != "1":
            return False
        lowers = [path.resolve(strict=True) for path in interface.glob("lower_*")]
        if lowers:
            return all(ethernet(lower, visited | {interface}) for lower in lowers)
        return (interface / "device").exists()
    except FileNotFoundError:
        return False


def wired_default(routes):
    if not routes:
        return False
    name = min(routes, key=lambda route: route.get("metric", 0)).get("dev")
    return bool(name and name != "lo" and ethernet((NET / name).resolve()))


def idle_radio():
    for interface in sorted(NET.iterdir()):
        if not re.fullmatch(r"[A-Za-z0-9_.-]{1,15}", interface.name):
            continue
        phy = interface / "phy80211"
        if not phy.exists() or (interface / "master").exists():
            continue
        try:
            device = (interface / "device").resolve(strict=True).parent
            if (read(device / "idVendor"), read(device / "idProduct")) != ("0e8d", "7612"):
                continue
            if int(read(interface / "flags"), 16) & 1:
                continue
            if any(
                peer != interface and (peer / "phy80211").resolve() == phy.resolve() for peer in NET.iterdir()
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
            mac = read(interface / "address")
            if re.fullmatch(r"(?:[0-9a-f]{2}:){5}[0-9a-f]{2}", mac):
                return interface, phy.resolve().name, mac
        except (FileNotFoundError, subprocess.CalledProcessError):
            continue
    raise HotspotError("no_idle_radio")


def same_radio(name, mac):
    try:
        return read(NET / name / "address") == mac
    except FileNotFoundError:
        return False


def namespace_pid(holder):
    original = os.readlink("/proc/self/ns/net")
    for _ in range(30):
        if holder.poll() is not None:
            break
        try:
            if os.readlink(f"/proc/{holder.pid}/ns/net") != original:
                return holder.pid
        except FileNotFoundError:
            break
        time.sleep(0.1)
    raise HotspotError("isolation_failed")


def tls_host():
    try:
        host = read(TLS / "domain").lower()
        if len(host) > 253 or "." not in host or not all(
            re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", label) for label in host.split(".")
        ):
            raise HotspotError("tls_unavailable")
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(TLS / "fullchain.pem", TLS / "privkey.pem")
    except (ValueError, OSError) as error:
        raise HotspotError("tls_unavailable") from error
    return host


def configure_radio(name, host, ssid, password):
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
    write("dnsmasq.conf", f"""interface={name}
except-interface=lo
bind-interfaces
no-resolv
no-hosts
local=/#/
host-record={host},{ADDRESS}
dhcp-range={SUBNET[20]},{SUBNET[79]},255.255.255.0,1h
dhcp-option=3
dhcp-option=option:dns-server,{ADDRESS}
leasefile-ro
pid-file={RUNTIME}/dnsmasq.pid
""")


def configure_proxy(host):
    # The existing Caddy system account reads just this instance's config and
    # credentials. The socket is the only way out of the Wi-Fi namespace.
    user = pwd.getpwnam("caddy")
    os.chown(RUNTIME, 0, user.pw_gid)
    RUNTIME.chmod(0o710)
    web = RUNTIME / "web"
    web.mkdir(mode=0o750)
    os.chown(web, 0, user.pw_gid)
    web.chmod(0o750)
    for name in ("fullchain.pem", "privkey.pem"):
        target = web / name
        shutil.copyfile(TLS / name, target)
        os.chown(target, 0, user.pw_gid)
        target.chmod(0o640)
    data = web / "data"
    data.mkdir(mode=0o700)
    os.chown(data, user.pw_uid, user.pw_gid)
    config = web / "Caddyfile"
    config.write_text(f"""{{
    admin off
    auto_https off
}}
https://{host}:{HTTPS_PORT} {{
    bind {ADDRESS}
    tls {web}/fullchain.pem {web}/privkey.pem
    reverse_proxy unix/{web}/upstream.sock
}}
""", encoding="ascii")
    os.chown(config, 0, user.pw_gid)
    config.chmod(0o640)
    return web


def ap_enabled(name, namespace):
    status = command(
        "hostapd_cli", "-p", str(RUNTIME / "ctrl"), "-i", name, "status", namespace=namespace, check=False
    )
    return status.returncode == 0 and "state=ENABLED" in status.stdout.splitlines()


def check_web(host):
    # Connect to the local hotspot address, but authenticate its DNS identity.
    # Never send credentials or turn off certificate verification.
    ca = TLS / "trust.pem"
    connection = http.client.HTTPConnection(host, HTTPS_PORT, timeout=2)
    try:
        context = ssl.create_default_context()
        if ca.is_file():
            context.load_verify_locations(cafile=ca)
        with socket.create_connection((str(ADDRESS), HTTPS_PORT), timeout=2) as transport:
            connection.sock = context.wrap_socket(transport, server_hostname=host)
            connection.request("GET", "/healthz")
            response = connection.getresponse()
            if response.status != 200:
                return 1
            return 0
    except ssl.SSLError:
        return 2
    except (OSError, http.client.HTTPException):
        return 1
    finally:
        connection.close()


def ready():
    address = os.environ["NOTIFY_SOCKET"]
    if address.startswith("@"):
        address = "\0" + address[1:]
    with socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM) as notify:
        notify.sendto(b"READY=1", address)


def publish_connection(host, ssid, password):
    write("connection.json", json.dumps({"ssid": ssid, "password": password, "url": f"https://{host}:{HTTPS_PORT}/"}))


def start_proxy(web, namespace):
    listener = f"UNIX-LISTEN:{web}/upstream.sock,fork,mode=0660,group=caddy"
    relay = start("setpriv", "--bounding-set=-all,+chown", TOOLS["socat"], listener, "TCP4:127.0.0.1:8080")
    proxy = start(
        "setpriv", "--reuid=caddy", "--regid=caddy", "--clear-groups", "--bounding-set=-all", TOOLS["caddy"],
        "run", "--adapter", "caddyfile", "--config", str(web / "Caddyfile"),
        namespace=namespace, env={**os.environ, "XDG_DATA_HOME": str(web / "data"), "XDG_CONFIG_HOME": str(web / "data")},
    )
    return relay, proxy


def serve():
    (RUNTIME / "reason").unlink(missing_ok=True)
    if not all(os.access(path, os.X_OK) for path in TOOLS.values()):
        raise HotspotError("dependencies_missing")
    cleanup()
    if not wired_default(json.loads(command("ip", "-j", "-4", "route", "show", "default").stdout)):
        raise HotspotError("no_wired_uplink")
    radio, phy, mac = idle_radio()
    host = tls_host()
    name = radio.name
    ssid = f"A-NAS-{mac.replace(':', '')[-6:].upper()}"
    password = secrets.token_hex(10)
    configure_radio(name, host, ssid, password)
    web = configure_proxy(host)
    if command("dnsmasq", "--test", f"--conf-file={RUNTIME}/dnsmasq.conf", check=False).returncode:
        raise HotspotError("dhcp_failed")

    holder = start("unshare", "--net", "--", TOOLS["setpriv"], "--bounding-set=-all", TOOLS["sleep"], "infinity")
    namespace = namespace_pid(holder)
    if not same_radio(name, mac) or int(read(radio / "flags"), 16) & 1:
        raise HotspotError("no_idle_radio")
    write("interface.json", json.dumps([name, mac]))
    moved = command("iw", "phy", phy, "set", "netns", str(namespace), check=False)
    if moved.returncode:
        raise HotspotError("isolation_failed")
    # There is no veth, bridge or default route in this namespace. The radio
    # and its connected subnet never add any routes to the host.
    command("ip", "link", "set", "lo", "up", namespace=namespace)
    command("ip", "-4", "addr", "add", f"{ADDRESS}/{SUBNET.prefixlen}", "dev", name, namespace=namespace)
    command("ip", "link", "set", "dev", name, "up", namespace=namespace)

    relay, proxy = start_proxy(web, namespace)
    # Authenticate the running HTTPS endpoint before advertising Wi-Fi.
    for _ in range(5):
        if proxy.poll() is not None or relay.poll() is not None:
            raise HotspotError("web_unavailable")
        probe = command("python", __file__, "check-web", host, namespace=namespace, check=False)
        if probe.returncode == 0:
            break
        if probe.returncode == 2:
            raise HotspotError("tls_unavailable")
        time.sleep(0.5)
    else:
        raise HotspotError("web_unavailable")

    ap = start("setpriv", "--bounding-set=-sys_admin", TOOLS["hostapd"], str(RUNTIME / "hostapd.conf"), namespace=namespace)
    for _ in range(12):
        if ap.poll() is not None:
            raise HotspotError("ap_failed")
        if ap_enabled(name, namespace):
            break
        time.sleep(1)
    else:
        raise HotspotError("ap_failed")
    dhcp = start(
        "setpriv", "--bounding-set=-sys_admin", TOOLS["dnsmasq"], "--keep-in-foreground",
        f"--conf-file={RUNTIME}/dnsmasq.conf", namespace=namespace,
    )
    time.sleep(1)
    if dhcp.poll() is not None:
        raise HotspotError("dhcp_failed")
    publish_connection(host, ssid, password)
    ready()
    while True:
        if holder.poll() is not None:
            raise HotspotError("isolation_failed")
        if proxy.poll() is not None or relay.poll() is not None:
            raise HotspotError("web_unavailable")
        if ap.poll() is not None or not ap_enabled(name, namespace):
            raise HotspotError("ap_failed")
        if dhcp.poll() is not None:
            raise HotspotError("dhcp_failed")
        time.sleep(2)


def restore_radio():
    marker = RUNTIME / "interface.json"
    if not marker.exists():
        return
    name, mac = json.loads(marker.read_text(encoding="utf-8"))
    # systemd has reaped every process sharing the private namespace. The
    # kernel returns the physical radio. Preserve a replacement's state.
    if same_radio(name, mac):
        try:
            status = command("iw", "dev", name, "info").stdout
            if re.search(r"^\s*type AP$", status, re.MULTILINE):
                command("ip", "link", "set", "dev", name, "down")
                command("iw", "dev", name, "set", "type", "managed")
        except subprocess.SubprocessError:
            if same_radio(name, mac):
                raise
    marker.unlink()


def cleanup():
    errors = []
    # Always remove connection details and TLS copies even if the USB radio
    # disappears in the middle of a command. Retain failed radio ownership.
    for name in ("connection.json", ".connection.json", "hostapd.conf", ".hostapd.conf", "dnsmasq.conf",
                 ".dnsmasq.conf", "dnsmasq.pid"):
        try:
            (RUNTIME / name).unlink(missing_ok=True)
        except OSError as error:
            errors.append(error)
    for directory in ("web", "ctrl"):
        try:
            shutil.rmtree(RUNTIME / directory)
        except FileNotFoundError:
            pass
        except OSError as error:
            errors.append(error)
    try:
        restore_radio()
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        errors.append(error)
    if errors:
        raise ExceptionGroup("hotspot cleanup failed", errors)


if __name__ == "__main__":
    if sys.argv[1:2] == ["check-web"]:
        sys.exit(check_web(sys.argv[2]))
    try:
        if sys.argv[1:] == ["cleanup"]:
            cleanup()
        else:
            serve()
    except HotspotError as error:
        write("reason", f"{error}\n")
        print(f"Phone hotspot: {error}", file=sys.stderr)
        sys.exit(1)
    except ExceptionGroup:
        write("reason", "cleanup_failed\n")
        raise
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"Phone hotspot command failed: {error}", file=sys.stderr)
        sys.exit(1)
