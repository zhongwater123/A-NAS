"""The video edit plans' evaluation harness, host side (docs/specs/video-edit-evaluation.md).

Standard library only: it drives Docker, and runs what needs PyYAML,
jsonschema or OpenTimelineIO inside the evaluation image
(eval.video_edit.inner). From ai/, on an x86_64 Linux or WSL2 machine:

    python3 -m eval.video_edit image
    python3 -m eval.video_edit fetch --manifest eval/video_edit/testset-v1.json --dest ~/video-edit/testset
    python3 -m eval.video_edit setup --plan <方案目录> --env ~/video-edit/env-A
    python3 -m eval.video_edit run --plan <方案目录> --env ~/video-edit/env-A \\
        --testset ~/video-edit/testset --manifest eval/video_edit/testset-v1.json --out ~/video-edit/runs
    (setup and run eval/video_edit/baseline the same way, into the same --out)
    python3 -m eval.video_edit pack --runs ~/video-edit/runs --out A.tar
    python3 -m eval.video_edit report --a A.tar --b B.tar --out report
    python3 -m eval.video_edit blind --a A.tar --b B.tar --out review --key key.json
    python3 -m eval.video_edit unblind --key key.json --review review/review.csv --out result
"""

import argparse
import datetime
import json
import os
import platform
import random
import shutil
import subprocess
import sys
import tarfile
import tempfile

from eval.video_edit import (
    BASELINE,
    CPUS_NEEDED,
    CPUSET,
    IMAGE,
    MEMORY_LIMIT,
    METHOD_VERSION,
    OUTPUTS,
    TIMEOUT_SECONDS,
    blind,
    manifest,
)

HERE = os.path.dirname(os.path.abspath(__file__))
AI = os.path.dirname(os.path.dirname(HERE))
REPO = os.path.dirname(AI)
# Only the harness's own steps see /harness on their path; a plan's processes do not.
INNER = ["/usr/bin/env", "PYTHONPATH=/harness", "/opt/harness/bin/python", "-m", "eval.video_edit.inner"]
# Session facts that must agree between a plan and its baseline, and between two bundles.
SAME = ("method_version", "harness_commit", "dev", "fingerprint", "manifest_sha256", "cases")


def executable(name):
    path = shutil.which(name)
    if path is None:
        sys.exit(f"找不到 {name}")
    return path


def capture(command):
    result = subprocess.run(command, capture_output=True, text=True, check=False)
    return result.stdout.strip() if result.returncode == 0 else None


def mount(source, target, writable=False):
    option = f"type=bind,source={os.path.abspath(source)},target={target}"
    return ["--mount", option if writable else option + ",readonly"]


def container(command, mounts, *, network=True, limits=False, workdir=None):
    """docker run for IMAGE as the calling user, with the harness at /harness."""
    args = [executable("docker"), "run", "--rm", "--init", "--user", f"{os.getuid()}:{os.getgid()}",
            "--env", "HOME=/tmp", "--env", "PYTHONDONTWRITEBYTECODE=1", *mount(AI, "/harness")]
    if limits:
        args += ["--cpuset-cpus", CPUSET, "--memory", MEMORY_LIMIT, "--memory-swap", MEMORY_LIMIT]
    if not network:
        args += ["--network", "none"]
    if workdir:
        args += ["--workdir", workdir]
    for item in mounts:
        args += item
    return [*args, IMAGE, *command]


def need_image():
    if capture([executable("docker"), "image", "inspect", "--format", "{{.Id}}", IMAGE]) is None:
        sys.exit(f"没有镜像 {IMAGE}：先运行 python3 -m eval.video_edit image")


def read_json(path):
    with open(path, encoding="utf-8") as file:
        return json.load(file)


def write_json(path, value):
    with open(path, "w", encoding="utf-8", newline="\n") as file:
        json.dump(value, file, ensure_ascii=False, indent=2)


def describe(plan):
    output = capture(container([*INNER, "describe", "--plan", "/plan"], [mount(plan, "/plan")]))
    if output is None:
        subprocess.run(container([*INNER, "describe", "--plan", "/plan"], [mount(plan, "/plan")]), check=False)
        sys.exit("pipeline.yaml 没有通过检查")
    return json.loads(output)["pipeline"]


