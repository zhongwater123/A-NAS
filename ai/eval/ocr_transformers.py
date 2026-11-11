"""Answers the eval.ocr items with a model's official transformers pipeline.

Development only (docs/research/photo-ocr-model-evaluation.md): it separates what
the model reads from what our llama.cpp setup (Q8_0, 1600 px, 1024 visual
tokens) lets it read. As Xiaomi-OCR-0's repository does (evaluation_pipeline/
cropping.py and the model card's Transformers example): the image fitted into
2048 x 2048, the model's own processor, float32 on the CPU, greedy decoding,
non-thinking, the card's prompts. Answers go to /runs/<model>-hf in eval.ocr's
format, so eval.ocr_score scores them; token log-probabilities are those greedy
decoding chose.

    python -m eval.ocr_transformers --model xiaomi-ocr-0 --weights /hf/xiaomi-ocr-0/hf --threads 8
"""

import argparse
import base64
import io
import json
import os
import resource
import time

import torch
from PIL import Image
from transformers import AutoModelForImageTextToText, AutoProcessor, LogitsProcessor, LogitsProcessorList

from eval.ocr import LIMITS, RUNS_DIR, answer_record, done_indices, load_subset, prompt_for

# Xiaomi-OCR-0's evaluation_pipeline/cropping.py: MAX_IMAGE_EDGE, thumbnail with LANCZOS.
MAX_IMAGE_EDGE = 2048
# The model card's Transformers example and the repository's MCP service default.
MAX_NEW_TOKENS = 4096


class Chosen(LogitsProcessor):
    """Records each step's time and the log-probability of the token greedy decoding takes.

    It runs after the default processors, so the argmax of the scores it sees is the token chosen.
    """

    def __init__(self):
        self.first = None
        self.logprobs = []

    def __call__(self, input_ids, scores):
        if self.first is None:
            self.first = time.time()
        logprobs = torch.log_softmax(scores.float(), dim=-1)
        self.logprobs.append(round(float(logprobs[0].max()), 4))
        return scores


def fitted(encoded):
    image = Image.open(io.BytesIO(base64.b64decode(encoded))).convert("RGB")
    if image.width > MAX_IMAGE_EDGE or image.height > MAX_IMAGE_EDGE:
        image.thumbnail((MAX_IMAGE_EDGE, MAX_IMAGE_EDGE), Image.Resampling.LANCZOS)
    return image


def pieces(tokenizer, ids):
    """Each token's share of the decoded text; a character split across tokens goes to its last token."""
    out, before = [], ""
    for count in range(1, len(ids) + 1):
        text = tokenizer.decode(ids[:count], skip_special_tokens=True, clean_up_tokenization_spaces=False)
        if text.endswith("�"):
            out.append("")
            continue
        out.append(text[len(before):])
        before = text
    return out


def ask(model, processor, image, prompt):
    messages = [{"role": "user", "content": [{"type": "image", "image": image}, {"type": "text", "text": prompt}]}]
    inputs = processor.apply_chat_template(
        messages, tokenize=True, add_generation_prompt=True, return_dict=True, return_tensors="pt",
    )
    chosen = Chosen()
    began = time.time()
    with torch.inference_mode():
        output = model.generate(**inputs, max_new_tokens=MAX_NEW_TOKENS, do_sample=False,
                                logits_processor=LogitsProcessorList([chosen]))
    ended = time.time()
    new = output[0, inputs["input_ids"].shape[1]:].tolist()
    eos = set(model.generation_config.eos_token_id if isinstance(model.generation_config.eos_token_id, list)
              else [model.generation_config.eos_token_id])
    finish = "stop" if new and new[-1] in eos else "length"
    text = processor.batch_decode([new], skip_special_tokens=True)[0]
    first = (chosen.first or ended) - began
    # One processor call per generated token, so the two lists are the same length.
    tokens = [list(pair) for pair in zip(pieces(processor.tokenizer, new), chosen.logprobs, strict=True)]
    return {
        "output": text, "tokens": tokens,
        "finish": finish, "first_token_seconds": round(first, 3),
        "timings": {"prompt_n": inputs["input_ids"].shape[1], "prompt_ms": round(first * 1000),
                    "predicted_n": len(new), "predicted_ms": round((ended - began - first) * 1000)},
        "peak_rss_mib": round(resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1024),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--model", default="xiaomi-ocr-0", help="an eval.ocr model, for its prompts")
    parser.add_argument("--weights", required=True, help="the official checkpoint's directory")
    parser.add_argument("--subsets", default="cold_sibr,cold_cell,zh_doc,doc_photo_chn")
    parser.add_argument("--per-subset", type=int, default=25)
    parser.add_argument("--threads", type=int, default=8)
    parser.add_argument("--limit", type=int, default=0, help="answer at most this many items, for a trial")
    args = parser.parse_args()
    torch.set_num_threads(args.threads)
    name = args.model + "-hf"
    os.makedirs(os.path.join(RUNS_DIR, name), exist_ok=True)
    todo = []
    for subset in [s for s in args.subsets.split(",") if s]:
        path = os.path.join(RUNS_DIR, name, subset + ".jsonl")
        done = done_indices(path)
        limit = min(args.per_subset, LIMITS.get(subset, args.per_subset))
        todo += [(path, item) for item in load_subset(subset)[:limit] if item["index"] not in done]
    if args.limit:
        todo = todo[:args.limit]
    began = time.time()
    processor = AutoProcessor.from_pretrained(args.weights)
    model = AutoModelForImageTextToText.from_pretrained(args.weights, dtype=torch.float32).eval()
    print(f"{name}: loaded in {time.time() - began:.1f}s, {len(todo)} items", flush=True)
    for count, (path, item) in enumerate(todo, 1):
        prompt = prompt_for(args.model, item)
        image = fitted(item["image"])
        started = time.time()
        reply = ask(model, processor, image, prompt)
        record = answer_record(name, item, prompt, list(image.size), reply, time.time() - started)
        with open(path, "a", encoding="utf-8") as file:
            file.write(json.dumps(record, ensure_ascii=False) + "\n")
        print(f"{name} {count}/{len(todo)} {item['subset']} {record['seconds']:.1f}s {record['finish']}"
              f" in {reply['timings']['prompt_n']} out {reply['timings']['predicted_n']}"
              f" peak {reply['peak_rss_mib']} MiB", flush=True)


if __name__ == "__main__":
    main()
