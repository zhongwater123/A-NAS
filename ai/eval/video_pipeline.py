"""The montage end to end, timed: a set of videos prepared once, then a member's instruction cut (#326).

Two stages, as a member meets them. `prepare` runs the steps of
eval.video_moments for a set in order (frames, embed, index, then describe and
measure side by side) and times each; `cut` takes an instruction in the
member's words, or a file of them one after another, has Claude Code plan it
with the assistant's tools (eval.video_planner) and times it from the
instruction to the cut copied out.
`run` is both, for a set the sandbox already serves. Each stage's times are
added to pipeline.json in the set's model directory, beside index.json:

    prepare: the seconds of video, the cards, the wall time and each step's
    cut:     the wall time, the planning model's share, the tools' share
             (frame check and render), the plans it took, the refusals, the cost

Steps resume from their cache, so a second prepare of the same set times only
what was left; a fresh measurement wants a set of its own (--set names the
cache). Stopping it leaves a step that runs in the VM running there. The cut
needs the prototype sandbox serving this set's index (docs/specs/
photo-to-video.md, "运行"); it checks that before it starts.

On the Mac the AI Worker's Python is in the anas VM, where this checkout is
mounted at the same path and the Mac's llama-server is 192.168.5.2:

    python3 -m eval.video_pipeline --set eval/video_moments_earth_meridian.json run \\
      --python "colima ssh -p anas -- env ANAS_DATASETS=$HOME/A-NAS-datasets /home/$USER.guest/anas-ai/bin/python" \\
      --model ~/A-NAS-models/embeddinggemma-2-740m/embeddinggemma-2-740m.litertlm \\
      --vision http://192.168.5.2:8090/v1/chat/completions --instruction 这部侦探片的预告片

Development only, like the steps it runs.
"""

import argparse
import json
import os
import shlex
import subprocess
import sys
import time

from eval import video_moments as vm, video_planner

HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TIMES = "pipeline.json"
# The sandbox's copy of the set's model directory (run.sh SPIKE_VIDEOS).
SERVED = "/spike-video-index"


def step(python, name, *args):
    return [*python, "-m", "eval.video_moments", "--set", vm.SET, name, *args]


def timed(command):
    began = time.monotonic()
    subprocess.run(command, check=True, cwd=HERE)
    return round(time.monotonic() - began, 1)


def model_directory():
    """The model directory the index step wrote last: its name comes from the embedding model."""
    found = [os.path.join(vm.CACHE, name) for name in os.listdir(vm.CACHE)
             if os.path.exists(os.path.join(vm.CACHE, name, "index.json"))]
    if not found:
        raise SystemExit(f"no index.json under {vm.CACHE}")
    return max(found, key=lambda d: os.path.getmtime(os.path.join(d, "index.json")))


def record(directory, stage, entry):
    path = os.path.join(directory, TIMES)
    times = {}
    if os.path.exists(path):
        with open(path, encoding="utf-8") as file:
            times = json.load(file)
    if stage == "cut":
        times.setdefault("cuts", []).append(entry)
    else:
        times[stage] = entry
    with open(path, "w", encoding="utf-8") as file:
        json.dump(times, file, ensure_ascii=False, indent=2)


def prepare(args):
    python = shlex.split(args.python)
    at = time.strftime("%Y-%m-%dT%H:%M:%S%z")
    began = time.monotonic()
    steps = {"frames": timed(step(python, "frames")),
             "embed": timed(step(python, "embed", "--model", args.model)),
             "index": timed(step(python, "index", "--model", args.model))}
    directory = model_directory()
    # The vision model and OpenCV share nothing, so they run at once.
    side = {"describe": step(python, "describe", "--cache", directory, "--vision", args.vision),
            "measure": step(python, "measure", "--cache", directory)}
    started = time.monotonic()
    running = {name: subprocess.Popen(command, cwd=HERE) for name, command in side.items()}
    for name, process in running.items():
        if process.wait():
            raise SystemExit(f"{name} failed with {process.returncode}")
        steps[name] = round(time.monotonic() - started, 1)
    with open(os.path.join(directory, "index.json"), encoding="utf-8") as file:
        videos = json.load(file)["videos"]
    with open(os.path.join(directory, "captions.json"), encoding="utf-8") as file:
        cards = len(json.load(file)["cards"])
    entry = {"at": at, "videos": len(videos), "videoSeconds": round(sum(v["duration"] for v in videos.values()), 1),
             "cards": cards, "seconds": round(time.monotonic() - began, 1), "steps": steps}
    record(directory, "prepare", entry)
    print("prepare", json.dumps(entry, ensure_ascii=False), flush=True)
    return directory


