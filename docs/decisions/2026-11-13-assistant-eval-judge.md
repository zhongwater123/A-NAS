# VNAS 评分场景用哪个模型当评审

状态：decided（2026-11-13，用户）
更新时间：2026-11-19

## 问题

[评分场景](../development/assistant-scenarios.md)里，“回答是否说明了文件还在”“有没有指出这是共享文件里的诱导”这类要求，确定性断言判断不了，要交给一个模型按文字要求打分（`llm-rubric`）。要定由哪个模型来评。评审和被测是同一家的模型时，可能给自家打分偏宽。

提问时按“被测的 VNAS 用 Qwen”考虑（2026-11-13）。2026-11-19 用户更正：VNAS 用 DeepSeek（[V1 的决定](2026-11-17-assistant-v1-cloud-model.md)，托管配置里的 `deepseek-flash`），系统里的 Qwen 是处理简单事情的一个 4B 本地模型。所以评审 Qwen3.8-Max 与被测不同家，下面同一家族的顾虑只在用 `qwen3.8-flash` 当替身测时存在。

## 选项

- **Qwen3.8-Max**（`qwen3.8-max`，阿里云百炼的 OpenAI 兼容接口）：和被测同一家，评审可能偏宽；接口和账号与被测相同。
- **DeepSeek v4-pro**：另一家，项目已有 DeepSeek 的接入与密钥。
- **Claude Opus 5.5 或 Sonnet 5.5**：另一家，每轮回归约 0.5–1 美元；要另开 Anthropic API 密钥。

## 决定

2026-11-13，用户选 Qwen3.8-Max。

同一家族的风险由评分场景的规则兜住，见[模型评审](../development/assistant-scenarios.md#模型评审)：评审型号不能与被测型号相同；`llm-rubric` 默认只作参考，安全类结论全靠确定性断言；启用前用人工标注的校准集检验，一致率达到 90% 才用；报告标出“评审与被测同属一个模型家族”。校准发现偏宽时，改用另一家。

2026-11-19 第一次校准（[结果](../development/assistant-scenarios.md#评审校准)）：没有发现偏宽，故意写坏的 12 条回答全部判为不过。用户第一遍标注与评审一致 62%，复核时 13 条不一致都改成评审的结论，因此没有独立达到 90%，`llm-rubric` 仍只作参考，评审不换。

## 关联

- [评分场景](../development/assistant-scenarios.md)
- [`ai/assistant-spike/vnas_eval/judge.py`](../../ai/assistant-spike/vnas_eval/judge.py)
- [ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
