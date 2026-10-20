"""Every tool Hermes could enable is either one A-NAS enables or one the managed policy disables.

agent.disabled_toolsets lists the toolsets of the pinned commit. A toolset or tool added upstream
would be on by default for every platform the policy does not pin (serve also builds agents for
cron and others), so a Hermes upgrade must fail here instead. Uses Hermes's own selection
(model_tools._select_tool_names), which subtracts a disabled toolset's tools, or only the
non-core part for platform bundles and postures. Runs in the sandbox from probe.sh with the
runtime's Python; exits 1 and names what is wrong.
"""
import logging
import sys

sys.path.insert(0, "/opt/a-nas/assistant-runtimes/spike/src")
logging.disable(logging.WARNING)

from utils import fast_safe_load  # Hermes's own YAML loader

with open("/etc/hermes/config.yaml", encoding="utf-8") as f:
    policy = fast_safe_load(f)

import toolsets
from tools.registry import discover_builtin_tools

discover_builtin_tools()  # tools and toolsets that modules register, as at startup
try:
    from hermes_cli.plugins import discover_plugins

    discover_plugins()  # plugins may register toolsets of their own
except Exception as exc:
    print(f"plugin discovery failed: {exc}")
    sys.exit(1)
from model_tools import _select_tool_names

mcp = set(policy.get("mcp_servers") or {})
platforms = policy["platform_toolsets"]
allowed = sorted(set(platforms["cli"]) - mcp)
disabled = list(policy["agent"]["disabled_toolsets"])
problems = []
if set(platforms.get("api_server", platforms["cli"])) != set(platforms["cli"]):
    problems.append("platform_toolsets.cli and api_server differ")

known = sorted(toolsets.get_all_toolsets())
problems += [f"unknown toolset {name!r} in the policy" for name in sorted(set(allowed + disabled) - set(known))]

# Every toolset is named, except platform bundles and composites, which only regroup the
# tools of others (checked tool by tool below).
for name in known:
    if name in allowed or name in disabled or name.startswith("hermes-"):
        continue
    if not (toolsets.get_toolset(name) or {}).get("includes"):
        problems.append(f"toolset {name!r} is neither allowed nor disabled")

wanted = {tool for name in allowed for tool in toolsets.resolve_toolset(name)}
kept = _select_tool_names(allowed, disabled, True)
# Tools of an allowed toolset that a disabled one takes out on purpose: browser brings raw
# DevTools access (browser_cdp, browser_dialog) and the Browser Use CLI (browser_exec), which
# the disabled browser-cdp and browser-use remove; the VNAS window serves only the browser
# control's own capabilities.
removed_on_purpose = {"browser_cdp", "browser_dialog", "browser_exec"}
problems += [f"allowed tool {tool!r} is removed by a disabled toolset" for tool in sorted(wanted - kept - removed_on_purpose)]
problems += [f"tool {tool!r} should be removed but is kept" for tool in sorted(removed_on_purpose & kept)]
# The widest selection any platform could make: every toolset, then the disabled ones removed.
widest = _select_tool_names(known, disabled, True)
problems += [f"tool {tool!r} survives the disabled list" for tool in sorted(widest - wanted)]

for problem in problems:
    print(problem)
print(f"{len(known)} toolsets; allowed {allowed} = {len(wanted)} tools; {len(disabled)} disabled")
sys.exit(1 if problems else 0)
