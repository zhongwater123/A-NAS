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

import functools
import itertools
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
# How the camera moves in each card and how its picture looks (eval.video_moments measure),
# and the measures a picture is called blurred, shaky, dark or blown out past (montage.picture),
# set on the pilot set's extremes. Sharpness is mostly how much texture a picture has: aurora,
# fog and a close-up with a soft background score low too, so only the softest are blurred.
# Shake also catches a cut the shot detection missed. A sky is a tenth to a fifth blown out.
PICTURES = "pictures.json"
BLURRED = 20.0
SHAKY = 0.01
DARK = 25.0
BLOWN = 0.3
TAGS = 5
SEGMENT_MIN = 1.5
# A segment the program's own trim off a cut at its ends shortened may run down to this (#350):
# a 1.5 s segment kept 0.05 s clear of a cut is not refused for it.
TRIMMED_MIN = 1.2
SEGMENTS_MAX = 30
REASON_CHARS = 200
SHOWS_CHARS = 40
# Why a card the search gave was not used, kept with the cut for review.
WHY_CHARS = 80
ROUNDING = 0.005
# Windows at least this alike to the part so far show the same picture (a slow pan or a
# followed boat is 0.93-0.98, a new shot 0.63-0.75). Grey-level frame differences
# cannot tell: a moving camera changes the pixels, not the picture.
STILL = 0.9
# The longest a segment may run, changing or not: one picture for 8 s tires the eye.
CLIP_MAX = 6.0
# Index.span's two ways to grow a segment from the window a probe matched (#350): over
# neighbours this close to its probe score, or over those STILL alike to its picture.
SPANS = ("probe", "frame")
SPAN_DROP = 0.02
# Index.fill grows and shrinks segments by this much, each step judged on the frame at its middle.
STEP = 0.5
# A trailer's last quarter moves faster: its segments run TAIL_MIN to TAIL_MAX, so that it
# tightens without a string of two-second jumps.
TAIL_MAX = 4.0
TAIL_MIN = 2.5
# A pause between words at least this long is a place to cut; an end inside speech moves
# at most SNAP to reach one.
PAUSE = 0.3
SNAP = 1.5
# A step whose frame is less alike than this to its segment's peak frame shows another shot
# (one the cut detection missed), and the segment does not take it.
FLOOR = 0.8
# What the vision model said of each card's place alone (eval.video_moments scenes, #350),
# and the scenes they make: cards whose place descriptions' vectors are on average this alike
# (EmbeddingGemma's clustering prompt) are one scene. 0.92 told apart case-01's office, car,
# beach and dark rooms, which pictures alone do not, and joined case-08's park, which the
# film shows twice five minutes apart.
SCENES = "scenes.json"
SCENE_ALIKE = 0.92
# Within one such group, cards whose mean pictures are on average less alike than this are
# other places: a description as broad as "室内，光线昏暗" took case-08's wooden room and its
# foggy colonnade together, which the pictures tell apart. 0.80 split case-08's 17 groups into
# 28 and case-01's 11 into 16, its beach from its cliff top.
SCENE_PICTURE = 0.80
# A cut ends on a segment it can settle on: its last one aims at this pace's seconds at the least (#350).
ENDING_PACE = "中"
# A card's pace from how much its picture changes (montage.picture.change, #350), not from
# what a model reads into its description, which called case-08's still snipers fast: under
# the first share slow, from the second fast. Case-01 then has no fast card and 64 slow of
# 78; case-08 17 slow, 91 middle and 27 fast of 135, its snipers slow or middle, its
# machines fighting fast.
PACE_CHANGE = (0.01, 0.06)
# How alike two scenes are, for a plan's scene changes (#350): the mean of two ranks among the
# film's pairs of scenes, by their place descriptions' vectors and by their colour and light
# (colours), each 0 for the least alike pair and 1 for the most. Ranks, for each measure runs
# over a span of its own (case-08's descriptions 0.86–0.95, its colours 0.5–0.9). Pictures'
# vectors are left out: case-08's put its brain model and its sniper's scope, the plan's
# harshest cut, near the middle. Scenes at least this alike are the ones a scene may change to
# (near; listed only with catalog joins): 0.45 keeps that cut out (0.30) and lets case-01's
# street lead into its office (0.49). Plans are no longer held to it: the list was more than the
# model could weigh, and the check cost case-01 its car scene (#350).
SCENE_NEAR = 0.45
# A picture's colour and light: its pixels in BINS × BINS × BINS bins of red, green and blue.
BINS = 4
# The seconds a segment of each pace runs when the plan does not say (#350): a trailer's shots
# run one and a half to four seconds, a still one read as soon as a moving one. The planner sets
# each segment's seconds itself (剪法18) and is told no paces; without seconds, each segment
# aims at the middle of its pace's range, all scaled to the cut's target.
PACE_RANGES = {"慢": (3.0, 4.0), "中": (2.0, 3.0), "快": (1.5, 2.0)}
PACES = {name: (low + high) / 2 for name, (low, high) in PACE_RANGES.items()}

