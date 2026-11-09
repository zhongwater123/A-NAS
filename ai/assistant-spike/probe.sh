#!/bin/bash
# Runs inside anas-assistant-probe@alice.service: the same sandbox as the
# assistant, with this script in place of Hermes.
ok()  { echo "PASS $1"; }
bad() { echo "FAIL $1${2:+: $2}"; }
expect_ok()   { local n=$1; shift; if out=$("$@" 2>&1); then ok "$n"; else bad "$n" "$out"; fi; }
# expect_refused NAME ERROR COMMAND...: the command fails with ERROR, such as "Permission
# denied", in its output. Any other failure, such as a crash or a missing interpreter, is not
# a refusal and fails the check (#423).
expect_refused() {
  local n=$1 e=$2
  shift 2
  if out=$("$@" 2>&1); then bad "$n" "succeeded: $out"
  elif [[ $out == *"$e"* ]]; then ok "$n ($e)"
  else bad "$n" "not refused with $e: $out"; fi
}
# hidden PATH...: what the probe is refused without seeing it, so that the refusal alone does
# not show it exists. ai/assistant-spike/system-test.sh checks, as root, that each is there
# outside the sandbox.
hidden() { local path; for path in "$@"; do echo "HIDDEN $path"; done; }

echo "INFO id: $(id)"
echo "INFO caps: $(grep CapEff /proc/self/status)"
# ADR 0024 decision 2: the member's own group and a-nas-users. For an administrator systemd
# also adds a-nas-admins (User= applies the account's groups from /etc/group, and
# SupplementaryGroups= can only add), so what holds is that nothing the sandbox reaches grants
# that group (PROBE_IDENTITY_ONLY, run by system-test.sh for its administrator). Checked: the
# spaces, Hermes's home, /run, the assistant's policy and config, and /opt/a-nas, as owning
# group, ACL entry or default ACL entry.
if [ -n "${PROBE_IDENTITY_ONLY:-}" ]; then
  # An empty listing proves nothing, so getfacl must have printed something.
  # shellcheck disable=SC2016 # $acls expands in the inner sh
  expect_ok "nothing in the sandbox grants a-nas-admins" sh -c \
    'acls=$(getfacl -R -p /nas /var/lib/hermes-home /run /etc/hermes /etc/a-nas-assistant /opt/a-nas 2>/dev/null)
      [ -n "$acls" ] && ! printf "%s\n" "$acls" | grep -Eq "^(# group: |(default:)?group:)a-nas-admins(:|\$)"'
  exit 0
fi
expect_ok   "no a-nas-admins in the sandbox"     sh -c '! id -G | tr " " "\n" | grep -qx 20001'
expect_ok   "read own personal file"            cat /nas/个人空间/计划.md
expect_ok   "read own trash entry"               cat /nas/个人空间/.a-nas-trash/alice/old.txt
expect_ok   "read shared file"                   cat /nas/共享空间/family.md
expect_ok   "read another member's Agent space"  cat /nas/共享空间/Agent空间/bob/report.md
# Each recycle bin in the sandbox holds only the member's own folder.
hidden /nas/共享空间/.a-nas-trash/bob/gone.txt
expect_refused "read another member's shared trash" 'No such file or directory' cat /nas/共享空间/.a-nas-trash/bob/gone.txt
expect_ok   "recursive file search over Shared"  sh -c 'rg --files -g "*" -- /nas/共享空间 >/dev/null; [ $? -le 1 ]'
expect_ok   "recursive file search over personal" sh -c 'rg --files -g "*" -- /nas/个人空间 >/dev/null; [ $? -le 1 ]'
# shellcheck disable=SC2012 # names listed for the log
echo "INFO shared bin as seen: $(ls -A /nas/共享空间/.a-nas-trash | tr '
' ' ')"
if [ -z "$(ls -A /srv 2>/dev/null)" ]; then ok "data volume hidden (/srv empty)"; else bad "data volume hidden" "$(ls -A /srv)"; fi
found=$(find / -xdev \( -name bob-secret.txt -o -name 'snap-1' -o -name '.a-nas-snapshots' \) 2>/dev/null | head -3)
if [ -z "$found" ]; then ok "no other member's space or snapshot anywhere"; else bad "other space visible" "$found"; fi
expect_refused "write into personal space"          'Read-only file system' sh -c 'echo x > /nas/个人空间/new.txt'
expect_refused "delete in personal space"           'Read-only file system' rm /nas/个人空间/secret.txt
expect_refused "rename in personal space"           'Read-only file system' mv /nas/个人空间/secret.txt /nas/个人空间/s2.txt
expect_refused "chmod in personal space"            'Read-only file system' chmod 0644 /nas/个人空间/secret.txt
expect_refused "setfacl in personal space"          'Read-only file system' setfacl -m o::r /nas/个人空间/secret.txt
expect_refused "write into shared space"            'Read-only file system' sh -c 'echo x > /nas/共享空间/x.txt'
expect_refused "write into own Agent space directly" 'Read-only file system' sh -c 'echo x > /nas/共享空间/Agent空间/alice/x.txt'
expect_ok   "write into assistant directory"     sh -c 'mkdir -p /nas/助手/待确认 && echo x > /nas/助手/待确认/x.txt'
# The directories below are made inaccessible when they exist outside the sandbox
# (InaccessiblePaths=-), so inside it their contents cannot be told apart from nothing.
connect='import socket, sys; socket.socket(socket.AF_UNIX).connect(sys.argv[1])'
if [ -e /etc/anas-spike ]; then
  hidden /etc/anas-spike/model.key
  expect_refused "read the gateway's model key" 'Permission denied' cat /etc/anas-spike/model.key
else
  echo "INFO model key: none here (the scripted model)"
