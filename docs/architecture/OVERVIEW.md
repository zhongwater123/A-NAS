# 架构总览

本页记录开发时必须保持的系统边界和当前代码入口。完整产品背景与研究依据见 [PROJECT_CONTEXT.md](../../PROJECT_CONTEXT.md)。

## 系统边界

```text
NAS 本地控制台（Cage + Chromium）或隧道后的远程浏览器
  │ 回环地址上的同源 HTTP / REST
  ▼
产品服务（非特权）
  ├── Auth / Files / Storage / Share / Jobs / Policy
  ├── Photo Library / Catalog / Search
  ├── AI Orchestrator
  └── hoststate.Reader
          ├── Fake Adapter（当前本地开发与契约测试）
          └── Host Agent Client Adapter
                    │ 窄而类型化的 IPC
                    ▼
                Host Agent（root 系统服务、受限 UDS）
                    ├── Linux 状态 Adapter
                    └── 类型化卷、Samba 与 Btrfs 快照操作

Web / SMB3 → Policy → 个人空间或 Shared → Btrfs 数据卷
                         ├── 隐藏回收站
                         └── 不通过 SMB 暴露的只读快照

照片导入 → 受管对象存储 + Catalog → 派生任务 → AI Provider → Search Index → Policy 过滤 → 照片资产
```

## 不变量

1. AI 服务停止时，文件共享、权限、快照、备份和恢复仍然工作。
2. 产品服务以非 root 身份运行；特权操作只进入 Host Agent。
3. Host Agent 接收经过校验的高层意图，不提供任意 Shell 执行接口。
4. 磁盘和文件使用稳定资源 ID；临时路径和 `/dev/sdX` 不是身份。
5. 修改宿主机状态的操作遵循“规划、确认、执行”三阶段。
6. 文件浏览、共享、传统搜索和 AI 检索共同使用 Policy。
7. AI 派生数据可重建、可清除，原文件只读进入 AI Pipeline。
8. Fake 与 Linux Adapter 遵循同一契约，差异由契约测试暴露。
9. 本地控制台与远程浏览器使用同一 Web UI 和产品 API；Kiosk 不获得额外宿主机权限。
10. 受管图库以稳定照片资产 ID 表达用户可见身份；内容对象路径和内容哈希不成为公共文件身份。
11. AI Worker 不能写原图、权限或用户元数据；只有 Photo Library Module 可以提交权威目录状态。
12. 数据卷离线、只读或低于保留容量时拒绝写入；不得在系统盘创建替代空间。
13. Web 与 SMB 共用账号和 Policy，但密码凭据分别以不可逆格式保存，明文只存在于创建或重置调用期间。

## 当前代码入口

| 路径 | 责任 | 当前状态 |
|---|---|---|
| `cmd/anas-api` | 非特权产品 API 进程入口 | 持久化账号、存储、文件、回收站、快照与可选终端已接线 |
| `cmd/anas-host-agent` | root Host Agent 进程入口 | 通过组限制 UDS 提供状态及类型化特权操作 |
| `web` / `internal/webui` | React Web 桌面、嵌入式静态资源和本地控制台外部媒体 Handler | 登录、文件、回收站、快照、账号、存储、资源管理、终端窗口与本地屏保已实现 |
| `internal/terminal` | 回环同源 WebSocket 上的 PTY 终端，以产品服务用户运行 | 仅管理员可访问；默认关闭，`ANAS_TERMINAL=enabled` 启用，见[终端规格](../specs/web-terminal.md) |
| `deploy/systemd/system` / `deploy/pam` / `deploy/config` | 直连屏幕的非特权 Cage/Chromium 会话与设备配置 | 显示和鼠标已验收；浏览器约束与 VT 恢复待处理 |
| `internal/hoststate/agent` | Unix Socket 上的 Host Agent server/client Adapter | 状态、指标、卷、凭据与快照 IPC 已实现 |
| `internal/hoststate` | 只读宿主机状态与 CPU/内存/网速指标接口、Fake Adapter 与 Debian Linux Adapter | 状态已通过本地及实验 NAS 测试；指标采样见[状态栏规格](../specs/host-metrics-status-bar.md)，尚未在实验 NAS 部署 |
| `internal/accounts` / `internal/files` / `internal/storage` | 身份 Policy、文件闭环和持久化执行计划 | 本地实现与测试完成，实机验收待进行 |
| `internal/hostops/linux` | 固定命令的卷、Samba 和 Btrfs 快照执行器 | Fake command 测试完成，实机验收待进行 |
| `internal/httpapi` | REST/JSON 路由与 DTO 映射 | 已实现并通过 OpenAPI 契约测试 |
| `api/openapi.yaml` | 客户端产品接口契约 | OpenAPI 3.1 |
| `tools/doccheck` | 文档结构和链接检查 | 开发工具 |
| `docs/specs` | 可验收的产品行为 | 已记录首个只读状态规格 |
| `docs/investigations` | 复杂缺陷的证据和根因 | 已记录并关闭首个实机部署调查 |
| `docs/runbooks` | 可重复且可验证的操作 | SSH、用户态部署与 Linux Adapter 验收手册已验证 |

新增模块时，应在其代码附近放置包级说明和测试；只有跨模块关系或系统不变量才更新本页。

## 关联

- [领域语言](../../CONTEXT.md)
- [当前状态](../status/CURRENT.md)
- [ADR 索引](../adr/README.md)
- [Go 语言决策](../adr/0001-use-go-for-product-services.md)
- [REST/OpenAPI 决策](../adr/0002-use-rest-openapi-for-product-clients.md)
- [Web 桌面技术决策](../adr/0003-use-react-typescript-for-web-desktop.md)
- [Host Agent IPC 决策](../adr/0004-use-http-json-over-unix-socket-for-host-state.md)
- [本地控制台决策](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- [受管图库决策](../adr/0006-use-a-managed-photo-library.md)
- [相册技术设计](photo-library.md)
- [存储与文件架构](storage-and-files.md)
- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [基础存储 ADR](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- [相册与本地智能检索规格](../specs/photo-library.md)
- [只读宿主机状态规格](../specs/read-only-host-state.md)
- [Web 桌面终端规格](../specs/web-terminal.md)
- [状态栏与实时指标规格](../specs/host-metrics-status-bar.md)