# The cards a plan picks can run at least this share of its target, so that the program has
# room to set each segment's seconds (#350).
MARGIN = 1.1
# A scene's name is cut to this many characters: a 2B model sometimes repeats itself.
SCENE_CHARS = 40
# Words with their seconds by video, beside the index, from speech recognition.
SPEECH = "speech.json"
# The film cut into parts for the planner's catalog (#398, 剪法17), in place of scenes: about
# PART_SECONDS each, each cut moved to the card boundary within PART_SNAP of it where colour and
# light change most, so that a part seldom ends inside a scene, without judging what a scene is.
# A part whose card lines run past PART_CHARS is listed by a summary the local model writes
# (look.Vision.summary, eval.video_moments parts), and the planner names the part, not a card.
PARTS = "parts.json"
PART_SECONDS = 60.0
PART_SNAP = 10.0
PART_CHARS = 300
# A plan's paragraph (剪法19) takes at most this many shots in a row from the film.
SHOTS_MAX = 6
# Cards of a part scoring within this of its best for a segment's probe count as about as good
# (Index.pick): case-08's best windows for one probe differ by a few hundredths.
PICK_NEAR = 0.03


def part_starts(starts: list[float], changes: list[float], every: float = PART_SECONDS,
                snap: float = PART_SNAP) -> list[int]:
    """The indices of the cards that open a part, the first card always: cards in film order by
    their starts in seconds, changes[k] how much colour and light change from card k - 1 to card
    k. A part runs about every seconds: the card within snap of its end with the most change
    opens the next part, or with none within snap the card starting nearest that end."""
    out = [0]
    while True:
        due = starts[out[-1]] + every
        later = range(out[-1] + 1, len(starts))
        if not later or starts[-1] < due - snap:
            return out
        near = [k for k in later if abs(starts[k] - due) <= snap]
        out.append(max(near, key=lambda k: (changes[k], -k)) if near else min(later, key=lambda k: abs(starts[k] - due)))


def screened(found: dict, verdicts: dict, each: int) -> dict:
    """An experiment (#326): a search with each narrowed to the cards a vision model said show
    their probe (verdicts by (card id, probe), as look.screened gives them), its best scores
    first and the search's order between equals, at most each a probe. Every card kept says
    what it scored for each probe it is kept for; refused counts each probe's cards that failed."""
    cards = {card["id"]: card for card in found["cards"]}
    chosen, kept, refused = {}, {}, {}
    for probe, ids in found["each"].items():
        passed = [i for i in ids if verdicts[(i, probe)]["passes"]]
        refused[probe] = len(ids) - len(passed)
        passed.sort(key=lambda i: -(verdicts[(i, probe)]["score"] or 0))
        chosen[probe] = passed[:each]
        for i in chosen[probe]:
            verdict = verdicts[(i, probe)]
            kept.setdefault(i, cards[i] | {"screen": {}})["screen"][probe] = {
                "score": verdict["score"], "seen": verdict["seen"]}
    return {"threshold": found["threshold"], "each": chosen, "refused": refused, "cards": list(kept.values())}


class PlanError(Exception):
    """The plan broke the rules; problems lists each in a sentence."""

    def __init__(self, problems):
        super().__init__("；".join(problems))
        self.problems = problems


def _for_cards(entries: dict, cards: list[dict]) -> dict:
    """The entries written for these cards, over the same seconds."""
    return {c["id"]: entries[c["id"]] for c in cards
            if c["id"] in entries and [entries[c["id"]]["start"], entries[c["id"]]["end"]] == [c["start"], c["end"]]}


def aims(paces: list[str]) -> list[float]:
    """The seconds each paced segment aims at, the last one at least ENDING_PACE's."""
    out = [PACES[p] for p in paces]
    if out:
        out[-1] = max(out[-1], PACES[ENDING_PACE])
    return out


