"""Stub of the A-NAS MCP tools for the spike.

In A-NAS the MCP endpoint is the product service. Here a stdio server inside
the sandbox forwards each call to the change broker's agent socket, which
knows the member by the caller's UID and decides everything: it applies a
submission only when the member approved exactly that call.

Hermes runs this server with `trust: untrusted`, so every call of a tool
without readOnlyHint waits for the member's in-turn approval first.

The photo tools (#113) read the photo library through the photo service's
assistant socket, which knows the member by the caller's UID and lets an
assistant only read. They hand the model text only: names, capture times,
libraries, tags and albums, never pixels. The same JSON is the structured
result, so the A-NAS window finds the photo IDs in it and shows the
thumbnails with the member's own session.
"""
import base64
import http.client
import io
import json
import os
import socket
import urllib.parse

from mcp.server import MCPServer
from mcp_types import CallToolResult, ImageContent, TextContent, ToolAnnotations

BROKER = "/run/anas-change-agent/agent.sock"
PHOTOS = "/run/anas-assistant-photos/photos.sock"
READ_ONLY = ToolAnnotations(read_only_hint=True)

server = MCPServer("anas", instructions=(
    "A-NAS 文件更改与相册。个人空间与本人的 Agent空间子目录只能经 submit_changes 更改，"
    "用户在对话中同意后才执行；共享空间的其他位置不能写。相册只能读。"))


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path: str):
        super().__init__("localhost")
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(120)
        self.sock.connect(self.path)


def broker(method: str, path: str, body: dict | None = None) -> str:
    conn = UnixHTTP(BROKER)
    conn.request(method, path, json.dumps(body, ensure_ascii=False).encode() if body is not None else None,
                 {"Content-Type": "application/json"})
    return conn.getresponse().read().decode()


@server.tool()
def submit_changes(summary: str, operations: list[dict]) -> str:
    """更改个人空间或本人 Agent空间子目录中的文件。调用时用户会在对话里看到这批更改并决定是否同意；同意后立即执行并返回结果，拒绝或没有回答时什么都不会改变。

    summary: 用一句话告诉用户这批更改做什么。
    operations: 按顺序执行的操作，每项为一个对象：
      {"op": "write", "path": 目标文件, "source": 暂存区 /nas/助手/待确认/ 中写好完整新内容的文件}
      {"op": "copy", "path": 目标, "source": 个人空间、共享空间或 /nas/助手 中要复制的文件或文件夹}（图片、视频等任意文件，不需要暂存）
      {"op": "delete", "path": 要删除的文件或文件夹（进入回收站）}
      {"op": "move", "path": 原路径, "to": 新路径}（移动或改名）
      {"op": "mkdir", "path": 新文件夹}
    """
    return broker("POST", "/apply", {"summary": summary, "operations": operations})


@server.tool(annotations=READ_ONLY)
def recent_changes() -> str:
    """列出最近提交给用户的文件更改，以及每一批是已执行、被拒绝还是没有确认。"""
    return broker("GET", "/recent")


class PhotoError(Exception):
    pass


def photos(path: str, **query) -> dict:
    """One read of the photo API; any failure is a PhotoError the model can read."""
    query = {key: value for key, value in query.items() if value}
    conn = UnixHTTP(PHOTOS)
    try:
        conn.request("GET", "/api/v1/photos" + path + ("?" + urllib.parse.urlencode(query) if query else ""))
        response = conn.getresponse()
        raw = response.read()
    except OSError:
        # Also what an assistant sees after the photo service restarted under it.
        raise PhotoError("相册暂时连不上，请稍后再试") from None
    finally:
        conn.close()
    try:
        body = json.loads(raw or b"{}")
    except ValueError:
        raise PhotoError(f"相册返回了看不懂的结果（{response.status}）") from None
    if response.status != 200:
        raise PhotoError((body.get("error") or {}).get("message") or f"相册返回 {response.status}")
    return body


# A month of one library longer than this is reported as cut short.
MONTH_LIMIT = 2000


def every_page(path: str, **query) -> tuple[list[dict], bool]:
    """All items of a paged listing, up to MONTH_LIMIT; True when there were more."""
    items, cursor = [], ""
    while True:
        page = photos(path, cursor=cursor, limit="500", **query)
        items += page["items"]
        cursor = page.get("next") or ""
        if not cursor:
            return items, False
        if len(items) >= MONTH_LIMIT:
            return items, True


