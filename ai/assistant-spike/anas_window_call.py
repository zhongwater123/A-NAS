"""How the assistant's window tools reach the product service.

The companion's server (anas_companion_mcp.py) and the desktop's (anas_desktop_mcp.py) both
pass each call to the product service's socket for assistants, which knows the member by the
caller's UID, finds the conversation the call belongs to from Hermes's own events and passes
the command on to that conversation's VNAS window. A scheduled task's sandbox has no such
socket, so its calls are answered here: no desktop is in use.
"""
import http.client
import json
import socket

SOCKET = "/run/anas-assistant-companion/companion.sock"

NO_DESKTOP = {"status": "no_desktop"}
BAD_REQUEST = {"status": "rejected", "reason": "bad-request", "message": "参数无法读取"}
BUSY = {"status": "busy"}


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path: str, timeout: float):
        super().__init__("localhost")
        self.path = path
        self.wait = timeout

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.wait)
        self.sock.connect(self.path)


def ask(path: str, route: str, tool: str, args: dict, timeout: float, too_long: dict) -> dict:
    """The product service's answer to one call on route, or what to say without it."""
    try:
        body = json.dumps({"tool": tool, "args": args}, ensure_ascii=False).encode()
    except (TypeError, ValueError):
        # A string with a lone surrogate, which UTF-8 cannot carry: the
        # product service would not read it either.
        return BAD_REQUEST
    conn = UnixHTTP(path, timeout)
    try:
        try:
            conn.connect()
        except OSError:
            # No socket (a scheduled task's sandbox), or nobody serving it:
            # the call never reached the product service.
            return NO_DESKTOP
        sent = True
        try:
            conn.request("POST", route, body, {"Content-Type": "application/json"})
        except OSError:
            # The product service closed the connection before taking the
            # call: over the member's connections it answers busy as it
            # accepts one, before reading anything, and the answer is still
            # there to read.
            sent = False
        try:
            response = conn.getresponse()
            raw = response.read()
        except (OSError, http.client.HTTPException):
            if not sent:
                # Not even a refusal: the call never got there.
                return BUSY
            # A timeout, or the product service went away (a restart) once the
            # call was on its way: the window may have acted on it.
            return {"status": "timeout"}
    finally:
        conn.close()
    if response.status == 413:
        return too_long
    if response.status == 400:
        return BAD_REQUEST
    if response.status != 200:
        # The product service does not take this member's calls.
        return NO_DESKTOP
    try:
        data = json.loads(raw)
    except ValueError:
        return NO_DESKTOP
    if not isinstance(data, dict) or not isinstance(data.get("status"), str):
        return NO_DESKTOP
    return data
