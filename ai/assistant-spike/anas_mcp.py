"""Stub of the A-NAS MCP tool "提交更改" for the spike.

The real tool lives in the product service and turns a submission into a
Pending Change the member confirms. Here it only checks the targets the same
way and records the proposal next to the staging area, so the spike can see
what the model submitted.
"""
import getpass
import hashlib
import json
import time
from pathlib import Path

from mcp.server import MCPServer

ACCOUNT = getpass.getuser()
PERSONAL = Path("/nas/个人空间")
AGENT_SPACE = Path("/nas/共享空间/Agent空间") / ACCOUNT
STAGING = Path("/nas/助手/待确认")
RECORD = Path("/nas/助手/.spike-proposals.jsonl")

server = MCPServer("anas", instructions=(
    "A-NAS 文件更改。个人空间与本人的 Agent空间子目录只能经 submit_changes 提交，"
    "由用户本人确认后才执行；共享空间的其他位置不能写。"))


def inside(path: Path, root: Path) -> bool:
    try:
        path.resolve(strict=False).relative_to(root.resolve(strict=False))
        return True
    except ValueError:
        return False


def writable_target(raw: str) -> str | None:
    path = Path(raw)
    if not path.is_absolute():
        return f"{raw}: 需要绝对路径"
    if inside(path, PERSONAL) or inside(path, AGENT_SPACE):
        return None
    return f"{raw}: 只能更改 {PERSONAL} 或 {AGENT_SPACE} 中的文件"


@server.tool()
def submit_changes(summary: str, operations: list[dict]) -> str:
    """把对个人空间或本人 Agent空间子目录的一批文件更改提交给用户确认；确认前什么都不会改变。

    summary: 用一句话告诉用户这批更改做什么。
    operations: 按顺序执行的操作，每项为一个对象：
      {"op": "write", "path": 目标文件, "source": 暂存区 /nas/助手/待确认/ 中写好完整新内容的文件}
      {"op": "delete", "path": 要删除的文件或文件夹（进入回收站）}
      {"op": "move", "path": 原路径, "to": 新路径}（移动或改名）
      {"op": "mkdir", "path": 新文件夹}
    """
    errors, recorded = [], []
    for i, op in enumerate(operations, 1):
        kind = op.get("op")
        if kind not in ("write", "delete", "move", "mkdir"):
            errors.append(f"第 {i} 项：未知操作 {kind!r}")
            continue
        for key in ("path", "to") if kind == "move" else ("path",):
            if problem := writable_target(str(op.get(key, ""))):
                errors.append(f"第 {i} 项：{problem}")
        if kind == "write":
            source = Path(str(op.get("source", "")))
            if not inside(source, STAGING) or not source.is_file():
                errors.append(f"第 {i} 项：source 必须是 {STAGING} 中已写好的文件")
                continue
            op = {**op, "sha256": hashlib.sha256(source.read_bytes()).hexdigest()}
        if kind in ("delete", "move") and not Path(str(op.get("path", ""))).exists():
            errors.append(f"第 {i} 项：{op.get('path')} 不存在")
        recorded.append(op)
    if errors:
        return "提交被拒绝：\n" + "\n".join(errors)
    number = int(time.time() * 1000) % 100000
    with RECORD.open("a", encoding="utf-8") as out:
        out.write(json.dumps({"id": number, "summary": summary, "operations": recorded}, ensure_ascii=False) + "\n")
    return f"已提交 {len(recorded)} 项更改（编号 {number}），等待用户在确认卡片中确认；确认前不会生效。"


if __name__ == "__main__":
    import anyio
    anyio.run(server.run_stdio_async)
