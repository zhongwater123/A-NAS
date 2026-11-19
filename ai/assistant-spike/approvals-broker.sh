#!/usr/bin/env bash
# What the executor of the assistant's changes applies, driven directly (no Hermes): only a
# change the member approved, once, from the copy the member saw, and only for that member.
# The agent side runs as the member's UID, like the MCP tool in the sandbox. The executor is
# the spike's change_broker.py, or with EXECUTOR=host-agent the Host Agent's File Broker
# (broker_client.py speaks to either).
set -uo pipefail
python3 - <<'PY'
import json, os, pwd, subprocess, sys

sys.path.insert(0, "/spike")
from broker_client import product as call_product

DATA = "/srv/a-nas/data"
STAGED = f"{DATA}/assistant/alice/files/待确认/个人空间/计划.md"
PLAN = f"{DATA}/spaces/private/alice/计划.md"


def product(method, path, body=None):
    # The member's session (BROKER_SESSION), when the executor checks it (the session gate).
    return call_product(method, path + ("&" if "?" in path else "?") + "account=alice", body or {})


AGENT = r'''
import http.client, json, socket, sys
class UDS(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX); self.sock.connect("/run/anas-change-agent/agent.sock")
method, path, body = sys.argv[1], sys.argv[2], sys.argv[3]
c = UDS("uds"); c.request(method, path, body.encode() if body else None)
r = c.getresponse(); print(r.status, r.read().decode())
'''


def agent(user, method, path, body=None):
    out = subprocess.run(["setpriv", f"--reuid={user}", f"--regid={user}", "--groups=20000", "--", "python3", "-c", AGENT,
                          method, path, json.dumps(body, ensure_ascii=False) if body else ""], capture_output=True, text=True)
    return (out.stdout or out.stderr).strip()


def as_member(user, cmd):
    subprocess.run(["setpriv", f"--reuid={user}", f"--regid={user}", "--groups=20000", "--", "sh", "-c", cmd], check=True)


def as_alice(cmd):
    as_member("alice", cmd)


def check(name, ok, detail=""):
    print(f"{'PASS' if ok else 'FAIL'} {name}" + (f" — {detail}" if detail else ""))


write = {"summary": "计划加一项", "operations": [{"op": "write", "path": "/nas/个人空间/计划.md", "source": "/nas/助手/待确认/个人空间/计划.md"}]}
delete = {"summary": "删除 secret.txt", "operations": [{"op": "delete", "path": "/nas/个人空间/secret.txt"}]}

as_alice(f"mkdir -p '{os.path.dirname(STAGED)}' && printf '# 计划\\n- 买菜\\n- 看牙医\\n' > '{STAGED}'")
status, change = product("POST", "/changes/prepare", write)
check("prepare a write shows the staged text", status == 200 and "看牙医" in change["operations"][0].get("after", ""), str(status))
as_alice(f"printf '# 计划\\n- 全部删光\\n' > '{STAGED}'")
product("POST", f"/changes/{change['id']}/approve")
result = agent("alice", "POST", "/apply", write)
written = open(PLAN, encoding="utf-8").read()
check("applies the copy the member saw, not the staged file rewritten after the card", "看牙医" in written and "删光" not in written, result)
check("the written file belongs to the member", os.stat(PLAN).st_uid == pwd.getpwnam("alice").pw_uid)
result = agent("alice", "POST", "/apply", write)
check("the same approval applies only once", "没有得到用户本人的确认" in result, result)

status, change = product("POST", "/changes/prepare", delete)
result = agent("alice", "POST", "/apply", delete)
check("an unapproved change is not applied", "没有得到用户本人的确认" in result and os.path.exists(f"{DATA}/spaces/private/alice/secret.txt"), result)
result = agent("alice", "POST", f"/changes/{change['id']}/approve")
check("the agent socket cannot approve", result.startswith("404"), result)
product("POST", f"/changes/{change['id']}/approve")
result = agent("bob", "POST", "/apply", delete)
check("another member's assistant cannot use alice's approval", "已执行" not in result
      and os.path.exists(f"{DATA}/spaces/private/bob/bob-secret.txt") and os.path.exists(f"{DATA}/spaces/private/alice/secret.txt"), result)
product("POST", f"/changes/{change['id']}/expire")
result = agent("alice", "POST", "/apply", delete)
check("an expired approval is not applied", "没有得到用户本人的确认" in result, result)

