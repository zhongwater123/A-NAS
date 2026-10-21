"""Measures how well a speaker model recognises a voice from one recording in another.

Development only: the voiceprint evaluation behind docs/research/voiceprint-evaluation.md.
AISHELL-4's test meetings are far-field recordings of the same groups of
people in the same rooms on different days; a speaker's label (such as
"004-M") names the same person in every session of a group (L_R003S01 and
L_R003S02). Channel 0 of the eight-microphone array stands in for a phone on
the table.

Each speaker in each session gets a profile the way the Product Service
builds one (internal/voice): their stretches of speech that overlap nobody
else's, 1 to 15 s each, longest first until the budget is spent, embedded by
the voice Worker's provider (anas_ai.voice.SpeakerProvider) and averaged by
length. A profile from one session is then matched against the profiles of
the other session of every group:

- pairs: every cross-session pair of profiles, genuine when group and label
  agree; the equal error rate and the false accept rate at each threshold;
- naming: the library holds every profile of the other session; a probe is
  named after its best match when that scores at least the threshold, and
  the naming is correct when it is the same person;
- strangers: the probe's own profile is left out of the library, so any
  name it gets is wrong; this is the speaker nobody has named yet.

Some people took part in more than one group: their labels differ but their
profiles agree in every session (the report lists the closest such pairs), so
pairs across groups are not certainly different people. The household view
therefore counts only what is certain, and is what a member's library looks
like: within one group, the same label in the other session is the same
person and any other label is someone else; the library is the other session
of the same group, and the strangers are the group's people not yet named.
Different labels within one session are the voices a long recording must not
join across its pieces.

The budgets 120 s, 30 s and 10 s cover a speaker who talks through a meeting
and one who says a few sentences.

With --halves, each speaker gets one profile from the first half of a session
and one from the second, and the second half's voices are joined to the
first's the way the Product Service joins the pieces of a long recording
(internal/voice identify): each goes to the best-scoring voice of the other
half that scores at least the threshold and has not been taken. A join to
another label is a wrong join; a voice left apart while its own label is in
the other half is a split.

    python -m eval.voiceprints --model /models/3dspeaker_speech_campplus_sv_zh-cn_16k-common.onnx \\
        --data /datasets/aishell4-test --out /datasets/runs/voiceprints-campplus-zh.json
    python -m eval.voiceprints --profiles /datasets/runs/voiceprints-campplus-zh.npz --out report.json
    python -m eval.voiceprints --halves --model ... --data ... --out halves.json
"""

import argparse
import hashlib
import json
import os
import resource
import tempfile
import time
from collections import defaultdict

import numpy as np
import soundfile

from anas_ai import voice

RATE = voice.SAMPLE_RATE
MIN_STRETCH = 1.0
MAX_STRETCH = 15.0
BUDGETS = (120, 30, 10)
THRESHOLDS = [round(0.20 + 0.02 * i, 2) for i in range(31)]


def read_rttm(path):
    """(label, start, end) of each turn, in seconds."""
    turns = []
    with open(path, encoding="utf-8") as file:
        for line in file:
            fields = line.split()
            if len(fields) >= 8 and fields[0] == "SPEAKER":
                start, duration = float(fields[3]), float(fields[4])
                turns.append((fields[7], start, start + duration))
    return turns


def clean_stretches(turns, label):
    """The label's speech that overlaps no other speaker, as (start, end)."""
    others = sorted((start, end) for other, start, end in turns if other != label)
    stretches = []
    for own, start, end in sorted(turns, key=lambda turn: turn[1]):
        if own != label:
            continue
        pieces = [(start, end)]
        for other_start, other_end in others:
            if other_end <= start or other_start >= end:
                continue
            cut = []
            for piece_start, piece_end in pieces:
                if other_end <= piece_start or other_start >= piece_end:
                    cut.append((piece_start, piece_end))
                    continue
                if other_start > piece_start:
                    cut.append((piece_start, other_start))
                if other_end < piece_end:
                    cut.append((other_end, piece_end))
            pieces = cut
        stretches.extend(piece for piece in pieces if piece[1] - piece[0] >= MIN_STRETCH)
    return stretches


