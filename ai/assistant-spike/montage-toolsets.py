"""A planning round of the montage center gets the montage tools and nothing else from the pinned Hermes.

The round's job is created with enabled_toolsets ["montage", "no_mcp"] (ai/assistant/montage_round.py). This asks
Hermes's own resolver for a scheduled job what that list becomes:
- with the policy of a member's scheduled tasks, which has the montage server: the montage toolset alone;
- with a configuration that has no server named montage (the policy of every other unit, or a member's settings
  written before the server existed): still the montage toolset alone, which then holds no tool. Without no_mcp
  Hermes adds every enabled MCP server to a job's list that names none it knows, and the round would run with the
  A-NAS and companion tools; the INFO line shows what the list would be.
And that a scheduled run still skips the background review of memory and skills: one line upstream, which
docs/architecture/montage-center.md relies on (an injected text in a brief must not reach memory or skills).

Runs with the runtime's Python, from the system test (ai/assistant-spike/system-test.sh) when it has the real
runtime; exits 1 and names what is wrong. A Hermes upgrade that changes any of this must fail here.
"""
import inspect
import logging
import sys

sys.path.insert(0, "/opt/a-nas/assistant-runtimes/spike/src")
sys.path.append("/usr/lib/a-nas/assistant")  # after Hermes's own packages: gateway.py there is not Hermes's gateway
logging.disable(logging.WARNING)

from cron import scheduler
from utils import fast_safe_load  # Hermes's own YAML loader

import montage_round


def policy(path):
    with open(path, encoding="utf-8") as source:
        return fast_safe_load(source)


problems = []
job = {"enabled_toolsets": list(montage_round.TOOLSETS)}
if montage_round.TOOLSETS != ["montage", "no_mcp"]:
    problems.append(f"the planning job's toolsets are {montage_round.TOOLSETS}")
with_server = policy("/usr/lib/a-nas/assistant/managed-cron/config.yaml")
without_server = policy("/usr/lib/a-nas/assistant/managed/config.yaml")
if "montage" not in (with_server.get("mcp_servers") or {}) or "montage" in (without_server.get("mcp_servers") or {}):
    problems.append("the montage server is not in the cron policy alone")
for name, config in (("the cron policy", with_server), ("a configuration without the montage server", without_server)):
    resolved = scheduler._resolve_cron_enabled_toolsets(dict(job), config)
    if resolved != ["montage"]:
        problems.append(f"with {name} the planning job gets {resolved}")
widened = scheduler._resolve_cron_enabled_toolsets({"enabled_toolsets": ["montage"]}, without_server)
print(f"INFO without no_mcp and without the server, the job would get {widened}")
others = sorted(set(without_server.get("mcp_servers") or {}) & set(widened))
if not others:
    # Upstream no longer widens the list: no_mcp costs nothing, and this line says the reason is gone.
    print("INFO Hermes no longer adds the other MCP servers to such a job")
if "skip_background_review=True" not in inspect.getsource(scheduler):
    problems.append("cron/scheduler.py no longer passes skip_background_review=True to a scheduled run")

for problem in problems:
    print(problem)
if problems:
    sys.exit(1)
print("montage toolsets: a planning round gets the montage tools alone")
