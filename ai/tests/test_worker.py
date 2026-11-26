"""Protocol tests of the AI Worker with the fake provider (standard library only)."""

import base64
import gc
import hashlib
import importlib.util
import json
import os
import shutil
import socket
import struct
import subprocess
import tempfile
import threading
import time
import unittest
import weakref
from unittest.mock import patch

from anas_ai import protocol, providers, worker


def call(path, request, image=None, images=None, *, timeout=5):
    """Sends one request the way internal/aiworker does and returns the response."""
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
        conn.settimeout(timeout)
        conn.connect(path)
        payload = json.dumps(request).encode()
        frame = struct.pack(">I", len(payload)) + payload
        files = [image] if image is not None else images or []
        if files:
            socket.send_fds(conn, [frame], [file.fileno() for file in files])
        else:
            conn.sendall(frame)
        header = b""
        while len(header) < 4:
            part = conn.recv(4 - len(header))
            if not part:
                raise EOFError("Worker closed before the response header")
            header += part
        (length,) = struct.unpack(">I", header)
        body = b""
        while len(body) < length:
            part = conn.recv(length - len(body))
            if not part:
                raise EOFError("Worker closed inside the response body")
            body += part
        return json.loads(body)


class Failing:
    """A provider whose image embedding raises error."""

    model, dimensions = "failing", 4

    def __init__(self, error):
        self.error = error

    def embed_image(self, data):
        raise self.error


