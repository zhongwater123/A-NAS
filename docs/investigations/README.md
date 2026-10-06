# 调查记录

调查记录保存复杂缺陷的证据链，使后续会话不必重复排查。根因不能由报错直接看出、问题跨越组件边界、曾出现错误假设或可能复发时创建记录；简单拼写和局部显然错误只由测试与提交记录覆盖。

## 索引

- [2026-10-06：本地 Kiosk 可进入通用 Chromium 界面](2026-10-06-kiosk-browser-confinement.md)（open, deferred for developer baseline）
- [2026-10-06：Host Agent 无法创建运行时 Socket](2026-10-06-host-agent-runtime-directory.md)（resolved）

## 使用方式

从 [`_TEMPLATE.md`](_TEMPLATE.md) 创建 `YYYY-MM-DD-short-title.md`。调查期间持续区分观察事实和假设；解决后必须记录根因、修复机制和回归证据。
