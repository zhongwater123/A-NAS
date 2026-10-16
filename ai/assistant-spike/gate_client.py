"""The assistant window's gateway, end to end through the session gate (ADR 0022 decision 10).

Run with the Hermes runtime's Python (it has `websockets`) in the system-test container:

  gate_client.py URL COOKIE [WAIT]

Opens the product service's /api/v1/assistant/ws with the member's session cookie, as the
window does, prints one line per step, then keeps reading for up to WAIT seconds (default 0)
and says when the connection closed: the File Broker cuts it once the session ends.
"""
import asyncio
import json
import sys
import time

from websockets.asyncio.client import connect
from websockets.exceptions import ConnectionClosed

url, cookie = sys.argv[1], sys.argv[2]
wait = float(sys.argv[3]) if len(sys.argv) > 3 else 0
origin = url.replace("ws://", "http://").split("/api/")[0]


async def main():
    started = time.time()
    async with connect(url, additional_headers={"Cookie": f"anas_session={cookie}", "Origin": origin},
                       open_timeout=120, max_size=None) as ws:
        pending, next_id = {}, 0

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
        refused = await call("shell.exec", {"command": "id"})
        print(f"shell.exec error {refused.get('error', {}).get('code')}")
        profile = await call("session.create", {"cols": 80, "profile": "evil"})
        print(f"profile error {profile.get('error', {}).get('code')}")
        created = await call("session.create", {"cols": 80})
        result = created.get("result") or {}
        print(f"session created {bool(result.get('stored_session_id'))}; system prompt shown {'system_prompt' in json.dumps(result)}")
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
