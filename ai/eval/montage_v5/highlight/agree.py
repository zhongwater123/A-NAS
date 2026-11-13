"""How well model marks agree with the user's: the 4B's four levels from rate_4b.py, beat by beat, and, at no
extra cost, what the 第六版 descriptions already hold: the 4B's card score (1-5) and the 1-2 beats it named as
a card's highlights. Needs SciPy and scikit-learn (the anas-ai-dev image has both).

The user's marks are ranked 有问题 < 平淡 < 可用 < 精彩 (0-3).

  python agree.py LABELS.json 4B.jsonl CARDS.json OUT.json
"""
import json
import sys
from collections import Counter
from pathlib import Path

import numpy as np
from scipy.stats import kendalltau, spearmanr
from sklearn.metrics import cohen_kappa_score, confusion_matrix, roc_auc_score

RANK = {"x": 0, "1": 1, "2": 2, "3": 3}
ORDER = ["3", "2", "1", "x"]
NAME = {"3": "精彩", "2": "可用", "1": "平淡", "x": "有问题"}


def read(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def rank_stats(user, model):
    rho = spearmanr(user, model)
    tau = kendalltau(user, model)
    return {"spearman": round(float(rho.statistic), 3), "spearman_p": float(rho.pvalue),
            "kendall": round(float(tau.statistic), 3), "n": len(user)}


CARD_SCORE = {1: "x", 2: "1", 3: "1", 4: "2", 5: "3"}


def agreement(user, model):
    """Two lists of marks (3, 2, 1, x): rank correlation, exact agreement, quadratic kappa and the confusion
    matrix (rows the user's, columns the model's, in ORDER)."""
    u, m = [RANK[x] for x in user], [RANK[x] for x in model]
    return rank_stats(u, m) | {"accuracy": round(float(np.mean(np.array(u) == np.array(m))), 3),
                                "kappa_quadratic": round(float(cohen_kappa_score(u, m, weights="quadratic")), 3),
                                "confusion": confusion_matrix(user, model, labels=ORDER).tolist()}


def main():
    labels_path, rated_path, cards_path, out_path = sys.argv[1:5]
    labels = read(labels_path)["labels"]
    rated = {r["beat"]: r for r in map(json.loads, Path(rated_path).read_text(encoding="utf-8").splitlines())}
    beats = [b for b in labels if b in rated]
    user = np.array([RANK[labels[b]] for b in beats])
    score = np.array([rated[b]["score"] for b in beats])
    answer = [rated[b]["mark"] for b in beats]
    hard = np.array([RANK[a] for a in answer])
    best = user == 3
    top = np.argsort(-score, kind="stable")[: int(best.sum())]
    out = {
        "beats": len(beats),
        "user": {NAME[k]: int(v) for k, v in Counter(labels[b] for b in beats).items()},
        "model": {NAME[k]: int(v) for k, v in Counter(answer).items()},
        "score_vs_user": rank_stats(user, score),
        "answer_vs_user": rank_stats(user, hard),
        "accuracy": round(float((hard == user).mean()), 3),
        "kappa_quadratic": round(float(cohen_kappa_score(user, hard, weights="quadratic")), 3),
        # rows: the user's mark, columns: the 4B's answer, both in ORDER
        "confusion": {"order": [NAME[k] for k in ORDER],
                      "rows": confusion_matrix([labels[b] for b in beats], answer, labels=ORDER).tolist()},
        "auc_best": round(float(roc_auc_score(best, score)), 3),
        "auc_problem": round(float(roc_auc_score(user == 0, [rated[b]["probs"]["0"] for b in beats])), 3),
        # Of the beats the 4B ranks highest, as many as the user marked 精彩: how many the user marked 精彩.
        "top_best": {"of": int(best.sum()), "hit": int(best[top].sum()), "share_of_all": round(float(best.mean()), 3)},
    }
    # What the 第六版 descriptions already hold (no new model run).
    cards = {c["id"]: c for c in read(cards_path).values() if "answer" in c}
    by_card = {}
    for b in beats:
        by_card.setdefault(b.split("/")[0], []).append(RANK[labels[b]])
    ids = [c for c in by_card if c in cards]
    card_user = [max(by_card[c]) for c in ids]
    card_4b = [cards[c]["answer"]["highlight"] for c in ids]
    named = [b for b in beats if int(b.split("/")[1]) in cards.get(b.split("/")[0], {}).get("answer", {}).get("highlights", [])]
    named_best = sum(labels[b] == "3" for b in named)
    # The user's reading of that score (2026-11-13): 1 有问题, 2-3 平淡, 4 可用, 5 精彩; given to each beat of the
    # card, and to the card against its best beat.
    mapped = {c: CARD_SCORE[cards[c]["answer"]["highlight"]] for c in ids}
    beat_mapped = [mapped[b.split("/")[0]] for b in beats]
    card_best = [ORDER[3 - max(by_card[c])] for c in ids]
    out["described"] = {
        "card_score_vs_card_best": rank_stats(card_user, card_4b),
        "card_score_mapped": {
            "model": {NAME[k]: int(v) for k, v in Counter(mapped.values()).items()},
            "beats": agreement([labels[b] for b in beats], beat_mapped),
            "cards": agreement(card_best, [mapped[c] for c in ids]),
        },
        "highlight_beats": {"named": len(named), "user_best": named_best, "precision": round(named_best / len(named), 3),
                            "share_of_all": round(float(best.mean()), 3)},
    }
    Path(out_path).write_text(json.dumps(out, ensure_ascii=False, indent=1), encoding="utf-8")
    print(json.dumps(out, ensure_ascii=False, indent=1))


if __name__ == "__main__":
    main()
