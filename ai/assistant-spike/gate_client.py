"""The assistant window's gateway, end to end through the session gate (ADR 0024 decision 10).

Run with the Hermes runtime's Python (it has `websockets`) in the system-test container:

  gate_client.py URL COOKIE [WAIT]
      Opens the product service's /api/v1/assistant/ws with the member's session cookie, as the
      window does, prints one line per step, then keeps reading for up to WAIT seconds (default
      0) and says when the connection closed: the File Broker cuts it once the session ends.
  gate_client.py URL COOKIE submit CSRF JSON
      Instead starts a turn in which the scripted model calls 提交更改 with JSON, confirms the
      card that comes back with the member's CSRF token, as the window does, and prints the
      assistant's answer.
"""
import asyncio
import json
import sys
import time
import urllib.request

from websockets.asyncio.client import connect
from websockets.exceptions import ConnectionClosed

url, cookie = sys.argv[1], sys.argv[2]
mode = sys.argv[3] if len(sys.argv) > 3 else "0"
origin = url.replace("ws://", "http://").split("/api/")[0]


def confirm(request_id, csrf):
    request = urllib.request.Request(f"{origin}/api/v1/assistant/approvals/{request_id}/confirm", method="POST",
                                     headers={"Cookie": f"anas_session={cookie}", "X-CSRF-Token": csrf})
    try:
        with urllib.request.urlopen(request, timeout=120) as reply:
            return reply.status
    except urllib.error.HTTPError as failure:
        return failure.code


async def main():
    started = time.time()
    async with connect(url, additional_headers={"Cookie": f"anas_session={cookie}", "Origin": origin},
                       open_timeout=120, max_size=None) as ws:
        next_id = 0

        async def call(method, params):
            nonlocal next_id
            next_id += 1
            await ws.send(json.dumps({"jsonrpc": "2.0", "id": f"r{next_id}", "method": method, "params": params}))
            while True:
                frame = json.loads(await asyncio.wait_for(ws.recv(), 120))
                if frame.get("id") == f"r{next_id}":
                    return frame

        while True:
            frame = json.loads(await asyncio.wait_for(ws.recv(), 120))
            if frame.get("method") == "event" and frame["params"].get("type") == "gateway.ready":
                print(f"ready after {time.time() - started:.1f} s")
                break
        if mode == "submit":
            csrf, arguments = sys.argv[4], sys.argv[5]
            created = await call("session.create", {"cols": 80})
            sid = created["result"]["session_id"]
            await call("prompt.submit", {"session_id": sid, "text": f"CALL mcp__anas__submit_changes {arguments}"})
            while True:
                frame = json.loads(await asyncio.wait_for(ws.recv(), 180))
                if frame.get("method") != "event":
                    continue
                kind, payload = frame["params"].get("type"), frame["params"].get("payload") or {}
                if kind == "anas.approval":
                    print(f"card {payload.get('status')}: {payload.get('change', {}).get('summary')}")
                    if payload.get("status") == "awaiting":
                        print(f"confirm answered {await asyncio.to_thread(confirm, payload['requestId'], csrf)}")
                elif kind == "message.complete" and frame["params"].get("session_id") == sid:
                    print(f"answer: {payload.get('text')}")
                    return
        refused = await call("shell.exec", {"command": "id"})
        print(f"shell.exec error {refused.get('error', {}).get('code')}")
        profile = await call("session.create", {"cols": 80, "profile": "evil"})
        print(f"profile error {profile.get('error', {}).get('code')}")
        created = await call("session.create", {"cols": 80})
        result = created.get("result") or {}
        print(f"session created {bool(result.get('stored_session_id'))}; system prompt shown {'system_prompt' in json.dumps(result)}")
        wait = float(mode)
        if wait:
            opened = time.time()
            try:
                while (left := wait - (time.time() - opened)) > 0:
                    await asyncio.wait_for(ws.recv(), left)
            except ConnectionClosed as closed:
                print(f"closed after {time.time() - opened:.0f} s: {closed.rcvd.code if closed.rcvd else '-'} "
                      f"{closed.rcvd.reason if closed.rcvd else ''}")
                return
            except TimeoutError:
                pass
            print(f"still open after {wait:.0f} s")


asyncio.run(main())
