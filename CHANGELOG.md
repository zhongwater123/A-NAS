# Changelog

本文件记录 A-NAS 对使用者可见的版本变化。实现过程、提交者和逐文件差异由 Git 保存；发布范围和验收证据见 [`docs/releases`](docs/releases/README.md)。

## [Unreleased]

### Planned

- 发现并验证当前未被 Debian 识别的 512 GB 数据盘。
- 增加只读 SMART、NVMe 健康和温度观测。

## [v1.0.0] - 2026-10-06

### Added

- 基于 Go `hoststate.Reader` 的原子只读系统与磁盘状态模型、确定性 Fake Adapter 和 Debian 13 Linux Adapter。
- 版本化 REST/JSON API、OpenAPI 3.1 契约以及 `/api/v1/host-state` 聚合读取接口。
- React/TypeScript 桌面、资源管理与系统设置窗口，以及加载、断线、503、过期数据和恢复状态。
- Unix Domain Socket 上的只读 Host Agent IPC，产品服务无需获得 root 权限。
- 带哈希校验、原子切换、健康验证和失败回滚的版本化部署流程。
- Cage/Chromium 本地控制台，以及 Experimental NAS 显示器方向与缩放的持久设备配置。

### Security

- API 默认只监听 `127.0.0.1:8080`；Host Agent 只监听权限为 `0600` 的 UDS。
- 产品服务、Host Agent 和 Kiosk 均以无 `sudo` 权限的 `anas-dev` 运行。
- 未实现的入口明确禁用；没有 SMART 证据时健康状态保持 `unknown`。

### Scope

- 这是首个硬件集成开发基线，不是具备存储写入、共享、认证、升级和恢复能力的消费级 NAS 正式版。
- 详细范围、证据和已知限制见 [v1.0.0 发布记录](docs/releases/v1.0.0.md)。

[Unreleased]: https://github.com/zhongwater123/A-NAS/compare/v1.0.0...HEAD
[v1.0.0]: https://github.com/zhongwater123/A-NAS/releases/tag/v1.0.0
