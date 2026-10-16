#!/usr/bin/env python3
"""Exercise sharing through production sandboxes inside system-test's container."""
import http.cookiejar
import json
import sys
import time
import urllib.error
import urllib.request

api, sender_jar, recipient_jar, sender_csrf, recipient_csrf, source_id, target_space = sys.argv[1:]

def client(jar_path, csrf):
    jar = http.cookiejar.MozillaCookieJar(jar_path)
    jar.load(ignore_discard=True, ignore_expires=True)
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    def call(method, path, body=None, expected=200):
        payload = json.dumps(body, ensure_ascii=False).encode() if body is not None else None
        request = urllib.request.Request(api + path, data=payload, method=method,
            headers={"X-CSRF-Token": csrf, "Content-Type": "application/json"})
        try:
            response = opener.open(request, timeout=20)
        except urllib.error.HTTPError as error:
            response = error
        data = response.read()
        assert response.status == expected, (method, path, response.status, data.decode(errors="replace"))
        if response.headers.get_content_type() == "application/json":
            return json.loads(data)
        return data
    return call

sender = client(sender_jar, sender_csrf)
recipient = client(recipient_jar, recipient_csrf)
recipient_id = recipient("GET", "/api/v1/session")["user"]["id"]
for attempt in range(60):
    try:
        sender("GET", "/api/v1/shares/members")
        break
    except (AssertionError, urllib.error.URLError):
        if attempt == 59:
            raise
        time.sleep(0.25)
share = sender("POST", "/api/v1/shares", {"requestId": "system-create", "sourceIds": [source_id],
    "recipientIds": [recipient_id], "message": "独立副本 👩🏽‍💻"}, 202)
base = "/api/v1/shares/" + share["id"]
for _ in range(100):
    share = sender("GET", base)
    if share["status"] != "preparing":
        break
    time.sleep(0.1)
assert share["status"] == "published", share
entry = share["entries"][0]
original = sender("GET", "/api/v1/files/" + source_id + "/content")
assert recipient("GET", base + "/entries/" + entry["id"] + "/content") == original
recipient("POST", base + "/messages", {"recipientId": recipient_id, "requestId": "system-reply", "text": "收到 👍"}, 201)
messages = sender("GET", base + "/messages?recipientId=" + recipient_id)["items"]
assert messages[-1]["text"] == "收到 👍", messages
folder = recipient("POST", "/api/v1/spaces/" + target_space + "/directories", {"name": "分享系统测试"}, 201)
job = recipient("POST", base + "/save", {"requestId": "system-save", "entryIds": [entry["id"]],
    "target": {"rootId": target_space, "parentId": folder["id"]}}, 202)
for _ in range(100):
    job = recipient("GET", "/api/v1/shares/jobs/" + job["id"])
    if job["status"] != "running":
        break
    time.sleep(0.1)
assert job["status"] == "completed", job
saved = recipient("GET", "/api/v1/spaces/" + target_space + "/entries?parentId=" + folder["id"])["items"]
assert len(saved) == 1, saved
assert recipient("GET", "/api/v1/files/" + saved[0]["id"] + "/content") == original
sent = sender("GET", base)
assert sent["recipients"][0]["savedFiles"] == 1, sent
assert target_space not in json.dumps(sent), "recipient destination leaked"
sender("POST", base + "/revoke", expected=204)
recipient("GET", base + "/entries/" + entry["id"] + "/content", expected=410)
assert recipient("GET", base + "/messages?recipientId=" + recipient_id)["items"][-1]["text"] == "收到 👍"
print("member sharing: independent copy, private replies, save and revoke passed")
