"""Shared harness of the Qdrant retrieval trial (docs/research/qdrant-retrieval-features.md).

Development only. The official static Qdrant binary (the musl build of the
GitHub release, not a container image) runs as a child of the evaluation
process inside anas-ai-dev:trixie, so a run can stop it, push its files out of
the page cache and start it again. Telemetry is off, it listens on loopback
only, and its storage lives on WSL's own disk rather than /mnt/d:

    docker run --rm --user $(id -u):$(id -g) -e HOME=/tmp --cpuset-cpus 0-7 \\
      -v $PWD:/src -w /src/ai -v /mnt/d/A-NAS-datasets:/datasets -v ~/qdrant-eval:/qe \\
      anas-ai-dev:trixie /qe/venv/bin/python -m eval.qdrant_facts

/qe/venv comes from eval/qdrant-requirements.lock. Eight CPUs match the NAS's
i3-12100 (4 cores, 8 threads), so Qdrant's CPU-derived defaults (segments,
indexing threads) are the ones the device would pick.
"""

import contextlib
import hashlib
import json
import os
import shutil
import socket
import statistics
import subprocess
import time
import urllib.error
import urllib.request

import numpy as np

from eval import datasets
from eval.embed import QUERY_PROMPT, load_vectors

BINARY = os.environ.get("ANAS_QDRANT_BINARY", "/qe/bin/qdrant")
STORAGE = os.environ.get("ANAS_QDRANT_STORAGE", "/qe/storage")
RUNS = os.path.join(datasets.ROOT, "runs", "qdrant")
# Away from Qdrant's own 6333/6334, which a local product stack may hold; a
# second run beside a long one takes another base.
HTTP_PORT = int(os.environ.get("ANAS_QDRANT_PORT", "16333"))
GRPC_PORT = HTTP_PORT + 1
IMAGE_VECTORS = os.path.join(
    datasets.ROOT, "cache", "embeddinggemma-2-740m@e7a8a2204b91+mediapipe-1.1.0+tok70+l2-e512")
DETECTIONS = os.path.join(datasets.ROOT, "cache", "dfine-m-obj365@cd832825f883+ort-1.31.0+640+s0.05k100")
# Two scores closer than this are a tie: Qdrant's SIMD and numpy's BLAS sum in different orders.
TIE = 1e-5


