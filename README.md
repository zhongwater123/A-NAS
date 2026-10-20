# A-NAS

A-NAS 是一套基于 Debian 的 AI 智能家庭 NAS。项目首先保证存储、权限、备份与恢复可靠；AI 是可关闭、可审计、可替换的上层能力。

当前开发基线为 [v1.0.1 实验 NAS 基础存储与共享闭环](docs/releases/v1.0.1.md)：实验 NAS 跟随主干持续部署，实机验收完成前只发布 `v1.0.1-rc.N`，只允许可丢弃测试数据，不是家庭生产版。哪些功能已经部署、哪些验收尚未完成、下一步做什么，只在[当前状态](docs/status/CURRENT.md)中记录；架构决策见 [ADR](docs/adr/README.md)，版本变化见 [CHANGELOG](CHANGELOG.md)。[PROJECT_CONTEXT.md](PROJECT_CONTEXT.md) 保存早期产品背景与研究，不代表当前状态。

## 功能

用户通过浏览器中的 Web 桌面使用 A-NAS：局域网电脑与手机访问 NAS 的地址，NAS 直连屏幕运行同一个桌面。下面每项链接到它的规格，规格描述行为与边界，实现阶段见[规格索引](docs/specs/README.md)。

- **文件与共享**：每位成员有自己的个人空间，全家共用一个 Shared；Web 文件管理与 Windows SMB 都以登录者本人的 Linux 身份读写，由内核 ACL 授权，删除先进回收站（[基础存储与共享](docs/specs/basic-storage-and-sharing.md)、[统一身份与文件授权](docs/specs/unified-identity-and-file-acl.md)）。
- **传输与分享**：可续传的分片上传与打包下载（[传输中心](docs/specs/transfer-center.md)）；成员之间分享固定副本并逐人私聊（[成员文件分享](docs/specs/member-sharing.md)）。
- **备份中心**：每个空间的定时快照、智能保留、按名称找回、历史版本与整体恢复（[备份中心](docs/specs/backup-center.md)）；备份到外接盘仍在规划。
- **相册**：照片时间线、动态照片与视频；搜索、AI 聚合、物体与人物宠物识别都由设备上离线运行的本地模型完成（[ADR 0016](docs/adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)，[相册](docs/specs/photo-library.md)、[相册界面](docs/specs/photo-gallery-ui.md)、[动态照片](docs/specs/photo-motion.md)、[相册视频](docs/specs/photo-video.md)）。
- **影视与音乐**：原地索引空间中的影片与音频，浏览器不能直接播放的格式在设备上转换（[影视中心](docs/specs/media-center.md)、[音乐](docs/specs/music.md)）。
- **应用**：从应用中心安装第三方应用，在 Docker 应用中管理容器（[应用中心](docs/specs/app-center.md)、[容器管理](docs/specs/container-management.md)）。
- **设置**：账号与头像昵称、存储、系统健康、任务管理器、活动与服务日志，以及管理员可选开启的终端（[设置](docs/specs/settings.md)、[系统健康](docs/specs/system-health.md)、[任务管理器](docs/specs/task-manager.md)、[日志](docs/specs/logs.md)、[终端](docs/specs/web-terminal.md)、[头像与昵称](docs/specs/account-profile.md)）。
- **AI 助手 VNAS**：每位成员一个沙箱中的 Hermes Agent，只读本人能读的位置，写入前由成员本人确认（[规格](docs/specs/ai-assistant.md)，草案；[ADR 0024](docs/adr/0024-run-a-sandboxed-hermes-agent-per-member.md)）。正式产品尚未包含，实验 NAS 上按[试用手册](docs/runbooks/try-ai-assistant-on-experimental-nas.md)以云端模型开启。
- **其他**：[问题反馈](docs/specs/feedback.md)、[局域网 Web 访问](docs/specs/lan-web-access.md)、直连屏幕的[开机动画](docs/specs/boot-ident.md)与[屏保](docs/specs/local-console-screensaver.md)。

## 设备上的组件

各组件以不同的系统身份运行，特权只在 Host Agent 中；数据流与不变量见[架构总览](docs/architecture/OVERVIEW.md)。

| systemd 单元 | 身份 | 职责 |
|---|---|---|
| `anas-api` | `a-nas` | 产品服务：Web 桌面与 REST API，只监听回环地址 |
| `anas-host-agent` | root | 类型化的特权操作（身份、ACL、卷、Samba、快照）与文件代理；不提供任意 Shell |
| `anas-photos` | `a-nas-photos` | 相册服务，独占数据卷上的照片库（[ADR 0011](docs/adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)） |
| `anas-shares` | `a-nas-shares` | 成员分享的独立副本（[ADR 0023](docs/adr/0023-isolate-member-share-copies-in-a-dedicated-service.md)） |
| `anas-ai` | 动态身份 | 相册的本地 AI Worker，按需启动，无网络、看不到数据卷（[ADR 0016](docs/adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)） |
| `anas-media@` | 动态身份 | FFmpeg 媒体 Worker，按连接启动，无网络、看不到数据卷 |
| `anas-container-agent` | `anas-container` | Docker 的类型化代理（[ADR 0009](docs/adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)） |
| `caddy`、`smbd` | — | 局域网 Web 入口（[ADR 0012](docs/adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)）与 SMB3 |
| `anas-kiosk@tty1` | `anas-dev` | 直连屏幕上的 Cage + Chromium |

