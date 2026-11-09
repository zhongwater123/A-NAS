"""The companion's tools for a member's assistant (#412).

A stdio server inside the sandbox, beside anas_mcp.py. Each call goes to the
product service's companion socket, which knows the member by the caller's
UID, finds the conversation the call belongs to from Hermes's own events and
passes the command on to that conversation's VNAS window, the desktop the
member is talking on. The window carries it out with its companion and
answers; the tools take dsh-pet's routes and arguments (/anim, /broadcast,
/bubble/clear, /position, /throw, /sleep, /wake, /lock), without a pet, a
surface, a URL or an image.

Hermes runs this server with `trust: full` (managed-config.yaml), so these
tools do not wait for the member's approval. None of them is marked
read-only to get there: they change what the member sees, not their data,
and only on the screen of the conversation that asked.

Every answer is a result, never an error, so that Hermes's circuit breaker
never counts a desktop that is not there: its `status` says what happened
(ok, ended, cancelled, failed, rejected, no_desktop, busy or timeout), and
one line of text says it to the model. A scheduled task's sandbox has no
companion socket, so its calls are answered here: no desktop is in use.
"""
import http.client
import json
import socket
from typing import Literal

from mcp.server import MCPServer
from mcp_types import CallToolResult, TextContent

COMPANION = "/run/anas-assistant-companion/companion.sock"
# The product service waits for the window up to 15 s, and for the turn's own
# tool.start 1.5 s before that; Hermes gives up on this server after 30 s.
TIMEOUT = 20

server = MCPServer("companion", instructions=(
    "成员桌面上的伙伴“大肥鱼”，只在成员正与你对话的那台设备的 VNAS 窗口里动。"
    "没有在用的桌面（例如定时任务）时什么都不会发生，结果会如实说明。"))


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path: str):
        super().__init__("localhost")
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(TIMEOUT)
        self.sock.connect(self.path)


NO_DESKTOP = {"status": "no_desktop"}

SAID = {
    "ok": "伙伴照做了。",
    "ended": "动画播完了。",
    "cancelled": "动画没播完就停了",
    "failed": "动画没能播放",
    "rejected": "伙伴没有照做",
    "no_desktop": "没有在用的桌面：这段对话不在成员的 VNAS 窗口里进行（例如定时任务），伙伴没有动。",
    "busy": "伙伴现在顾不过来，稍后再试。",
    "timeout": "桌面没有及时回应，不知道伙伴动了没有。",
}


def ask(tool: str, args: dict) -> dict:
    """The product service's answer to one call, or what to say without it."""
    conn = UnixHTTP(COMPANION)
    try:
        conn.request("POST", "/call", json.dumps({"tool": tool, "args": args}, ensure_ascii=False).encode(),
                     {"Content-Type": "application/json"})
        response = conn.getresponse()
        raw = response.read()
    except TimeoutError:
        return {"status": "timeout"}
    except OSError:
        # No socket (a scheduled task's sandbox), or nobody serving it.
        return NO_DESKTOP
    finally:
        conn.close()
    if response.status != 200:
        return NO_DESKTOP
    try:
        data = json.loads(raw)
    except ValueError:
        return NO_DESKTOP
    if not isinstance(data, dict) or not isinstance(data.get("status"), str):
        return NO_DESKTOP
    return data


def line(data: dict) -> str:
    status = data["status"]
    said = SAID.get(status, f"伙伴回答：{status}")
    reason = data.get("reason")
    if status in ("cancelled", "failed", "rejected") and reason:
        said += f"（{reason}）"
    elif not said.endswith("。"):
        said += "。"
    return said


def call(tool: str, **given) -> CallToolResult:
    # Only what the model gave: the product service matches the call with the
    # arguments Hermes reported when the tool started.
    data = ask(tool, {key: value for key, value in given.items() if value is not None})
    return CallToolResult(content=[TextContent(type="text", text=line(data))], structured_content=data, is_error=False)


LOCK = "lock：伙伴被锁定时，锁的 lockId；没被锁就不填。"


@server.tool(structured_output=False, description=(
    "让伙伴播放一段动画，等它播完再返回（最多约 10 秒）。name：动画名；facing：朝向 left 或 right。" + LOCK))
def anim(name: str, facing: Literal["left", "right"] | None = None, lock: str | None = None):
    return call("anim", name=name, facing=facing, lock=lock)


@server.tool(structured_output=False, description=(
    "让伙伴用气泡说一句话。text：1–200 字的纯文本，可以换行；duration：显示多少毫秒（0–60000，默认 10000）；"
    "anim：说话时是否配动画（默认是）。" + LOCK))
def broadcast(text: str, duration: float | None = None, anim: bool | None = None, lock: str | None = None):
    return call("broadcast", text=text, duration=duration, anim=anim, lock=lock)


@server.tool(structured_output=False, description="收起伙伴的气泡。" + LOCK)
def bubble_clear(lock: str | None = None):
    return call("bubble_clear", lock=lock)


@server.tool(structured_output=False, description=(
    "把伙伴移到屏幕上的某处。x、y：伙伴中心的位置，0–1，左上角为 0；mode：instant 立即到、spring 弹过去、"
    "line 直线移动；duration：line 用多少毫秒（0–5000）。结果给出实际到达的 x、y。" + LOCK))
def position(x: float, y: float, mode: Literal["instant", "spring", "line"], duration: float | None = None,
             lock: str | None = None):
    return call("position", x=x, y=y, mode=mode, duration=duration, lock=lock)


@server.tool(structured_output=False, description=(
    "把伙伴抛出去。angle：角度，0 向右，逆时针为正；power：力度，大于 0、最大 3。" + LOCK))
def throw(angle: float, power: float, lock: str | None = None):
    return call("throw", angle=angle, power=power, lock=lock)


@server.tool(structured_output=False, description="让伙伴睡觉；它被锁定时不行。")
def sleep():
    return call("sleep")


@server.tool(structured_output=False, description="叫醒伙伴。")
def wake():
    return call("wake")


@server.tool(structured_output=False, description=(
    "锁定伙伴一段时间，期间别人的命令不会打断你；之后的命令都要带上返回的 lockId。成员拖动或点击伙伴会收回锁，"
    "这一轮对话结束时锁也会释放。lockId：续期时带上原来的；ttl：毫秒，1000–300000，默认 120000。"))
def lock(lockId: str | None = None, ttl: float | None = None):
    return call("lock", lockId=lockId, ttl=ttl)


@server.tool(structured_output=False, description="释放 lock 拿到的锁。lockId：锁的 id。")
def lock_release(lockId: str):
    return call("lock_release", lockId=lockId)


if __name__ == "__main__":
    import anyio
    anyio.run(server.run_stdio_async)
