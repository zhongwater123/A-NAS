#!/usr/bin/env bash
# Can the agent read the instance's session token? It is in the environment of `hermes serve`
# (and tini); the agent asks for /proc/<pid>/environ with its own file tools, through the
# scripted fake model. The token itself is never printed: matches show as <TOKEN>.
set -uo pipefail
user=${1:-alice}
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
token=$("$py" /spike/hermes_login.py "$user")  # an access token of Hermes's own login
"$py" /spike/serve_client.py "$user" ready >/dev/null
sleep 3  # MCP discovery starts with the first client
nspid() { awk '/^NSpid/ {print $NF}' "/proc/$1/status"; }
serve=$(nspid "$(pgrep -u "$user" -f serve_nondumpable | head -1)")
mcp=$(nspid "$(pgrep -u "$user" -f anas_mcp.py | head -1)")
echo "inside the instance: tini is pid 1, hermes serve pid $serve, the MCP server pid $mcp"
# The kernel's answer, from a shell in the same sandbox (shell.exec runs there as the member).
"$py" /spike/serve_client.py "$user" rpc shell.exec "{\"command\": \"for p in self 1 $serve $mcp; do printf 'pid %s: %s; ' \$p \$(grep -ac HERMES_DASHBOARD_SESSION_TOKEN= /proc/\$p/environ 2>&1); done\"}" |
  sed "s/$token/<TOKEN>/g" | sed 's/^/kernel (environ files holding the token, self = the shell): /'
cat > /var/tmp/calls-environ.txt <<EOF
hello
CALL read_file {"path": "/proc/self/environ"}
CALL read_file {"path": "/proc/1/environ"}
CALL read_file {"path": "/proc/$serve/environ"}
CALL read_file {"path": "/proc/$mcp/environ"}
CALL search_files {"pattern": "HERMES_DASHBOARD_SESSION_TOKEN", "path": "/proc/self/environ", "target": "content"}
CALL search_files {"pattern": "HERMES_DASHBOARD_SESSION_TOKEN", "path": "/proc/$serve", "target": "content"}
CALL search_files {"pattern": "HERMES_DASHBOARD_SESSION_TOKEN", "path": "/proc/$serve/environ", "target": "content"}
CALL search_files {"pattern": "HERMES_DASHBOARD_SESSION_TOKEN", "path": "/proc/1/environ", "target": "content"}
CALL search_files {"pattern": "environ", "path": "/proc/self", "target": "files"}
EOF
# What ripgrep (behind search_files) does with an environ file, in the same sandbox.
"$py" /spike/serve_client.py "$user" rpc shell.exec "{\"command\": \"rg -c HERMES_DASHBOARD /proc/$serve/environ; echo rg exit \$?; rg -a -c HERMES_DASHBOARD /proc/$serve/environ; echo rg -a exit \$?\"}" |
  sed "s/$token/<TOKEN>/g" | sed 's/^/ripgrep on the serve environ: /'
"$py" /spike/serve_client.py "$user" calls /var/tmp/calls-environ.txt > /var/tmp/environ-out.txt 2>&1
sed "s/$token/<TOKEN>/g" /var/tmp/environ-out.txt | grep -E '^(>|<)' | cut -c1-330
echo "token in what the model was sent: $(grep -cF "$token" /var/tmp/environ-out.txt) of $(grep -c '^<' /var/tmp/environ-out.txt) results"
home=/srv/a-nas/data/assistant/$user/hermes
echo "token in the Hermes home (sessions, state.db, logs): $(grep -rlF "$token" "$home" 2>/dev/null | sed "s#$home/##" | tr '\n' ' ' || true)"
# ripgrep does not print a matching line that contains NUL, but match counts still come back:
# can the agent read the token out piece by piece? Only counts are printed here.
right=${token:0:8}
wrong=${token:0:7}$([ "${token:7:1}" = a ] && echo b || echo a)
cat > /var/tmp/calls-oracle.txt <<EOF
hello
CALL search_files {"pattern": "HERMES_DASHBOARD_SESSION_TOKEN=$right", "path": "/proc/$serve/environ", "output_mode": "count"}
CALL search_files {"pattern": "HERMES_DASHBOARD_SESSION_TOKEN=$wrong", "path": "/proc/$serve/environ", "output_mode": "count"}
EOF
"$py" /spike/serve_client.py "$user" calls /var/tmp/calls-oracle.txt > /var/tmp/oracle-out.txt 2>&1
grep '^<' /var/tmp/oracle-out.txt | tail -n 2 | sed -e "s/$token/<TOKEN>/g" -e "s/$right/<PREFIX>/g" -e "s/$wrong/<WRONG>/g" |
  paste -d' ' <(printf 'count, first 8 characters right:\ncount, 8th character wrong:\n') - | cut -c1-260
