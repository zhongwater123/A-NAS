"""The photo-to-video demo's tools against a fake product service (standard library only).

Rendering tests need ffmpeg and ffprobe and are skipped without them.
"""

import http.server
import json
import os
import shutil
import subprocess
import tempfile
import threading
import unittest
import urllib.parse

from photo_video_demo import render, tools
from photo_video_demo.client import Client

HAS_FFMPEG = bool(shutil.which("ffmpeg") and shutil.which("ffprobe"))


def still(path, width, height):
    subprocess.run(["ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i",
                    f"color=c=teal:s={width}x{height}", "-frames:v", "1", path], check=True)
    with open(path, "rb") as file:
        return file.read()


def probe(path):
    out = subprocess.run(["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",
                          "stream=width,height,codec_name:format=duration", "-of", "json", path],
                         capture_output=True, text=True, check=True).stdout
    data = json.loads(out)
    return data["streams"][0], float(data["format"]["duration"])


class FakeProduct(http.server.ThreadingHTTPServer):
    """The few routes the tools call, with one test account."""

    def __init__(self):
        super().__init__(("127.0.0.1", 0), Handler)
        self.sign_ins = 0
        self.sessions = set()
        self.views = []
        self.search = {"items": [], "semantic": True, "closest": 0}
        self.search_status = 200
        self.assets = {}
        self.image = b"\xff\xd8\xff\xd9"

    def asset(self, asset_id, **fields):
        self.assets[asset_id] = {"id": asset_id, "libraryId": "lib-p", "name": asset_id + ".jpg",
                                 "mediaType": "image/jpeg", **fields}
        return self.assets[asset_id]


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def send(self, status, body, content_type="application/json", headers=()):
        data = body if isinstance(body, bytes) else json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        for name, value in headers:
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(data)

    def fail(self, status, code):
        self.send(status, {"error": {"code": code, "message": code}})

    def do_POST(self):
        product = self.server
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if self.path != "/api/v1/session" or body != {"username": "tester", "password": "test-password-123"}:
            return self.fail(401, "invalid_credentials")
        product.sign_ins += 1
        token = f"t{product.sign_ins}"
        product.sessions.add(token)
        self.send(200, {"csrfToken": "c"}, headers=[("Set-Cookie", f"anas_session={token}; Path=/; HttpOnly")])

    def do_GET(self):
        product = self.server
        token = (self.headers.get("Cookie") or "").removeprefix("anas_session=")
        if token not in product.sessions:
            return self.fail(401, "unauthenticated")
        url = urllib.parse.urlsplit(self.path)
        parts = [urllib.parse.unquote(part) for part in url.path.split("/")]
        if url.path == "/api/v1/photos/libraries":
            return self.send(200, {"items": [{"id": "lib-p", "kind": "private"}, {"id": "lib-s", "kind": "shared"}]})
        if url.path == "/api/v1/photos/search":
            if product.search_status != 200:
                return self.fail(product.search_status, "photos_unavailable")
            return self.send(200, product.search)
        if parts[:5] == ["", "api", "v1", "photos", "assets"] and parts[5] in product.assets:
            if len(parts) == 6:
                return self.send(200, product.assets[parts[5]])
            if parts[6:] == ["view"]:
                product.views.append(parts[5])
                return self.send(200, product.image, "image/jpeg")
        self.fail(404, "not_found")


