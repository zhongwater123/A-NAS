"""进程内的假 Qdrant：用标准库 http.server 实现 QdrantStore 用到的那几个 REST 接口。

只为离线测试客户端的"线上协议"（路径、请求体、返回体形状、错误码），不是 Qdrant 的替代品。
行为对齐 Qdrant 1.10+ 的 REST：
  GET    /                                  版本
  GET    /collections/{c}                   集合信息（不存在 404）
  PUT    /collections/{c}                   建集合 {"vectors": {"size", "distance"}}
  DELETE /collections/{c}
  PUT    /collections/{c}/points            upsert {"points": [{id, vector, payload}]}
  POST   /collections/{c}/points            按 id 取 {"ids": [...]}
  POST   /collections/{c}/points/query      {"query": [...], "limit", "filter", "score_threshold"} -> {"points": [...]}
  POST   /collections/{c}/points/delete     {"points": [...]} 或 {"filter": ...}
  POST   /collections/{c}/points/count      {"exact", "filter"} -> {"count"}
  POST   /collections/{c}/points/scroll     {"limit", "offset", "filter"} -> {"points", "next_page_offset"}
id 只接受无符号整数或 UUID 字符串，否则 400（和真 Qdrant 一致，这是最容易踩的坑）。

手工联调 finetune.vectors 时可以单独起：cd ai && python -m tests.fake_qdrant 6333

过滤条件用本文件自己的求值器（_FakeFilter），**不**导入 gemma_embed.store 的过滤器：两边各写一份，
契约测试才能发现语义分歧（A-NAS #152 S3：之前共用一个函数，两个实现一起错也照样通过）。
求值器只认 must / should / must_not、match value / any、range；别的写法回 400，就像服务端不认识。
集合 metadata（PUT 时的 "metadata"）原样存、在 GET 的 config.metadata 里返回，和真 Qdrant 一致。
"""

from __future__ import annotations

import json
import re
import sys
import threading
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import unquote, urlparse

from finetune.embedders import cosine, normalize


class _Unsupported(Exception):
    pass


class _FakeFilter:
    """按 Qdrant 文档的语义单独实现：字段缺失不满足 match/range；数组任一元素满足即可；布尔与整数不相等。"""

    @classmethod
    def ok(cls, flt, payload) -> bool:
        if flt is None:
            return True
        if not isinstance(flt, dict) or set(flt) - {"must", "should", "must_not"}:
            raise _Unsupported(f"unknown filter clause {flt!r}")
        def lst(x):
            return x if isinstance(x, list) else [x]
        must = [cls._cond(c, payload) for c in lst(flt.get("must", []))]
        should = [cls._cond(c, payload) for c in lst(flt.get("should", []))]
        must_not = [cls._cond(c, payload) for c in lst(flt.get("must_not", []))]
        if not all(must):
            return False
        if "should" in flt and lst(flt["should"]) and not any(should):
            return False
        return not any(must_not)

    @classmethod
    def _cond(cls, c, payload) -> bool:
        if isinstance(c, dict) and ({"must", "should", "must_not"} & set(c)):
            return cls.ok(c, payload)
        if not isinstance(c, dict) or "key" not in c or not ({"match", "range"} & set(c)) or set(c) - {"key", "match", "range"}:
            raise _Unsupported(f"unknown condition {c!r}")
        raw = payload.get(c["key"])
        if raw is None:
            return False
        items = raw if isinstance(raw, list) else [raw]
        if "match" in c:
            m = c["match"]
            if "value" in m:
                wanted = [m["value"]]
            elif "any" in m:
                wanted = list(m["any"])
            else:
                raise _Unsupported(f"unknown match {m!r}")
            same = lambda a, b: (type(a) is type(b) and a == b) if isinstance(a, bool) or isinstance(b, bool) else a == b  # noqa: E731
            return any(same(x, w) for x in items for w in wanted)
        rng = c["range"]
        for x in items:
            if isinstance(x, bool) or not isinstance(x, (int, float)):
                continue
            if ("gt" in rng and not x > rng["gt"]) or ("gte" in rng and not x >= rng["gte"]) \
                    or ("lt" in rng and not x < rng["lt"]) or ("lte" in rng and not x <= rng["lte"]):
                continue
            return True
        return False


