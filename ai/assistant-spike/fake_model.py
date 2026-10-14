"""Scripted OpenAI-compatible model for the Hermes spike.

Listens on a Unix socket, logs every request (which tools Hermes offered), and
answers deterministically:
  user "CALL <tool> <json>"  -> one tool call with those arguments
  last message is a tool result -> text echoing the start of that result
  anything else              -> "ok"
"""
import json
import os
import socketserver
import sys
import time
import uuid
from http.server import BaseHTTPRequestHandler

SOCK, LOG = sys.argv[1], sys.argv[2]


def text_of(content):
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return " ".join(p.get("text", "") for p in content if isinstance(p, dict))
    return ""


def decide(messages):
    last = messages[-1] if messages else {}
    if last.get("role") == "tool":
        return {"text": "TOOL_RESULT: " + text_of(last.get("content"))[:600]}
    user = next((m for m in reversed(messages) if m.get("role") == "user"), {})
    said = text_of(user.get("content")).strip()
    if said.startswith("CALL "):
        _, name, *rest = said.split(" ", 2)
        return {"tool": name, "args": rest[0] if rest else "{}"}
    return {"text": "ok"}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def address_string(self):
        return "uds"

    def reply(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path.rstrip("/").endswith("/models"):
            return self.reply(200, {"object": "list", "data": [{"id": "spike-model", "object": "model", "owned_by": "spike"}]})
        self.reply(404, {"error": {"message": "not found"}})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b"{}")
        messages = body.get("messages", [])
        tools = [t.get("function", {}).get("name") for t in body.get("tools", []) or []]
        choice = decide(messages)
        if body.get("tools"):
            with open(LOG + ".tools.json", "w", encoding="utf-8") as dump:
                json.dump(body["tools"], dump, ensure_ascii=False, indent=1)
        if messages and messages[0].get("role") == "system":
            with open(LOG + ".system.txt", "w", encoding="utf-8") as dump:
                dump.write(text_of(messages[0].get("content")))
        with open(LOG, "a", encoding="utf-8") as log:
            log.write(json.dumps({"t": time.time(), "path": self.path, "stream": bool(body.get("stream")),
                                  "tools": tools, "messages": len(messages), "decision": choice,
                                  "system_chars": sum(len(text_of(m.get("content"))) for m in messages if m.get("role") == "system")},
                                 ensure_ascii=False) + "\n")
        cid = "chatcmpl-" + uuid.uuid4().hex[:12]
        if "tool" in choice:
            call = {"id": "call_" + uuid.uuid4().hex[:8], "type": "function",
                    "function": {"name": choice["tool"], "arguments": choice["args"]}}
            message, finish = {"role": "assistant", "content": None, "tool_calls": [call]}, "tool_calls"
        else:
            message, finish = {"role": "assistant", "content": choice["text"]}, "stop"
        usage = {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
        if not body.get("stream"):
            return self.reply(200, {"id": cid, "object": "chat.completion", "created": int(time.time()), "model": "spike-model",
                                    "choices": [{"index": 0, "message": message, "finish_reason": finish}], "usage": usage})
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()

        def chunk(delta, finish_reason=None, **extra):
            payload = {"id": cid, "object": "chat.completion.chunk", "created": int(time.time()), "model": "spike-model",
                       "choices": [{"index": 0, "delta": delta, "finish_reason": finish_reason}], **extra}
            self.wfile.write(b"data: " + json.dumps(payload).encode() + b"\n\n")

        if "tool" in choice:
            call = message["tool_calls"][0]
            chunk({"role": "assistant", "tool_calls": [{"index": 0, **call}]})
        else:
            chunk({"role": "assistant", "content": message["content"]})
        chunk({}, finish, usage=usage)
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()
        self.close_connection = True


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


if os.path.exists(SOCK):
    os.unlink(SOCK)
with Server(SOCK, Handler) as server:
    os.chmod(SOCK, 0o666)
    server.serve_forever()
