"""The desktop tools in the assistant's sandbox, on a logged-in /api/ws (spike, fake model):
the instance refuses the old session token, the model is offered the desktop server's tools
and none of Hermes's own browser tools, and a step with no VNAS window behind it says that
no desktop is in use. The way from a step to the window and back needs the product service:
gate_client.py's desktop mode, in the system test. Runs as root in the spike container,
outside the sandbox, with the Hermes runtime's Python; prints PASS and FAIL lines.

  desktop_control.py USER
"""
import asyncio
import json
import sys

import hermes_login
from serve_client import Gateway, short

TOOLS = {f"mcp__desktop__{name}" for name in ("snapshot", "click", "type", "press", "scroll", "look")}
TOOLS_LOG = "/var/log/fake-model.jsonl.tools.json"
failures = 0


def check(name: str, ok: bool, detail: str = ""):
    global failures
    print(f"{'PASS' if ok else 'FAIL'} {name}" + (f" ({detail})" if detail and not ok else ""))
    failures += 0 if ok else 1


def offered() -> set[str]:
    """The tools of the last model request that offered tools."""
    try:
        with open(TOOLS_LOG, encoding="utf-8") as dump:
            return {tool["function"]["name"] for tool in json.load(dump)}
    except (OSError, ValueError):
        return set()


def legacy_status(user: str) -> int:
    conn = hermes_login.UnixHTTP(f"/run/a-nas-assistant/{user}.sock", 60)
    conn.request("GET", "/api/sessions?limit=1", headers={"X-Hermes-Session-Token": hermes_login.secret(user), "Host": hermes_login.HOST})
    return conn.getresponse().status


async def main(user: str):
    check("the old session token is refused", legacy_status(user) == 401)
    async with Gateway(user, token=hermes_login.login(user)) as window:
        sid = await window.new_session()
        result = await window.turn(sid, "CALL mcp__desktop__snapshot {}")
        names = offered()
        check("the model is offered the desktop server's tools", names >= TOOLS, str(sorted(TOOLS - names)))
        browser = sorted(name for name in names if name.startswith("browser"))
        check("and none of Hermes's own browser tools", not browser, str(browser))
        check("the step starts without Hermes's approval", "mcp__desktop__snapshot" in result["tools"] and not result["requests"],
              short(result, 300))
        check("with no VNAS window behind it, the step says that no desktop is in use",
              "没有在用的桌面" in (result["answer"] or ""), short(result["answer"], 300))
    print(f"desktop control: {failures} failed")
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    asyncio.run(main(sys.argv[1] if len(sys.argv) > 1 else "alice"))
