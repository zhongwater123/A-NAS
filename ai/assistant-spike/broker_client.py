"""One call to the change broker (change_broker.py) from a shell script.

  broker_client.py product METHOD PATH [JSON]   as the product service (the product socket;
                                                PATH carries ?account=<member>)
  broker_client.py agent METHOD PATH [JSON]     as a member's assistant (the agent socket;
                                                run it as the member, the broker reads the UID)

Prints the HTTP status, a space, and the response body.
"""
import http.client
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
conn.request(method, path, body, {"Content-Type": "application/json"})
reply = conn.getresponse()
print(reply.status, reply.read().decode())
