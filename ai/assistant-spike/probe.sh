#!/bin/bash
# Runs inside anas-assistant-probe@alice.service: the same sandbox as the
# assistant, with this script in place of Hermes.
ok()  { echo "PASS $1"; }
bad() { echo "FAIL $1${2:+: $2}"; }
expect_ok()   { local n=$1; shift; if out=$("$@" 2>&1); then ok "$n"; else bad "$n" "$out"; fi; }
expect_fail() { local n=$1; shift; if out=$("$@" 2>&1); then bad "$n" "succeeded: $out"; else ok "$n (${out##*: })"; fi; }

echo "INFO id: $(id)"
echo "INFO caps: $(grep CapEff /proc/self/status)"
# ADR 0022 decision 2: the member's own group and a-nas-users. For an administrator systemd
# also adds a-nas-admins (User= applies the account's groups from /etc/group, and
# SupplementaryGroups= can only add), so what holds is that nothing the sandbox reaches grants
# that group (PROBE_IDENTITY_ONLY, run by system-test.sh for its administrator). Checked: the
# spaces, Hermes's home, /run, the assistant's policy and config, and /opt/a-nas, as owning
# group, ACL entry or default ACL entry.
if [ -n "${PROBE_IDENTITY_ONLY:-}" ]; then
  expect_fail "nothing in the sandbox grants a-nas-admins" sh -c \
    'getfacl -R -p /nas /var/lib/hermes-home /run /etc/hermes /etc/a-nas-assistant /opt/a-nas 2>/dev/null |
      grep -Eq "^(# group: |(default:)?group:)a-nas-admins(:|\$)"'
  exit 0
fi
expect_fail "no a-nas-admins in the sandbox"     sh -c 'id -G | tr " " "\n" | grep -qx 20001'
expect_ok   "read own personal file"            cat /nas/个人空间/计划.md
expect_ok   "read own trash entry"               cat /nas/个人空间/.a-nas-trash/alice/old.txt
expect_ok   "read shared file"                   cat /nas/共享空间/family.md
expect_ok   "read another member's Agent space"  cat /nas/共享空间/Agent空间/bob/report.md
expect_fail "read another member's shared trash" cat /nas/共享空间/.a-nas-trash/bob/gone.txt
expect_ok   "recursive file search over Shared"  sh -c 'rg --files -g "*" -- /nas/共享空间 >/dev/null; [ $? -le 1 ]'
expect_ok   "recursive file search over personal" sh -c 'rg --files -g "*" -- /nas/个人空间 >/dev/null; [ $? -le 1 ]'
echo "INFO shared bin as seen: $(ls -A /nas/共享空间/.a-nas-trash | tr '
' ' ')"
if [ -z "$(ls -A /srv 2>/dev/null)" ]; then ok "data volume hidden (/srv empty)"; else bad "data volume hidden" "$(ls -A /srv)"; fi
found=$(find / -xdev \( -name bob-secret.txt -o -name 'snap-1' -o -name '.a-nas-snapshots' \) 2>/dev/null | head -3)
if [ -z "$found" ]; then ok "no other member's space or snapshot anywhere"; else bad "other space visible" "$found"; fi
expect_fail "write into personal space"          sh -c 'echo x > /nas/个人空间/new.txt'
expect_fail "delete in personal space"           rm /nas/个人空间/secret.txt
expect_fail "rename in personal space"           mv /nas/个人空间/secret.txt /nas/个人空间/s2.txt
expect_fail "chmod in personal space"            chmod 0644 /nas/个人空间/secret.txt
expect_fail "setfacl in personal space"          setfacl -m o::r /nas/个人空间/secret.txt
expect_fail "write into shared space"            sh -c 'echo x > /nas/共享空间/x.txt'
expect_fail "write into own Agent space directly" sh -c 'echo x > /nas/共享空间/Agent空间/alice/x.txt'
expect_ok   "write into assistant directory"     sh -c 'mkdir -p /nas/助手/待确认 && echo x > /nas/助手/待确认/x.txt'
expect_fail "read the gateway's model key"       cat /etc/anas-spike/model.key
expect_fail "read its own serve session token"  cat /etc/a-nas-assistant/tokens/alice.env
expect_fail "read another member's session token" cat /etc/a-nas-assistant/tokens/bob.env
expect_fail "reach the change broker's product socket" python3 -c 'import socket; socket.socket(socket.AF_UNIX).connect("/run/anas-spike-broker/broker.sock")'
expect_ok   "reach the change broker's agent socket"   python3 -c 'import socket; socket.socket(socket.AF_UNIX).connect("/run/anas-change-agent/agent.sock")'
expect_fail "change the Hermes user config"      sh -c 'echo x >> /var/lib/hermes-home/config.yaml'
expect_fail "write the Hermes .env"              sh -c 'echo x >> /var/lib/hermes-home/.env'
expect_fail "rewrite SOUL.md"                    sh -c 'echo x >> /var/lib/hermes-home/SOUL.md'
expect_fail "add a Hermes hook"                  mkdir /var/lib/hermes-home/hooks/x
expect_fail "add a Hermes plugin"                mkdir /var/lib/hermes-home/plugins/x
expect_fail "add a Hermes profile"               mkdir /var/lib/hermes-home/profiles/x
expect_fail "set a sticky Hermes profile"        sh -c 'echo x > /var/lib/hermes-home/active_profile'
expect_ok   "write Hermes state"                 sh -c 'echo x > /var/lib/hermes-home/probe-state'
expect_fail "write into /etc/hermes policy"     sh -c 'echo x >> /etc/hermes/config.yaml'
expect_fail "write into /opt runtime"            sh -c 'echo x > /opt/a-nas/assistant-runtimes/spike/x'
expect_fail "outbound TCP to 1.1.1.1:443"        python3 -c 'import socket; socket.create_connection(("1.1.1.1", 443), timeout=3)'
expect_fail "outbound TCP to docker host"        python3 -c 'import socket; socket.create_connection(("172.17.0.1", 22), timeout=3)'
echo "INFO links: $(ip -brief link 2>/dev/null | awk '{print $1}' | tr '\n' ' ')"
pids=$(find /proc -maxdepth 1 -name '[0-9]*' -printf '%f ')
echo "INFO visible pids: $pids"
expect_fail "ptrace attach"                      python3 -c '
import ctypes, os, sys
libc = ctypes.CDLL(None, use_errno=True)
pid = os.fork()
if pid == 0:
    import time; time.sleep(5); os._exit(0)
r = libc.ptrace(16, pid, None, None)
os.kill(pid, 9)
print(f"ptrace returned {r} errno {ctypes.get_errno()}")
sys.exit(0 if r == 0 else 1)'
expect_fail "setuid to root"                     python3 -c 'import os; os.setuid(0)'
expect_fail "unshare a user namespace"           unshare -U true
echo "INFO lsm: $(cat /sys/kernel/security/lsm 2>/dev/null || echo unreadable)"
# Policy, not sandbox: every toolset and tool Hermes knows is allowed or disabled, so a
# Hermes upgrade that adds one fails here (toolsets-check.py).
if [ ! -x /opt/a-nas/assistant-runtimes/spike/venv/bin/python ]; then
  echo "INFO toolsets check skipped: no Hermes runtime"
elif out=$(/opt/a-nas/assistant-runtimes/spike/venv/bin/python /spike/toolsets-check.py 2>&1); then
  ok "every Hermes toolset allowed or disabled ($(tail -n 1 <<<"$out"))"
else
  bad "every Hermes toolset allowed or disabled" "$(tr '\n' ';' <<<"$out")"
fi
