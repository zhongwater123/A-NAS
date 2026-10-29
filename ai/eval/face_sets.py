"""The labelled face photos the people evaluation reads.

Development only: the datasets stay under $ANAS_DATASETS on the development
machine and are never packaged (docs/architecture/photo-people.md#评估与验收).
Each set lists photos with the faces a person labelled in them. A labelled
face carries its identity, an optional box in the original's pixels and an
optional group the report breaks results down by. A face with no box is the
photo's central face, as in LFW, whose images are centred on one person.
"""

import hashlib
import os

from eval import datasets


class Face:
    __slots__ = ("box", "eyes", "group", "identity")

    def __init__(self, identity, box=None, group="", eyes=None):
        # None for a face whose identity nobody labelled (WIDER FACE): it
        # counts for detection only.
        self.identity = identity
        # (x0, y0, x1, y1) in the original's pixels; with neither a box nor
        # eyes, the face is the photo's central one.
        self.box = box
        # ((x, y), (x, y)): the two eye centres, as Gallagher labels faces.
        self.eyes = eyes
        self.group = group


class Photo:
    __slots__ = ("faces", "key", "library", "path")

    def __init__(self, key, path, faces, library=""):
        self.key = key
        self.path = path
        self.faces = faces
        # Photos of one library are clustered together (one family, one album).
        self.library = library


LFW = os.path.join("lfw", "lfw")


LFW_LIBRARIES = 40


def lfw():
    """LFW: 13,233 photos of 5,749 people, each centred on the named person.

    LFW has no albums, so its people are dealt into LFW_LIBRARIES stand-in
    libraries by a hash of their name; the 4,069 people seen once are the
    strangers a real library has too.
    """
    root = os.path.join(datasets.ROOT, LFW)
    photos = []
    for person in sorted(os.listdir(root)):
        folder = os.path.join(root, person)
        if not os.path.isdir(folder):
            continue
        library = f"lfw-{int(hashlib.sha256(person.encode()).hexdigest()[:8], 16) % LFW_LIBRARIES}"
        photos.extend(Photo(f"lfw:{name[:-4]}", os.path.join(folder, name), [Face(person)], library)
                      for name in sorted(os.listdir(folder)) if name.endswith(".jpg"))
    return photos


GALLAGHER = "gallagher"


def gallagher():
    """Gallagher's collection: one family's album of 589 photos, 931 faces of 32 people.

    The truth gives each face's eye centres and person; the family's
    children grow up across the album. It is a single library.
    """
    root = os.path.join(datasets.ROOT, GALLAGHER)
    faces = {}
    with open(os.path.join(root, "GallagherDatasetGT.txt"), encoding="ascii") as file:
        for line in file:
            parts = line.split()
            if len(parts) != 6:
                continue
            name, lx, ly, rx, ry, person = parts
            eyes = ((float(lx), float(ly)), (float(rx), float(ry)))
            faces.setdefault(name, []).append(Face(f"gallagher:{person}", eyes=eyes))
    return [Photo(f"gallagher:{name}", os.path.join(root, "images", name), found, "gallagher")
            for name, found in sorted(faces.items())]


FGNET = os.path.join("fgnet", "FGNET")
FGNET_LIBRARIES = 8


def fgnet_box(path):
    """The box around FG-NET's 68 landmark points for one image, or None."""
    try:
        with open(path, encoding="ascii") as file:
            text = file.read()
    except OSError:
        return None
    body = text[text.find("{") + 1: text.rfind("}")]
    points = [tuple(float(v) for v in line.split()) for line in body.strip().splitlines() if line.strip()]
    if not points:
        return None
    xs, ys = [p[0] for p in points], [p[1] for p in points]
    return (min(xs), min(ys), max(xs), max(ys))


def age_group(age):
    if age < 13:
        return "child"
    if age < 20:
        return "teen"
    return "adult"


def fgnet():
    """FG-NET: 1,002 scanned photos of 82 people from infancy to old age.

    Names are <person>A<age>[a-z].JPG. The box comes from the 68 labelled
    landmarks; people are dealt into FGNET_LIBRARIES libraries.
    """
    root = os.path.join(datasets.ROOT, FGNET)
    images = os.path.join(root, "images")
    points = os.path.join(root, "points")
    photos = []
    for name in sorted(os.listdir(images)):
        stem, ext = os.path.splitext(name)
        if ext.lower() != ".jpg" or "A" not in stem.upper():
            continue
        person, age = stem.upper().split("A", 1)
        age = int("".join(ch for ch in age if ch.isdigit()))
        box = fgnet_box(os.path.join(points, stem.lower() + ".pts"))
        library = f"fgnet-{int(person) % FGNET_LIBRARIES}"
        photos.append(Photo(f"fgnet:{stem}", os.path.join(images, name),
                            [Face(f"fgnet:{person}", box, age_group(age))], library))
    return photos


WIDER = "wider-face"


def wider():
    """WIDER FACE validation: 3,226 photos with every face boxed, for detection only."""
    root = os.path.join(datasets.ROOT, WIDER)
    truth = os.path.join(root, "wider_face_split", "wider_face_val_bbx_gt.txt")
    photos = []
    with open(truth, encoding="ascii") as file:
        lines = [line.strip() for line in file]
    at = 0
    while at < len(lines):
        name = lines[at]
        count = int(lines[at + 1])
        rows = lines[at + 2: at + 2 + max(count, 1)]
        at += 2 + max(count, 1)
        faces = []
        for row in rows[:count]:
            x, y, w, h, *_rest, invalid = (int(v) for v in row.split()[:8])
            if w > 0 and h > 0 and not invalid:
                faces.append(Face(None, (x, y, x + w, y + h)))
        photos.append(Photo(f"wider:{name}", os.path.join(root, "WIDER_val", "images", name), faces))
    return photos


SETS = {
    "lfw": lfw,
    "gallagher": gallagher,
    "fgnet": fgnet,
    "wider": wider,
}