def matches_filter(flt, payload) -> bool:      # 名字保留，供本文件内部用
    return _FakeFilter.ok(flt, payload)



def _valid_id(pid) -> bool:
    if isinstance(pid, bool):
        return False
    if isinstance(pid, int):
        return pid >= 0
    if isinstance(pid, str):
        try:
            uuid.UUID(pid)
            return True
        except ValueError:
            return False
    return False


class FakeQdrant:
    def __init__(self):
        self.collections: dict[str, dict] = {}       # name -> {"dim": int, "points": {id: (vec, payload)}}
        self.lock = threading.Lock()
        self.requests: list[tuple[str, str]] = []     # (method, path) 方便测试断言走了哪些接口

    def handle(self, method: str, path: str, body: dict | None) -> tuple[int, dict]:
        try:
            return self._handle(method, path, body)
        except _Unsupported as e:
            return 400, self._err(f"Format error in JSON body: {e}")

    def _handle(self, method: str, path: str, body: dict | None) -> tuple[int, dict]:
        self.requests.append((method, path))
        if path == "/":
            return 200, {"title": "qdrant - vector search engine", "version": "fake-1.10"}
        m = re.match(r"^/collections/([^/]+)(/points(?:/(query|delete|count|scroll))?)?$", path)
        if not m:
            return 404, self._err("not found")
        name = unquote(m.group(1))
        sub = m.group(2)
        action = m.group(3)
        with self.lock:
            if sub is None:
                return self._collection(method, name, body)
            col = self.collections.get(name)
            if col is None:
                return 404, self._err(f"Collection `{name}` doesn't exist!")
            if action is None and method == "PUT":
                return self._upsert(col, body or {})
            if action is None and method == "POST":
                return self._retrieve(col, body or {})
            if action == "query":
                return self._query(col, body or {})
            if action == "delete":
                return self._delete(col, body or {})
            if action == "count":
                flt = (body or {}).get("filter")
                n = sum(1 for pid, (_, pl) in col["points"].items() if matches_filter(flt, pl))
                return 200, self._ok({"count": n})
            if action == "scroll":
                return self._scroll(col, body or {})
        return 405, self._err("method not allowed")

    # -- helpers --
    @staticmethod
    def _ok(result) -> dict:
        return {"result": result, "status": "ok", "time": 0.0}

    @staticmethod
    def _err(msg: str) -> dict:
        return {"status": {"error": msg}, "time": 0.0}

    def _collection(self, method, name, body):
        if method == "GET":
            col = self.collections.get(name)
            if col is None:
                return 404, self._err(f"Collection `{name}` doesn't exist!")
            return 200, self._ok({
                "status": "green", "points_count": len(col["points"]),
                "config": {"params": {"vectors": {"size": col["dim"], "distance": "Cosine"}},
                           "metadata": col.get("metadata") or {}},
            })
        if method == "PUT":
            if name in self.collections:
                return 409, self._err(f"Collection `{name}` already exists!")
            vectors = (body or {}).get("vectors", {})
            size = vectors.get("size")
            if not isinstance(size, int) or size <= 0:
                return 400, self._err("vectors.size must be a positive integer")
            self.collections[name] = {"dim": size, "points": {}, "metadata": (body or {}).get("metadata") or {}}
            return 200, self._ok(True)
        if method == "DELETE":
            existed = self.collections.pop(name, None) is not None
            return 200, self._ok(existed)
        return 405, self._err("method not allowed")

    def _upsert(self, col, body):
        pts = body.get("points")
        if not isinstance(pts, list):
            return 400, self._err("points must be a list")
        for p in pts:
            pid = p.get("id")
            if not _valid_id(pid):
                return 400, self._err(f"value {pid!r} is not a valid point ID, valid values are either an unsigned "
                                      f"integer or a UUID")
            vec = p.get("vector")
            if not isinstance(vec, list) or len(vec) != col["dim"]:
                return 400, self._err(f"Wrong input: Vector dimension error: expected dim: {col['dim']}, "
                                      f"got {len(vec) if isinstance(vec, list) else 'none'}")
        for p in pts:
            col["points"][p["id"]] = (normalize(p["vector"]), dict(p.get("payload") or {}))
        return 200, self._ok({"operation_id": len(self.requests), "status": "completed"})

    def _retrieve(self, col, body):
        out = []
        for pid in body.get("ids", []):
            hit = col["points"].get(pid)
            if hit:
                out.append({"id": pid, "payload": hit[1]})
        return 200, self._ok(out)

    def _query(self, col, body):
        q = body.get("query")
        if not isinstance(q, list):
            return 400, self._err("query must be a vector")
        if len(q) != col["dim"]:
            return 400, self._err(f"Wrong input: Vector dimension error: expected dim: {col['dim']}, got {len(q)}")
        q = normalize(q)
        flt = body.get("filter")
        thr = body.get("score_threshold")
        hits = []
        for pid, (v, pl) in col["points"].items():
            if not matches_filter(flt, pl):
                continue
            s = cosine(q, v)
            if thr is not None and s < thr:
                continue
            hits.append({"id": pid, "version": 0, "score": s, "payload": pl if body.get("with_payload") else None})
        hits.sort(key=lambda h: h["score"], reverse=True)
        return 200, self._ok({"points": hits[: int(body.get("limit", 10))]})

    def _delete(self, col, body):
        if "points" in body:
            for pid in body["points"]:
                col["points"].pop(pid, None)
        elif "filter" in body:
            flt = body["filter"]
            col["points"] = {pid: v for pid, v in col["points"].items() if not matches_filter(flt, v[1])}
        else:
            return 400, self._err("points or filter required")
        return 200, self._ok({"operation_id": len(self.requests), "status": "completed"})

    def _scroll(self, col, body):
        flt = body.get("filter")
        limit = int(body.get("limit", 10))
        ids = sorted((str(pid) for pid, (_, pl) in col["points"].items() if matches_filter(flt, pl)))
        offset = body.get("offset")
        start = ids.index(str(offset)) if offset is not None and str(offset) in ids else 0
        page = ids[start:start + limit]
        nxt = ids[start + limit] if start + limit < len(ids) else None
        by_str = {str(pid): pid for pid in col["points"]}
        pts = [{"id": by_str[s], "payload": col["points"][by_str[s]][1]} for s in page]
        return 200, self._ok({"points": pts, "next_page_offset": (by_str[nxt] if nxt is not None else None)})


