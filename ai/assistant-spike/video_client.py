"""One request to the video moments service as the member it runs as (#350, development only).

  video_client.py PATH < body.json > answer

The body goes to PATH on the service's assistant socket (videos_spike.py), the answer's body
comes out on stdout, and its status, content type and the X- headers as one JSON line on
stderr. eval.video_plan runs it through video-as-member.sh, in a member's assistant namespace,
where the socket is bound as anas_mcp.py sees it.
"""
import http.client
import json
import socket
import sys

SOCKET = "/run/anas-assistant-videos/videos.sock"


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path: str, seconds: float):
        super().__init__("localhost", timeout=seconds)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def main():
    body = sys.stdin.buffer.read()
    # A cut takes a while: frames are embedded, then FFmpeg decodes and encodes every second.
    conn = UnixHTTP(SOCKET, 1800)
    conn.request("POST", sys.argv[1], body, {"Content-Type": "application/json"})
    response = conn.getresponse()
    data = response.read()
    sys.stdout.buffer.write(data)
    sys.stdout.flush()
    head = {"status": response.status, "type": response.getheader("Content-Type", "")}
    head |= {k: v for k, v in response.getheaders() if k.lower().startswith("x-")}
    sys.stderr.write(json.dumps(head) + "\n")


if __name__ == "__main__":
    main()