def served(container, directory):
    """Whether the sandbox serves this index, by its SHA-256."""
    out = subprocess.run(["docker", "exec", container, "sha256sum", f"{SERVED}/index.json"],  # noqa: S607
                         capture_output=True, text=True, check=False).stdout.split()
    return bool(out) and out[0] == vm.sha256(os.path.join(directory, "index.json"))


def instructions(args):
    """The member's instructions: the one given, or those of a file such as video_montage_library.json."""
    if args.instruction:
        return [{"text": args.instruction} | ({"style": args.style} if args.style else {})]
    with open(args.instructions, encoding="utf-8") as file:
        listed = json.load(file)["instructions"]
    return [i | ({"style": args.style} if args.style else {}) for i in listed if not args.only or i["id"] in args.only]


def cut(args, directory, instruction):
    if not served(args.container, directory):
        raise SystemExit(f"the sandbox {args.container} does not serve {directory}: start it with "
                         f"SPIKE_VIDEOS={directory} (docs/specs/photo-to-video.md, 运行)")
    at = time.strftime("%Y%m%d-%H%M%S")
    out = os.path.join(directory, "cuts")
    os.makedirs(out, exist_ok=True)
    target = os.path.join(out, "-".join(filter(None, (at, instruction.get("id")))))
    instruction = {"id": at} | instruction
    began = time.monotonic()
    done = video_planner.turn(instruction, args.container, args.planner, target + ".jsonl")
    length = None
    if done["cut"]:
        video_planner.copy_out(args.container, done["cut"], target)
        with open(target + ".json", encoding="utf-8") as file:
            length = round(sum(s["end"] - s["start"] for s in json.load(file)["segments"]), 1)
    entry = {"at": at, "instruction": instruction["text"], "style": instruction.get("style"),
             "seconds": round(time.monotonic() - began, 1), "model": done["modelSeconds"],
             "tools": round(done["seconds"] - done["modelSeconds"], 1),
             "plans": done["calls"].count("make_montage"), "refused": done["refused"], "cost": done["cost"],
             "cut": target + ".mp4" if done["cut"] else None, "cutSeconds": length}
    record(directory, "cut", entry)
    print("cut", json.dumps(entry, ensure_ascii=False), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--set", default=vm.SET, help="the videos, as video_moments --set; its cache is the pipeline's")
    stages = parser.add_subparsers(dest="stage", required=True)
    making = argparse.ArgumentParser(add_help=False)
    making.add_argument("--python", default=sys.executable,
                        help="the AI Worker's Python, as a command line; it must see this checkout at the same path")
    making.add_argument("--model", required=True, help="the embedding model, as the Python sees it")
    making.add_argument("--vision", default="http://127.0.0.1:8090/v1/chat/completions",
                        help="the local vision model's chat completions URL, as the Python sees it")
    cutting = argparse.ArgumentParser(add_help=False)
    asking = cutting.add_mutually_exclusive_group(required=True)
    asking.add_argument("--instruction", help="what the member asks for, in their words")
    asking.add_argument("--instructions", help="a file of instructions to cut one after another, as video_montage.json")
    cutting.add_argument("--only", nargs="*", help="with --instructions: the ids to cut, all by default")
    cutting.add_argument("--style", help="the style the member picked (anas_mcp.STYLES), the planner's guess otherwise")
    cutting.add_argument("--planner", help="the Claude model that plans, Claude Code's default otherwise")
    cutting.add_argument("--container", default=os.environ.get("HERMES_SPIKE_NAME", "video-montage-sbx"))
    stages.add_parser("prepare", parents=[making], help="frames, vectors, cards, captions and measurements, timed")
    step = stages.add_parser("cut", parents=[cutting], help="one instruction to a cut, timed")
    step.add_argument("--cache", help="the set's model directory, the one the index step wrote last by default")
    stages.add_parser("run", parents=[making, cutting], help="prepare, then cut")
    args = parser.parse_args()
    vm.use_set(args.set)
    if args.stage == "prepare":
        prepare(args)
    elif args.stage == "cut":
        for instruction in instructions(args):
            cut(args, args.cache or model_directory(), instruction)
    else:
        directory = prepare(args)
        # The captions and measurements are read when the video service starts, and the
        # member's assistant still holds the old service's socket (the spec's "运行").
        subprocess.run(["docker", "exec", args.container, "sh", "-c",  # noqa: S607
                        "systemctl restart videos-spike; systemctl stop anas-assistant@alice"], check=True)
        for instruction in instructions(args):
            cut(args, directory, instruction)


if __name__ == "__main__":
    main()
