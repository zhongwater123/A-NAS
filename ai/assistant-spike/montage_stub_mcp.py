"""剪辑中心规划工具的替身（Hermes 探针 P1，docs/research/montage-hermes-probe.md）。

剪辑中心的 montage MCP 服务器还没写；探针只需要一个名为 montage、带同样四个工具名的
stdio 服务器，看 Hermes 把哪些工具交给规划这一轮、调用能不能到达服务器。每次调用在
助手目录写一行（/nas/助手/montage-stub.log），参数里有 sleep 秒数时先睡这么久，用来在
运行中途杀掉单元。

不在定时任务单元里（ANAS_ASSISTANT_CHANNEL 不是 cron）时一个工具也不列，看 Hermes 怎样
对待零工具的服务器：方案里对话实例也会起这个子进程，但那里没有剪辑的套接字。
"""

import json
import os
import time

import anyio
import anyio.to_thread
from mcp.server import Server
from mcp.server.stdio import stdio_server
from mcp_types import CallToolResult, ListToolsResult, TextContent, Tool

LOG = os.environ.get("MONTAGE_STUB_LOG", "/nas/助手/montage-stub.log")
# 方案里剪辑的套接字只绑定进定时任务单元；探针给那个单元绑一个占位文件，别处没有。
SOCKET = "/run/anas-assistant-montage/montage.sock"
CRON = os.path.exists(SOCKET) or os.environ.get("ANAS_ASSISTANT_CHANNEL") == "cron"
ARGS = {
    "type": "object",
    "properties": {
        "jobId": {"type": "string"},
        "attemptToken": {"type": "string"},
        "sleep": {"type": "number"},
        "answer": {"type": "object"},
    },
}
NAMES = ["montage_brief", "montage_submit_outline", "montage_shots_brief", "montage_submit_shots"]


def note(entry):
    with open(LOG, "a", encoding="utf-8") as log:
        log.write(json.dumps({"t": round(time.time(), 3), "pid": os.getpid(), **entry}, ensure_ascii=False) + "\n")


def call(name, arguments):
    arguments = arguments or {}
    note({"call": name, "args": arguments})
    if isinstance(arguments.get("sleep"), int | float):
        time.sleep(min(float(arguments["sleep"]), 600))
    data = {"status": "accepted", "tool": name}
    return CallToolResult(content=[TextContent(type="text", text=json.dumps(data))], structured_content=data, is_error=False)


async def list_tools(_context, _params) -> ListToolsResult:
    tools = [Tool(name=n, description=f"替身：{n}", input_schema=ARGS) for n in NAMES] if CRON else []
    note({"list_tools": len(tools), "channel": os.environ.get("ANAS_ASSISTANT_CHANNEL", "")})
    return ListToolsResult(tools=tools)


async def call_tool(_context, params) -> CallToolResult:
    return await anyio.to_thread.run_sync(call, params.name, params.arguments)


server = Server("montage", instructions="剪辑中心规划工具的替身。", on_list_tools=list_tools, on_call_tool=call_tool)


async def main():
    note({"start": True, "channel": os.environ.get("ANAS_ASSISTANT_CHANNEL", "")})
    async with stdio_server() as (read, write):
        await server.run(read, write, server.create_initialization_options())


if __name__ == "__main__":
    anyio.run(main)
