"""Clusters face and pet vectors as the photo service will, and scores the result.

Development only: the evaluation behind docs/architecture/photo-people.md.
The photo service clusters each library's occurrences (faces, cats or dogs)
incrementally as they arrive. The candidates here are the Immich-style rule
the plan describes, with the product's own constraints:

- An occurrence joins a person when an already assigned occurrence within the
  similarity threshold belongs to it ("nearest") or when most of its assigned
  neighbours do ("majority").
- With no person nearby, it founds one when it has at least min_core - 1
  unassigned neighbours (itself included, min_core occurrences), and those
  neighbours join it.
- Two occurrences of the same photo never share a person.
- Whatever stays unassigned is tried once more after the pass, as the service
  will on its periodic tidy-up.

Precision comes first: a person split in two is merged by the user in one
step; two people merged into one mixes family members up.
"""

import collections
import itertools
import math

import numpy as np


class Occurrence:
    """One face or pet in one photo, with the truth the evaluation knows."""

    __slots__ = ("key", "photo", "identity", "vector", "group")

    def __init__(self, key, photo, identity, vector, group=""):
        self.key = key
        self.photo = photo
        self.identity = identity
        self.vector = vector
        # A label the report breaks results down by (child, relative...).
        self.group = group


def cluster(occurrences, threshold, min_core=3, assign="nearest", retry=True):
    """The person index of each occurrence in arrival order, or -1 for unassigned."""
    count = len(occurrences)
    if count == 0:
        return []
    vectors = np.stack([o.vector for o in occurrences]).astype(np.float32)
    person = np.full(count, -1, dtype=np.int64)
    photos_of = []  # person -> set of photos it already holds

    def free_of(p, photo):
        return photo not in photos_of[p]

    def join(i, p):
        person[i] = p
        photos_of[p].add(occurrences[i].photo)

    def place(i, seen):
        """Gives occurrence i a person among the first `seen` occurrences, if it earns one."""
        if seen == 0:
            return False
        similarity = vectors[:seen] @ vectors[i]
        near = np.flatnonzero(similarity >= threshold)
        near = [j for j in near if j != i and occurrences[j].photo != occurrences[i].photo]
        if not near:
            return False
        photo = occurrences[i].photo
        assigned = [j for j in near if person[j] >= 0 and free_of(person[j], photo)]
        if assigned:
            if assign == "nearest":
                best = max(assigned, key=lambda j: similarity[j])
                join(i, person[best])
                return True
            votes = collections.Counter(person[j] for j in assigned)
            top, top_votes = votes.most_common(1)[0]
            # A tie between two people is not a majority: leave it to the user.
            if len(votes) > 1 and votes.most_common(2)[1][1] == top_votes:
                return False
            join(i, top)
            return True
        unassigned = [j for j in near if person[j] < 0]
        # One per photo: the founding members must be distinct photos.
        members, used = [], {photo}
        for j in sorted(unassigned, key=lambda j: -similarity[j]):
            if occurrences[j].photo not in used:
                members.append(j)
                used.add(occurrences[j].photo)
        if len(members) + 1 < min_core:
            return False
        photos_of.append(set())
        p = len(photos_of) - 1
        join(i, p)
        for j in members:
            join(j, p)
        return True

    for i in range(count):
        place(i, i)
    if retry:
        for i in range(count):
            if person[i] < 0:
                place(i, count)
    return person.tolist()


def pairs(n):
    return n * (n - 1) // 2


def score(occurrences, person):
    """Pairwise precision and recall, coverage and how people were split or mixed."""
    by_person = collections.defaultdict(list)
    for o, p in zip(occurrences, person):
        if p >= 0:
            by_person[p].append(o.identity)
    identities = collections.Counter(o.identity for o in occurrences)
    same_pairs = sum(pairs(n) for n in identities.values())
    clustered_pairs = right_pairs = 0
    mixed = 0
    for members in by_person.values():
        counts = collections.Counter(members)
        clustered_pairs += pairs(len(members))
        right_pairs += sum(pairs(n) for n in counts.values())
        if len(counts) > 1:
            mixed += 1
    # How many people each identity ended up as, among those clustered at all.
    split = collections.defaultdict(set)
    for o, p in zip(occurrences, person):
        if p >= 0:
            split[o.identity].add(p)
    assigned = sum(1 for p in person if p >= 0)
    return summary({
        "occurrences": len(occurrences),
        "identities": len(identities),
        "people": len(by_person),
        "assigned": assigned,
        "clusteredPairs": clustered_pairs,
        "rightPairs": right_pairs,
        "samePairs": same_pairs,
        "mixedPeople": mixed,
        "splitIdentities": len(split),
        "identityPeople": sum(len(s) for s in split.values()),
    })


COUNTS = ("occurrences", "identities", "people", "assigned", "clusteredPairs", "rightPairs", "samePairs",
          "mixedPeople", "splitIdentities", "identityPeople")


def summary(counts):
    """The rates of a score's counts; sums of several libraries' counts work too."""
    found = {key: counts[key] for key in COUNTS}
    found["precision"] = counts["rightPairs"] / counts["clusteredPairs"] if counts["clusteredPairs"] else 1.0
    found["recall"] = counts["rightPairs"] / counts["samePairs"] if counts["samePairs"] else 0.0
    found["coverage"] = counts["assigned"] / counts["occurrences"] if counts["occurrences"] else 0.0
    found["peoplePerIdentity"] = counts["identityPeople"] / counts["splitIdentities"] if counts["splitIdentities"] else 0.0
    return found


def total(scores):
    """One score over several libraries: counts add up, rates are recomputed."""
    return summary({key: sum(s[key] for s in scores) for key in COUNTS})


def by_group(occurrences, person):
    """score() restricted to each group's occurrences, for children, relatives and so on."""
    groups = collections.defaultdict(list)
    for o, p in zip(occurrences, person):
        if o.group:
            groups[o.group].append((o, p))
    return {name: score([o for o, _ in found], [p for _, p in found]) for name, found in sorted(groups.items())}


def verification(occurrences, max_pairs=2_000_000, seed=0):
    """Similarities of same-identity and different-identity pairs across photos."""
    rng = np.random.default_rng(seed)
    vectors = np.stack([o.vector for o in occurrences]).astype(np.float32)
    by_identity = collections.defaultdict(list)
    for i, o in enumerate(occurrences):
        by_identity[o.identity].append(i)
    same = []
    for members in by_identity.values():
        for a, b in itertools.combinations(members, 2):
            if occurrences[a].photo != occurrences[b].photo:
                same.append(float(vectors[a] @ vectors[b]))
    count = len(occurrences)
    draws = min(max_pairs, max(10 * len(same), 10_000))
    a = rng.integers(0, count, draws)
    b = rng.integers(0, count, draws)
    keep = [k for k in range(draws) if occurrences[a[k]].identity != occurrences[b[k]].identity]
    different = np.einsum("ij,ij->i", vectors[a[keep]], vectors[b[keep]]).tolist()
    return np.asarray(same), np.asarray(different)


def threshold_for_far(different, far):
    """The lowest similarity whose false-accept rate on different pairs is at most far."""
    if len(different) == 0:
        return math.nan
    ordered = np.sort(different)[::-1]
    allowed = int(far * len(ordered))
    return float(ordered[allowed]) + 1e-6
