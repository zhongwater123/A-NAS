import importlib.util
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
        web = hotspot.configure_proxy(hotspot.tls_host())
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
        self.assertEqual(hotspot.command("ip", "route", "get", peer).stdout, route)
        self.assertEqual(hotspot.command("ip", "route", "show", "default", namespace=namespace).stdout, "")
        http_ports = f"import socket; assert all(socket.socket().connect_ex(({address!r}, p)) != 0 for p in (80, 8080))"
        hotspot.command("python", "-c", http_ports, namespace=namespace)