def budgeted(stretches, budget):
    """Longest first, each at most MAX_STRETCH, until budget seconds."""
    chosen, total = [], 0.0
    for start, end in sorted(stretches, key=lambda piece: piece[1] - piece[0], reverse=True):
        if total >= budget:
            break
        length = min(end - start, MAX_STRETCH, budget - total)
        if length < MIN_STRETCH:
            break
        chosen.append((start, start + length))
        total += length
    return chosen, total


def channel_zero(path, cache=None):
    """The first channel of a recording as int16, read in blocks; with cache,
    kept there as .npy so the next model reads it at once."""
    kept = os.path.join(cache, os.path.basename(path) + ".ch0.npy") if cache else None
    if kept and os.path.exists(kept):
        return np.load(kept)
    samples = read_channel_zero(path)
    if kept:
        os.makedirs(cache, exist_ok=True)
        np.save(kept + ".part.npy", samples)
        os.replace(kept + ".part.npy", kept)
    return samples


def read_channel_zero(path):
    with soundfile.SoundFile(path) as file:
        if file.samplerate != RATE:
            raise SystemExit(f"{path} is {file.samplerate} Hz, not {RATE}")
        blocks = [block[:, 0].copy() if block.ndim == 2 else block for block in
                  file.blocks(blocksize=RATE * 60, dtype="int16", always_2d=True)]
    return np.concatenate(blocks)


def manifest_for(model):
    digest = hashlib.sha256()
    with open(model, "rb") as file:
        for chunk in iter(lambda: file.read(1 << 20), b""):
            digest.update(chunk)
    name = os.path.splitext(os.path.basename(model))[0]
    manifest = {"name": name, "kind": "3d-speaker", "file": os.path.basename(model),
                "sizeBytes": os.path.getsize(model), "sha256": digest.hexdigest()}
    handle, path = tempfile.mkstemp(suffix=".json")
    with os.fdopen(handle, "w", encoding="utf-8") as out:
        json.dump(manifest, out)
    return path


def profiles(provider, data, sessions, cache=None):
    """{budget: {(session, label): (vector, seconds)}} and the embedding time."""
    found = {budget: {} for budget in BUDGETS}
    spent, audio = 0.0, 0.0
    for session in sessions:
        turns = read_rttm(os.path.join(data, "TextGrid", f"{session}.rttm"))
        samples = channel_zero(os.path.join(data, "wav", f"{session}.flac"), cache)
        for label in sorted({turn[0] for turn in turns}):
            stretches = clean_stretches(turns, label)
            for budget in BUDGETS:
                chosen, total = budgeted(stretches, budget)
                if total < min(budget, 5):
                    continue
                started = time.perf_counter()
                members = [(provider.embed(samples[int(start * RATE):int(end * RATE)]), int((end - start) * RATE))
                           for start, end in chosen]
                spent += time.perf_counter() - started
                audio += total
                found[budget][(session, label)] = (np.asarray(voice.weighted_mean(members), np.float32), total)
        print(f"{session}: {len({turn[0] for turn in turns})} speakers", flush=True)
    return found, spent, audio


def half_profiles(provider, data, sessions, cache=None):
    """{budget: {(session, label, half): vector}} from the stretches that lie
    wholly in each half of a session."""
    found = {budget: {} for budget in BUDGETS}
    for session in sessions:
        turns = read_rttm(os.path.join(data, "TextGrid", f"{session}.rttm"))
        samples = channel_zero(os.path.join(data, "wav", f"{session}.flac"), cache)
        middle = len(samples) / RATE / 2
        for label in sorted({turn[0] for turn in turns}):
            stretches = clean_stretches(turns, label)
            for half, part in enumerate(([s for s in stretches if s[1] <= middle], [s for s in stretches if s[0] >= middle])):
                for budget in BUDGETS:
                    chosen, total = budgeted(part, budget)
                    if total < min(budget, 5):
                        continue
                    members = [(provider.embed(samples[int(start * RATE):int(end * RATE)]), int((end - start) * RATE))
                               for start, end in chosen]
                    found[budget][(session, label, half)] = np.asarray(voice.weighted_mean(members), np.float32)
        print(f"{session}: halves done", flush=True)
    return found


