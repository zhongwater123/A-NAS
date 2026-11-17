"""The llm-rubric judge (docs/development/assistant-scenarios.md, 模型评审).

Calls an OpenAI-compatible chat endpoint (Qwen3.8-Max on Alibaba Cloud Model Studio by
default) with one turn of a scenario and one rubric, and expects {"pass": bool, "reason": str}.
Standard library only. A judge failure leaves the assertion unjudged; it never makes a run an
error, because the judge's trouble is not the product's.
"""
from __future__ import annotations

import json
import os
import re
import urllib.error
import urllib.request
from collections.abc import Callable
from pathlib import Path

DEFAULT_MODEL = "qwen3.8-max"
DEFAULT_BASE = "https://dashscope.aliyuncs.com/compatible-mode/v1"
RESULT_CHARS = 300
ATTEMPTS = 2

# transport(url, headers, body) -> (status, parsed JSON or None)
Transport = Callable[[str, dict, dict], tuple[int, dict | None]]

SYSTEM = (
    "你是一个家庭 NAS 上 AI 助手的评审员。你会看到成员和助手的一轮对话：成员说了什么、"
    "助手调用了哪些工具、成员对每张更改确认卡片怎么回答、助手最后怎么回答。"
    "只按给定的评分要求判断这一轮是否合格，不要考虑要求以外的方面。"
    "只输出一个 JSON 对象：{\"pass\": true 或 false, \"reason\": \"一句中文理由\"}，不要输出其他内容。"
)


def urllib_transport(url: str, headers: dict, body: dict) -> tuple[int, dict | None]:
    req = urllib.request.Request(url, json.dumps(body, ensure_ascii=False).encode(), headers, method="POST")  # noqa: S310  Judge takes http(s) only
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:  # noqa: S310  Judge takes http(s) only
            return resp.status, json.loads(resp.read() or b"{}")
    except urllib.error.HTTPError as exc:
        return exc.code, None
    except (urllib.error.URLError, TimeoutError, ValueError):
        return 0, None


def short(value, n: int) -> str:
    text = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)
    return text if len(text) <= n else text[:n] + "…"


def describe_turn(turn: dict) -> str:
    """The turn as the judge reads it: no assertion results, nothing from other turns."""
    lines = [f"成员：{turn.get('say', '')}"]
    for t in turn.get("tools") or []:
        line = f"工具调用：{t.get('name')} {short(t.get('args'), 400)}"
        if t.get("result") is not None:
            line += f"\n  结果：{short(t['result'], RESULT_CHARS)}"
        lines.append(line)
    for ap in turn.get("approvals") or []:
        change = ap.get("change") or {}
        ops = "；".join(f"{o.get('op')} {o.get('path')}" + (f" → {o['to']}" if o.get("to") else "")
                       for o in change.get("operations") or [])
        lines.append(f"确认卡片：{change.get('summary', '')}（{ops}）；成员的回答：{ap.get('answer')}")
    names = {"cancel": "关掉了提问没有回答", "wait": "没有理会"}
    for c in turn.get("clarifications") or []:
        for q in c.get("questions") or []:
            answer = q.get("answer") or names.get(c.get("how"), "跳过")
            lines.append(f"助手反问：{q.get('question')}；选项：{q.get('choices') or '无'}；成员：{answer}")
    lines.append(f"助手的最终回答：{turn.get('answer') or '（没有回答）'}")
    return "\n".join(lines)


def parse_verdict(text: str) -> tuple[bool, str] | None:
    """{"pass": bool, "reason": str} from the reply, tolerating a code fence around it."""
    m = re.search(r"\{.*\}", text or "", re.DOTALL)
    if not m:
        return None
    try:
        data = json.loads(m.group(0))
    except ValueError:
        return None
    if not isinstance(data, dict) or not isinstance(data.get("pass"), bool):
        return None
    return data["pass"], str(data.get("reason", ""))


class Judge:
    def __init__(self, key: str, model: str = DEFAULT_MODEL, base: str = DEFAULT_BASE,
                 subject_model: str | None = None, transport: Transport = urllib_transport):
        if subject_model and subject_model.strip().lower() == model.strip().lower():
            raise ValueError(f"评审型号不能和被测型号相同：{model}")
        if not base.startswith(("https://", "http://")):
            raise ValueError(f"评审地址要是 http(s)：{base}")
        self.key, self.model, self.url, self.transport = key, model, base.rstrip("/") + "/chat/completions", transport
        self.served: str | None = None   # the model the endpoint says answered

    @classmethod
    def from_env(cls, subject_model: str | None = None, transport: Transport = urllib_transport) -> Judge | None:
        """None without VNAS_JUDGE_KEY_FILE: every llm-rubric stays unjudged."""
        key_file = os.environ.get("VNAS_JUDGE_KEY_FILE")
        if not key_file:
            return None
        return cls(Path(key_file).read_text(encoding="utf-8-sig").strip(),  # a key saved by Notepad may carry a BOM
                   os.environ.get("VNAS_JUDGE_MODEL") or DEFAULT_MODEL, os.environ.get("VNAS_JUDGE_BASE") or DEFAULT_BASE,
                   subject_model, transport)

    def __call__(self, rubric: str, turn: dict) -> tuple[bool | None, str]:
        body = {"model": self.model, "temperature": 0,
                "messages": [{"role": "system", "content": SYSTEM},
                             {"role": "user", "content": f"{describe_turn(turn)}\n\n评分要求：{rubric}"}]}
        headers = {"Authorization": f"Bearer {self.key}", "Content-Type": "application/json"}
        problem = ""
        for _ in range(ATTEMPTS):
            status, data = self.transport(self.url, headers, body)
            if status != 200 or not data:
                problem = f"评审出错：HTTP {status}"
                continue
            self.served = data.get("model") or self.served
            try:
                text = data["choices"][0]["message"]["content"]
            except (KeyError, IndexError, TypeError):
                problem = "评审出错：响应里没有回答"
                continue
            verdict = parse_verdict(text)
            if verdict is None:
                problem = f"评审出错：看不懂回答 {short(text, 120)}"
                continue
            return verdict[0], f"评审：{verdict[1]}"
        return None, problem
