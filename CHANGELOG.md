# Changelog

本文件记录 A-NAS 对使用者可见的版本变化。实现过程、提交者和逐文件差异由 Git 保存；发布范围和验收证据见 [`docs/releases`](docs/releases/README.md)。

## [Unreleased]

### Added

- v1.0.1 本地实现单盘 Btrfs 三阶段初始化、SQLite/WAL 控制面、管理员与成员、个人空间与唯一 Shared。
- Web 与 SMB3 共用账号和 Policy，提供文件管理、30 天回收站、手动只读快照和按文件恢复。
- 磁盘 SMART、温度、容量、占用、文件系统、数据卷资格及统一审计导出。
- root Host Agent 类型化 IPC、非特权产品服务、root 所有的系统发布目录与 systemd 安装流程。
- 桌面“终端”应用：通过回环同源 WebSocket 打开以产品服务用户运行的 PTY Shell，支持窗口自适应、最小化保留会话和退出后新建会话；仅管理员可访问，默认关闭，以 `ANAS_TERMINAL=enabled` 启用。
- 桌面应用图标支持鼠标拖动或 Alt+方向键调整顺序，顺序保存在当前浏览器。
- 重新设计屏幕下方的程序坞：只显示彩色应用图标，以指示条区分焦点、后台与最小化应用，点击焦点应用可将其最小化。
- 重新设计桌面右上角状态栏：CPU 与内存速度仪表盘、上下行网速、实时更新的时间日期与连接状态；新增 `/api/v1/metrics` 及 Host Agent `/v1/metrics` 采样。
- Web 桌面改用为 A-NAS 生成的深蓝抽象壁纸，并以覆盖模式适配本地控制台屏幕，避免黑边。

### Security

- 本地设备启用只收集首个管理员账号和密码，不提供默认凭据；Web 使用 Argon2id、服务端会话和 CSRF，Samba 凭据失败时账号不可登录。
- 数据卷身份或 Btrfs 挂载验证失败时拒绝文件写入，绝不回退到系统盘。
- Samba 要求 SMB3、加密和签名，禁用 guest、SMB1 和 Unix extensions，只绑定显式局域网接口。

### Fixed

- 桌面时钟改为在真实分钟边界重新读取系统墙钟，不再冻结在页面首次渲染时刻。
- 用户级 RC 过渡服务获得唯一状态目录写权限，失败回滚后也能恢复旧 API，不再因 `ProtectHome=read-only` 反复退出。
- 本地首次启用不再要求用户从系统日志取得和抄录 setup code。
- 空白磁盘的格式化计划稳定返回空数组；旧 `null` 计划不再使 React 桌面只剩蓝色壁纸，存储计划、确认和执行均显示明确进度。
- Kiosk 通过 root 管理的 Chromium policy 禁止保存密码、通行密钥和同步，不再依赖 `--password-store` 参数抑制密码提示。
- 同一提交、版本和二进制哈希已经通过完整门禁时，stage 部署复用验证清单，不再重复执行全量测试。

- 桌面“Docker”应用：查看容器与镜像、运行状态与 CPU/内存占用，启动、停止（需确认）、重启容器并查看日志；新增只持有 Docker socket 的 `anas-container-agent` 与 `/api/v1/containers`。

### Security

- 容器写操作要求回环 Host、JSON 请求体与同源 Origin；产品服务用户不加入 `docker` 组，仅能调用容器代理的类型化接口。

### Planned

- 在已恢复在线的 Experimental NAS 上完成已确认 500 GB 实验盘、Btrfs、Windows SMB、文件、回收站与快照验收。
- 补齐安全移除、运行中 SATA 热拔插、同盘重新接入与自动恢复；该任务不阻塞 v1.0.1。
- 全部实机门禁通过前仅发布 `v1.0.1-rc.N`。

## [v1.0.0] - 2026-10-07

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
- 本地 Kiosk 的浏览器约束和 VT 恢复尚有开放调查，不得作为面向非受信任本地用户的安全边界。
- 详细范围、证据和已知限制见 [v1.0.0 发布记录](docs/releases/v1.0.0.md)。

[Unreleased]: https://github.com/zhongwater123/A-NAS/compare/v1.0.0...HEAD
[v1.0.0]: https://github.com/zhongwater123/A-NAS/releases/tag/v1.0.0
