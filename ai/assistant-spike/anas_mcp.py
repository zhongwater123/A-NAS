"""Stub of the A-NAS MCP tools for the spike.

In A-NAS the MCP endpoint is the product service. Here a stdio server inside
the sandbox forwards each call to the change broker's agent socket, which
knows the member by the caller's UID and decides everything: it applies a
submission only when the member approved exactly that call.

Hermes runs this server with `trust: untrusted`, so every call of a tool
without readOnlyHint waits for the member's in-turn approval first.
"""
import http.client
import json
import socket

from mcp.server import MCPServer
from mcp_types import ToolAnnotations

BROKER = "/run/anas-change-agent/agent.sock"

server = MCPServer("anas", instructions=(
    "A-NAS 文件更改。个人空间与本人的 Agent空间子目录只能经 submit_changes 更改，"
    "用户在对话中同意后才执行；共享空间的其他位置不能写。"))


class Broker(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(120)
        self.sock.connect(BROKER)


def broker(method: str, path: str, body: dict | None = None) -> str:
    conn = Broker("broker")
    conn.request(method, path, json.dumps(body, ensure_ascii=False).encode() if body is not None else None,
                 {"Content-Type": "application/json"})
    return conn.getresponse().read().decode()


@server.tool()
def submit_changes(summary: str, operations: list[dict]) -> str:
    """更改个人空间或本人 Agent空间子目录中的文件。调用时用户会在对话里看到这批更改并决定是否同意；同意后立即执行并返回结果，拒绝或没有回答时什么都不会改变。

    summary: 用一句话告诉用户这批更改做什么。
    operations: 按顺序执行的操作，每项为一个对象：
      {"op": "write", "path": 目标文件, "source": 暂存区 /nas/助手/待确认/ 中写好完整新内容的文件}
      {"op": "delete", "path": 要删除的文件或文件夹（进入回收站）}
      {"op": "move", "path": 原路径, "to": 新路径}（移动或改名）
      {"op": "mkdir", "path": 新文件夹}
    """
    return broker("POST", "/apply", {"summary": summary, "operations": operations})


@server.tool(annotations=ToolAnnotations(read_only_hint=True))
def recent_changes() -> str:
    """列出最近提交给用户的文件更改，以及每一批是已执行、被拒绝还是没有确认。"""
    return broker("GET", "/recent")


if __name__ == "__main__":
    import anyio
    anyio.run(server.run_stdio_async)