class WorkerTest(unittest.TestCase):
    def start(self, provider, reason=None, idle_seconds=30, detector=None, faces=None, animals=None, started=0,
              reader=None, scorer=None):
        directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, directory, True)
        path = os.path.join(directory, "ai.sock")
        listener = worker.listening_socket(path)
        self.addCleanup(listener.close)
        thread = threading.Thread(target=worker.serve,
                                  args=(listener, provider, reason, idle_seconds, detector, faces, animals, started,
                                        reader, scorer),
                                  daemon=True)
        thread.start()
        return path, thread

    def image(self, content):
        file = self.enterContext(tempfile.TemporaryFile())
        file.write(content)
        file.seek(0)
        return file

    def test_info_and_deterministic_image_vectors(self):
        path, _ = self.start(providers.FakeProvider(dimensions=8))
        self.assertEqual(call(path, {"op": "info"}),
                         {"ok": True, "model": "fake-sha256-8", "dimensions": 8, "memoryBytes": 0})

        first = call(path, {"op": "embed_image"}, self.image(b"thumbnail bytes"))
        again = call(path, {"op": "embed_image"}, self.image(b"thumbnail bytes"))
        other = call(path, {"op": "embed_image"}, self.image(b"another thumbnail"))
        self.assertTrue(first["ok"])
        self.assertEqual(first["vector"], again["vector"])
        self.assertNotEqual(first["vector"], other["vector"])
        vector = struct.unpack("<8f", base64.b64decode(first["vector"]))
        self.assertAlmostEqual(sum(value * value for value in vector), 1.0, places=5)

        query = call(path, {"op": "embed_query", "text": "海边的猫"})
        self.assertTrue(query["ok"])
        self.assertEqual(query["vector"], call(path, {"op": "embed_query", "text": "海边的猫"})["vector"])
        self.assertNotEqual(query["vector"], call(path, {"op": "embed_query", "text": "车库里的电动车"})["vector"])

    def test_rejects_requests_it_cannot_serve(self):
        path, _ = self.start(providers.FakeProvider())
        self.assertEqual(call(path, {"op": "embed_image"})["error"]["code"], protocol.CODE_INVALID_INPUT)
        self.assertEqual(call(path, {"op": "embed_image"}, self.image(b""))["error"]["code"], protocol.CODE_INVALID_INPUT)
        self.assertEqual(call(path, {"op": "embed_text"})["error"]["code"], protocol.CODE_INTERNAL)
        for request in ({"op": "embed_query"}, {"op": "embed_query", "text": "  "},
                        {"op": "embed_query", "text": 7}, {"op": "embed_query", "text": "猫" * (worker.MAX_QUERY_CHARS + 1)}):
            self.assertEqual(call(path, request)["error"]["code"], protocol.CODE_INVALID_INPUT, request)

        failing, _ = self.start(Failing(providers.InvalidInput("not an image")))
        self.assertEqual(call(failing, {"op": "embed_image"}, self.image(b"x"))["error"]["code"], protocol.CODE_INVALID_INPUT)
        crashing, _ = self.start(Failing(MemoryError()))
        response = call(crashing, {"op": "embed_image"}, self.image(b"x"))
        self.assertEqual(response["error"], {"code": protocol.CODE_INTERNAL, "message": "MemoryError"})

    def test_answers_unavailable_without_its_model(self):
        path, _ = self.start(None, reason="model file missing")
        for request in ({"op": "info"}, {"op": "embed_image"}, {"op": "embed_query", "text": "猫"}):
            self.assertEqual(call(path, request)["error"]["code"], protocol.CODE_UNAVAILABLE)

    def test_detects_objects_with_the_fake(self):
        fake = providers.FakeDetector()
        path, _ = self.start(providers.FakeProvider(dimensions=8), detector=worker.Detector(fake))
        self.assertEqual(call(path, {"op": "info"})["detector"], fake.model)
        seen = 0
        for content in (b"photo %d" % i for i in range(20)):
            first = call(path, {"op": "detect_objects"}, self.image(content))
            self.assertTrue(first["ok"], first)
            self.assertEqual(first["model"], fake.model)
            self.assertEqual(first, call(path, {"op": "detect_objects"}, self.image(content)))
            scores = [box[1] for box in first["boxes"]]
            self.assertEqual(scores, sorted(scores, reverse=True))
            for label, score, *corners in first["boxes"]:
                self.assertTrue(1 <= label <= 365 and providers.MIN_BOX_SCORE <= score <= 1)
                self.assertTrue(all(0 <= value <= 1 for value in corners))
            seen += len(first["boxes"])
        self.assertGreater(seen, 0)
        self.assertEqual(call(path, {"op": "detect_objects"})["error"]["code"], protocol.CODE_INVALID_INPUT)
        self.assertEqual(call(path, {"op": "detect_objects"}, self.image(b""))["error"]["code"], protocol.CODE_INVALID_INPUT)

    def test_finds_faces_with_the_fake(self):
        fake = providers.FakeFaces()
        path, _ = self.start(providers.FakeProvider(dimensions=8), faces=worker.Detector(fake))
        info = call(path, {"op": "info"})
        self.assertEqual(info["faceModel"], fake.model)
        self.assertNotIn("detector", info)
        vectors = []
        for content in (b"photo %d" % i for i in range(30)):
            found = call(path, {"op": "detect_faces"}, self.image(content))
            self.assertTrue(found["ok"], found)
            self.assertEqual(found["model"], fake.model)
            self.assertEqual(found, call(path, {"op": "detect_faces"}, self.image(content)))
            for face in found["faces"]:
                self.assertEqual(len(face["box"]), 4)
                self.assertEqual(len(face["landmarks"]), 10)
                self.assertTrue(all(0 <= value <= 1 for value in face["box"] + face["landmarks"]))
                vector = struct.unpack(f"<{fake.dimensions}f", base64.b64decode(face["vector"]))
                self.assertAlmostEqual(sum(value * value for value in vector), 1.0, places=4)
                vectors.append((content, vector))
        # The same fake person comes back in other photos with nearly the same
        # vector, and different people are far apart.
        similarities = [sum(x * y for x, y in zip(a, b, strict=True))
                        for i, (photo_a, a) in enumerate(vectors) for photo_b, b in vectors[i + 1:] if photo_a != photo_b]
        self.assertTrue(any(value > 0.99 for value in similarities))
        self.assertTrue(any(value < 0.5 for value in similarities))
        self.assertEqual(call(path, {"op": "detect_faces"})["error"]["code"], protocol.CODE_INVALID_INPUT)
        self.assertEqual(call(path, {"op": "detect_faces"}, self.image(b""))["error"]["code"],
                         protocol.CODE_INVALID_INPUT)
        self.assertTrue(fake.loaded)
        self.assertEqual(call(path, {"op": "release", "what": "detector"}), {"ok": True})
        self.assertFalse(fake.loaded)

    def test_embeds_animals_with_the_fake(self):
        fake = providers.FakeAnimals()
        path, _ = self.start(providers.FakeProvider(dimensions=8), animals=worker.Detector(fake))
        self.assertEqual(call(path, {"op": "info"})["animalModel"], fake.model)
        boxes = [[0.1, 0.2, 0.5, 0.9], [0.5, 0.1, 0.95, 0.6]]
        vectors = []
        for content in (b"photo %d" % i for i in range(20)):
            found = call(path, {"op": "embed_animals", "boxes": boxes}, self.image(content))
            self.assertTrue(found["ok"], found)
            self.assertEqual(found["model"], fake.model)
            self.assertEqual(len(found["vectors"]), len(boxes))
            self.assertEqual(found, call(path, {"op": "embed_animals", "boxes": boxes}, self.image(content)))
            for encoded in found["vectors"]:
                vector = struct.unpack(f"<{fake.dimensions}f", base64.b64decode(encoded))
                self.assertAlmostEqual(sum(value * value for value in vector), 1.0, places=4)
                vectors.append(vector)
        similarities = [sum(x * y for x, y in zip(a, b, strict=True)) for i, a in enumerate(vectors) for b in vectors[i + 1:]]
        self.assertTrue(any(value > 0.99 for value in similarities))
        self.assertTrue(any(value < 0.5 for value in similarities))
        for bad in (None, [], [[0.1, 0.2, 0.5]], [[0.5, 0.2, 0.1, 0.9]], [[0, 0, 1, 1.5]], [[0, 0, True, 1]],
                    [[0, 0, 1, 1]] * (providers.MAX_ANIMALS + 1)):
            response = call(path, {"op": "embed_animals", "boxes": bad}, self.image(b"photo"))
            self.assertEqual(response["error"]["code"], protocol.CODE_INVALID_INPUT, bad)
        self.assertEqual(call(path, {"op": "embed_animals", "boxes": boxes})["error"]["code"],
                         protocol.CODE_INVALID_INPUT)
        self.assertEqual(call(path, {"op": "embed_animals", "boxes": boxes}, self.image(b""))["error"]["code"],
                         protocol.CODE_INVALID_INPUT)
        self.assertTrue(fake.loaded)
        self.assertEqual(call(path, {"op": "release", "what": "detector"}), {"ok": True})
        self.assertFalse(fake.loaded)

    def test_animals_are_unavailable_without_their_model(self):
        path, _ = self.start(providers.FakeProvider(dimensions=8), faces=worker.Detector(providers.FakeFaces()))
        self.assertNotIn("animalModel", call(path, {"op": "info"}))
        response = call(path, {"op": "embed_animals", "boxes": [[0, 0, 1, 1]]}, self.image(b"photo"))
        self.assertEqual(response["error"], {"code": protocol.CODE_UNAVAILABLE, "message": "no animal model"})

    def test_faces_are_unavailable_without_their_models(self):
        path, _ = self.start(providers.FakeProvider(dimensions=8), detector=worker.Detector(providers.FakeDetector()))
        self.assertNotIn("faceModel", call(path, {"op": "info"}))
        response = call(path, {"op": "detect_faces"}, self.image(b"photo"))
        self.assertEqual(response["error"], {"code": protocol.CODE_UNAVAILABLE, "message": "no face models"})
        self.assertTrue(call(path, {"op": "detect_objects"}, self.image(b"photo"))["ok"])

    def test_vectors_go_on_without_the_detector(self):
        path, _ = self.start(providers.FakeProvider(dimensions=8),
                             detector=worker.Detector(reason="detector file missing"))
        self.assertNotIn("detector", call(path, {"op": "info"}))
        response = call(path, {"op": "detect_objects"}, self.image(b"photo"))
        self.assertEqual(response["error"], {"code": protocol.CODE_UNAVAILABLE, "message": "detector file missing"})
        self.assertTrue(call(path, {"op": "embed_image"}, self.image(b"photo"))["ok"])

    def test_hanging_up_stops_the_detection(self):
        fake = providers.FakeDetector(delay=30)
        path, _ = self.start(providers.FakeProvider(), detector=worker.Detector(fake))
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
            conn.connect(path)
            payload = json.dumps({"op": "detect_objects"}).encode()
            socket.send_fds(conn, [struct.pack(">I", len(payload)) + payload], [self.image(b"photo").fileno()])
            time.sleep(0.2)
        started = time.monotonic()
        self.assertTrue(call(path, {"op": "info"})["ok"])
        self.assertLess(time.monotonic() - started, 5)
        self.assertEqual(fake.cancelled, 1)

    def test_a_cancelled_detection_lets_go_of_what_it_held(self):
        # Without the cyclic collector: the stored error's traceback must not
        # keep the detection's frame, and the session in it, alive.
        class Session:
            pass

        class Holding(providers.FakeDetector):
            def __init__(self):
                super().__init__()
                self.sessions = []

            def detect(self, data, run):
                session = Session()
                self.sessions.append(weakref.ref(session))
                while not run.terminate:
                    time.sleep(0.01)
                raise providers.Cancelled()

        gc.disable()
        self.addCleanup(gc.enable)
        holding = Holding()
        path, _ = self.start(providers.FakeProvider(), detector=worker.Detector(holding))
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
            conn.connect(path)
            payload = json.dumps({"op": "detect_objects"}).encode()
            socket.send_fds(conn, [struct.pack(">I", len(payload)) + payload], [self.image(b"photo").fileno()])
            time.sleep(0.2)
        self.assertTrue(call(path, {"op": "info"})["ok"])
        self.assertEqual(len(holding.sessions), 1)
        self.assertIsNone(holding.sessions[0]())

    def test_release_frees_the_detector_and_can_end_the_worker(self):
        fake = providers.FakeDetector()
        path, thread = self.start(providers.FakeProvider(), detector=worker.Detector(fake))
        self.assertTrue(call(path, {"op": "detect_objects"}, self.image(b"photo"))["ok"])
        self.assertTrue(fake.loaded)
        self.assertEqual(call(path, {"op": "release", "what": "detector"}), {"ok": True})
        self.assertFalse(fake.loaded)
        self.assertTrue(call(path, {"op": "detect_objects"}, self.image(b"photo"))["ok"])
        self.assertEqual(call(path, {"op": "release"})["error"]["code"], protocol.CODE_INVALID_INPUT)
        self.assertEqual(call(path, {"op": "release", "what": "all"}), {"ok": True})
        thread.join(timeout=5)
        self.assertFalse(thread.is_alive())
        self.assertFalse(fake.loaded)

    def test_reads_text_and_fields(self):
        fake = providers.FakeReader()
        fake.memory = 3 << 30
        path, _ = self.start(providers.FakeProvider(dimensions=8), reader=worker.Detector(fake), started=300 << 20)
        info = call(path, {"op": "info"})
        # Reading is a phase of its own: its memory is not added to the other models'.
        self.assertEqual((info["ocrModel"], info["ocrMemoryBytes"], info["memoryBytes"]),
                         (fake.model, (300 << 20) + (3 << 30), 300 << 20))
        text = call(path, {"op": "read_text", "kind": "text"}, self.image(b"photo"))
        self.assertEqual(text["model"], fake.model)
        self.assertTrue(text["reading"]["text"].startswith("fake text "))
        self.assertEqual(text["reading"]["finish"], "stop")
        schema = {"金额": "", "明细": [{"名称": ""}]}
        fields = call(path, {"op": "read_text", "kind": "fields", "schema": schema}, self.image(b"photo"))
        self.assertEqual(set(json.loads(fields["reading"]["text"])), {"金额", "明细"})
        for bad in ({"op": "read_text"}, {"op": "read_text", "kind": "fields"},
                    {"op": "read_text", "kind": "fields", "schema": {}},
                    {"op": "read_text", "kind": "fields", "schema": ["金额"]},
                    {"op": "read_text", "kind": "text", "schema": schema},
                    {"op": "read_text", "kind": "summary"}):
            self.assertEqual(call(path, bad, self.image(b"photo"))["error"]["code"], protocol.CODE_INVALID_INPUT, bad)
        self.assertEqual(call(path, {"op": "read_text", "kind": "text"})["error"]["code"], protocol.CODE_INVALID_INPUT)
        self.assertEqual(call(path, {"op": "read_text", "kind": "text"}, self.image(b""))["error"]["code"],
                         protocol.CODE_INVALID_INPUT)
        self.assertTrue(fake.loaded)
        self.assertEqual(call(path, {"op": "release", "what": "detector"}), {"ok": True})
        self.assertFalse(fake.loaded)

    def test_reading_text_is_unavailable_without_a_reader(self):
        path, _ = self.start(providers.FakeProvider(dimensions=8), reader=worker.Detector(reason="no OCR model"))
        self.assertNotIn("ocrModel", call(path, {"op": "info"}))
        response = call(path, {"op": "read_text", "kind": "text"}, self.image(b"photo"))
        self.assertEqual(response["error"], {"code": protocol.CODE_UNAVAILABLE, "message": "no OCR model"})

    def test_hanging_up_stops_the_reading(self):
        fake = providers.FakeReader(delay=30)
        path, _ = self.start(providers.FakeProvider(), reader=worker.Detector(fake))
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
            conn.connect(path)
            payload = json.dumps({"op": "read_text", "kind": "text"}).encode()
            socket.send_fds(conn, [struct.pack(">I", len(payload)) + payload], [self.image(b"photo").fileno()])
            time.sleep(0.2)
        started = time.monotonic()
        self.assertTrue(call(path, {"op": "info"})["ok"])
        self.assertLess(time.monotonic() - started, 5)
        self.assertEqual(fake.cancelled, 1)

    def test_scores_photos_with_the_fake(self):
        fake = providers.FakeScorer()
        fake.memory = 2 << 30
        path, _ = self.start(providers.FakeProvider(dimensions=8), scorer=worker.Detector(fake), started=300 << 20)
        info = call(path, {"op": "info"})
        # Scoring is a phase of its own: its memory is not added to the other models'.
        self.assertEqual((info["aestheticModel"], info["aestheticMemoryBytes"], info["memoryBytes"]),
                         (fake.model, (300 << 20) + (2 << 30), 300 << 20))
        seen = set()
        for content in (b"photo %d" % i for i in range(10)):
            scored = call(path, {"op": "score_aesthetic"}, self.image(content))
            self.assertTrue(scored["ok"], scored)
            self.assertEqual(scored["model"], fake.model)
            self.assertEqual(scored, call(path, {"op": "score_aesthetic"}, self.image(content)))
            self.assertEqual(set(scored["scores"]), {"aesthetic", "technical"})
            self.assertTrue(all(1 <= value <= 5 for value in scored["scores"].values()))
            seen.add(scored["scores"]["aesthetic"])
        self.assertGreater(len(seen), 1)
        self.assertEqual(call(path, {"op": "score_aesthetic"})["error"]["code"], protocol.CODE_INVALID_INPUT)
        self.assertEqual(call(path, {"op": "score_aesthetic"}, self.image(b""))["error"]["code"],
                         protocol.CODE_INVALID_INPUT)
        self.assertTrue(fake.loaded)
        self.assertEqual(call(path, {"op": "release", "what": "detector"}), {"ok": True})
        self.assertFalse(fake.loaded)

    def test_scoring_is_unavailable_without_a_scorer(self):
        path, _ = self.start(providers.FakeProvider(dimensions=8), scorer=worker.Detector(reason="no scorer bundle"))
        self.assertNotIn("aestheticModel", call(path, {"op": "info"}))
        response = call(path, {"op": "score_aesthetic"}, self.image(b"photo"))
        self.assertEqual(response["error"], {"code": protocol.CODE_UNAVAILABLE, "message": "no scorer bundle"})
        self.assertTrue(call(path, {"op": "embed_image"}, self.image(b"photo"))["ok"])

    def test_hanging_up_stops_the_scoring(self):
        fake = providers.FakeScorer(delay=30)
        path, _ = self.start(providers.FakeProvider(), scorer=worker.Detector(fake))
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
            conn.connect(path)
            payload = json.dumps({"op": "score_aesthetic"}).encode()
            socket.send_fds(conn, [struct.pack(">I", len(payload)) + payload], [self.image(b"photo").fileno()])
            time.sleep(0.2)
        started = time.monotonic()
        self.assertTrue(call(path, {"op": "info"})["ok"])
        self.assertLess(time.monotonic() - started, 5)
        self.assertEqual(fake.cancelled, 1)

    def card(self, count=3, content=b"frame"):
        """A describe_frames request for count key frames, a beat each, and the frames."""
        request = {"op": "describe_frames", "offsets": [2.0 * k for k in range(count)],
                   "groups": list(range(1, count + 1)), "duration": 2.0 * count}
        return request, [self.image(content + str(k).encode()) for k in range(count)]

    def test_describes_a_card_from_its_key_frames(self):
        fake = providers.FakeReader()
        path, _ = self.start(providers.FakeProvider(dimensions=8), reader=worker.Detector(fake))
        request, frames = self.card(4)
        response = call(path, request, images=frames)
        self.assertTrue(response["ok"], response)
        self.assertEqual((response["model"], response["template"]), (fake.model, "montage-card/v2"))
        description = response["description"]
        self.assertEqual([beat["frames"] for beat in description["beats"]], [[1, 1], [2, 2], [3, 3], [4, 4]])
        self.assertEqual(set(description), {"camera", "beats", "process", "summary", "scene"})
        request, frames = self.card(4)
        self.assertEqual(call(path, request, images=frames)["description"], description)
        # Frames grouped two to a beat make two beats.
        request["groups"] = [1, 1, 2, 2]
        response = call(path, request, images=self.card(4)[1])
        self.assertEqual([beat["frames"] for beat in response["description"]["beats"]], [[1, 2], [3, 4]])
        self.assertTrue(fake.loaded)

    def test_a_card_out_of_bounds_is_an_invalid_request(self):
        path, _ = self.start(providers.FakeProvider(dimensions=8), reader=worker.Detector(providers.FakeReader()))
        # More than 20 key frames: the kernel drops the rest, and the request is refused whole.
        request, frames = self.card(21)
        self.assertEqual(call(path, request, images=frames)["error"]["code"], protocol.CODE_INVALID_REQUEST)
        request, frames = self.card(2)
        self.assertEqual(call(path, request, images=frames)["error"]["code"], protocol.CODE_INVALID_REQUEST)
        request, frames = self.card(20)
        self.assertTrue(call(path, request, images=frames)["ok"])
        request, frames = self.card(3)
        for change in ({"offsets": [0, 2]}, {"offsets": [0, 4, 2]}, {"offsets": [-1, 2, 4]}, {"offsets": [0, "2", 4]},
                       {"groups": [1, 1]}, {"groups": [2, 2, 3]}, {"groups": [1, 3, 3]}, {"groups": [1, 2, 1]},
                       {"groups": [1, True, 2]}, {"duration": 3}, {"duration": None}, {"duration": float("nan")},
                       {"offsets": None}, {"offsets": 5}, {"offsets": True}, {"duration": 10**400}):
            bad = {**request, **change}
            self.assertEqual(call(path, bad, images=frames)["error"]["code"], protocol.CODE_INVALID_REQUEST, change)
        self.assertEqual(call(path, request)["error"]["code"], protocol.CODE_INVALID_REQUEST)
        self.assertTrue(call(path, {"op": "info"})["ok"])
        request, frames = self.card(3, b"")
        frames[1] = self.image(b"")
        self.assertEqual(call(path, request, images=frames)["error"]["code"], protocol.CODE_INVALID_INPUT)
        # One descriptor more than an operation takes is refused as it was.
        self.assertEqual(call(path, {"op": "embed_image"}, images=[self.image(b"a"), self.image(b"b")])["error"]["code"],
                         protocol.CODE_INVALID_INPUT)

    def test_an_answer_that_never_fits_the_schema_is_an_invalid_result(self):
        fake = providers.FakeReader(answers=["{", '{"camera": "海上"}', "```json\n[]\n```"])
        path, _ = self.start(providers.FakeProvider(dimensions=8), reader=worker.Detector(fake))
        request, frames = self.card()
        response = call(path, request, images=frames)
        self.assertEqual(response["error"]["code"], protocol.CODE_INVALID_RESULT)
        self.assertEqual(response["error"]["message"].count(";"), 2)

    def test_a_card_past_the_context_is_invalid_input(self):
        path, _ = self.start(providers.FakeProvider(dimensions=8), reader=worker.Detector(providers.FakeReader()))
        request, frames = self.card(3, providers.FAKE_REFUSE)
        self.assertEqual(call(path, request, images=frames)["error"]["code"], protocol.CODE_INVALID_INPUT)

    def test_describing_is_unavailable_without_a_reader(self):
        path, _ = self.start(providers.FakeProvider(dimensions=8), reader=worker.Detector(reason="no OCR model"))
        request, frames = self.card()
        self.assertEqual(call(path, request, images=frames)["error"],
                         {"code": protocol.CODE_UNAVAILABLE, "message": "no OCR model"})

    def test_hanging_up_stops_the_description(self):
        fake = providers.FakeReader(delay=30)
        path, _ = self.start(providers.FakeProvider(), reader=worker.Detector(fake))
        request, frames = self.card()
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
            conn.connect(path)
            payload = json.dumps(request).encode()
            socket.send_fds(conn, [struct.pack(">I", len(payload)) + payload], [frame.fileno() for frame in frames])
            time.sleep(0.2)
        started = time.monotonic()
        self.assertTrue(call(path, {"op": "info"})["ok"])
        self.assertLess(time.monotonic() - started, 5)
        self.assertEqual(fake.cancelled, 1)

    def test_leaves_cores_for_the_user(self):
        self.assertEqual(providers.inference_threads(), max(1, len(os.sched_getaffinity(0)) - providers.RESERVED_CORES))

    def test_exits_when_idle_and_survives_bad_frames(self):
        path, thread = self.start(providers.FakeProvider(), idle_seconds=1)
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
            conn.connect(path)
            conn.sendall(struct.pack(">I", protocol.MAX_FRAME + 1))
        self.assertTrue(call(path, {"op": "info"})["ok"])
        started = time.monotonic()
        thread.join(timeout=10)
        self.assertFalse(thread.is_alive())
        self.assertLess(time.monotonic() - started, 10)

    def test_info_tells_what_the_worker_holds_with_every_model_loaded(self):
        embedder, detector, faces = providers.FakeProvider(dimensions=8), providers.FakeDetector(), providers.FakeFaces()
        embedder.memory, detector.memory, faces.memory = 128 << 20, 272 << 20, 400 << 20
        # The animal model could not load, so it takes nothing.
        path, _ = self.start(embedder, detector=worker.Detector(detector), faces=worker.Detector(faces),
                             animals=worker.Detector(reason="no animal model"), started=300 << 20)
        self.assertEqual(call(path, {"op": "info"})["memoryBytes"], (300 + 128 + 272 + 400) << 20)

    def test_the_worker_knows_what_it_holds_once_started(self):
        self.assertGreater(worker.started_memory(), 0)


