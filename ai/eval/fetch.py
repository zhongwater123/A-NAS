"""Downloads the evaluation images that are fetched one by one.

COCO-CN val/test images come from the COCO 2014 servers and the Open Images
subset from the public CVDF bucket. The total size is checked with HEAD
requests first and the download stops when it exceeds --budget-gb.

    python -m eval.fetch --budget-gb 4
"""

import argparse
import concurrent.futures
import os
import sys
import time
import urllib.request

from eval import datasets


def with_retries(action, attempts=5):
    for attempt in range(attempts):
        try:
            return action()
        except OSError:
            if attempt == attempts - 1:
                raise
            time.sleep(2 * (attempt + 1))
    return None


def size_of(url):
    request = urllib.request.Request(url, method="HEAD")
    with with_retries(lambda: urllib.request.urlopen(request, timeout=60)) as response:
        return int(response.headers["Content-Length"])


def download(url, path):
    def fetch():
        with urllib.request.urlopen(url, timeout=120) as response:
            return response.read()

    data = with_retries(fetch)
    partial = path + ".partial"
    with open(partial, "wb") as file:
        file.write(data)
    os.replace(partial, path)
    return len(data)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--budget-gb", type=float, default=4.0, help="refuse when the missing images exceed this")
    parser.add_argument("--workers", type=int, default=16)
    args = parser.parse_args()

    ground_truth = datasets.load_ground_truth()
    jobs = []
    for split in ("val", "test"):
        images, _captions = datasets.coco_cn(split)
        jobs += [(datasets.coco_cn_url(name), path) for name, path in images]
    for image_id in datasets.select_openimages(ground_truth):
        jobs.append((datasets.openimages_url(image_id), os.path.join(datasets.ROOT, datasets.OPENIMAGES_IMAGES, f"{image_id}.jpg")))
    missing = [(url, path) for url, path in jobs if not os.path.exists(path)]
    print(f"{len(jobs)} images, {len(missing)} missing", flush=True)

    with concurrent.futures.ThreadPoolExecutor(args.workers) as pool:
        total = sum(pool.map(lambda job: size_of(job[0]), missing))
    print(f"missing images total {total / 1e9:.2f} GB", flush=True)
    if total > args.budget_gb * 1e9:
        sys.exit(f"over the {args.budget_gb} GB budget; nothing downloaded")

    for path in {os.path.dirname(path) for _url, path in missing}:
        os.makedirs(path, exist_ok=True)
    done = 0
    with concurrent.futures.ThreadPoolExecutor(args.workers) as pool:
        for _ in pool.map(lambda job: download(*job), missing):
            done += 1
            if done % 500 == 0:
                print(f"{done}/{len(missing)}", flush=True)
    print(f"downloaded {done} images", flush=True)


if __name__ == "__main__":
    main()
