#!/usr/bin/env bash
# What the assistant learns and how a member sees and clears it (#114): where Hermes keeps
# memory and the skills the agent creates, what the official learning interface
# (/api/learning/*, /api/memory*) shows and deletes, and what a new conversation still gets
# afterwards (system prompt, session_search, archived skills). Fake model, `hermes serve`,
# called as root with the instance's token, the position of the product service.
set -uo pipefail
user=${1:-alice}
home=/srv/a-nas/data/assistant/$user/hermes
sock=/run/a-nas-assistant/$user.sock
py=/opt/a-nas/assistant-runtimes/spike/venv/bin/python
token=$(sed -n 's/^HERMES_DASHBOARD_SESSION_TOKEN=//p' "/etc/a-nas-assistant/tokens/$user.env")
system=/var/log/fake-model.jsonl.system.txt
rest() {  # rest METHOD PATH [JSON]: the body, then the status on its own line
  curl -sS --max-time 150 --unix-socket "$sock" -X "$1" -H "X-Hermes-Session-Token: $token" \
    -H 'Content-Type: application/json' ${3:+-d "$3"} -w '\n%{http_code}\n' "http://127.0.0.1:9119$2"
}
body() { rest "$@" | head -n -1; }
show() { printf '%-52s ' "$1 $2"; rest "$@" | tr '\n' ' ' | cut -c1-230; echo; }
chat() { "$py" /spike/serve_client.py "$user" chat "$@" | grep -E '^(session|<)' | cut -c1-230; }
q() { python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$1"; }
memories() { for f in "$home"/memories/*.md; do [ -e "$f" ] && echo "  [$(basename "$f")] $(tr '\n' ' ' < "$f")"; done; }
# Stored id of the newest conversation whose list entry (title, preview of the first message) contains $1.
stored() { body GET '/api/sessions?limit=50' | python3 -c '
import json, sys
d = json.load(sys.stdin)
print(next((s["id"] for s in d.get("sessions", d.get("data", [])) if sys.argv[1] in json.dumps(s, ensure_ascii=False)), ""))' "$1"; }
# Which conversations a session_search call in conversation $1 returned (from its stored transcript).
found() { body GET "/api/sessions/$1/messages?limit=50" | python3 -c '
import json, sys
rows = json.load(sys.stdin)
rows = rows.get("messages", rows.get("data", [])) if isinstance(rows, dict) else rows
for r in rows:
    if r.get("role") == "tool" and r.get("tool_name") == "session_search":
        text = r.get("content") or ""
        text = text[text.find("{"):text.rfind("}") + 1]
        try:
            print(" ".join(sorted({x.get("session_id", "?") for x in json.loads(text).get("results", [])})) or "nothing")
        except ValueError:
            print("unparsed:", text[:160])'; }

echo "=== the assistant learns (one conversation)"
chat 'CALL memory {"action": "add", "target": "memory", "content": "家里的猫叫团子，每天早上七点喂。"}' \
     'CALL memory {"action": "add", "target": "user", "content": "alice 喜欢简短的回答。"}' \
     'CALL skill_manage {"operations": [{"action": "create", "name": "family-trip", "content": "---\nname: family-trip\ndescription: 规划家庭出游：先问人数、地点与预算。\n---\n# 家庭出游\n1. 先问人数、地点与预算。\n"}]}' \
     'CALL skill_view {"name": "anas-files"}'
learned=$(stored 'CALL memory')

echo "=== on disk"
memories
echo "  skills the agent created: $(cd "$home/skills" && find . -name SKILL.md -newer "$home/config.yaml" | grep -v '^./.archive' | tr '\n' ' ')"
echo "  SKILL.md files in the Hermes home: $(find "$home/skills" -name SKILL.md | grep -vc '/.archive/') (bundled copies included)"
python3 -c 'import json, sys; u = json.load(open(sys.argv[1])); print("  usage record of family-trip:", {k: v for k, v in u.get("family-trip", {}).items() if v})' "$home/skills/.usage.json"

echo "=== official interfaces"
show GET /api/memory
graph=$(body GET /api/learning/graph)
python3 -c '
import json, sys
g = json.loads(sys.argv[1])
for n in g["nodes"]:
    print("  node %-6s %-46s createdBy=%s useCount=%s" % (n["kind"], n["id"][:44], n.get("createdBy"), n.get("useCount")))
print("  stats:", {k: g["stats"][k] for k in ("learned_skills", "memory_nodes", "agent_created")})' "$graph"
body GET /api/skills | python3 -c '
import json, sys
d = json.load(sys.stdin)
items = d if isinstance(d, list) else d.get("skills", [])
print("  GET /api/skills:", len(items), "skills; fields", sorted(items[0]) if items else "-")'
mem_id=$(python3 -c 'import json, sys; print(next(n["id"] for n in json.loads(sys.argv[1])["nodes"] if n["kind"] == "memory"))' "$graph")
show GET "/api/learning/node?id=$(q "$mem_id")"
show GET "/api/learning/node?id=family-trip"

echo "=== the same interface on skills that are not the assistant's own"
show GET "/api/learning/node?id=anas-files"
show DELETE /api/learning/node '{"id": "anas-member"}'
bundled=$(cd "$home/skills" && find . -mindepth 3 -maxdepth 3 -name SKILL.md -not -path './.archive/*' ! -newer "$home/config.yaml" | head -1 | cut -d/ -f3)
show DELETE /api/learning/node "{\"id\": \"$bundled\"}"

echo "=== a member deletes one memory entry and the skill, then resets all memory"
show DELETE /api/learning/node "{\"id\": \"$mem_id\"}"
memories
show DELETE /api/learning/node '{"id": "family-trip"}'
echo "  archived: $(cd "$home/skills/.archive" 2>/dev/null && find . -name SKILL.md | tr '\n' ' ')"
show POST /api/memory/reset '{"target": "all"}'
echo "  memories left: $(find "$home/memories" -mindepth 1 -printf '%f ')"

echo "=== a new conversation"
chat hello
for word in 团子 简短 family-trip; do echo "  system prompt mentions $word: $(grep -c -- "$word" "$system")"; done
chat 'CALL session_search {"query": "团子"}' > /dev/null
search1=$(stored session_search)
echo "  session_search for 团子 returned: $(found "$search1")   (the conversation that taught it: $learned)"
archived=$(cd "$home/skills/.archive" && find . -name SKILL.md | grep family-trip | head -1)
chat "CALL read_file {\"path\": \"/var/lib/hermes-home/skills/.archive/${archived#./}\"}" | grep '^<' | sed 's/^/  read_file of the archived skill: /'

echo "=== the member deletes the conversation that taught it"
show DELETE "/api/sessions/$learned"
chat 'CALL session_search {"query": "团子"}' > /dev/null
search2=$(body GET '/api/sessions?limit=50' | python3 -c '
import json, sys
d = json.load(sys.stdin)
print([s["id"] for s in d.get("sessions", d.get("data", [])) if "session_search" in json.dumps(s, ensure_ascii=False)][0])')
echo "  session_search for 团子 now returns: $(found "$search2")   (deleted: $learned; the first search: $search1)"
