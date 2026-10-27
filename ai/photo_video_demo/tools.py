"""What the two tools do, as plain functions that return the tool's JSON."""

import os
import tempfile
import urllib.parse

from photo_video_demo.client import Client, ProductError
from photo_video_demo.render import RenderError, render

QUERY_MAX = 200
PAGE_SIZE = 30
PHOTOS_MAX = 60
SECONDS_MIN, SECONDS_MAX = 1, 10


class ToolError(Exception):
    """A refusal or failure the model reads: a code and one sentence."""

    def __init__(self, code: str, message: str, **detail):
        super().__init__(message)
        self.body = {"error": code, "message": message, **detail}


def search_photos(client: Client, query: str, page: str = "") -> dict:
    """The assistant's search_photos (ai/assistant-spike/anas_mcp.py), plus video marks."""
    if not 1 <= len(query) <= QUERY_MAX:
        raise ToolError("invalid_query", f"query 要有 1–{QUERY_MAX} 个字符")
    found = _call(client.json, "/api/v1/photos/search", q=query, cursor=page, limit=str(PAGE_SIZE))
    libs = _call(_libraries, client)
    data = {"query": query, "closest": found.get("closest", 0),
            "photos": [_photo(asset, libs) for asset in found["items"]]}
    if not found.get("semantic"):
        data["note"] = "设备上的 AI 暂时不可用，只按名称与标签匹配。"
    if found.get("next"):
        data["next"] = found["next"]
    return data


def make_slideshow(client: Client, output_dir: str, asset_ids: list[str],
                   seconds_per_photo: float = 3, crossfade: bool = False) -> dict:
    if not 1 <= len(asset_ids) <= PHOTOS_MAX:
        raise ToolError("invalid_asset_ids", f"asset_ids 要有 1–{PHOTOS_MAX} 个，收到 {len(asset_ids)} 个")
    if isinstance(seconds_per_photo, bool) or not SECONDS_MIN <= seconds_per_photo <= SECONDS_MAX:
        raise ToolError("invalid_seconds_per_photo",
                        f"seconds_per_photo 要在 {SECONDS_MIN}–{SECONDS_MAX} 秒之间，收到 {seconds_per_photo}")
    _check(client, asset_ids)
    with tempfile.TemporaryDirectory(prefix="anas-slideshow-") as work:
        images = []
        for i, asset_id in enumerate(asset_ids):
            data = _call(client.image, f"/api/v1/photos/assets/{_quote(asset_id)}/view")
            path = os.path.join(work, f"{i:03d}.jpg")
            with open(path, "wb") as file:
                file.write(data)
            images.append(path)
        try:
            output = render(images, seconds_per_photo, crossfade, output_dir)
        except RenderError as error:
            raise ToolError("render_failed", str(error)) from None
    return {"path": output, "seconds": len(asset_ids) * seconds_per_photo, "photos": len(asset_ids)}


def _check(client: Client, asset_ids: list[str]) -> None:
    """Refuses the whole call when any asset is missing, trashed or a video."""
    problems: dict[str, list[str]] = {}
    for asset_id in dict.fromkeys(asset_ids):
        try:
            asset = client.json(f"/api/v1/photos/assets/{_quote(asset_id)}")
        except ProductError as error:
            if error.status != 404:
                raise ToolError(error.code, error.message) from None
            problems.setdefault("not_found", []).append(asset_id)
            continue
        # The photo service still shows trashed assets to whoever may change them.
        if asset.get("trash"):
            problems.setdefault("not_found", []).append(asset_id)
        elif asset.get("video") or not asset.get("mediaType", "").startswith("image/"):
            problems.setdefault("video", []).append(asset_id)
    if problems:
        reasons = {"not_found": "不存在或看不到", "video": "是视频，第一阶段只拼接照片"}
        raise ToolError("invalid_assets", "；".join(f"{len(ids)} 个{reasons[kind]}" for kind, ids in problems.items()),
                        assets=problems)


def _libraries(client: Client) -> dict[str, str]:
    return {lib["id"]: "我的" if lib["kind"] == "private" else "共享"
            for lib in client.json("/api/v1/photos/libraries")["items"]}


def _photo(asset: dict, libs: dict[str, str]) -> dict:
    item = {"id": asset["id"], "name": asset["name"], "library": libs.get(asset["libraryId"], "")}
    if asset.get("takenAt"):
        item["taken"] = asset["takenAt"][:16].replace("T", " ")
    if asset.get("tags"):
        item["tags"] = asset["tags"]
    if asset.get("albums"):
        item["albums"] = [album["name"] for album in asset["albums"]]
    if asset.get("video"):
        item["video"] = True
    return item


def _call(fn, *args, **kwargs):
    try:
        return fn(*args, **kwargs)
    except ProductError as error:
        raise ToolError(error.code, error.message) from None


def _quote(asset_id: str) -> str:
    return urllib.parse.quote(asset_id, safe="")
