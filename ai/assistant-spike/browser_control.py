"""Hermes's browser control over a logged-in /api/ws (spike, fake model): the way the VNAS
window registers as the browser of its session and answers the browser commands of a turn
for the WebOS page it shows. Runs as root in the spike container, outside the sandbox,
with the Hermes runtime's Python; prints PASS and FAIL lines.

  browser_control.py USER
"""
import asyncio
import json
import os
import sys

import hermes_login
from serve_client import Gateway, RpcError, short

CAPABILITIES = ["browser_snapshot", "browser_click", "browser_type", "browser_press", "browser_scroll", "browser_screenshot"]
# What the model gets: the window's tools, and browser_vision looking at its screenshot (hermes-patches/0003).
TOOLS = {"browser_snapshot", "browser_click", "browser_type", "browser_press", "browser_scroll", "browser_vision"}
SNAPSHOT = '- button "文件管理" [ref=e1]\n- button "相册" [ref=e2]'
# A 1x1 PNG, as the window sends a picture of the page.
PNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
MODEL_LOG = "/var/log/fake-model.jsonl"
TOOLS_LOG = MODEL_LOG + ".tools.json"
failures = 0


def check(name: str, ok: bool, detail: str = ""):
    global failures
    print(f"{'PASS' if ok else 'FAIL'} {name}" + (f" ({detail})" if detail and not ok else ""))
    failures += 0 if ok else 1


def offered() -> set[str]:
    """The browser tools of the last model request that offered tools."""
    try:
        return {t["function"]["name"] for t in json.load(open(TOOLS_LOG)) if t["function"]["name"].startswith("browser")}
    except (OSError, ValueError):
        return set()


def images_sent_since(offset: int) -> int:
    """Image parts the model was sent after its log was ``offset`` bytes long."""
    try:
        with open(MODEL_LOG, encoding="utf-8") as log:
            log.seek(offset)
            return sum(json.loads(line).get("image_parts", 0) for line in log if line.strip())
    except OSError:
        return 0


def legacy_status(user: str) -> int:
    conn = hermes_login.UnixHTTP(f"/run/a-nas-assistant/{user}.sock", 60)
    conn.request("GET", "/api/sessions?limit=1", headers={"X-Hermes-Session-Token": hermes_login.secret(user), "Host": hermes_login.HOST})
    return conn.getresponse().status


def register(gw: Gateway, sid: str):
    return gw.call("browser.controller.register", {
        "session_id": sid, "protocol_version": 1, "controller_id": "vnas-window",
        "browser_profile_id": "webos", "capabilities": CAPABILITIES})


async def turn(gw: Gateway, sid: str, text: str, answer_commands: bool) -> tuple[list, str]:
    commands = []
    await gw.call("prompt.submit", {"session_id": sid, "text": text})
    while True:
        frame = await asyncio.wait_for(gw.events.get(), 180)
        kind, payload = frame.get("type"), frame.get("payload") or {}
        if kind == "browser.controller.command" and answer_commands:
            commands.append(payload)
            action = payload.get("action")
            result = ({"snapshot": SNAPSHOT} if action == "browser_snapshot"
                      else {"mime_type": "image/png", "data": PNG, "width": 1, "height": 1} if action == "browser_screenshot"
                      else {"clicked": payload.get("arguments")})
            await gw.call("browser.controller.result", {"session_id": sid, "command_id": payload["command_id"], "ok": True, "result": result})
        elif kind == "message.complete" and frame.get("session_id") == sid:
            return commands, payload.get("text") or ""


async def main(user: str):
    check("the old session token is refused", legacy_status(user) == 401)
    token = hermes_login.login(user)
    async with Gateway(user, token=token) as window, Gateway(user, token=token) as other:
        plain = await window.new_session()
        await turn(window, plain, "SAY ok END", False)
        check("a session without the window as its browser has no browser tools", not offered(), str(offered()))

        sid = await window.new_session()
        try:
            await register(other, sid)
            check("another connection cannot register for the window's session", False)
        except RpcError as error:
            check("another connection cannot register for the window's session", "4403" in str(error), str(error))
        registered = await register(window, sid)
        check("the logged-in window registers as its session's browser",
              sorted(registered["scope"]["capabilities"]) == sorted(CAPABILITIES), short(registered))

        commands, answer = await turn(window, sid, "CALL browser_snapshot {}", True)
        check("the model is offered the window's browser tools and browser_vision", offered() == TOOLS, str(sorted(offered())))
        check("browser_snapshot reaches the window", [c.get("action") for c in commands] == ["browser_snapshot"], short(commands))
        check("its answer returns into the turn, as untrusted page content",
              "相册" in answer and "untrusted" in answer, short(answer, 300))
        commands, answer = await turn(window, sid, 'CALL browser_click {"ref": "@e2"}', True)
        check("browser_click carries the ref", commands and commands[0].get("arguments") == {"ref": "@e2"}, short(commands))
        before = os.path.getsize(MODEL_LOG)
        commands, answer = await turn(window, sid, 'CALL browser_vision {"question": "桌面上开着哪些窗口？"}', True)
        check("browser_vision asks the window for a screenshot",
              [c.get("action") for c in commands] == ["browser_screenshot"], short(commands))
        check("the screenshot goes to the vision model", images_sent_since(before) >= 1, str(images_sent_since(before)))
        check("what the vision model saw returns into the turn", '"analysis"' in answer and '"success": true' in answer, short(answer, 300))
    print(f"browser control: {failures} failed")
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    asyncio.run(main(sys.argv[1] if len(sys.argv) > 1 else "alice"))
