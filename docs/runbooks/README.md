# 运行手册

运行手册描述需要可重复执行、可验证并在失败时恢复的操作。安装、部署、升级、备份、恢复、诊断以及任何真实磁盘写操作都应使用运行手册。

## 索引

- [配置实验 NAS 的 SSH 开发账号](bootstrap-experimental-nas-ssh.md)（verified）
- [部署 M1 API 到实验 NAS](deploy-m1-api-to-experimental-nas.md)（verified）
- [验证 Debian Linux Adapter](verify-linux-host-state-adapter.md)（verified）
- [部署 Web 桌面预览到实验 NAS](deploy-web-preview-to-experimental-nas.md)（verified）
- [运行实验 NAS 本地控制台](operate-local-kiosk.md)（display/pointer verified；confinement/VT pending）

## 使用方式

从 [`_TEMPLATE.md`](_TEMPLATE.md) 创建 `verb-object.md`。高风险步骤必须在执行前给出稳定目标身份、预期变化和停止条件；每一步都写明完成标准。
