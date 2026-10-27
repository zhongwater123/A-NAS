"""Embeds the docs chunks and the 42 questions with EmbeddingGemma 2 through MediaPipe.

Development only (docs/research/qdrant-retrieval-features.md, experiment 3).
The product runtime and model file (the .litertlm the AI Worker loads), with
the text study's prompts: "title: none | text: " before a chunk, the photo
search's retrieval prompt before a question. Vectors are cached under
$ANAS_DATASETS/cache/<model>-docs-<commit>/ and the run resumes.

The text encoder takes at most 1,024 tokens and refuses a longer input (it
does not cut it), while the chunker allows 1,800 characters. A chunk that does
not fit is embedded by its longest prefix that does, found by bisection on
characters, and the report lists those chunks with the characters kept; the
text study's baseline cut its inputs too, at 512 tokens.

    python -m eval.qdrant_embed_docs --model /models/embeddinggemma-2-740m.litertlm \\
        --docs /qe/docs-6f25568/docs [--weight-cache /xnnpack]
"""

import argparse
import json
import os
import time

import numpy as np

from anas_ai import providers
from eval import datasets, qdrant_docs
from eval.embed import MANIFEST, Cache

DOCUMENT_PROMPT = "title: none | text: "
TOO_LONG = "exceeds maximum supported signature length"


def embed_fitting(provider, text):
    """(vector, characters kept): the whole text, or its longest prefix the encoder accepts."""
    try:
        return provider.embed_text(text), len(text)
    except ValueError as error:
        if TOO_LONG not in str(error):
            raise
    low, high, best = 1, len(text) - 1, None
    while low <= high:
        middle = (low + high) // 2
        try:
            best = (provider.embed_text(text[:middle]), middle)
            low = middle + 1
        except ValueError as error:
            if TOO_LONG not in str(error):
                raise
            high = middle - 1
    return best


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--model", required=True)
    parser.add_argument("--docs", required=True, help="docs/ at the corpus commit")
    parser.add_argument("--weight-cache", default=os.path.join(datasets.ROOT, "cache", "xnnpack"))
    args = parser.parse_args()
    started = time.monotonic()
    provider = providers.MediaPipeProvider(args.model, MANIFEST, cache_dir=args.weight_cache)
    print(f"model {provider.model} loaded in {time.monotonic() - started:.1f} s", flush=True)
    directory = os.path.join(datasets.ROOT, "cache", f"{provider.model}-docs-{qdrant_docs.CORPUS_COMMIT}")
    os.makedirs(directory, exist_ok=True)
    chunks = qdrant_docs.chunk_markdown(args.docs)
    queries = qdrant_docs.load_queries()
    qdrant_docs.check_targets(chunks, queries)

    questions = Cache(os.path.join(directory, "queries.npz"))
    for query in queries:
        if query["q"] not in questions.vectors:
            questions.vectors[query["q"]] = np.asarray(provider.embed_query(query["q"]), dtype=np.float32)
    questions.save()

    cache = Cache(os.path.join(directory, "chunks.npz"))
    todo = [chunk for chunk in chunks if chunk.id not in cache.vectors]
    print(f"{len(chunks)} chunks, {len(todo)} to embed", flush=True)
    began, took, cut = time.monotonic(), [], {}
    for done, chunk in enumerate(todo, 1):
        one = time.monotonic()
        text = DOCUMENT_PROMPT + chunk.text
        vector, kept = embed_fitting(provider, text)
        cache.vectors[chunk.id] = np.asarray(vector, dtype=np.float32)
        if kept < len(text):
            cut[chunk.id] = {"chars": len(text), "kept": kept}
        took.append(time.monotonic() - one)
        if done % 100 == 0 or done == len(todo):
            cache.save()
            print(f"{done}/{len(todo)} in {time.monotonic() - began:.0f} s", flush=True)
    about = {"model": provider.model, "chunks": len(chunks), "documentPrompt": DOCUMENT_PROMPT,
             "queryPrompt": provider.QUERY_PROMPT, "commit": qdrant_docs.CORPUS_COMMIT,
             "secondsPerChunk": round(float(np.median(took)), 3) if took else None,
             "cutToFit": cut}
    with open(os.path.join(directory, "about.json"), "w", encoding="utf-8") as file:
        json.dump(about, file, ensure_ascii=False, indent=1)
    print(json.dumps(about, ensure_ascii=False, indent=1), flush=True)
    provider.close()


if __name__ == "__main__":
    main()
