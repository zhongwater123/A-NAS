"""Runs one plan command inside its evaluation container and measures it.

Docker's init is PID 1; this script starts the plan, reads the container's own
cgroup (the namespace root, /sys/fs/cgroup) every second, sizes /tmp and the
output directory every five seconds, and writes harness/run.json and
harness/samples.csv into the output directory
(docs/specs/video-edit-evaluation.md#指标). It runs on the image's system
Python with the standard library only, outside the eval package:

    python3 -I sampler.py --out /out --timeout 10800 --expect output.mp4 -- <entry> --case … --out /out
"""

import argparse
import csv
import datetime
import json
import os
import shutil
import signal
import subprocess
import sys
import time

CGROUP = "/sys/fs/cgroup"
NET = "/sys/class/net"
DISK_EVERY = 5


def read(path, default=""):
    try:
        with open(path, encoding="ascii") as file:
            return file.read()
    except OSError:
        return default


def keyed(text):
    """The "key value" lines of memory.stat, cpu.stat and memory.events."""
    values = {}
    for line in text.splitlines():
        key, _, value = line.partition(" ")
        if value.strip().isdigit():
            values[key] = int(value)
    return values


def number(path):
    text = read(path).strip()
    return int(text) if text.isdigit() else 0


def net_bytes(root=NET):
    """Received and sent bytes over every interface but loopback."""
    received = sent = 0
    for name in sorted(os.listdir(root)) if os.path.isdir(root) else []:
        if name != "lo":
            received += number(os.path.join(root, name, "statistics", "rx_bytes"))
            sent += number(os.path.join(root, name, "statistics", "tx_bytes"))
    return received, sent


def tree_size(path, skip=None):
    """Bytes under path, leaving out the directory skip; files that vanish meanwhile count as gone."""
    total = 0
    try:
        entries = list(os.scandir(path))
    except OSError:
        return 0
    for entry in entries:
        try:
            if entry.is_dir(follow_symlinks=False):
                if entry.path != skip:
                    total += tree_size(entry.path, skip)
            else:
                total += entry.stat(follow_symlinks=False).st_size
        except OSError:
            continue
    return total


def raise_oom_score():
    # The plan, not this sampler, is what the kernel should kill at the limit.
    with open("/proc/self/oom_score_adj", "w", encoding="ascii") as file:
        file.write("1000")


def measure(command, out, tmp, timeout, expect, interval=1.0, cgroup=CGROUP):
    harness = os.path.join(out, "harness")
    shutil.rmtree(harness, ignore_errors=True)
    os.makedirs(harness)
    load = read("/proc/loadavg").split()[:3]
    received0, sent0 = net_bytes()
    harness_bytes = number(os.path.join(cgroup, "memory.current"))
    samples = []
    anon_peak = file_peak = disk_peak = 0
    timed_out = False

    started = datetime.datetime.now(datetime.UTC).isoformat(timespec="seconds")
    with open(os.path.join(harness, "stdout.log"), "wb") as stdout, open(os.path.join(harness, "stderr.log"), "wb") as stderr:
        begin = time.monotonic()
        process = subprocess.Popen(command, stdout=stdout, stderr=stderr, start_new_session=True, preexec_fn=raise_oom_score)
        tick = 0
        while True:
            try:
                process.wait(timeout=interval)
                break
            except subprocess.TimeoutExpired:
                pass
            tick += 1
            elapsed = time.monotonic() - begin
            stat = keyed(read(os.path.join(cgroup, "memory.stat")))
            anon_peak = max(anon_peak, stat.get("anon", 0))
            file_peak = max(file_peak, stat.get("file", 0))
            samples.append((round(elapsed, 1), number(os.path.join(cgroup, "memory.current")), stat.get("anon", 0),
                            stat.get("file", 0), keyed(read(os.path.join(cgroup, "cpu.stat"))).get("usage_usec", 0)))
            if tick % DISK_EVERY == 0:
                disk_peak = max(disk_peak, tree_size(tmp) + tree_size(out, skip=harness))
            if elapsed > timeout:
                timed_out = True
                os.killpg(process.pid, signal.SIGKILL)
        wall = time.monotonic() - begin

    disk_peak = max(disk_peak, tree_size(tmp) + tree_size(out, skip=harness))
    received1, sent1 = net_bytes()
    events = keyed(read(os.path.join(cgroup, "memory.events")))
    outputs = {}
    for name in expect:
        path = os.path.join(out, name)
        outputs[name] = os.path.getsize(path) if os.path.isfile(path) and os.path.getsize(path) > 0 else None
    code = process.returncode
    failure = None
    if timed_out:
        failure = "超时"
    elif events.get("oom_kill", 0):
        failure = "内存超限，被 OOM 杀掉"
    elif code != 0:
        failure = f"退出码 {code}" if code > 0 else f"被信号 {-code} 结束"
    elif missing := [name for name, size in outputs.items() if size is None]:
        failure = "缺少 " + "、".join(missing)

    run = {
        "command": command,
        "started_at": started,
        "wall_seconds": round(wall, 3),
        "exit_code": code,
        "timed_out": timed_out,
        "oom_kill": events.get("oom_kill", 0),
        "cpu_seconds": round(keyed(read(os.path.join(cgroup, "cpu.stat"))).get("usage_usec", 0) / 1e6, 3),
        "memory_peak_bytes": number(os.path.join(cgroup, "memory.peak")),
        "memory_harness_bytes": harness_bytes,
        "anon_peak_bytes": anon_peak,
        "file_peak_bytes": file_peak,
        "disk_peak_bytes": disk_peak,
        "net_received_bytes": received1 - received0,
        "net_sent_bytes": sent1 - sent0,
        "load_at_start": load,
        "outputs": outputs,
        "ok": failure is None,
        "failure": failure,
    }
    with open(os.path.join(harness, "samples.csv"), "w", encoding="ascii", newline="\n") as file:
        writer = csv.writer(file, lineterminator="\n")
        writer.writerow(("seconds", "memory_current", "anon", "file", "cpu_usec"))
        writer.writerows(samples)
    with open(os.path.join(harness, "run.json"), "w", encoding="utf-8", newline="\n") as file:
        json.dump(run, file, ensure_ascii=False, indent=2)
    return run


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--out", required=True)
    parser.add_argument("--tmp", default="/tmp")  # noqa: S108  the container's own /tmp, a fresh bind mount per case
    parser.add_argument("--timeout", type=float, required=True)
    parser.add_argument("--expect", action="append", default=[])
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if not command:
        sys.exit("no command")
    run = measure(command, args.out, args.tmp, args.timeout, args.expect)
    print(json.dumps({key: run[key] for key in ("ok", "failure", "wall_seconds", "memory_peak_bytes")}, ensure_ascii=False))


if __name__ == "__main__":
    main()
