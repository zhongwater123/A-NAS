#!/usr/bin/env python3
"""HTTPS file server for verify.sh.

Logs when each response starts and ends and how many bytes it sent, so the
driver can measure downloads and cut the power mid-transfer. The rate file
holds lines "<path substring> <bytes per second>"; the first line whose
substring is in the request path slows that response down. It is read for
every chunk, so the driver can change it during a transfer.

Usage: serve.py ROOT PORT CERT KEY LOG RATE_FILE
"""

import functools
import http.server
import ssl
import sys
import time

root, port, cert, key, log_path, rate_path = sys.argv[1:7]


def rate_for(path):
    try:
        with open(rate_path, encoding="utf-8") as rates:
            for line in rates:
                fields = line.split()
                if len(fields) == 2 and fields[0] in path:
                    return int(fields[1])
    except FileNotFoundError:
        pass
    return 0


def log(event, path, size):
    with open(log_path, "a", encoding="utf-8") as out:
        out.write(f"{time.time():.3f} {event} {path} {size}\n")


class Handler(http.server.SimpleHTTPRequestHandler):
    def copyfile(self, source, outputfile):
        sent = 0
        log("start", self.path, 0)
        try:
            while True:
                rate = rate_for(self.path)
                chunk = source.read(max(16384, rate // 20) if rate else 1 << 20)
                if not chunk:
                    break
                outputfile.write(chunk)
                sent += len(chunk)
                if rate:
                    time.sleep(len(chunk) / rate)
        finally:
            log("end", self.path, sent)

    def log_message(self, format, *args):  # noqa: A002 (the base class's name)
        log("request", self.path, format % args)


context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(cert, key)
server = http.server.ThreadingHTTPServer(
    ("0.0.0.0", int(port)), functools.partial(Handler, directory=root)
)
server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
