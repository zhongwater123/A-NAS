"""Ground truth from the public datasets that calibrate the AI labels.

Development only: the datasets stay on the development machine and are never
packaged. Each (image, label) pair is positive, negative or unknown:

- COCO 2017 val annotates its 80 categories exhaustively, so a label mapped
  with "coco" is negative wherever none of its categories is annotated.
- "cocoAbsentUnless" makes a COCO image negative for a label COCO does not
  annotate when none of the listed categories is present (no bird, no duck).
  Images that hold the label anyway count against it, which only makes the
  threshold stricter.
- Open Images V7 validation has human-verified labels: positive when any
  mapped class is verified present, negative when the first mapped class is
  verified absent. Everything else is unknown and left out.
"""

import collections
import csv
import hashlib
import json
import os

ROOT = os.environ.get("ANAS_DATASETS", "/datasets")
HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(HERE))
VOCABULARY = os.path.join(HERE, "labels", "v1.json")
GROUND_TRUTH = os.path.join(HERE, "ground_truth.json")

COCO_ANNOTATIONS = os.path.join("coco", "annotations", "instances_val2017.json")
COCO_IMAGES = os.path.join("coco", "val2017")
COCO_CN = os.path.join("coco-cn", "coco-cn-version1805v1.1")
COCO_CN_IMAGES = os.path.join("coco-cn", "images")
OPENIMAGES = "openimages"
OPENIMAGES_IMAGES = os.path.join("openimages", "validation")


class Sample:
    """One image with the labels known to be present or absent."""

    def __init__(self, key, path):
        self.key = key
        self.path = path
        self.positive = set()
        self.negative = set()

    @property
    def split(self):
        # A stable half of the images calibrates; the other half only tests.
        return "calibration" if hashlib.sha256(self.key.encode()).digest()[0] % 2 == 0 else "test"


def load_vocabulary(path=VOCABULARY):
    with open(path, encoding="utf-8") as file:
        return json.load(file)["labels"]


def load_ground_truth(path=GROUND_TRUTH):
    with open(path, encoding="utf-8") as file:
        return json.load(file)


def coco_samples(ground_truth, root=ROOT):
    with open(os.path.join(root, COCO_ANNOTATIONS), encoding="utf-8") as file:
        data = json.load(file)
    category = {entry["id"]: entry["name"] for entry in data["categories"]}
    names = set(category.values())
    present = collections.defaultdict(set)
    for annotation in data["annotations"]:
        present[annotation["image_id"]].add(category[annotation["category_id"]])
    for label, mapping in ground_truth.items():
        for name in mapping.get("coco", []) + mapping.get("cocoAbsentUnless", []):
            if name not in names:
                raise ValueError(f"{label}: COCO has no category {name!r}")
    samples = []
    for image in sorted(data["images"], key=lambda entry: entry["id"]):
        sample = Sample(f"coco:{image['id']}", os.path.join(root, COCO_IMAGES, image["file_name"]))
        found = present[image["id"]]
        for label, mapping in ground_truth.items():
            if "coco" in mapping:
                (sample.positive if found & set(mapping["coco"]) else sample.negative).add(label)
            elif "cocoAbsentUnless" in mapping and not found & set(mapping["cocoAbsentUnless"]):
                sample.negative.add(label)
        samples.append(sample)
    return samples


def openimages_classes(root=ROOT):
    """Maps lower-case display names to their machine IDs."""
    names = collections.defaultdict(set)
    with open(os.path.join(root, OPENIMAGES, "oidv7-class-descriptions.csv"), encoding="utf-8") as file:
        for row in csv.DictReader(file):
            names[row["DisplayName"].lower()].add(row["LabelName"])
    return names


def openimages_verified(root=ROOT):
    """Returns {image ID: {class ID: True when present, False when absent}}."""
    verified = collections.defaultdict(dict)
    with open(os.path.join(root, OPENIMAGES, "oidv7-val-annotations-human-imagelabels.csv"), encoding="utf-8") as file:
        for row in csv.DictReader(file):
            verified[row["ImageID"]][row["LabelName"]] = row["Confidence"] == "1"
    return verified


