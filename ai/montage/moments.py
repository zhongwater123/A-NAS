"""Shot cards for a query and the checks on an editing plan (#322).

An index (eval.video_moments index) holds, per video, the shots FFmpeg found
and one window every 2 s with its frame vector, whether the frame is flat
(black or one colour) and its nearest vocabulary labels. A search scores the
windows against one or more query vectors and hands back cards: a part of a
shot where the picture holds, at most SEGMENT_MAX long, with its best window
per probe, its labels, what a local vision model said of its key frame
(captions.json, when written) and a group shared with the parts that look
alike. Text and numbers only; the model that plans never sees the frames.

A plan is a list of segments, each inside the shot of the card it names. The
checks are plain rules, so a refusal says which segment broke which one. The
same rules also make a plan with no model (plan), the version a model's plan
is compared with.
"""

import json
import math
import os

import numpy as np

# A card is a part of a shot where the picture holds (see STILL), of at least
# PIECE_MIN windows unless the shot is shorter; a part longer than SEGMENT_MAX
# is cut into equal parts. A 67-minute walk in 5 shots gives 337 cards, median
# 8 s, where cutting every 20 s gave 204 that mixed sights.
SEGMENT_MAX = 20.0
PIECE_MIN = 2
# Windows at least this alike (cosine of their vectors) show the same thing.
SAME_PICTURE = 0.97
# The pilot's 2s1f threshold that turns away 80% of queries with no answer
# (docs/research/video-moment-retrieval.md); below it a window and "not in
# the library" cannot be told apart.
THRESHOLD = 0.6533
CARDS_MAX = 40
# What a local vision model said of each card's key frame (eval.video_moments describe).
CAPTIONS = "captions.json"
TAGS = 5
SEGMENT_MIN = 1.5
SEGMENTS_MAX = 30
REASON_CHARS = 200
SHOWS_CHARS = 40
ROUNDING = 0.005
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
        rows, frames, flat, tags, shot_of, self.frame_ms = [], [], [], [], [], []
        for video, meta in sorted(self.videos.items()):
            shots = meta["shots"]
            for window in meta["windows"]:
                middle = (window["start"] + window["end"]) / 2
                rows.append((video, window["start"], window["end"]))
                self.frame_ms.append(window["frame"])
                frames.append(vectors[f"{video}/{window['frame']}"])
                flat.append(window["flat"])
                tags.append(window["tags"])
                shot_of.append(next(i for i, (s, e) in enumerate(shots) if s <= middle < e or i == len(shots) - 1))
        self.rows, self.frames = rows, np.stack(frames)
        self.flat, self.tags, self.shot_of = np.array(flat), tags, shot_of
        self.cards = self._cards()
        self.captions = {}

    @classmethod
    def load(cls, index_path: str, vectors_path: str) -> "Index":
        """The index, with the cards' captions when captions.json beside it holds them for these cards."""
        with open(index_path, encoding="utf-8") as file:
            index = json.load(file)
        with np.load(vectors_path) as data:
            vectors = dict(zip(data["keys"].tolist(), data["vectors"], strict=True))
        loaded = cls(index, vectors)
        captions = os.path.join(os.path.dirname(index_path), CAPTIONS)
        if os.path.exists(captions):
            with open(captions, encoding="utf-8") as file:
                said = json.load(file)["cards"]
            loaded.captions = {c["id"]: said[c["id"]]["caption"] for c in loaded.cards
                               if c["id"] in said and [said[c["id"]]["start"], said[c["id"]]["end"]] == [c["start"], c["end"]]}
        return loaded

    def keyframes(self, card: dict, count: int) -> list[int]:
        """count of the card's windows spread evenly from its first to its last, in time order."""
        windows = sorted(card["windows"], key=lambda i: self.rows[i][1])
        if len(windows) <= count:
            return windows
        return [windows[round(k * (len(windows) - 1) / (count - 1))] for k in range(count)]

    def _cards(self) -> list[dict]:
        """Every shot cut where its picture changes, each part of at most SEGMENT_MAX, with its windows."""
        by_shot: dict[tuple[str, int], list[int]] = {}
        for i, (video, _start, _end) in enumerate(self.rows):
            by_shot.setdefault((video, self.shot_of[i]), []).append(i)
        cards = []
        for (video, shot), members in sorted(by_shot.items()):
            start, end = self.videos[video]["shots"][shot]
            members.sort(key=lambda i: self.rows[i][1])
            part = 0
            for piece in self._pieces(members):
                lo = start if piece[0] == members[0] else self.rows[piece[0]][1]
                hi = end if piece[-1] == members[-1] else self.rows[piece[-1]][2]
                parts = max(1, math.ceil((hi - lo) / SEGMENT_MAX - 1e-9))
                for k in range(parts):
                    a, b = lo + (hi - lo) * k / parts, lo + (hi - lo) * (k + 1) / parts
                    windows = [i for i in piece if a <= (self.rows[i][1] + self.rows[i][2]) / 2 < b]
                    if not windows:
                        continue
                    cards.append({"id": f"{video}-{shot}-{part}", "video": video, "shot": [start, end],
                                  "start": round(a, 2), "end": round(b, 2), "windows": windows})
                    part += 1
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

    def _pieces(self, members: list[int]) -> list[list[int]]:
        """A shot's windows in runs whose picture holds: a window less than STILL alike to
        the mean of the run so far starts the next, once the run has PIECE_MIN windows. A walk
        or a pan that never cuts still moves from one sight to the next."""
        pieces, mean = [[members[0]]], self.frames[members[0]].copy()
        for i in members[1:]:
            if len(pieces[-1]) >= PIECE_MIN and float(self.frames[i] @ mean) / np.linalg.norm(mean) < STILL:
                pieces.append([i])
                mean = self.frames[i].copy()
            else:
                pieces[-1].append(i)
                mean += self.frames[i]
        return pieces

    def search(self, probes: dict[str, np.ndarray], limit: int = CARDS_MAX, videos=None, when=(), each=None) -> dict:
        """The cards whose best window is highest for any probe, best first; only in videos
        when given, and only over windows that meet every condition in when. With each, every
        probe keeps its own best cards instead, each of them from another group, so that a
        part of an instruction no card scores high for is not crowded out by the others."""
        if each is not None:
            found = self.search(probes, len(self.cards), videos, when)["cards"]
            picked, chosen = {}, {}
            for text in probes:
                groups = set()
                for card in sorted(found, key=lambda c: -c["probes"][text]["score"]):
                    if len(chosen.setdefault(text, [])) == each:
                        break
                    if card["group"] not in groups:
                        groups.add(card["group"])
                        chosen[text].append(card["id"])
                        picked.setdefault(card["id"], card)
            return {"threshold": THRESHOLD, "each": chosen, "cards": list(picked.values())}
        scores = {text: self.frames @ vector for text, vector in probes.items()}
        out = self._left_out(when)
        found = []
        for card in self.cards:
            if videos is not None and card["video"] not in videos:
                continue
            windows = [i for i in card["windows"] if not out[i]]
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
                **({"caption": self.captions[card["id"]]} if card["id"] in self.captions else {}),
                "score": max(b["score"] for b in best.values()), "probes": best,
                "tags": [label for label, _ in sorted(labels.items(), key=lambda kv: -kv[1])[:TAGS]],
            })
        found.sort(key=lambda c: -c["score"])
        return {"threshold": THRESHOLD, "cards": found[:limit]}

    def check(self, segments: list[dict], shows_needed: bool = False) -> list[tuple[str, float, float]]:
        """The plan's (video, start, end) in order, or PlanError listing what is wrong. With
        shows_needed every segment says in shows what its picture is to show, to be looked at."""
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
            # Cards give seconds to two places and cuts to three: half a hundredth, under a frame, is let pass.
            if start < lo - ROUNDING or end > hi + ROUNDING:
                problems.append(f"第 {n} 段 {start:g}–{end:g} 秒越出了镜头 {lo:g}–{hi:g} 秒，片段不能跨过镜头切点")
            if end - start < SEGMENT_MIN:
                problems.append(f"第 {n} 段只有 {end - start:.2g} 秒，每段至少 {SEGMENT_MIN:g} 秒")
            if any(self.flat[i] and self.rows[i][1] < end and start < self.rows[i][2] for i in self._shot_windows(card)):
                problems.append(f"第 {n} 段 {start:g}–{end:g} 秒里有纯黑或纯色的画面")
            reason = segment.get("reason", "")
            if not isinstance(reason, str) or len(reason) > REASON_CHARS:
                problems.append(f"第 {n} 段的 reason 要是一句话，最多 {REASON_CHARS} 个字符")
            shows = segment.get("shows", "")
            if not isinstance(shows, str) or len(shows) > SHOWS_CHARS or (shows_needed and not shows.strip()):
                problems.append(f"第 {n} 段的 shows 要写这段画面呈现指令里的什么，1–{SHOWS_CHARS} 个字符")
            clips.append((card["video"], float(start), float(end)))
        problems += [f"第 {a + 1} 段和第 {b + 1} 段用了同一段画面" for a in range(len(clips)) for b in range(a)
                     if clips[a][0] == clips[b][0] and min(clips[a][2], clips[b][2]) > max(clips[a][1], clips[b][1])]
        if problems:
            raise PlanError(problems)
        return clips

    def plan(self, probes: dict[str, np.ndarray], seconds: float, videos=None, when=(), look=None) -> list[dict]:
        """A plan with no model. The target is an upper bound: only cards at or over
        THRESHOLD, best first, and when they run out the cut is shorter. Each card gives
        one segment grown from its best window over neighbours also over THRESHOLD and
        not flat, at most STILL_MAX while the picture hardly changes and MOVING_MAX while
        it does; a segment showing the same picture as one taken from the same shot is
        left out. Then each video's segments in time order.

        look, when given, is asked look(card, start, end, probe) with the probe the card
        scored best on before a segment is taken: it answers the part of start-end whose
        frames show that, or None to pass the card over for the next."""
        scores = np.max([self.frames @ vector for vector in probes.values()], axis=0)
        found = [c for c in self.search(probes, CARDS_MAX, videos, when)["cards"] if c["score"] >= THRESHOLD]
        out = self._left_out(when)
        cards = {c["id"]: c for c in self.cards}
        segments, taken, left = [], [], seconds
        for card in found:
            if left < SEGMENT_MIN + 0.02:
                break
            lo, hi = card["shot"]
            windows = sorted(cards[card["id"]]["windows"], key=lambda i: self.rows[i][1])
            usable = [i for i in windows if not out[i]]
            best = max(usable, key=lambda i: scores[i])
            run = self._grow(windows, best, scores, out, (lo, hi), left)
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
            probe = max(card["probes"], key=lambda text: card["probes"][text]["score"])
            if look is not None:
                seen = look(card, start, end, probe)
                if seen is None:
                    continue
                start, end = seen
            segments.append({"card": card["id"], "start": start, "end": end, "shows": probe,
                             "reason": f"规则：分数 {card['score']}，标签 {'、'.join(card['tags'][:3])}"})
            taken.append((shot, picture))
            left -= end - start
        order = {video: n for n, video in enumerate(dict.fromkeys(c["video"] for c in found))}
        return sorted(segments, key=lambda s: (order[s["card"].rsplit("-", 2)[0]], s["start"]))

    def _still(self, run: list[int]) -> bool:
        frames = self.frames[run]
        return bool((frames @ frames.T).min() >= STILL)

    def _left_out(self, when) -> np.ndarray:
        """Flat windows, and those failing a condition: a picture that is (is, isnt) is
        closer to is than to isnt, as night to 夜晚 rather than 白天."""
        out = self.flat.copy()
        for is_, isnt in when:
            out |= self.frames @ is_ <= self.frames @ isnt
        return out

    def _grow(self, windows: list[int], best: int, scores: np.ndarray, out: np.ndarray, shot: tuple,
              left: float) -> list[int]:
        """Windows next to best, the higher-scoring side first, while each is over
        THRESHOLD and not left out and the run stays within its length."""
        n = windows.index(best)
        a, b = n, n
        while True:
            sides = [k for k in (a - 1, b + 1) if 0 <= k < len(windows)
                     and not out[windows[k]] and scores[windows[k]] >= THRESHOLD]
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
