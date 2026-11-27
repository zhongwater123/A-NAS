import importlib.util
import json
import os
import shutil
import subprocess
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("phone_hotspot", Path(__file__).with_name("phone-hotspot.py"))
hotspot = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hotspot)


class HotspotTests(unittest.TestCase):
    def test_only_authenticated_stations_are_online(self):
        stations = """02:00:00:00:00:01
flags=[ASSOC][AUTHORIZED]
02:00:00:00:00:02
flags=[ASSOC]
02:00:00:00:00:03
flags=[ASSOC][AUTHORIZED]
"""
        leases = """120 02:00:00:00:00:01 192.168.173.20 phone *
120 02:00:00:00:00:02 192.168.173.21 other-phone *
120 02:00:00:00:00:04 192.168.173.22 left *
99 02:00:00:00:00:03 192.168.173.23 stale *
"""
        self.assertEqual(hotspot.connected_devices(stations, leases, 100), [
            {"mac": "02:00:00:00:00:01", "ip": "192.168.173.20", "name": "phone"},
            {"mac": "02:00:00:00:00:03"},
        ])

    def test_unreadable_leases_preserve_authenticated_stations(self):
        with tempfile.TemporaryDirectory() as directory:
            runtime = Path(directory)
            (runtime / "dnsmasq.leases").mkdir()
            stations = subprocess.CompletedProcess([], 0, stdout="02:00:00:00:00:01\nflags=[ASSOC][AUTHORIZED]\n")
            with patch.multiple(hotspot, RUNTIME=runtime), patch.object(hotspot, "command", return_value=stations):
                hotspot.publish_devices("wlan0", 42)
            self.assertEqual(hotspot.json.loads((runtime / "devices.json").read_text()), [{"mac": "02:00:00:00:00:01"}])

    def test_custom_credentials_use_hostapd_byte_encoding(self):
        with tempfile.TemporaryDirectory() as directory:
            runtime = Path(directory)
            with patch.object(hotspot, "RUNTIME", runtime):
                hotspot.configure_radio("wlan0", "fixture.example.test", "访客 Wi-Fi#", "fixture # only", True)
            ap = (runtime / "hostapd.conf").read_text(encoding="ascii")
            self.assertIn(f"ssid2={'访客 Wi-Fi#'.encode().hex()}\n", ap)
            self.assertRegex(ap, r"(?m)^wpa_psk=[a-f0-9]{64}$")
            self.assertNotIn("fixture # only", ap)
            dhcp = (runtime / "dnsmasq.conf").read_text(encoding="ascii")
            self.assertIn("dhcp-option-force=114,https://fixture.example.test:8443/__anas_hotspot/capport\n", dhcp)
            self.assertIn("dhcp-option=3\n", dhcp)

    def test_wired_vlan_and_bridge_use_their_physical_uplink(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ("eth0", "bond0", "vlan10", "br0"):
                (root / name).mkdir()
                (root / name / "type").write_text("1", encoding="ascii")
            (root / "eth0" / "device").mkdir()
            (root / "bond0" / "lower_eth0").symlink_to(root / "eth0", target_is_directory=True)
            (root / "vlan10" / "lower_bond0").symlink_to(root / "bond0", target_is_directory=True)
            (root / "br0" / "lower_vlan10").symlink_to(root / "vlan10", target_is_directory=True)
            with patch.object(hotspot, "NET", root):
                self.assertTrue(hotspot.wired_default([{"dev": "br0"}]))
                (root / "eth0" / "phy80211").mkdir()
                self.assertFalse(hotspot.wired_default([{"dev": "br0"}]))

    def test_bridge_requires_a_wired_uplink_among_its_non_guest_ports(self):
        tap = {"ifname": "guest0", "linkinfo": {"info_kind": "tun", "info_data": {"type": "tap"}}}
        veth = {"ifname": "peer0", "linkinfo": {"info_kind": "veth"}}
        wired = {"ifname": "eth0"}
        vlan = {"ifname": "vlan10", "linkinfo": {"info_kind": "vlan"}}
        wifi = {"ifname": "radio0"}
        cases = [
            ([wired, tap], True),
            ([vlan, veth], True),
            ([tap, veth], False),
            ([wired, wifi, tap], False),
            ([wired, {"ifname": "unknown0"}], False),
            ([], False),
        ]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ("br0", "eth0", "vlan10", "radio0", "guest0", "peer0", "unknown0"):
                (root / name).mkdir()
                (root / name / "type").write_text("1", encoding="ascii")
            (root / "eth0" / "device").mkdir()
            (root / "vlan10" / "lower_eth0").symlink_to(root / "eth0")
            (root / "radio0" / "device").mkdir()
            (root / "radio0" / "phy80211").mkdir()
            bridge = root / "br0"
            (bridge / "bridge").mkdir()
            for ports, expected in cases:
                with self.subTest(ports=ports):
                    for link in bridge.glob("lower_*"):
                        link.unlink()
                    for port in ports:
                        (bridge / f"lower_{port['ifname']}").symlink_to(root / port["ifname"])
                    listing = subprocess.CompletedProcess([], 0, stdout=json.dumps(ports))
                    with patch.object(hotspot, "NET", root), patch.object(hotspot, "command", return_value=listing):
                        self.assertEqual(hotspot.wired_default([{"dev": "br0"}]), expected)

    def test_hot_unplug_does_not_leave_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            runtime = Path(directory)
            (runtime / "interface.json").write_text('["wlan0","02:00:00:00:00:01"]', encoding="ascii")
            for name in ("connection.json", "hostapd.conf", "dnsmasq.conf"):
                (runtime / name).touch()
            (runtime / "web").mkdir()
            (runtime / "web" / "privkey.pem").touch()
            with (
                patch.object(hotspot, "RUNTIME", runtime),
                patch.object(hotspot, "same_radio", side_effect=[True, False]),
                patch.object(hotspot, "command", side_effect=subprocess.CalledProcessError(1, "iw")),
            ):
                hotspot.cleanup()
            self.assertEqual(list(runtime.iterdir()), [])

    def test_radio_error_keeps_only_the_marker_for_retry(self):
        with tempfile.TemporaryDirectory() as directory:
            runtime = Path(directory)
            (runtime / "interface.json").write_text('["wlan0","02:00:00:00:00:01"]', encoding="ascii")
            (runtime / "connection.json").touch()
            with (
                patch.object(hotspot, "RUNTIME", runtime),
                patch.object(hotspot, "same_radio", return_value=True),
                patch.object(hotspot, "command", side_effect=subprocess.CalledProcessError(1, "iw")),
                self.assertRaises(ExceptionGroup),
            ):
                hotspot.cleanup()
            self.assertEqual(list(runtime.iterdir()), [runtime / "interface.json"])

    @unittest.skipUnless(os.environ.get("ANAS_PHONE_HOTSPOT_NETWORK_TEST") == "1", "needs an isolated root network")
    def test_unit_can_remove_private_caddy_runtime_files(self):
        unit = Path(__file__).parents[1] / "systemd/system/anas-phone-hotspot.service"
        capabilities = next(line for line in unit.read_text(encoding="ascii").splitlines()
                            if line.startswith("CapabilityBoundingSet=")).split("=", 1)[1].split()
        bounding_set = ",".join(["-all", *("+" + cap.removeprefix("CAP_").lower() for cap in capabilities)])
        with tempfile.TemporaryDirectory() as directory:
            runtime = Path(directory)
            data = runtime / "web" / "data"
            data.mkdir(parents=True, mode=0o700)
            user = hotspot.pwd.getpwnam("caddy")
            os.chown(data, user.pw_uid, user.pw_gid)
            (data / "autosave.json").touch()
            cleanup = """
import runpy, sys
from pathlib import Path
clean = runpy.run_path(sys.argv[1])["cleanup"]
clean.__globals__["RUNTIME"] = Path(sys.argv[2])
clean()
"""
            hotspot.command("setpriv", f"--bounding-set={bounding_set}", "--no-new-privs", hotspot.TOOLS["python"],
                            "-B", "-c", cleanup, str(Path(__file__).with_name("phone-hotspot.py")), str(runtime))
            self.assertEqual(list(runtime.iterdir()), [])

    @unittest.skipUnless(os.environ.get("ANAS_PHONE_HOTSPOT_NETWORK_TEST") == "1", "needs an isolated root network")
    def test_https_proxy_survives_host_filter_reload_without_capturing_wired_route(self):
        self.assertNotEqual(os.readlink("/proc/self/ns/net"), os.readlink("/proc/1/ns/net"))
        with tempfile.TemporaryDirectory(dir="/run") as directory:
            root = Path(directory)
            tls, runtime, content = (root / name for name in ("tls", "runtime", "content"))
            for path in (tls, runtime, content):
                path.mkdir()
            root.chmod(0o711)
            (content / "healthz").touch()
            domain = "phone-hotspot.example.test"
            (tls / "domain").write_text(domain, encoding="ascii")
            subprocess.run([
                "/usr/bin/openssl", "req", "-x509", "-newkey", "rsa:2048", "-noenc", "-days", "1",
                "-subj", f"/CN={domain}", "-addext", f"subjectAltName=DNS:{domain}",
                "-keyout", str(tls / "privkey.pem"), "-out", str(tls / "fullchain.pem"),
            ], check=True, capture_output=True, timeout=10)
            shutil.copyfile(tls / "fullchain.pem", tls / "trust.pem")
            with patch.multiple(hotspot, TLS=tls, RUNTIME=runtime):
                self.exercise_isolated_proxy(domain, content)

    def stop_process(self, process):
        if process.poll() is None:
            process.terminate()
        process.wait(timeout=5)

    def exercise_isolated_proxy(self, domain, content):
        address = str(hotspot.ADDRESS)
        hotspot.command("ip", "link", "set", "lo", "up")
        hotspot.command("ip", "link", "add", "test-wired", "type", "veth", "peer", "name", "test-gateway")
        hotspot.command("ip", "addr", "add", "192.0.2.2/24", "dev", "test-wired")
        hotspot.command("ip", "link", "set", "test-wired", "up")
        hotspot.command("ip", "link", "set", "test-gateway", "up")
        hotspot.command("ip", "route", "add", "default", "via", "192.0.2.1", "dev", "test-wired")
        peer = "192.168.173.50"
        route = hotspot.command("ip", "route", "get", peer).stdout
        self.assertIn("via 192.0.2.1", route)
        backend = hotspot.start("python", "-m", "http.server", "8080", "--bind", "127.0.0.1", "--directory", str(content))
        self.addCleanup(self.stop_process, backend)
        holder = hotspot.start("unshare", "--net", "--", hotspot.TOOLS["sleep"], "30")
        self.addCleanup(self.stop_process, holder)
        namespace = hotspot.namespace_pid(holder)
        hotspot.command("ip", "link", "set", "lo", "up", namespace=namespace)
        hotspot.command("ip", "addr", "add", f"{address}/24", "dev", "lo", namespace=namespace)
        hotspot.configure_radio("wlan0", domain, "Sample Wi-Fi", "sample-password", True)
        hotspot.command("dnsmasq", "--test", f"--conf-file={hotspot.RUNTIME}/dnsmasq.conf")
        web = hotspot.configure_proxy(hotspot.tls_host(), True)
        for process in hotspot.start_proxy(web, namespace):
            self.addCleanup(self.stop_process, process)
        check = """
import runpy, sys
from pathlib import Path
probe = runpy.run_path(sys.argv[1])["check_web"]
probe.__globals__["TLS"] = Path(sys.argv[2])
sys.exit(probe(sys.argv[3]))
"""
        args = ("-B", "-c", check, str(Path(__file__).with_name("phone-hotspot.py")), str(hotspot.TLS))
        for _ in range(10):
            probe = hotspot.command("python", *args, domain, namespace=namespace, check=False)
            if probe.returncode == 0:
                break
            time.sleep(0.2)
        self.assertEqual(probe.returncode, 0, probe.stderr)
        (hotspot.TLS / "trust.pem").unlink()
        untrusted = hotspot.command("python", *args, domain, namespace=namespace, check=False)
        self.assertEqual(untrusted.returncode, 2)
        shutil.copyfile(hotspot.TLS / "fullchain.pem", hotspot.TLS / "trust.pem")
        subprocess.run(["/usr/sbin/nft", "flush", "ruleset"], check=True, timeout=5)
        hotspot.command("python", *args, domain, namespace=namespace)
        self.verify_portal(domain, address, namespace)
        self.assertEqual(hotspot.command("ip", "route", "get", peer).stdout, route)
        self.assertEqual(hotspot.command("ip", "route", "show", "default", namespace=namespace).stdout, "")
        http_ports = f"import socket; assert all(socket.socket().connect_ex(({address!r}, p)) != 0 for p in (80, 8080))"
        hotspot.command("python", "-c", http_ports, namespace=namespace)

    def verify_portal(self, domain, address, namespace):
        script = """
import http.client, json, socket, ssl, sys
from urllib.parse import urlsplit
host, address, port, ca, api = sys.argv[1:]
context = ssl.create_default_context(cafile=ca)
def get(path):
    connection = http.client.HTTPSConnection(host, int(port), context=context, timeout=2)
    with socket.create_connection((address, int(port)), timeout=2) as transport:
        connection.sock = context.wrap_socket(transport, server_hostname=host)
        connection.request("GET", path)
        response = connection.getresponse()
        return response.status, response.getheader("Content-Type"), response.getheader("Cache-Control"), response.read()
status, mime, cache, body = get(api)
assert (status, mime, cache) == (200, "application/captive+json", "no-store"), (status, mime, cache)
info = json.loads(body)
assert info["captive"]
welcome = urlsplit(info["user-portal-url"])
assert (welcome.scheme, welcome.hostname, welcome.port) == ("https", host, int(port))
status, mime, cache, body = get(welcome.path)
assert status == 200 and mime.startswith("text/html") and cache == "no-store"
assert f'href="https://{host}:{port}/"'.encode() in body
assert b":root { color-scheme:" in body and "打开 WebOS".encode() in body
assert get("/privkey.pem")[0] == 404
"""
        hotspot.command("python", "-c", script, domain, address, str(hotspot.HTTPS_PORT), str(hotspot.TLS / "trust.pem"),
                        hotspot.PORTAL_API, namespace=namespace)
