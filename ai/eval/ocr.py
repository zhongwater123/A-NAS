"""Runs OCR vision-language models over CC-OCR subsets through llama-server.

Development only: the OCR model evaluation for the photo library
(docs/research/photo-ocr-model-evaluation.md). Each model runs as llama.cpp's
llama-server on the CPU, one image per request, with the prompt its authors
publish for the task and greedy decoding. Answers are cached as JSON lines per
model and subset, so a run can stop and resume; eval.ocr_score scores them.

Images are re-encoded with the long side capped at 1600 px, the size the
photo service already renders for the object detector, so the models see what
they would see on the NAS.

    python -m eval.ocr --models glm-ocr,qwen3.5-2b --per-subset 20 --threads 8
    python -m eval.ocr --bench 12 --threads 4    # speed and memory, one model at a time

Paths inside the evaluation container: /models (GGUF files), /data (CC-OCR),
/runs (answers), /llama (llama.cpp build/bin).
"""

import argparse
import base64
import csv
import hashlib
import io
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request

from PIL import Image, ImageOps

MODELS_DIR = os.environ.get("ANAS_OCR_MODELS", "/models")
DATA_DIR = os.environ.get("ANAS_OCR_DATA", "/data")
RUNS_DIR = os.environ.get("ANAS_OCR_RUNS", "/runs")
LLAMA_DIR = os.environ.get("ANAS_OCR_LLAMA", "/llama")
LONG_SIDE = 1600
# Visual tokens per image. Without a cap GLM-OCR makes ~2,460 tokens of a 1600x1200 photo
# and needs ~700 s on the development machine; at 1024 it needs ~110 s (2026-10-15).
IMAGE_TOKENS = 1024
PORT = 18301

# kind: "text" (plain transcription), "doc" (photographed document pages), "kie" (fields into a JSON schema)
SUBSETS = {
    "cold_sibr": ("kie/open_category/COLD_SIBR_400.tsv", "kie"),
    "cold_cell": ("kie/open_category/COLD_CELL_600.tsv", "kie"),
    "zh_doc": ("multi_scene_ocr/document_text/zh_doc_100.tsv", "text"),
    "doc_photo_chn": ("doc_parsing/doc/doc_photo_chn_75.tsv", "doc"),
}
# Dense full pages are the slowest items and the furthest from cards, receipts and bank slips.
LIMITS = {"doc_photo_chn": 10}
MAX_TOKENS = {"text": 2048, "doc": 4096, "kie": 2048}

CC_OCR_TEXT = "Please output only the text content from the image without any additional descriptions or formatting."
HUNYUAN_DOC = "提取文档图片中正文的所有信息用markdown格式表示，其中页眉、页脚部分忽略，表格用html格式表达，文档中公式用latex格式表示，按照阅读顺序组织进行解析。"
SCHEMA_MARK = "The input json schema content is as follows:"


def schema_of(question):
    return json.loads(question.split(SCHEMA_MARK, 1)[1].strip())


def glm_kie(item):
    # The model card's information extraction example.
    return "请按下列JSON格式输出图中信息:\n" + json.dumps(schema_of(item["question"]), ensure_ascii=False, indent=4)


def hunyuan_kie(item):
    # README "Application-oriented Prompts": 提取图片中的: ['key1','key2', ...] 的字段内容，并按照JSON格式返回。
    keys = ", ".join("'%s'" % key for key in schema_of(item["question"]))
    return f"提取图片中的: [{keys}] 的字段内容，并按照JSON格式返回。"


