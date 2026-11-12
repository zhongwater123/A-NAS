"""The tools with which a member's assistant operates the member's WebOS desktop (ADR 0024 decision 8).

A stdio server inside the sandbox, beside anas_mcp.py and anas_companion_mcp.py. The page is
not in the sandbox: it is the WebOS desktop in the member's own browser, where the VNAS
window lives. Each call goes the companion's way (anas_window_call.py) to the product
service, which passes it to the VNAS window of the conversation that asked; the window
reads the page as a list of controls, acts on one by its ref or draws a picture of the
page, in the member's session, and answers. What a step may change is decided there: the
window holds the page's writes that delete, move, share or change settings until the member
allows them.

Hermes runs this server with `trust: full` (managed-config.yaml): its own approval would ask
before every click, while the window asks only for the steps that need it, on the device the
member is using. As with the companion's server, every answer is a result and never an
error, so that Hermes's circuit breaker never counts a desktop that is not there, and the
server is the SDK's low-level one, which passes the model's arguments on as written.

These are not Hermes's browser tools: those drive a browser over the Chrome DevTools
Protocol, which a member's browser on a phone or a television does not offer, and which
would let the model run code in the page.
"""
import json

import anyio
import anyio.to_thread
from mcp.server import Server
from mcp.server.stdio import stdio_server
from mcp_types import CallToolResult, ImageContent, ListToolsResult, TextContent, Tool

import anas_window_call

DESKTOP = anas_window_call.SOCKET
# The product service waits for the window up to 30 s (the member has 25 s to allow a step),
# and for the turn's own tool.start 1.5 s before that; Hermes gives up on this server after
# 120 s, counted from when the call queued behind the server's other calls.
TIMEOUT = 40

# The product service reads a call as far as 4000 bytes of typed text can take in JSON
# (maxDesktopCall, internal/assistantapi/desktop.go).
TOO_LONG = {"status": "rejected", "reason": "bad-request", "message": "参数太长：一次输入最多 4000 字节"}

SAID = {
    "failed": "这一步没有完成",
    "rejected": "参数不对",
    "cancelled": "这一步没有进行：这一轮已经停止，或窗口已经关闭。",
    "no_desktop": "没有在用的桌面：这段对话不在成员的 VNAS 窗口里进行（例如定时任务，或成员关掉了窗口），"
                  "不能操作桌面。告诉成员需要在 VNAS 窗口里对话。",
    "busy": "桌面上的操作太频繁，稍后再试。",
    "timeout": "桌面没有及时回应，不知道这一步做了没有；先用 snapshot 看看。",
}

TEXT = {"type": "string"}
FLAG = {"type": "boolean"}
DIRECTION = {"type": "string", "enum": ["up", "down"]}

# Each tool: what the model is told, its arguments with their schemas, and the ones it must give.
TOOLS = {
    "snapshot": ("查看成员的 WebOS 桌面：每个打开的窗口一行，下面是它的控件，控件带 [ref=eN]。"
                 "页面变了就再查看一次，旧的 ref 可能失效。full：true 时给出完整文字。",
                 {"full": FLAG}, []),
    "click": ("点击桌面上的一个控件。ref：snapshot 或 look 给出的编号，例如 e12。", {"ref": TEXT}, ["ref"]),
    "type": ("在一个输入框里输入文字，替换原有内容。ref：输入框的编号；text：要输入的文字。",
             {"ref": TEXT, "text": TEXT}, ["ref", "text"]),
    "press": ("在上一次操作的控件上按一个键：Enter、Escape、Tab、Space、ArrowUp、ArrowDown、ArrowLeft、ArrowRight、"
              "PageUp、PageDown、Home、End；"
              "Backspace 和 Delete 只能在输入框里。key：键名。", {"key": TEXT}, ["key"]),
    "scroll": ("滚动上一次操作所在的区域。direction：up 或 down。", {"direction": DIRECTION}, ["direction"]),
    "look": ("取成员桌面此刻的画面（不含 VNAS 自己的窗口）：快照里看不出来的，例如图片里是什么、颜色、布局。"
             "返回一张图片；结果里有它的路径，用 vision_analyze 读这个路径并写清要看什么，才能看到画面。"
             "annotate：true 时画面上每个控件标着它的 ref，看完可以直接用这个 ref 点击。",
             {"annotate": FLAG}, []),
}


