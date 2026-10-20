"""What one caller gets from a photo API socket (#113), as whoever runs it.

  photos_peer.py SOCKET [--session TOKEN] ACTION [ARG]
    libraries        the libraries, as "kind:owner" pairs
    search QUERY     the photo names a search returns
    ids QUERY        name=id for each photo a search returns
    get ID           one photo's name
    thumbnail ID     the thumbnail's size
    view ID          the size and type of the photo's view (what the model looks at)
    rename ID        try to rename a photo

Prints the HTTP status and a short answer; exits 0 on 2xx, 1 otherwise and 2
when the socket cannot be reached at all.
"""
import http.client
import json
import socket
import sys
import urllib.parse

args = sys.argv[1:]
path, args = args[0], args[1:]
token = ""
if args[0] == "--session":
    token, args = args[1], args[2:]
action, arg = args[0], (args[1] if len(args) > 1 else "")


class UnixHTTP(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(30)
        self.sock.connect(path)


def call(method, route, body=None):
    conn = UnixHTTP("localhost")
    headers = {"X-A-NAS-Session": token} if token else {}
    if body is not None:
        headers["Content-Type"] = "application/json"
    try:
        conn.request(method, "/api/v1/photos" + route, json.dumps(body) if body is not None else None, headers)
        response = conn.getresponse()
    except OSError as error:
        print(f"unreachable: {error.__class__.__name__}: {error}")
        sys.exit(2)
    data = response.read()
    return response.status, data


quoted = urllib.parse.quote(arg, safe="")
if action == "libraries":
    status, data = call("GET", "/libraries")
    answer = [f"{lib['kind']}:{lib.get('ownerUserId', '')}" for lib in json.loads(data)["items"]] if status == 200 else data[:200]
elif action in ("search", "ids"):
    status, data = call("GET", "/search?q=" + quoted)
    items = json.loads(data)["items"] if status == 200 else []
    answer = sorted(a["name"] if action == "search" else f"{a['name']}={a['id']}" for a in items) if status == 200 else data[:200]
elif action == "get":
    status, data = call("GET", "/assets/" + quoted)
    answer = json.loads(data)["name"] if status == 200 else data[:200]
elif action == "thumbnail":
    status, data = call("GET", f"/assets/{quoted}/thumbnail")
    answer = f"{len(data)} bytes" if status == 200 else data[:200]
elif action == "view":
    status, data = call("GET", f"/assets/{quoted}/view")
    answer = f"{len(data)} bytes {'image/jpeg' if data[:3] == bytes([0xff, 0xd8, 0xff]) else 'not a JPEG'}" if status == 200 else data[:200]
elif action == "rename":
    status, data = call("PATCH", "/assets/" + quoted, {"name": "renamed.jpg"})
    answer = data[:200]
else:
    raise SystemExit(__doc__)
print(status, answer if isinstance(answer, (str, list)) else answer.decode("utf-8", "replace"))
sys.exit(0 if 200 <= status < 300 else 1)
