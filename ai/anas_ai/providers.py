"""Embedding providers: the real model through MediaPipe, and a fake.

Only MediaPipeProvider imports MediaPipe, so the Worker and its tests run
with the Python standard library alone when the fake is used.
"""

import hashlib
import json
import math
import os
import struct


class InvalidInput(Exception):
    """The provider cannot process this input; the same input fails again."""


class Unavailable(Exception):
    """The provider cannot serve at all, for example without its model."""


def verify_model(model_path, manifest_path):
    """Returns the manifest when model_path is exactly the file it pins."""
    with open(manifest_path, encoding="utf-8") as file:
        manifest = json.load(file)
    try:
        size = os.stat(model_path).st_size
    except OSError as error:
        raise Unavailable(f"model file {model_path}: {error.strerror}") from error
    if size != manifest["sizeBytes"]:
        raise Unavailable(f"model file {model_path} has {size} bytes, the manifest pins {manifest['sizeBytes']}")
    digest = hashlib.sha256()
    with open(model_path, "rb") as file:
        for chunk in iter(lambda: file.read(1 << 20), b""):
            digest.update(chunk)
    if digest.hexdigest() != manifest["sha256"]:
        raise Unavailable(f"model file {model_path} does not match the manifest's SHA-256")
    return manifest


# The LiteRT package carries image signatures of 70 and 140 soft tokens.
# 70 is about three times faster on a 4-core CPU with similar zero-shot
# results in the first trial; the benchmark decides between them.
VISION_TOKENS = (70, 140)


class MediaPipeProvider:
    """EmbeddingGemma 2 through MediaPipe's Universal Embedder."""

    # EmbeddingGemma's retrieval prompt; images get none. The label
    # calibration (docs/research/photo-ai-label-calibration.md) found it the
    # better of the prompts tried for Chinese search.
    QUERY_PROMPT = "task: search result | query: {}"

    def __init__(self, model_path, manifest_path, vision_tokens=70, cache_dir=None):
        if vision_tokens not in VISION_TOKENS:
            raise ValueError(f"vision tokens must be one of {VISION_TOKENS}")
        manifest = verify_model(model_path, manifest_path)
        import mediapipe
        from mediapipe.tasks.python.core.base_options import BaseOptions
        from mediapipe.tasks.python.retrieval.universal_embedder import (
            UniversalEmbedder,
            UniversalEmbedderOptions,
        )

        self._embedder = UniversalEmbedder.create_from_options(
            UniversalEmbedderOptions(
                base_options=BaseOptions(model_asset_path=model_path),
                l2_normalize=True,
                vision_tokens_per_image=vision_tokens,
                # XNNPack keeps repacked weights here; by default it tries the
                # model's own directory, which is read-only in deployment.
                cache_dir=cache_dir,
            )
        )
        self.dimensions = len(self._vector(self._embedder.embed_text("probe")))
        # The runtime and the token budget change the vectors, so they are
        # part of the model the vectors came from.
        self.model = (
            f"{manifest['name']}@{manifest['sha256'][:12]}"
            f"+mediapipe-{mediapipe.__version__}+tok{vision_tokens}+l2"
        )

    def embed_image(self, data):
        try:
            result = self._embedder.embed_image(data)
        except (RuntimeError, ValueError) as error:
            raise InvalidInput(str(error)) from error
        return self._vector(result)

    def embed_text(self, text):
        return self._vector(self._embedder.embed_text(text))

    def embed_query(self, text):
        return self.embed_text(self.QUERY_PROMPT.format(text))

    def close(self):
        self._embedder.close()

    @staticmethod
    def _vector(result):
        return [float(value) for value in result.embeddings[0].embedding]


class FakeProvider:
    """A deterministic stand-in: equal images get equal unit vectors."""

    def __init__(self, dimensions=768):
        self.dimensions = dimensions
        self.model = f"fake-sha256-{dimensions}"

    def embed_image(self, data):
        if not data:
            raise InvalidInput("empty image")
        return self._vector(data)

    def embed_query(self, text):
        return self._vector(text.encode())

    def _vector(self, data):
        values = []
        counter = 0
        while len(values) < self.dimensions:
            block = hashlib.sha256(counter.to_bytes(4, "big") + data).digest()
            values.extend(byte / 255 - 0.5 for byte in block)
            counter += 1
        values = values[: self.dimensions]
        norm = math.sqrt(sum(value * value for value in values)) or 1.0
        return [value / norm for value in values]

    def close(self):
        pass


def encode_vector(vector):
    """Little-endian float32, as the protocol carries vectors."""
    return struct.pack(f"<{len(vector)}f", *vector)