as_alice(f"printf '# 计划\\n- 买菜\\n- 交电费\\n' > '{STAGED}'")
status, change = product("POST", "/changes/prepare", write)
product("POST", f"/changes/{change['id']}/approve")
as_alice(f"echo '- 本人刚加的' >> '{PLAN}'")
result = agent("alice", "POST", "/apply", write)
check("a target the member changed after the card is not overwritten", "被改动过" in result and "本人刚加的" in open(PLAN, encoding="utf-8").read(), result)

status, change = product("POST", "/changes/prepare", {"summary": "x", "operations": [{"op": "delete", "path": "/nas/个人空间/../../共享空间/family.md"}]})
check("a path with .. is refused at prepare", status == 422, json.dumps(change, ensure_ascii=False))
as_alice(f"ln -sf /etc/shadow '{os.path.dirname(STAGED)}/影子.md'")
status, change = product("POST", "/changes/prepare", {"summary": "x", "operations": [{"op": "write", "path": "/nas/个人空间/影子.md", "source": "/nas/助手/待确认/个人空间/影子.md"}]})
check("a staged link is not followed", status == 422, json.dumps(change, ensure_ascii=False)[:200])
status, change = product("POST", "/changes/prepare", {"summary": "x", "operations": [{"op": "move", "path": "/nas/个人空间/secret.txt", "to": "/nas/个人空间/计划.md"}]})
check("a move onto an existing file is refused", status == 422, json.dumps(change, ensure_ascii=False)[:200])

# Links the member made (web terminal, SMB) in the middle of a path: nothing is read or written through them.
as_alice(f"printf '# 计划\\n- 买菜\\n' > '{STAGED}' && ln -s /etc '{DATA}/spaces/private/alice/系统' && ln -s /etc '{os.path.dirname(STAGED)}/系统'")
status, change = product("POST", "/changes/prepare", {"summary": "x", "operations": [{"op": "write", "path": "/nas/个人空间/系统/shadow", "source": "/nas/助手/待确认/个人空间/计划.md"}]})
check("a target behind a linked folder is refused and not read", status == 422 and "root:" not in json.dumps(change), json.dumps(change, ensure_ascii=False)[:200])
status, change = product("POST", "/changes/prepare", {"summary": "x", "operations": [{"op": "write", "path": "/nas/个人空间/新.md", "source": "/nas/助手/待确认/个人空间/系统/shadow"}]})
check("a staged file behind a linked folder is refused and not read", status == 422 and "root:" not in json.dumps(change), json.dumps(change, ensure_ascii=False)[:200])
as_alice(f"mkdir '{DATA}/spaces/private/alice/子目录'")
sub = {"summary": "x", "operations": [{"op": "write", "path": "/nas/个人空间/子目录/a.md", "source": "/nas/助手/待确认/个人空间/计划.md"}]}
status, change = product("POST", "/changes/prepare", sub)
product("POST", f"/changes/{change['id']}/approve")
as_alice(f"rmdir '{DATA}/spaces/private/alice/子目录' && ln -s {DATA}/spaces/shared '{DATA}/spaces/private/alice/子目录'")
result = agent("alice", "POST", "/apply", sub)
check("a folder swapped for a link to Shared after the card is not written through", "已执行" not in result
      and not os.path.exists(f"{DATA}/spaces/shared/a.md"), result)

# copy: a file or folder from wherever the assistant reads, nothing staged; applying copies it as the member.
ALICE, MINE, OWN = pwd.getpwnam("alice").pw_uid, f"{DATA}/spaces/private/alice", f"{DATA}/assistant/alice/files"


def copy(path, source):
    return {"summary": "复制", "operations": [{"op": "copy", "path": path, "source": source}]}


def approved(sub):
    """Prepare, approve and apply a submission; the card's first operation and the reply."""
    status, change = product("POST", "/changes/prepare", sub)
    if status != 200:
        return {}, f"{status} {json.dumps(change, ensure_ascii=False)[:200]}"
    product("POST", f"/changes/{change['id']}/approve")
    return change["operations"][0], agent("alice", "POST", "/apply", sub)


