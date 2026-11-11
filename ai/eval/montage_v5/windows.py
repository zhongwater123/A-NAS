"""A set's window index, read: every 2 s window of every video, with its key frame's vector (EmbeddingGemma 2)
and whether the frame is flat (black, a single colour). Built beforehand by A-NAS's video indexer
(eval/video_moments.py there): video-moments-<key>/embeddinggemma*/index.json and frames.npz.

What A-NAS's montage.moments.Index reads, without its catalog, search and planning: the cards here are cut
by cards.py, and the montage reads only the windows and their vectors.
"""
import json

import numpy as np


class Index:
    def __init__(self, index: dict, vectors: dict):
        self.videos = index["videos"]
        rows, frames, flat = [], [], []
        for video, meta in sorted(self.videos.items()):
            for window in meta["windows"]:
                rows.append((video, window["start"], window["end"]))
                frames.append(vectors[f"{video}/{window['frame']}"])
                flat.append(window["flat"])
        self.rows, self.frames, self.flat = rows, np.stack(frames), np.array(flat)

    @classmethod
    def load(cls, index_path, vectors_path) -> "Index":
        with open(index_path, encoding="utf-8") as file:
            index = json.load(file)
        with np.load(vectors_path) as data:
            vectors = dict(zip(data["keys"].tolist(), data["vectors"], strict=True))
        return cls(index, vectors)
