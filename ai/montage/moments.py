"""Shot cards for a query and the checks on an editing plan (#322).

An index (eval.video_moments index) holds, per video, the shots FFmpeg found
and one window every 2 s with its frame vector, whether the frame is flat
(black or one colour) and its nearest vocabulary labels. A search scores the
windows against one or more query vectors and hands back cards: a part of a
shot at most SEGMENT_MAX long, with its best window per probe, its labels and
a group shared with the parts that look alike. Text and numbers only; the
model that plans never sees the frames.

A plan is a list of segments, each inside the shot of the card it names. The
checks are plain rules, so a refusal says which segment broke which one. The
same rules also make a plan with no model (plan), the version a model's plan
is compared with.
"""

import json
import math

import numpy as np

# A card covers at most this much of a shot; longer shots are cut into equal parts.
SEGMENT_MAX = 20.0
# Windows at least this alike (cosine of their vectors) show the same thing.
SAME_PICTURE = 0.97
# The pilot's 2s1f threshold that turns away 80% of queries with no answer
# (docs/research/video-moment-retrieval.md); below it a window and "not in
# the library" cannot be told apart.
THRESHOLD = 0.6533
CARDS_MAX = 40
TAGS = 5
SEGMENT_MIN = 1.5
SEGMENTS_MAX = 30
TOTAL_MAX = 180.0
REASON_CHARS = 200
# A segment may run this long while its picture hardly changes (every two of its
# windows at least STILL alike: a slow pan or a followed boat is 0.93-0.98, a new
# shot 0.63-0.75), and this long while it keeps changing. Grey-level frame
# differences cannot tell: a moving camera changes the pixels, not the picture.
STILL = 0.9
STILL_MAX = 6.0
MOVING_MAX = 8.0


class PlanError(Exception):
    """The plan broke the rules; problems lists each in a sentence."""

    def __init__(self, problems):
        super().__init__("；".join(problems))
        self.problems = problems


