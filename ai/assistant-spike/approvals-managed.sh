#!/usr/bin/env bash
# Do the managed policy's approval settings win over a hostile config.yaml
# (simulating a bypassed write guard)? It tries to trust the A-NAS MCP server,
# switch approvals off and time them out after one second. The companion's
# server (#412) is trusted by the policy itself: its calls must not ask.
set -uo pipefail
home=/etc/a-nas-assistant/members/alice
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
check() {
  local name=$1
  shift
  if "$@" >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; fi
}
check "the policy has anas ask and companion not" \
  yq -e '.mcp_servers.anas.trust == "untrusted" and .mcp_servers.companion.trust == "full" and .mcp_servers.desktop.trust == "full"' /etc/hermes/config.yaml
cp -p "$home/config.yaml" /tmp/user-config.yaml
# The member config repeats the managed mcp_servers block (setup.sh); trust it
# there. yq is Debian's jq wrapper for YAML; "off" stays a string.
yq -y -i '(.mcp_servers[] | select(.trust == "untrusted")).trust = "full" | .approvals = {mode: "off", timeout: 1}' \
  "$home/config.yaml"
yq -e '.mcp_servers.anas.trust == "full"' "$home/config.yaml" >/dev/null ||
  echo "FAIL the hostile config does not set trust: full"
systemctl stop anas-assistant-api@alice.service anas-assistant@alice.service 2>/dev/null
echo hello > /root/managed-warm.txt
"$py" /spike/scenarios.py /root/managed-warm.txt > /dev/null
printf '%s\n' 'wait | CALL mcp__anas__submit_changes {"summary": "建文件夹", "operations": [{"op": "mkdir", "path": "/nas/个人空间/新文件夹"}]}' > /root/managed-calls.txt
start=$(date +%s)
"$py" /spike/scenarios.py /root/managed-calls.txt | grep -E '^channel|^###|approval|card|member:|Hermes|turn |run[.]' | cut -c1-140
echo "the unanswered approval was given up after $(( $(date +%s) - start )) s (managed approvals.timeout: $(yq -r .approvals.timeout /etc/hermes/config.yaml) s)"
# A companion tool runs at once, without a card. With no VNAS window here it answers
# that no desktop is in use.
printf '%s\n' 'CALL mcp__companion__wake {}' > /root/managed-companion.txt
companion=$("$py" /spike/scenarios.py --answer approve /root/managed-companion.txt)
check "a companion tool asks for no approval" test "$(grep -c '⏸ approval' <<<"$companion")" = 0
check "a companion tool answers without a desktop" grep -qE 'mcp__companion__wake: .*(no_desktop|没有在用的桌面)' <<<"$companion"
cp -p /tmp/user-config.yaml "$home/config.yaml"
systemctl stop anas-assistant-api@alice.service anas-assistant@alice.service 2>/dev/null