class ManifestTest(unittest.TestCase):
    def test_only_the_pinned_file_passes(self):
        directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, directory, True)
        model = os.path.join(directory, "model.litertlm")
        with open(model, "wb") as file:
            file.write(b"weights")
        manifest = os.path.join(directory, "manifest.json")

        def pin(size, digest, memory=1 << 20):
            with open(manifest, "w", encoding="utf-8") as file:
                fields = {"name": "test", "sizeBytes": size, "sha256": digest}
                if memory is not None:
                    fields["memoryBytes"] = memory
                json.dump(fields, file)

        pin(7, hashlib.sha256(b"weights").hexdigest())
        self.assertEqual(providers.verify_model(model, manifest)["name"], "test")
        # The gate weighs the model's memory before loading it, so a manifest
        # must give it.
        for memory in (None, -1, True, "1 MiB"):
            pin(7, hashlib.sha256(b"weights").hexdigest(), memory)
            with self.assertRaises(providers.Unavailable):
                providers.verify_model(model, manifest)
        pin(8, hashlib.sha256(b"weights").hexdigest())
        with self.assertRaises(providers.Unavailable):
            providers.verify_model(model, manifest)
        pin(7, hashlib.sha256(b"Weights").hexdigest())
        with self.assertRaises(providers.Unavailable):
            providers.verify_model(model, manifest)
        with self.assertRaises(providers.Unavailable):
            providers.verify_model(os.path.join(directory, "absent.litertlm"), manifest)

    @unittest.skipIf(importlib.util.find_spec("mediapipe"), "MediaPipe is installed")
    def test_a_runtime_without_mediapipe_is_unavailable(self):
        directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, directory, True)
        model = os.path.join(directory, "model.litertlm")
        with open(model, "wb") as file:
            file.write(b"weights")
        manifest = os.path.join(directory, "manifest.json")
        with open(manifest, "w", encoding="utf-8") as file:
            json.dump({"name": "test", "sizeBytes": 7, "memoryBytes": 1 << 20,
                       "sha256": hashlib.sha256(b"weights").hexdigest()}, file)
        with self.assertRaises(providers.Unavailable):
            providers.MediaPipeProvider(model, manifest)


