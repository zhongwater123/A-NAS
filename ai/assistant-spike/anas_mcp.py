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

The text the device read from a photo (docs/architecture/photo-ocr.md) comes
with the search results that matched by it and with a photo's details: the
document type, its fields and the whole text. The photo service has already
replaced every sensitive number with a handle such as ⟦身份证号·1a2b3c⟧; the
model writes the handle in its answer and the A-NAS window shows the member
the number in its place, so no tool here ever holds one.
"""
import base64
import contextlib
import http.client
import io
import json
import os
import secrets
import socket
import stat
import sys
import tempfile
import time
import urllib.parse

from mcp.server import MCPServer
from mcp_types import CallToolResult, ImageContent, TextContent, ToolAnnotations

BROKER = "/run/anas-change-agent/agent.sock"
PHOTOS = "/run/anas-assistant-photos/photos.sock"
READ_ONLY = ToolAnnotations(read_only_hint=True)

server = MCPServer("anas", instructions=(
    "A-NAS 文件更改与相册。个人空间与本人的 Agent空间子目录只能经 submit_changes 更改，"
    "用户在对话中同意后才执行；共享空间的其他位置不能写。相册只能读；证件、银行卡、发票、回单、小票"
    "上的信息（号码、有效期、金额等）在相册照片读出的文字里，用 search_photos 找。"))


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path: str, seconds: float = 120):
        super().__init__("localhost")
        self.path, self.seconds = path, seconds

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.seconds)
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
      {"op": "move", "path": 原路径, "to": 新路径}（移动或改名，只在个人空间之内或 Agent空间之内；两者之间先 copy 再 delete）
      {"op": "mkdir", "path": 新文件夹}
    一批最多 100 项操作；暂存文件每个不超过 64 MiB，一批合计不超过 256 MiB。
    """
    return broker("POST", "/apply", {"summary": summary, "operations": operations})


@server.tool(annotations=READ_ONLY)
def recent_changes() -> str:
    """列出最近提交给用户的文件更改，以及每一批是已执行、被拒绝还是没有确认。"""
    return broker("GET", "/recent")


class PhotoError(Exception):
    """A failed read of the photo API: the message is for the model, the code is the API's."""

    def __init__(self, message: str, code: str = ""):
        super().__init__(message)
        self.code = code


def refused(status: int, raw: bytes) -> PhotoError:
    """What the photo API said when it answered anything but 200."""
    try:
        error = json.loads(raw or b"{}").get("error") or {}
    except (ValueError, AttributeError):
        return PhotoError(f"相册返回了看不懂的结果（{status}）")
    # What to do next, in the tools' own words; the API's message says only what happened.
    code = error.get("code") or ""
    return PhotoError(NEXT_STEP.get(code) or error.get("message") or f"相册返回 {status}", code)


# Refusals an assistant can act on, by the photo API's error code.
NEXT_STEP = {
    "sensitive_photo": ("这张照片是证件、银行卡或票据，或写有账号一类的长号码，不提供画面。用 photo_details 读它的文字"
                        "和字段；其中的敏感号码是句柄，把句柄原样写进回答，窗口会向成员显示真实的号码。"),
    "unread_photo": ("这张照片上可能有文字，设备还没读完或没读出来，在那之前不提供画面。请成员稍后再问，"
                     "或自己在相册里打开它；不要改用别的办法去看。"),
}


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
    if response.status != 200:
        raise refused(response.status, raw)
    try:
        return json.loads(raw or b"{}")
    except ValueError:
        raise PhotoError(f"相册返回了看不懂的结果（{response.status}）") from None


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


# The document types the device reads fields from, as members call them.
DOCUMENTS = {"id_card": "身份证人像面", "id_card_back": "身份证国徽面", "bank_card": "银行卡", "bank_slip": "银行回单",
             "invoice": "发票", "receipt": "小票"}
# A search result carries this much of a photo's plain text; photo_details has all of it.
EXCERPT = 200
# The text of this many leading results comes with a search.
TEXT_RESULTS = 10


