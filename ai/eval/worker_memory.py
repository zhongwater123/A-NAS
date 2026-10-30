"""Measures what each of the AI Worker's models adds to its memory.

Development only: the "memoryBytes" of each manifest in deploy/models, which
the photo service's gate weighs before background work loads the model
(docs/architecture/photo-objects.md#资源策略). Starts the Worker with the given
arguments and sends one request per model in the order a scan loads them,
printing after each the Worker's resident memory and its cgroup's memory and
peak, page cache included, as the gate counts it. With --text-photo it then
releases those models and reads that photo's text, as the reading phase does
(docs/architecture/photo-ocr.md#资源策略): the cgroup then holds the
llama-server child too, which the Worker's own resident memory leaves out.

Run it as the only process in a fresh cgroup (a container, or a scope of its
own: the cgroup is the one this process is in), with the model files already
in the page cache as they stay on the device, and the XNNPack cache kept
between runs as CacheDirectory keeps it: one container per run, the second and
later runs being the ones that count.

    docker run --rm --network none -v anas-xnnpack:/cache -v "$PWD/ai:/src/ai:ro" \\
        -v "$PWD/deploy/models:/manifests:ro" -v /mnt/d/A-NAS-models:/models:ro -v PHOTOS:/photos:ro \\
        anas-ai-dev-ort:trixie /opt/anas-ai/bin/python -I /src/ai/eval/worker_memory.py \\
        --photo /photos/pet.jpg --faces-photo /photos/group.jpg -- \\
        --model /models/embeddinggemma-2-740m/embeddinggemma-2-740m.litertlm \\
        --manifest /manifests/embeddinggemma-2-740m.json --cache-dir /cache ...

PET is a photo with one animal and no face, GROUP one with several faces.
"""

import argparse
import json
import os
import socket
import struct
import subprocess
import sys
import tempfile
import time

AI = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def call(path, request, image=None):
    """Sends one request the way internal/aiworker does and returns the response."""
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
        conn.connect(path)
        payload = json.dumps(request).encode()
        frame = struct.pack(">I", len(payload)) + payload
        if image is None:
            conn.sendall(frame)
        else:
            with open(image, "rb") as file:
                socket.send_fds(conn, [frame], [file.fileno()])
        header = b""
        while len(header) < 4:
            header += conn.recv(4 - len(header))
        (length,) = struct.unpack(">I", header)
        body = b""
        while len(body) < length:
            body += conn.recv(length - len(body))
        return json.loads(body)


def resident(pid):
    """The process's resident memory and its peak, in bytes."""
    values = {}
    with open(f"/proc/{pid}/status", encoding="ascii") as file:
        for line in file:
            name, _, rest = line.partition(":")
            if name in ("VmRSS", "VmHWM"):
                values[name] = int(rest.split()[0]) << 10
    return values["VmRSS"], values["VmHWM"]


def own_cgroup():
    """This process's cgroup v2 directory."""
    with open("/proc/self/cgroup", encoding="ascii") as file:
        for line in file:
            if line.startswith("0::"):
                return "/sys/fs/cgroup" + line[3:].strip().rstrip("/")
    return "/sys/fs/cgroup"


CGROUP = own_cgroup()


def cgroup(name):
    with open(f"{CGROUP}/memory.{name}", encoding="ascii") as file:
        return int(file.read())


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--photo", required=True, help="a photo with one animal and no face")
    parser.add_argument("--faces-photo", required=True, help="a photo with several faces")
    parser.add_argument("--text-photo", help="a photo with text, read after the other models are released")
    parser.add_argument("worker", nargs=argparse.REMAINDER, help="-- and the Worker's arguments")
    args = parser.parse_args()
    worker_args = args.worker[1:] if args.worker[:1] == ["--"] else args.worker
    path = os.path.join(tempfile.mkdtemp(), "ai.sock")
    worker = subprocess.Popen([sys.executable, "-m", "anas_ai.worker", "--socket", path, *worker_args],
                              cwd=AI, env={**os.environ, "PYTHONPATH": AI})
    deadline = time.monotonic() + 300
    while not os.path.exists(path):
        if time.monotonic() > deadline or worker.poll() is not None:
            raise SystemExit("the Worker did not start")
        time.sleep(0.2)
    print(f"{'MiB':20s}{'rss':>8s}{'+peak':>8s}{'cgroup':>8s}{'+peak':>8s}")
    last_rss, last_cgroup = 0, 0

    def step(name, request, image=None):
        nonlocal last_rss, last_cgroup
        if request is not None:
            response = call(path, request, image)
            if not response.get("ok"):
                raise SystemExit(f"{name}: {response}")
        rss, rss_peak = resident(worker.pid)
        current, peak = cgroup("current"), cgroup("peak")
        print(f"{name:20s}{rss >> 20:8d}{(rss_peak - last_rss) >> 20:8d}{current >> 20:8d}{(peak - last_cgroup) >> 20:8d}")
        last_rss, last_cgroup = rss, current
        return request is not None and response

    step("started", None)
    info = step("info", {"op": "info"})
    print(f"  info says memoryBytes = {info.get('memoryBytes', 0) >> 20} MiB, "
          f"ocrMemoryBytes = {info.get('ocrMemoryBytes', 0) >> 20} MiB")
    started = cgroup("current")
    step("embed_image", {"op": "embed_image"}, args.photo)
    detected = step("detect_objects", {"op": "detect_objects"}, args.photo)
    step("detect_faces (none)", {"op": "detect_faces"}, args.photo)
    step("detect_faces", {"op": "detect_faces"}, args.faces_photo)
    boxes = [box[2:] for box in detected["boxes"][:1]] or [[0.1, 0.1, 0.9, 0.9]]
    step("embed_animals", {"op": "embed_animals", "boxes": boxes}, args.photo)
    if args.text_photo:
        step("release detector", {"op": "release", "what": "detector"})
        before = cgroup("current")
        # Since Linux 6.12 a write resets the peak for reads through the same
        # descriptor: the reading phase's own peak. Older kernels keep the
        # peak since the start.
        with open(f"{CGROUP}/memory.peak", "r+", encoding="ascii") as peak_file:
            try:
                peak_file.write("reset")
                peak_file.flush()
                since = "the release"
            except OSError:
                since = "the start (this kernel cannot reset the peak)"
            began = time.monotonic()
            reading = step("read_text", {"op": "read_text", "kind": "text"}, args.text_photo)
            peak_file.seek(0)
            peak = int(peak_file.read())
        print(f"  read in {time.monotonic() - began:.1f} s, {len(reading['reading']['text'])} characters, "
              f"finish {reading['reading']['finish']}; peak since {since}: {(peak - before) >> 20} MiB over the "
              f"released Worker, {(peak - started) >> 20} MiB over its start")
    worker.terminate()
    worker.wait()


if __name__ == "__main__":
    main()
