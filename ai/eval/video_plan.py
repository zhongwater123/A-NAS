"""A montage planned in one request and cut by the video service (#350, development only).

The program reads the catalog itself and gives the planning model all it needs in one request
(montage.plan): its own system prompt, no tools, and one message of the instruction, the cut's
seconds, its style and the catalog. The model answers with a plan as JSON; the plan goes to the
prototype sandbox's video service (video-as-member.sh), which sets every segment's seconds and
cuts; a refused plan goes back with its problems in the same conversation, ROUNDS times at most.

Claude Code carries the request for now (claude -p with --system-prompt, no tools, no MCP, no
background traffic), so it still adds two short system blocks and the date; the eval's container
will send the same system prompt and message to the API itself.

  python -m eval.video_plan --set eval/video_moments_case01.json --instructions eval/video_montage_case01.json \\
    --cache <the set's model directory> --out plan-v13 --container video-montage-v4

Each cut, its plan (planned seconds, cut seconds) and the conversation go to <cache>/<out>/, and
runs.json there keeps each instruction's seconds, cost and rounds.
"""

import argparse
import base64
import json
import os
import subprocess
import tempfile
import time
import uuid

from eval import video_moments as vm
from montage import moments, plan

ROUNDS = 4
MEMBER = "alice"
HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def ask(text: str, session: str, work: str, model: str | None, first: bool) -> dict:
    """One request to the planning model: the claude -p result (result, cost, durations)."""
    command = ["claude", "-p", text, "--system-prompt", plan.SYSTEM, "--tools", "", "--strict-mcp-config",
               "--mcp-config", '{"mcpServers":{}}', "--setting-sources", "", "--output-format", "json"]
    command += ["--session-id", session] if first else ["--resume", session]
    if model:
        command += ["--model", model]
    environment = os.environ | {"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}
    done = subprocess.run(command, capture_output=True, text=True, check=True, timeout=1800, cwd=work,
                          stdin=subprocess.DEVNULL, env=environment)
    return json.loads(done.stdout)


def cut(body: dict, container: str, target: str) -> tuple[dict, bytes]:
    """The video service's answer to /montage: its head (status, X- headers) and its body."""
    environment = os.environ | {"HERMES_SPIKE_NAME": container}
    done = subprocess.run([os.path.join(HERE, "assistant-spike", "video-as-member.sh"), MEMBER, "/montage"],
                          input=json.dumps(body, ensure_ascii=False).encode(), capture_output=True, check=True,
                          timeout=3600, env=environment)
    head = json.loads(done.stderr.decode().strip().splitlines()[-1])
    if head["status"] == 200:
        with open(target + ".mp4", "wb") as file:
            file.write(done.stdout)
    return head, done.stdout


def run(instruction: dict, index: moments.Index, container: str, model: str | None, target: str) -> dict:
    seconds, style = instruction["seconds"], instruction.get("style")
    session, began = str(uuid.uuid4()), time.monotonic()
    text = plan.message(index, instruction["text"], seconds, style)
    talk = [{"role": "system", "text": plan.SYSTEM}]
    calls, cost, model_ms, refused, made = [], 0.0, 0, 0, None
    with tempfile.TemporaryDirectory() as work:
        for round_ in range(ROUNDS):
            talk.append({"role": "user", "text": text})
            answer = ask(text, session, work, model, round_ == 0)
            calls.append("plan")
            cost += answer.get("total_cost_usd") or 0.0
            model_ms += answer.get("duration_api_ms") or 0
            talk.append({"role": "assistant", "text": answer.get("result", ""), "usage": answer.get("usage")})
            try:
                planned = plan.parsed(answer.get("result", ""))
            except ValueError as error:
                refused += 1
                text = plan.refused([f"回答不是可读的计划：{error}"])
                continue
            body = plan.request(planned, seconds, style)
            head, data = cut(body, container, target)
            calls.append("make_montage")
            if head["status"] == 200:
                review = json.loads(base64.b64decode(head["X-Review"])) if head.get("X-Review") else []
                segments = [s | {"start": r["planned"][0], "end": r["planned"][1]}
                            for s, r in zip(body["segments"], review, strict=True)]
                done = [s | {"start": r["start"], "end": r["end"]} for s, r in zip(body["segments"], review, strict=True)]
                with open(target + ".json", "w", encoding="utf-8") as file:
                    json.dump({"style": style, "story": planned.get("story"), "planned": segments, "segments": done,
                               "review": review, "skipped": []},
                              file, ensure_ascii=False, indent=1)
                made = target + ".mp4"
                talk.append({"role": "service", "text": f"出片 {head.get('X-Seconds')} 秒"})
                break
            refused += 1
            try:
                problems = json.loads(data).get("problems") or [json.loads(data).get("error", "")]
            except ValueError:
                problems = [data.decode(errors="replace")[:500]]
            talk.append({"role": "service", "text": "\n".join(problems)})
            text = plan.refused(problems)
    with open(target + ".talk.json", "w", encoding="utf-8") as file:
        json.dump(talk, file, ensure_ascii=False, indent=1)
    return {"seconds": round(time.monotonic() - began), "modelSeconds": round(model_ms / 1000), "cost": cost,
            "calls": calls, "refused": refused, "cut": made}


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--set", default=vm.SET, help="the videos, as video_moments --set")
    parser.add_argument("--instructions", required=True, help="a file of instructions with their seconds and style")
    parser.add_argument("--cache", required=True, help="the set's model directory, with index.json and scenes.json")
    parser.add_argument("--out", required=True, help="the folder under the cache for the cuts")
    parser.add_argument("--container", default=os.environ.get("HERMES_SPIKE_NAME", "video-montage-sbx"))
    parser.add_argument("--model", help="the Claude model that plans, Claude Code's default otherwise")
    parser.add_argument("--only", nargs="*", help="instruction ids, all by default")
    args = parser.parse_args()
    vm.use_set(args.set)
    index = moments.Index.load(os.path.join(args.cache, "index.json"), os.path.join(args.cache, "frames.npz"))
    with open(args.instructions, encoding="utf-8") as file:
        instructions = json.load(file)["instructions"]
    out = os.path.join(args.cache, args.out)
    os.makedirs(out, exist_ok=True)
    path = os.path.join(out, "runs.json")
    runs = {}
    if os.path.exists(path):
        with open(path, encoding="utf-8") as file:
            runs = json.load(file)
    for instruction in instructions:
        if args.only and instruction["id"] not in args.only:
            continue
        runs[instruction["id"]] = run(instruction, index, args.container, args.model,
                                      os.path.join(out, instruction["id"]))
        with open(path, "w", encoding="utf-8") as file:
            json.dump(runs, file, ensure_ascii=False, indent=2)
        print(instruction["id"], runs[instruction["id"]], flush=True)


if __name__ == "__main__":
    main()