def openimages_rotated(root=ROOT):
    """Image IDs whose stored pixels are not upright."""
    rotated = set()
    with open(os.path.join(root, OPENIMAGES, "validation-images-with-rotation.csv"), encoding="utf-8") as file:
        for row in csv.DictReader(file):
            if row["Rotation"] not in ("", "0", "0.0"):
                rotated.add(row["ImageID"])
    return rotated


def openimages_mapping(ground_truth, root=ROOT):
    """Returns {label: (class IDs that make it negative, class IDs that make it positive)}."""
    classes = openimages_classes(root)
    mapping = {}
    for label, entry in ground_truth.items():
        names = entry.get("openimages")
        if not names:
            continue
        ids = []
        for name in names:
            if name.lower() not in classes:
                raise ValueError(f"{label}: Open Images has no class {name!r}")
            ids.append(classes[name.lower()])
        mapping[label] = (ids[0], set().union(*ids))
    return mapping


def labels_of(verified_labels, mapping):
    positive, negative = set(), set()
    for label, (absent, present) in mapping.items():
        if any(verified_labels.get(cls) is True for cls in present):
            positive.add(label)
        elif any(verified_labels.get(cls) is False for cls in absent):
            negative.add(label)
    return positive, negative


def select_openimages(ground_truth, positives=50, negatives=20, root=ROOT):
    """Picks a stable subset: up to N verified positives and negatives per label."""
    mapping = openimages_mapping(ground_truth, root)
    verified = openimages_verified(root)
    rotated = openimages_rotated(root)
    by_label = collections.defaultdict(lambda: ([], []))
    for image_id in sorted(verified, key=lambda key: hashlib.sha256(key.encode()).hexdigest()):
        if image_id in rotated:
            continue
        positive, negative = labels_of(verified[image_id], mapping)
        for label in positive:
            by_label[label][0].append(image_id)
        for label in negative:
            by_label[label][1].append(image_id)
    chosen = set()
    for found_positive, found_negative in by_label.values():
        chosen.update(found_positive[:positives])
        chosen.update(found_negative[:negatives])
    return sorted(chosen)


def openimages_samples(ground_truth, image_ids, root=ROOT):
    mapping = openimages_mapping(ground_truth, root)
    verified = openimages_verified(root)
    samples = []
    for image_id in image_ids:
        sample = Sample(f"oi:{image_id}", os.path.join(root, OPENIMAGES_IMAGES, f"{image_id}.jpg"))
        sample.positive, sample.negative = labels_of(verified[image_id], mapping)
        samples.append(sample)
    return samples


def coco_cn(split, root=ROOT):
    """Returns ([(image name, path)], {image name: {"written": [...], "translated": [...]}}) of a split."""
    base = os.path.join(root, COCO_CN)
    with open(os.path.join(base, f"coco-cn_{split}.txt"), encoding="utf-8") as file:
        names = [line.strip() for line in file if line.strip()]
    captions = {name: {"written": [], "translated": []} for name in names}
    for kind, file_name in (("written", "imageid.human-written-caption.txt"),
                            ("translated", "imageid.manually-translated-caption.txt")):
        with open(os.path.join(base, file_name), encoding="utf-8") as file:
            for line in file:
                # The key ends at the first tab or, in some files, space.
                parts = line.split(None, 1)
                if len(parts) < 2:
                    continue
                name = parts[0].split("#")[0]
                if name in captions:
                    captions[name][kind].append(parts[1].strip())
    return [(name, os.path.join(root, COCO_CN_IMAGES, f"{name}.jpg")) for name in names], captions


def coco_cn_url(name):
    folder = "train2014" if "_train2014_" in name else "val2014"
    return f"http://images.cocodataset.org/{folder}/{name}.jpg"


def openimages_url(image_id):
    return f"https://open-images-dataset.s3.amazonaws.com/validation/{image_id}.jpg"
