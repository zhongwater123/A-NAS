"""Blind review material: each case's two edits as X and Y, and putting the plans back.

The key goes outside the review folder. The review sheet has one row per edit;
the reviewers add their own columns, since the project decides how to judge
(docs/specs/video-edit-evaluation.md#盲评材料). Standard library only.
"""

import csv
import os
import shutil

LABELS = ("X", "Y")
FIXED = ("case", "label", "file")
# Excel and WPS read a UTF-8 CSV as such only with the byte order mark.
ENCODING = "utf-8-sig"


def assign(cases, plans, rng):
    """{case: {"X": plan, "Y": plan}} in a random order per case."""
    key = {}
    for case in sorted(cases):
        order = list(plans)
        rng.shuffle(order)
        key[case] = dict(zip(LABELS, order, strict=True))
    return key


def write_review(key, outputs, folder):
    """Copies each edit to <folder>/<case>/<label>.mp4 and writes review.csv; outputs maps plan → case → path."""
    rows = []
    for case, labels in sorted(key.items()):
        os.makedirs(os.path.join(folder, case))
        for label, plan in labels.items():
            name = f"{case}/{label}.mp4"
            shutil.copyfile(outputs[plan][case], os.path.join(folder, name))
            rows.append({"case": case, "label": label, "file": name})
    with open(os.path.join(folder, "review.csv"), "w", encoding=ENCODING, newline="") as file:
        writer = csv.DictWriter(file, fieldnames=FIXED)
        writer.writeheader()
        writer.writerows(rows)


def read_rows(path):
    with open(path, encoding=ENCODING, newline="") as file:
        return list(csv.DictReader(file))


def write_rows(path, rows):
    fields = list(rows[0]) if rows else ["case", "label", "plan", "file"]
    with open(path, "w", encoding=ENCODING, newline="") as file:
        writer = csv.DictWriter(file, fieldnames=fields)
        writer.writeheader()
        writer.writerows(rows)


def unblind(key, rows):
    """Adds each row's plan and turns every reviewer cell that reads X or Y into a plan.

    Returns the rows and the columns that held X or Y (choices).
    """
    out, choices = [], set()
    for row in rows:
        labels = key[row["case"]]
        new = {"case": row["case"], "label": row["label"], "plan": labels[row["label"]]}
        for column, value in row.items():
            if column in FIXED:
                new[column] = value
            elif (value or "").strip() in labels:
                new[column] = labels[value.strip()]
                choices.add(column)
            else:
                new[column] = value
        out.append(new)
    return out, sorted(choices)


def number(text):
    try:
        return float(text)
    except ValueError:
        return None


def tally(rows, plans, choices):
    """Per reviewer column: for a choice, the cases each plan was chosen in; else the mean of numbers per plan."""
    columns = [column for column in (rows[0] if rows else {}) if column not in (*FIXED, "plan")]
    lines = []
    for column in columns:
        if column in choices:
            chosen = {plan: sorted({row["case"] for row in rows if row[column].strip() == plan}) for plan in plans}
            parts = [f"方案 {plan} {len(cases)} 个" for plan, cases in chosen.items()]
            lines.append(f"- {column}：" + "，".join(parts))
            continue
        values = [(row["plan"], number(row[column].strip())) for row in rows if (row[column] or "").strip()]
        if values and all(value is not None for _, value in values):
            means = []
            for plan in plans:
                mine = [value for owner, value in values if owner == plan]
                means.append(f"方案 {plan} {sum(mine) / len(mine):.2f}（{len(mine)} 条）" if mine else f"方案 {plan} —")
            lines.append(f"- {column} 平均：" + "，".join(means))
    return lines