def libraries() -> dict[str, str]:
    """Library IDs, named the way the member knows them."""
    return {lib["id"]: "我的" if lib["kind"] == "private" else "共享"
            for lib in photos("/libraries")["items"]}


def photo(asset: dict, libs: dict[str, str]) -> dict:
    item = {"id": asset["id"], "name": asset["name"], "library": libs.get(asset["libraryId"], "")}
    if asset.get("takenAt"):
        item["taken"] = asset["takenAt"][:16].replace("T", " ")
    if asset.get("tags"):
        item["tags"] = asset["tags"]
    if asset.get("albums"):
        item["albums"] = [album["name"] for album in asset["albums"]]
    return item


def result(data: dict) -> CallToolResult:
    # The text is the structured result itself, so Hermes passes it on once.
    return CallToolResult(content=[TextContent(type="text", text=json.dumps(data, ensure_ascii=False))],
                          structured_content=data)


def paged(page_of: dict, libs: dict[str, str]) -> CallToolResult:
    data = {"photos": [photo(asset, libs) for asset in page_of["items"]]}
    if page_of.get("next"):
        data["next"] = page_of["next"]
    return result(data)


def failed(error: PhotoError) -> CallToolResult:
    return CallToolResult(content=[TextContent(type="text", text=f"相册：{error}")], is_error=True)


PHOTO_NOTE = ("只读。结果里没有照片画面，只有名称、拍摄时间（taken）、所在图库（我的／共享）、"
              "本人加的标签与相册；没有拍摄地点，也认不出照片里是谁。要看画面用 look_at_photo。回答中提到某张照片时写成 "
              "[名称](照片的 id)，id 原样照抄（它本身以 photo: 开头），窗口会显示它的缩略图。")


@server.tool(annotations=READ_ONLY, structured_output=False, description=(
    "用一句话按画面内容、名称和标签搜索本人图库与共享图库的照片，最相关的在前：前 closest 张是最接近的一组，"
    "其余不太相关。搜索不看时间，请按 taken 自己筛选。query：要找的内容，如“海边”“生日蛋糕”；"
    "page：上一次结果中的 next。" + PHOTO_NOTE))
def search_photos(query: str, page: str = ""):
    try:
        found = photos("/search", q=query, cursor=page, limit="30")
        libs = libraries()
    except PhotoError as error:
        return failed(error)
    data = {"query": query, "closest": found.get("closest", 0),
            "photos": [photo(asset, libs) for asset in found["items"]]}
    if not found.get("semantic"):
        data["note"] = "设备上的 AI 暂时不可用，只按名称与标签匹配。"
    if found.get("next"):
        data["next"] = found["next"]
    return result(data)


@server.tool(annotations=READ_ONLY, structured_output=False, description=(
    "按时间、相册或标签浏览照片；什么都不填时列出每个月的照片数、相册与标签。month：年月，如 2025-07，"
    "列出这个月拍的照片；album_id：相册 id；tag：标签名；page：上一次结果中的 next（相册与标签）。" + PHOTO_NOTE))
def browse_photos(month: str = "", album_id: str = "", tag: str = "", page: str = ""):
    try:
        libs = libraries()
        if month:
            items, cut = [], False
            for library in libs:
                found, more = every_page(f"/libraries/{library}/timeline", month=month)
                items, cut = items + found, cut or more
            items.sort(key=lambda asset: asset.get("takenAt") or "")
            data = {"month": month, "count": len(items), "photos": [photo(asset, libs) for asset in items]}
            if cut:
                data["note"] = f"这个月的照片太多，只列出了前 {len(items)} 张，实际更多。"
            return result(data)
        if album_id:
            return paged(photos(f"/albums/{urllib.parse.quote(album_id, safe='')}/assets", cursor=page), libs)
        if tag:
            return paged(photos(f"/tags/{urllib.parse.quote(tag, safe='')}/assets", cursor=page), libs)
        months: dict[str, dict] = {}
        albums = []
        for library, label in libs.items():
            for entry in photos(f"/libraries/{library}/timeline/months")["items"]:
                months.setdefault(entry["month"], {"month": entry["month"]})[label] = entry["photos"]
            albums += [{"id": album["id"], "name": album["name"], "photos": album["photos"], "library": label}
                       for album in photos(f"/libraries/{library}/albums")["items"]]
        tags = [{"name": t["name"], "photos": t["photos"]} for t in photos("/tags")["items"]]
        return result({"months": sorted(months.values(), key=lambda m: m["month"]), "albums": albums, "tags": tags})
    except PhotoError as error:
        return failed(error)