## 本地开发入口

当前推荐环境是 Windows 主机加 WSL2 Ubuntu。项目有两台开发机，差异见[本地开发环境](docs/development/LOCAL_ENVIRONMENT.md)；以下是源码在 `E:\A-NAS`、使用专用 WSL 用户的那一台：

```bash
wsl -d Ubuntu-24.04
cd ~/workspace/A-NAS
bash scripts/check-dev-env.sh
```

WSL 专用用户为 `anas-dev`。该账号不属于 `sudo` 或 `docker` 组，避免 A-NAS 开发环境默认拥有主机管理权限。需要维护 WSL 时，从 Windows 显式进入 root：

```powershell
wsl -d Ubuntu-24.04 -u root
```

仓库已使用公开 GitHub 身份配置本地提交信息；`anas-dev` 的全局 Git 身份保持为空，避免影响其他项目。

产品服务和 Host Agent 使用 Go，当前版本记录在 `.go-version`；Web 桌面使用 React、TypeScript、Vite 和 npm lockfile；相册的本地 AI Worker 使用 Python。控制面数据库使用 SQLite/WAL，存储与特权边界见 [ADR 0007](docs/adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)。

## 开发与检查

运行前端类型检查与测试、生产资源构建、文档检查、部署脚本检查、Go 检查与 AI Worker 测试，并构建三个 Linux 命令：

```bash
make check
```

构建产物写入被 Git 忽略的 `build/`：

```text
build/anas-api
build/anas-host-agent
build/anas-container-agent
```

修改服务单元、安装器、Host Agent、文件代理或文件权限时，部署前还要运行在 systemd 下安装整套服务的[系统测试](docs/development/LOCAL_ENVIRONMENT.md#系统测试)：`make system-test`。

Go 模块路径为 `github.com/zhongwater123/A-NAS`。架构语言决策记录在 [ADR 0001](docs/adr/0001-use-go-for-product-services.md)。

启动带内嵌 Web 桌面和确定性 Fake Adapter 的本地产品服务：

```bash
make web-build
go run ./cmd/anas-api
```

浏览器访问 `http://127.0.0.1:8080/`。服务默认只监听回环地址，可通过 `ANAS_HTTP_ADDR` 覆盖；聚合状态接口为 `/api/v1/host-state`，完整契约见 [OpenAPI](api/openapi.yaml)。

需要由设备管理员在“设置 → 终端”中打开设备 Shell 时，显式启用终端（设备上按[运行手册](docs/runbooks/enable-admin-terminal.md)开启）；生产中 Shell 经文件代理以该管理员本人的 Linux 账号运行（本地模拟模式下以开发进程用户运行），只接受已认证管理员的回环同源连接，边界见[终端规格](docs/specs/web-terminal.md)：

```bash
ANAS_TERMINAL=enabled go run ./cmd/anas-api
```

桌面“Docker”应用在模拟模式下默认使用确定性的 Fake 容器；连接真实 Docker 需要容器代理，见 [ADR 0009](docs/adr/0009-use-docker-engine-through-a-dedicated-container-agent.md) 与[安装运行手册](docs/runbooks/install-container-agent.md)。

前端单独开发时可运行 `cd web && npm run dev`；Vite 把 `/api` 和 `/healthz` 代理到本地 Go 服务。前端技术原因见 [ADR 0003](docs/adr/0003-use-react-typescript-for-web-desktop.md)。

## 部署

实验 NAS 上的系统服务由 root 运行 release 自带的安装器安装：开发机构建并校验 release，经 `scripts/deploy-dev.ps1 -StageOnly` 以 `anas-dev` 暂存到 NAS，再由设备所有者以 root 安装。安装、回滚、破坏性磁盘计划与实机验收见[实机配置手册](docs/runbooks/provision-v1.0.1-experimental-storage.md)，其余操作见[运行手册索引](docs/runbooks/README.md)；从开发机经限定的 SSH 隧道远程浏览见 [Web 预览运行手册](docs/runbooks/deploy-web-preview-to-experimental-nas.md#远程浏览与验证)。

## 项目文档

新会话从 [AGENTS.md](AGENTS.md) 开始；它会根据任务把开发者或 Agent 路由到当前状态、领域语言、架构、规格、调查记录或运行手册。文档分类和维护规则见 [文档系统](docs/README.md)。

文档结构和仓库内链接由 Go 工具检查：

```bash
make docs-check
```
