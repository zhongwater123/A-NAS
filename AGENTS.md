# A-NAS agent guide

## 开工

1. 先读 `docs/status/CURRENT.md`，确认当前阶段、下一步和已知阻塞。
2. 再读 `CONTEXT.md`，使用项目的规范术语。
3. 按 `docs/README.md` 的路由只加载与任务相关的文档。
4. 检查 `git status`；代码任务开始前确认 `make check` 的既有状态。

## 文档路由

- 组件边界、数据流或系统不变量：读 `docs/architecture/OVERVIEW.md` 和相关 ADR。
- 新功能或行为变化：读或创建 `docs/specs/` 中的规格。
- 难复现、跨边界或可能复发的缺陷：先查 `docs/investigations/`，再更新或创建调查记录。
- 安装、部署、恢复、磁盘或其他高风险操作：使用 `docs/runbooks/` 中的手册。
- 产品目标、历史研究或完整决策盘点：按需读 `PROJECT_CONTEXT.md`。
- 本机工具或 WSL 问题：读 `docs/development/LOCAL_ENVIRONMENT.md`。

## 完工

1. 运行 `make check`，留下测试或可复现的验证证据。
2. 只更新发生变化的事实源：术语、当前状态、架构、ADR、规格、调查或手册。
3. 在文档的“关联”部分链接相关代码、测试、ADR 或调查记录。
4. 若任务改变了当前阶段、下一步或阻塞，更新 `docs/status/CURRENT.md`。

## 记录原则

- 一项事实只有一个权威位置；其他位置使用链接。
- Git 提交记录实现历史，文档记录代码无法表达的原因、边界、复现方法和操作知识。
- `CURRENT.md` 只保留当前事实和下一步，不写逐会话流水账。
- ADR 只记录难以逆转且存在真实权衡的决定。
- 涉及真实磁盘的写操作必须由运行手册给出目标识别、确认、回滚和验证步骤。