class Server:
    """One Qdrant process over its own storage directory."""

    def __init__(self, name, settings=None, fresh=True):
        self.root = os.path.join(STORAGE, name)
        if fresh and os.path.exists(self.root):
            shutil.rmtree(self.root)
        os.makedirs(self.root, exist_ok=True)
        self.storage = os.path.join(self.root, "storage")
        self.settings = dict(settings or {})
        self.process = None

    def environment(self):
        env = {
            "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
            "QDRANT__TELEMETRY_DISABLED": "true",
            "QDRANT__SERVICE__HOST": "127.0.0.1",
            "QDRANT__SERVICE__HTTP_PORT": str(HTTP_PORT),
            "QDRANT__SERVICE__GRPC_PORT": str(GRPC_PORT),
            "QDRANT__STORAGE__STORAGE_PATH": self.storage,
            "QDRANT__STORAGE__SNAPSHOTS_PATH": os.path.join(self.root, "snapshots"),
            "QDRANT__STORAGE__TEMP_PATH": os.path.join(self.root, "tmp"),
            "QDRANT__LOG_LEVEL": "WARN",
        }
        env.update(self.settings)
        return env

    def start(self):
        """Starts the process; seconds from the start until gRPC accepts and until /readyz answers."""
        if self.process is not None:
            raise RuntimeError("already running")
        with open(os.path.join(self.root, "qdrant.log"), "ab") as log:
            started = time.perf_counter()
            self.process = subprocess.Popen([BINARY, "--disable-telemetry"], cwd=self.root, env=self.environment(),
                                            stdout=log, stderr=subprocess.STDOUT)
        accepts = self._wait(lambda: port_open(GRPC_PORT))
        ready = self._wait(lambda: http_status("/readyz") == 200)
        self.started = started
        return {"grpcAcceptsS": accepts - started, "readyS": ready - started}

    def stop(self):
        if self.process is None:
            return
        self.process.terminate()
        try:
            self.process.wait(60)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait()
        self.process = None
        # The ports must be free before the next start measures anything.
        self._wait(lambda: not port_open(GRPC_PORT) and not port_open(HTTP_PORT), alive=False)

    def evict(self):
        """Pushes the storage files out of the page cache, without root: a start after it reads from disk.

        WSL's ext4 sits in a virtual disk that Windows caches too, so this is
        the coldest a start gets on the development machine, not a cold boot.
        """
        for folder, _dirs, files in os.walk(self.storage):
            for name in files:
                with contextlib.suppress(OSError):
                    fd = os.open(os.path.join(folder, name), os.O_RDONLY)
                    try:
                        os.fsync(fd)
                        os.posix_fadvise(fd, 0, 0, os.POSIX_FADV_DONTNEED)
                    finally:
                        os.close(fd)

    def memory(self):
        """MiB of the process: VmRSS, VmHWM and the anonymous, file and shared parts of RSS."""
        fields = {}
        with open(f"/proc/{self.process.pid}/status", encoding="ascii") as file:
            for line in file:
                name, _sep, value = line.partition(":")
                if name in ("VmRSS", "VmHWM", "RssAnon", "RssFile", "RssShmem"):
                    fields[name] = round(int(value.split()[0]) / 1024, 1)
        return fields

    def disk(self, collection=None):
        """MiB on disk: allocated blocks and apparent size, all and the write-ahead log alone."""
        base = os.path.join(self.storage, "collections", collection) if collection else self.storage
        total = {"allocated": 0, "apparent": 0, "walAllocated": 0, "walApparent": 0}
        for folder, _dirs, files in os.walk(base):
            for name in files:
                path = os.path.join(folder, name)
                info = os.lstat(path)
                allocated, apparent = info.st_blocks * 512, info.st_size
                total["allocated"] += allocated
                total["apparent"] += apparent
                if f"{os.sep}wal" in path[len(base):]:
                    total["walAllocated"] += allocated
                    total["walApparent"] += apparent
        return {key: round(value / 2**20, 1) for key, value in total.items()}

    def log_tail(self, lines=20):
        with open(os.path.join(self.root, "qdrant.log"), encoding="utf-8", errors="replace") as file:
            return "".join(file.readlines()[-lines:])

    def _wait(self, ready, timeout=300, alive=True):
        deadline = time.perf_counter() + timeout
        while time.perf_counter() < deadline:
            if ready():
                return time.perf_counter()
            if alive and self.process.poll() is not None:
                raise RuntimeError(f"qdrant exited with {self.process.returncode}:\n{self.log_tail()}")
            time.sleep(0.002)
        raise TimeoutError(f"qdrant did not get there in {timeout} s:\n{self.log_tail()}")

    def __enter__(self):
        return self

    def __exit__(self, *_exc):
        self.stop()


def port_open(port):
    with socket.socket() as sock:
        sock.settimeout(0.05)
        return sock.connect_ex(("127.0.0.1", port)) == 0


def http_status(path):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{HTTP_PORT}{path}", timeout=1) as response:
            return response.status
    except (urllib.error.URLError, OSError):
        return None


def rest(method, path, body=None):
    """(status, decoded JSON) of a raw REST call: for what the Python client checks or embeds on its own side."""
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(f"http://127.0.0.1:{HTTP_PORT}{path}", data=data, method=method,
                                     headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=600) as response:
            return response.status, json.loads(response.read() or b"null")
    except urllib.error.HTTPError as error:
        return error.code, json.loads(error.read() or b"null")


def query_errors():
    """What a refused request raises: gRPC's error, or the REST client's."""
    import grpc
    from qdrant_client.http.exceptions import UnexpectedResponse

    return grpc.RpcError, UnexpectedResponse


def error_text(error):
    """The server's own words from a gRPC or REST error."""
    details = getattr(error, "details", None)
    text = details() if callable(details) else str(error)
    return " ".join(text.split())[:400]


def client():
    from qdrant_client import QdrantClient

    return QdrantClient(host="127.0.0.1", port=HTTP_PORT, grpc_port=GRPC_PORT, prefer_grpc=True, timeout=600,
                        check_compatibility=False, grpc_options={"grpc.max_receive_message_length": 1 << 30,
                                      "grpc.max_send_message_length": 1 << 30})