def machine():
    def field(path, key, separator):
        try:
            with open(path, encoding="utf-8") as file:
                for line in file:
                    name, _, value = line.partition(separator)
                    if name.strip() == key:
                        return value.strip().strip('"')
        except OSError:
            pass
        return ""

    docker = executable("docker")
    return {
        "arch": platform.machine(),
        "cpu": field("/proc/cpuinfo", "model name", ":"),
        "cpus": os.cpu_count(),
        "memory_bytes": int(field("/proc/meminfo", "MemTotal", ":").split()[0] or 0) * 1024,
        "os": field("/etc/os-release", "PRETTY_NAME", "="),
        "kernel": platform.release(),
        "docker": capture([docker, "version", "--format", "{{.Server.Version}}"]) or "",
        "cgroup": capture([docker, "info", "--format", "{{.CgroupVersion}}"]) or "",
    }


def machine_problems(facts):
    found = []
    if facts["arch"] != "x86_64":
        found.append(f"设备是 {facts['arch']}，正式测评要 x86_64")
    if (facts["cpus"] or 0) < CPUS_NEEDED:
        found.append(f"设备只有 {facts['cpus']} 个 CPU，至少要 {CPUS_NEEDED} 个")
    if facts["cgroup"] != "2":
        found.append(f"Docker 用的是 cgroup v{facts['cgroup'] or '?'}，要 v2")
    return found


def harness_state():
    """The commit and whether ai/eval/video_edit has changes; (None, True) without git."""
    git = shutil.which("git")
    if git is None:
        return None, True
    commit = capture([git, "-C", REPO, "rev-parse", "HEAD"])
    changes = capture([git, "-C", REPO, "status", "--porcelain", "--", "ai/eval/video_edit"])
    return commit, commit is None or changes != ""


def command_image(args):
    subprocess.run([executable("bash"), os.path.join(HERE, "image", "build.sh"), IMAGE, args.builder], check=True)
    print("环境指纹", capture(container([*INNER, "fingerprint"], [])))


def command_manifest(args):
    write_json(args.out, manifest.build(args.dir))
    print(f"写入 {args.out}")


def command_fetch(args):
    document, _sha256 = manifest.load(args.manifest)
    os.makedirs(args.dest, exist_ok=True)
    for entry in document["files"]:
        path = os.path.join(args.dest, entry["name"])
        if os.path.isfile(path) and manifest.sha256_file(path) == entry["sha256"]:
            continue
        print(f"下载 {entry['name']}（{entry['size'] / 2**20:,.0f} MiB）", flush=True)
        manifest.download(f"{manifest.RELEASE_URL}/{entry['name']}", path, entry["sha256"])
    print("\n".join(manifest.problems(args.dest, document)) or "测试集齐全")


def command_setup(args):
    need_image()
    plan, env = os.path.abspath(args.plan), os.path.abspath(args.env)
    pipeline = describe(plan)
    os.makedirs(env, exist_ok=True)
    with tempfile.TemporaryDirectory() as tmp:
        if pipeline.get("setup"):
            subprocess.run(container(["bash", f"/plan/{pipeline['setup']}"],
                                     [mount(plan, "/plan"), mount(env, "/env", True), mount(tmp, "/tmp", True)],  # noqa: S108  the container's /tmp
                                     workdir="/plan"), check=True)
    result = subprocess.run(container([*INNER, "check-env", "--plan", "/plan", "--env", "/env", "--out", "/env/harness-env.json"],
                                      [mount(plan, "/plan"), mount(env, "/env", True)]), check=False)
    if result.returncode != 0:
        sys.exit("安装后的核对没有通过")
    info = read_json(os.path.join(env, "harness-env.json"))
    print(f"方案 {pipeline['plan']} 安装完成：/env {info['env_bytes'] / 2**20:,.0f} MiB")


