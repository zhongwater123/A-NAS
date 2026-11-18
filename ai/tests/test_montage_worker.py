"""The montage center's compute Worker (ai/anas_montage): its split of the
prototype's computation gives the prototype's numbers, its fill asks for what
it lacks in a fixed order, and it speaks the protocol with memfd arrays.

NumPy and Pillow are in the AI runtime only: make ai-test skips these, and the
runtime's image runs them (docs/development/ai-development.md)."""

import importlib.util
import io
import json
import os
import socket
import struct
import tempfile
import threading
import unittest

RUNTIME = bool(importlib.util.find_spec("numpy") and importlib.util.find_spec("PIL"))

if RUNTIME:
    import numpy as np

    from anas_montage import worker
    from anas_montage.domain import cards as cardrules, picture


def memfd(array):
    fd = os.memfd_create("test-array")
    os.write(fd, array.tobytes())
    return fd


@unittest.skipUnless(RUNTIME, "NumPy and Pillow are in the AI runtime")
class ComputationTest(unittest.TestCase):
    def test_shake_from_moves_in_chunks_is_the_prototypes_jitter(self):
        rng = np.random.default_rng(7)
        base = rng.integers(0, 255, (180 + 40, 320 + 40)).astype(np.float32)
        frames = []
        for i in range(37):
            dx, dy = int(rng.integers(0, 6)), int(rng.integers(0, 6))
            frames.append(base[dy + i % 3 : dy + i % 3 + 180, dx : dx + 320])
        frames = np.stack(frames).astype(np.uint8)
        whole = picture.jitter(frames.astype(np.float32))
        # Chunks of 15 frames that share their edge frame, as the photo service sends them.
        pairs = []
        for start in range(0, len(frames) - 1, 14):
            pairs += worker.moves(frames[start : start + 15])
        self.assertEqual(len(pairs), len(frames) - 1)
        self.assertEqual(worker.shake(pairs), whole)

    def test_cards_are_the_prototypes(self):
        rng = np.random.default_rng(3)
        vectors = rng.normal(size=(40, 16)).astype(np.float32)
        vectors[10:25] = vectors[10] + 0.01 * rng.normal(size=(15, 16))
        vectors /= np.linalg.norm(vectors, axis=1, keepdims=True)
        flat = np.zeros(40, np.uint8)
        flat[39] = 1
        windows = [{"start": 2 * i, "end": 2 * i + 2} for i in range(40)]
        answer = worker.op_cards({"windows": windows, "key": "v"}, {"vectors": vectors, "flat": flat})
        index = cardrules.WindowIndex({"v": {}}, [("v", 2.0 * i, 2.0 * i + 2) for i in range(40)], vectors, flat.astype(bool))
        expected = [{k: c[k] for k in ("id", "start", "end", "windows", "times", "keys")} for c in cardrules.plan_cards(index, "v")]
        self.assertEqual(answer["cards"], expected)

    def test_quality_reasons_follow_the_rules(self):
        poor = worker.op_quality({"blurs": [9.5, 9.0, 8.8], "blocked": [0.0, 0.1, 0.0], "points": [None, None, None],
                                  "moves": [[3.0 * (i % 2), 0.0] for i in range(30)]}, {})
        self.assertIn("模糊", poor["reasons"])
        black = worker.op_quality({"blurs": [None, None, None], "blocked": [0, 0, 0], "points": [None] * 3, "moves": []}, {})
        self.assertEqual(black["reasons"], ["黑帧"])
        self.assertIsNone(black["blur"])


def tiny_case():
    """One part of one picture with two 2-second slots, from a card of two beats."""
    card = {"id": "c1", "video": "v", "start": 0.0, "end": 20.0, "times": [3.0, 12.0], "frames": ["f1", "f2"],
            "answer": {"beats": [{"shot_size": "远景", "what": "海"}, {"shot_size": "全景", "what": "船"}]}}
    rows = [{"video": "v", "start": 2.0 * i, "end": 2.0 * i + 2} for i in range(10)]
    return {
        "op": "fill", "fps": 30, "rows": rows, "cards": {"c1": card}, "catalog": ["c1"],
        "music": {"parts": [{"level": "主歌"}]},
        "planned": {"answer": {"parts": [{"name": "开场", "pictures": [{"beats": ["c1/1", "c1/2"], "shows": "海", "link": ""}]}]},
                    "slots": [[[[0.0, 2.0], [2.0, 4.0]]]]},
        "picked": {"parts": [{"scenes": [{"cards": ["c1"], "query": "海边"}]}]},
    }


@unittest.skipUnless(RUNTIME, "NumPy and Pillow are in the AI runtime")
class FillTest(unittest.TestCase):
    def test_fill_asks_for_one_span_at_a_time_in_a_fixed_order(self):
        vectors = np.eye(10, 8, dtype=np.float32) + 0.1
        vectors /= np.linalg.norm(vectors, axis=1, keepdims=True)
        request, known, asked = tiny_case(), {}, []
        for _ in range(10):
            request["known"] = dict(known)
            first = worker.op_fill(request, {"vectors": vectors})
            again = worker.op_fill(request, {"vectors": vectors})
            self.assertEqual(first, again)
            if "need" not in first:
                break
            need = first["need"]
            asked.append(need["name"])
            known[need["name"]] = [need["lo"], need["hi"]]
        self.assertNotIn("need", first)
        self.assertEqual(len(asked), len(set(asked)))
        self.assertEqual(sum(round((c["end"] - c["start"]) * 30) for c in first["clips"]), 120)
        self.assertEqual(first["short"], [])


