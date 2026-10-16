"""JSON-RPC client for a member's `hermes serve` (spike), the way the product service talks to it.

Runs as root in the spike container, outside the sandbox, with the Hermes runtime's Python
(it has `websockets`). One WebSocket on /api/ws through the member's Unix socket,
authenticated with the instance's session token as `?token=` (Hermes's loopback mode).

  serve_client.py USER ready                 connect and wait for gateway.ready (cold start)
  serve_client.py USER chat TEXT [TEXT...]   one new session, one turn per TEXT
  serve_client.py USER calls FILE            the same with the lines of FILE (in /spike)
  serve_client.py USER resume ID TEXT...     continue the stored session ID, one turn per TEXT
  serve_client.py USER rpc METHOD [JSON]     one raw call; prints the result or the error
"""
import asyncio
import json
import sys
import time
from pathlib import Path

from websockets.asyncio.client import unix_connect

PORT = 9119


def session_token(user: str) -> str:
    for line in Path(f"/etc/a-nas-assistant/tokens/{user}.env").read_text().splitlines():
        if line.startswith("HERMES_DASHBOARD_SESSION_TOKEN="):
            return line.split("=", 1)[1].strip()
    raise SystemExit(f"no session token for {user}")


def short(value, n=600):
    text = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)
    text = text.replace("\n", "⏎")
    return text if len(text) <= n else text[:n] + "…"


class RpcError(Exception):
    pass


class Gateway:
    def __init__(self, user: str, token: str | None = None, timeout: float = 180):
        self.user, self.timeout = user, timeout
        self.token = session_token(user) if token is None else token
        self.next_id, self.pending, self.events = 0, {}, asyncio.Queue()
        self.ready_payload = None

    async def __aenter__(self):
        self.ws = await unix_connect(f"/run/a-nas-assistant/{self.user}.sock",
                                     f"ws://127.0.0.1:{PORT}/api/ws?token={self.token}",
                                     max_size=None, open_timeout=self.timeout)
        self.reader = asyncio.create_task(self._read())
        while True:
            frame = await asyncio.wait_for(self.events.get(), self.timeout)
            if frame.get("type") == "gateway.ready":
                self.ready_payload = frame.get("payload")
                return self

    async def __aexit__(self, *exc):
        self.reader.cancel()
        await self.ws.close()

    async def _read(self):
        async for raw in self.ws:
            for line in raw.splitlines():
                if not line.strip():
                    continue
                msg = json.loads(line)
                if msg.get("method") == "event":
                    await self.events.put(msg.get("params") or {})
                elif "id" in msg and msg["id"] in self.pending and "method" not in msg:
                    self.pending.pop(msg["id"]).set_result(msg)
                else:
                    # A request from the server (approval, clarify, ...): record it unanswered.
                    await self.events.put({"type": "server.request", "payload": msg})

    async def call(self, method: str, params: dict | None = None):
        self.next_id += 1
        rid = self.next_id
        future = asyncio.get_running_loop().create_future()
        self.pending[rid] = future
        await self.ws.send(json.dumps({"jsonrpc": "2.0", "id": rid, "method": method, "params": params or {}}))
        reply = await asyncio.wait_for(future, self.timeout)
        if "error" in reply:
            raise RpcError(json.dumps(reply["error"], ensure_ascii=False))
        return reply.get("result")

    async def new_session(self) -> str:
        result = await self.call("session.create", {"cols": 100})
        return result["session_id"]

    async def turn(self, sid: str, text: str) -> dict:
        """Submit one prompt and collect its events until message.complete for that session."""
        started = time.time()
        await self.call("prompt.submit", {"session_id": sid, "text": text})
        tools, types, answer, requests = [], [], None, []
        while True:
            frame = await asyncio.wait_for(self.events.get(), self.timeout)
            kind = frame.get("type")
            if frame.get("session_id") not in (None, sid):
                continue
            types.append(kind)
            payload = frame.get("payload") or {}
            if kind == "tool.start":
                tools.append(payload.get("name"))
            elif kind == "server.request":
                requests.append(payload)
            elif kind == "message.complete":
                answer = payload.get("text")
                status = payload.get("status")
                break
        return {"answer": answer, "status": status, "tools": tools, "requests": requests,
                "types": sorted(set(types)), "seconds": round(time.time() - started, 1)}


async def chat(user: str, lines: list[str], resume: str | None = None):
    async with Gateway(user) as gw:
        if resume:
            sid = (await gw.call("session.resume", {"session_id": resume, "cols": 100}))["session_id"]
            print(f"session {sid} (resumed {resume})")
        else:
            sid = await gw.new_session()
            print(f"session {sid}")
        for said in lines:
            result = await gw.turn(sid, said)
            print(f"> {short(said, 110)}\n< {short(result['answer'])}")
            print(f"  tools started: {result['tools'] or '-'}; status {result['status']}; {result['seconds']} s")
            for request in result["requests"]:
                print(f"  unanswered server request: {short(request, 300)}")
            print()


async def main():
    user, action, *rest = sys.argv[1:]
    if action == "ready":
        started = time.time()
        async with Gateway(user) as gw:
            print(f"gateway.ready after {time.time() - started:.1f} s; payload keys {sorted(gw.ready_payload or {})}")
    elif action == "chat":
        await chat(user, rest)
    elif action == "resume":
        await chat(user, rest[1:], resume=rest[0])
    elif action == "calls":
        lines = Path("/spike", rest[0]).read_text(encoding="utf-8").splitlines()
        await chat(user, [line for line in lines if line.strip()])
    elif action == "rpc":
        async with Gateway(user) as gw:
            try:
                print(short(await gw.call(rest[0], json.loads(rest[1]) if len(rest) > 1 else {}), 2000))
            except RpcError as exc:
                print(f"error {exc}")
    else:
        raise SystemExit(__doc__)


if __name__ == "__main__":
    asyncio.run(main())