def colours(pixels: np.ndarray) -> np.ndarray:
    """A picture's colour and light, from its RGB pixels: the square root of the share of them in
    each bin (BINS), so that the dot product of two is their Bhattacharyya coefficient, 1 alike."""
    bins = (np.asarray(pixels, dtype=np.uint8).reshape(-1, 3) // (256 // BINS)).astype(np.int64)
    counts = np.bincount((bins[:, 0] * BINS + bins[:, 1]) * BINS + bins[:, 2], minlength=BINS ** 3)
    return np.sqrt(counts / max(counts.sum(), 1)).astype(np.float32)


def _ranks(values: np.ndarray) -> np.ndarray:
    """Each value's rank among values from 0 (the least) to 1 (the most), ties sharing theirs."""
    if len(values) < 2:
        return np.ones(len(values))
    below = (values[:, None] > values[None, :]).sum(axis=1)
    tied = (values[:, None] == values[None, :]).sum(axis=1) - 1
    return (below + tied / 2) / (len(values) - 1)


def _linked(alike: np.ndarray, members: list[int], threshold: float) -> list[list[int]]:
    """members grouped by average linkage on alike while the two closest groups are threshold alike."""
    groups = [[k] for k in members]
    while len(groups) > 1:
        best, pair = -1.0, None
        for a in range(len(groups)):
            for b in range(a):
                s = float(alike[np.ix_(groups[a], groups[b])].mean())
                if s > best:
                    best, pair = s, (a, b)
        if best < threshold:
            break
        a, b = pair
        groups[b] += groups.pop(a)
    return groups


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
        self.captions, self.notes, self.pictures = {}, {}, {}
        self.words: dict[str, list] = {}
        self.scenes: dict[str, str] = {}
        # The title, credits or caption over a card's picture ("kind：words", montage.look.TITLES).
        self.texts: dict[str, str] = {}
        # Each card's scene number, from 1 in order of first appearance, and each scene's name.
        self.scene_of: dict[str, int] = {}
        self.scene_names: dict[int, str] = {}
        # How alike each two scenes are (SCENE_NEAR), by scene number.
        self.scene_alike: dict[int, dict[int, float]] = {}
        # The film's parts for the catalog (PARTS), in order: each its video, cards and summary.
        self.parts: list[dict] = []

    @classmethod
    def load(cls, index_path: str, vectors_path: str) -> "Index":
        """The index, with the cards' captions (captions.json) and their camera motion and looks
        (pictures.json) when the files beside it hold them for these cards."""
        with open(index_path, encoding="utf-8") as file:
            index = json.load(file)
        with np.load(vectors_path) as data:
            vectors = dict(zip(data["keys"].tolist(), data["vectors"], strict=True))
        loaded = cls(index, vectors)
        captions = os.path.join(os.path.dirname(index_path), CAPTIONS)
        if os.path.exists(captions):
            with open(captions, encoding="utf-8") as file:
                said = json.load(file)["cards"]
            said = _for_cards(said, loaded.cards)
            loaded.captions = {i: entry["caption"] for i, entry in said.items()}
            loaded.notes = {i: {k: entry[k] for k in ("light", "highlight")} for i, entry in said.items()
                            if all(k in entry for k in ("light", "highlight"))}
        pictures = os.path.join(os.path.dirname(index_path), PICTURES)
        if os.path.exists(pictures):
            with open(pictures, encoding="utf-8") as file:
                loaded.pictures = _for_cards(json.load(file)["cards"], loaded.cards)
        scenes = os.path.join(os.path.dirname(index_path), SCENES)
        if os.path.exists(scenes):
            with open(scenes, encoding="utf-8") as file:
                said = json.load(file)
            kept = _for_cards(said["cards"], loaded.cards)
            loaded.scenes = {i: e["scene"] for i, e in kept.items()}
            loaded.texts = {i: e["text"] for i, e in kept.items() if e.get("text")}
            loaded.scene_of = {i: e["group"] for i, e in kept.items() if "group" in e}
            loaded.scene_names = {int(n): name for n, name in said.get("groups", {}).items()}
            loaded.scene_alike = {int(a): {int(b): v for b, v in row.items()} for a, row in said.get("alike", {}).items()}
        parts = os.path.join(os.path.dirname(index_path), PARTS)
        if os.path.exists(parts):
            with open(parts, encoding="utf-8") as file:
                said = json.load(file)
            # Kept only when written for these cards over the same seconds.
            spans = {c["id"]: [c["start"], c["end"]] for c in loaded.cards}
            if said.get("cards") == spans:
                loaded.parts = said["parts"]
        speech = os.path.join(os.path.dirname(index_path), SPEECH)
        if os.path.exists(speech):
            with open(speech, encoding="utf-8") as file:
                loaded.words = {v: [(a, b) for a, b, *_ in words] for v, words in json.load(file)["videos"].items()}
        return loaded

    @staticmethod
    def keyframes(card: dict, count: int) -> list[int]:
        """The milliseconds of count frames spread evenly over the card's own seconds, each in the
        middle of its equal share of them (#394). Not its windows' frames: a window's frame is
        every 2 seconds, so that a short card had one or two of them, and a frame on its end,
        which shows the next shot (case-08: 56 cards of 156 one frame, 5 the next shot)."""
        span = card["end"] - card["start"]
        return [round((card["start"] + span * (k + 0.5) / count) * 1000) for k in range(count)]

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

    def catalog(self, videos=None, times=True, scenes=False, lengths=False, titles=True, paces=False,
                joins=False) -> dict:
        """What the library holds, for writing a story and probes from (#326): every card of every
        video (or of videos), in time order, as one line of its seconds and its shot's, light,
        caption, highlight and flaws, enough to cut it without searching for it. Cards with no
        caption say so; a vector alone cannot be read. Without times, the line leaves out the
        seconds, for a planner that does not set them (#350). With scenes, each line says its
        card's scene number, and "scenes" names each scene once with its number of cards. With
        lengths, each line says the most seconds a segment of its card can run (usable); with
        paces, its pace from its picture's change (pace_of), and each scene how many cards of each
        pace it has; with joins, the scenes it may change to (near). Without titles, cards with a title,
        credits, a logo or a caption over them (self.texts) are left out."""
        out = {}
        listed = [c for c in self.cards if (videos is None or c["video"] in videos)
                  and (titles or c["id"] not in self.texts)]
        for card in listed:
            notes = self.notes.get(card["id"], {})
            a, b = card["shot"]
            line = card["id"] + (f" {card['start']:g}–{card['end']:g} 秒（镜头 {round(a, 2):g}–{round(b, 2):g}）"
                                 if times else "")
            if scenes and card["id"] in self.scene_of:
                line += f" 场景{self.scene_of[card['id']]}"
            pace = self.pace_of(card["id"]) if paces else None
            if lengths:
                line += f" 可剪{self.usable(card):g}秒"
            if pace:
                line += f" 节奏{pace}"
            if notes.get("light"):
                line += f" {notes['light']}"
            line += " " + self.captions.get(card["id"], "（没有描述）")
            if notes.get("highlight"):
                line += f"（亮点：{notes['highlight']}）"
            flaws = self._looks(card["id"]).get("flaws")
            if flaws:
                line += f" 毛病：{'、'.join(flaws)}"
            entry = out.setdefault(card["video"], {"seconds": round(self.videos[card["video"]]["duration"], 1), "cards": []})
            entry["cards"].append(line)
        if not scenes or not self.scene_of:
            return {"videos": out}
        counts, paced = {}, {}
        for card in listed:
            if card["id"] in self.scene_of:
                n = self.scene_of[card["id"]]
                counts[n] = counts.get(n, 0) + 1
                pace = self.pace_of(card["id"]) if paces else None
                if pace:
                    paced.setdefault(n, dict.fromkeys(PACES, 0))[pace] += 1
        return {"scenes": {str(n): f"{self.scene_names.get(n, '')}（{k} 张卡片{self._paces(paced.get(n))}）{self._joins(n, counts) if joins else ''}"
                           for n, k in sorted(counts.items())}, "videos": out}

    @staticmethod
    def _paces(paced: dict | None) -> str:
        return "：" + "、".join(f"{p} {k}" for p, k in paced.items() if k) if paced else ""

    def pick(self, part: int, probe: np.ndarray, taken=()) -> str | None:
        """The card of part (from 1) whose best window shows probe most, of those not taken if
        any are left and not under a title, for a segment that names a part (#398); None for
        no such part."""
        if not isinstance(part, int) or isinstance(part, bool) or not 1 <= part <= len(self.parts):
            return None
        cards = {c["id"]: c for c in self.cards}
        # Only cards a segment can be cut from (#398: case-01's last part gave a black card).
        ids = [i for i in self.parts[part - 1]["cards"]
               if i in cards and i not in self.texts and self.usable(cards[i]) >= SEGMENT_MIN]
        free = [i for i in ids if i not in taken] or ids
        scored = {i: max((float(self.frames[w] @ probe) for w in cards[i]["windows"] if not self.flat[w]), default=-1.0)
                  for i in free}
        if not scored:
            return None
        # Of the cards about as good as the best, the one that can run longest: the planner
        # cannot see how long a part's cards are, and the cut needs room to set its seconds.
        best = max(scored.values())
        return max((i for i, s in scored.items() if s >= best - PICK_NEAR), key=lambda i: (self.usable(cards[i]), scored[i]))

    def run(self, anchor: str, count: int, taken=()) -> list[str]:
        """anchor and the cards after it in film order, count in all (#398, 剪法19): of its video
        and, with parts, of its part, none under a title, taken or too short to cut (SEGMENT_MIN);
        those just before it when the part or the video ends first. The film's own editing joins
        them."""
        cards = {c["id"]: c for c in self.cards}
        video = cards[anchor]["video"]
        part = next((p["cards"] for p in self.parts if anchor in p["cards"]), None)
        order = [c["id"] for c in sorted(self.cards, key=lambda c: c["start"])
                 if c["video"] == video and (part is None or c["id"] in part)]
        def fit(i):
            return i not in self.texts and i not in taken and self.usable(cards[i]) >= SEGMENT_MIN
        k = order.index(anchor)
        after = [i for i in order[k + 1:] if fit(i)][:count - 1]
        before = [i for i in reversed(order[:k]) if fit(i)][:count - 1 - len(after)]
        return [*reversed(before), anchor, *after]

    def pace_of(self, card_id: str) -> str | None:
        """The card's pace from how much its picture changes (PACE_CHANGE), however many seconds
        it has; None unmeasured."""
        change = self.pictures.get(card_id, {}).get("change")
        card = next((c for c in self.cards if c["id"] == card_id), None)
        if change is None or card is None:
            return None
        slow, fast = PACE_CHANGE
        return "慢" if change < slow else "快" if change >= fast else "中"

    def near(self, scene: int, among=None) -> list[int]:
        """The scenes a plan may change to from scene (SCENE_NEAR), among those given if any."""
        row = self.scene_alike.get(scene, {})
        return sorted(n for n, v in row.items() if v >= SCENE_NEAR and (among is None or n in among))

    def _joins(self, scene: int, listed) -> str:
        if not self.scene_alike:
            return ""
        near = self.near(scene, listed)
        return f" 可接：{'、'.join(map(str, near))}" if near else " 可接：无（只能放开头或结尾）"

    def search(self, probes: dict[str, np.ndarray], limit: int = CARDS_MAX, videos=None, when=(), each=None,
               lights=None, words=None) -> dict:
        """The cards whose best window is highest for any probe, best first; only in videos
        when given, and only over windows that meet every condition in when. With each, every
        probe keeps its own best cards instead, each of them from another group, so that a
        part of an instruction no card scores high for is not crowded out by the others.
        lights and words narrow the cards before they are ranked: only those whose light the
        vision model gave is one of lights, and only those whose caption or highlight holds
        one of words, so that a condition is met first and the probe ranks only what meets it."""
        if each is not None:
            found = self.search(probes, len(self.cards), videos, when, lights=lights, words=words)["cards"]
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
            notes = self.notes.get(card["id"], {})
            if lights is not None and notes.get("light") not in lights:
                continue
            if words is not None and not any(
                    w in self.captions.get(card["id"], "") or w in notes.get("highlight", "") for w in words):
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
                **self.notes.get(card["id"], {}), **self._looks(card["id"]),
                "score": max(b["score"] for b in best.values()), "probes": best,
                "tags": [label for label, _ in sorted(labels.items(), key=lambda kv: -kv[1])[:TAGS]],
            })
        found.sort(key=lambda c: -c["score"])
        return {"threshold": THRESHOLD, "cards": found[:limit]}

    def _looks(self, card_id: str) -> dict:
        """The camera's motion and what is wrong with the picture, when the card was measured."""
        measured = self.pictures.get(card_id)
        if measured is None:
            return {}
        flaws = [word for word, bad in (
            ("模糊", measured["sharp"] < BLURRED), ("抖动", measured["shake"] > SHAKY),
            ("过暗", measured["bright"] < DARK), ("过曝", measured["clipped"] > BLOWN)) if bad]
        return {"motion": measured["motion"], "flaws": flaws}

    def check(self, segments: list[dict], shows_needed: bool = False, skipped=None,
              shortest=SEGMENT_MIN) -> list[tuple[str, float, float]]:
        """The plan's (video, start, end) in order, or PlanError listing what is wrong. With
        shows_needed every segment says in shows what its picture is to show, to be looked at;
        skipped, when given, lists cards left out, each once and with why in a sentence. Segments run
        shortest seconds at least (TRIMMED_MIN after the program's trims). Which scene follows
        which, and how many segments a scene gives, are the plan's (#350): rules on them kept the
        plan from cutting between places, and case-08's battle spans five of them."""
        cards = {c["id"]: c for c in self.cards}
        problems = []
        if not 1 <= len(segments) <= SEGMENTS_MAX:
            raise PlanError([f"segments 要有 1–{SEGMENTS_MAX} 段，收到 {len(segments)} 段"])
        clips = []
        for n, segment in enumerate(segments, 1):
            card = cards.get(segment.get("card")) if isinstance(segment, dict) else None
            if card is None:
                problems.append(f"第 {n} 段的 card 不是目录或检索结果里的卡片")
                continue
            start, end = segment.get("start"), segment.get("end")
            if not all(isinstance(t, (int, float)) and not isinstance(t, bool) for t in (start, end)):
                problems.append(f"第 {n} 段的 start、end 要是秒数")
                continue
            lo, hi = card["shot"]
            # Cards give seconds to two places and cuts to three: half a hundredth, under a frame, is let pass.
            if start < lo - ROUNDING or end > hi + ROUNDING:
                problems.append(f"第 {n} 段 {start:g}–{end:g} 秒越出了镜头 {lo:g}–{hi:g} 秒，片段不能跨过镜头切点")
            if end - start < shortest - ROUNDING:
                problems.append(f"第 {n} 段只有 {end - start:.2g} 秒，每段至少 {shortest:g} 秒")
            if end - start > CLIP_MAX + ROUNDING:
                problems.append(f"第 {n} 段有 {end - start:.3g} 秒，每段最多 {CLIP_MAX:g} 秒")
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
        if skipped is not None:
            problems += self._skipped_problems(skipped, {s.get("card") for s in segments if isinstance(s, dict)}, cards)
        if problems:
            raise PlanError(problems)
        return clips

    def scene_groups(self, vectors: dict) -> tuple[dict, dict]:
        """Scenes from the place descriptions' vectors, by card id: cards whose descriptions are
        SCENE_ALIKE alike on average, then within each such group those whose mean pictures are
        SCENE_PICTURE alike (average linkage both times). Each card's scene number, from 1 in
        time order, and each scene's name, the description nearest its mean."""
        cards = {c["id"]: c for c in self.cards}
        ids = [c["id"] for c in self.cards if c["id"] in vectors and c["id"] in self.scenes]
        unit = np.stack([vectors[i] / np.linalg.norm(vectors[i]) for i in ids])
        pictures = np.stack([self.frames[cards[i]["windows"]].mean(axis=0) for i in ids])
        pictures /= np.linalg.norm(pictures, axis=1, keepdims=True)
        groups = [part for group in _linked(unit @ unit.T, list(range(len(ids))), SCENE_ALIKE)
                  for part in _linked(pictures @ pictures.T, group, SCENE_PICTURE)]
        groups.sort(key=min)
        scene_of, names = {}, {}
        for n, members in enumerate(groups, 1):
            mean = unit[members].mean(axis=0)
            names[n] = self.scenes[ids[max(members, key=lambda k: float(unit[k] @ mean))]][:SCENE_CHARS]
            for k in members:
                scene_of[ids[k]] = n
        return scene_of, names

    def scene_likeness(self, vectors: dict, colours: dict) -> dict[int, dict[int, float]]:
        """How alike each two scenes are (SCENE_NEAR), from their cards' place descriptions'
        vectors and colours (average linkage of each), each ranked among the pairs of scenes
        not all of titles (self.texts), and the two ranks averaged; rounded to two places."""
        members: dict[int, list[str]] = {}
        for card, scene in sorted(self.scene_of.items()):
            if card not in self.texts and card in vectors and card in colours:
                members.setdefault(scene, []).append(card)
        scenes = sorted(members)
        pairs = list(itertools.combinations(scenes, 2))
        if not pairs:
            return {}
        unit = {i: vectors[i] / np.linalg.norm(vectors[i]) for cards in members.values() for i in cards}

        def linked(feature):
            return np.array([np.mean([float(feature[a] @ feature[b]) for a in members[s] for b in members[t]])
                             for s, t in pairs])

        scores = (_ranks(linked(unit)) + _ranks(linked(colours))) / 2
        out: dict[int, dict[int, float]] = {}
        for (s, t), v in zip(pairs, scores, strict=True):
            out.setdefault(s, {})[t] = out.setdefault(t, {})[s] = round(float(v), 2)
        return out

    @staticmethod
    def _skipped_problems(skipped, used: set, cards: dict) -> list[str]:
        if not isinstance(skipped, list) or len(skipped) > CARDS_MAX:
            return [f"skipped 要是最多 {CARDS_MAX} 项的列表"]
        problems, seen = [], set()
        for n, entry in enumerate(skipped, 1):
            card = entry.get("card") if isinstance(entry, dict) else None
            why = entry.get("why") if isinstance(entry, dict) else None
            if card not in cards:
                problems.append(f"skipped 第 {n} 项的 card 不是检索结果里的镜头卡片")
            elif card in used:
                problems.append(f"skipped 第 {n} 项的 {card} 已经用在计划里")
            elif card in seen:
                problems.append(f"skipped 里 {card} 出现了不止一次")
            if not isinstance(why, str) or not 1 <= len(why.strip()) <= WHY_CHARS:
                problems.append(f"skipped 第 {n} 项的 why 要用一句话写为什么不用，1–{WHY_CHARS} 个字符")
            seen.add(card)
        return problems

    def plan(self, probes: dict[str, np.ndarray], seconds: float, videos=None, when=(), look=None) -> list[dict]:
        """A plan with no model. The target is an upper bound: only cards at or over
        THRESHOLD, best first, and when they run out the cut is shorter. Each card gives
        one segment grown from its best window over neighbours also over THRESHOLD and
        not flat, at most CLIP_MAX; a segment showing the same picture as one taken from the same shot is
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
            cap = min(left, CLIP_MAX)
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
            if length > min(left, CLIP_MAX) + 2:
                return windows[a:b + 1]
            a, b = na, nb

    def span(self, card_id: str, probe: np.ndarray, mode: str, taken=()) -> tuple[float, float] | None:
        """A segment of the card set by the program rather than the planner (#350): the card's
        window probe scores best, grown over the neighbours in its shot, the closer side first,
        while each scores within SPAN_DROP of that window ("probe") or its picture is STILL alike
        to that window's ("frame"), up to CLIP_MAX, and at least SEGMENT_MIN. Flat windows and
        those of taken, the (video, start, end) already in the plan, are left out; None when
        too few are left."""
        if mode not in SPANS:
            raise ValueError(f"mode 要是 {'、'.join(SPANS)} 之一")
        card = next(c for c in self.cards if c["id"] == card_id)
        video, shot_index = card["video"], int(card_id.rsplit("-", 2)[1])
        lo, hi = card["shot"]
        shot = sorted((i for i, row in enumerate(self.rows) if row[0] == video and self.shot_of[i] == shot_index),
                      key=lambda i: self.rows[i][1])
        free = {i for i in shot if not self.flat[i] and not any(
            v == video and self.rows[i][1] < e and s < self.rows[i][2] for v, s, e in taken)}
        scores = self.frames @ probe
        mine = [i for i in card["windows"] if i in free]
        if not mine:
            return None
        best = max(mine, key=lambda i: scores[i])
        near, floor = (scores, scores[best] - SPAN_DROP) if mode == "probe" else (self.frames @ self.frames[best], STILL)
        a = b = shot.index(best)
        while True:
            sides = [k for k in (a - 1, b + 1) if 0 <= k < len(shot) and shot[k] in free and near[shot[k]] >= floor]
            if not sides:
                break
            k = max(sides, key=lambda k: near[shot[k]])
            na, nb = min(a, k), max(b, k)
            if min(hi, self.rows[shot[nb]][2]) - max(lo, self.rows[shot[na]][1]) > CLIP_MAX + ROUNDING:
                break
            a, b = na, nb

        def seconds(a, b):
            # Rounded inward, so that the segment stays inside its shot.
            return (math.ceil(max(lo, self.rows[shot[a]][1]) * 100) / 100,
                    math.floor(min(hi, self.rows[shot[b]][2]) * 100) / 100)

        # A window cut short by its shot's edge is under SEGMENT_MIN: free neighbours join
        # however they score, the closer first; a shot too short for that has no segment.
        while seconds(a, b)[1] - seconds(a, b)[0] < SEGMENT_MIN:
            sides = [k for k in (a - 1, b + 1) if 0 <= k < len(shot) and shot[k] in free]
            if not sides:
                return None
            k = max(sides, key=lambda k: near[shot[k]])
            a, b = min(a, k), max(b, k)
        return seconds(a, b)

    def fill(self, segments: list, mode: str, target: float, at, caps=None, mins=None,
             shares=None, floor=None) -> tuple[list, list[str]]:
        """Seconds for segments, each (card id, probe vector), set by the program to add up to
        target (#350). A segment starts as the window of its card the probe matches best, clear
        of flat windows and of the segments before it. Then, STEP at a time, the segment end whose
        next step is best grows, first among the segments short of their share of target
        (shares[n], an even share by default) and then among all, until the cut reaches target;
        over it, the worst end steps go.
        A step is judged on its middle frame, at(video, seconds) as a unit vector, against the
        segment's own peak frame: by how little its probe score falls from the peak's ("probe"),
        or by its likeness to the peak picture ("frame"), so that segments whose probes score on
        other scales are compared fairly. Steps stay in their shot, off flat windows and the
        other segments, and within caps[n] (CLIP_MAX by default); a segment keeps mins[n]
        (SEGMENT_MIN by default). A segment also keeps to its card, and to the windows next to it
        in its shot that are STILL alike to the card (region), and takes no step whose frame
        is less than FLOOR alike to its peak frame: the time it cannot take goes to the others.
        An end inside speech (self.words) then moves to the nearest pause that keeps those rules,
        in or out: out by at most SNAP, and a line may take a segment SNAP past its cap, never
        past CLIP_MAX; in as far as the segment's minimum allows. With floor, a segment that cannot
        reach mins[n] keeps what it reaches when that is floor or more, and that is its minimum.
        Returns [(start, end) or None] and the problems."""
        if mode not in SPANS:
            raise ValueError(f"mode 要是 {'、'.join(SPANS)} 之一")
        cards = {c["id"]: c for c in self.cards}
        caps = caps or [CLIP_MAX] * len(segments)
        mins = list(mins or [SEGMENT_MIN] * len(segments))
        spans, problems, peaks, regions = [], [], [], []
        for n, (card_id, probe) in enumerate(segments):
            card = cards[card_id]
            lo, hi = self.region(card)
            regions.append((lo, hi))
            free = [i for i in card["windows"] if not self.flat[i] and not self._taken(
                card["video"], max(lo, self.rows[i][1]), min(hi, self.rows[i][2]), spans)]
            if not free:
                spans.append(None)
                peaks.append(None)
                problems.append(f"第 {n + 1} 段的 {card_id} 已没有可用的画面：前面的段用了它，或只剩纯色画面")
                continue
            best = max(free, key=lambda i: float(self.frames[i] @ probe))
            spans.append([card["video"], max(lo, self.rows[best][1]), min(hi, self.rows[best][2])])
            peaks.append(at(card["video"], self.frame_ms[best] / 1000))

        def judged(n, t):
            seen = at(spans[n][0], t)
            if mode == "frame":
                return float(seen @ peaks[n])
            return float(seen @ segments[n][1]) - float(peaks[n] @ segments[n][1])

        def grown(n):
            """Each step that may join segment n: (score, n, side, new start, new end)."""
            video, start, end = spans[n]
            lo, hi = regions[n]
            out = []
            for side, a, b in (("start", max(lo, start - STEP), start), ("end", end, min(hi, end + STEP))):
                if b - a < 0.01 or (end - start) + (b - a) > caps[n] + ROUNDING or self._flat_in(video, a, b) \
                        or self._taken(video, a, b, spans, n) or float(at(video, (a + b) / 2) @ peaks[n]) < FLOOR:
                    continue
                out.append((judged(n, (a + b) / 2), n, side, a if side == "start" else start,
                            end if side == "start" else b))
            return out

        def length(n):
            return spans[n][2] - spans[n][1] if spans[n] else 0.0

        # A window cut short by its shot's edge first grows to its minimum, however it scores.
        for n in range(len(spans)):
            while spans[n] and length(n) < mins[n]:
                steps = grown(n)
                if not steps and floor is not None and length(n) >= floor - ROUNDING:
                    mins[n] = length(n)
                    break
                if not steps:
                    problems.append(f"第 {n + 1} 段的 {segments[n][0]} 剪不出 {mins[n]:g} 秒：镜头太短或被前后的段挡住")
                    spans[n] = None
                    break
                _score, _n, _side, spans[n][1], spans[n][2] = max(steps)
        # Each segment first grows towards an even share of the target, its best end first, so
        # that a still picture, whose score hardly falls, does not take the others' time; what
        # is left of the target then goes to the best steps of any.
        shares = shares or [target / max(1, sum(1 for s in spans if s))] * len(segments)
        for even in (True, False):
            while sum(map(length, range(len(spans)))) < target - STEP / 2:
                steps = [step for n in range(len(spans)) if spans[n] and (not even or length(n) < shares[n] - STEP / 2)
                         for step in grown(n)]
                if not steps:
                    break
                _score, n, _side, start, end = max(steps)
                spans[n][1:] = [start, end]
        while sum(map(length, range(len(spans)))) > target + STEP / 2:
            ends = []
            for n in range(len(spans)):
                if not spans[n] or length(n) - STEP < mins[n]:
                    continue
                video, start, end = spans[n]
                ends += [(judged(n, start + STEP / 2), n, start + STEP, end),
                         (judged(n, end - STEP / 2), n, start, end - STEP)]
            if not ends:
                break
            _score, n, start, end = min(ends)
            spans[n][1:] = [start, end]
        out = []
        for n, span in enumerate(spans):
            if span is None:
                out.append(None)
                continue
            video, start, end = span
            lo, hi = regions[n]
            # A line may take a segment past its cap, never past CLIP_MAX.
            longest = min(caps[n] + SNAP, CLIP_MAX)
            fits = functools.partial(self._fits, video, (lo, hi), (mins[n], longest), spans, n)
            start = self._snapped(video, start, (start - SNAP, end - mins[n]), lambda t, e=end, ok=fits: ok(t, e))
            end = self._snapped(video, end, (start + mins[n], end + SNAP), lambda t, s=start, ok=fits: ok(s, t))
            spans[n] = [video, start, end]
            # Rounded inward, so that the segment stays inside its shot.
            out.append((math.ceil(start * 100) / 100, math.floor(end * 100) / 100))
        return out, problems

    def usable(self, card: dict) -> float:
        """The most seconds a segment of card can run (Index.fill): its region less its flat
        windows, at most CLIP_MAX, down to the half second."""
        lo, hi = self.region(card)
        flat = sum(max(0.0, min(hi, row[2]) - max(lo, row[1])) for i, row in enumerate(self.rows)
                   if self.flat[i] and row[0] == card["video"])
        return math.floor(min(CLIP_MAX, hi - lo - flat) * 2) / 2

    def region(self, card: dict) -> tuple[float, float]:
        """The seconds a segment of card may run: the card, and the windows next to it in its
        shot whose pictures are STILL alike to the card's mean, up to a window that is not."""
        lo, hi = card["shot"]
        mean = self.frames[card["windows"]].mean(axis=0)
        mean /= np.linalg.norm(mean)
        shot = sorted((i for i, row in enumerate(self.rows) if row[0] == card["video"]
                       and self.shot_of[i] == int(card["id"].rsplit("-", 2)[1])), key=lambda i: self.rows[i][1])
        mine = [shot.index(i) for i in card["windows"]]
        a, b = min(mine), max(mine)
        while a > 0 and float(self.frames[shot[a - 1]] @ mean) >= STILL:
            a -= 1
        while b + 1 < len(shot) and float(self.frames[shot[b + 1]] @ mean) >= STILL:
            b += 1
        start = lo if a == 0 else max(lo, min(card["start"], self.rows[shot[a]][1]))
        end = hi if b == len(shot) - 1 else min(hi, max(card["end"], self.rows[shot[b]][2]))
        return start, end

    def _fits(self, video, shot, length, spans, n, a, b) -> bool:
        """Whether segment n may run a-b: inside its region (shot), length[0] to length[1] long,
        clear of flat windows and of the other segments."""
        return (shot[0] <= a and b <= shot[1] and length[0] <= b - a <= length[1]
                and not self._flat_in(video, a, b) and not self._taken(video, a, b, spans, n))

    def _taken(self, video, a, b, spans, skip=None) -> bool:
        """Whether a-b overlaps a segment of spans, ([video, start, end] or None), but skip's."""
        return any(k != skip and s is not None and s[0] == video and s[1] < b - 0.005 and a + 0.005 < s[2]
                   for k, s in enumerate(spans))

    def _flat_in(self, video, a, b) -> bool:
        return any(self.flat[i] and row[0] == video and row[1] < b and a < row[2] for i, row in enumerate(self.rows))

    def _snapped(self, video, t, reach, allowed) -> float:
        """t, or when it falls inside speech the nearest pause in reach that allowed(pause) passes."""
        words = sorted(self.words.get(video, []))
        if not any(a + 0.05 < t < b - 0.05 for a, b in words) and not any(
                b <= t <= words[k + 1][0] and words[k + 1][0] - b < PAUSE for k, (_a, b) in enumerate(words[:-1])):
            return t
        pauses = [(b + c) / 2 for (_a, b), (c, _d) in itertools.pairwise(words) if c - b >= PAUSE]
        if words:
            pauses += [words[0][0] - PAUSE / 2, words[-1][1] + PAUSE / 2]
        near = sorted((p for p in pauses if reach[0] <= p <= reach[1] and allowed(p)),
                      key=lambda p: abs(p - t))
        return near[0] if near else t

    def _shot_windows(self, card: dict) -> list[int]:
        lo, hi = card["shot"]
        return [i for i, (video, start, end) in enumerate(self.rows)
                if video == card["video"] and start < hi and lo < end]
