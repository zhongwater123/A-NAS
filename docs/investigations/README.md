# 调查记录

调查记录保存复杂缺陷的证据链，使后续会话不必重复排查。根因不能由报错直接看出、问题跨越组件边界、曾出现错误假设或可能复发时创建记录；简单拼写和局部显然错误只由测试与提交记录覆盖。

## 索引

- [2026-10-06：本地 Kiosk 可进入通用 Chromium 界面](2026-10-06-kiosk-browser-confinement.md)（open, deferred for developer baseline）
- [2026-10-06：Host Agent 无法创建运行时 Socket](2026-10-06-host-agent-runtime-directory.md)（resolved）
- [2026-10-06：桌面时钟持续落后于系统时间](2026-10-06-system-clock-drift-and-network-sync.md)（resolved）
- [2026-10-07：RC 激活与回滚后用户 API 无法启动](2026-10-07-user-service-state-directory.md)（resolved）
- [2026-10-07：首次管理员创建在 Samba 系统账号边界失败](2026-10-07-samba-account-sandbox.md)（resolved）
- [2026-10-07：空白磁盘计划导致本地桌面蓝屏](2026-10-07-blank-disk-plan-ui-crash.md)（resolved）
- [2026-10-07：数据卷空间权限阻断文件闭环](2026-10-07-data-volume-space-permissions.md)（superseded by ADR 0008）
- [2026-10-08：ADR 0008 首次升级时 Host Agent 无法启动](2026-10-08-adr0008-bootstrap-private-acl.md)（resolved）
- [2026-10-08：Experimental NAS 的 Docker 与应用中心保持禁用](2026-10-08-experimental-nas-containers-disabled.md)（resolved）
- [2026-10-08：OpenList 安装后因数据目录权限反复重启](2026-10-08-openlist-runtime-identity.md)（resolved）
- [2026-10-08：Host Agent 在 systemd 沙箱下丢失 CAP_SETUID](2026-10-08-host-agent-loses-setuid-under-systemd.md)（fix in PR #39；Experimental NAS verification pending）

## 使用方式

从 [`_TEMPLATE.md`](_TEMPLATE.md) 创建 `YYYY-MM-DD-short-title.md`。调查期间持续区分观察事实和假设；解决后必须记录根因、修复机制和回归证据。
