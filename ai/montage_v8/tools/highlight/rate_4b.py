"""The local Qwen3.5-4B marks each beat on the user's four levels, from its key frame alone: no description, no
other field. The answer is one digit (3 精彩, 2 可用, 1 平淡, 0 有问题), held to those by a grammar; the score
is the digit's probability-weighted mean (G-Eval, the way the 1-5 highlight score was read before
2026-11-13), so beats can be ranked.

Writes one line a beat to OUT (JSONL); beats already there are skipped, so a stopped run goes on.

  python3 -m montage_v8.tools.highlight.rate_4b http://127.0.0.1:8094 OUT.jsonl [--workers 2]
"""
import argparse
import json
import math
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import requests

from montage_v8.adapters.vision import image_part
from montage_v8.tools import usable_cards

CASE = "pj"
DIGITS = {"3": "3", "2": "2", "1": "1", "0": "x"}     # the answer's digit -> the user's mark
SYSTEM = "你是旅行短片的剪辑师，判断素材画面放进成片的价值。只根据画面回答。"
# The user's own four levels, as the labelling page wrote them.
ASK = ("这张图是一段旅行视频里一个镜头的关键帧。按下面四档判断这个镜头，只回答一个数字：\n"
       "3 精彩：值得放在开场或副歌\n"
       "2 可用：好看，但不突出\n"
       "1 平淡：交代、过渡或普通记录\n"
       "0 有问题：遮挡、模糊、抖动等，不该用")
GRAMMAR = "root ::= [0-3]"
TOP = 10


def beats():
    for card in usable_cards(CASE):
        for n, beat in enumerate(card["answer"]["beats"], 1):
            yield f"{card['id']}/{n}", card["frames"][beat["frames"][0] - 1]


def rate(url, beat, frame):
    body = {"messages": [{"role": "system", "content": SYSTEM},
                         {"role": "user", "content": [image_part(frame), {"type": "text", "text": ASK}]}],
            "temperature": 0, "max_tokens": 1, "grammar": GRAMMAR, "logprobs": True, "top_logprobs": TOP,
            "chat_template_kwargs": {"enable_thinking": False}}
    t0 = time.perf_counter()
    resp = requests.post(f"{url}/v1/chat/completions", json=body, timeout=600)
    resp.raise_for_status()
    choice = resp.json()["choices"][0]
    answer = choice["message"]["content"].strip()
    probs = dict.fromkeys(DIGITS, 0.0)
    for alt in choice["logprobs"]["content"][0]["top_logprobs"]:
        token = alt["token"].strip()
        if token in probs:
            probs[token] += math.exp(alt["logprob"])
    total = sum(probs.values()) or 1.0
    shares = {d: p / total for d, p in probs.items()}
    return {"beat": beat, "answer": answer, "mark": DIGITS.get(answer, ""),
            "score": round(sum(int(d) * p for d, p in shares.items()), 4),
            "probs": {d: round(p, 4) for d, p in shares.items()}, "seconds": round(time.perf_counter() - t0, 2),
            "usage": resp.json().get("usage", {})}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("url")
    ap.add_argument("out", type=Path)
    ap.add_argument("--workers", type=int, default=2)
    args = ap.parse_args()
    done = {json.loads(line)["beat"] for line in args.out.read_text(encoding="utf-8").splitlines()} if args.out.exists() else set()
    todo = [(b, f) for b, f in beats() if b not in done]
    print(f"{len(done)} done, {len(todo)} to go", flush=True)
    with args.out.open("a", encoding="utf-8") as out, ThreadPoolExecutor(args.workers) as pool:
        for k, row in enumerate(pool.map(lambda bf: rate(args.url, *bf), todo), 1):
            out.write(json.dumps(row, ensure_ascii=False) + "\n")
            out.flush()
            if k % 50 == 0:
                print(k, flush=True)
    print("DONE", flush=True)


if __name__ == "__main__":
    main()