def ask(tool: str, args: dict) -> dict:
    """The product service's answer to one call, or what to say without it."""
    return anas_window_call.ask(DESKTOP, "/desktop/call", tool, args, TIMEOUT, TOO_LONG)


def tools() -> list[Tool]:
    return [Tool(name=name, description=description,
                 input_schema={"type": "object", "properties": properties, "required": required})
            for name, (description, properties, required) in TOOLS.items()]


def refused(data: dict) -> str:
    """One line for a step that was not taken; the model reads only this text."""
    status = data["status"]
    said = SAID.get(status, f"桌面回答：{status}")
    message = data.get("message")
    if status in ("failed", "rejected") and isinstance(message, str) and message.strip():
        said += f"：{message.strip()}"
    if not said.endswith("。"):
        said += "。"
    return said


def picture(result: dict) -> list:
    """A picture of the page as an image the assistant passes to vision_analyze."""
    data, kind = result.get("data"), result.get("mime_type")
    if not isinstance(data, str) or not data or kind != "image/png":
        return [TextContent(type="text", text="桌面的画面没有取到。")]
    said = "成员桌面此刻的画面如下；用 vision_analyze 看它，并写清要看什么。"
    annotations = result.get("annotations")
    labels = [f"{label['ref']} {label.get('name') or ''}".strip() for label in (annotations if isinstance(annotations, list) else [])
              if isinstance(label, dict) and isinstance(label.get("ref"), str) and label["ref"]]
    if labels:
        said += "画面上标出的控件：" + "；".join(labels) + "。"
    return [TextContent(type="text", text=said), ImageContent(type="image", data=data, mime_type=kind)]


def shown(tool: str, result) -> list:
    """What a step returned, as the model reads it."""
    if not isinstance(result, dict):
        result = {}
    if tool == "look":
        return picture(result)
    if tool == "snapshot" and isinstance(result.get("snapshot"), str):
        return [TextContent(type="text", text=result["snapshot"])]
    return [TextContent(type="text", text=json.dumps(result, ensure_ascii=False) if result else "做了。")]


def call(tool: str, arguments: dict | None) -> CallToolResult:
    # Only what the model gave of the tool's own arguments: the product service matches the
    # call with the arguments Hermes reported when the tool started, less nulls and keys the
    # tool does not take.
    properties = TOOLS[tool][1] if tool in TOOLS else {}
    given = {key: value for key, value in (arguments or {}).items() if key in properties and value is not None}
    data = ask(tool, given)
    status = data["status"]
    if status == "ok":
        # Nothing but what the step returned: Hermes passes structured content on beside the
        # text, and the model reads a snapshot better without it.
        return CallToolResult(content=shown(tool, data.get("result")), is_error=False)
    summary = {key: data[key] for key in ("status", "reason", "message") if key in data}
    return CallToolResult(content=[TextContent(type="text", text=refused(data))], structured_content=summary, is_error=False)


async def list_tools(_context, _params) -> ListToolsResult:
    return ListToolsResult(tools=tools())


async def call_tool(_context, params) -> CallToolResult:
    # ask blocks on the socket; the server goes on answering meanwhile.
    return await anyio.to_thread.run_sync(call, params.name, params.arguments)


server = Server("desktop", instructions=(
    "成员正在用的 A-NAS WebOS 桌面，就是 VNAS 窗口所在的那个页面，用成员自己的登录。"
    "不是独立的浏览器：不能打开网址，也不会离开这个桌面。"
    "没有在用的桌面（例如定时任务）时什么都不会发生，结果会如实说明。"),
    on_list_tools=list_tools, on_call_tool=call_tool)


async def main():
    async with stdio_server() as (read, write):
        await server.run(read, write, server.create_initialization_options())


if __name__ == "__main__":
    anyio.run(main)