def wait_indexed(qdrant, collection, timeout=3600, settle=3):
    """Seconds until the collection turned green with its optimizers idle, and stayed so for `settle` checks."""
    started, quiet, since = time.perf_counter(), 0, None
    while time.perf_counter() - started < timeout:
        info = qdrant.get_collection(collection)
        status = getattr(info.status, "value", info.status)
        optimizer = getattr(info.optimizer_status, "value", info.optimizer_status)
        if status == "green" and optimizer == "ok":
            since = since if quiet else time.perf_counter()
            quiet += 1
        else:
            quiet = 0
        if quiet >= settle:
            return since - started
        time.sleep(0.5)
    raise TimeoutError(f"{collection} still optimizing after {timeout} s")


def collection_facts(qdrant, collection):
    """What Qdrant itself says about a collection: counts, segments and the effective config."""
    info = qdrant.get_collection(collection)
    status, config = rest("GET", f"/collections/{collection}")[1]["result"], info.config
    return {
        "points": info.points_count,
        "indexedVectors": info.indexed_vectors_count,
        "segments": info.segments_count,
        "indexingThresholdKb": config.optimizer_config.indexing_threshold,
        "defaultSegmentNumber": config.optimizer_config.default_segment_number,
        "maxSegmentSizeKb": config.optimizer_config.max_segment_size,
        "hnsw": {"m": config.hnsw_config.m, "efConstruct": config.hnsw_config.ef_construct,
                 "fullScanThresholdKb": config.hnsw_config.full_scan_threshold,
                 "payloadM": config.hnsw_config.payload_m},
        "walCapacityMb": config.wal_config.wal_capacity_mb if config.wal_config else None,
        "strictMode": status["config"].get("strict_mode_config"),
        "payloadSchema": {name: field.get("data_type") for name, field in status.get("payload_schema", {}).items()},
    }


def cold_start(server, ready_query, repeats=3):
    """Medians over `repeats` stops: from the process start to gRPC accepting, /readyz, the
    first answered query (the collection loaded), and that query's own time."""
    runs = []
    for _ in range(repeats):
        server.stop()
        server.evict()
        timing = server.start()
        qdrant = client()
        first = None
        while first is None:
            try:
                began = time.perf_counter()
                ready_query(qdrant)
                first = time.perf_counter()
            except Exception:
                if time.perf_counter() - server.started > 300:
                    raise
                time.sleep(0.005)
        timing["firstQueryDoneS"] = first - server.started
        timing["firstQueryMs"] = (first - began) * 1000
        timing["rssAfterFirstQuery"] = server.memory()
        qdrant.close()
        runs.append(timing)
    keys = ("grpcAcceptsS", "readyS", "firstQueryDoneS", "firstQueryMs")
    return {**{key: round(statistics.median(run[key] for run in runs), 4) for key in keys},
            "runs": runs}


def timed_ms(function, *args, **kwargs):
    began = time.perf_counter()
    result = function(*args, **kwargs)
    return (time.perf_counter() - began) * 1000, result


def latency(values):
    values = sorted(values)
    if not values:
        return None
    return {"n": len(values), "p50": round(statistics.median(values), 3),
            "p95": round(values[min(len(values) - 1, int(0.95 * len(values)))], 3),
            "mean": round(statistics.fmean(values), 3), "max": round(values[-1], 3)}


@contextlib.contextmanager
def one_thread():
    """numpy's BLAS on one thread: the photo service's Go scan is one goroutine per search."""
    from threadpoolctl import threadpool_limits

    with threadpool_limits(1):
        yield


def recall(found, exact, allowed, k):
    """Share of the true top k among the first k found. A hit is any allowed point whose exact score
    reaches the k-th best exact score less TIE, so tied neighbours count either way."""
    candidates = exact[allowed] if allowed is not None else exact
    k = min(k, len(candidates))
    if k == 0:
        return None
    kth = np.partition(candidates, len(candidates) - k)[len(candidates) - k]
    hits = sum(1 for index in found[:k] if (allowed is None or allowed[index]) and exact[index] >= kth - TIE)
    return hits / k


