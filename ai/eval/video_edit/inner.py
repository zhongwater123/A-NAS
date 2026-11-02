"""The harness steps that run inside the evaluation image, with its PyYAML,
jsonschema and OpenTimelineIO (docs/specs/video-edit-evaluation.md):

    /opt/harness/bin/python -m eval.video_edit.inner describe --plan /plan
    /opt/harness/bin/python -m eval.video_edit.inner check-env --plan /plan --env /env --out /env/harness-env.json
    /opt/harness/bin/python -m eval.video_edit.inner prepare --plan /plan --case-dir /case --out /work/prepare.json case-01 …
    /opt/harness/bin/python -m eval.video_edit.inner report --a /a --b /b --out /report

The host side (__main__) mounts the paths and starts the container.
"""

import argparse
import hashlib
import importlib.metadata
import json
import os
import subprocess
import sys

import jsonschema
import opentimelineio as otio
import yaml

from eval.video_edit import BASELINE, EXTERNAL, METHOD_VERSION, checks, manifest, metrics, report, trace

SCHEMAS = os.path.join(os.path.dirname(os.path.abspath(__file__)), "schemas")
FFPROBE = "/usr/bin/ffprobe"
DPKG_QUERY = "/usr/bin/dpkg-query"


def validator(name):
    with open(os.path.join(SCHEMAS, name), encoding="utf-8") as file:
        return jsonschema.Draft202012Validator(json.load(file))


def schema_problems(name, document, source):
    found = []
    for error in sorted(validator(name).iter_errors(document), key=lambda error: list(error.absolute_path)):
        where = "/".join(str(part) for part in error.absolute_path) or "（顶层）"
        found.append(f"{source} {where}：{error.message}")
    return found


def load_pipeline(plan):
    """The plan's pipeline.yaml, its sha256, and what is wrong with it."""
    path = os.path.join(plan, "pipeline.yaml")
    try:
        with open(path, "rb") as file:
            data = file.read()
        pipeline = yaml.safe_load(data)
    except (OSError, yaml.YAMLError) as error:
        return None, None, [f"pipeline.yaml 无法读取：{error}"]
    found = schema_problems("pipeline.schema.json", pipeline, "pipeline.yaml")
    if not found:
        ids = [stage["id"] for stage in pipeline["stages"]]
        found += [f"pipeline.yaml 步骤 id 重复：{stage}" for stage in sorted({i for i in ids if ids.count(i) > 1})]
    return pipeline, manifest.sha256_bytes(data), found


def tree_size(path):
    total = 0
    for root, _dirs, files in os.walk(path):
        for name in files:
            try:
                total += os.lstat(os.path.join(root, name)).st_size
            except OSError:
                continue
    return total


def fingerprint():
    """The image's Debian packages and harness Python packages, hashed: two builds match only when every version does."""
    debian = subprocess.run([DPKG_QUERY, "-W", "-f", "${Package}=${Version}\n"], capture_output=True, text=True, check=True).stdout
    python = [f"{dist.metadata['Name'].lower()}=={dist.version}" for dist in importlib.metadata.distributions()]
    lines = [*sorted(debian.split()), "--", *sorted(python)]
    return hashlib.sha256("\n".join(lines).encode()).hexdigest()


