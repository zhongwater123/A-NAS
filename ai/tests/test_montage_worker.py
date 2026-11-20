"""The montage center's compute Worker (ai/anas_montage): its split of the
prototype's computation gives the prototype's numbers, its fill asks for what
it lacks in a fixed order, and it speaks the protocol with memfd arrays.

CI installs NumPy and Pillow from the AI runtime lock."""

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

if not RUNTIME and os.environ.get("ANAS_MONTAGE_PYTHON"):
    raise RuntimeError("montage Worker tests require NumPy and Pillow")

if RUNTIME:
    import numpy as np

    from anas_montage import worker
    from anas_montage.domain import cards as cardrules, fill, picture


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

    def test_catalog_filters_black_cards_and_short_cards_have_no_shake(self):
        cards = [{"id": key, "video": "v", "start": 0, "end": 2} for key in ("black", "good")]
        good = worker.op_quality({"blurs": [2.0], "blocked": [0.0], "points": [None], "moves": None}, {})
        self.assertEqual(good["shake"], 0)
        for blurs in ([None, 3, 1], [3, None, 1], [3, 1, None]):
            black = worker.op_quality({"blurs": blurs, "blocked": [0.0] * 3, "points": [None] * 3, "moves": []}, {})
            catalog = worker.op_catalog({"cards": cards, "measures": {"black": black, "good": good}}, {})
            self.assertEqual(catalog, {"cards": ["good"]})

    def test_cards_with_no_cut_point_between_two_nonflat_ends(self):
        windows = [{"start": 2 * i, "end": 2 * i + 2} for i in range(70)]
        flat = np.ones(70, np.uint8)
        flat[:5] = flat[-5:] = 0
        vectors = np.tile(np.array([1, 0], dtype=np.float32), (70, 1))
        answer = worker.op_cards({"windows": windows}, {"vectors": vectors, "flat": flat})
        self.assertEqual([(c["start"], c["end"]) for c in answer["cards"]], [(0, 140)])


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
    def test_shooting_order_preserves_each_slots_exact_frame_count(self):
        slots = [[0, 2.05], [2.05, 4.10]]
        pieces = [{"card": "later", "start": 0, "slot": slots[0]}, {"card": "earlier", "start": 0, "slot": slots[1]}]
        ordered = fill.in_shooting_order(pieces, slots, 30, {"earlier": 0, "later": 1}.__getitem__)
        counts = [round(b * 30) - round(a * 30) for a, b in slots]
        self.assertNotEqual(*counts)
        self.assertEqual([c["card"] for c in ordered], ["earlier", "later"])
        self.assertEqual([round(c["slot"][1] * 30) - round(c["slot"][0] * 30) for c in ordered], counts[::-1])

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


    def filled(self, request, vectors):
        """The fill to its end, every span it asks for taken as clear."""
        request = {**request, "known": {}}
        for _ in range(20):
            answer = worker.op_fill(request, {"vectors": vectors})
            if "need" not in answer:
                return answer
            request["known"][answer["need"]["name"]] = [answer["need"]["lo"], answer["need"]["hi"]]
        self.fail("the fill kept asking")

    def test_pins_lead_each_slot_where_the_film_had_it(self):
        vectors = np.eye(10, 8, dtype=np.float32) + 0.1
        request = tiny_case()
        plain = self.filled(request, vectors)
        self.assertEqual([c["beat"] for c in plain["clips"]], ["c1/1", "c1/2"])
        pins = [{"part": 1, "picture": 1, "slot": [0.0, 2.0], "beat": "c1/2"},
                {"part": 1, "picture": 1, "slot": [2.0, 4.0], "beat": "c1/1"}]
        swapped = self.filled({**request, "pins": pins}, vectors)
        self.assertEqual([(c["beat"], c["slot"]) for c in swapped["clips"]], [("c1/2", [0.0, 2.0]), ("c1/1", [2.0, 4.0])])
        self.assertEqual(swapped["short"], [])
        # A pinned slot that a deleted neighbour's slot went to is one longer slot.
        merged = self.filled({**request, "pins": [{"part": 1, "picture": 1, "slot": [0.0, 4.0], "beat": "c1/2"}]}, vectors)
        self.assertEqual(merged["clips"][0]["beat"], "c1/2")
        self.assertEqual(sum(round(c["share"] * 30) for c in merged["clips"]), 120)

    def test_pins_that_repeat_the_film_give_it_again(self):
        vectors = np.eye(10, 8, dtype=np.float32) + 0.1
        plain = self.filled(tiny_case(), vectors)
        pins = [{"part": c["part"], "picture": c["picture"], "slot": c["slot"], "beat": c["beat"]} for c in plain["clips"]]
        self.assertEqual(self.filled({**tiny_case(), "pins": pins}, vectors), plain)

    def test_a_pin_keeps_its_key_frame_from_the_slots_before_it(self):
        # Two beats of one card a second apart: the first slot's shot, cut around its own key frame, would run over
        # the second's, and the second pin would be passed over as footage already used.
        vectors = np.eye(10, 8, dtype=np.float32) + 0.1
        request = tiny_case()
        request["cards"]["c1"]["times"] = [3.0, 4.0]
        pins = [{"part": 1, "picture": 1, "slot": [0.0, 2.0], "beat": "c1/1"},
                {"part": 1, "picture": 1, "slot": [2.0, 4.0], "beat": "c1/2"}]
        pinned = self.filled({**request, "pins": pins}, vectors)
        self.assertEqual([(c["beat"], c["slot"]) for c in pinned["clips"]], [("c1/1", [0.0, 2.0]), ("c1/2", [2.0, 4.0])])
        first, second = pinned["clips"]
        self.assertLessEqual(first["end"], 4.0)
        self.assertGreaterEqual(second["start"], first["end"])
        self.assertEqual(pinned["short"], [])
        # Dragged the other way round, with the card ending soon after: the first slot's shot, moved back to fit, would
        # run over the earlier key frame. It stops short of it, and what it lacks is reported, not taken from the pin.
        request["cards"]["c1"] |= {"times": [3.5, 4.0], "end": 5.2}
        swapped = self.filled({**request, "pins": [{**pins[0], "beat": "c1/2"}, {**pins[1], "beat": "c1/1"}]}, vectors)
        self.assertEqual([(c["beat"], c["slot"]) for c in swapped["clips"]], [("c1/2", [0.0, 2.0]), ("c1/1", [2.0, 4.0])])
        self.assertGreater(swapped["clips"][0]["start"], 3.5)
        self.assertEqual([s[:4] for s in swapped["short"]], [[1, 1, 0.0, 2.0]])

    def test_pins_that_leave_a_gap_or_name_another_beat_are_refused(self):
        request = tiny_case()
        for pins in ([{"part": 1, "picture": 1, "slot": [0.0, 2.0], "beat": "c1/1"},
                      {"part": 1, "picture": 1, "slot": [2.5, 4.0], "beat": "c1/2"}],
                     [{"part": 1, "picture": 1, "slot": [0.0, 4.0], "beat": "c1/3"}],
                     [{"part": 2, "picture": 1, "slot": [0.0, 4.0], "beat": "c1/1"}],
                     []):
            with self.subTest(pins=pins), self.assertRaises(worker.Refused):
                worker.op_fill({**request, "pins": pins}, {"vectors": np.ones((10, 8), np.float32)})

    def test_likeness_is_the_cosine_of_the_mean_windows(self):
        a = np.array([[1, 0], [1, 0]], np.float32)
        self.assertAlmostEqual(worker.op_likeness({}, {"a": a, "b": a * 3})["likeness"], 1.0, places=6)
        self.assertAlmostEqual(worker.op_likeness({}, {"a": a, "b": np.array([[0, 1]], np.float32)})["likeness"], 0.0, places=6)
        self.assertAlmostEqual(worker.op_likeness({}, {"a": a, "b": np.array([[1, 1], [1, -1], [0, 2]], np.float32)})["likeness"],
                               2 / 3 / np.hypot(2 / 3, 2 / 3), places=6)
        for b in (np.ones((1, 3), np.float32), np.zeros((0, 2), np.float32), np.zeros((2, 2), np.float32)):
            with self.subTest(b=b.shape), self.assertRaises(worker.Refused):
                worker.op_likeness({}, {"a": a, "b": b})


