# A-NAS agent guide

## 开工

1. 先检查 `git status` 和任务涉及的路径。
2. 延续既有计划、判断优先级或接手未完成工作时，读 `docs/status/CURRENT.md`。
3. 涉及产品概念、领域命名或资源身份时，读 `CONTEXT.md`。
4. 文档类型或事实归属不明确时，使用 `docs/README.md` 路由；其余情况只加载下面与任务匹配的文档。

## 文档路由

- 组件边界、数据流或系统不变量：读 `docs/architecture/OVERVIEW.md` 和相关 ADR。
- 新功能或行为变化：读或创建 `docs/specs/` 中的规格。
- 难复现、跨边界或可能复发的缺陷：先查 `docs/investigations/`，再更新或创建调查记录。
- 安装、部署、恢复、磁盘或其他高风险操作：使用 `docs/runbooks/` 中的手册。
- 产品定位：读 `README.md`；早期产品背景、历史研究或 2026-10-06 的决策盘点：按需读 `PROJECT_CONTEXT.md`。
- 本机工具或 WSL 问题：读 `docs/development/LOCAL_ENVIRONMENT.md`。

## 验证

- `make docs-check` 和 `make check` 只使用本地源码与可丢弃构建产物，不访问实验 NAS 或生产环境；可直接运行、修复当前变更导致的失败并重跑相关检查。
- 纯文档变更运行 `make docs-check`。
- Go 代码、构建、CI 或跨模块契约变更运行 `make check`。
- 改了 `api/` 下的接口契约：运行 `make api`（或 `cd web && npm run generate`），提交它写出的打包文档、Web 类型与响应校验；契约的分文件布局见 `docs/development/LOCAL_ENVIRONMENT.md#接口契约`。
- 改 Web 代码后先运行 `cd web && npm run format`；`make check` 还包含 Oxlint、golangci-lint、Ruff 与 ShellCheck，修改并发代码另跑 `make race-test`（见 `docs/development/LOCAL_ENVIRONMENT.md#静态检查`）。
- 其他变更运行能证明结果的最小相关检查。

## Claude Code

- 本文件是唯一的项目指令文件，Claude Code 直接读取。不要新建 `CLAUDE.md` 或 `CLAUDE.local.md`：任一级目录中出现它们，Claude Code 就不再读本文件；确需时在其中写一行 `@AGENTS.md`。
- `.claude/settings.json` 是共享设置：禁止读取密钥文件，并在编辑后对 Go 文件运行 gofmt。个人覆盖写进被 Git 忽略的 `.claude/settings.local.json`。
- 可复用流程写成技能，其他工具也可以照着做：
  - [check-in-wsl](.claude/skills/check-in-wsl/SKILL.md)：在 Windows 开发机上运行门禁；
  - [system-test](.claude/skills/system-test/SKILL.md)：systemd 系统测试；
  - [fake-preview](.claude/skills/fake-preview/SKILL.md)：在独立的 `*.localhost` 主机名上启动 Fake 预览栈。

## 完工

1. 留下已运行检查或其他可复现的验证证据。
2. 只更新发生变化的事实源：术语、当前状态、架构、ADR、规格、调查或手册。
3. 在文档的“关联”部分链接相关代码、测试、ADR 或调查记录。
4. 若任务改变了当前阶段、下一步或阻塞，更新 `docs/status/CURRENT.md`。

## 记录原则

- 一项事实只有一个权威位置；其他位置使用链接。
- Git 提交记录实现历史，文档记录代码无法表达的原因、边界、复现方法和操作知识。
- `CURRENT.md` 只保留当前事实和下一步，不写逐会话流水账。
- ADR 只记录难以逆转且存在真实权衡的决定。
- 涉及真实磁盘的写操作必须由运行手册给出目标识别、确认、回滚和验证步骤。
