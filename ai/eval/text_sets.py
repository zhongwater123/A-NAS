"""The photos the OCR routing evaluation sorts into "read its text" and "leave it".

Development only (docs/architecture/photo-ocr.md#哪些照片读文字): the
datasets stay under $ANAS_DATASETS on the development machine. Each sample has
a set and a kind:

- "read": CC-OCR's photographed documents, receipts, cards, forms and
  handwriting, the photos the library should read;
- "scene": CC-OCR's scene text, words on street signs, shops and packaging,
  which the library should leave: reading one costs one to two minutes;
- "photo": ordinary photos: COCO 2017 val, the Open Images selection of the
  label calibration and the Gallagher family collection;
- "web": CC-OCR's web images with text (posters, product pictures, dense text
  rendered as images), reported apart: whether to read them is the user's call.

CC-OCR carries its images in base64 inside TSV files; they are written out
once under $ANAS_DATASETS/cc-ocr/images/<subset>/<index>.<extension>.
"""

import base64
import csv
import os

from eval import datasets

# Subset: (TSV under cc-ocr/, kind).
CC_OCR = {
    "zh_doc": ("multi_scene_ocr/document_text/zh_doc_100.tsv", "read"),
    "doc_photo_chn": ("doc_parsing/doc/doc_photo_chn_75.tsv", "read"),
    "COLD_SIBR": ("kie/open_category/COLD_SIBR_400.tsv", "read"),
    "COLD_CELL": ("kie/open_category/COLD_CELL_600.tsv", "read"),
    "CORD": ("multi_scene_ocr/document_text/CORD_100.tsv", "read"),
    "FUNSD": ("multi_scene_ocr/document_text/FUNSD_50.tsv", "read"),
    "zh_handwriting": ("multi_scene_ocr/document_text/zh_handwriting_50.tsv", "read"),
    "zh_scene": ("multi_scene_ocr/scene_text/zh_scene_450.tsv", "scene"),
    "TotalText": ("multi_scene_ocr/scene_text/TotalText_300.tsv", "scene"),
    "IC15": ("multi_scene_ocr/scene_text/IC15_500.tsv", "scene"),
    "ugc_laion": ("multi_scene_ocr/ugc_text/ugc_laion_400.tsv", "web"),
    "zh_dense": ("multi_scene_ocr/ugc_text/zh_dense_50.tsv", "web"),
}
GALLAGHER_IMAGES = os.path.join("gallagher", "images")


class Sample:
    def __init__(self, key, path, group, kind):
        self.key = key
        self.path = path
        self.group = group
        self.kind = kind


def extension(data):
    if data.startswith(b"\xff\xd8"):
        return "jpg"
    if data.startswith(b"\x89PNG"):
        return "png"
    return "img"


def cc_ocr(subset, root=datasets.ROOT):
    """The subset's images, written out of its TSV on first use; none when the TSV is missing."""
    path, kind = CC_OCR[subset]
    tsv = os.path.join(root, "cc-ocr", path)
    if not os.path.exists(tsv):
        return []
    directory = os.path.join(root, "cc-ocr", "images", subset)
    os.makedirs(directory, exist_ok=True)
    csv.field_size_limit(1 << 30)
    samples = []
    with open(tsv, encoding="utf-8", newline="") as file:
        for row in csv.DictReader(file, delimiter="\t"):
            data = base64.b64decode(row["image"])
            image = os.path.join(directory, f"{row['index']}.{extension(data)}")
            if not os.path.exists(image):
                with open(image + ".partial", "wb") as out:
                    out.write(data)
                os.replace(image + ".partial", image)
            samples.append(Sample(f"cc-ocr:{subset}/{row['index']}", image, subset, kind))
    return samples


def gallagher(root=datasets.ROOT):
    directory = os.path.join(root, GALLAGHER_IMAGES)
    names = sorted(name for name in os.listdir(directory) if name.lower().endswith((".jpg", ".jpeg")))
    return [Sample(f"gallagher:{name}", os.path.join(directory, name), "gallagher", "photo") for name in names]


def samples(sets=("cc-ocr", "coco", "openimages", "gallagher"), root=datasets.ROOT):
    """Every sample of the named sets."""
    found = []
    if "cc-ocr" in sets:
        for subset in CC_OCR:
            found += cc_ocr(subset, root)
    ground_truth = None
    if "coco" in sets or "openimages" in sets:
        ground_truth = datasets.load_ground_truth()
    if "coco" in sets:
        found += [Sample(sample.key, sample.path, "coco", "photo") for sample in datasets.coco_samples(ground_truth, root)]
    if "openimages" in sets:
        ids = datasets.select_openimages(ground_truth, root=root)
        found += [Sample(sample.key, sample.path, "openimages", "photo")
                  for sample in datasets.openimages_samples(ground_truth, ids, root)
                  if os.path.exists(sample.path)]
    if "gallagher" in sets:
        found += gallagher(root)
    return found
