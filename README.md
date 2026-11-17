# A-NAS

A-NAS 是一套基于 Debian 的 AI 智能家庭 NAS。项目首先保证存储、权限、备份与恢复可靠；AI 是可关闭、可审计、可替换的上层能力。

当前开发基线为 [v1.0.1 实验 NAS 基础存储与共享闭环](docs/releases/v1.0.1.md)：实验 NAS 跟随主干持续部署，实机验收完成前只发布 `v1.0.1-rc.N`，只允许可丢弃测试数据，不是家庭生产版。哪些功能已经部署、哪些验收尚未完成、下一步做什么，只在[当前状态](docs/status/CURRENT.md)中记录；架构决策见 [ADR](docs/adr/README.md)，版本变化见 [CHANGELOG](CHANGELOG.md)。[PROJECT_CONTEXT.md](PROJECT_CONTEXT.md) 保存早期产品背景与研究，不代表当前状态。

## 功能

用户通过浏览器中的 Web 桌面使用 A-NAS：局域网电脑与手机访问 NAS 的地址，在外面经设备的域名以 HTTPS 访问，NAS 直连屏幕运行同一个桌面。下面每项链接到它的规格，规格描述行为与边界，实现阶段见[规格索引](docs/specs/README.md)。

- **文件与共享**：每位成员有自己的个人空间，全家共用一个 Shared；Web 文件管理与 Windows SMB 都以登录者本人的 Linux 身份读写，由内核 ACL 授权，删除先进回收站（[基础存储与共享](docs/specs/basic-storage-and-sharing.md)、[统一身份与文件授权](docs/specs/unified-identity-and-file-acl.md)）。
- **硬盘与数据卷**：插入新硬盘后在“设置 → 存储”中把它加入数据卷，选择镜像、扩容或条带；镜像少了一块盘时数据卷继续服务，盘接回或换上新盘后恢复冗余（[硬盘用途与数据卷冗余](docs/specs/disk-roles-and-redundancy.md)、[ADR 0025](docs/adr/0025-span-the-data-volume-across-disks-with-btrfs-profiles.md)）。
- **传输与分享**：可续传的分片上传与打包下载（[传输中心](docs/specs/transfer-center.md)）；成员之间分享固定副本并逐人私聊（[成员文件分享](docs/specs/member-sharing.md)）。
- **备份中心**：每个空间的定时快照、智能保留、按名称找回、历史版本与整体恢复（[备份中心](docs/specs/backup-center.md)）；备份到外接盘仍在规划。
- **相册**：照片时间线、动态照片与视频；搜索、AI 聚合、物体与人物宠物识别、照片中文字的识别与搜索，都由设备上离线运行的本地模型完成（[ADR 0016](docs/adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)，[相册](docs/specs/photo-library.md)、[相册界面](docs/specs/photo-gallery-ui.md)、[动态照片](docs/specs/photo-motion.md)、[相册视频](docs/specs/photo-video.md)、[照片中的文字](docs/architecture/photo-ocr.md)）。
- **影视与音乐**：原地索引空间中的影片与音频，浏览器不能直接播放的格式在设备上转换（[影视中心](docs/specs/media-center.md)、[音乐](docs/specs/music.md)）。
- **家庭中心**：家人在宝宝频道里一起记录照片视频、录音、日志、身高体重、疫苗接种与大事记，照片视频留在共享图库；第一版只有宝宝频道（[家庭中心](docs/specs/family-center.md)、[ADR 0029](docs/adr/0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)）。
- **网页搜索**：在桌面上直接搜索互联网，内置的 SearXNG 把查询发给多个公网搜索引擎，合并去重后显示；默认开启，管理员可以关闭（[网页搜索](docs/specs/web-search.md)、[ADR 0030](docs/adr/0030-ship-web-search-as-a-built-in-searxng-service.md)）。
- **应用**：从应用中心安装第三方应用，在 Docker 应用中管理容器（[应用中心](docs/specs/app-center.md)、[容器管理](docs/specs/container-management.md)）。
- **设置**：账号与头像昵称、用户与权限（含重新启用与删除账号）、存储、个性化（壁纸与本机息屏）、系统健康、设备温度与风扇、任务管理器、活动与服务日志，以及管理员可选开启的终端（[设置](docs/specs/settings.md)、[头像与昵称](docs/specs/account-profile.md)、[重新启用与删除账号](docs/specs/account-enable-and-delete.md)、[个性化](docs/specs/personalization.md)、[系统健康](docs/specs/system-health.md)、[设备温度与风扇](docs/specs/device-temperatures.md)、[任务管理器](docs/specs/task-manager.md)、[日志](docs/specs/logs.md)、[终端](docs/specs/web-terminal.md)）。
- **系统更新**：管理员在“设置 → 系统更新”中下载并安装新版本；设备定时检查、后台下载，安装后检查不通过时自动回到原来的版本。目前支持以软件包安装的设备，镜像设备仍是草案（[系统更新](docs/specs/system-update.md)、[ADR 0034](docs/adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)）。
- **AI 助手 VNAS**：每位成员一个沙箱中的 Hermes Agent，以成员本人的身份运行，只读本人能读的位置，写入前由成员本人确认；可以按时间运行定时任务（[规格](docs/specs/ai-assistant.md)、[ADR 0024](docs/adr/0024-run-a-sandboxed-hermes-agent-per-member.md)）。它以单独的试用软件包 `a-nas-assistant-trial` 提供，使用云端模型，开启方法见[试用手册](docs/runbooks/try-ai-assistant-on-experimental-nas.md)。
- **虚拟伙伴**：桌面右下角的“大肥鱼”随本设备上 VNAS 的对话切换动作，VNAS 能让它说话、移动和睡觉；形象是 CC BY-NC-SA 4.0 的社区二创，只作非商业试验（[虚拟伙伴](docs/specs/virtual-companion.md)，V1）。
- **照片打分**：可移除模块。选定的成员在“照片打分”窗口里给共享文件夹中的照片打美学分并导出结果，打分结束后整体移除；不随软件包发布，按[运行手册](docs/runbooks/install-and-remove-photo-scoring.md)单独安装（[照片打分](docs/specs/photo-scoring.md)、[ADR 0037](docs/adr/0037-plug-removable-modules-in-as-separate-services.md)）。
- **其他**：[问题反馈](docs/specs/feedback.md)、[系统快捷栏](docs/specs/system-rail.md)、[状态栏的实时资源指标](docs/specs/host-metrics-status-bar.md)、[局域网 Web 访问](docs/specs/lan-web-access.md)、[互联网访问](docs/specs/internet-access.md)、直连屏幕的[开机动画](docs/specs/boot-ident.md)与[屏保](docs/specs/local-console-screensaver.md)。

