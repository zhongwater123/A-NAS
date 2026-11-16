"""规划模型（Planner）的两种实现。

- ClaudeCli：开发机上的 Claude Code 命令行（claude -p），不带任何工具、设置和 MCP 服务，只看到提示词。第六版的做法。
- ClaudeApi：Anthropic Python SDK 调 Messages API，用结构化输出（output_config.format）约束回答。板子和 NAS 上
  没有命令行客户端时用它；按 #341 声明外部调用，密钥从 ANTHROPIC_API_KEY 读。
"""

import json
import os
import subprocess
import time
from dataclasses import dataclass


@dataclass
class ClaudeCli:
    binary: str = "claude"
    model: str = "claude-opus-5-5"
    cwd: str = "."
    timeout: int = 1800

    def ask(self, system, user, schema):
        t0 = time.perf_counter()
        run = subprocess.run([self.binary, "-p", "--model", self.model, "--output-format", "json", "--tools", "",
                              "--setting-sources", "", "--strict-mcp-config", "--no-session-persistence",
                              "--disable-slash-commands", "--system-prompt", system,
                              "--json-schema", json.dumps(schema, ensure_ascii=False)],
                             input=user, capture_output=True, text=True, timeout=self.timeout, cwd=self.cwd, check=False)
        out = json.loads(run.stdout or "{}")
        if run.returncode or out.get("is_error") or "structured_output" not in out:
            raise RuntimeError(f"{self.model}: {out.get('result') or run.stderr[-500:]}")
        usage = out.get("usage", {})
        return out["structured_output"], {
            "seconds": round(time.perf_counter() - t0, 1), "model": self.model, "usd": out.get("total_cost_usd"),
            "input": sum(usage.get(k) or 0 for k in ("input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens")),
            "output": usage.get("output_tokens")}


# 每百万 token 的美元价（输入、输出），只用来估开销。
PRICES = {"claude-opus-5-5": (4.0, 20.0), "claude-sonnet-5-5": (2.0, 10.0)}


def strict(schema):
    """结构化输出要求每个对象都写 additionalProperties: false。"""
    if isinstance(schema, dict):
        out = {k: strict(v) for k, v in schema.items()}
        if out.get("type") == "object":
            out.setdefault("additionalProperties", False)
        return out
    if isinstance(schema, list):
        return [strict(v) for v in schema]
    return schema


@dataclass
class ClaudeApi:
    model: str = "claude-opus-5-5"
    effort: str = "high"
    max_tokens: int = 64000

    def ask(self, system, user, schema):
        import anthropic  # 只在用到时需要：开发机走命令行时不必安装

        client = anthropic.Anthropic(api_key=os.environ.get("ANTHROPIC_API_KEY"))
        t0 = time.perf_counter()
        # 服务端回退：安全分类器拒绝时由服务端按类别换模型重答，不用自己维护模型列表。
        with client.beta.messages.stream(
            model=self.model, max_tokens=self.max_tokens, system=system,
            messages=[{"role": "user", "content": user}],
            output_config={"effort": self.effort, "format": {"type": "json_schema", "schema": strict(schema)}},
            betas=["server-side-fallback-2026-07-01"], fallbacks="default",
        ) as stream:
            message = stream.get_final_message()
        if message.stop_reason == "refusal":
            raise RuntimeError(f"{self.model} 拒绝了这次规划")
        if message.stop_reason == "max_tokens":
            raise RuntimeError(f"{self.model} 的回答超过 {self.max_tokens} tokens 被截断")
        text = next(b.text for b in message.content if b.type == "text")
        u = message.usage
        tokens_in = (u.input_tokens or 0) + (u.cache_creation_input_tokens or 0) + (u.cache_read_input_tokens or 0)
        price = PRICES.get(self.model)
        usd = round((tokens_in * price[0] + u.output_tokens * price[1]) / 1e6, 4) if price else None
        return json.loads(text), {"seconds": round(time.perf_counter() - t0, 1), "model": self.model, "usd": usd,
                                  "input": tokens_in, "output": u.output_tokens}
