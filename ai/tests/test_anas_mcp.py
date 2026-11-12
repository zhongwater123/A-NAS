"""The photo tools hand the model what the device read from a photo, and say what to do when refused.

The tests stand in for the MCP SDK, which only the assistant's runtime has, and
for the photo service's assistant socket: `photos` answers from a table of
paths. What the photo service itself gives an assistant (handles in place of
numbers, no view of a document) is tested with it, in internal/photos.
"""

import importlib.util
import json
import sys
import types
import unittest
from pathlib import Path

SOURCE = Path(__file__).resolve().parents[1] / "assistant-spike" / "anas_mcp.py"


class _Result:
    def __init__(self, **fields):
        self.__dict__.update(fields)


class _Server:
    """Stands in for the MCP SDK's server: a tool stays the plain function."""

    def __init__(self, name, instructions=""):
        self.name = name

    def tool(self, **_):
        return lambda function: function


def load():
    stand_ins = {
        "mcp": types.ModuleType("mcp"),
        "mcp.server": types.ModuleType("mcp.server"),
        "mcp_types": types.ModuleType("mcp_types"),
    }
    stand_ins["mcp.server"].MCPServer = _Server
    for name in ("CallToolResult", "ImageContent", "TextContent", "ToolAnnotations"):
        setattr(stand_ins["mcp_types"], name, _Result)
    saved = {name: sys.modules.get(name) for name in stand_ins}
    sys.modules.update(stand_ins)
    try:
        spec = importlib.util.spec_from_file_location("anas_mcp", SOURCE)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module
    finally:
        for name, previous in saved.items():
            if previous is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = previous


anas = load()

HANDLE = "⟦身份证号·1a2b3c⟧"
FRONT = {
    "status": "read", "kind": "id_card", "complete": True,
    "text": f"姓名 张三\n性别 男\n住址 某市某路1号\n公民身份号码 {HANDLE}",
    "fields": [
        {"name": "姓名", "value": "张三", "read": "张三", "passed": True, "sensitive": False},
        {"name": "民族", "value": "", "read": "", "passed": False, "sensitive": False},
        {"name": "住址", "value": "某市某路1号", "read": "某市某路1号", "passed": False, "sensitive": False},
        {"name": "公民身份号码", "value": HANDLE, "read": HANDLE, "passed": True, "sensitive": True},
    ],
}
SCREENSHOT = {"status": "read", "kind": "plain", "complete": False, "text": "会议通知 " * 60, "fields": []}


def asset(photo_id, name, text=""):
    item = {"id": photo_id, "name": name, "libraryId": "lib:alice", "mediaType": "image/jpeg",
            "width": 4000, "height": 3000, "sizeBytes": 1234}
    if text:
        item["text"] = text
    return item


