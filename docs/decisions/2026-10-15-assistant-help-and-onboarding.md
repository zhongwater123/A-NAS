# 助手的回答提到 Hermes 的 `/help` 与上手引导怎么处理

状态：pending
更新时间：2026-11-05

## 问题

VNAS 的窗口里没有斜杠命令。系统技能写明这一点后，模型（DeepSeek flash）仍会在回答末尾提到“输入 `/help`”，并主动提出整理用户档案；这来自 Hermes 自带的 `tui` 平台说明与引导（[研究记录](../research/hermes-agent-runtime.md#未解决与待验证)，2026-10-15）。

## 选项

- **保持现状**：成员偶尔看到用不了的提示。
- **另写 A-NAS 的 `SOUL.md`**：替换 Hermes 默认的人格与引导文字。
- **另定会话平台**：不再使用 `tui` 平台说明。

## 决定

待定。

## 关联

- [AI 助手规格](../specs/ai-assistant.md)、[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
- [试用手册](../runbooks/try-ai-assistant-on-experimental-nas.md)
