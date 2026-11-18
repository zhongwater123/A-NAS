"""视觉模型（Describer）：经 OpenAI 兼容的 /v1/chat/completions 接口描述卡片。

开发机和 x86 NAS 上是 llama.cpp 的 llama-server（Qwen3.5-4B + mmproj）。RK3588 上同一个适配器指向板子上的服务，
两种可能（明天实测）：
- llama-server 跑在 CPU 上：和开发机完全一样，只是慢；
- RKLLM（NPU）：Rockchip 的运行时本身不是 HTTP 服务，需要在板子上起一个 OpenAI 兼容的服务包住它。它多半不支持
  按 JSON Schema 约束输出，所以 schema 模式设成 "prompt"：把 schema 写进提示词，回答在本地检查、不合格就重问。

schema 模式：
- "server"：response_format 带 json_schema，由服务端约束（llama-server 支持）；
- "prompt"：不发 response_format，schema 附在提示词末尾，本地解析和检查。
"""

import base64
import json
import re
import time
from dataclasses import dataclass, field
from pathlib import Path

from ..domain.describe import DescribeError

# 回答陷入重复或不合 schema 时，换更强的重复惩罚重问。
REPEAT_PENALTIES = (1.1, 1.2, 1.3)


def image_part(path: str) -> dict:
    data = base64.b64encode(Path(path).read_bytes()).decode("ascii")
    return {"type": "image_url", "image_url": {"url": f"data:image/jpeg;base64,{data}"}}


def _required(value, schema: dict, where: str = "回答") -> None:
    """只查必需字段、类型、数组个数和枚举：够发现小模型常见的错，不引入 jsonschema 依赖。"""
    kind = schema.get("type")
    if kind == "object":
        if not isinstance(value, dict):
            raise DescribeError(f"{where} 不是对象")
        for key in schema.get("required", []):
            if key not in value:
                raise DescribeError(f"{where} 缺 {key}")
        for key, sub in schema.get("properties", {}).items():
            if key in value:
                _required(value[key], sub, f"{where}.{key}")
    elif kind == "array":
        if not isinstance(value, list):
            raise DescribeError(f"{where} 不是数组")
        if not schema.get("minItems", 0) <= len(value) <= schema.get("maxItems", len(value)):
            raise DescribeError(f"{where} 有 {len(value)} 项")
        for n, item in enumerate(value):
            _required(item, schema.get("items", {}), f"{where}[{n}]")
    elif kind == "string":
        if not isinstance(value, str):
            raise DescribeError(f"{where} 不是字符串")
        if "enum" in schema and value not in schema["enum"]:
            raise DescribeError(f"{where} 不在 {schema['enum']} 里")


def parse_json(text: str):
    """回答里的 JSON；"prompt" 模式下模型可能包上 ```json 代码块。"""
    fenced = re.search(r"```(?:json)?\s*(.*?)```", text, re.DOTALL)
    return json.loads(fenced.group(1) if fenced else text)


@dataclass
class OpenAIVision:
    """url：服务地址（http://127.0.0.1:8094）；model：服务要求时填模型名；schema_mode："server" 或 "prompt"。"""

    url: str
    model: str = ""
    schema_mode: str = "server"
    temperature: float = 0.2
    max_tokens: int = 1800
    timeout: int = 600
    extra: dict = field(default_factory=lambda: {"chat_template_kwargs": {"enable_thinking": False}})

    def _content(self, content: list, schema: dict) -> list:
        out = []
        for kind, value in content:
            out.append({"type": "text", "text": value} if kind == "text" else image_part(value))
        if self.schema_mode == "prompt":
            out.append({"type": "text", "text": "只输出符合下面 JSON Schema 的 JSON，不要别的文字：\n"
                                                + json.dumps(schema, ensure_ascii=False)})
        return out

    def describe(self, system, content, schema):
        import requests  # 用到时才要：只跑领域测试的环境可以不装

        parts = self._content(content, schema)
        errors, total = [], 0.0
        for penalty in REPEAT_PENALTIES:
            body = {"messages": [{"role": "system", "content": system}, {"role": "user", "content": parts}],
                    "temperature": self.temperature, "repeat_penalty": penalty, "max_tokens": self.max_tokens, **self.extra}
            if self.model:
                body["model"] = self.model
            if self.schema_mode == "server":
                body["response_format"] = {"type": "json_schema", "json_schema": {"name": "answer", "schema": schema}}
            t0 = time.perf_counter()
            resp = requests.post(f"{self.url}/v1/chat/completions", json=body, timeout=self.timeout)
            total += time.perf_counter() - t0
            resp.raise_for_status()
            try:
                answer = parse_json(resp.json()["choices"][0]["message"]["content"])
                _required(answer, schema)
                return answer, total, errors
            except (ValueError, KeyError, DescribeError) as error:
                errors.append(f"repeat_penalty={penalty}: {error}")
        raise DescribeError("; ".join(errors))