fi
hidden /etc/a-nas-assistant/tokens/alice.env /etc/a-nas-assistant/tokens/bob.env
expect_refused "read its own serve session token" 'Permission denied' cat /etc/a-nas-assistant/tokens/alice.env
expect_refused "read another member's session token" 'Permission denied' cat /etc/a-nas-assistant/tokens/bob.env
if [ -e /run/anas-spike-broker ]; then
  hidden /run/anas-spike-broker/broker.sock
  expect_refused "reach the change broker's product socket" 'Permission denied' python3 -c "$connect" /run/anas-spike-broker/broker.sock
else
  echo "INFO change broker's product socket: none here (the Host Agent executes)"
fi
expect_ok   "reach the change broker's agent socket"   python3 -c 'import socket; socket.socket(socket.AF_UNIX).connect("/run/anas-change-agent/agent.sock")'
expect_refused "change the Hermes user config"      'Read-only file system' sh -c 'echo x >> /var/lib/hermes-home/config.yaml'
expect_refused "write the Hermes .env"              'Read-only file system' sh -c 'echo x >> /var/lib/hermes-home/.env'
expect_refused "rewrite SOUL.md"                    'Read-only file system' sh -c 'echo x >> /var/lib/hermes-home/SOUL.md'
expect_refused "add a Hermes hook"                  'Read-only file system' mkdir /var/lib/hermes-home/hooks/x
expect_refused "add a Hermes plugin"                'Read-only file system' mkdir /var/lib/hermes-home/plugins/x
expect_refused "add a Hermes profile"               'Read-only file system' mkdir /var/lib/hermes-home/profiles/x
expect_refused "set a sticky Hermes profile"        'Read-only file system' sh -c 'echo x > /var/lib/hermes-home/active_profile'
expect_ok   "write Hermes state"                 sh -c 'echo x > /var/lib/hermes-home/probe-state'
expect_refused "write into /etc/hermes policy"     'Read-only file system' sh -c 'echo x >> /etc/hermes/config.yaml'
expect_refused "write into the system skills"   'Read-only file system' sh -c 'echo x > /opt/a-nas/assistant-skills/x'
if [ -d /opt/a-nas/assistant-runtimes/spike ]; then
  expect_refused "write into /opt runtime"       'Read-only file system' sh -c 'echo x > /opt/a-nas/assistant-runtimes/spike/x'
else
  echo "INFO /opt runtime: no Hermes runtime"
fi
expect_refused "outbound TCP to 1.1.1.1:443"     'Network is unreachable' python3 -c 'import socket; socket.create_connection(("1.1.1.1", 443), timeout=3)'
expect_refused "outbound TCP to docker host"     'Network is unreachable' python3 -c 'import socket; socket.create_connection(("172.17.0.1", 22), timeout=3)'
echo "INFO links: $(ip -brief link 2>/dev/null | awk '{print $1}' | tr '\n' ' ')"
pids=$(find /proc -maxdepth 1 -name '[0-9]*' -printf '%f ')
echo "INFO visible pids: $pids"
expect_refused "ptrace attach"                   'errno EPERM' python3 -c '
import ctypes, errno, os, sys
libc = ctypes.CDLL(None, use_errno=True)
pid = os.fork()
if pid == 0:
    import time; time.sleep(5); os._exit(0)
r = libc.ptrace(16, pid, None, None)
os.kill(pid, 9)
print(f"ptrace returned {r} errno {errno.errorcode.get(ctypes.get_errno(), ctypes.get_errno())}")
sys.exit(0 if r == 0 else 1)'
expect_refused "setuid to root"                  'Operation not permitted' python3 -c 'import os; os.setuid(0)'
expect_refused "unshare a user namespace"        'Operation not permitted' unshare -U true
echo "INFO lsm: $(cat /sys/kernel/security/lsm 2>/dev/null || echo unreadable)"
# The photo service's assistant socket (#113): the spike's photo service (SPIKE_PHOTOS) or,
# on the real installer's layout, A-NAS's own with product-settings.sh.
photos=/run/anas-assistant-photos/photos.sock
if [ -S "$photos" ]; then
  expect_ok   "read own and shared photo libraries" sh -c "python3 /spike/photos_peer.py $photos libraries | grep -q 'private:.*shared:'"
  # The spike seeds alice's, bob's and the shared library with beach photos.
  if python3 /spike/photos_peer.py "$photos" search 海边 2>/dev/null | grep -q IMG_3101; then
    expect_ok "search photos without another member's" sh -c "out=\$(python3 /spike/photos_peer.py $photos search 海边) && echo \"\$out\" | grep -q IMG_3101 && ! echo \"\$out\" | grep -q IMG_900"
  else
    echo "INFO photo search: no seeded photos here (the system test checks its own)"
  fi
  hidden /run/a-nas-photos/photos.sock
  expect_refused "reach the photo service's session socket" 'Permission denied' python3 /spike/photos_peer.py /run/a-nas-photos/photos.sock --session seed-alice libraries
else
  echo "INFO photo socket not bound (no photo service for assistants)"
fi
# Policy, not sandbox: every toolset and tool Hermes knows is allowed or disabled, so a
# Hermes upgrade that adds one fails here (toolsets-check.py).
if [ ! -x /opt/a-nas/assistant-runtimes/spike/venv/bin/python ]; then
  echo "INFO toolsets check skipped: no Hermes runtime"
elif out=$(/opt/a-nas/assistant-runtimes/spike/venv/bin/python /spike/toolsets-check.py 2>&1); then
  ok "every Hermes toolset allowed or disabled ($(tail -n 1 <<<"$out"))"
else
  bad "every Hermes toolset allowed or disabled" "$(tr '\n' ';' <<<"$out")"
fi