def command_run(args):
    need_image()
    facts = machine()
    if found := machine_problems(facts):
        sys.exit("\n".join(found))
    commit, changed = harness_state()
    if changed and not args.dev:
        sys.exit("测评管线有未提交的改动或不在 Git 仓库里：正式运行要干净的提交，试跑加 --dev")
    if (args.cases or args.timeout_hours) and not args.dev:
        sys.exit("--cases 与 --timeout-hours 只用于试跑（--dev）")
    plan, env, testset = os.path.abspath(args.plan), os.path.abspath(args.env), os.path.abspath(args.testset)
    document, manifest_sha256 = manifest.load(args.manifest)
    print("核对测试集…", flush=True)
    if found := manifest.problems(testset, document):
        sys.exit("\n".join(found))
    cases = args.cases or manifest.case_ids(document)
    env_info_path = os.path.join(env, "harness-env.json")
    if not os.path.isfile(env_info_path) or read_json(env_info_path)["problems"]:
        sys.exit("方案还没有安装好：先运行 setup")
    pipeline = describe(plan)
    out = os.path.join(os.path.abspath(args.out), pipeline["plan"])
    os.makedirs(out, exist_ok=True)

    result = subprocess.run(container([*INNER, "prepare", "--plan", "/plan", "--case-dir", "/case", "--out", "/work/prepare.json", *cases],
                                      [mount(plan, "/plan"), mount(testset, "/case"), mount(out, "/work", True)]), check=False)
    if result.returncode != 0:
        sys.exit("用例核对没有通过")
    prepared = read_json(os.path.join(out, "prepare.json"))
    timeout = args.timeout_hours * 3600 if args.timeout_hours else TIMEOUT_SECONDS
    session = {
        "method_version": METHOD_VERSION,
        "plan": pipeline["plan"],
        "harness_commit": commit or "",
        "dev": changed,
        "fingerprint": prepared["fingerprint"],
        "manifest_sha256": manifest_sha256,
        "manifest_release": document["release"],
        "cases": cases,
        "machine": facts,
        "image": IMAGE,
        "cpuset": CPUSET,
        "memory_limit": MEMORY_LIMIT,
        "timeout_seconds": timeout,
        "started_at": datetime.datetime.now(datetime.UTC).isoformat(timespec="seconds"),
    }
    session_path = os.path.join(out, "session.json")
    if os.path.isfile(session_path):
        earlier = read_json(session_path)
        if any(earlier[key] != session[key] for key in (*SAME, "machine")):
            sys.exit(f"{out} 里已有另一次运行的结果（管线、环境、测试集或设备不同）：换一个 --out，或删掉它")
        session["started_at"] = earlier["started_at"]
    write_json(session_path, session)
    shutil.copyfile(os.path.join(plan, "pipeline.yaml"), os.path.join(out, "pipeline.yaml"))
    shutil.copyfile(env_info_path, os.path.join(out, "env.json"))

    passes = [("cases", True)] + ([("offline", False)] if prepared["external"] else [])
    expect = [item for name in OUTPUTS for item in ("--expect", name)]
    for folder, network in passes:
        for case in cases:
            case_out = os.path.join(out, folder, case)
            if os.path.isfile(os.path.join(case_out, "harness", "run.json")):
                print(f"{folder} {case}：已有结果，跳过", flush=True)
                continue
            shutil.rmtree(case_out, ignore_errors=True)
            os.makedirs(case_out)
            tmp = os.path.join(out, ".tmp", case)
            shutil.rmtree(tmp, ignore_errors=True)
            os.makedirs(tmp)
            command = ["/usr/bin/python3", "-I", "/harness/eval/video_edit/sampler.py", "--out", "/out", "--timeout", str(timeout),
                       *expect, "--", *pipeline["entry"], "--case", f"/case/{case}.json", "--out", "/out"]
            mounts = [mount(plan, "/plan"), mount(env, "/env"), mount(testset, "/case"),
                      mount(case_out, "/out", True), mount(tmp, "/tmp", True)]  # noqa: S108  the container's /tmp
            print(f"{folder} {case}：", end="", flush=True)
            if subprocess.run(container(command, mounts, network=network, limits=True, workdir="/plan"), check=False).returncode != 0:
                sys.exit(f"测评管线在 {case} 出错")
            shutil.rmtree(tmp, ignore_errors=True)
    shutil.rmtree(os.path.join(out, ".tmp"), ignore_errors=True)


def sessions(runs):
    found = {}
    for name in sorted(os.listdir(runs)):
        path = os.path.join(runs, name, "session.json")
        if os.path.isfile(path):
            found[name] = read_json(path)
    return found


