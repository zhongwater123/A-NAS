"""Hermes's own login to a member's `hermes serve` (spike), as the Host Agent does for a
signed-in session (internal/assistantlogin): the instance's root-only secret is the member's
password, and the access token from the session cookie is the bearer for REST and the
WebSocket's ?token=. serve refuses its legacy session token since its login gate is on.

Runs as root in the spike container with the Hermes runtime's Python.

  hermes_login.py USER      print an access token for USER's instance
"""
import http.client
import json
import socket
import sys
from pathlib import Path

HOST = "127.0.0.1:9119"


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path: str, timeout: float = 180):
        super().__init__(HOST, timeout=timeout)
        self.socket_path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.socket_path)


def secret(user: str) -> str:
    """The instance's secret, the member's password for Hermes's login."""
    for line in Path(f"/etc/a-nas-assistant/tokens/{user}.env").read_text().splitlines():
        if line.startswith("HERMES_DASHBOARD_SESSION_TOKEN="):
            return line.split("=", 1)[1].strip()
    raise SystemExit(f"no assistant secret for {user}")


def login(user: str, password: str | None = None, timeout: float = 180) -> str:
    """Log in to USER's instance (starting it through its socket) and return the access token."""
    conn = UnixHTTP(f"/run/a-nas-assistant/{user}.sock", timeout)
    body = json.dumps({"provider": "basic", "username": user, "password": secret(user) if password is None else password})
    conn.request("POST", "/auth/password-login", body, {"Content-Type": "application/json", "Host": HOST})
    response = conn.getresponse()
    response.read()
    if response.status != 200:
        raise SystemExit(f"login to {user}'s assistant answered {response.status}")
    for header in response.headers.get_all("Set-Cookie") or []:
        name, _, rest = header.partition("=")
        if name.strip().endswith("hermes_session_at"):
            return rest.split(";", 1)[0]
    raise SystemExit(f"login to {user}'s assistant set no session")


if __name__ == "__main__":
    print(login(sys.argv[1]))
