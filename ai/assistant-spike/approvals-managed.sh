#!/usr/bin/env bash
# Do the managed policy's approval settings win over a hostile config.yaml
# (simulating a bypassed write guard)? It tries to trust the A-NAS MCP server,
# switch approvals off and time them out after one second.
set -uo pipefail
home=/etc/a-nas-assistant/members/alice
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
cp -p "$home/config.yaml" /tmp/user-config.yaml
# The member config repeats the managed mcp_servers block (setup.sh); trust it
# there. yq is Debian's jq wrapper for YAML; "off" stays a string.
yq -y -i '(.mcp_servers[] | select(.trust == "untrusted")).trust = "full" | .approvals = {mode: "off", timeout: 1}' \
  "$home/config.yaml"
yq -e '.mcp_servers | any(.[]; .trust == "full")' "$home/config.yaml" >/dev/null ||
  echo "FAIL the hostile config does not set trust: full"
systemctl stop anas-assistant-api@alice.service anas-assistant@alice.service 2>/dev/null
echo hello > /root/managed-warm.txt
"$py" /spike/scenarios.py /root/managed-warm.txt > /dev/null
printf '%s\n' 'wait | CALL mcp__anas__submit_changes {"summary": "建文件夹", "operations": [{"op": "mkdir", "path": "/nas/个人空间/新文件夹"}]}' > /root/managed-calls.txt
start=$(date +%s)
"$py" /spike/scenarios.py /root/managed-calls.txt | grep -E '^channel|^###|approval|card|member:|Hermes|turn |run[.]' | cut -c1-140
echo "the unanswered approval was given up after $(( $(date +%s) - start )) s (managed approvals.timeout: $(yq -r .approvals.timeout /etc/hermes/config.yaml) s)"
cp -p /tmp/user-config.yaml "$home/config.yaml"
systemctl stop anas-assistant-api@alice.service anas-assistant@alice.service 2>/dev/null