def described(cid, video, start, end):
    """A card as the photo library describes it: the reader's beats name their key frames, not their spans."""
    return {"id": cid, "video": video, "start": start, "end": end, "times": [start + 1, start + 4], "frames": [f"{cid}/1", f"{cid}/2"],
            "answer": {"scene": "海边", "summary": "沿海边走", "process": ["走到海边", "望向远处"],
                       "beats": [{"frames": [1, 1], "shot_size": "全景", "what": "海"},
                                 {"frames": [2, 2], "shot_size": "近景", "what": "人"}]}}


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
        # Each part takes no more pictures than its own music part has slots: a picture past them is never filled.
        uneven = {**brief_music(), "parts": [{**brief_music()["parts"][0], "slots": [[0.0, 4.0]]},
                                             {**brief_music()["parts"][1], "slots": [[4.0, 5.0], [5.0, 6.0], [6.0, 8.0]]}]}
        _system, _user, uneven_schema = planrules.pick_request(case, opts, brief.numbered(cards), uneven)
        parts = uneven_schema["properties"]["parts"]
        self.assertEqual([p["properties"]["scenes"]["maxItems"] for p in parts["prefixItems"]], [1, 3])
        self.assertIs(parts["items"], False)

        picked = {"design": "从海边开始", "opening": "v1-0", "ending": "v2-0", "unmet": [],
                  "parts": [{"name": "海", "summary": "海边", "link": "开场", "scenes": [{"query": "海边远景", "cards": ["v1-0"]}]},
                            {"name": "人", "summary": "人", "link": "走近", "scenes": [{"query": "人望海", "cards": ["v2-0"]}]}]}
        shots = worker.op_plan_brief({**request, "step": "shots", "picked": picked}, {})
        self.assertEqual(shots["pictures"], [1, 1])
        beats = shots["data"]["parts"][0]["pictures"][0]["cards"][0]["beats"]
        self.assertEqual([b["beat"] for b in beats], ["v1-0/1", "v1-0/2"])
        # Each beat runs from its key frame to the next (the first from the card's start), as the eighth version's
        # beat_spans gave them: 0–4 s and 4–10 s, cut at the plan's cap.
        self.assertEqual([b["seconds"] for b in beats], [round(min(planrules.CAP, 4.0)), round(min(planrules.CAP, 6.0))])
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