def probe(path):
    result = subprocess.run([FFPROBE, "-v", "error", "-show_entries", "format=duration:stream=codec_type,codec_name,width,height",
                             "-of", "json", path], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        return None
    data = json.loads(result.stdout)
    video = next((stream for stream in data.get("streams", []) if stream.get("codec_type") == "video"), {})
    return {
        "seconds": float(data.get("format", {}).get("duration") or 0),
        "codec": video.get("codec_name"),
        "width": video.get("width"),
        "height": video.get("height"),
        "audio": any(stream.get("codec_type") == "audio" for stream in data.get("streams", [])),
    }


def write_json(path, value):
    with open(path, "w", encoding="utf-8", newline="\n") as file:
        json.dump(value, file, ensure_ascii=False, indent=2)


def check_env(args):
    pipeline, pipeline_sha256, found = load_pipeline(args.plan)
    models = []
    # An invalid pipeline.yaml is reported as it is; its models are not looked for.
    for model in [] if found else pipeline.get("models", []):
        path = os.path.normpath(os.path.join(args.env, model["path"]))
        entry = {**model, "bytes": None}
        if not path.startswith(os.path.normpath(args.env) + os.sep) or not os.path.isfile(path):
            found.append(f"模型 {model['name']} 不在 /env/{model['path']}")
        elif manifest.sha256_file(path) != model["sha256"]:
            found.append(f"模型 {model['name']} 的 sha256 与 pipeline.yaml 不符")
        else:
            entry["bytes"] = os.path.getsize(path)
        models.append(entry)
    write_json(args.out, {
        "pipeline_sha256": pipeline_sha256,
        "env_bytes": tree_size(args.env),
        "plan_bytes": tree_size(args.plan),
        "models": models,
        "problems": found,
    })
    return found


def prepare(args):
    pipeline, pipeline_sha256, found = load_pipeline(args.plan)
    cases = {}
    for case in args.cases:
        path = os.path.join(args.case_dir, f"{case}.json")
        try:
            with open(path, encoding="utf-8") as file:
                document = json.load(file)
        except (OSError, ValueError) as error:
            found.append(f"{case}.json 无法读取：{error}")
            continue
        problems = schema_problems("case.schema.json", document, f"{case}.json")
        if not problems and document["id"] != case:
            problems.append(f"{case}.json 的 id 是 {document['id']}")
        if not problems:
            problems += [f"{case}.json 的素材 {source['file']} 不属于这个用例"
                         for source in document["sources"] if not source["file"].startswith(case + "-")]
        found += problems
        if problems:
            continue
        sources = []
        for source in document["sources"]:
            file_path = os.path.join(args.case_dir, source["file"])
            if not os.path.isfile(file_path) or manifest.sha256_file(file_path) != source["sha256"]:
                found.append(f"{case} 的素材 {source['file']} 缺失或与 sha256 不符")
                continue
            facts = probe(file_path)
            if facts is None:
                found.append(f"{case} 的素材 {source['file']} ffprobe 读不出来")
                continue
            sources.append({"file": source["file"], **facts})
        cases[case] = {
            "provider": document["provider"],
            "request": document["request"],
            "sources": sources,
            "source_seconds": round(sum(source["seconds"] for source in sources), 3),
        }
    external = bool(pipeline) and any(stage["runs_on"] in EXTERNAL for stage in pipeline["stages"])
    write_json(args.out, {
        "plan": (pipeline or {}).get("plan"),
        "pipeline_sha256": pipeline_sha256,
        "external": external,
        "fingerprint": fingerprint(),
        "cases": cases,
        "problems": found,
    })
    return found


def timeline_ok(path):
    if not os.path.isfile(path):
        return None
    try:
        return isinstance(otio.adapters.read_from_file(path), otio.schema.Timeline)
    except (otio.exceptions.OTIOError, ValueError, TypeError, OSError):
        return False


def read_json(path):
    with open(path, encoding="utf-8") as file:
        return json.load(file)


def load_plan(folder):
    """One plan's runs inside a bundle: its pipeline and, per case, run.json, spans and the timeline check."""
    pipeline, _sha256, found = load_pipeline(folder)
    if found:
        sys.exit("\n".join(found))
    stages = [stage["id"] for stage in pipeline["stages"]]
    runs = {}
    cases_dir = os.path.join(folder, "cases")
    for case in sorted(os.listdir(cases_dir)):
        directory = os.path.join(cases_dir, case)
        try:
            spans = trace.load(os.path.join(directory, "trace.json"))
        except (OSError, ValueError, KeyError, TypeError, AttributeError):
            spans = None
        runs[case] = {
            "run": read_json(os.path.join(directory, "harness", "run.json")),
            "spans": spans,
            "timeline_ok": timeline_ok(os.path.join(directory, "timeline.otio")),
        }
    offline = None
    offline_dir = os.path.join(folder, "offline")
    if os.path.isdir(offline_dir):
        offline = {case: read_json(os.path.join(offline_dir, case, "harness", "run.json"))["ok"]
                   for case in sorted(os.listdir(offline_dir))}
    return {
        "pipeline": pipeline,
        "stages": stages,
        "runs": runs,
        "offline": offline,
        "session": read_json(os.path.join(folder, "session.json")),
        "env": read_json(os.path.join(folder, "env.json")),
        "prepare": read_json(os.path.join(folder, "prepare.json")),
    }


def load_result(bundle):
    """The plan of a bundle with its metrics, set against the baseline of the same bundle."""
    names = [name for name in os.listdir(bundle) if os.path.isdir(os.path.join(bundle, name)) and name != BASELINE]
    plan = load_plan(os.path.join(bundle, names[0]))
    baseline = load_plan(os.path.join(bundle, BASELINE))
    sources = {case: info["source_seconds"] for case, info in plan["prepare"]["cases"].items()}

    def measured(loaded):
        return {case: metrics.case_metrics(data["run"], data["spans"], loaded["stages"], sources.get(case))
                for case, data in loaded["runs"].items()}

    cases, base = measured(plan), measured(baseline)
    relatives = {case: metrics.relative(cases[case], base.get(case)) for case in cases}
    return {
        "name": plan["pipeline"]["plan"],
        "pipeline": plan["pipeline"],
        "session": plan["session"],
        "env": plan["env"],
        "cases": cases,
        "relative": relatives,
        "offline": plan["offline"],
        "findings": checks.findings(plan["pipeline"], plan["runs"]),
        "summary": metrics.summary(cases, relatives),
        "categories": metrics.category_seconds(plan["pipeline"], cases),
        "prepare": plan["prepare"],
    }


def write_report(args):
    a, b = load_result(args.a), load_result(args.b)
    session = a["session"]
    header = {key: session[key] for key in ("method_version", "harness_commit", "fingerprint", "manifest_sha256")}
    os.makedirs(args.out, exist_ok=True)
    for result in (a, b):
        with open(os.path.join(args.out, f"card-{result['name']}.md"), "w", encoding="utf-8", newline="\n") as file:
            file.write(report.render_card(result))
    with open(os.path.join(args.out, "report.md"), "w", encoding="utf-8", newline="\n") as file:
        file.write(report.render_report(a, b, a["prepare"]["cases"], header))
    write_json(os.path.join(args.out, "metrics.json"), {
        "method_version": METHOD_VERSION,
        "plans": {result["name"]: {key: result[key] for key in ("cases", "relative", "offline", "findings", "summary", "categories")}
                  for result in (a, b)},
    })


def describe(args):
    """Prints the checked pipeline.yaml as JSON for the host side, which reads no YAML."""
    pipeline, pipeline_sha256, found = load_pipeline(args.plan)
    if not found:
        print(json.dumps({"pipeline": pipeline, "sha256": pipeline_sha256}, ensure_ascii=False))
    return found


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)
    command = commands.add_parser("describe")
    command.add_argument("--plan", required=True)
    commands.add_parser("fingerprint")
    command = commands.add_parser("check-env")
    command.add_argument("--plan", required=True)
    command.add_argument("--env", required=True)
    command.add_argument("--out", required=True)
    command = commands.add_parser("prepare")
    command.add_argument("--plan", required=True)
    command.add_argument("--case-dir", required=True)
    command.add_argument("--out", required=True)
    command.add_argument("cases", nargs="+")
    command = commands.add_parser("report")
    command.add_argument("--a", required=True)
    command.add_argument("--b", required=True)
    command.add_argument("--out", required=True)
    args = parser.parse_args()
    if args.command == "report":
        write_report(args)
        return
    if args.command == "fingerprint":
        print(fingerprint())
        return
    found = {"describe": describe, "check-env": check_env, "prepare": prepare}[args.command](args)
    if found:
        sys.exit("\n".join(found))


if __name__ == "__main__":
    main()