def described(cid, video, start, end):
    return {"id": cid, "video": video, "start": start, "end": end, "times": [start + 1, start + 4], "frames": [f"{cid}/1", f"{cid}/2"],
            "answer": {"scene": "海边", "summary": "沿海边走", "process": ["走到海边", "望向远处"],
                       "beats": [{"shot_size": "全景", "what": "海", "end": start + 4},
                                 {"shot_size": "近景", "what": "人", "end": end}]}}


def brief_music():
    return {"song": "/songs/abc/audio", "seconds": 8.0,
            "parts": [{"start": 0.0, "end": 4.0, "level": "主歌", "slots": [[0.0, 2.0], [2.0, 4.0]]},
                      {"start": 4.0, "end": 8.0, "level": "副歌", "slots": [[4.0, 6.0], [6.0, 8.0]]}]}


@unittest.skipUnless(RUNTIME, "NumPy and Pillow are in the AI runtime")
class BriefTest(unittest.TestCase):
    def test_the_briefs_hold_data_and_the_eighth_versions_schemas(self):
        from anas_montage import brief
        from anas_montage.domain import plan as planrules

        cards = [described("v1-0", "photo:a", 0.0, 10.0), described("v2-0", "photo:b", 0.0, 12.0)]
        request = {"step": "pick", "cards": cards, "music": brief_music(), "ask": "多一点海", "chronological": False}
        pick = worker.op_plan_brief(request, {})
        self.assertEqual(pick["template"], brief.TEMPLATE)
        data = pick["data"]
        self.assertEqual((data["cardCount"], data["partCount"], data["order"], data["targetSeconds"]), (2, 2, "free", 8))
        self.assertIn("多一点海", data["instruction"])
        self.assertTrue(data["catalog"][0].startswith("v1-0 视频1 "))
        # The data carries no rules: the template has them.
        self.assertNotIn("请按用户的要求", json.dumps(data, ensure_ascii=False))
        case, opts = brief.case_of(request)
        _system, _user, schema = planrules.pick_request(case, opts, brief.numbered(cards), brief_music())
        self.assertEqual(pick["schema"], schema)

        picked = {"design": "从海边开始", "opening": "v1-0", "ending": "v2-0", "unmet": [],
                  "parts": [{"name": "海", "summary": "海边", "link": "开场", "scenes": [{"query": "海边远景", "cards": ["v1-0"]}]},
                            {"name": "人", "summary": "人", "link": "走近", "scenes": [{"query": "人望海", "cards": ["v2-0"]}]}]}
        shots = worker.op_plan_brief({**request, "step": "shots", "picked": picked}, {})
        self.assertEqual(shots["pictures"], [1, 1])
        beats = shots["data"]["parts"][0]["pictures"][0]["cards"][0]["beats"]
        self.assertEqual([b["beat"] for b in beats], ["v1-0/1", "v1-0/2"])
        answer = {"unmet": [], "parts": {"段落1": {"画面1": {"beats": ["v1-0/1"], "shows": "海", "link": "开场"}},
                                         "段落2": {"画面1": {"beats": ["v2-0/1"], "shows": "人", "link": "走近"}}}}
        turned = worker.op_shots_answer({"answer": answer, "pictures": shots["pictures"], "picked": picked}, {})
        self.assertEqual(turned["answer"]["parts"][1]["pictures"][0]["beats"], ["v2-0/1"])


@unittest.skipUnless(RUNTIME, "NumPy and Pillow are in the AI runtime")
class ProtocolTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.path = os.path.join(self.dir, "compute.sock")
        listener = worker.listening_socket(self.path)
        self.addCleanup(listener.close)
        thread = threading.Thread(target=self.serve_one, args=(listener,), daemon=True)
        thread.start()

    @staticmethod
    def serve_one(listener):
        conn, _ = listener.accept()
        with conn:
            worker.handle(conn)

    def call(self, request, arrays):
        fds = [memfd(a) for a in arrays]
        try:
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
                conn.connect(self.path)
                body = json.dumps(request).encode()
                socket.send_fds(conn, [struct.pack(">I", len(body)) + body], fds)
                header = conn.recv(4)
                (length,) = struct.unpack(">I", header)
                data = b""
                while len(data) < length:
                    data += conn.recv(length - len(data))
                return json.loads(data)
        finally:
            for fd in fds:
                os.close(fd)

    def test_a_keyframe_part_comes_over_the_socket(self):
        from PIL import Image

        buffer = io.BytesIO()
        Image.new("RGB", (512, 288), (200, 180, 160)).save(buffer, "JPEG", quality=82)
        jpeg = np.frombuffer(buffer.getvalue(), np.uint8)
        answer = self.call({"op": "quality_part", "kind": "keyframe",
                            "arrays": [{"name": "jpeg", "dtype": "uint8", "shape": [len(jpeg)]}]}, [jpeg])
        self.assertTrue(answer["ok"], answer)
        self.assertGreater(answer["light"], 150)

    def test_fields_too_large_for_a_frame_come_as_the_body(self):
        body = json.dumps({"blurs": [None] * 3, "blocked": [0, 0, 0], "points": [None] * 3, "moves": [],
                           "padding": "x" * (2 << 20)}).encode()
        answer = self.call({"op": "quality", "arrays": [{"name": "body", "dtype": "uint8", "shape": [len(body)]}]},
                           [np.frombuffer(body, np.uint8)])
        self.assertTrue(answer["ok"], answer)
        self.assertEqual(answer["reasons"], ["黑帧"])

    def test_an_array_of_another_length_is_refused(self):
        frame = np.zeros((10, 10), np.uint8)
        answer = self.call({"op": "quality_part", "kind": "sharp",
                            "arrays": [{"name": "frame", "dtype": "uint8", "shape": [20, 10]}]}, [frame])
        self.assertFalse(answer["ok"])
        self.assertEqual(answer["error"]["code"], "invalid_input")


if __name__ == "__main__":
    unittest.main()
