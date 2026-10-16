"""One call to the change broker (change_broker.py) from a shell script.

  broker_client.py product METHOD PATH [JSON]   as the product service (the product socket;
                                                PATH carries ?account=<member>)
  broker_client.py agent METHOD PATH [JSON]     as a member's assistant (the agent socket;
                                                run it as the member, the broker reads the UID)

Prints the HTTP status, a space, and the response body. A product call carries the member's
session from BROKER_SESSION when it is set, as the product service does (the broker checks it
with the Host Agent when it runs as the session gate's executor).
"""
import http.client
import os
import socket
import sys

SOCKETS = {"product": "/run/anas-spike-broker/broker.sock", "agent": "/run/anas-change-agent/agent.sock"}
side, method, path = sys.argv[1:4]
body = sys.argv[4].encode() if len(sys.argv) > 4 and sys.argv[4] else None


class UDS(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(60)
        self.sock.connect(SOCKETS[side])


conn = UDS("uds")
headers = {"Content-Type": "application/json"}
if side == "product" and os.environ.get("BROKER_SESSION"):
    headers["X-A-NAS-Session"] = os.environ["BROKER_SESSION"]
conn.request(method, path, body, headers)
reply = conn.getresponse()
print(reply.status, reply.read().decode())
