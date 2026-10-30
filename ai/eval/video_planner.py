"""The model's montage for each instruction of #322: Claude Code plans with the assistant's tools.

Each instruction is one `claude -p` turn, as a member would ask, with only the two
video tools of anas_mcp.py (search_video_moments, make_montage), which
mcp-as-member.sh runs as alice inside the prototype sandbox (docs/specs/
photo-to-video.md, "运行"). The turn's events are kept as <id>.jsonl, and the cut
with its plan (anas_mcp saves the plan beside the cut) is copied out of the
sandbox to <cache>/<out>/<id>.mp4 and .json, where eval.video_montage score and
eval.video_judge read it. An instruction the model does not cut gets a plan with
no segments.

  python -m eval.video_planner --set eval/video_moments.json --cache <the set's model directory>

Development only: the sandbox's container is HERMES_SPIKE_NAME on the Docker
context DOCKER_CONTEXT (colima-anas on the Mac).
"""

import argparse
import json
import os
import re
import subprocess
import tempfile
import time

from eval import video_moments as vm

# No length is asked for: the cut is as long as the material that fits (#326).
ASK = "用测试视频剪一段视频：{text}。"
TOOLS = ("mcp__anas__search_video_moments", "mcp__anas__make_montage")
OTHER_TOOLS = ("browse_photos", "look_at_photo", "make_slideshow", "photo_details", "recent_changes", "search_photos",
               "similar_photos", "submit_changes")
CUT = re.compile(r"/nas/助手/视频/(montage-[0-9]{8}-[0-9]{6}-[0-9a-f]{6})\.mp4")
# The member's /nas/助手/视频 as the sandbox's container holds it (anas-assistant@.service).
MEMBER_VIDEOS = "/srv/a-nas/data/assistant/alice/files/视频"


def turn(instruction, container, model, log):
    here = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    config = {"mcpServers": {"anas": {
        "command": os.path.join(here, "assistant-spike", "mcp-as-member.sh"), "args": ["alice"],
        "env": {"HERMES_SPIKE_NAME": container, "DOCKER_CONTEXT": os.environ.get("DOCKER_CONTEXT", "colima-anas"),
                "PATH": os.environ["PATH"]}}}}
    command = ["claude", "-p", ASK.format(**instruction), "--mcp-config", json.dumps(config), "--strict-mcp-config",
               "--tools", "", "--allowedTools", *TOOLS, "--disallowedTools", *(f"mcp__anas__{t}" for t in OTHER_TOOLS),
               "--output-format", "stream-json", "--verbose",
               "--no-session-persistence", "--setting-sources", ""]
    if model:
        command += ["--model", model]
    # The member's assistant is woken first, so that the tools are there when the turn
    # starts; the turn runs in an empty folder, so that only the tools are at hand, and
    # loads them up front (ENABLE_TOOL_SEARCH): with no built-in tools it could not search for them.
    subprocess.run(["docker", "exec", container, "/opt/a-nas/assistant-runtimes/spike/venv/bin/python",  # noqa: S607
                    "/spike/serve_client.py", "alice", "ready"], check=True, capture_output=True, timeout=300)
    environment = os.environ | {"MCP_TIMEOUT": "120000", "ENABLE_TOOL_SEARCH": "false"}
    began = time.monotonic()
    with open(log, "w", encoding="utf-8") as out, tempfile.TemporaryDirectory() as empty:
        subprocess.run(command, stdout=out, check=True, timeout=1800, stdin=subprocess.DEVNULL, cwd=empty,
                       env=environment)
    with open(log, encoding="utf-8") as file:
        events = [json.loads(line) for line in file if line.strip()]
    calls, errors, cut = [], 0, None
    for event in events:
        for part in (event.get("message") or {}).get("content") or []:
            if not isinstance(part, dict):
                continue
            if part.get("type") == "tool_use":
                calls.append(part["name"].rsplit("__", 1)[-1])
            if part.get("type") == "tool_result":
                errors += bool(part.get("is_error"))
                made = CUT.search(json.dumps(part.get("content"), ensure_ascii=False))
                if made and not part.get("is_error"):
                    cut = made.group(1)
    final = next((e for e in reversed(events) if e.get("type") == "result"), {})
    return {"seconds": round(time.monotonic() - began), "cost": final.get("total_cost_usd"), "calls": calls,
            "refused": errors, "cut": cut, "answer": final.get("result", "")}


def copy_out(container, name, target):
    for suffix in (".mp4", ".json"):
        with open(target + suffix, "wb") as out:
            subprocess.run(["docker", "exec", container, "cat", f"{MEMBER_VIDEOS}/{name}{suffix}"],  # noqa: S607
                           stdout=out, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--set", default=vm.SET, help="the videos, as video_moments --set")
    parser.add_argument("--instructions", default=os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                                                "video_montage.json"))
    parser.add_argument("--cache", required=True, help="the embedding model's directory under the set's cache")
    parser.add_argument("--out", default="montage-model3", help="the folder under the cache for the cuts")
    parser.add_argument("--container", default=os.environ.get("HERMES_SPIKE_NAME", "video-montage-sbx"))
    parser.add_argument("--model", help="the Claude model that plans, Claude Code's default otherwise")
    parser.add_argument("--only", nargs="*", help="instruction ids, all by default")
    args = parser.parse_args()
    vm.use_set(args.set)
    with open(args.instructions, encoding="utf-8") as file:
        instructions = json.load(file)["instructions"]
    out = os.path.join(args.cache, args.out)
    os.makedirs(out, exist_ok=True)
    runs = {}
    path = os.path.join(out, "runs.json")
    if os.path.exists(path):
        with open(path, encoding="utf-8") as file:
            runs = json.load(file)
    for instruction in instructions:
        if args.only and instruction["id"] not in args.only:
            continue
        target = os.path.join(out, instruction["id"])
        done = turn(instruction, args.container, args.model, target + ".jsonl")
        if done["cut"]:
            copy_out(args.container, done["cut"], target)
        else:
            with open(target + ".json", "w", encoding="utf-8") as file:
                json.dump({"segments": []}, file)
        runs[instruction["id"]] = done
        with open(path, "w", encoding="utf-8") as file:
            json.dump(runs, file, ensure_ascii=False, indent=2)
        print(instruction["id"], {k: v for k, v in done.items() if k != "answer"}, flush=True)


if __name__ == "__main__":
    main()