# Prompts are the ones each model's authors publish; Qwen3.5 is a general model and gets CC-OCR's own.
MODELS = {
    "paddleocr-vl-1.6": {
        "model": "paddleocr-vl-1.6/PaddleOCR-VL-1.6-Q8_0.gguf",
        "mmproj": "paddleocr-vl-1.6/PaddleOCR-VL-1.6-GGUF-mmproj.gguf",
        "prompts": {"text": "OCR:", "doc": "OCR:"},
    },
    "glm-ocr": {
        "model": "glm-ocr/GLM-OCR-Q8_0.gguf",
        "mmproj": "glm-ocr/mmproj-GLM-OCR-Q8_0.gguf",
        "prompts": {"text": "Text Recognition:", "doc": "Text Recognition:", "kie": glm_kie},
    },
    "hunyuanocr-1.5": {
        "model": "hunyuanocr-1.5/hyocr-Q8_0.gguf",
        "mmproj": "hunyuanocr-1.5/mmproj-hyocr-f16.gguf",
        "prompts": {"text": "提取图中的文字。", "doc": HUNYUAN_DOC, "kie": hunyuan_kie},
        # inference/vLLM client: temperature 0, repetition_penalty 1.08
        "sampling": {"repeat_penalty": 1.08},
    },
    "qwen3.5-2b": {
        "model": "qwen3.5-2b/Qwen3.5-2B-Q8_0.gguf",
        "mmproj": "qwen3.5-2b/mmproj-Qwen3.5-2B-f16.gguf",
        "prompts": {"text": CC_OCR_TEXT, "doc": CC_OCR_TEXT, "kie": lambda item: item["question"]},
        "sampling": {"chat_template_kwargs": {"enable_thinking": False}},
    },
}

CJK = re.compile(r"[一-鿿]")


def load_subset(name):
    """Items in a fixed order independent of the file: sorted by a hash of the image name.

    KIE subsets mix Chinese and English documents; only those whose answer has
    Chinese are kept, since the photo library's documents are Chinese.
    """
    path, kind = SUBSETS[name]
    csv.field_size_limit(1 << 30)
    items = []
    with open(os.path.join(DATA_DIR, path), encoding="utf-8", newline="") as file:
        for row in csv.DictReader(file, delimiter="\t"):
            if kind == "kie" and not CJK.search(row["answer"]):
                continue
            items.append({
                "subset": name, "kind": kind, "index": row["index"], "image_name": row["image_name"],
                "image": row["image"], "question": row["question"], "answer": row["answer"],
            })
    items.sort(key=lambda item: hashlib.sha1(item["image_name"].encode()).hexdigest())
    return items


def image_url(encoded):
    image = Image.open(io.BytesIO(base64.b64decode(encoded)))
    image = ImageOps.exif_transpose(image).convert("RGB")
    scale = LONG_SIDE / max(image.size)
    if scale < 1:
        image = image.resize((round(image.width * scale), round(image.height * scale)), Image.LANCZOS)
    buffer = io.BytesIO()
    image.save(buffer, "JPEG", quality=92)
    return "data:image/jpeg;base64," + base64.b64encode(buffer.getvalue()).decode(), image.size


def prompt_for(model, item):
    prompt = MODELS[model]["prompts"].get(item["kind"])
    return prompt(item) if callable(prompt) else prompt


