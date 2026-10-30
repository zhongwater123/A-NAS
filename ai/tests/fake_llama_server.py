"""A stand-in for llama-server in the tests of LlamaReader.

It listens where --host says, a UNIX socket, answers /health, and streams a
fixed answer to /v1/chat/completions the way llama-server does, with each
token's log probability. Environment variables shape it:

- FAKE_LLAMA_RECORD: a file for the command line and the last request's body;
- FAKE_LLAMA_DELAY: seconds to wait before answering, as the image's encoding does;
- FAKE_LLAMA_STATUS: answer with this HTTP status instead;
- FAKE_LLAMA_REPEAT: answer the same piece this many times;
- FAKE_LLAMA_EXIT: exit at once with this code, as on a model that does not load.
"""

import http.server
import json
import os
import socketserver
import sys
import time

ANSWER = ["第一行", "\n", "第二行"]


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"

    def address_string(self):
        return "unix"

    def log_message(self, format, *args):
        pass

    def do_GET(self):
        if self.path != "/health":
            self.send_error(404)
            return
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'{"status": "ok"}')

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        record = os.environ.get("FAKE_LLAMA_RECORD")
        if record:
            with open(record, "w", encoding="utf-8") as file:
                json.dump({"argv": sys.argv[1:], "body": body}, file, ensure_ascii=False)
        status = int(os.environ.get("FAKE_LLAMA_STATUS", "200"))
        if status != 200:
            self.send_error(status, "image could not be decoded")
            return
        time.sleep(float(os.environ.get("FAKE_LLAMA_DELAY", "0")))
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        repeat = int(os.environ.get("FAKE_LLAMA_REPEAT", "0"))
        pieces = ["回单"] * repeat if repeat else ANSWER
        for piece in pieces:
            event = {"choices": [{"delta": {"content": piece}, "finish_reason": None,
                                  "logprobs": {"content": [{"token": piece, "logprob": -0.01}]}}]}
            self.wfile.write(b"data: " + json.dumps(event, ensure_ascii=False).encode() + b"\n\n")
        self.wfile.write(b'data: {"choices": [{"delta": {}, "finish_reason": "stop"}]}\n\ndata: [DONE]\n\n')


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


def main():
    code = os.environ.get("FAKE_LLAMA_EXIT")
    if code:
        print("failed to load the model", flush=True)
        sys.exit(int(code))
    with Server(sys.argv[sys.argv.index("--host") + 1], Handler) as server:
        server.serve_forever()


if __name__ == "__main__":
    main()