def make_handler(fake: FakeQdrant, api_key: str | None):
    class Handler(BaseHTTPRequestHandler):
        def _dispatch(self):
            if api_key and self.headers.get("api-key") != api_key:
                self._send(401, {"status": {"error": "Must provide an API key or an Authorization bearer token"}})
                return
            n = int(self.headers.get("Content-Length", "0") or 0)
            raw = self.rfile.read(n) if n else b""
            try:
                body = json.loads(raw.decode("utf-8")) if raw else None
            except json.JSONDecodeError:
                self._send(400, {"status": {"error": "invalid json"}})
                return
            code, obj = fake.handle(self.command, urlparse(self.path).path, body)
            self._send(code, obj)

        def _send(self, code: int, obj: dict):
            data = json.dumps(obj, ensure_ascii=False).encode("utf-8")
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        do_GET = do_POST = do_PUT = do_DELETE = _dispatch  # noqa: N815

        def log_message(self, *a):
            pass

    return Handler


def serve(host: str = "127.0.0.1", port: int = 0, api_key: str | None = None) -> tuple[ThreadingHTTPServer, FakeQdrant]:
    fake = FakeQdrant()
    srv = ThreadingHTTPServer((host, port), make_handler(fake, api_key))
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, fake


if __name__ == "__main__":
    p = int(sys.argv[1]) if len(sys.argv) > 1 else 6333
    s, _ = serve("127.0.0.1", p)
    print(f"假 Qdrant 在 http://127.0.0.1:{p}（只在内存里，退出即丢）")
    try:
        threading.Event().wait()
    except KeyboardInterrupt:
        s.shutdown()
