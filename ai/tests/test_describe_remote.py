"""Card descriptions through a remote OpenAI-compatible endpoint, for the internal test only (#648).

The endpoint is a stand-in HTTP server here; the text is still read by the
stand-in llama-server (fake_llama_server.py).
"""

import hashlib
import http.server
import json
import os
import shutil
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

from anas_ai import providers, worker

ANSWER = {"camera": "地面", "beats": [{"shot_size": "远景", "what": "雪山"}, {"shot_size": "全景", "what": "走路"},
                                    {"shot_size": "特写", "what": "手"}],
          "process": ["走向雪山"], "summary": "两人走向雪山", "scene": "雪山下"}
STAND_IN = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fake_llama_server.py")


class Endpoint(http.server.ThreadingHTTPServer):
    """Answers each POST with the next of answers: (status, body), or "stream" or "length", an answer as vLLM streams it.

    "length" is an answer the context cut short, after 7000 prompt tokens.
    """

    def __init__(self, answers):
        super().__init__(("127.0.0.1", 0), EndpointHandler)
        self.answers, self.requests = list(answers), []
        self.url = f"http://127.0.0.1:{self.server_address[1]}/v1/chat/completions"


class EndpointHandler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.requests.append({"path": self.path, "authorization": self.headers.get("Authorization"),
                                     "body": body})
        answer = self.server.answers.pop(0)
        if answer not in ("stream", "length"):
            status, text = answer
            self.send_response(status)
            self.send_header("Content-Length", str(len(text)))
            self.end_headers()
            self.wfile.write(text.encode())
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        text = json.dumps(ANSWER, ensure_ascii=False)
        cut = answer == "length"
        events = [{"choices": [{"delta": {"content": text[:10]}}]},
                  {"choices": [{"delta": {"content": text[10:20] if cut else text[10:]},
                                "finish_reason": "length" if cut else "stop"}]},
                  {"choices": [], "usage": {"prompt_tokens": 7000 if cut else 1210}}]
        for event in events:
            self.wfile.write(b"data: " + json.dumps(event, ensure_ascii=False).encode() + b"\n\n")
        self.wfile.write(b"data: [DONE]\n\n")

    def log_message(self, *_):
        pass


class RemoteDescriberTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.directory, True)
        self.record = os.path.join(self.directory, "request.json")
        saved = dict(os.environ)
        self.addCleanup(lambda: (os.environ.clear(), os.environ.update(saved)))
        os.environ["FAKE_LLAMA_RECORD"] = self.record

    def endpoint(self, *answers):
        server = Endpoint(answers)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        return server

    def reader(self, server, key="secret", busy_waits=(0.01, 0.01)):
        files = []
        for name, content in (("model.gguf", b"language"), ("mmproj.gguf", b"vision")):
            path = os.path.join(self.directory, name)
            with open(path, "wb") as file:
                file.write(content)
            manifest = os.path.join(self.directory, name + ".json")
            with open(manifest, "w", encoding="utf-8") as file:
                json.dump({"name": name.split(".")[0], "sizeBytes": len(content), "memoryBytes": 1 << 30,
                           "sha256": hashlib.sha256(content).hexdigest()}, file)
            files += [path, manifest]
        runtime = os.path.join(self.directory, "llama.json")
        with open(runtime, "w", encoding="utf-8") as file:
            json.dump({"version": "b11655"}, file)
        reader = providers.LlamaReader([sys.executable, STAND_IN], *files, runtime, threads=2)
        self.addCleanup(reader.release)
        reader.remote = providers.RemoteDescriber(server.url, "qwen3.5-4b", key, busy_waits)
        return reader

    def describe(self, reader):
        return reader.describe([b"one", b"two", b"three"], [0, 2.4, 5], [1, 2, 3], 6, reader.run_options())

    def test_a_card_is_described_by_the_endpoint_as_vllm_takes_the_request(self):
        server = self.endpoint("stream")
        reader = self.reader(server)
        answer = self.describe(reader)
        self.assertEqual(answer["summary"], "两人走向雪山")
        self.assertEqual([beat["frames"] for beat in answer["beats"]], [[1, 1], [2, 2], [3, 3]])
        # llama-server was not started for it.
        self.assertIsNone(reader._process)
        (request,) = server.requests
        self.assertEqual((request["path"], request["authorization"]), ("/v1/chat/completions", "Bearer secret"))
        body = request["body"]
        self.assertEqual((body["model"], body["repetition_penalty"], body["temperature"], body["max_tokens"]),
                         ("qwen3.5-4b", 1.1, 0.2, 1800))
        self.assertNotIn("repeat_penalty", body)
        self.assertNotIn("cache_prompt", body)
        self.assertEqual(body["chat_template_kwargs"], {"enable_thinking": False})
        self.assertEqual(body["response_format"]["type"], "json_schema")

    def test_text_is_still_read_on_the_device(self):
        server = self.endpoint()
        reader = self.reader(server)
        reading = reader.read(b"jpeg bytes", "text", None, reader.run_options())
        self.assertEqual(reading["text"], "第一行\n第二行")
        self.assertEqual(server.requests, [])
        with open(self.record, encoding="utf-8") as file:
            self.assertIn("cache_prompt", json.load(file)["body"])

    def test_a_busy_endpoint_is_asked_again(self):
        server = self.endpoint((429, "cooling down"), (503, "busy"), "stream")
        answer = self.describe(self.reader(server))
        self.assertEqual(answer["summary"], "两人走向雪山")
        self.assertEqual(len(server.requests), 3)

    def test_an_endpoint_that_stays_busy_is_waited_for_without_failing_the_card(self):
        server = self.endpoint((429, "cooling down"), (429, "cooling down"), (429, "cooling down"))
        # Unavailable: the photo library asks again later and does not count it against the card.
        with self.assertRaisesRegex(providers.Unavailable, "429"):
            self.describe(self.reader(server))
        self.assertEqual(len(server.requests), 3)

    def test_a_card_cancelled_before_it_is_sent_is_not_sent(self):
        server = self.endpoint()
        reader = self.reader(server)
        run = reader.run_options()
        run.terminate = True
        with self.assertRaises(providers.Cancelled):
            reader.describe([b"one", b"two", b"three"], [0, 2.4, 5], [1, 2, 3], 6, run)
        self.assertEqual(server.requests, [])

    def test_a_key_the_endpoint_refuses_makes_describing_unavailable(self):
        server = self.endpoint((401, "no such key"))
        with self.assertRaisesRegex(providers.Unavailable, "401"):
            self.describe(self.reader(server, key="wrong"))

    def test_a_request_the_endpoint_cannot_serve_waits_for_the_endpoint(self):
        # Its configuration, not the card: the card is not failed for it.
        for answer in ((400, "response_format is not supported"), (413, "too large a body"), (422, "bad image"),
                       (301, "moved")):
            server = self.endpoint(answer)
            with self.assertRaisesRegex(providers.Unavailable, str(answer[0])):
                self.describe(self.reader(server))

    def test_a_card_too_long_for_the_model_is_invalid(self):
        server = self.endpoint((400, "This model's maximum context length is 8192 tokens."))
        with self.assertRaises(providers.InvalidInput):
            self.describe(self.reader(server))

    def test_a_gateway_that_fails_is_waited_for(self):
        server = self.endpoint((502, "bad gateway"), (504, "timeout"), "stream")
        self.assertEqual(self.describe(self.reader(server))["summary"], "两人走向雪山")
        server = self.endpoint((500, "oops"), (500, "oops"), (500, "oops"))
        with self.assertRaisesRegex(providers.Unavailable, "500"):
            self.describe(self.reader(server))

    def test_an_answer_cut_short_is_asked_again_whatever_the_prompt_took(self):
        # llama-server's context is not the endpoint's: a long prompt does
        # not make the card fail at once, it is asked again as any answer.
        server = self.endpoint("length", "length", "length")
        with self.assertRaises(providers.InvalidResult):
            self.describe(self.reader(server))
        self.assertEqual(len(server.requests), 3)

    def test_looking_the_endpoint_up_stops_when_told_and_in_time(self):
        server = self.endpoint()
        reader = self.reader(server)
        never = threading.Event()
        self.addCleanup(never.set)

        def slow(*_args, **_kwargs):
            never.wait(10)
            raise OSError("too late")

        run = reader.run_options()
        threading.Timer(0.3, lambda: setattr(run, "terminate", True)).start()
        started = time.monotonic()
        with mock.patch.object(providers.socket, "getaddrinfo", slow), self.assertRaises(providers.Cancelled):
            reader.describe([b"one", b"two", b"three"], [0, 2.4, 5], [1, 2, 3], 6, run)
        self.assertLess(time.monotonic() - started, 3)
        with mock.patch.object(providers.socket, "getaddrinfo", slow), \
                mock.patch.object(providers, "DESCRIBE_CONNECT_SECONDS", 0.3), \
                self.assertRaisesRegex(providers.Unavailable, "no address"):
            self.describe(reader)
        self.assertEqual(server.requests, [])

    def test_an_endpoint_that_cannot_be_reached_is_unavailable(self):
        server = self.endpoint()
        reader = self.reader(server)
        server.shutdown()
        server.server_close()
        with self.assertRaises(providers.Unavailable):
            self.describe(reader)

    def test_the_worker_uses_the_endpoint_only_when_its_unit_names_one(self):
        server = self.endpoint()
        reader = self.reader(server)
        reader.remote = None
        worker.remote_describer(reader, {})
        self.assertIsNone(reader.remote)
        worker.remote_describer(reader, {"ANAS_AI_DESCRIBE_URL": server.url})
        self.assertIsNone(reader.remote, "a URL without a model name")
        worker.remote_describer(reader, {"ANAS_AI_DESCRIBE_URL": "http://127.0.0.1:port/v1/chat/completions",
                                         "ANAS_AI_DESCRIBE_MODEL": "qwen3.5-4b"})
        self.assertIsNone(reader.remote, "a URL whose port is no number")
        worker.remote_describer(reader, {"ANAS_AI_DESCRIBE_URL": "https://[::1/v1/chat/completions",
                                         "ANAS_AI_DESCRIBE_MODEL": "qwen3.5-4b"})
        self.assertIsNone(reader.remote, "a URL that does not parse")
        worker.remote_describer(reader, {"ANAS_AI_DESCRIBE_URL": "http://describer.example/v1/chat/completions",
                                         "ANAS_AI_DESCRIBE_MODEL": "qwen3.5-4b", "ANAS_AI_DESCRIBE_KEY": "secret"})
        self.assertIsNone(reader.remote, "plain http to another machine")
        worker.remote_describer(reader, {"ANAS_AI_DESCRIBE_URL": "https://describer.example/v1/chat/completions",
                                         "ANAS_AI_DESCRIBE_MODEL": "qwen3.5-4b"})
        self.assertEqual(reader.remote.host, "describer.example")
        reader.remote = None
        worker.remote_describer(reader, {"ANAS_AI_DESCRIBE_URL": server.url, "ANAS_AI_DESCRIBE_MODEL": "qwen3.5-4b",
                                         "ANAS_AI_DESCRIBE_KEY": "secret"})
        self.assertEqual(reader.remote.host, "127.0.0.1")
        fake = providers.FakeReader()
        worker.remote_describer(fake, {"ANAS_AI_DESCRIBE_URL": server.url, "ANAS_AI_DESCRIBE_MODEL": "m"})
        self.assertFalse(hasattr(fake, "remote"))


if __name__ == "__main__":
    unittest.main()