def stable_sample(items, count, salt=""):
    """The same `count` items on every run: ordered by a hash of the item, not by a random generator."""
    return sorted(items, key=lambda item: hashlib.sha256(f"{salt}{item}".encode()).digest())[:count]


def image_vectors(sets=None):
    """(keys, float32 matrix) of the cached whole-photo vectors, one row per distinct vector.

    COCO-CN reuses COCO 2014 photos that may also sit in COCO 2017 under another
    key; such exact duplicates would make ties, so only the first key stays.
    """
    vectors = load_vectors(IMAGE_VECTORS, "images")
    keys = sorted(key for key in vectors if sets is None or key.split(":", 1)[0] in sets)
    seen, kept = set(), []
    for key in keys:
        digest = hashlib.sha256(np.asarray(vectors[key], dtype=np.float32).tobytes()).digest()
        if digest not in seen:
            seen.add(digest)
            kept.append(key)
    return kept, np.stack([vectors[key] for key in kept]).astype(np.float32)


def caption_queries(count):
    """(texts, matrix) of COCO-CN captions sent through the retrieval prompt, as the photo search encodes them."""
    texts = load_vectors(IMAGE_VECTORS, "texts")
    captions = set()
    for split in ("val", "test"):
        _images, found = datasets.coco_cn(split)
        for entry in found.values():
            captions.update(QUERY_PROMPT.format(caption) for caption in entry["written"] + entry["translated"])
    chosen = stable_sample(sorted(captions & set(texts)), count, "captions")
    return chosen, np.stack([texts[text] for text in chosen]).astype(np.float32)


def calibration(path=None):
    """internal/photos/objects/v1.calibration.json in eval.hybrid_search's form."""
    from eval.hybrid_search import CALIBRATION

    with open(path or CALIBRATION, encoding="utf-8") as file:
        data = json.load(file)
    words = {frozenset(int(cls) for cls in key.split(",")): {"fusion": value["fusion"],
                                                             "tierScore": value.get("tierScore")}
             for key, value in data["words"].items()}
    return {"weight": data["weight"], "tierCoverage": data["tierCoverage"], "fusion": set(data["fusion"]),
            "words": words, "model": data["model"]}


class Report:
    """A JSON report rewritten after every result, so a long run shows its interim state."""

    def __init__(self, name):
        os.makedirs(RUNS, exist_ok=True)
        stamp = time.strftime("%Y%m%d-%H%M%S")
        self.path = os.path.join(RUNS, f"{name}-{stamp}.json")
        self.data = {"started": stamp, "host": host()}

    def put(self, key, value):
        self.data[key] = value
        self.flush()

    def flush(self):
        partial = self.path + ".partial"
        with open(partial, "w", encoding="utf-8") as file:
            json.dump(self.data, file, ensure_ascii=False, indent=1, default=str)
        os.replace(partial, self.path)
        print(f"report: {self.path}", flush=True)


def round_trip_ms(qdrant, collection, count=200):
    """Latency of a query that scores nothing (a filter on a missing id): the client and gRPC floor."""
    from qdrant_client import models

    nothing = models.Filter(must=[models.HasIdCondition(has_id=[2**40])])
    vectors = qdrant.get_collection(collection).config.params.vectors
    using, params = (None, vectors) if hasattr(vectors, "size") else next(iter(vectors.items()))
    vector = [0.0] * params.size
    query = [vector] if params.multivector_config else vector
    took = [timed_ms(qdrant.query_points, collection, query=query, using=using, query_filter=nothing, limit=1)[0]
            for _ in range(count)]
    return latency(took)


def host():
    from importlib.metadata import version as package_version

    model = ""
    with open("/proc/cpuinfo", encoding="ascii", errors="replace") as file:
        for line in file:
            if line.startswith("model name"):
                model = line.split(":", 1)[1].strip()
                break
    binary = subprocess.run([BINARY, "--version"], capture_output=True, text=True, check=False).stdout.strip()
    return {"cpu": model, "cpus": len(os.sched_getaffinity(0)), "qdrant": binary,
            "qdrantClient": package_version("qdrant-client"), "grpcio": package_version("grpcio"),
            "numpy": np.__version__}
