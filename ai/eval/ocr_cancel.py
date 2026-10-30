"""Measures how soon hanging up stops a reading in the AI Worker's text reader.

Development only: docs/architecture/photo-ocr.md#资源策略 cancels a reading by
hanging up when the gate closes, and asks how long llama-server keeps the CPU
busy after that, above all while it encodes the image. This drives the
Worker's own LlamaReader: one reading to the end, whose server log gives the
encoding and generation times, then for each delay a reading that is
cancelled after it. For each it prints when read() returned and when the
server's CPU use fell below a third of a core, sampled every 0.1 s.

    python -I ai/eval/ocr_cancel.py --server llama/llama-server --model M.gguf --model-manifest M.json \\
        --mmproj P.gguf --mmproj-manifest P.json --runtime-manifest deploy/llama/llama.json \\
        --image receipt.jpg --at 3,15,45 [--threads 6] [--server-arg --no-mmap]
"""

import argparse
import os
import sys
import threading
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from anas_ai import providers

TICK = os.sysconf("SC_CLK_TCK")


def cpu_seconds(pid):
    with open(f"/proc/{pid}/stat", encoding="ascii") as file:
        fields = file.read().rpartition(")")[2].split()
    return (int(fields[11]) + int(fields[12])) / TICK


def idle_after(pid, since, limit=600):
    """Seconds from since until the process uses less than a third of a core over half a second."""
    window = []
    while time.monotonic() - since < limit:
        window.append((time.monotonic(), cpu_seconds(pid)))
        window = [sample for sample in window if sample[0] >= window[-1][0] - 0.5]
        if window[-1][0] - window[0][0] >= 0.45 and (window[-1][1] - window[0][1]) / (window[-1][0] - window[0][0]) < 1 / 3:
            return window[0][0] - since
        time.sleep(0.1)
    return None


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    for name in ("server", "model", "model-manifest", "mmproj", "mmproj-manifest", "runtime-manifest", "image"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--at", default="3,15,45", help="seconds after the request to hang up, comma-separated")
    parser.add_argument("--threads", type=int)
    parser.add_argument("--server-arg", action="append", default=[], help="another llama-server flag")
    args = parser.parse_args()
    reader = providers.LlamaReader([args.server, *args.server_arg], args.model, args.model_manifest, args.mmproj,
                                   args.mmproj_manifest, args.runtime_manifest, args.threads)
    with open(args.image, "rb") as file:
        image = file.read()
    try:
        began = time.monotonic()
        reader._start()
        print(f"server ready in {time.monotonic() - began:.1f} s", flush=True)
        began = time.monotonic()
        reading = reader.read(image, "text", None, reader.run_options())
        print(f"whole reading: {time.monotonic() - began:.1f} s, {len(reading['logprobs'])} tokens, "
              f"finish {reading['finish']}", flush=True)
        with open(os.path.join(reader._directory, "server.log"), encoding="utf-8", errors="replace") as log:
            for line in log:
                if "encoded in" in line or "decoded in" in line or "eval time" in line:
                    print("  " + line.strip(), flush=True)
        for at in (float(value) for value in args.at.split(",")):
            run = reader.run_options()
            outcome = {}

            def read(run=run, outcome=outcome):
                try:
                    reader.read(image, "text", None, run)
                    outcome["result"] = "finished"
                except providers.Cancelled:
                    outcome["result"] = "cancelled"
                outcome["returned"] = time.monotonic()

            worker = threading.Thread(target=read)
            started = time.monotonic()
            worker.start()
            time.sleep(at)
            hung_up = time.monotonic()
            run.terminate = True
            idle = idle_after(reader._process.pid, hung_up)
            worker.join()
            print(f"hang up at {at:.0f} s: read() {outcome['result']} after {outcome['returned'] - hung_up:.1f} s, "
                  f"server idle after {idle if idle is None else round(idle, 1)} s "
                  f"(request began {hung_up - started:.1f} s before)", flush=True)
    finally:
        reader.release()


if __name__ == "__main__":
    main()
