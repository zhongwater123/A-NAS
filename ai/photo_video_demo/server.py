"""The demo's stdio MCP server; run it from ai/ as `python -m photo_video_demo.server`.

Connection and test account come from the environment, never the repository:
ANAS_DEMO_URL (such as http://127.0.0.1:18080), ANAS_DEMO_USER,
ANAS_DEMO_PASSWORD and ANAS_DEMO_OUTPUT (the directory videos are written to).
"""

import json
import os

from mcp.server import MCPServer
from mcp_types import CallToolResult, TextContent, ToolAnnotations

from photo_video_demo import tools
from photo_video_demo.client import Client

client = Client(os.environ["ANAS_DEMO_URL"], os.environ["ANAS_DEMO_USER"], os.environ["ANAS_DEMO_PASSWORD"])
output_dir = os.path.abspath(os.path.expanduser(os.environ["ANAS_DEMO_OUTPUT"]))

server = MCPServer("anas-photo-video-demo", instructions=(
    "A-NAS 相册的照片拼视频演示。先用 search_photos 找照片，再把选中的照片 ID 按顺序交给 make_slideshow。"
    "只拼接照片，不剪视频；找不到合适的照片时如实告诉用户，不用无关照片凑数。"))


def result(data: dict) -> CallToolResult:
    return CallToolResult(content=[TextContent(type="text", text=json.dumps(data, ensure_ascii=False))],
                          structured_content=data)


def failed(error: tools.ToolError) -> CallToolResult:
    return CallToolResult(content=[TextContent(type="text", text=json.dumps(error.body, ensure_ascii=False))],
                          is_error=True)


@server.tool(annotations=ToolAnnotations(read_only_hint=True), structured_output=False, description=(
    "用一句话按画面内容、名称和标签搜索测试账号的私有图库与共享图库，最相关的在前：前 closest 张是最接近的一组，"
    "其余不太相关。搜索不看时间，请按 taken 自己筛选。query：要找的内容，如“海边”“小明 生日”（可含人物全名）；"
    "page：上一次结果中的 next。结果只有名称、拍摄时间（taken）、所在图库、标签与相册，没有画面；"
    "video 为 true 的是视频，不能交给 make_slideshow。note 出现时表示只按名称与标签匹配。"))
def search_photos(query: str, page: str = ""):
    try:
        return result(tools.search_photos(client, query, page))
    except tools.ToolError as error:
        return failed(error)


@server.tool(structured_output=False, description=(
    f"把照片按给出的顺序拼成一段 1920×1080 的 MP4（无声），写到开发机的输出目录，返回文件路径与时长。"
    f"asset_ids：search_photos 结果中的照片 id，1–{tools.PHOTOS_MAX} 个，按出现顺序；"
    f"seconds_per_photo：每张的秒数，{tools.SECONDS_MIN}–{tools.SECONDS_MAX}，可带小数，默认 3；"
    "总时长 = 张数 × seconds_per_photo，要凑出指定总时长时据此选张数与每张秒数。"
    "crossfade：相邻两张淡入淡出，默认否，不改变总时长。有视频、不存在或已删除的 id 时整次拒绝。"))
def make_slideshow(asset_ids: list[str], seconds_per_photo: float = 3, crossfade: bool = False):
    try:
        return result(tools.make_slideshow(client, output_dir, asset_ids, seconds_per_photo, crossfade))
    except tools.ToolError as error:
        return failed(error)


if __name__ == "__main__":
    server.run()