## 设备上的组件

各组件以不同的系统身份运行，特权只在 Host Agent 中；数据流与不变量见[架构总览](docs/architecture/OVERVIEW.md)。

| systemd 单元 | 身份 | 职责 |
|---|---|---|
| `anas-api` | `a-nas` | 产品服务：Web 桌面与 REST API，只监听回环地址 |
| `anas-host-agent` | root | 类型化的特权操作（身份、ACL、卷、Samba、快照）与文件代理；不提供任意 Shell |
| `anas-photos` | `a-nas-photos` | 相册服务，独占数据卷上的照片库（[ADR 0011](docs/adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)） |
| `anas-shares` | `a-nas-shares` | 成员分享的独立副本（[ADR 0023](docs/adr/0023-isolate-member-share-copies-in-a-dedicated-service.md)） |
| `anas-family` | `a-nas-family` | 家庭中心：家人、宝宝档案与频道里的记录和录音，照片视频留在共享图库（[ADR 0029](docs/adr/0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)） |
| `anas-ai` | `a-nas-ai` | 相册的本地 AI Worker，经套接字按需启动，无网络、看不到数据卷（[ADR 0016](docs/adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)） |
| `anas-ffmpeg@` | 动态身份 | FFmpeg Worker（FFmpeg 与 libheif），经 `anas-ffmpeg.socket` 按连接启动，无网络、看不到数据卷 |
| `anas-vectors@` | `a-nas-vec-<所有者>` | 向量索引：每个所有者一个可重建的 Qdrant 实例，无网络，经 `anas-vectors-proxy@` 访问（[ADR 0028](docs/adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)） |
| `anas-search` | `a-nas-search` | 网页搜索（SearXNG），看不到数据卷（[ADR 0030](docs/adr/0030-ship-web-search-as-a-built-in-searxng-service.md)） |
| `anas-update` | root | 系统更新，由定时器触发检查、下载与安装（[ADR 0034](docs/adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)） |
| `anas-container-agent` | `anas-container` | Docker 的类型化代理（[ADR 0009](docs/adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)） |
| `caddy`、`smbd` | — | 局域网 Web 入口（[ADR 0012](docs/adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)）、启用时的互联网入口（[ADR 0032](docs/adr/0032-reach-the-web-desktop-from-the-internet-through-an-edge-relay.md)）与 SMB3 |
| `anas-kiosk@tty1` | `a-nas-kiosk` | 直连屏幕上的 Cage + Chromium |

AI 助手试用包中的单元（`anas-assistant@` 等，以成员本人的身份运行）与照片打分模块的 `anas-module-scoring` 不在主软件包里，分别随各自的软件包或安装脚本安装。

## 本地开发入口

