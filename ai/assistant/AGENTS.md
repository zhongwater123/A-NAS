# AI 助手 VNAS（ai/assistant/）

本目录是 VNAS 的产品文件，随 `a-nas` 装到 `/usr/lib/a-nas/assistant`：每个成员一个沙箱里的 Hermes Agent，A-NAS 自己的 MCP 服务、网关、定时任务与给成员用的技能。根目录的 [AGENTS.md](../../AGENTS.md) 仍然适用。权威说明：[ADR 0024](../../docs/adr/0024-run-a-sandboxed-hermes-agent-per-member.md)、[规格](../../docs/specs/ai-assistant.md)、[Hermes 运行环境研究](../../docs/research/hermes-agent-runtime.md)。

- **Hermes 是固定版本：** 运行环境的来源、补丁与 SHA-256 记在 `runtime.json`。重新构建会改变哈希，要同时更新这里，内容包随之更新。不跟随上游最新版。
- **先看上游：** Hermes 的行为有问题时，先在固定版本的源码、官方桌面客户端与测试里找上游怎么处理，照着做并在修复和文档中写明依据。
- **权限与隐私已经定下：** 助手能做什么、哪些操作要成员确认、看得到哪些数据，按 ADR 0024 与 `docs/decisions/` 中以 assistant 开头的决定记录执行；要改先问维护者，不在代码里悄悄放宽。
- **`skills/` 是给成员的助手用的技能，** 随产品发布；不是开发用的技能（那些在 `.agents/skills/`）。
- **接缝两边一起测：** 产品服务与 Hermes、Go 与 Python 之间的消息格式按[跨进程契约](../../docs/development/testing.md#跨进程契约)，两边的测试读同一份样例。`ai/assistant-spike/` 是系统测试与真实模型评估用的沙箱脚本，用法见其中 `run.sh` 的开头；交部署前按根 AGENTS.md 跑一次带真实运行环境的系统测试。