class Server:
    """One llama-server process for one model. Its log goes to a file; peak RSS is sampled after each answer."""

    def __init__(self, model, threads, image_tokens=IMAGE_TOKENS, log_path=os.devnull, context=16384):
        spec = MODELS[model]
        self.args = [
            os.path.join(LLAMA_DIR, "llama-server"),
            "-m", os.path.join(MODELS_DIR, spec["model"]),
            "--mmproj", os.path.join(MODELS_DIR, spec["mmproj"]),
            "--host", "127.0.0.1", "--port", str(PORT),
            "-c", str(context), "-np", "1", "-t", str(threads), "-tb", str(threads),
            "--jinja", "--no-webui",
            # Each request is one image and nothing is reused. llama-server otherwise keeps up to
            # 8 GiB of past prompts in RAM (--cache-ram), which got it OOM-killed at 5.5 GB (2026-10-15).
            "--cache-ram", "0", "--ctx-checkpoints", "0",
        ]
        if image_tokens:
            self.args += ["--image-max-tokens", str(image_tokens)]
        self.log_path = log_path
        self.process = None
        self.log = None
        self.load_seconds = None
        self.peak_mib = 0
        self.restarts = 0

    def __enter__(self):
        self.start()
        return self

    def __exit__(self, *exc):
        self.stop()

    def start(self):
        began = time.time()
        self.log = open(self.log_path, "ab")
        self.process = subprocess.Popen(self.args, stdout=self.log, stderr=subprocess.STDOUT)
        while True:
            if self.process.poll() is not None:
                raise RuntimeError(f"llama-server exited ({self.process.returncode}): {self.log_tail()}")
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{PORT}/health", timeout=5) as response:
                    if response.status == 200:
                        break
            except (urllib.error.URLError, ConnectionError, TimeoutError):
                pass
            time.sleep(0.5)
        self.load_seconds = time.time() - began

    def stop(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(30)
            except subprocess.TimeoutExpired:
                self.process.kill()
        self.log.close()

    def restart(self):
        self.stop()
        self.restarts += 1
        self.start()

    def alive(self):
        return self.process.poll() is None

    def sample_rss(self):
        try:
            with open(f"/proc/{self.process.pid}/status") as status:
                for line in status:
                    if line.startswith("VmHWM:"):
                        self.peak_mib = max(self.peak_mib, int(line.split()[1]) / 1024)
        except OSError:
            pass

    def log_tail(self, size=1500):
        try:
            with open(self.log_path, "rb") as file:
                file.seek(max(0, os.path.getsize(self.log_path) - size))
                return file.read().decode(errors="replace")
        except OSError:
            return ""

    def ask(self, url, prompt, max_tokens, sampling):
        """Streams one answer; stops early when greedy decoding falls into a loop."""
        body = {
            "messages": [{"role": "user", "content": [
                {"type": "image_url", "image_url": {"url": url}},
                {"type": "text", "text": prompt},
            ]}],
            "temperature": 0, "max_tokens": max_tokens, "logprobs": True, "cache_prompt": False,
            "stream": True, "stream_options": {"include_usage": True},
        }
        body.update(sampling)
        request = urllib.request.Request(
            f"http://127.0.0.1:{PORT}/v1/chat/completions",
            data=json.dumps(body).encode(), headers={"Content-Type": "application/json"},
        )
        text, tokens, finish, timings, first = [], [], None, None, None
        began = time.time()
        with urllib.request.urlopen(request, timeout=1800) as response:
            for line in response:
                if not line.startswith(b"data: ") or line.strip() == b"data: [DONE]":
                    continue
                event = json.loads(line[6:])
                timings = event.get("timings") or timings
                for choice in event.get("choices") or []:
                    piece = (choice.get("delta") or {}).get("content")
                    if piece:
                        if first is None:
                            first = time.time() - began
                        text.append(piece)
                    for token in (choice.get("logprobs") or {}).get("content") or []:
                        tokens.append([token["token"], round(token["logprob"], 4)])
                    finish = choice.get("finish_reason") or finish
                if len(text) >= 400 and len(text) % 50 == 0 and looping("".join(text)):
                    finish = "repetition"
                    break  # closing the connection makes llama-server stop generating
        return {"output": "".join(text), "tokens": tokens, "finish": finish, "timings": timings,
                "first_token_seconds": round(first, 3) if first is not None else None}


def looping(text, repeats=8, longest=300):
    """Whether the tail of text is one unit repeated `repeats` times (greedy decoding's failure mode)."""
    tail = text[-repeats * longest:]
    for size in range(1, min(longest, len(tail) // repeats) + 1):
        unit = tail[-size:]
        if unit.strip() and tail.endswith(unit * repeats):
            return True
    return False


def answer_record(model, item, prompt, size, reply, seconds):
    return {
        "model": model, "subset": item["subset"], "index": item["index"], "image_name": item["image_name"],
        "input_size": size, "prompt": prompt, "seconds": round(seconds, 3), **reply,
    }


def done_indices(path):
    """Items answered, or that crashed llama-server (not retried); other errors are tried again."""
    if not os.path.exists(path):
        return set()
    with open(path, encoding="utf-8") as file:
        records = [json.loads(line) for line in file if line.strip()]
    return {record["index"] for record in records if "error" not in record or record.get("crash")}


def run_name(model, image_tokens):
    # Answers at another image token cap go to their own directory.
    return model if image_tokens == IMAGE_TOKENS else f"{model}-img{image_tokens}"


def run(models, subsets, per_subset, threads, image_tokens):
    for model in models:
        name = run_name(model, image_tokens)
        todo = []
        for subset in subsets:
            if prompt_for_kind(model, SUBSETS[subset][1]) is None:
                continue
            path = os.path.join(RUNS_DIR, name, subset + ".jsonl")
            done = done_indices(path)
            limit = min(per_subset, LIMITS.get(subset, per_subset))
            todo += [(path, item) for item in load_subset(subset)[:limit] if item["index"] not in done]
        if not todo:
            print(f"{model}: nothing to do", flush=True)
            continue
        os.makedirs(os.path.join(RUNS_DIR, name), exist_ok=True)
        with Server(model, threads, image_tokens, os.path.join(RUNS_DIR, name, "server.log")) as server:
            print(f"{model}: loaded in {server.load_seconds:.1f}s, {len(todo)} items", flush=True)
            for count, (path, item) in enumerate(todo, 1):
                record = answer(server, model, item)
                with open(path, "a", encoding="utf-8") as file:
                    file.write(json.dumps(record, ensure_ascii=False) + "\n")
                print(f"{model} {count}/{len(todo)} {item['subset']} {record.get('seconds', 0):.1f}s"
                      f" {record.get('finish', record.get('error', '')[:200])}", flush=True)
        with open(os.path.join(RUNS_DIR, name, "servers.jsonl"), "a", encoding="utf-8") as file:
            file.write(json.dumps({"time": time.strftime("%Y-%m-%dT%H:%M:%S"), "threads": threads,
                                   "image_tokens": image_tokens, "load_seconds": round(server.load_seconds, 1),
                                   "peak_rss_mib": round(server.peak_mib), "restarts": server.restarts,
                                   "items": len(todo)}) + "\n")


def answer(server, model, item):
    """ask_item, plus: an item during which llama-server died is recorded as a crash and the server restarted."""
    record = ask_item(server, model, item)
    server.sample_rss()
    if "error" in record and not server.alive():
        record["error"] = f"llama-server exited ({server.process.returncode}): " + server.log_tail(1200)
        record["crash"] = True
        server.restart()
    return record


def prompt_for_kind(model, kind):
    return MODELS[model]["prompts"].get(kind)


def ask_item(server, model, item):
    prompt = prompt_for(model, item)
    url, size = image_url(item["image"])
    began = time.time()
    try:
        reply = server.ask(url, prompt, MAX_TOKENS[item["kind"]], MODELS[model].get("sampling", {}))
    except Exception as error:  # noqa: BLE001 - recorded with the item and retried on the next run
        return {"model": model, "subset": item["subset"], "index": item["index"], "error": repr(error)[:500],
                "seconds": round(time.time() - began, 3)}
    return answer_record(model, item, prompt, size, reply, time.time() - began)


def bench(models, count, threads, image_tokens):
    """Times `count` items per model (a third from each kind) on `threads` threads, with load time and peak RSS."""
    picks = []
    per_kind = max(1, count // 3)
    for subset in ("zh_doc", "doc_photo_chn", "cold_sibr"):
        picks += load_subset(subset)[:per_kind]
    out = os.path.join(RUNS_DIR, f"bench-t{threads}-img{image_tokens}")
    os.makedirs(out, exist_ok=True)
    for model in models:
        with Server(model, threads, image_tokens, os.path.join(out, model + ".server.log")) as server:
            records = [answer(server, model, item) for item in picks if prompt_for_kind(model, item["kind"])]
            summary = {"model": model, "threads": threads, "image_tokens": image_tokens,
                       "load_seconds": round(server.load_seconds, 1), "peak_rss_mib": round(server.peak_mib),
                       "restarts": server.restarts, "items": len(records)}
        with open(os.path.join(out, model + ".jsonl"), "w", encoding="utf-8") as file:
            file.write(json.dumps(summary) + "\n")
            for record in records:
                file.write(json.dumps(record, ensure_ascii=False) + "\n")
        print(json.dumps(summary), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--models", default=",".join(MODELS))
    parser.add_argument("--subsets", default=",".join(SUBSETS))
    parser.add_argument("--per-subset", type=int, default=10)
    parser.add_argument("--threads", type=int, default=8)
    parser.add_argument("--image-tokens", type=int, default=IMAGE_TOKENS, help="visual tokens per image at most")
    parser.add_argument("--bench", type=int, default=0, help="time this many items per model instead")
    args = parser.parse_args()
    models = [model for model in args.models.split(",") if model]
    unknown = [model for model in models if model not in MODELS]
    if unknown:
        sys.exit(f"unknown models: {unknown}")
    if args.bench:
        bench(models, args.bench, args.threads, args.image_tokens)
    else:
        run(models, [subset for subset in args.subsets.split(",") if subset], args.per_subset, args.threads,
            args.image_tokens)


if __name__ == "__main__":
    main()
