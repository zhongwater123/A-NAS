#!/usr/bin/env bash
# The tools ADR 0024 decision 3 opens: the terminal and code run as the member inside the
# sandbox. What they reach, what they cannot, that the session token stays out of reach, and
# that a dangerous command or a script waits for an approval. Runs as root in a container
# started with
#   SPIKE_APPROVAL_TIMEOUT=20 ai/assistant-spike/run.sh sandbox
# through the scripted fake model; serve_client.py answers the approvals as told
# (SERVE_CLIENT_APPROVALS), as the product service does once the member decides. The token
# itself is never printed.
set -uo pipefail
user=${1:-alice}
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
data=/srv/a-nas/data
files=$data/assistant/$user/files
token=$(sed -n 's/^HERMES_DASHBOARD_SESSION_TOKEN=//p' "/etc/a-nas-assistant/tokens/$user.env")
pass=0 fail=0
check() {
  local name=$1 pattern=$2 out=$3
  if grep -qE -- "$pattern" <<<"$out"; then echo "PASS $name"; pass=$((pass + 1))
  else echo "FAIL $name: $(head -c 300 <<<"$out")"; fail=$((fail + 1)); fi
}
run() {  # run APPROVALS FILE: the calls in FILE, approvals answered APPROVALS; the token masked
  local out
  out=$(SERVE_CLIENT_APPROVALS=$1 "$py" /spike/serve_client.py "$user" calls "$2" 2>&1)
  printf '%s\n' "$(grep -cF "$token" <<<"$out")" > /var/tmp/terminal-token-count
  sed "s/$token/<TOKEN>/g" <<<"$out"
}

# A binary file in Shared, written by the member as a Web upload is.
setpriv --reuid "$user" --regid "$user" --groups 20000 -- \
  sh -c "printf '\211PNG\r\n\032\n\0\0\0binary' > $data/spaces/shared/avatar.png"
"$py" /spike/serve_client.py "$user" ready >/dev/null
cat > /var/tmp/calls-terminal.txt <<'EOF'
hello
CALL terminal {"command": "echo who=$(id -un) where=$(pwd)"}
CALL terminal {"command": "cp /nas/共享空间/avatar.png /nas/助手/avatar.png && cmp /nas/共享空间/avatar.png /nas/助手/avatar.png && echo copied-binary"}
CALL terminal {"command": "mkdir -p /nas/助手/待确认 && cp /nas/共享空间/avatar.png /nas/助手/待确认/avatar.png && echo staged-binary"}
CALL terminal {"command": "touch /nas/个人空间/x 2>&1 | grep -c Read-only; touch /nas/共享空间/x 2>&1 | grep -c Read-only"}
CALL terminal {"command": "(getent hosts deb.debian.org >/dev/null && echo dns) || echo no-dns; ((exec 3<>/dev/tcp/1.1.1.1/443) 2>/dev/null && echo tcp-open) || echo no-tcp"}
CALL terminal {"command": "echo environs-with-token=$(grep -l HERMES_DASHBOARD_SESSION_TOKEN /proc/[0-9]*/environ 2>/dev/null | wc -l) staged-token-bytes=$(wc -c < /run/anas-assistant-start/token) root-dir=$(ls /run/a-nas-assistant-start >/dev/null 2>&1 && echo open || echo closed) handoff-files=$(find ~/.hermes/desktop-ssh -type f | wc -l)"}
CALL execute_code {"code": "import os\nprint('session-token-in-env=' + str('HERMES_DASHBOARD_SESSION_TOKEN' in os.environ))"}
CALL terminal {"command": "rm -rf /nas/助手/avatar.png"}
EOF
cat > /var/tmp/calls-terminal-deny.txt <<'EOF'
CALL terminal {"command": "rm -rf /nas/助手/待确认/avatar.png"}
EOF
shown=$(run once /var/tmp/calls-terminal.txt)
leaked=$(cat /var/tmp/terminal-token-count)
denied=$(run deny /var/tmp/calls-terminal-deny.txt)
leaked=$((leaked + $(cat /var/tmp/terminal-token-count)))
printf '%s\n%s\n' "$shown" "$denied" > /var/tmp/terminal-out.txt
grep -E '^(>|<|  (un)?answered)' /var/tmp/terminal-out.txt | cut -c1-260

check "commands run as the member in the assistant's folder" "who=$user where=/nas/助手" "$shown"
check "a binary file copies from Shared into the assistant's folder" "copied-binary" "$shown"
check "and into the staging area 提交更改 takes it from" "staged-binary" "$shown"
check "the personal space and Shared are read-only to it" '"output": "1\\n1"' "$shown"
check "no name lookups and no connections out" 'no-dns\\nno-tcp' "$shown"
check "no process environment shows the session token" "environs-with-token=0" "$shown"
check "the staged token file is empty once serve listens" "staged-token-bytes=0" "$shown"
check "the directory root stages it in is closed" "root-dir=closed" "$shown"
check "serve deleted the token file it was handed" "handoff-files=0" "$shown"
check "a script waits for an approval of the whole script" "answered once server request:.*execute_code" "$shown"
# (Its own HERMES_RPC_TOKEN, which lets the script call tools under the same approvals, is meant.)
check "and runs approved, without the session token in its environment" "session-token-in-env=False" "$shown"
check "a dangerous command waits for an approval with the command" "answered once server request:.*rm -rf /nas/助手/avatar.png" "$shown"
check "and runs approved" "gone" "$([[ -e $files/avatar.png ]] || echo gone)"
check "refused, it does not run" "kept" "$([[ -e $files/待确认/avatar.png ]] && echo kept)"
check "the model was never sent the token" "^0$" "$leaked"
echo "serve-terminal: $pass passed, $fail failed"
((fail == 0))
