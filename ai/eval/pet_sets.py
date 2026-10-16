"""The labelled cat and dog photos the pet evaluation reads.

Development only: the datasets stay under $ANAS_DATASETS on the development
machine. A family has one to three pets, and its library also holds other
people's animals seen once, so each set deals its individuals into
households of HOUSEHOLD pets with DISTRACTORS photos of other animals
beside them. A distractor never forms a pet on its own (it is seen once);
the question is whether it is pulled into a family pet, or two family pets
into one.

- dogfacenet: 1,393 dogs' faces, aligned (the whole image is the crop);
  distractors are single photos of other DogFaceNet dogs.
- mpdd: 192 dogs photographed whole from several sides; distractors are
  Oxford-IIIT Pet dogs, each a different animal.
- oxford-cats: no cat identity set could be downloaded without signing in,
  so households here hold only distractor cats: any pet formed is a merge
  of different cats.
"""

import csv
import hashlib
import os

from eval import datasets
from eval.face_sets import Face, Photo

HOUSEHOLD = 3
DISTRACTORS = 10
# Households kept per set, so that a run takes about an hour on the
# development machine (EmbeddingGemma needs about 2 s a crop there).
MAX_HOUSEHOLDS = {"dogfacenet": 50, "mpdd": 30, "oxford-cats": 30}
CAT, DOG = 140, 93


def stable(text):
    return int(hashlib.sha256(text.encode()).hexdigest()[:8], 16)


def households(individuals, prefix, limit):
    """Deals individuals (name -> photo paths) into at most limit households of HOUSEHOLD."""
    names = sorted(individuals, key=stable)[: limit * HOUSEHOLD]
    homes = {}
    for index, name in enumerate(names):
        homes[name] = f"{prefix}-{index // HOUSEHOLD}"
    return homes


def oxford(species):
    """Oxford-IIIT Pet photos of one species as (key, path, breed); every photo is a different animal."""
    root = os.path.join(datasets.ROOT, "oxford-pets")
    found = []
    with open(os.path.join(root, "images.csv"), encoding="utf-8") as file:
        for row in csv.DictReader(file):
            # timm's copy labels cats 0 and dogs 1.
            if {"0": "cat", "1": "dog"}.get(row["species"]) == species:
                found.append((f"oxford:{row['file']}", os.path.join(root, "images", row["file"]), row["breed"]))
    return sorted(found, key=lambda item: stable(item[0]))


def with_distractors(photos, homes, distractors, kind):
    """Adds DISTRACTORS distractor photos to each household, never reusing one."""
    pool = iter(distractors)
    for home in sorted(set(homes.values())):
        for _ in range(DISTRACTORS):
            item = next(pool, None)
            if item is None:
                return photos
            key, path, group = item
            photos.append(Photo(key + "@" + home, path, [Face(f"stranger:{key}", group=f"stranger-{kind}")], home))
    return photos


def dogfacenet():
    root = os.path.join(datasets.ROOT, "dogfacenet", "after_4_bis")
    individuals = {}
    for dog in os.listdir(root):
        folder = os.path.join(root, dog)
        if os.path.isdir(folder):
            individuals[dog] = sorted(os.path.join(folder, name) for name in os.listdir(folder) if name.endswith(".jpg"))
    many = {dog: paths for dog, paths in individuals.items() if len(paths) >= 3}
    homes = households(many, "dfn", MAX_HOUSEHOLDS["dogfacenet"])
    photos = [Photo(f"dfn:{dog}/{os.path.basename(path)}", path, [Face(f"dfn:{dog}", group="family-dog")], homes[dog])
              for dog, paths in many.items() if dog in homes for path in paths]
    # Strangers: the first photo of dogs not in any household.
    strangers = [(f"dfn:{dog}/{os.path.basename(paths[0])}", paths[0], "dog")
                 for dog, paths in sorted(individuals.items(), key=lambda item: stable(item[0])) if dog not in many and paths]
    return with_distractors(photos, homes, strangers, "dog")


def mpdd():
    root = os.path.join(datasets.ROOT, "mpdd", "MPDD", "pytorch")
    individuals = {}
    for folder, _dirs, files in os.walk(root):
        for name in files:
            if name.endswith(".jpg"):
                individuals.setdefault(name.split("_")[0], []).append(os.path.join(folder, name))
    homes = households(individuals, "mpdd", MAX_HOUSEHOLDS["mpdd"])
    photos = [Photo(f"mpdd:{os.path.relpath(path, root)}", path, [Face(f"mpdd:{dog}", group="family-dog")], homes[dog])
              for dog, paths in individuals.items() if dog in homes for path in sorted(paths)]
    return with_distractors(photos, homes, oxford("dog"), "dog")


def oxford_cats():
    cats = oxford("cat")[: MAX_HOUSEHOLDS["oxford-cats"] * DISTRACTORS]
    photos = []
    for index, (key, path, breed) in enumerate(cats):
        home = f"cats-{index // DISTRACTORS}"
        photos.append(Photo(key, path, [Face(f"stranger:{key}", group=f"stranger-cat-{breed}")], home))
    return photos


SETS = {
    "dogfacenet": dogfacenet,
    "mpdd": mpdd,
    "oxford-cats": oxford_cats,
}
# Sets whose images are one animal's face already: the class of the whole image.
WHOLE = {"dogfacenet": DOG}