def join_halves(profiles_of_budget):
    """Same-label and different-label scores across the halves of a session,
    and the wrong joins and splits of the Product Service's greedy join."""
    sessions = sorted({key[0] for key in profiles_of_budget})
    genuine, impostor = [], []
    for session in sessions:
        first = {key[1]: vector for key, vector in profiles_of_budget.items() if key[0] == session and key[2] == 0}
        second = {key[1]: vector for key, vector in profiles_of_budget.items() if key[0] == session and key[2] == 1}
        for a, x in first.items():
            for b, y in second.items():
                (genuine if a == b else impostor).append(float(x @ y))
    rows = []
    for threshold in THRESHOLDS:
        wrong = split = voices = 0
        for session in sessions:
            first = {key[1]: vector for key, vector in profiles_of_budget.items() if key[0] == session and key[2] == 0}
            second = {key[1]: vector for key, vector in profiles_of_budget.items() if key[0] == session and key[2] == 1}
            taken = set()
            for label, vector in sorted(second.items()):
                voices += 1
                best, target = threshold, None
                for other, candidate in first.items():
                    score = float(vector @ candidate)
                    if other not in taken and score >= best:
                        best, target = score, other
                if target is None:
                    split += label in first
                    continue
                taken.add(target)
                wrong += target != label
        rows.append({"threshold": threshold, "wrongJoins": wrong, "splits": split, "voices": voices})
    genuine, impostor = np.array(genuine), np.array(impostor)
    return {
        "genuinePairs": len(genuine), "impostorPairs": len(impostor),
        "genuine": {"min": float(genuine.min()), "p05": float(np.quantile(genuine, 0.05)), "median": float(np.median(genuine))},
        "impostor": {"max": float(impostor.max()), "p99": float(np.quantile(impostor, 0.99)), "median": float(np.median(impostor))},
        "thresholds": rows,
    }


def person(session, label):
    """The group of sessions (L_R003 of L_R003S01C02) and the label."""
    return session[:6], label


def evaluate(profiles_of_budget, sessions):
    keys = list(profiles_of_budget)
    genuine, impostor = [], []
    for i, a in enumerate(keys):
        for b in keys[i + 1:]:
            if a[0] == b[0]:
                continue
            score = float(profiles_of_budget[a][0] @ profiles_of_budget[b][0])
            (genuine if person(*a) == person(*b) else impostor).append(score)
    genuine, impostor = np.array(genuine), np.array(impostor)
    # Naming: probes from the second session of each group against a library
    # of every first-session profile, and the other way round.
    first = {session for session in sessions if session[6:9] == "S01"}
    rows = []
    for threshold in THRESHOLDS:
        named = correct = probes = strangers_named = strangers = 0
        for probe in keys:
            library = [key for key in keys if (key[0] in first) != (probe[0] in first)]
            known = [key for key in library if person(*key) == person(*probe)]
            if not known:
                continue
            probes += 1
            scores = sorted(((float(profiles_of_budget[key][0] @ profiles_of_budget[probe][0]), key) for key in library),
                            reverse=True)
            best, key = scores[0]
            if best >= threshold:
                named += 1
                correct += person(*key) == person(*probe)
            strangers += 1
            others = [score for score, key in scores if person(*key) != person(*probe)]
            if others and others[0] >= threshold:
                strangers_named += 1
        rows.append({
            "threshold": threshold,
            "falseAccept": float((impostor >= threshold).mean()) if len(impostor) else 0.0,
            "falseReject": float((genuine < threshold).mean()) if len(genuine) else 0.0,
            "namedPrecision": correct / named if named else 1.0,
            "namedRecall": correct / probes if probes else 0.0,
            "strangersNamed": strangers_named / strangers if strangers else 0.0,
        })
    order = np.sort(np.concatenate([genuine, impostor]))
    eer, gap = 1.0, 2.0
    for threshold in order:
        far, frr = float((impostor >= threshold).mean()), float((genuine < threshold).mean())
        if abs(far - frr) < gap:
            gap, eer = abs(far - frr), (far + frr) / 2
    return {
        "genuinePairs": len(genuine), "impostorPairs": len(impostor), "eer": eer,
        "genuine": {"min": float(genuine.min()), "median": float(np.median(genuine))},
        "impostor": {"max": float(impostor.max()), "p999": float(np.quantile(impostor, 0.999)),
                     "median": float(np.median(impostor))},
        "thresholds": rows,
    }


def provider_threads(threads):
    from anas_ai import providers

    return threads or providers.inference_threads()