# The fields of a scorer bundle's MANIFEST.json that the Worker reads.
SCORER_RELEASE = {"name": "test-scorer", "version": "1.0.0", "dimensions": ["aesthetic", "technical"],
                  "tower_sha256": "a" * 64, "head_sha256": "b" * 64,
                  "tower": {"taps": [5], "min_pixels": 65536, "max_pixels": 1048576, "precision": "bf16"}}


class ScorerBundleTest(unittest.TestCase):
    """What the aesthetic scorer checks when the Worker starts, before it loads anything."""

    def bundle(self, release=None, kind="qv-head", pin=None):
        """A bundle directory holding only its MANIFEST.json, and the deploy manifest that pins it."""
        directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, directory, True)
        content = json.dumps(release or SCORER_RELEASE).encode()
        with open(os.path.join(directory, "MANIFEST.json"), "wb") as file:
            file.write(content)
        manifest = os.path.join(directory, "deploy.json")
        pinned = pin or {"sizeBytes": len(content), "sha256": hashlib.sha256(content).hexdigest()}
        with open(manifest, "w", encoding="utf-8") as file:
            json.dump({"name": "test-scorer", "kind": kind, "memoryBytes": 2 << 30, "bundleManifest": pinned}, file)
        return directory, manifest

    def test_only_the_pinned_bundle_and_kind_pass_the_check(self):
        for bundle, manifest in (self.bundle(kind="onnx"), self.bundle(pin={"sizeBytes": 2, "sha256": "0" * 64}),
                                 self.bundle(release={**SCORER_RELEASE, "dimensions": ["aesthetic"]})):
            with self.assertRaises(providers.Unavailable):
                providers.AestheticScorer(bundle, manifest, threads=1)

    @unittest.skipIf(importlib.util.find_spec("torch"), "PyTorch is installed")
    def test_a_runtime_without_pytorch_is_unavailable(self):
        with self.assertRaisesRegex(providers.Unavailable, "no torch"):
            providers.AestheticScorer(*self.bundle(), threads=1)

    def valid_release(self):
        metrics = {"srcc": 1, "plcc": 1, "mae_1_5": 0}
        return {**SCORER_RELEASE, "schema_version": "1.0", "created_at": "2026-01-01T00:00:00Z",
                   "base_model": {"model_id": "model", "license": "test", "revision": "0" * 40},
                   "score_min": 1, "score_max": 5, "train_run_id": "train", "best_epoch": 1,
                   "eval": {"dataset": "eva", "run_id": "eval", "n_items": 1, "ids_fingerprint": "a" * 64,
                            "metrics": dict.fromkeys(SCORER_RELEASE["dimensions"], metrics),
                            "device": "cpu", "latency_p50_ms": 1},
                   "git_commit": "1" * 40, "git_dirty": False,
                   "files": [{"path": path, "sha256": "a" * 64, "size_bytes": 1} for path in
                             ("model/head.pt", "model/tower/config.json", "model/tower/model.safetensors",
                              "model/tower/preprocessor_config.json")]}

    @unittest.skipUnless(importlib.util.find_spec("pydantic"), "requires the scorer schema dependency")
    @patch("importlib.metadata.version", return_value="test")
    def test_model_id_includes_preprocessing_and_bad_schema_is_unavailable(self, _version):
        release = self.valid_release()
        first = providers.AestheticScorer(*self.bundle(release), threads=1)
        release["files"][-1]["sha256"] = "b" * 64
        second = providers.AestheticScorer(*self.bundle(release), threads=1)
        self.assertNotEqual(first.model, second.model)
        self.assertIsNone(first._process)
        del release["tower"]
        with self.assertRaises(providers.Unavailable):
            providers.AestheticScorer(*self.bundle(release), threads=1)


    @unittest.skipUnless(importlib.util.find_spec("pydantic"), "requires the scorer schema dependency")
    @patch("importlib.metadata.version", return_value="test")
    def test_cancel_during_cold_load_reaps_child_and_allows_retry(self, _version):
        scorer = providers.AestheticScorer(*self.bundle(self.valid_release()), threads=1)
        self.addCleanup(scorer.release)
        run = scorer.run_options()

        def cancel_when_loading():
            deadline = time.monotonic() + 5
            while not (scorer._bundle / "first-load").exists() and time.monotonic() < deadline:
                time.sleep(0.01)
            run.terminate = True

        # Stand in for native loading without requiring torch or 680 MB of weights.
        child = """
import sys, time
from pathlib import Path
from multiprocessing.connection import Connection
with Connection(int(sys.argv[1])) as channel:
    channel.recv_bytes()
    marker = Path(sys.argv[2]) / "first-load"
    if not marker.exists():
        marker.touch()
        time.sleep(30)
    channel.send_bytes(b'["ready",null]')
    channel.recv_bytes()
    channel.send_bytes(b'["scores",{"aesthetic":2.5,"technical":3.5}]')
"""
        popen = subprocess.Popen

        def start_child(args, **kwargs):
            return popen([args[0], "-c", child, *args[3:]], **kwargs)

        with patch("anas_ai.providers.subprocess.Popen", side_effect=start_child):
            cancellation = threading.Thread(target=cancel_when_loading)
            cancellation.start()
            try:
                with self.assertRaises(providers.Cancelled):
                    scorer.score(b"photo", run)
            finally:
                cancellation.join()
            self.assertTrue((scorer._bundle / "first-load").exists())
            self.assertIsNone(scorer._process)
            self.assertEqual(scorer.score(b"photo", scorer.run_options()), {"aesthetic": 2.5, "technical": 3.5})


if __name__ == "__main__":
    unittest.main()
