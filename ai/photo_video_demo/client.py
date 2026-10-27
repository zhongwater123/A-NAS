"""The product service as the test account sees it, over HTTP with a session cookie."""

import http.client
import http.cookies
import json
import urllib.parse

SESSION_COOKIE = "anas_session"
TIMEOUT = 60


class ProductError(Exception):
    """A failed product call, carrying the service's own error code."""

    def __init__(self, code: str, message: str, status: int = 0):
        super().__init__(message)
        self.code = code
        self.message = message
        self.status = status


class Client:
    """Signs in on first use and once more when the session has expired (401)."""

    def __init__(self, base_url: str, username: str, password: str):
        url = urllib.parse.urlsplit(base_url)
        if url.scheme not in ("http", "https") or not url.hostname:
            raise ValueError(f"not an http(s) URL: {base_url!r}")
        self._url = url
        self._username = username
        self._password = password
        self._cookie = ""

    def json(self, path: str, **query: str) -> dict:
        status, body, _ = self._get(path, query)
        try:
            return json.loads(body or b"{}")
        except ValueError:
            raise ProductError("bad_response", f"产品服务返回了看不懂的结果（{status}）", status) from None

    def image(self, path: str) -> bytes:
        status, body, content_type = self._get(path, {})
        if not content_type.startswith("image/"):
            raise ProductError("bad_response", f"产品服务没有返回图片（{status}，{content_type}）", status)
        return body

    def _get(self, path: str, query: dict) -> tuple[int, bytes, str]:
        query = {key: value for key, value in query.items() if value}
        target = path + ("?" + urllib.parse.urlencode(query) if query else "")
        if not self._cookie:
            self._sign_in()
        status, body, headers = self._request("GET", target)
        if status == 401:
            self._sign_in()
            status, body, headers = self._request("GET", target)
        if status != 200:
            raise _error(status, body)
        return status, body, headers.get("Content-Type", "")

    def _sign_in(self) -> None:
        payload = json.dumps({"username": self._username, "password": self._password}).encode()
        status, body, headers = self._request("POST", "/api/v1/session", payload)
        if status != 200:
            raise _error(status, body)
        cookies = http.cookies.SimpleCookie()
        for value in headers.get_all("Set-Cookie") or []:
            cookies.load(value)
        if SESSION_COOKIE not in cookies:
            raise ProductError("bad_response", "登录成功但产品服务没有给出会话", status)
        self._cookie = f"{SESSION_COOKIE}={cookies[SESSION_COOKIE].value}"

    def _request(self, method: str, target: str, body: bytes | None = None):
        kind = http.client.HTTPSConnection if self._url.scheme == "https" else http.client.HTTPConnection
        conn = kind(self._url.hostname, self._url.port, timeout=TIMEOUT)
        headers = {"Accept": "application/json, image/*"}
        if body is not None:
            headers["Content-Type"] = "application/json"
        if self._cookie:
            headers["Cookie"] = self._cookie
        try:
            conn.request(method, self._url.path.rstrip("/") + target, body=body, headers=headers)
            response = conn.getresponse()
            return response.status, response.read(), response.headers
        except OSError as error:
            raise ProductError("product_unreachable", f"连不上产品服务：{error}") from None
        finally:
            conn.close()


def _error(status: int, body: bytes) -> ProductError:
    try:
        detail = json.loads(body).get("error") or {}
    except (ValueError, AttributeError):
        detail = {}
    return ProductError(detail.get("code") or f"http_{status}", detail.get("message") or f"产品服务返回 {status}", status)
