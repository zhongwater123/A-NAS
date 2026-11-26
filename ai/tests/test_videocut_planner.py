"""Root's planner hands a planning round to the member's planning instance as one conversation
(ai/assistant/videocut-planner.py, docs/architecture/montage-planning-session.md).

The montage center's socket, the planning instance's login and the model's gateway are stand-ins on Unix sockets; the
instance's JSON-RPC is a scripted connection in place of the runtime's websockets.
"""

import asyncio
import importlib.util
import json
import os
import socketserver
import tempfile
import threading
import unittest
from datetime import UTC, datetime
from http.server import BaseHTTPRequestHandler
from pathlib import Path
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / "assistant" / "videocut-planner.py"
SPEC = importlib.util.spec_from_file_location("videocut_planner", SOURCE)
planner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(planner)

ROUND = "round:" + "A" * 26
JOB = "job:" + "B" * 26
TOKEN = "0123456789abcdef" * 2
SECRET = "f" * 64


class UnixServer(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


def serve(path, answer):
    """An HTTP server on a Unix socket: answer(method, path, headers, body) gives (status, headers, body)."""
    calls = []

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def address_string(self):
            return "uds"

        def handle_one(self, method):
            raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            body = json.loads(raw) if raw else None
            calls.append((method, self.path, dict(self.headers), body))
            status, headers, out = answer(method, self.path, self.headers, body)
            data = json.dumps(out).encode() if out is not None else b""
            self.send_response(status)
            for name, value in headers:
                self.send_header(name, value)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_POST(self):
            self.handle_one("POST")

        def do_GET(self):
            self.handle_one("GET")

    server = UnixServer(path, Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, calls


class ScriptedHermes:
    """In place of the planning instance's /api/ws: answers each call, and for the prompt sends the events a turn
    would; turn(ws, message) may replace that."""

    def __init__(self, test, turn=None, failing=()):
        self.test, self.inbox, self.sent = test, asyncio.Queue(), []
        self.failing, self.turn = set(failing), turn or self.complete
        self.push({"method": "event", "params": {"type": "gateway.ready"}})

    def push(self, message):
        self.inbox.put_nowait(json.dumps({"jsonrpc": "2.0", **message}))

    def event(self, kind, payload=None, session="s1"):
        self.push({"method": "event", "params": {"type": kind, "session_id": session, "payload": payload or {}}})

    def __aiter__(self):
        return self

    async def __anext__(self):
        raw = await self.inbox.get()
        if raw is None:
            raise StopAsyncIteration
        return raw

    async def close(self):
        self.inbox.put_nowait(None)

    async def send(self, raw):
        message = json.loads(raw)
        self.sent.append(message)
        method = message.get("method")
        if method is None:
            return  # the client's answer to a request of the server's
        if method in self.failing:
            return self.push({"id": message["id"], "error": {"code": 4002, "message": "unknown method"}})
        result = {"session.create": {"session_id": "s1", "stored_session_id": "20261120_120000_abcdef"}}.get(method, {})
        self.push({"id": message["id"], "result": result})
        if method == "prompt.submit":
            self.turn(self)

    @staticmethod
    def complete(ws):
        ws.push({"id": "srq-1", "method": "approval", "params": {"session_id": "s1", "request_id": "r1"}})
        ws.event("tool.start", {"name": "mcp__videocut__videocut_brief"})
        ws.event("message.complete", {"text": "[SILENT]", "status": "complete"})
        ws.event("review.summary", {"text": "记下了一个技能"})

    def calls(self, method):
        return [m for m in self.sent if m.get("method") == method]


class PlannerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name)
        self.root = root
        paths = {"MARKERS": root / "markers", "GRANTS": root / "grants", "TOKENS": root / "tokens",
                 "STATE": root / "state" / "state.json", "VIDEOCUT": root / "videocut.sock",
                 "PLAN_SOCKETS": root / "plan", "PROMPTS": root / "prompts", "MODEL": root / "model.sock"}
        for name, path in paths.items():
            self.addCleanup(setattr, planner, name, getattr(planner, name))
            setattr(planner, name, str(path))
        for name in ("markers", "grants", "tokens", "plan", "prompts"):
            # Whatever the umask: grants and secrets count only in a directory nobody else may write to.
            (root / name).mkdir(mode=0o700)
        (root / "prompts" / "plan-v2.md").write_text("这是剪辑中心的一轮剪辑规划。任务编号 $job_id，token 是 $token（轮次 $round_id），约 $target 秒。",
                                                      encoding="utf-8")
        self.now = datetime.now(UTC).timestamp()
        self.claims, self.ended, self.abandoned, self.started = [], [], [], []
        self.claim = {"roundId": ROUND, "jobId": JOB, "attemptToken": TOKEN, "template": "plan-v2", "targetSeconds": 12,
                      "project": "海边"}
        self.started_status = 204
        self.videocut_server, self.videocut_calls = serve(str(root / "videocut.sock"), self.videocut)
        self.addCleanup(self.videocut_server.server_close)
        self.addCleanup(self.videocut_server.shutdown)
        self.login_status = 200
        # Each conversation is a connection of its own, scripted with turn and failing.
        self.turn, self.failing = None, set()
        self.hermes = ScriptedHermes(self)
        self.connected, self.said = [], []

        async def connect(account, token):
            self.connected.append((account, token))
            self.hermes = ScriptedHermes(self, self.turn, self.failing)
            return planner.Hermes(self.hermes)

        for p in (patch.object(planner, "active_account", lambda account: account != "carol"),
                  patch.object(planner, "model_answers", lambda: True),
                  patch.object(planner, "connect", connect),
                  patch.object(planner, "REVIEW", 2),
                  patch.object(planner, "say", self.said.append)):
            p.start()
            self.addCleanup(p.stop)

    def videocut(self, method, path, headers, body):
        account = headers.get("X-A-NAS-Account")
        if path == "/v1/rounds/claim":
            self.claims.append(account)
            return (200, [], self.claim) if self.claim else (409, [], {"error": {"code": "no_plan_wanted"}})
        if path.endswith("/started"):
            self.started.append(body)
            return self.started_status, [], None
        if path.endswith("/end"):
            self.ended.append(body)
        elif path.endswith("/abandon"):
            self.abandoned.append(body)
        return 204, [], None

    def instance(self, account="alice"):
        """The member's planning instance's login on its socket."""

        def login(method, path, headers, body):
            if path != "/auth/password-login" or body != {"provider": "basic", "username": account, "password": SECRET}:
                return 401, [], None
            if self.login_status != 200:
                return self.login_status, [], None
            return 200, [("Set-Cookie", "hermes_session_at=access-token; Path=/; HttpOnly")], {"ok": True}

        server, calls = serve(str(self.root / "plan" / f"{account}.sock"), login)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        return calls

    def member(self, account, *, issued=None, rounds=5, marker_age=0):
        issued = issued if issued is not None else self.now - 60
        grant = self.root / "grants" / account
        grant.unlink(missing_ok=True)
        grant.write_text(json.dumps({"issuedAt": datetime.fromtimestamp(issued, UTC).isoformat(), "rounds": rounds}))
        grant.chmod(0o600)
        secret = self.root / "tokens" / f"{account}.env"
        secret.write_text(f"HERMES_DASHBOARD_SESSION_TOKEN={SECRET}\n")
        secret.chmod(0o600)
        marker = self.root / "markers" / account
        marker.write_text("")
        os.utime(marker, (self.now - marker_age, self.now - marker_age))

    def counts(self):
        return planner.load_state()["counts"]

    def test_a_round_is_claimed_held_as_one_conversation_and_reported(self):
        self.member("alice")
        logins = self.instance()
        planner.Planner().cycle(self.now)
        self.assertEqual(self.claims, ["alice"])
        self.assertEqual(len(logins), 1)
        self.assertEqual(self.connected, [("alice", "access-token")])
        self.assertEqual(self.hermes.calls("client.capabilities")[0]["params"], {"server_requests": True})
        self.assertEqual(self.hermes.calls("session.title")[0]["params"], {"session_id": "s1", "title": "剪辑规划 · 海边"})
        self.assertEqual(self.started, [{"runRef": "20261120_120000_abcdef"}])
        text = self.hermes.calls("prompt.submit")[0]["params"]["text"]
        self.assertIn(JOB, text)
        self.assertIn(TOKEN, text)
        self.assertNotIn("$", text)
        # The prompt went in only once the round was recorded as started.
        order = [m.get("method") for m in self.hermes.sent if m.get("method")]
        self.assertLess(order.index("session.title"), order.index("prompt.submit"))
        # The approval Hermes asked for is refused.
        self.assertIn({"jsonrpc": "2.0", "id": "srq-1", "result": {"choice": "deny"}}, self.hermes.sent)
        self.assertEqual(self.ended, [{"result": "complete", "outcome": "complete"}])
        self.assertEqual(self.abandoned, [])
        self.assertEqual(list(self.counts().values()), [1])

    def test_without_a_marker_grant_rounds_left_secret_or_an_account_in_use_nothing_is_claimed(self):
        self.member("alice", issued=self.now - 8 * 24 * 3600)
        self.member("bob", rounds=0)
        self.member("carol")
        self.member("dave")
        (self.root / "tokens" / "dave.env").unlink()
        self.member("erin")
        (self.root / "markers" / "erin").unlink()
        planner.Planner().cycle(self.now)
        self.assertEqual(self.claims, [])

    def test_one_round_at_a_time_for_the_member_waiting_longest(self):
        self.member("alice", marker_age=10)
        self.member("bob", marker_age=60)
        self.instance("bob")
        planner.Planner().cycle(self.now)
        self.assertEqual(self.claims, ["bob"])

    def test_a_grant_counts_only_as_roots_own_file_in_roots_own_directory(self):
        """What anybody else could have written is no grant: it would let them have a member's VNAS plan."""
        self.member("alice")
        self.claim = None
        grant, grants = self.root / "grants" / "alice", self.root / "grants"
        elsewhere = self.root / "elsewhere"
        elsewhere.write_text(grant.read_text())
        elsewhere.chmod(0o600)

        def refused(why):
            planner.Planner().cycle(self.now)
            self.assertEqual(self.claims, [], why)

        grant.chmod(0o660)
        refused("a grant its group may write")
        grant.chmod(0o606)
        refused("a grant anyone may write")
        grant.unlink()
        grant.symlink_to(elsewhere)
        refused("a link to a grant somewhere else")
        self.member("alice")
        grants.chmod(0o770)
        refused("a directory its group may write to")
        grants.chmod(0o700)
        moved = self.root / "grants-of-root"
        grants.rename(moved)
        grants.symlink_to(moved)
        refused("a directory reached through a link")
        grants.unlink()
        moved.rename(grants)
        somebody_else = os.geteuid() + 1
        with patch.object(planner.os, "geteuid", lambda: somebody_else):
            refused("a grant that is not the planner's own user's")
        for document in ("[]", "{", json.dumps({"issuedAt": 5, "rounds": 5}), json.dumps({"issuedAt": "2026-11-18T10:00:00+00:00"}), "x" * 5000):
            grant.write_text(document)
            refused(f"a grant that reads {document[:20]!r}")
        self.member("alice")
        planner.Planner().cycle(self.now)
        self.assertEqual(self.claims, ["alice"])

    def test_a_grant_gives_its_rounds_and_old_counts_are_forgotten(self):
        self.member("alice", rounds=2)
        self.instance()
        for _ in range(3):
            planner.Planner().cycle(self.now)
        self.assertEqual(len(self.claims), 2, "a third round on a grant of two")
        key = next(iter(self.counts()))
        # Each registration issues a new grant: the count of one that ran out is dropped.
        self.now += planner.GRANT_LIFE + 60
        self.member("alice")
        planner.Planner().cycle(self.now)
        self.assertNotIn(key, self.counts())
        self.assertEqual(len(self.claims), 3)

    def test_an_instance_that_does_not_answer_abandons_the_round_as_unreachable(self):
        self.member("alice")  # no instance on her socket
        planner.Planner().cycle(self.now)
        self.assertEqual([a["failure"] for a in self.abandoned], ["unreachable"])
        self.assertIn("VNAS 的规划进程连不上", self.abandoned[0]["outcome"])
        self.assertEqual(sum(self.counts().values()), 0)
        self.assertEqual(self.started, [])

    def test_the_models_gateway_not_answering_abandons_the_round_as_unreachable(self):
        self.member("alice")
        self.instance()
        with patch.object(planner, "model_answers", lambda: False):
            planner.Planner().cycle(self.now)
        self.assertEqual([a["failure"] for a in self.abandoned], ["unreachable"])
        self.assertEqual(self.connected, [])

    def test_an_answer_that_is_not_hermess_before_the_prompt_is_an_error(self):
        self.member("alice")
        self.instance()
        self.failing.add("session.create")
        planner.Planner().cycle(self.now)
        self.assertEqual([a["failure"] for a in self.abandoned], ["error"])
        self.assertIn("session.create", self.abandoned[0]["outcome"])
        self.assertEqual(self.hermes.calls("prompt.submit"), [])
        self.login_status = 403
        planner.Planner().cycle(self.now)
        self.assertEqual([a["failure"] for a in self.abandoned], ["error", "error"])

    def test_a_claim_the_planner_cannot_fill_in_is_abandoned_as_an_error(self):
        self.member("alice")
        self.instance()
        for broken in ({"template": "../x"}, {"jobId": "job:short"}, {"targetSeconds": True}, {"template": "plan-v9"}):
            with self.subTest(claim=broken):
                self.abandoned.clear()
                self.claim = {"roundId": ROUND, "jobId": JOB, "attemptToken": TOKEN, "template": "plan-v2", "targetSeconds": 12, **broken}
                planner.Planner().cycle(self.now)
                self.assertEqual([a["failure"] for a in self.abandoned], ["error"])
        self.assertEqual(self.connected, [])

    def test_a_round_no_longer_standing_gets_no_prompt(self):
        self.member("alice")
        self.instance()
        self.started_status = 409
        planner.Planner().cycle(self.now)
        self.assertEqual(self.hermes.calls("prompt.submit"), [])
        self.assertEqual((self.ended, self.abandoned), ([], []))
        self.assertEqual(sum(self.counts().values()), 0)

    def test_a_turn_that_fails_before_the_model_called_a_montage_tool_is_given_back(self):
        self.member("alice")
        self.instance()
        self.turn = lambda ws: ws.event("message.complete", {"status": "error", "error": "the model's gateway answered 502"})
        planner.Planner().cycle(self.now)
        self.assertEqual(self.ended, [{"result": "error", "outcome": "error: the model's gateway answered 502"}])
        self.assertEqual(sum(self.counts().values()), 0)

    def test_a_turn_cut_off_after_a_montage_tool_counts(self):
        self.member("alice")
        self.instance()

        def cut(ws):
            ws.event("tool.start", {"name": "mcp__videocut__videocut_submit_outline"})
            ws.inbox.put_nowait(None)

        self.turn = cut
        planner.Planner().cycle(self.now)
        self.assertEqual([e["result"] for e in self.ended], ["interrupted"])
        self.assertEqual(sum(self.counts().values()), 1)

    def test_a_turn_that_runs_too_long_is_interrupted_and_reported(self):
        self.member("alice")
        self.instance()
        self.turn = lambda ws: None
        with patch.object(planner, "TURN", 0.2):
            planner.Planner().cycle(self.now)
        self.assertEqual([e["result"] for e in self.ended], ["timeout"])
        self.assertEqual(len(self.hermes.calls("session.interrupt")), 1)

    def test_a_montage_center_that_does_not_answer_is_asked_again_later_or_for_a_new_grant(self):
        self.member("alice")
        self.instance()
        self.videocut_server.shutdown()
        self.videocut_server.server_close()
        os.unlink(planner.VIDEOCUT)
        loop = planner.Planner()
        loop.cycle(self.now)
        self.assertEqual(loop.until, self.now + 120)
        # Within the wait nothing is claimed: the socket is not even tried.
        with patch.object(planner, "videocut", side_effect=AssertionError("claimed within the wait")):
            loop.cycle(self.now + 60)
        # A grant registered since is tried at once.
        self.now += 90
        self.member("alice")
        with patch.object(planner, "plan_round") as plan_round:
            loop.cycle(self.now)
        plan_round.assert_called_once()

    def test_notes_that_do_not_read_as_written_do_not_stop_planning(self):
        self.member("alice")
        self.instance()
        notes = Path(planner.STATE)
        notes.parent.mkdir(parents=True)
        for damaged in ("{", "[]", json.dumps({"counts": []}), json.dumps({"counts": {"alice|x": "many"}})):
            with self.subTest(notes=damaged):
                notes.write_text(damaged)
                claims = len(self.claims)
                planner.Planner().cycle(self.now)
                self.assertEqual(len(self.claims), claims + 1)


if __name__ == "__main__":
    unittest.main()
