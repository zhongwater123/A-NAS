"""The test set's manifest: every Release file with its size and sha256.

The manifest is written once the uploads are frozen (`manifest`), and every
run checks the downloaded files against it (`fetch`, `run`). Standard library
only.
"""

import hashlib
import json
import os
import re
import urllib.request

from eval.fetch import with_retries

RELEASE = "video-edit-testset-v1"
RELEASE_URL = f"https://github.com/zhongwater123/A-NAS/releases/download/{RELEASE}"
CHUNK = 1 << 20


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as file:
        while chunk := file.read(CHUNK):
            digest.update(chunk)
    return digest.hexdigest()


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def build(directory, release=RELEASE):
    files = []
    for name in sorted(os.listdir(directory)):
        path = os.path.join(directory, name)
        if os.path.isfile(path):
            files.append({"name": name, "size": os.path.getsize(path), "sha256": sha256_file(path)})
    return {"release": release, "files": files}


def load(path):
    with open(path, "rb") as file:
        data = file.read()
    return json.loads(data), sha256_bytes(data)


def case_ids(manifest):
    return sorted(name[: -len(".json")] for name in (entry["name"] for entry in manifest["files"])
                  if re.fullmatch(r"case-\d{2}\.json", name))


def problems(directory, manifest):
    """What in directory differs from the manifest; an empty list when nothing does."""
    found = []
    for entry in manifest["files"]:
        path = os.path.join(directory, entry["name"])
        if not os.path.isfile(path):
            found.append(f"缺少 {entry['name']}")
        elif os.path.getsize(path) != entry["size"] or sha256_file(path) != entry["sha256"]:
            found.append(f"{entry['name']} 与清单不符")
    return found


def download(url, path, sha256):
    """Streams url to path and keeps it only when its sha256 matches."""
    partial = path + ".partial"

    def fetch():
        digest = hashlib.sha256()
        with urllib.request.urlopen(url, timeout=120) as response, open(partial, "wb") as file:  # noqa: S310  the Release's https URL
            while chunk := response.read(CHUNK):
                digest.update(chunk)
                file.write(chunk)
        return digest.hexdigest()

    got = with_retries(fetch)
    if got != sha256:
        os.remove(partial)
        raise ValueError(f"{os.path.basename(path)}: sha256 {got}, manifest {sha256}")
    os.replace(partial, path)
