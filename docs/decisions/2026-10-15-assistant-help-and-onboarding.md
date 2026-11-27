# 助手的回答提到 Hermes 的 `/help` 与上手引导怎么处理

状态：pending
更新时间：2026-11-23

## 问题

VNAS 的窗口里没有斜杠命令。系统技能写明这一点后，模型（DeepSeek flash）仍会在回答末尾提到“输入 `/help`”，并主动提出整理用户档案；这来自 Hermes 自带的 `tui` 平台说明与引导（[研究记录](../research/hermes-agent-runtime.md#未解决与待验证)，2026-10-15）。

2026-11-23 在固定提交 8fd9f706 上重查（[评分场景](../development/assistant-scenarios.md#回归里看到的-hermes-默认行为)）：`tui` 平台说明已不再提 `/help`，但第一句是 “You are in the Hermes terminal UI”；`/help` 与建档案来自首次接触提示（`agent/onboarding.py`），只附在成员第一次说的话后面，建档案那段还提议上网查公开资料，而沙箱里没有网络。回归每遍都算第一次：11-19 的 101 条回答里 81% 提 `/help`、76% 提议建档案、约 9% 自称 Hermes。托管配置加 `onboarding: {profile_build: "off"}` 后，boundary 30 遍里建档案的提议从 26 条降到 0 条，`/help` 仍有 25 条；Hermes 没有关掉整段提示的开关。

## 选项

- **保持现状**：成员第一次对话时看到用不了的提示。
- **托管配置关掉建档案的提议**（`onboarding.profile_build: "off"`）：只剩一两句自我介绍和 `/help`，已在回归里验证，`/help` 那句还在。
- **另写 A-NAS 的 `SOUL.md`**：替换 Hermes 默认的人格与引导文字。
- **另定会话平台**：不再使用 `tui` 平台说明。

## 决定

待定。

## 关联

- [AI 助手规格](../specs/ai-assistant.md)、[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
- [启用 AI 助手](../runbooks/enable-ai-assistant.md)
