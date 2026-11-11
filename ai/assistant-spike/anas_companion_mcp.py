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
For the same reason the server is the SDK's low-level one: the high-level
MCPServer checks a call's arguments against the function's signature and
answers a missing one as an error before the tool runs, and parses a string
that looks like JSON ("[1,2]", "null") into a list or nothing. Here the
schema is only what the model is shown; the arguments reach the product
service as the model wrote them, and it answers a wrong or missing one with
rejected/bad-request.
"""
import http.client
import json
import socket

import anyio
import anyio.to_thread
from mcp.server import Server
from mcp.server.stdio import stdio_server
from mcp_types import CallToolResult, ListToolsResult, TextContent, Tool

COMPANION = "/run/anas-assistant-companion/companion.sock"
# The product service waits for the window up to 15 s, and for the turn's own
# tool.start 1.5 s before that; Hermes gives up on this server after 120 s,
# counted from when the call queued behind the server's other calls.
TIMEOUT = 20


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path: str):
        super().__init__("localhost")
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(TIMEOUT)
        self.sock.connect(self.path)


NO_DESKTOP = {"status": "no_desktop"}
# The product service reads at most 8 KiB of a call (maxCompanionCall); only a
# text far over its 200 characters gets there.
TOO_LONG = {"status": "rejected", "reason": "bad-request",
            "message": "参数太长：text 最多 200 字，name 最多 64 字，lock、lockId 最多 128 字"}
BAD_REQUEST = {"status": "rejected", "reason": "bad-request", "message": "参数无法读取"}
BUSY = {"status": "busy"}

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
    try:
        body = json.dumps({"tool": tool, "args": args}, ensure_ascii=False).encode()
    except (TypeError, ValueError):
        # A string with a lone surrogate, which UTF-8 cannot carry: the
        # product service would not read it either.
        return BAD_REQUEST
    conn = UnixHTTP(COMPANION)
    try:
        try:
            conn.connect()
        except OSError:
            # No socket (a scheduled task's sandbox), or nobody serving it:
            # the call never reached the product service.
            return NO_DESKTOP
        sent = True
        try:
            conn.request("POST", "/call", body, {"Content-Type": "application/json"})
        except OSError:
            # The product service closed the connection before taking the
            # call: over the member's connections it answers busy as it
            # accepts one, before reading anything, and the answer is still
            # there to read.
            sent = False
        try:
            response = conn.getresponse()
            raw = response.read()
        except (OSError, http.client.HTTPException):
            if not sent:
                # Not even a refusal: the call never got there.
                return BUSY
            # A timeout, or the product service went away (a restart) once the
            # call was on its way: the window may have acted on it.
            return {"status": "timeout"}
    finally:
        conn.close()
    if response.status == 413:
        return TOO_LONG
    if response.status == 400:
        return BAD_REQUEST
    if response.status != 200:
        # The product service does not take this member's calls.
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
    # The window says what to do next, such as waking the companion or passing
    # the lock's id; the model reads only this text, not the reason code.
    message = data.get("message")
    if status in ("cancelled", "failed", "rejected") and isinstance(message, str) and message.strip():
        said += f"：{message.strip()}"
    if not said.endswith("。"):
        said += "。"
    # Where the companion went instead of where it was asked to, as the window answers a position.
    if status == "ok" and data.get("stepped_aside") is True:
        said += f"它让开了最上层的窗口，停在 x={data.get('x')}、y={data.get('y')}。"
    return said


TEXT = {"type": "string"}
NUMBER = {"type": "number"}
FLAG = {"type": "boolean"}
FACING = {"type": "string", "enum": ["left", "right"]}
MODE = {"type": "string", "enum": ["instant", "spring", "line"]}

LOCK = "lock：伙伴被锁定时，锁的 lockId；没被锁就不填。"

# Each tool: what the model is told, its arguments with their schemas, and the
# ones it must give.
TOOLS = {
    "anim": ("让伙伴播放一段动画，等它播完再返回（最多约 11.5 秒）。name：动画名；facing：朝向 left 或 right。" + LOCK,
             {"name": TEXT, "facing": FACING, "lock": TEXT}, ["name"]),
    "broadcast": ("让伙伴用气泡说一句话。text：1–200 字的纯文本，可以换行；duration：显示多少毫秒（0–60000，默认 10000）；"
                  "anim：说话时是否配动画（默认是）。" + LOCK,
                  {"text": TEXT, "duration": NUMBER, "anim": FLAG, "lock": TEXT}, ["text"]),
    "bubble_clear": ("收起伙伴的气泡。" + LOCK, {"lock": TEXT}, []),
    "position": ("把伙伴移到屏幕上的某处。x、y：伙伴中心的位置，0–1，左上角为 0；mode：instant 立即到、spring 弹过去、"
                 "line 直线移动；duration：line 用多少毫秒（0–5000）。伙伴会让开最上层的窗口：目标会盖住它时，伙伴改去窗口旁最近的"
                 "空位，结果的 stepped_aside 为 true。结果给出实际到达的 x、y。" + LOCK,
                 {"x": NUMBER, "y": NUMBER, "mode": MODE, "duration": NUMBER, "lock": TEXT}, ["x", "y", "mode"]),
    "throw": ("把伙伴抛出去。angle：角度，0 向右，逆时针为正；power：力度，大于 0、最大 3。落地后如果盖住最上层的窗口，"
              "伙伴会自己让开。" + LOCK,
              {"angle": NUMBER, "power": NUMBER, "lock": TEXT}, ["angle", "power"]),
    "sleep": ("让伙伴睡觉；它被锁定时不行。", {}, []),
    "wake": ("叫醒伙伴。", {}, []),
    "lock": ("锁定伙伴一段时间，期间别人的命令不会打断你；之后的命令都要带上返回的 lockId。成员拖动或点击伙伴会收回锁，"
             "这一轮对话结束时锁也会释放。lockId：续期时带上原来的；ttl：毫秒，1000–300000，默认 120000。",
             {"lockId": TEXT, "ttl": NUMBER}, []),
    "lock_release": ("释放 lock 拿到的锁。lockId：锁的 id。", {"lockId": TEXT}, ["lockId"]),
}


def tools() -> list[Tool]:
    return [Tool(name=name, description=description,
                 input_schema={"type": "object", "properties": properties, "required": required})
            for name, (description, properties, required) in TOOLS.items()]


def call(tool: str, arguments: dict | None) -> CallToolResult:
    # Only what the model gave of the tool's own arguments: the product service
    # matches the call with the arguments Hermes reported when the tool started,
    # less nulls and keys the tool does not take.
    properties = TOOLS[tool][1] if tool in TOOLS else {}
    given = {key: value for key, value in (arguments or {}).items() if key in properties and value is not None}
    data = ask(tool, given)
    return CallToolResult(content=[TextContent(type="text", text=line(data))], structured_content=data, is_error=False)


async def list_tools(_context, _params) -> ListToolsResult:
    return ListToolsResult(tools=tools())


async def call_tool(_context, params) -> CallToolResult:
    # ask blocks on the socket; the server goes on answering meanwhile.
    return await anyio.to_thread.run_sync(call, params.name, params.arguments)


server = Server("companion", instructions=(
    "成员桌面上的伙伴“大肥鱼”，只在成员正与你对话的那台设备的 VNAS 窗口里动。"
    "没有在用的桌面（例如定时任务）时什么都不会发生，结果会如实说明。"),
    on_list_tools=list_tools, on_call_tool=call_tool)


async def main():
    async with stdio_server() as (read, write):
        await server.run(read, write, server.create_initialization_options())


if __name__ == "__main__":
    anyio.run(main)