class Index:
    def __init__(self, index: dict, vectors: dict):
        self.model = index["model"]
        self.videos = index["videos"]
        rows, frames, flat, tags, shot_of = [], [], [], [], []
        for video, meta in sorted(self.videos.items()):
            shots = meta["shots"]
            for window in meta["windows"]:
                middle = (window["start"] + window["end"]) / 2
                rows.append((video, window["start"], window["end"]))
                frames.append(vectors[f"{video}/{window['frame']}"])
                flat.append(window["flat"])
                tags.append(window["tags"])
                shot_of.append(next(i for i, (s, e) in enumerate(shots) if s <= middle < e or i == len(shots) - 1))
        self.rows, self.frames = rows, np.stack(frames)
        self.flat, self.tags, self.shot_of = np.array(flat), tags, shot_of
        self.cards = self._cards()

    @classmethod
    def load(cls, index_path: str, vectors_path: str) -> "Index":
        with open(index_path, encoding="utf-8") as file:
            index = json.load(file)
        with np.load(vectors_path) as data:
            vectors = dict(zip(data["keys"].tolist(), data["vectors"], strict=True))
        return cls(index, vectors)

    def _cards(self) -> list[dict]:
        """Every shot cut into parts of at most SEGMENT_MAX, each with its windows."""
        by_shot: dict[tuple[str, int], list[int]] = {}
        for i, (video, _start, _end) in enumerate(self.rows):
            by_shot.setdefault((video, self.shot_of[i]), []).append(i)
        cards = []
        for (video, shot), members in sorted(by_shot.items()):
            start, end = self.videos[video]["shots"][shot]
            parts = max(1, math.ceil((end - start) / SEGMENT_MAX - 1e-9))
            for part in range(parts):
                lo, hi = start + (end - start) * part / parts, start + (end - start) * (part + 1) / parts
                windows = [i for i in members if lo <= (self.rows[i][1] + self.rows[i][2]) / 2 < hi]
                if not windows:
                    continue
                cards.append({"id": f"{video}-{shot}-{part}", "video": video, "shot": [start, end],
                              "start": round(lo, 2), "end": round(hi, 2), "windows": windows})
        # Parts whose mean pictures are SAME_PICTURE alike share a group, numbered by first appearance.
        means = np.stack([self.frames[c["windows"]].mean(axis=0) for c in cards])
        means /= np.linalg.norm(means, axis=1, keepdims=True)
        group = list(range(len(cards)))
        for i in range(len(cards)):
            for j in range(i):
                if group[j] == j and float(means[i] @ means[j]) >= SAME_PICTURE:
                    group[i] = j
                    break
        firsts = {g: n for n, g in enumerate(dict.fromkeys(group))}
        for card, g in zip(cards, group, strict=True):
            card["group"] = firsts[g]
        return cards

    def search(self, probes: dict[str, np.ndarray], limit: int = CARDS_MAX, videos=None) -> dict:
        """The cards whose best window is highest for any probe, best first; only in videos when given."""
        scores = {text: self.frames @ vector for text, vector in probes.items()}
        found = []
        for card in self.cards:
            if videos is not None and card["video"] not in videos:
                continue
            windows = [i for i in card["windows"] if not self.flat[i]]
            if not windows:
                continue
            best = {}
            for text, s in scores.items():
                i = max(windows, key=lambda w: s[w])
                best[text] = {"score": round(float(s[i]), 4), "at": [self.rows[i][1], self.rows[i][2]]}
            labels: dict[str, int] = {}
            for i in windows:
                for label in self.tags[i]:
                    labels[label] = labels.get(label, 0) + 1
            found.append({
                "id": card["id"], "video": card["video"], "start": card["start"], "end": card["end"],
                "shot": card["shot"], "group": card["group"],
                "score": max(b["score"] for b in best.values()), "probes": best,
                "tags": [label for label, _ in sorted(labels.items(), key=lambda kv: -kv[1])[:TAGS]],
            })
        found.sort(key=lambda c: -c["score"])
        return {"threshold": THRESHOLD, "cards": found[:limit]}

    def check(self, segments: list[dict]) -> list[tuple[str, float, float]]:
        """The plan's (video, start, end) in order, or PlanError listing what is wrong."""
        cards = {c["id"]: c for c in self.cards}
        problems = []
        if not 1 <= len(segments) <= SEGMENTS_MAX:
            raise PlanError([f"segments 要有 1–{SEGMENTS_MAX} 段，收到 {len(segments)} 段"])
        clips = []
        for n, segment in enumerate(segments, 1):
            card = cards.get(segment.get("card")) if isinstance(segment, dict) else None
            if card is None:
                problems.append(f"第 {n} 段的 card 不是检索结果里的镜头卡片")
                continue
            start, end = segment.get("start"), segment.get("end")
            if not all(isinstance(t, (int, float)) and not isinstance(t, bool) for t in (start, end)):
                problems.append(f"第 {n} 段的 start、end 要是秒数")
                continue
            lo, hi = card["shot"]
            if start < lo or end > hi:
                problems.append(f"第 {n} 段 {start:g}–{end:g} 秒越出了镜头 {lo:g}–{hi:g} 秒，片段不能跨过镜头切点")
            if end - start < SEGMENT_MIN:
                problems.append(f"第 {n} 段只有 {end - start:.2g} 秒，每段至少 {SEGMENT_MIN:g} 秒")
            if any(self.flat[i] and self.rows[i][1] < end and start < self.rows[i][2] for i in self._shot_windows(card)):
                problems.append(f"第 {n} 段 {start:g}–{end:g} 秒里有纯黑或纯色的画面")
            reason = segment.get("reason", "")
            if not isinstance(reason, str) or len(reason) > REASON_CHARS:
                problems.append(f"第 {n} 段的 reason 要是一句话，最多 {REASON_CHARS} 个字符")
            clips.append((card["video"], float(start), float(end)))
        problems += [f"第 {a + 1} 段和第 {b + 1} 段用了同一段画面" for a in range(len(clips)) for b in range(a)
                     if clips[a][0] == clips[b][0] and min(clips[a][2], clips[b][2]) > max(clips[a][1], clips[b][1])]
        total = sum(end - start for _video, start, end in clips)
        if total > TOTAL_MAX:
            problems.append(f"总长 {total:.1f} 秒，超过上限 {TOTAL_MAX:g} 秒")
        if problems:
            raise PlanError(problems)
        return clips

    def plan(self, probes: dict[str, np.ndarray], seconds: float, videos=None) -> list[dict]:
        """A plan with no model. The target is an upper bound: only cards at or over
        THRESHOLD, best first, and when they run out the cut is shorter. Each card gives
        one segment grown from its best window over neighbours also over THRESHOLD and
        not flat, at most STILL_MAX while the picture hardly changes and MOVING_MAX while
        it does; a segment showing the same picture as one taken from the same shot is
        left out. Then each video's segments in time order."""
        scores = np.max([self.frames @ vector for vector in probes.values()], axis=0)
        found = [c for c in self.search(probes, CARDS_MAX, videos)["cards"] if c["score"] >= THRESHOLD]
        cards = {c["id"]: c for c in self.cards}
        segments, taken, left = [], [], seconds
        for card in found:
            if left < SEGMENT_MIN + 0.02:
                break
            lo, hi = card["shot"]
            windows = sorted(cards[card["id"]]["windows"], key=lambda i: self.rows[i][1])
            usable = [i for i in windows if not self.flat[i]]
            best = max(usable, key=lambda i: scores[i])
            run = self._grow(windows, best, scores, (lo, hi), left)
            start, end = max(lo, self.rows[run[0]][1]), min(hi, self.rows[run[-1]][2])
            if end - start < SEGMENT_MIN + 0.02:  # a short shot: the whole of it
                start, end = max(lo, min(start, card["start"])), min(hi, max(end, card["end"]))
            cap = min(left, STILL_MAX if self._still(run) else MOVING_MAX)
            if end - start > cap:  # trimmed around the best window
                middle = (self.rows[best][1] + self.rows[best][2]) / 2
                start = min(max(start, middle - cap / 2), end - cap)
                end = start + cap
            picture = self.frames[run].mean(axis=0)
            picture /= np.linalg.norm(picture)
            shot = (card["video"], lo, hi)
            if any(s == shot and float(p @ picture) >= SAME_PICTURE for s, p in taken):
                continue
            # Rounded inward, so that the segment stays inside its shot.
            start, end = math.ceil(start * 100) / 100, math.floor(end * 100) / 100
            if end - start < SEGMENT_MIN:
                continue
            segments.append({"card": card["id"], "start": start, "end": end,
                             "reason": f"规则：分数 {card['score']}，标签 {'、'.join(card['tags'][:3])}"})
            taken.append((shot, picture))
            left -= end - start
        order = {video: n for n, video in enumerate(dict.fromkeys(c["video"] for c in found))}
        return sorted(segments, key=lambda s: (order[s["card"].rsplit("-", 2)[0]], s["start"]))

    def _still(self, run: list[int]) -> bool:
        frames = self.frames[run]
        return bool((frames @ frames.T).min() >= STILL)

    def _grow(self, windows: list[int], best: int, scores: np.ndarray, shot: tuple, left: float) -> list[int]:
        """Windows next to best, the higher-scoring side first, while each is over
        THRESHOLD and not flat and the run stays within its length."""
        n = windows.index(best)
        a, b = n, n
        while True:
            sides = [k for k in (a - 1, b + 1) if 0 <= k < len(windows)
                     and not self.flat[windows[k]] and scores[windows[k]] >= THRESHOLD]
            if not sides:
                return windows[a:b + 1]
            k = max(sides, key=lambda k: scores[windows[k]])
            na, nb = min(a, k), max(b, k)
            run = windows[na:nb + 1]
            length = min(shot[1], self.rows[run[-1]][2]) - max(shot[0], self.rows[run[0]][1])
            if length > min(left, STILL_MAX if self._still(run) else MOVING_MAX) + 2:
                return windows[a:b + 1]
            a, b = na, nb

    def _shot_windows(self, card: dict) -> list[int]:
        lo, hi = card["shot"]
        return [i for i, (video, start, end) in enumerate(self.rows)
                if video == card["video"] and start < hi and lo < end]