class DemoTest(unittest.TestCase):
    def setUp(self):
        self.product = FakeProduct()
        threading.Thread(target=self.product.serve_forever, daemon=True).start()
        self.addCleanup(self.product.server_close)
        self.addCleanup(self.product.shutdown)
        self.client = Client(f"http://127.0.0.1:{self.product.server_port}", "tester", "test-password-123")
        self.out = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.out)

    def refusal(self, fn, *args, **kwargs):
        with self.assertRaises(tools.ToolError) as caught:
            fn(*args, **kwargs)
        return caught.exception.body

    def test_search_keeps_the_assistants_shape(self):
        self.product.search = {
            "semantic": True, "closest": 1, "next": "n2",
            "items": [
                {"id": "photo:1", "libraryId": "lib-p", "name": "beach.jpg", "takenAt": "2026-07-01T09:30:00Z"},
                {"id": "photo:2", "libraryId": "lib-s", "name": "waves.mp4", "video": {"duration": 4}},
            ],
        }
        self.assertEqual(tools.search_photos(self.client, "海边"), {
            "query": "海边", "closest": 1, "next": "n2",
            "photos": [
                {"id": "photo:1", "name": "beach.jpg", "library": "我的", "taken": "2026-07-01 09:30"},
                {"id": "photo:2", "name": "waves.mp4", "library": "共享", "video": True},
            ],
        })

    def test_search_says_when_only_names_matched(self):
        self.product.search = {"items": [], "semantic": False, "closest": 0}
        self.assertIn("note", tools.search_photos(self.client, "海边"))

    def test_search_refuses_a_query_out_of_range(self):
        self.assertEqual(self.refusal(tools.search_photos, self.client, "")["error"], "invalid_query")
        self.assertEqual(self.refusal(tools.search_photos, self.client, "海" * 201)["error"], "invalid_query")
        self.assertEqual(self.product.sign_ins, 0)

    def test_search_passes_on_the_service_error_code(self):
        self.product.search_status = 503
        self.assertEqual(self.refusal(tools.search_photos, self.client, "海边")["error"], "photos_unavailable")

    def test_an_expired_session_signs_in_again_once(self):
        tools.search_photos(self.client, "海边")
        self.product.sessions.clear()
        tools.search_photos(self.client, "海边")
        self.assertEqual(self.product.sign_ins, 2)

    def test_slideshow_refuses_arguments_out_of_range(self):
        for ids, seconds, code in [
            ([], 3, "invalid_asset_ids"),
            (["photo:1"] * 61, 3, "invalid_asset_ids"),
            (["photo:1"], 0.5, "invalid_seconds_per_photo"),
            (["photo:1"], 11, "invalid_seconds_per_photo"),
            (["photo:1"], True, "invalid_seconds_per_photo"),
        ]:
            with self.subTest(ids=len(ids), seconds=seconds):
                body = self.refusal(tools.make_slideshow, self.client, self.out, ids, seconds)
                self.assertEqual(body["error"], code)
        self.assertEqual(self.product.sign_ins, 0)

    def test_slideshow_refuses_missing_trashed_and_video_assets_at_once(self):
        self.product.asset("photo:ok")
        self.product.asset("photo:gone", trash={"trashedAt": "2026-10-01T00:00:00Z"})
        self.product.asset("photo:clip", mediaType="video/mp4", video={"duration": 3})
        body = self.refusal(tools.make_slideshow, self.client, self.out,
                            ["photo:ok", "photo:gone", "photo:clip", "photo:other"])
        self.assertEqual(body["error"], "invalid_assets")
        self.assertEqual(body["assets"], {"not_found": ["photo:gone", "photo:other"], "video": ["photo:clip"]})
        self.assertEqual(self.product.views, [])
        self.assertEqual(os.listdir(self.out), [])

    def test_crossfade_keeps_the_total(self):
        args = render.ffmpeg_args(["a", "b", "c"], 2, True, "out.mp4")
        self.assertEqual(args[args.index("-t") + 1], "6")
        graph = args[args.index("-filter_complex") + 1]
        self.assertEqual([part.rsplit("trim=duration=", 1)[1] for part in graph.split(";")[:3]],
                         ["2.5[v0]", "2.5[v1]", "2[v2]"])
        self.assertIn("xfade=transition=fade:duration=0.5:offset=2[x1]", graph)
        self.assertIn("xfade=transition=fade:duration=0.5:offset=4[x2]", graph)

    @unittest.skipUnless(HAS_FFMPEG, "needs ffmpeg and ffprobe")
    def test_slideshow_renders_landscape_and_portrait_photos(self):
        work = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, work)
        wide, tall = still(os.path.join(work, "w.jpg"), 1600, 900), still(os.path.join(work, "t.jpg"), 900, 1600)
        for asset_id in ("photo:w", "photo:t", "photo:w2"):
            self.product.asset(asset_id)
        for crossfade in (False, True):
            with self.subTest(crossfade=crossfade):
                self.product.image = tall if crossfade else wide
                made = tools.make_slideshow(self.client, self.out, ["photo:w", "photo:t", "photo:w2"], 1.5, crossfade)
                self.assertEqual((made["seconds"], made["photos"]), (4.5, 3))
                stream, duration = probe(made["path"])
                self.assertEqual((stream["codec_name"], stream["width"], stream["height"]), ("h264", 1920, 1080))
                self.assertAlmostEqual(duration, 4.5, delta=0.1)
        self.assertEqual(len(os.listdir(self.out)), 2)

    @unittest.skipUnless(HAS_FFMPEG, "needs ffmpeg and ffprobe")
    def test_a_failed_render_leaves_nothing(self):
        self.product.asset("photo:1")
        self.product.image = b"\xff\xd8 not really a jpeg"
        body = self.refusal(tools.make_slideshow, self.client, self.out, ["photo:1"])
        self.assertEqual(body["error"], "render_failed")
        self.assertEqual(os.listdir(self.out), [])


if __name__ == "__main__":
    unittest.main()