def command_pack(args):
    runs = os.path.abspath(args.runs)
    found = sessions(runs)
    plans = [name for name in found if name != BASELINE]
    if BASELINE not in found or len(plans) != 1:
        sys.exit("结果目录里要正好有一个方案和 baseline 的结果")
    plan = plans[0]
    differ = [key for key in (*SAME, "machine") if found[plan][key] != found[BASELINE][key]]
    if differ:
        sys.exit(f"方案与 baseline 的结果来源不一致：{'、'.join(differ)}")
    for name, session in found.items():
        folders = ["cases"] + (["offline"] if read_json(os.path.join(runs, name, "prepare.json"))["external"] else [])
        missing = [f"{name}/{folder}/{case}" for folder in folders for case in session["cases"]
                   if not os.path.isfile(os.path.join(runs, name, folder, case, "harness", "run.json"))]
        if missing:
            sys.exit("还没有跑完：" + "、".join(missing))
    files = {}
    for name in found:
        for root, dirs, names in os.walk(os.path.join(runs, name)):
            dirs[:] = sorted(item for item in dirs if item != ".tmp")
            for item in sorted(names):
                path = os.path.join(root, item)
                files[os.path.relpath(path, runs).replace(os.sep, "/")] = manifest.sha256_file(path)
    bundle = {"method_version": METHOD_VERSION, "plan": plan, "files": files,
              "created_at": datetime.datetime.now(datetime.UTC).isoformat(timespec="seconds")}
    write_json(os.path.join(runs, "bundle.json"), bundle)
    with tarfile.open(args.out, "w") as archive:
        archive.add(os.path.join(runs, "bundle.json"), "bundle.json")
        for name in files:
            archive.add(os.path.join(runs, name), name)
    print(f"方案 {plan}：{len(files)} 个文件写入 {args.out}")


def unpack(path, folder):
    """Extracts a bundle and checks every file against bundle.json; returns the bundle and the plan's session."""
    with tarfile.open(path) as archive:
        archive.extractall(folder, filter="data")
    bundle = read_json(os.path.join(folder, "bundle.json"))
    present = set()
    for root, _dirs, names in os.walk(folder):
        present |= {os.path.relpath(os.path.join(root, name), folder).replace(os.sep, "/") for name in names}
    present.discard("bundle.json")
    if present != set(bundle["files"]):
        sys.exit(f"{path} 的文件与 bundle.json 不符")
    if bad := [name for name, sha256 in bundle["files"].items() if manifest.sha256_file(os.path.join(folder, name)) != sha256]:
        sys.exit(f"{path} 中 {len(bad)} 个文件的 sha256 不符，例如 {bad[0]}")
    return bundle, sessions(folder)[bundle["plan"]]


def unpack_pair(args, work):
    a_dir, b_dir = os.path.join(work, "a"), os.path.join(work, "b")
    a, a_session = unpack(args.a, a_dir)
    b, b_session = unpack(args.b, b_dir)
    if a["plan"] == b["plan"]:
        sys.exit(f"两个结果包都是方案 {a['plan']}")
    if differ := [key for key in SAME if a_session[key] != b_session[key]]:
        sys.exit(f"两个结果包的口径不一致：{'、'.join(differ)}")
    if a_session["dev"] and not args.allow_dev:
        sys.exit("这是试跑（--dev）的结果：正式报告不用它，查看时加 --allow-dev")
    return (a_dir, a), (b_dir, b)


def command_report(args):
    need_image()
    os.makedirs(args.out, exist_ok=True)
    with tempfile.TemporaryDirectory() as work:
        (a_dir, _a), (b_dir, _b) = unpack_pair(args, work)
        result = subprocess.run(container([*INNER, "report", "--a", "/a", "--b", "/b", "--out", "/report"],
                                          [mount(a_dir, "/a"), mount(b_dir, "/b"), mount(args.out, "/report", True)]), check=False)
    if result.returncode != 0:
        sys.exit("报告没有生成")
    print(f"报告写入 {os.path.join(args.out, 'report.md')}")