class PhotoToolTests(unittest.TestCase):
    def setUp(self):
        self.asked = []
        self.answers = {
            "/libraries": {"items": [{"id": "lib:alice", "kind": "private"}, {"id": "lib:shared", "kind": "shared"}]},
        }
        self.addCleanup(setattr, anas, "photos", anas.photos)
        anas.photos = self.photos

    def photos(self, path, **query):
        self.asked.append(path)
        answer = self.answers[path]
        if isinstance(answer, Exception):
            raise answer
        return answer

    def data(self, result):
        self.assertFalse(getattr(result, "is_error", False), getattr(result, "content", None))
        return json.loads(result.content[0].text)

    def test_a_search_brings_what_was_read_from_the_photos_that_matched_by_it(self):
        self.answers["/search"] = {
            "items": [asset("photo:named", "身份证.jpg"), asset("photo:front", "IMG_1.jpg"), asset("photo:shot", "IMG_2.png"),
                      asset("photo:sea", "IMG_3.jpg")],
            "names": 1, "texts": 2, "closest": 3, "semantic": True,
        }
        self.answers["/assets/photo%3Anamed/text"] = {"status": "none", "complete": False, "fields": []}
        self.answers["/assets/photo%3Afront/text"] = FRONT
        self.answers["/assets/photo%3Ashot/text"] = SCREENSHOT

        named, front, shot, sea = self.data(anas.search_photos("身份证"))["photos"]

        self.assertEqual(front["document"], "身份证人像面")
        # The number is the handle the photo service gave, untouched; empty fields are left out.
        self.assertEqual(front["fields"], {"姓名": "张三", "住址": "某市某路1号", "公民身份号码": HANDLE})
        self.assertEqual(front["unchecked"], ["住址"])
        # A document's fields say it all; its whole text comes with photo_details.
        self.assertNotIn("text", front)
        # A photo that is no document brings the start of its text.
        self.assertEqual(len(shot["text"]), anas.EXCERPT + 1)
        self.assertTrue(shot["text"].endswith("…"))
        for plain in (named, sea):
            self.assertFalse({"document", "fields", "text"} & plain.keys())
        # Only the name and text matches were asked about: the rest matched by the picture.
        self.assertNotIn("/assets/photo%3Asea/text", self.asked)

    def test_a_later_page_and_a_failed_read_leave_the_results_as_they_are(self):
        self.answers["/search"] = {"items": [asset("photo:front", "IMG_1.jpg")], "names": 0, "texts": 1, "closest": 1,
                                   "semantic": True}
        self.answers["/assets/photo%3Afront/text"] = anas.PhotoError("相册暂时连不上，请稍后再试")
        self.assertEqual(self.data(anas.search_photos("身份证"))["photos"][0]["name"], "IMG_1.jpg")

        self.asked.clear()
        self.answers["/assets/photo%3Afront/text"] = FRONT
        self.assertNotIn("fields", self.data(anas.search_photos("身份证", page="next"))["photos"][0])
        self.assertNotIn("/assets/photo%3Afront/text", self.asked)

    def test_details_carry_the_whole_text_and_say_when_it_is_not_all_there(self):
        self.answers["/assets/photo%3Ashot"] = asset("photo:shot", "IMG_2.png", text="read")
        self.answers["/assets/photo%3Ashot/metadata"] = {"exif": {}}
        self.answers["/assets/photo%3Ashot/text"] = SCREENSHOT
        details = self.data(anas.photo_details("photo:shot"))
        self.assertEqual(details["text"], SCREENSHOT["text"])
        self.assertEqual(details["text_status"], "文字没有读完，后面的缺失")

        self.answers["/assets/photo%3Ashot"] = asset("photo:shot", "IMG_2.png", text="queued")
        self.answers["/assets/photo%3Ashot/text"] = {"status": "queued", "complete": False, "fields": []}
        self.assertEqual(self.data(anas.photo_details("photo:shot"))["text_status"], "照片上的文字还没读完")

        # A photo the device reads no text from is not asked about.
        self.asked.clear()
        self.answers["/assets/photo%3Ashot"] = asset("photo:shot", "IMG_2.png")
        self.assertNotIn("text", self.data(anas.photo_details("photo:shot")))
        self.assertNotIn("/assets/photo%3Ashot/text", self.asked)


class RefusalTests(unittest.TestCase):
    def test_a_refused_view_tells_the_model_what_to_do_instead(self):
        refusal = json.dumps({"error": {"code": "sensitive_photo", "message": "the photo is an ID card"}}).encode()
        error = anas.refused(403, refusal)
        self.assertEqual(error.code, "sensitive_photo")
        self.assertIn("photo_details", str(error))
        self.assertIn("句柄", str(error))
        self.assertIn("还没读完", str(anas.refused(403, b'{"error": {"code": "unread_photo", "message": "x"}}')))

    def test_any_other_refusal_keeps_the_photo_services_words(self):
        error = anas.refused(404, b'{"error": {"code": "not_found", "message": "photo library item was not found"}}')
        self.assertEqual((error.code, str(error)), ("not_found", "photo library item was not found"))
        self.assertEqual(str(anas.refused(502, b"<html>")), "相册返回了看不懂的结果（502）")
        self.assertEqual(str(anas.refused(500, b"{}")), "相册返回 500")


if __name__ == "__main__":
    unittest.main()