def read_text(photo_id: str, whole: bool) -> dict:
    """What the device read from a photo, as the model gets it: the document type, its fields
    with the ones nobody checked yet, and the text (all of it, or for a search result only an
    excerpt of a photo that is no document). Empty for a photo with no text to give."""
    reading = photos(f"/assets/{urllib.parse.quote(photo_id, safe='')}/text")
    if reading["status"] == "queued":
        return {"text_status": "照片上的文字还没读完"} if whole else {}
    if reading["status"] == "failed":
        return {"text_status": "照片上的文字没能读出来"} if whole else {}
    if reading["status"] != "read":
        return {}
    data = {}
    document = DOCUMENTS.get(reading.get("kind", ""))
    if document:
        data["document"] = document
    fields = {field["name"]: field["value"] for field in reading["fields"] if field["value"]}
    if fields:
        data["fields"] = fields
        unchecked = [field["name"] for field in reading["fields"] if field["value"] and not field["passed"]]
        if unchecked:
            data["unchecked"] = unchecked
    text = reading.get("text", "")
    if whole:
        data["text"] = text
        if not reading["complete"]:
            data["text_status"] = "文字没有读完，后面的缺失"
    elif not document and text:
        data["text"] = text if len(text) <= EXCERPT else text[:EXCERPT] + "…"
    return data


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
              "本人加的标签与相册；拍摄地点与相机等原图信息用 photo_details 查，认不出照片里是谁。要看画面用 look_at_photo。回答中提到某张照片时写成 "
              "[名称](照片的 id)，id 原样照抄（它本身以 photo: 开头），窗口会显示它的缩略图。")

# How the text read from photos reaches the model, for the two tools that carry it.
TEXT_NOTE = ("设备读出过文字的照片带有：document（证件或票据的类型）、fields（读出的字段）、unchecked（还没有人核对的"
             "字段，转述时说明是机器识别、可能有误）和 text（照片上的文字）。形如 ⟦身份证号·1a2b3c⟧ 的是敏感号码的句柄："
             "你看不到号码本身，成员要这个号码时把句柄原样写进回答正文（不改动，不放进代码块），窗口会在那个位置向成员"
             "显示真实的号码，成员看到的就是号码：不要解释句柄，不要说自己看不到，也不要请成员自己去看。")


@server.tool(annotations=READ_ONLY, structured_output=False, description=(
    "用一句话按画面内容、名称、标签和照片上的文字搜索本人图库与共享图库的照片，最相关的在前：前 closest 张是最接近的一组，"
    "其余不太相关。成员问起写在证件、银行卡、发票、回单、小票上的信息（证件号、卡号、有效期、金额、商户等）时先用它，"
    "直接搜类型（“身份证”“银行卡”“发票”）或上面的字，相册里常有它们的照片；结果里带着 fields 的照片不必再用 "
    "photo_details 查。搜索不看时间，请按 taken 自己筛选。"
    "query：要找的内容，如“海边”“生日蛋糕”“身份证”；page：上一次结果中的 next。" + PHOTO_NOTE + TEXT_NOTE))
def search_photos(query: str, page: str = ""):
    try:
        found = photos("/search", q=query, cursor=page, limit="30")
        libs = libraries()
    except PhotoError as error:
        return failed(error)
    items = [photo(asset, libs) for asset in found["items"]]
    if not page:
        # The results that matched by name or by their text lead the first page; what was read
        # from them comes along, so that one search answers what a document says.
        for item in items[:min(found.get("names", 0) + found.get("texts", 0), TEXT_RESULTS)]:
            with contextlib.suppress(PhotoError):
                item.update(read_text(item["id"], whole=False))
    data = {"query": query, "closest": found.get("closest", 0), "photos": items}
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
    "一张照片的详细信息：尺寸、标签、所在相册，原图记录的全部元数据（exif：相机、镜头、曝光、拍摄时间等；"
    "location：相机记下的拍摄位置，经纬度与海拔，没有记下就没有），以及设备从照片上读出的全部文字与字段。"
    "要知道照片上写了什么就用它，不用看画面。经纬度可以换算成大致地点；没有人物与画面内容，"
    "不要为找这些逐张查看。" + PHOTO_NOTE + TEXT_NOTE))
def photo_details(photo_id: str):
    quoted = urllib.parse.quote(photo_id, safe='')
    try:
        asset = photos(f"/assets/{quoted}")
        data = photo(asset, libraries())
        if asset.get("mediaType", "").startswith("image/"):
            data.update(photos(f"/assets/{quoted}/metadata"))
        if asset.get("text"):
            data.update(read_text(photo_id, whole=True))
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
        raise refused(response.status, raw)
    return raw