def household(profiles_of_budget):
    """Scores and naming within each group only, where identities are certain."""
    keys = list(profiles_of_budget)
    vector = lambda key: profiles_of_budget[key][0]
    genuine, impostor, same_session = [], [], []
    for i, a in enumerate(keys):
        for b in keys[i + 1:]:
            if a[0][:6] != b[0][:6]:
                continue
            score = float(vector(a) @ vector(b))
            if a[0] == b[0]:
                same_session.append(score)
            elif a[1] == b[1]:
                genuine.append(score)
            else:
                impostor.append(score)
    genuine, impostor, same_session = np.array(genuine), np.array(impostor), np.array(same_session)
    rows = []
    for threshold in THRESHOLDS:
        named = correct = probes = strangers_named = 0
        for probe in keys:
            library = [key for key in keys if key[0][:6] == probe[0][:6] and key[0] != probe[0]]
            if not any(key[1] == probe[1] for key in library):
                continue
            probes += 1
            scores = sorted(((float(vector(key) @ vector(probe)), key) for key in library), reverse=True)
            best, key = scores[0]
            if best >= threshold:
                named += 1
                correct += key[1] == probe[1]
            others = [score for score, key in scores if key[1] != probe[1]]
            if others and others[0] >= threshold:
                strangers_named += 1
        rows.append({
            "threshold": threshold,
            "falseAccept": float((impostor >= threshold).mean()),
            "falseAccepts": int((impostor >= threshold).sum()),
            "sameRecordingJoined": int((same_session >= threshold).sum()),
            "falseReject": float((genuine < threshold).mean()),
            "namedPrecision": correct / named if named else 1.0,
            "namedRecall": correct / probes if probes else 0.0,
            "strangersNamed": strangers_named / probes if probes else 0.0,
        })
    return {
        "genuinePairs": len(genuine), "impostorPairs": len(impostor), "sameRecordingPairs": len(same_session),
        "genuine": {"min": float(genuine.min()), "p05": float(np.quantile(genuine, 0.05)), "median": float(np.median(genuine))},
        "impostor": {"max": float(impostor.max()), "p99": float(np.quantile(impostor, 0.99)), "median": float(np.median(impostor))},
        "sameRecording": {"max": float(same_session.max()), "median": float(np.median(same_session))},
        "thresholds": rows,
    }


def closest_across_groups(profiles_of_budget, count=8):
    """The pairs of labels from different groups whose profiles agree most,
    with the lowest of their cross-session scores."""
    by_person = {}
    for key, (vector, _) in profiles_of_budget.items():
        by_person.setdefault(person(*key), []).append((key, vector))
    pairs = []
    people = sorted(by_person)
    for i, a in enumerate(people):
        for b in people[i + 1:]:
            if a[0] == b[0]:
                continue
            scores = [float(x @ y) for ka, x in by_person[a] for kb, y in by_person[b] if ka[0] != kb[0]]
            if scores:
                pairs.append({"a": "/".join(a), "b": "/".join(b), "mean": float(np.mean(scores)), "min": min(scores)})
    return sorted(pairs, key=lambda pair: -pair["mean"])[:count]


def load_profiles(path):
    """Profiles saved by an earlier run, {budget: {(session, label): (vector, 0)}}."""
    data = np.load(path)
    found = {budget: {} for budget in BUDGETS}
    for key in data.files:
        budget, session, label = key.split("/")
        found[int(budget)][(session, label)] = (data[key], 0)
    return found