当前推荐环境是 Windows 主机加 WSL2 Ubuntu。项目有两台 Windows 开发机和一台 macOS 开发机，配置与平台限制见[本地开发环境](docs/development/LOCAL_ENVIRONMENT.md)；以下是源码在 `E:\A-NAS`、使用专用 WSL 用户的那一台：

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

运行全部门禁：接口契约、前端类型检查、lint、测试与生产构建、格式、文档、运维脚本与工作流检查、第三方收录核对、Go vet、golangci-lint 与测试、漏洞公告、Python lint 与 AI Worker 测试，最后构建四个 Linux 命令和 Debian 软件包（Ruff 需先安装，见[静态检查](docs/development/testing.md#静态检查)）：

```bash
make check
```

构建产物写入被 Git 忽略的 `build/`，软件包在 `build/packages/`：

```text
build/anas-api
build/anas-host-agent
build/anas-container-agent
build/anas-scoring
```

修改服务单元、软件包、Host Agent、文件代理或文件权限时，部署前还要运行在 systemd 下安装整套服务的[系统测试](docs/development/testing.md#系统测试)：`make system-test`。

Go 模块路径为 `github.com/zhongwater123/A-NAS`。架构语言决策记录在 [ADR 0001](docs/adr/0001-use-go-for-product-services.md)。

启动带内嵌 Web 桌面和确定性 Fake Adapter 的本地产品服务。`ANAS_HOSTSTATE_MODE` 没有默认值：开发时写 `fake`，NAS 上由 `anas-api.service` 设为 `agent`，不设置时服务拒绝启动：

```bash
make web-build
ANAS_HOSTSTATE_MODE=fake go run ./cmd/anas-api
```

浏览器访问 `http://127.0.0.1:8080/`。服务只监听回环地址，可通过 `ANAS_HTTP_ADDR` 换成其他回环地址与端口；聚合状态接口为 `/api/v1/host-state`，完整契约见 [OpenAPI](api/openapi.yaml)。全部变量及其校验见 [`cmd/anas-api/config.go`](cmd/anas-api/config.go)。

需要由设备管理员在“设置 → 终端”中打开设备 Shell 时，显式启用终端（设备上按[运行手册](docs/runbooks/enable-admin-terminal.md)开启）；生产中 Shell 经文件代理以该管理员本人的 Linux 账号运行（本地模拟模式下以开发进程用户运行），只接受已认证管理员的回环同源连接，边界见[终端规格](docs/specs/web-terminal.md)：

```bash
ANAS_HOSTSTATE_MODE=fake ANAS_TERMINAL=enabled go run ./cmd/anas-api
```

桌面“Docker”应用在模拟模式下默认使用确定性的 Fake 容器；连接真实 Docker 需要容器代理，见 [ADR 0009](docs/adr/0009-use-docker-engine-through-a-dedicated-container-agent.md) 与[安装运行手册](docs/runbooks/install-container-agent.md)。

前端单独开发时可运行 `cd web && npm run dev`；Vite 把 `/api` 和 `/healthz` 代理到本地 Go 服务。前端技术原因见 [ADR 0003](docs/adr/0003-use-react-typescript-for-web-desktop.md)。

## 部署

A-NAS 以签名软件源中的 Debian 软件包发布（[ADR 0034](docs/adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)）：开发机跑完门禁与系统测试后用 `make release` 把软件包放进软件源，部署机把软件源复制到 NAS，设备所有者以 root 用 apt 安装、升级与回滚，用 `a-nas-check` 检查，见[部署 A-NAS 软件包](docs/runbooks/deploy-a-nas-packages.md)。破坏性磁盘计划与实机验收见[实机配置手册](docs/runbooks/provision-v1.0.1-experimental-storage.md)，其余操作见[运行手册索引](docs/runbooks/README.md)；从开发机经限定的 SSH 隧道远程浏览见 [Web 预览运行手册](docs/runbooks/deploy-web-preview-to-experimental-nas.md#远程浏览与验证)。

## 项目文档

新会话从 [AGENTS.md](AGENTS.md) 开始；它会根据任务把开发者或 Agent 路由到当前状态、领域语言、架构、规格、调查记录或运行手册。文档分类和维护规则见 [文档系统](docs/README.md)。

文档结构和仓库内链接由 Go 工具检查：

```bash
make docs-check
```

## 版权

© 2026 A-NAS 作者。保留所有权利。本仓库公开供查看，没有附开源许可证：未经著作权人书面许可，不得复制、修改、分发本仓库的代码与文档，也不得用于任何商业用途（[决定记录](docs/decisions/2026-11-17-all-rights-reserved.md)）。

自带许可证的第三方代码仍按其许可证使用，例如 [CasaOS 应用目录](internal/appstore/catalog/NOTICE.md)（Apache-2.0）、[Hermes 界面代码](web/src/assistant/hermes/LICENSE)与 [dsh-pet](web/src/companion/dsh-pet/LICENSE)（MIT）。
