"""The model's montage for each instruction of #322: Claude Code plans with the assistant's tools.

Each instruction is one `claude -p` turn, as a member would ask, with only the
video tools of anas_mcp.py (list_video_contents, search_video_moments, make_montage), which
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
# With --about: the instruction's seconds as a member would say them, not a hard target (#350).
ASK_ABOUT = "用测试视频剪一段约 {seconds:g} 秒的视频：{text}。"
# The style a member picks before asking (anas_mcp.STYLES), said the way the app would say it.
STYLE = "体裁：{}。"
TOOLS = ("mcp__anas__list_video_contents", "mcp__anas__search_video_moments", "mcp__anas__make_montage")
OTHER_TOOLS = ("browse_photos", "look_at_photo", "make_slideshow", "photo_details", "recent_changes", "search_photos",
               "similar_photos", "submit_changes")
CUT = re.compile(r"/nas/助手/视频/(montage-[0-9]{8}-[0-9]{6}-[0-9a-f]{6})\.mp4")
# The member's /nas/助手/视频 as the sandbox's container holds it (anas-assistant@.service).
MEMBER_VIDEOS = "/srv/a-nas/data/assistant/alice/files/视频"


def turn(instruction, container, model, log, pictures=False, screen=False, about=False, retrieved=False, span=None,
         locate=None, fill=False, edges=False, no_look=False, flow=False):
    here = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    config = {"mcpServers": {"anas": {
        "command": os.path.join(here, "assistant-spike", "mcp-as-member.sh"), "args": ["alice"],
        "env": {"HERMES_SPIKE_NAME": container, "DOCKER_CONTEXT": os.environ.get("DOCKER_CONTEXT", "colima-anas"),
                "PATH": os.environ["PATH"]} | ({"ANAS_SPIKE_CARD_PICTURES": "1"} if pictures else {})
                | ({"ANAS_SPIKE_CARD_SCREEN": "1"} if screen else {})
                | ({"ANAS_SPIKE_RETRIEVED_ONLY": "1"} if retrieved else {})
                | ({"ANAS_SPIKE_SPAN": span} if span else {})
                | ({"ANAS_SPIKE_LOCATE": locate} if locate else {})
                | ({"ANAS_SPIKE_FILL": "1"} if fill else {})
                | ({"ANAS_SPIKE_EDGES": "1"} if edges else {})
                | ({"ANAS_SPIKE_NO_LOOK": "1"} if no_look else {})
                | ({"ANAS_SPIKE_FLOW": "1"} if flow else {})}}}
    ask = ((ASK_ABOUT if about else ASK).format(**instruction)
           + (STYLE.format(instruction["style"]) if instruction.get("style") else ""))
    command = ["claude", "-p", ask, "--mcp-config", json.dumps(config), "--strict-mcp-config",
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
    return {"seconds": round(time.monotonic() - began), "modelSeconds": round(final.get("duration_api_ms", 0) / 1000),
            "cost": final.get("total_cost_usd"), "calls": calls,
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
    parser.add_argument("--pictures", action="store_true",
                        help="show the planner each card's key frames in place of its caption (an experiment)")
    parser.add_argument("--style", help="the style the member picked for every instruction, the instructions' own otherwise")
    parser.add_argument("--screen", action="store_true",
                        help="hand the planner only the recalled cards the vision model passes (an experiment)")
    parser.add_argument("--about", action="store_true",
                        help="ask for about the instruction's seconds, as a member would (#350)")
    parser.add_argument("--retrieved-only", action="store_true",
                        help="the catalog as before, but a plan of retrieved cards only (an experiment, #350)")
    parser.add_argument("--span", choices=("probe", "frame"),
                        help="with --retrieved-only implied: no seconds in the plan, each segment grown from the "
                             "window its probe matched, while the score holds or the picture does (an experiment, #350)")
    parser.add_argument("--locate", choices=("probe", "frame"),
                        help="the catalog as before and no retrieval needed, but no seconds in the plan: each segment "
                             "grown from the window of its card its shows matches (an experiment, #350)")
    parser.add_argument("--edges", action="store_true",
                        help="trim each segment to a cut found near either end at the source's frame rate (#350)")
    parser.add_argument("--flow", action="store_true",
                        help="with --locate: a catalog without seconds, and neighbouring segments kept to one scene or "
                             "person (an experiment, #350)")
    parser.add_argument("--no-look", action="store_true",
                        help="cut without the vision model reviewing the frames first (an experiment, #350)")
    parser.add_argument("--fill", action="store_true",
                        help="with --locate: the segments' seconds set to add up to the cut's, their ends from "
                             "frames every half second (an experiment, #350)")
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
        if args.style:
            instruction = instruction | {"style": args.style}
        target = os.path.join(out, instruction["id"])
        done = turn(instruction, args.container, args.model, target + ".jsonl", args.pictures, args.screen,
                    args.about, args.retrieved_only, args.span, args.locate, args.fill, args.edges,
                    args.no_look, args.flow)
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