def summarise(report):
    for budget in BUDGETS:
        result = report["budgets"][str(budget)]
        home = result["household"]
        print(f"budget {budget:>3} s: {result['profiles']} profiles; household: genuine min {home['genuine']['min']:.3f}, "
              f"impostor max {home['impostor']['max']:.3f}, same recording max {home['sameRecording']['max']:.3f}")
        for row in home["thresholds"]:
            if row["threshold"] in (0.5, 0.56, 0.6, 0.66, 0.7, 0.72, 0.74, 0.76, 0.8):
                print(f"  t={row['threshold']:.2f} FAR {row['falseAccept']:.4f} ({row['falseAccepts']}) FRR {row['falseReject']:.3f} "
                      f"named P {row['namedPrecision']:.3f} R {row['namedRecall']:.3f} strangers {row['strangersNamed']:.3f} "
                      f"joined {row['sameRecordingJoined']}")


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--model")
    parser.add_argument("--data", help="the AISHELL-4 test subset: wav/ and TextGrid/")
    parser.add_argument("--profiles", help="evaluate the profiles an earlier run saved instead")
    parser.add_argument("--out", required=True)
    parser.add_argument("--threads", type=int)
    parser.add_argument("--cache", help="a directory to keep each session's first channel in")
    parser.add_argument("--halves", action="store_true", help="join the halves of each session instead")
    args = parser.parse_args()
    if args.profiles:
        found = load_profiles(args.profiles)
        sessions = sorted({key[0] for key in found[BUDGETS[0]]})
        report = {"profiles": args.profiles, "sessions": sessions, "budgets": {}}
        for budget in BUDGETS:
            report["budgets"][str(budget)] = {"profiles": len(found[budget]), **evaluate(found[budget], sessions),
                                              "household": household(found[budget]),
                                              "closestAcrossGroups": closest_across_groups(found[budget])}
        with open(args.out, "w", encoding="utf-8") as out:
            json.dump(report, out, ensure_ascii=False, indent=1)
        summarise(report)
        return
    if not args.model or not args.data:
        parser.error("give --model and --data, or --profiles")
    sessions = sorted(name[:-5] for name in os.listdir(os.path.join(args.data, "wav")) if name.endswith(".flac"))
    provider = voice.SpeakerProvider(args.model, manifest_for(args.model), args.threads)
    if args.halves:
        found = half_profiles(provider, args.data, sessions, args.cache)
        report = {"model": provider.model, "sessions": sessions,
                  "budgets": {str(budget): join_halves(found[budget]) for budget in BUDGETS}}
        with open(args.out, "w", encoding="utf-8") as out:
            json.dump(report, out, ensure_ascii=False, indent=1)
        for budget in BUDGETS:
            result = report["budgets"][str(budget)]
            print(f"budget {budget:>3} s: same voice min {result['genuine']['min']:.3f} p05 {result['genuine']['p05']:.3f}, "
                  f"other voices max {result['impostor']['max']:.3f}")
            for row in result["thresholds"]:
                if 0.6 <= row["threshold"] <= 0.84:
                    print(f"  t={row['threshold']:.2f} wrong joins {row['wrongJoins']} splits {row['splits']} of {row['voices']}")
        return
    found, spent, audio = profiles(provider, args.data, sessions, args.cache)
    report = {"model": provider.model, "sessions": sessions, "embedSeconds": round(spent, 1), "audioSeconds": round(audio, 1),
              "peakRSSMiB": round(resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1024), "threads": provider_threads(args.threads),
              "budgets": {}}
    for budget in BUDGETS:
        report["budgets"][str(budget)] = {"profiles": len(found[budget]), **evaluate(found[budget], sessions),
                                          "household": household(found[budget]),
                                          "closestAcrossGroups": closest_across_groups(found[budget])}
    with open(args.out, "w", encoding="utf-8") as out:
        json.dump(report, out, ensure_ascii=False, indent=1)
    # The profiles themselves, for looking at single pairs afterwards.
    np.savez(os.path.splitext(args.out)[0] + ".npz", **{
        f"{budget}/{session}/{label}": vector for budget in BUDGETS for (session, label), (vector, _) in found[budget].items()})
    print(f"{provider.model}: {audio:.0f} s of speech embedded in {spent:.1f} s with {report['threads']} threads, peak RSS {report['peakRSSMiB']} MiB")
    for budget in BUDGETS:
        result = report["budgets"][str(budget)]
        print(f"budget {budget:>3} s: {result['profiles']} profiles, EER {result['eer']:.3f}, "
              f"genuine min {result['genuine']['min']:.3f}, impostor max {result['impostor']['max']:.3f}")
        for row in result["thresholds"]:
            if row["threshold"] in (0.4, 0.5, 0.56, 0.6, 0.66, 0.7):
                print(f"  t={row['threshold']:.2f} FAR {row['falseAccept']:.4f} FRR {row['falseReject']:.3f} "
                      f"named P {row['namedPrecision']:.3f} R {row['namedRecall']:.3f} strangers named {row['strangersNamed']:.3f}")


if __name__ == "__main__":
    main()
