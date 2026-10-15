#!/usr/bin/env bash
# What the change broker applies, driven directly (no Hermes): only a change the
# member approved, once, from the copy the member saw, and only for that member.
# The agent side runs as the member's UID, like the MCP tool in the sandbox.
set -uo pipefail
python3 - <<'PY'
import http.client, json, os, pwd, socket, subprocess

DATA = "/srv/a-nas/data"
STAGED = f"{DATA}/assistant/alice/files/待确认/个人空间/计划.md"
PLAN = f"{DATA}/spaces/private/alice/计划.md"


class UDS(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("uds", timeout=60)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.connect(self.path)


def product(method, path, body=None):
    conn = UDS("/run/anas-spike-broker/broker.sock")
    conn.request(method, path + ("&" if "?" in path else "?") + "account=alice", json.dumps(body or {}, ensure_ascii=False).encode())
    resp = conn.getresponse()
    return resp.status, json.loads(resp.read() or b"{}")


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


def as_alice(cmd):
    subprocess.run(["setpriv", "--reuid=alice", "--regid=alice", "--groups=20000", "--", "sh", "-c", cmd], check=True)


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
print("INFO state dir mode", oct(os.stat("/var/lib/anas-spike/changes").st_mode & 0o777))
PY