@server.tool(annotations=READ_ONLY, structured_output=False, description=(
    "取出一张照片的画面，好看清里面有什么：成员要你看照片，或问到名称、时间、标签回答不了的画面内容时用。"
    "读照片上的字不用它，用 photo_details；证件、银行卡、票据、写有长号码的照片和文字还没读完的照片不提供画面。"
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


# Photo-to-video (docs/specs/photo-to-video.md) with ai/montage's renderer, which run.sh
# mounts at /spike-ai with SPIKE_PHOTOS; without it (the trial bundle) there is no such tool.
sys.path.append("/spike-ai")
try:
    from montage import render as slideshow
except ImportError:
    slideshow = None
VIDEOS = "/nas/助手/视频"
SLIDESHOW_MAX = 60
SECONDS_MIN, SECONDS_MAX = 1, 10


def slideshow_photos(photo_ids: list[str]) -> None:
    """Refuses the whole call when any ID is missing, out of reach or a video."""
    problems: dict[str, list[str]] = {}
    for photo_id in dict.fromkeys(photo_ids):
        try:
            asset = photos(f"/assets/{urllib.parse.quote(photo_id, safe='')}")
        except PhotoError:
            problems.setdefault("不存在或看不到", []).append(photo_id)
            continue
        if not asset.get("mediaType", "").startswith("image/"):
            problems.setdefault("是视频，只能拼接照片", []).append(photo_id)
    if problems:
        raise PhotoError("；".join(f"{', '.join(ids)} {reason}" for reason, ids in problems.items()))


if slideshow:
    # Not read-only: it adds a file, though only a new one in the assistant's own folder.
    @server.tool(annotations=ToolAnnotations(read_only_hint=False, destructive_hint=False),
                 structured_output=False, description=(
        "把照片按给出的顺序拼成一段 1920×1080 的无声 MP4，存进助手目录的“视频”文件夹，返回文件路径与时长。"
        f"photo_ids：照片的 id（search_photos 等的结果），1–{SLIDESHOW_MAX} 个，按出现顺序；"
        f"seconds_per_photo：每张的秒数，{SECONDS_MIN}–{SECONDS_MAX}，可带小数，默认 3；总时长 = 张数 × seconds_per_photo，"
        "要凑出指定总时长时据此选张数与每张秒数。crossfade：相邻两张淡入淡出，默认否，不改变总时长。"
        "有视频、不存在或看不到的 id 时整次拒绝。成片只在助手目录里；成员要放进个人空间时，再用 submit_changes 的 copy 提交。"))
    def make_slideshow(photo_ids: list[str], seconds_per_photo: float = 3, crossfade: bool = False):
        if not 1 <= len(photo_ids) <= SLIDESHOW_MAX:
            return failed(PhotoError(f"photo_ids 要有 1–{SLIDESHOW_MAX} 个，收到 {len(photo_ids)} 个"))
        if isinstance(seconds_per_photo, bool) or not SECONDS_MIN <= seconds_per_photo <= SECONDS_MAX:
            return failed(PhotoError(f"seconds_per_photo 要在 {SECONDS_MIN}–{SECONDS_MAX} 秒之间，收到 {seconds_per_photo}"))
        try:
            slideshow_photos(photo_ids)
            with tempfile.TemporaryDirectory(prefix="slideshow-") as work:
                images = []
                for i, photo_id in enumerate(photo_ids):
                    images.append(os.path.join(work, f"{i:03d}.jpg"))
                    with open(images[-1], "wb") as file:
                        file.write(photo_view(photo_id))
                path = slideshow.render(images, seconds_per_photo, crossfade, VIDEOS)
        except PhotoError as error:
            return failed(error)
        except slideshow.RenderError as error:
            return CallToolResult(content=[TextContent(type="text", text=f"拼接失败：{error}")], is_error=True)
        return result({"path": path, "seconds": len(photo_ids) * seconds_per_photo, "photos": len(photo_ids)})


# Video moments (#322): videos_spike.py, the stand-in for the photo service's video
# moments, holds the vectors and the videos; these tools only relay, and the model
# gets text and numbers, never frames: a local vision model looks at them for it. Its socket is bound over an empty file like the
# photo service's; while the file is still empty (no SPIKE_VIDEOS) there are no such tools.
VIDEO_MOMENTS = "/run/anas-assistant-videos/videos.sock"


def video_moments(path: str, body: dict) -> tuple[int, str, bytes, list]:
    """Status, content type, body and what the vision model saw of each segment (X-Review)."""
    # A cut takes a while: the frames are looked at, then FFmpeg decodes and encodes every second.
    conn = UnixHTTP(VIDEO_MOMENTS, 900)
    try:
        conn.request("POST", path, json.dumps(body, ensure_ascii=False).encode(), {"Content-Type": "application/json"})
        response = conn.getresponse()
        review = response.getheader("X-Review")
        return (response.status, response.getheader("Content-Type", ""), response.read(),
                json.loads(base64.b64decode(review)) if review else [])
    except OSError:
        raise PhotoError("视频检索暂时连不上，请稍后再试") from None
    finally:
        conn.close()


def video_failed(status: int, raw: bytes) -> CallToolResult:
    try:
        body = json.loads(raw)
    except ValueError:
        body = {"error": f"视频检索返回 {status}"}
    text = body.get("error", f"视频检索返回 {status}")
    if body.get("problems"):
        text += "：\n" + "\n".join(f"- {problem}" for problem in body["problems"])
    return CallToolResult(content=[TextContent(type="text", text=text)], is_error=True)


# An experiment (#326): the planner is shown each card's three key frames in place of the local
# vision model's caption. Frames of the public test videos leave the machine for it; the
# product sends text only (docs/specs/photo-to-video.md).
CARD_PICTURES = os.environ.get("ANAS_SPIKE_CARD_PICTURES") == "1"
SEEING = (
    "每张卡片随附一张图：这张卡片首、中、尾三帧从左到右拼在一起，图前标着卡片 id；直接看图判断画面是什么。"
    if CARD_PICTURES else
    "caption 是本机视觉模型看这张卡片的代表帧写的一句话，比 tags 准，但也可能看错；")
CHOOSING = "选卡片先看图里是不是要的画面；" if CARD_PICTURES else "选卡片先看 caption 是否就是要的画面；"
# Another (#326): every probe recalls more cards, the local vision model judges each on its
# key frames, and only those it passes come back, its best first.
CARD_SCREEN = os.environ.get("ANAS_SPIKE_CARD_SCREEN") == "1"
SCREENING = (
    "检索先为每条探针按分数召回 20 张卡片，本机视觉模型逐张看三帧，判断画面里有没有这条探针并打 0–10 分，"
    "只返回判为有的，按它的分数排在前面；each 是每条探针最多返回几张通过的，想剪长一些就调大。"
    "卡片的 screen 按探针给出这个分数 score 和它看到的 seen；refused 是每条探针被判为没有的张数。"
    if CARD_SCREEN else "")
# The fourth version's ablation (#350). ANAS_SPIKE_RETRIEVED_ONLY=1: the catalog is as before,
# but a plan may use only cards search_video_moments returned. ANAS_SPIKE_SPAN=probe or frame
# as well: the plan gives no seconds, the video service grows each segment from the window
# the card's probe matched (moments.Index.span). ANAS_SPIKE_LOCATE=probe or frame instead: no
# retrieval needed, each segment grows from the window of its card its shows matches.
LOCATE = os.environ.get("ANAS_SPIKE_LOCATE") or None
# ANAS_SPIKE_FILL=1 as well: the plan says the cut's seconds, and the service sets each
# segment's to add up to them, its ends from frames every half second (moments.Index.fill).
FILL = LOCATE is not None and os.environ.get("ANAS_SPIKE_FILL") == "1"
# ANAS_SPIKE_EDGES=1: before the frames are looked at, a cut near either end of a segment,
# found at the source's frame rate, trims it (videos_spike.edges).
EDGES = os.environ.get("ANAS_SPIKE_EDGES") == "1"
# ANAS_SPIKE_FLOW=1 with LOCATE: the catalog lines leave out the seconds, which the planner no
# longer sets, and the plan is asked to keep neighbouring segments in one scene or with one person.
FLOW = LOCATE is not None and os.environ.get("ANAS_SPIKE_FLOW") == "1"
CATALOG_LINE = ("每行是“卡片 id 光线 描述（亮点：…）毛病：…”，不列秒数：入点出点由程序定。卡片 id 是“视频-镜头序号-段”，"
                "镜头序号相邻的多半是同一场戏；" if FLOW else
                "每行是“卡片 id 起止秒数（所在镜头的起止）光线 描述（亮点：…）毛病：…”；")
CATALOG_SPAN = ("毛病是量出来的（模糊、抖动、过暗、过曝），没有就不写。" if FLOW else
                "镜头起止是剪这张卡片时不能越出的范围，毛病是量出来的（模糊、抖动、过暗、过曝），没有就不写。")
STORY = ("每段一句内容、对应的卡片范围，例如侦探片预告写“1947 年洛杉矶街景开场（01-1 到 01-4）→ 警局里交代案子（01-8 到 01-30）"
         "→ 开车出发 → 雨夜车里出现神秘女人 → …”；" if FLOW else
         "每段一句内容、对应视频里的大致秒数，例如侦探片预告写“1947 年洛杉矶街景开场（0–30 秒）→ 警局里交代案子（30–200 秒）"
         "→ 开车出发 → 雨夜车里出现神秘女人 → …”；")
FLOWING = ("排段时注意前后衔接：相邻两段尽量是同一场景或同一个人物，同一场戏的几段放在一起；换场景时先放一段交代新地点的画面，"
           "不要在不相干的场景、人物之间来回跳。" if FLOW else "")
# ANAS_SPIKE_SCENES=1 with FLOW: each catalog line says its card's scene, the catalog names the
# scenes, and a plan takes varied pictures from a scene.
SCENES = FLOW and os.environ.get("ANAS_SPIKE_SCENES") == "1"
SCENE_LINE = ("每行的“场景N”是这张卡片所在的场景，结果的 scenes 写每个场景是什么地方、有几张卡片：同一场景是同一个地方，"
              "片头片尾的字幕、标志和演职员表也各自成一个场景；" if SCENES else "")
SCENE_RULES = ("同一场景可以用多段，但画面要有变化；"
               "字幕、标志和演职员表的场景不用，除非成员要。" if SCENES else "")
# ANAS_SPIKE_LENGTHS=1 with FILL: each catalog line says the most seconds its card can run, and
# a plan's cards must be able to run moments.MARGIN times the cut's seconds.
LENGTHS = FILL and os.environ.get("ANAS_SPIKE_LENGTHS") == "1"
LENGTH_LINE = "“可剪 X 秒”是这张卡片一段最多能剪多长；" if LENGTHS else ""
LENGTH_RULE = ("选卡片时把各卡的“可剪”秒数加起来（超过这段 pace 上限的按上限算），至少要到 seconds 的 1.1 倍，"
               "程序才有余量定每段的长短，不够会被退回；可剪秒数比这段 pace 的下限还短的卡片，换一个慢一点的 pace 或换卡片。"
               if LENGTHS else "")
# What the tools say that no longer holds once the program sets the seconds (FILL), no search is
# needed (LOCATE) or no frames are looked at (NO_LOOK), left out in those modes (#350).
CATALOG_STEPS = ("读完先在回复里写一条故事线：按指令要的效果，把成片要有的段落按播放顺序列出来，"
                 if LOCATE else "读完先在回复里写一条故事线，再检索：按指令要的效果，把成片要有的段落按播放顺序列出来，")
SEARCH_STEP = ("" if LOCATE else "然后按故事线写 search_video_moments 的检索词，每段要的画面各写一条，段落之间的过渡画面也算；"
               "库里没有的不要硬找。")
SKIPPING = ("skipped：考虑过但没用的卡片，想说明的列几张 {card, why}，why 一句话，不超过 80 个字；不列就给空列表。" if LOCATE else
            "skipped：检索结果里每一张没用上的卡片各一项 {card, why}，why 用一句话写为什么不用；直接从目录取卡片时只列考虑过但没用的卡片，"
            "card 写具体的卡片 id，不写通配或整条视频"
            "（例如“描述是城市俯瞰，没有城堡”“flaws 有抖动”“和第 2 段同组”），不超过 80 个字，随成片存档供复盘；没有就给空列表。")
LENGTH_SAYS = ("不为凑时长放进不切题的片段，开场和铺垫镜头也要切题；" if FILL else
               "成片多长由故事线和切题的素材决定，素材少就短，不为凑时长放进不切题的片段，"
               "开场和铺垫镜头也要切题；成员明确说了时长时才按成员说的剪；")
# ANAS_SPIKE_NO_LOOK=1: the cut is made without the vision model looking at its frames first.
NO_LOOK = os.environ.get("ANAS_SPIKE_NO_LOOK") == "1"
SPAN = LOCATE or os.environ.get("ANAS_SPIKE_SPAN") or None
RETRIEVED_ONLY = os.environ.get("ANAS_SPIKE_RETRIEVED_ONLY") == "1" or (SPAN is not None and not LOCATE)
# The cards search_video_moments returned this session, each with the probe it scored best on.
RETRIEVED: dict[str, dict] = {}
FROM_CATALOG = ("剪辑计划只能用 search_video_moments 返回过的卡片：目录只用来了解库里有什么、写故事线和检索词，"
                "目录里看中的卡片要先检索到才能用。" if RETRIEVED_ONLY else
                "剪辑计划可以直接用目录里的卡片，不必为拿资料再检索。")
SEGMENT = (
    "每段是 {card, shows, pace, reason}，不写 start、end：card 是目录里的卡片 id；入点出点由程序定：从这张卡片里最符合 shows 的 2 秒开始，"
    "在这张卡片和画面相同的相邻部分里扩展，不越出镜头，不和别的段重叠。pace 是这段的节奏，定这段大约多长："
    "慢 4–6 秒（交代地点、铺垫情绪、悬念收尾），中 3–4.5 秒（交代人物和情节），快 1.5–2.5 秒（冲突、打斗、追逐，可以几段连着快切）；"
    "程序按各段的 pace 分配长短，再一起缩放到 seconds。段数和节奏由你定：过渡场景 1–2 段就够，不必每个场景都用满；"
    "按你定的节奏凑不到 seconds 时会退回，说明差多少。"
    if FILL else
    "每段是 {card, shows, reason}，不写 start、end：card 是 list_video_contents 或 search_video_moments 里的卡片 id，"
    "入点出点由程序定，从这张卡片里最符合 shows 的 2 秒往两边扩展，"
    + ("后面的秒数与 shows 的分数相差不到 0.02 才扩展" if LOCATE == "probe" else "后面的画面与那 2 秒相似才扩展") +
    "，最长 6 秒，不越出镜头，不和前面的段重叠，所以每段 2–6 秒；"
    if LOCATE else
    "每段是 {card, shows, reason}，不写 start、end：card 是 search_video_moments 返回过的卡片 id，"
    "入点出点由程序定，从这张卡片最符合找到它的那条检索词的 2 秒往两边扩展，"
    + ("后面的秒数与检索词的分数相差不到 0.02 才扩展" if SPAN == "probe" else "后面的画面与那 2 秒相似才扩展") +
    "，最长 6 秒，不越出镜头，不和前面的段重叠，所以每段 2–6 秒；"
    if SPAN else
    "每段是 {card, start, end, shows, reason}：card 是 "
    + ("search_video_moments 返回过的" if RETRIEVED_ONLY else "list_video_contents 或 search_video_moments 里的") +
    "卡片 id，start、end 是秒数，必须落在这张卡片的 shot 范围内，每段 1.5–6 秒，不能含纯黑或纯色画面，各段不能用同一段画面；")
SHOWS = (
    "shows 用一句短描述写这段要呈现的画面，例如“雨夜车里后视镜中的女人”“铁塔下的人群”，不超过 40 个字："
    "程序按它在卡片里定位入点出点，出片前也按它核对；写画面里看得见的东西，不写画面看不出的条件（地点名、年代、情绪）。"
    if LOCATE else
    "shows 写这段画面要呈现指令里的什么，只用指令里的词，如“湖上的小船”“夜晚的城市”，不超过 40 个字；"
    "不要写这段画面的细节或别的说法（“美食”就写“美食”，不写“美食,新鲜食材”），"
    "地点这类画面看不出的条件也不要写进去（“新加坡的美食”写“美食”）。")
TELLING = ("向成员介绍片段时只说你在图里和出片前核对看到的内容。" if CARD_PICTURES else
           "你看不到画面：向成员介绍片段时只说 caption 和出片前核对看到的内容，不要描述猜出来的细节。")

# How each kind of cut is told (montage/styles.py); the video tools come only with /spike-ai.
try:
    from montage.styles import STYLES
except ImportError:
    STYLES = {}

if os.path.exists(VIDEO_MOMENTS) and stat.S_ISSOCK(os.stat(VIDEO_MOMENTS).st_mode):
    @server.tool(annotations=READ_ONLY, structured_output=False, description=(
        "列出测试视频库里有什么，剪辑前先调用一次。按视频列出每张镜头卡片，按时间先后，" + CATALOG_LINE +
        "描述、光线和亮点是本机视觉模型看卡片的三帧写的，可能看错；" + CATALOG_SPAN + SCENE_LINE + LENGTH_LINE +
        CATALOG_STEPS + STORY +
        "只放能讲进这条故事线的内容，和它搭不上的不用放。" + SEARCH_STEP + FROM_CATALOG +
        "videos：只列这些视频，可省略。"
        "style：成片的体裁，" + "、".join(STYLES) + "之一；成员说了体裁就照说的传，没说就按指令判断，判断不出就不传。"
        "结果的 style_rules 是这个体裁的剪法（结构、怎么收尾、节奏），写故事线和排段时照着做，并在 make_montage 里传同一个 style。"))
    def list_video_contents(videos: list[str] | None = None, style: str | None = None):
        if style is not None and style not in STYLES:
            return CallToolResult(content=[TextContent(type="text", text=f"style 要是{'、'.join(STYLES)}之一，或不传")],
                                  is_error=True)
        try:
            status, _kind, raw, _review = video_moments("/catalog", ({"videos": videos} if videos else {})
                                                        | ({"times": False} if FLOW else {})
                                                        | ({"scenes": True} if SCENES else {})
                                                        | ({"lengths": True} if LENGTHS else {}))
        except PhotoError as error:
            return failed(error)
        if status != 200:
            return video_failed(status, raw)
        return result(json.loads(raw) | ({"style": style, "style_rules": STYLES[style]} if style else {}))

    @server.tool(annotations=READ_ONLY, structured_output=False, description=(
        "按画面内容在测试视频里找片段，返回镜头卡片。先用 list_video_contents 看过库里有什么再检索。"
        "probes：1–8 条要找的画面，按你写的故事线每段要的画面各写一条，每条一个主题、一句短描述，例如“城堡和老城的石头街道”写“城堡”“老城的石头街道”两条；"
        "同一主题的近义说法找回的多是同一批卡片，不必多写。"
        "条件不要和主题写进同一条检索词：向量检索会只匹配其中一半，“夜晚的城市”会找回白天的城市。条件按下面分层，先筛再排："
        "lights：只要这些光线的卡片（晴天、阴天、逆光、黄昏、夜晚、室内），例如“夜晚的城市”写 probes [\"城市\"]、lights [\"夜晚\"]；"
        "words：只要描述或亮点里含其中一个词的卡片，用目录里描述实际用的词，例如“下雨的街道”写 probes [\"街道\"]、words [\"雨\"]；"
        "目录描述里写到的东西（如“雨”“办公室”“照片”）用 words 找比检索词准；筛完没有卡片，就是库里没有这样的画面，不要换说法硬找；"
        "videos：只在这些视频里找，指令说的是某一条视频（如“这部侦探片”）时，按目录确定是哪条再限定。"
        "each：每条探针各取它分数最高的几张卡片，彼此 group 不同，默认 5，条数乘 each 不超过 40；"
        "这样一个主题的卡片不会被另一个主题的高分挤掉。结果里的 each 按探针列出各自取到的卡片 id，最相关的在前。" + SCREENING +
        "每张卡片是一个镜头里画面大致不变的一段，最长 20 秒：id；video；start、end 是这张卡片覆盖的秒数；shot 是所在镜头的起止，"
        "剪的时候不能越出；" + SEEING +
        "score 是各条探针里最高的分数，probes 给出每条探针的最高分与出现的时刻 at；tags 是词表里最接近的几个词，"
        "只说明大致内容；group 相同的卡片画面相似，别重复用同一画面。分数低于 threshold 时与“库里没有”分不开。"
        "light 是光线，highlight 是画面里最吸引人的一样东西，也是本机视觉模型看出来的；"
        "motion 是量出来的镜头运动（固定、向前走、向后退、向左转、向右转、抬头、低头）；"
        "flaws 是量出来的画面毛病（模糊、抖动、过暗、过曝），空表示没有。" +
        CHOOSING + "同样切题时优先 flaws 为空的，但夜景、黑色电影这类本来就暗的画面，过暗和随之而来的抖动多半是有意的，看描述是不是要的画面再定，"
        "不选 highlight 是垃圾桶、遮挡物这类不好看的东西的，再看分数。"
        "when：lights 和 words 都表达不了、画面本身能看出的条件，每条是 {is, not}，只留更像 is 的画面，"
        "例如 {\"is\": \"人很多\", \"not\": \"空无一人\"}；比 lights、words 粗，能用它们时不用 when。" +
        TELLING))
    def search_video_moments(probes: list[str], each: int = 5, videos: list[str] | None = None,
                             lights: list[str] | None = None, words: list[str] | None = None,
                             when: list[dict] | None = None):
        body = {"probes": probes, "each": each} | ({"videos": videos} if videos else {}) | (
            {"lights": lights} if lights else {}) | ({"words": words} if words else {}) | (
            {"when": when} if when else {}) | ({"pictures": True} if CARD_PICTURES else {}) | (
            {"screen": True} if CARD_SCREEN else {})
        try:
            status, _kind, raw, _review = video_moments("/search", body)
        except PhotoError as error:
            return failed(error)
        if status != 200:
            return video_failed(status, raw)
        found = json.loads(raw)
        for card in found["cards"]:
            for probe, best in card.get("probes", {}).items():
                if card["id"] in found.get("each", {}).get(probe, [card["id"]]) and best["score"] > RETRIEVED.get(
                        card["id"], {}).get("score", -2):
                    RETRIEVED[card["id"]] = {"probe": probe, "score": best["score"]}
        if not CARD_PICTURES:
            return result(found)
        pictures = [(card["id"], card.pop("picture")) for card in found["cards"]]
        content = [TextContent(type="text", text=json.dumps(found, ensure_ascii=False))]
        for card_id, picture in pictures:
            content += [TextContent(type="text", text=f"卡片 {card_id}："),
                        ImageContent(type="image", data=picture, mime_type="image/jpeg")]
        return CallToolResult(content=content)

    # Not read-only: it adds a file, though only a new one in the assistant's own folder.
    @server.tool(annotations=ToolAnnotations(read_only_hint=False, destructive_hint=False),
                 structured_output=False, description=(
        "按剪辑计划把视频片段剪成一段 1920×1080、30 fps 的 MP4，每段带原片同一区间的原声（不加配乐和字幕），存进助手目录的“视频”文件夹，返回文件路径与时长。"
        "segments：按播放顺序排列，" + SEGMENT
        + ("" if FILL else "成片总长不设上限。") + SHOWS + "reason 用一句话写这段属于故事线的哪一段、为什么选它，随成片存档。"
        + SKIPPING +
        "按故事线排段：故事线是骨架，每段挑最有看点的画面，不必把每一步都交代；不必每个检索主题都用上，和故事线搭不上的整个不用；"
        + FLOWING + SCENE_RULES + LENGTH_RULE +
        "同一场景相邻的几张卡片可以连着用，只有 group 相同才算重复画面。" + LENGTH_SAYS + ("" if NO_LOOK else
        "出片前本机视觉模型每 0.5 秒看一帧，核对是否呈现 shows：没有连续 1.5 秒符合的段整次退回，说明画面实际是什么，"
        "请换一张卡片或删掉这段；其余各段收窄到符合的那一截再剪，结果的 trimmed 列出收窄了哪些段。"
        "被退回或收窄的段，看到的画面若符合指令的另一部分（例如找城堡时退回的一段其实是老街），"
        "可以把它的 shows 改成那一部分、换到合适的位置再用，不必扔掉。") +
        "计划不合规则时整次拒绝并逐条说明是哪一段、哪一条，照着改了再调用。成片只在助手目录里；"
        "成员要放进个人空间时，再用 submit_changes 的 copy 提交。"
        "style：按哪个体裁剪的，和 list_video_contents 传的一样，随成片存档；没定体裁就不传。"
        + ("seconds：成员说的成片时长（秒），程序按它分配各段长短；成员没说时长就按故事线估一个。" if FILL else "")))
    def make_montage(segments: list[dict], skipped: list[dict], style: str | None = None,
                     seconds: float | None = None):
        if RETRIEVED_ONLY:
            unseen = [f"第 {n} 段的 {s.get('card')} 不是 search_video_moments 返回过的卡片，先检索到它再用"
                      for n, s in enumerate(segments, 1) if not isinstance(s, dict) or s.get("card") not in RETRIEVED]
            if unseen:
                return CallToolResult(content=[TextContent(type="text", text="剪辑计划不合规则：\n" + "\n".join(
                    f"- {problem}" for problem in unseen))], is_error=True)
        body = {"segments": segments, "skipped": skipped}
        if SPAN:
            body = {"segments": [{k: v for k, v in s.items() if k not in ("start", "end")}
                                 | {"probe": s.get("shows", "") if LOCATE else RETRIEVED[s["card"]]["probe"]}
                                 for s in segments],
                    "skipped": skipped, "span": SPAN}
            if FILL and seconds:
                body |= {"seconds": seconds, "style": style} | ({"margin": True} if LENGTHS else {})
        if EDGES:
            body["edges"] = True
        if NO_LOOK:
            body["look"] = False
        if SCENES:
            body["scenes"] = True
        try:
            status, kind, raw, review = video_moments("/montage", body)
        except PhotoError as error:
            return failed(error)
        if status != 200 or kind != "video/mp4":
            return video_failed(status, raw)
        os.makedirs(VIDEOS, exist_ok=True)
        name = time.strftime("montage-%Y%m%d-%H%M%S-") + secrets.token_hex(3) + ".mp4"
        path = os.path.join(VIDEOS, name)
        # O_EXCL: a new file only, never one the member already has.
        with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644), "wb") as file:
            file.write(raw)
        # The plan, reasons included, beside the cut: what the model chose and why, what it
        # left out and why, and the seconds cut after the frames were looked at.
        if SPAN:  # planned with the seconds the service set
            segments = [s | {"start": r["planned"][0], "end": r["planned"][1]}
                        for s, r in zip(body["segments"], review, strict=True)]
        cut = [segment | {"start": r["start"], "end": r["end"]} for segment, r in zip(segments, review, strict=True)]
        cut = cut or segments
        with os.fdopen(os.open(path[:-4] + ".json", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644), "w") as file:
            json.dump({"style": style, "planned": segments, "segments": cut, "review": review, "skipped": skipped}, file,
                      ensure_ascii=False, indent=2)
        trimmed = [{"segment": n, "planned": [s["start"], s["end"]], "cut": [c["start"], c["end"]], "seen": r["seen"]}
                   for n, (s, c, r) in enumerate(zip(segments, cut, review, strict=True), 1)
                   if (s["start"], s["end"]) != (c["start"], c["end"])]
        seconds = round(sum(segment["end"] - segment["start"] for segment in cut), 2)
        return result({"path": path, "seconds": seconds, "segments": len(cut), "trimmed": trimmed,
                       "seen": [r["seen"] for r in review]})


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