@server.tool(annotations=READ_ONLY, structured_output=False, description=(
    "一张照片的详细信息：在搜索与浏览结果之外只多出尺寸、标签与所在相册；没有地点、人物或画面内容，"
    "不要为找这些逐张查看。" + PHOTO_NOTE))
def photo_details(photo_id: str):
    try:
        asset = photos(f"/assets/{urllib.parse.quote(photo_id, safe='')}")
        data = photo(asset, libraries())
    except PhotoError as error:
        return failed(error)
    data.update(width=asset["width"], height=asset["height"], bytes=asset["sizeBytes"])
    return result(data)


@server.tool(annotations=READ_ONLY, structured_output=False, description=(
    "找与某张照片画面相似的照片，最相似的在前；page：上一次结果中的 next。" + PHOTO_NOTE))
def similar_photos(photo_id: str, page: str = ""):
    try:
        page_of = photos(f"/assets/{urllib.parse.quote(photo_id, safe='')}/similar", cursor=page, limit="30")
        return paged(page_of, libraries())
    except PhotoError as error:
        return failed(error)


def photo_view(photo_id: str) -> bytes:
    """A photo's view from the photo service: a JPEG of at most 1600 pixels without the
    original's metadata (ADR 0024 decision 6)."""
    conn = UnixHTTP(PHOTOS)
    try:
        conn.request("GET", f"/api/v1/photos/assets/{urllib.parse.quote(photo_id, safe='')}/view")
        response = conn.getresponse()
        raw = response.read()
    except OSError:
        raise PhotoError("相册暂时连不上，请稍后再试") from None
    finally:
        conn.close()
    if response.status != 200:
        try:
            message = (json.loads(raw).get("error") or {}).get("message")
        except ValueError:
            message = None
        raise PhotoError(message or f"相册返回 {response.status}")
    return raw


@server.tool(annotations=READ_ONLY, structured_output=False, description=(
    "取出一张照片的画面，好看清里面有什么：成员要你看照片，或问到名称、时间、标签回答不了的画面内容时用。"
    "返回一张图片（最长边不超过 1600 像素，不带拍摄地点等原图信息）；结果里有它的路径，用 vision_analyze "
    "读这个路径并写清要看什么，才能看到画面。一次一张，只取需要的几张；视频没有画面可取。photo_id：照片的 id。"))
def look_at_photo(photo_id: str):
    try:
        data = photo_view(photo_id)
    except PhotoError as error:
        return failed(error)
    return CallToolResult(content=[
        TextContent(type="text", text=f"照片 {photo_id} 的画面如下；用 vision_analyze 看它。"),
        ImageContent(type="image", data=base64.b64encode(data).decode(), mime_type="image/jpeg"),
    ])


if os.environ.get("ANAS_SPIKE_PHOTO_PIXELS"):
    # Only for photos-vision.sh: what Hermes does when a tool hands the model an
    # image. The photo service gives assistants no thumbnails, so this paints one:
    # sky, sea, sand and a red sun.
    @server.tool(annotations=READ_ONLY, structured_output=False, description="查看一张照片的画面。")
    def view_photo(photo_id: str):
        from PIL import Image, ImageDraw
        img = Image.new("RGB", (480, 320), (150, 200, 240))
        draw = ImageDraw.Draw(img)
        draw.rectangle((0, 150, 480, 230), fill=(20, 80, 170))
        draw.rectangle((0, 230, 480, 320), fill=(230, 200, 120))
        draw.ellipse((360, 30, 430, 100), fill=(230, 60, 40))
        encoded = io.BytesIO()
        img.save(encoded, "JPEG", quality=85)
        return CallToolResult(content=[
            TextContent(type="text", text=f"照片 {photo_id} 的画面："),
            ImageContent(type="image", data=base64.b64encode(encoded.getvalue()).decode(), mime_type="image/jpeg"),
        ])


if __name__ == "__main__":
    import anyio
    anyio.run(server.run_stdio_async)
