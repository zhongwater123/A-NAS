"""Which model requests after OFFSET in the gateway log were Hermes's background review, what
they cost (tokens, and how many of the prompt tokens the provider served from its cache), and
every tool call the review made (#114, review.sh).

  review_report.py GATEWAY_LOG OFFSET
"""
import json
import sys

MARK = "Review the conversation above"
records = [json.loads(line) for line in open(sys.argv[1], encoding="utf-8").readlines()[int(sys.argv[2]):]]
kinds, reviews = {}, []
for r in records:
    review = MARK in json.dumps(r["messages"], ensure_ascii=False)
    kind = "review" if review else "turn" if r["tools"] else "title"
    usage = r.get("usage") or {}
    k = kinds.setdefault(kind, [0, 0, 0.0, 0])
    k[0] += 1
    k[1] += usage.get("total_tokens", 0)
    k[2] += r.get("seconds", 0)
    # DeepSeek reports prompt_cache_hit_tokens; OpenAI-style APIs prompt_tokens_details.cached_tokens.
    k[3] += usage.get("prompt_cache_hit_tokens") or (usage.get("prompt_tokens_details") or {}).get("cached_tokens") or 0
    if review:
        reviews.append(r)
for kind, (n, tokens, seconds, cached) in kinds.items():
    print(f"{kind}: {n} requests, {tokens} tokens ({cached} prompt tokens from the cache), {seconds:.0f} s of model time")
if reviews:
    print("tools offered to the review:", reviews[-1]["tools"])
seen = set()
for r in reviews:
    msgs = r["messages"]
    start = max(i for i, m in enumerate(msgs) if m.get("role") == "user" and MARK in str(m.get("content")))
    prompt = str(msgs[start]["content"])[:60].replace("\n", " ")
    if ("prompt", start, prompt) not in seen:
        seen.add(("prompt", start, prompt))
        print(f"review prompt: {prompt}…")
    for m in msgs[start + 1:]:
        for call in m.get("tool_calls") or []:
            if call["id"] not in seen:
                seen.add(call["id"])
                print("  review →", call["function"]["name"], call["function"].get("arguments", "")[:400].replace("\n", "⏎"))
        if m.get("role") == "tool" and ("result", m.get("tool_call_id")) not in seen:
            seen.add(("result", m.get("tool_call_id")))
            print("    ←", str(m.get("content"))[:200].replace("\n", "⏎"))
        if m.get("role") == "assistant" and m.get("content") and ("text", m["content"]) not in seen:
            seen.add(("text", m["content"]))
            print("  review says:", str(m["content"])[:200].replace("\n", "⏎"))
