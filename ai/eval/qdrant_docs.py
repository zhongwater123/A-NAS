"""The repository docs as a file-chunk corpus, and Hit@k over 42 questions about them.

Development only (docs/research/qdrant-retrieval-features.md, experiment 3).
Standard library only, so the MediaPipe step (eval.qdrant_embed_docs) and the
Qdrant step (eval.qdrant_chunks) chunk alike. The chunker and the questions
(qdrant_docs_queries.json) come from the EmbeddingGemma 2 text study
(ai/finetune/eval_docs.py on the branch behind #152): Markdown split at
headings # to ####, a body under 80 characters joined to the previous chunk
of the same file, one over 1,800 split at blank lines (then lines) into
<id>~1, ~2, ... A question's target is "<path under docs/>#<heading>"; any
piece of that section counts.

The corpus is docs/ at a fixed commit, so this study's own document does not
enter it: git archive 6f25568 docs | tar -x -C ~/qdrant-eval/docs-6f25568
"""

import json
import os
import re
from dataclasses import dataclass

HERE = os.path.dirname(os.path.abspath(__file__))
QUERIES = os.path.join(HERE, "qdrant_docs_queries.json")
CORPUS_COMMIT = "6f25568"
HEADING = re.compile(r"^(#{1,4})\s+(.+?)\s*$")
SKIP = ("README.md", "_TEMPLATE.md")


@dataclass
class Chunk:
    id: str  # <path under docs/>#<heading>[~piece]
    path: str
    heading: str
    text: str  # heading, newline, body


def sections(text, stem):
    """[(heading, body)] of a Markdown file; headings inside code fences do not count."""
    found, heading, lines, in_code = [], stem, [], False
    for line in text.splitlines():
        if line.startswith("```"):
            in_code = not in_code
        match = None if in_code else HEADING.match(line)
        if match:
            body = "\n".join(lines).strip()
            if body:
                found.append((heading, body))
            heading, lines = match.group(2), []
        else:
            lines.append(line)
    body = "\n".join(lines).strip()
    if body:
        found.append((heading, body))
    return found


def pieces(body, max_chars):
    """A long body split at blank lines, then at the last line break before max_chars (or hard)."""
    if len(body) <= max_chars:
        return [body]
    parts, current = [], ""
    for paragraph in re.split(r"\n\s*\n", body):
        if current and len(current) + len(paragraph) > max_chars:
            parts.append(current)
            current = paragraph
        else:
            current = f"{current}\n\n{paragraph}" if current else paragraph
    if current:
        parts.append(current)
    cut_parts = []
    for part in parts:
        while len(part) > max_chars:
            cut = part.rfind("\n", 0, max_chars)
            cut = cut if cut > max_chars // 2 else max_chars
            cut_parts.append(part[:cut])
            part = part[cut:].lstrip("\n")
        cut_parts.append(part)
    return cut_parts


def chunk_markdown(docs_dir, min_chars=80, max_chars=1800):
    chunks = []
    paths = []
    for folder, _dirs, files in os.walk(docs_dir):
        paths.extend(os.path.join(folder, name) for name in files if name.endswith(".md") and name not in SKIP)
    for path in sorted(paths, key=lambda p: os.path.relpath(p, docs_dir).replace(os.sep, "/")):
        relative = os.path.relpath(path, docs_dir).replace(os.sep, "/")
        with open(path, encoding="utf-8") as file:
            text = file.read()
        for heading, body in sections(text, os.path.splitext(os.path.basename(path))[0]):
            if len(body) < min_chars and chunks and chunks[-1].path == relative:
                chunks[-1].text += "\n" + body
                continue
            parts = pieces(body, max_chars)
            for index, part in enumerate(parts):
                suffix = f"~{index + 1}" if len(parts) > 1 else ""
                chunks.append(Chunk(f"{relative}#{heading}{suffix}", relative, heading, f"{heading}\n{part}"))
    return chunks


def load_queries(path=QUERIES):
    with open(path, encoding="utf-8") as file:
        queries = json.load(file)
    for query in queries:
        query["targets"] = query["target"] if isinstance(query["target"], list) else [query["target"]]
    return queries


def check_targets(chunks, queries):
    known = {chunk.id.split("~")[0] for chunk in chunks}
    missing = [target for query in queries for target in query["targets"] if target not in known]
    if missing:
        raise SystemExit(f"targets not in the corpus: {missing}")


def hits(chunks, queries, rankings, files=None):
    """Hit@1/5/10, MRR@10 and file hit@1/5 of ranked chunk indices per query.

    files, if given, are ranked file paths per query (a grouped search);
    otherwise a ranking's files are its chunks' files in first-seen order.
    Per-query ranks come back too, for paired comparisons.
    """
    totals = {"hit@1": 0, "hit@5": 0, "hit@10": 0, "mrr@10": 0.0, "fileHit@1": 0, "fileHit@5": 0}
    ranks = []
    for number, (query, ranked) in enumerate(zip(queries, rankings, strict=True)):
        targets = set(query["targets"])
        rank = next((r for r, index in enumerate(ranked) if chunks[index].id.split("~")[0] in targets), None)
        if rank is not None:
            totals["hit@1"] += rank < 1
            totals["hit@5"] += rank < 5
            totals["hit@10"] += rank < 10
            totals["mrr@10"] += 1 / (rank + 1) if rank < 10 else 0
        if files is None:
            seen = []
            for index in ranked:
                if chunks[index].path not in seen:
                    seen.append(chunks[index].path)
        else:
            seen = files[number]
        target_files = {target.split("#")[0] for target in targets}
        file_rank = next((r for r, path in enumerate(seen[:5]) if path in target_files), None)
        if file_rank is not None:
            totals["fileHit@1"] += file_rank < 1
            totals["fileHit@5"] += file_rank < 5
        ranks.append(rank)
    count = len(queries)
    return {key: round(value / count, 3) for key, value in totals.items()}, ranks
