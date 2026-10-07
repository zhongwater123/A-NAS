"""Exercise the release probes against existing binaries and local Docker.

Only GET requests are used. State and media fixtures live in a temporary dir.
"""
import contextlib
import http.client
import importlib.util
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("probe", ROOT / "scripts/verify-container-deployment.py")
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)

with tempfile.TemporaryDirectory(prefix="anas-probe-") as folder:
    base = Path(folder)
    media = base / "fixture.mp4"
    media.write_bytes(b"\0" * 4096)
    sock = str(base / "agent.sock")
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    with contextlib.ExitStack() as stack:
        def launch(name, overrides):
            env = dict(os.environ)
            for key in list(env):
                if key.startswith("ANAS_") or key.startswith("DOCKER_"):
                    del env[key]
            env.update(overrides)
            log = stack.enter_context(open(base / (name + ".log"), "w+"))
            process = subprocess.Popen([str(ROOT / "build" / name)], env=env,
                                       stdout=log, stderr=subprocess.STDOUT)
            def stop():
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
            stack.callback(stop)
            return process, log

        agent, agent_log = launch("anas-container-agent", {
            "ANAS_CONTAINER_AGENT_SOCKET": sock,
            "STATE_DIRECTORY": str(base / "apps-state"),
            "ANAS_APP_DATA_ROOT": str(base / "apps"),
            "ANAS_SHARED_DATA_ROOT": str(base / "shared"),
            "DOCKER_HOST": "unix:///var/run/docker.sock",
        })
        api, api_log = launch("anas-api", {
            "ANAS_HTTP_ADDR": f"127.0.0.1:{port}",
            "ANAS_HOSTSTATE_MODE": "fake",
            "ANAS_CONTAINERS_MODE": "disabled",
            "ANAS_STATE_DIR": str(base / "api-state"),
            "ANAS_DATA_MOUNT": str(base / "data"),
            "ANAS_SCREENSAVER_VIDEO": str(media),
        })
        for _ in range(100):
            if agent.poll() is not None or api.poll() is not None:
                for log in (agent_log, api_log):
                    log.seek(0)
                    print(log.read())
                raise SystemExit("fixture process exited")
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                    if Path(sock).exists():
                        break
            except OSError:
                pass
            time.sleep(0.1)
        else:
            raise SystemExit("fixture startup timed out")

        for conn, wrong_path in (
            (probe.UnixHTTPConnection(sock), "/v1/containers"),
            (http.client.HTTPConnection("127.0.0.1", port, timeout=5), "/screensaver.mp4"),
        ):
            try:
                probe.request(conn, wrong_path)
            except ValueError as error:
                if "HTTP 404" not in str(error):
                    raise
                print(f"EXPECTED_FAILURE: {error}")
            else:
                raise SystemExit("wrong route was not detected")

        probe.verify_agent(sock)
        probe.verify_desktop(port)
        print("PASS: actual release agent/desktop routes and wrong-route rejection")