def text(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def leftovers(folder):
    return [name for name in os.listdir(folder) if name.startswith(".a-nas-assistant-")]


family = f"{DATA}/spaces/shared/family.md"
op, result = approved(copy("/nas/个人空间/复制/家庭.md", "/nas/共享空间/family.md"))
check("prepare a copy from Shared shows where from and its size", op.get("from") == "/nas/共享空间/family.md"
      and op.get("size") == os.path.getsize(family) and not op.get("folder") and op.get("exists") is False, json.dumps(op, ensure_ascii=False))
copied = f"{MINE}/复制/家庭.md"
check("applies the copy as the member, into a new folder", "已执行" in result and os.path.exists(copied)
      and text(copied) == text(family) and os.stat(copied).st_uid == ALICE, result)
as_alice(f"printf '# 计划\\n- 复制来的新版本\\n' > '{STAGED}'")
old = text(copied)
op, result = approved(copy("/nas/个人空间/复制/家庭.md", "/nas/助手/待确认/个人空间/计划.md"))
check("a copy that replaces a file moves the old one to the recycle bin", "已执行" in result and op.get("exists") is True
      and "复制来的新版本" in text(copied) and text(f"{MINE}/.a-nas-trash/alice/复制/家庭.md") == old, result)

as_alice(f"mkdir -p '{OWN}/素材/子' && printf aa > '{OWN}/素材/a.txt' && printf bbbb > '{OWN}/素材/子/b.txt'")
folder_copy = copy("/nas/个人空间/素材", "/nas/助手/素材")
op, result = approved(folder_copy)
check("prepare a folder copy shows a folder with the size of its files", op.get("folder") is True and op.get("size") == 6,
      json.dumps(op, ensure_ascii=False))
check("applies a folder copy as the member", "已执行" in result and text(f"{MINE}/素材/子/b.txt") == "bbbb"
      and os.stat(f"{MINE}/素材/子").st_uid == ALICE and not leftovers(MINE), result)
status, change = product("POST", "/changes/prepare", folder_copy)
check("a folder is not copied over an existing one", status == 422, json.dumps(change, ensure_ascii=False)[:200])
status, change = product("POST", "/changes/prepare", copy("/nas/个人空间/复制/家庭.md", "/nas/助手/素材"))
check("a folder is not copied over a file", status == 422 and "已经存在" in change.get("error", ""), json.dumps(change, ensure_ascii=False)[:200])
status, change = product("POST", "/changes/prepare", copy("/nas/个人空间/素材", "/nas/共享空间/family.md"))
check("a file is not copied over a folder", status == 422 and "不是普通文件" in change.get("error", ""), json.dumps(change, ensure_ascii=False)[:200])

status, change = product("POST", "/changes/prepare", copy("/nas/个人空间/passwd", "/etc/passwd"))
check("a source outside what the assistant reads is refused", status == 422 and "只能复制" in change.get("error", ""), json.dumps(change, ensure_ascii=False)[:200])
as_alice(f"ln -sfn ../共享空间/family.md '{MINE}/链接'")
status, change = product("POST", "/changes/prepare", copy("/nas/个人空间/链接副本", "/nas/个人空间/链接"))
check("a link is not copied", status == 422 and "不能复制" in change.get("error", ""), json.dumps(change, ensure_ascii=False)[:200])
as_alice(f"mkdir -p '{OWN}/带链接' && echo x > '{OWN}/带链接/a.txt' && ln -sfn /etc '{OWN}/带链接/系统'")
op, result = approved(copy("/nas/个人空间/带链接", "/nas/助手/带链接"))
check("a folder with a link inside is not copied, and nothing is left", "已执行" not in result and not os.path.exists(f"{MINE}/带链接")
      and not leftovers(MINE), result)
as_member("bob", f"echo bob-only > '{DATA}/spaces/shared/bob-only.txt' && chmod 600 '{DATA}/spaces/shared/bob-only.txt'")
op, result = approved(copy("/nas/个人空间/bob-only.txt", "/nas/共享空间/bob-only.txt"))
check("a file the member cannot read is not copied", "已执行" not in result and not os.path.exists(f"{MINE}/bob-only.txt"), result)

as_alice(f"printf v1 > '{OWN}/素材/a.txt'")
sub = copy("/nas/个人空间/a副本.txt", "/nas/助手/素材/a.txt")
status, change = product("POST", "/changes/prepare", sub)
product("POST", f"/changes/{change['id']}/approve")
as_alice(f"printf 'v2, rewritten after the card' > '{OWN}/素材/a.txt'")
result = agent("alice", "POST", "/apply", sub)
check("a source changed after the card is not copied", "被改动过" in result and not os.path.exists(f"{MINE}/a副本.txt"), result)

# Refused by its size and paths before any file is looked at: at most 100 operations and 64 KiB
# (summary and paths in the File Broker's JSON, in bytes), never a .a-nas-* system name, a move
# only within the personal space or within Agent空间/<account>.


def card(sub):
    """Prepare a submission and reject its card, so it is never applied; status and reply."""
    status, change = product("POST", "/changes/prepare", sub)
    if status == 200:
        product("POST", f"/changes/{change['id']}/reject")
    return status, change if isinstance(change, dict) else {"error": str(change)}


def said(change):
    return json.dumps(change, ensure_ascii=False)[:300]


folders = {"summary": "建文件夹", "operations": [{"op": "mkdir", "path": f"/nas/个人空间/批量/{n}"} for n in range(101)]}
status, change = card(folders)
check("more than 100 operations are refused", status == 422 and "一批最多 100 项操作，这批有 101 项" in change.get("error", ""), said(change))
folders["operations"].pop()
status, change = card(folders)
check("100 operations are one card", status == 200 and len(change.get("operations", [])) == 100, said(change))
one = [{"op": "mkdir", "path": "/nas/个人空间/说明很长"}]
bare = len(json.dumps({"summary": "", "operations": one}, ensure_ascii=False, separators=(",", ":")).encode())
status, change = card({"summary": "a" * (65536 - bare), "operations": one})
check("a submission of exactly 64 KiB is one card", status == 200, str(status))
status, change = card({"summary": "a" * (65537 - bare), "operations": one})
check("one byte more is refused", status == 422 and "说明与路径合计超过 64 KiB" in change.get("error", ""), said(change))
status, change = card({"summary": "改" * 22000, "operations": one})
check("the size is in bytes: 22000 Chinese characters are over", status == 422 and "64 KiB" in change.get("error", ""), said(change))

status, change = card({"summary": "x", "operations": [{"op": "delete", "path": "/nas/个人空间/.a-nas-trash"}]})
check("the recycle bin is not a target", status == 422 and "/nas/个人空间/.a-nas-trash：不能更改系统文件夹" in change.get("error", ""), said(change))
status, change = card({"summary": "x", "operations": [
    {"op": "write", "path": "/nas/共享空间/Agent空间/alice/子/.a-nas-x/a.md", "source": "/nas/助手/待确认/个人空间/计划.md"},
    {"op": "move", "path": "/nas/个人空间/计划.md", "to": "/nas/个人空间/.a-nas-trash/alice/计划.md"}]})
error = change.get("error", "")
check("nothing is written or moved into a system folder", status == 422 and "第 1 项：/nas/共享空间/Agent空间/alice/子/.a-nas-x/a.md：不能更改系统文件夹" in error
      and "第 2 项：/nas/个人空间/.a-nas-trash/alice/计划.md：不能更改系统文件夹" in error, said(change))

across = {"summary": "挪到 Agent空间", "operations": [
    {"op": "move", "path": "/nas/个人空间/计划.md", "to": "/nas/共享空间/Agent空间/alice/计划.md"},
    {"op": "move", "path": "/nas/共享空间/Agent空间/alice/report.md", "to": "/nas/个人空间/report.md"}]}
status, change = card(across)
error = change.get("error", "")
check("a move between the personal space and Agent空间 is refused, either way", status == 422 and os.path.exists(PLAN)
      and "第 1 项：/nas/个人空间/计划.md 与 /nas/共享空间/Agent空间/alice/计划.md 不在同一个空间，不能移动：先 copy 到新位置，再 delete 原来的" in error
      and "第 2 项：/nas/共享空间/Agent空间/alice/report.md 与 /nas/个人空间/report.md 不在同一个空间" in error, said(change))
result = agent("alice", "POST", "/apply", across)
check("the assistant reads why, and to copy then delete", "什么都没有改变" in result and "先 copy 到新位置，再 delete 原来的" in result, result)
status, change = card({"summary": "x", "operations": [{"op": "move", "path": "/nas/个人空间/secret.txt", "to": "/nas/个人空间/归档/secret.txt"}]})
check("a move within the personal space is still one card", status == 200, said(change))
state ="/srv/a-nas/data/assistant/.changes" if os.environ.get("EXECUTOR") == "host-agent" else "/var/lib/anas-spike/changes"
print("INFO state dir mode", oct(os.stat(state).st_mode & 0o777))
PY