def command_blind(args):
    out, key_path = os.path.abspath(args.out), os.path.abspath(args.key)
    if key_path.startswith(out + os.sep):
        sys.exit("对照表要放在评审目录之外")
    if os.path.exists(out) and os.listdir(out):
        sys.exit(f"{out} 不是空目录")
    with tempfile.TemporaryDirectory() as work:
        pairs = unpack_pair(args, work)
        outputs, failed = {}, set()
        for folder, bundle in pairs:
            outputs[bundle["plan"]] = {}
            cases_dir = os.path.join(folder, bundle["plan"], "cases")
            for case in sorted(os.listdir(cases_dir)):
                run = read_json(os.path.join(cases_dir, case, "harness", "run.json"))
                if run["ok"]:
                    outputs[bundle["plan"]][case] = os.path.join(cases_dir, case, "output.mp4")
                else:
                    failed.add(case)
        plans = [bundle["plan"] for _folder, bundle in pairs]
        cases = sorted(set.intersection(*(set(items) for items in outputs.values())))
        key = blind.assign(cases, plans, random.SystemRandom())
        os.makedirs(out, exist_ok=True)
        blind.write_review(key, outputs, out)
    write_json(key_path, {"plans": plans, "cases": key, "skipped": sorted(failed)})
    print(f"{len(cases)} 个用例写入 {out}；对照表 {key_path}")
    if failed:
        print("有一方失败、没有放进评审的用例：" + "、".join(sorted(failed)))


def command_unblind(args):
    key = read_json(args.key)
    rows, choices = blind.unblind(key["cases"], blind.read_rows(args.review))
    os.makedirs(args.out, exist_ok=True)
    blind.write_rows(os.path.join(args.out, "unblinded.csv"), rows)
    lines = ["# 盲评结果", "", *blind.tally(rows, key["plans"], choices)]
    if key["skipped"]:
        lines += ["", "有一方失败、没有放进评审的用例：" + "、".join(key["skipped"])]
    with open(os.path.join(args.out, "summary.md"), "w", encoding="utf-8", newline="\n") as file:
        file.write("\n".join(lines) + "\n")
    print("\n".join(lines))


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)
    command = commands.add_parser("image", help="构建测评镜像")
    command.add_argument("--builder", default="debian:bookworm", help="能安装 debootstrap 的本地 Debian 镜像")
    command = commands.add_parser("manifest", help="为测试集目录生成清单")
    command.add_argument("--dir", required=True)
    command.add_argument("--out", required=True)
    command = commands.add_parser("fetch", help="按清单从 Release 下载测试集")
    command.add_argument("--manifest", required=True)
    command.add_argument("--dest", required=True)
    command = commands.add_parser("setup", help="安装方案")
    command.add_argument("--plan", required=True)
    command.add_argument("--env", required=True)
    command = commands.add_parser("run", help="逐个用例运行方案")
    for name in ("--plan", "--env", "--testset", "--manifest", "--out"):
        command.add_argument(name, required=True)
    command.add_argument("--dev", action="store_true", help="试跑：允许管线有改动，结果不能用于正式报告")
    command.add_argument("--cases", nargs="+", help="试跑时只跑这些用例")
    command.add_argument("--timeout-hours", type=float, help="试跑时改每个用例的时限")
    command = commands.add_parser("pack", help="把一台设备的结果打成结果包")
    command.add_argument("--runs", required=True)
    command.add_argument("--out", required=True)
    for name, help_text in (("report", "核对两个结果包并写报告"), ("blind", "准备盲评材料")):
        command = commands.add_parser(name, help=help_text)
        command.add_argument("--a", required=True)
        command.add_argument("--b", required=True)
        command.add_argument("--out", required=True)
        command.add_argument("--allow-dev", action="store_true", help="也接受试跑的结果包")
        if name == "blind":
            command.add_argument("--key", required=True, help="对照表的路径，在评审目录之外")
    command = commands.add_parser("unblind", help="把评审表的 X、Y 换回方案")
    command.add_argument("--key", required=True)
    command.add_argument("--review", required=True)
    command.add_argument("--out", required=True)
    args = parser.parse_args()
    {
        "image": command_image, "manifest": command_manifest, "fetch": command_fetch, "setup": command_setup,
        "run": command_run, "pack": command_pack, "report": command_report, "blind": command_blind,
        "unblind": command_unblind,
    }[args.command](args)


if __name__ == "__main__":
    main()
