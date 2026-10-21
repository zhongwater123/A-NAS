# 架构总览

本页记录开发时必须保持的系统边界和当前代码入口。完整产品背景与研究依据见 [PROJECT_CONTEXT.md](../../PROJECT_CONTEXT.md)。

## 系统边界

```text
NAS 本地控制台（Cage + Chromium）或隧道后的远程浏览器      局域网浏览器
  │ 回环地址上的同源 HTTP / REST                          │ 明文 HTTP，TCP 80
  │                                                       ▼
  │                                       Caddy 局域网 Web 入口（可选；附加 X-Forwarded-For）
  │                                                       │ 回环
  ▼                                                       ▼
产品服务（非特权，只监听 127.0.0.1:8080）
  ├── Auth / Files / Storage / Share / Jobs / Policy
  ├── Transfers（/api/v1/uploads 的 tus 分片写入文件代理以用户身份创建的临时文件；下载按发送字节跟踪；ADR 0019）
  ├── /api/v1/media ── 影视中心：经文件代理以用户身份读取 ──► 媒体 Worker（按连接启动，无网络、无数据卷）
  ├── Music（原地索引个人空间与 Shared 中的音频，经 Files 以成员身份读取；浏览器不能播放的格式经同一媒体 Worker 探测与解码；ADR 0018）
  ├── /api/v1/assistant ── 按白名单转发 ──► 成员自己的 Hermes 实例（hermes serve，沙箱；连接经 File Broker 的会话闸门；ADR 0024）
  ├── /api/v1/voice ── 语音处理：经 Files 以成员身份读取录音 ──► 媒体 Worker 转 16 kHz ──► TOS 中转 ──► 豆包语音（云端转写）
  │                    声纹 ──► 声纹 Worker（套接字激活，无网络、无数据卷）；翻译与总结 ──► VNAS 的模型网关（ADR 0027）
  ├── /api/v1/photos 转发 ──► 相册服务（a-nas-photos，独占数据卷 photos 子卷）
  │                              ├── Photo Library / Catalog / Search / Policy
  │                              ├── 助手套接字 ◄── 成员的 AI 助手（对端 UID 即成员，只读；ADR 0024）
  │                              └── AI Orchestrator ──► AI Worker（M2 规划；只读 fd，无网络）
  └── hoststate.Reader
          ├── Fake Adapter（当前本地开发与契约测试）
          └── Host Agent Client Adapter
                    │ 窄而类型化的 IPC
                    ▼
                Host Agent（root 系统服务、受限 UDS）
                    ├── Linux 状态 Adapter
                    ├── 类型化身份、ACL、卷、Samba 与 Btrfs 快照操作
                    ├── 白名单服务日志查询（固定参数的 journalctl，只读；ADR 0020）
                    ├── File Broker ──► 按用户运行的文件 Worker（登录者 UID）
                    │        ├── 助手连接：核对会话后以 root 连接本人的实例套接字并交回，会话失效即切断（ADR 0024）
                    │        └── 助手更改的执行方：凭会话生成卡片与登记授权；助手套接字上按对端 UID 执行，Landlock 限制写入位置
                    └── 会话与账号查询（供相册和分享服务确认身份，或助手套接字对端 UID 的账号）

Web（File Broker 的用户 Worker）/ SMB3（smbd 以登录者身份）→ 内核 POSIX ACL → 个人空间或 Shared → Btrfs 数据卷
                         ├── 隐藏回收站
                         └── 不通过 SMB 暴露的只读快照

产品服务 ── containers.Manager ──┬── Fake Adapter
                                 └── 容器代理客户端
                                         │ 类型化 UDS（anas-container 组）
                                         ▼
                                 容器代理（anas-container，docker 组）
                                         ├── Docker Engine（docker.sock）
                                         └── docker compose（仅运行按安装策略渲染、经摘要确认的应用）

照片导入 → 受管对象存储 + Catalog → 派生任务 → AI Provider → Search Index → Policy 过滤 → 照片资产
```

## 不变量

1. AI 服务停止时，文件共享、权限、快照、备份和恢复仍然工作。
2. 产品服务以非 root 身份运行；特权操作只进入 Host Agent。
3. Host Agent 接收经过校验的高层意图，不提供任意 Shell 执行接口；唯一例外是管理员的 Web 终端，Shell 在该管理员本人的 Linux 账号（UID 20100–29999）下运行，从不以 root 运行；容器代理同样只提供类型化容器操作，不透传 Docker Engine API，产品服务用户永不加入 `docker` 组。
4. 磁盘和文件使用稳定资源 ID；临时路径和 `/dev/sdX` 不是身份。
5. 修改宿主机状态的操作遵循“规划、确认、执行”三阶段。
6. 个人空间和共享空间的文件数据由 POSIX ACL 授权（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)），Web 与 SMB 都以登录者本人身份访问；照片资产由相册 Catalog Policy 授权；独立分享副本由分享服务按参与者、有效期与任务授权（[ADR 0023](../adr/0023-isolate-member-share-copies-in-a-dedicated-service.md)）。搜索与 AI 检索在返回前按同一 Policy 过滤。
7. AI 派生数据可重建、可清除，原文件只读进入 AI Pipeline。
8. Fake 与 Linux Adapter 遵循同一契约，差异由契约测试暴露。
9. 本地控制台、隧道与局域网浏览器使用同一 Web UI 和产品 API；产品服务只监听回环，局域网只经 Caddy 入口进入并带转发头（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)）。Kiosk 只多一个不过期的本机会话，不获得额外宿主机权限。会话只在登录时的客户端上有效，桌面页面不允许其他来源嵌入：已安装应用与桌面同站，会收到会话 Cookie（ADR 0012 决定 7）。
10. 受管图库以稳定照片资产 ID 表达用户可见身份；内容对象路径和内容哈希不成为公共文件身份。
11. AI Worker 不能写原图、权限或用户元数据；只有 Photo Library Module 可以提交权威目录状态。
12. 数据卷离线、只读或低于保留容量时拒绝写入；不得在系统盘创建替代空间。
13. Web 与 SMB 共用同一个 Linux 账号与 ACL，但密码凭据分别以不可逆格式保存，明文只存在于创建或重置调用期间。
14. 受管图库存储只由相册服务身份访问，照片资产授权由相册 Policy 判定；个人空间与共享文件夹仍只由 ACL 授权，相册服务不获得其访问权（[ADR 0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)）。
15. 应用的 Docker 网络只从现场确认空闲的地址池分配，应用不能改变宿主机到局域网客户端的路由；地址池未配置时拒绝安装会新建网络的应用，安装后不在池内即回滚（[ADR 0013](../adr/0013-allocate-docker-networks-from-an-a-nas-address-pool.md)）。应用挂载 A-NAS 卷时只得到计划中的子目录：容器代理在规划时检查 Compose 与 Engine 的最低版本，安装后核对挂载（[ADR 0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)）。
16. 影视中心不持有存储：媒体文件只经文件代理以有效会话的用户身份读取，媒体库的可见性与所在空间的读权限一致；FFmpeg 只在没有网络、看不到数据卷与产品服务状态的按连接 Worker 中运行，只接收只读描述符（[ADR 0017](../adr/0017-run-the-media-center-through-the-file-broker.md)）。
17. 应用容器只以应用身份接触共享空间：服务以 `app-<id>` 启动，或由入口程序从 root 降到 `app-<id>`，并通过写在自身 Compose 中的 `a-nas-users` 组访问共享空间，挂载默认只读；保留镜像用户（可能是 root）的服务只能挂载自己的应用数据。宿主机账号的组成员关系不进入容器，容器代理安装后核对用户、组、能力与只读挂载（[ADR 0022](../adr/0022-reach-shared-folders-only-as-the-app-identity.md)）。

## 当前代码入口

| 路径 | 责任 | 实现状态 |
|---|---|---|
| `cmd/anas-api` | 非特权产品 API 进程入口 | 持久化账号、存储、文件、回收站、手动与定时快照、可选终端与容器管理已接线 |
| `cmd/anas-host-agent` | root Host Agent 进程入口 | 通过组限制 UDS 提供状态、类型化特权操作、File Broker 与相册会话查询 |
| `web` / `internal/webui` | React Web 桌面、嵌入式静态资源和本地控制台外部媒体 Handler | 登录、文件、相册、回收站、[备份中心](../specs/backup-center.md)（快照、找回与定时快照）、[设置](../specs/settings.md)（我的账号、概览、存储、[个性化](../specs/personalization.md)、用户与权限、[日志](../specs/logs.md)、[终端](../specs/web-terminal.md)）、Docker 窗口、随机单视频循环屏保与屏保后息屏已实现 |
| `internal/containers` / `cmd/anas-container-agent` | 容器领域模型、Fake 与 Docker Adapter、容器代理及其 UDS 协议 | 列表、启停、日志已实现，见[容器管理规格](../specs/container-management.md) |
| `internal/appstore` / `internal/appstoreapi` | 内置 CasaOS 清单、安装策略与计划渲染（含每个服务的运行方式，[ADR 0022](../adr/0022-reach-shared-folders-only-as-the-app-identity.md)）、Compose 执行任务与安装后核对、`/api/v1/apps`（仅管理员）与 `/api/v1/app-shortcuts`（所有已登录账号的桌面快捷方式） | 已实现，见[应用中心规格](../specs/app-center.md) |
| `internal/containersapi` / `internal/localorigin` | `/api/v1/containers` 与写操作的本机 Host、同源校验 | 已实现；Host 接受 `localhost` 与 IP 地址 |
| `internal/assistantchanges` | AI 助手“提交更改”的执行方：检查提交、为卡片复制暂存内容、登记本人的一次性授权、以本人身份经文件代理的受限工作进程执行（[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 决定 4） | 单元测试与系统测试；随助手默认关闭 |
| `internal/assistantapi` | AI 助手的产品接口：经会话闸门按白名单转发成员实例上 `hermes serve` 的 JSON-RPC 与对话 REST，把 Hermes 的审批对应到“提交更改”并生成确认卡片（[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)） | 开发机原型与系统测试；默认关闭（`ANAS_ASSISTANT_SOCKET_DIR`），见 [AI 助手规格](../specs/ai-assistant.md) |
| `internal/terminal` | 经本机屏幕或局域网入口的同源 WebSocket 上的 PTY 终端；生产中由文件代理以登录管理员本人身份启动 Shell | 仅管理员可访问；默认关闭，由 root 写入 `ANAS_TERMINAL=enabled` 开启且升级保留，入口在“设置 → 终端”，见[终端规格](../specs/web-terminal.md) |
| `deploy/caddy` | 局域网 Web 入口的 Caddy 配置；系统服务安装器在已安装 Caddy 时校验并写入 | 已实现，系统测试覆盖，见[局域网 Web 访问规格](../specs/lan-web-access.md) |
| `deploy/systemd/system` / `deploy/pam` / `deploy/config` | Host Agent、产品服务、相册服务与容器代理的系统服务单元，以及直连屏幕的非特权 Cage/Chromium 会话与设备配置 | Kiosk 显示和鼠标已验收；浏览器约束与 VT 恢复待处理 |
| `internal/hoststate/agent` | Unix Socket 上的 Host Agent server/client Adapter | 状态、指标、卷、凭据与快照 IPC 已实现 |
| `internal/hoststate` / `internal/taskmanager` | 只读宿主机状态、CPU/内存/网络指标与设备、服务、进程采样；Fake 与 Debian Linux Adapter | 状态和状态栏指标见[状态栏规格](../specs/host-metrics-status-bar.md)；[任务管理器](../specs/task-manager.md)经同一 Host Agent IPC 向所有已登录账号提供只读观测，不返回命令参数或环境变量 |
| `internal/accounts` / `internal/files` / `internal/storage` | 账号、会话与空间可见性（含[昵称与头像](../specs/account-profile.md)，头像由 `internal/avatar` 压缩为 256 px JPEG 后存入账号数据库），文件闭环和持久化执行计划；文件授权由 ACL 执行 | 已实现，单元测试与系统测试覆盖 |
| `internal/filebroker` | Host Agent 内的文件代理：自行校验会话，以用户本人身份执行 Web 文件操作并启动终端 Shell（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）；也是 AI 助手的会话闸门，只把本人的实例连接交给有效会话，并执行助手的更改（`internal/assistantchanges`，Landlock 限制的工作进程；[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 决定 4 与 10，默认关闭） | 已实现，root 集成测试与[系统测试](../development/LOCAL_ENVIRONMENT.md#系统测试)覆盖；要求 Host Agent 保留 `CAP_SETUID`，见[存储与文件架构](storage-and-files.md#文件代理) |
| `internal/hostops/linux` | 固定命令的卷、Linux 身份、空间 ACL、管理员查看授权、Samba 和 Btrfs 快照执行器 | 已实现，Fake command 与 root 集成测试覆盖 |
| internal/shares / internal/sharesapi / internal/shareservice / internal/sharefiles | 成员分享：固定副本、逐人私聊、通知和可撤回保存；独立身份持有副本，文件桥仍按成员身份读取和写入 | 开发分支已实现，见[成员分享技术设计](member-sharing.md) |
| internal/photos | 相册 Module：数据卷上的 Catalog、内容寻址原图、Policy、虚拟目录、回收站与崩溃对账（[ADR 0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)） | JPEG/PNG 库、EXIF 元数据、缩略图持久任务与测试完成；开发模式由产品服务进程内运行，生产由相册服务进程运行 |
| `internal/photoservice` / `internal/sessionlookup` | 以 `a-nas-photos` 运行的相册服务进程（`anas-api photo-service`），以及它经 Host Agent 确认会话或助手 UID 的查询接口 | 已实现，root 集成测试、相册冒烟与系统测试覆盖 |
| `internal/music` | 音乐库：经 `files.Service` 以成员身份扫描个人空间与 Shared 的音频、读取标签/封面/时长与歌词，保存歌曲索引、封面缩略图和每名成员的收藏、播放记录、歌单与音乐文件夹（[ADR 0018](../adr/0018-index-music-in-place-through-the-file-broker.md)） | 已实现，由 `internal/httpapi` 提供 `/api/v1/music`，见[音乐规格](../specs/music.md)；尚未部署 |
| `internal/photosapi` | `/api/v1/photos`：只接受上游确认的 `photos.Principal`，不自行认证 | 已实现并通过 OpenAPI 契约测试；生产中由产品服务转发到相册服务，相册服务不可用时返回 `photos_unavailable` |
| `internal/media` / `internal/mediaapi` | 影视中心：媒体库、按文件名与 NFO 识别的目录、每个用户的进度、收藏与合集，以及 `/api/v1/media` | 已实现，单元测试、OpenAPI 契约测试与系统测试覆盖，见[影视中心技术设计](media-center.md) |
| `internal/filepreview` | 普通文件的视频抽帧与 HEIC/HEIF 预览；每次以用户身份打开文件后使用隔离 Worker，带有界内存与系统盘派生缓存，命中与 HTTP 304 前仍验证源文件访问权 | 见[基础存储与共享](../specs/basic-storage-and-sharing.md) |
| `internal/mediaworker` | 媒体 Worker：与应用无关的类型化协议（探测、截图、HEIF 图片、字幕、视频流、音频解码）、FFmpeg 与 libheif 命令、按连接服务端（`anas-api media-worker`）与客户端，只使用自己的类型与错误；影视中心经 `media.WorkerProcessor`、音乐经 `music.Options.Worker`、相册经 `photos.Options.Media` 使用它；开发模式在进程内运行同一服务端 | 已实现，含真实 FFmpeg 测试（安装时运行） |
| `internal/transfers` / `internal/transfersapi` | 传输中心：每名账号的上传与下载记录（`transfers.db`），tus 分片上传（tusd `pkg/handler` 与写入 `files.PendingUpload` 的数据存储，[ADR 0019](../adr/0019-resume-uploads-over-tus-with-broker-held-files.md)）、按发送字节跟踪的下载与 ZIP 打包，以及 `/api/v1/transfers`、`/api/v1/uploads` | 已实现，单元测试、真实 tus 请求的 API 测试与 OpenAPI 契约测试覆盖，见[传输中心规格](../specs/transfer-center.md)；尚未部署 |
| `internal/voice` / `internal/voiceapi` / `internal/voiceworker` | 语音处理：`voice.db` 中每个成员的录音、转写、发言人与声纹、人、译文与总结；经 `files.Service` 以成员身份读取录音，录音经 TOS 存储桶中转、由豆包语音标准版 2.0 转写，声纹 Worker（`ai/anas_ai/voice.py`，sherpa-onnx）计算声纹，VNAS 的模型网关翻译与总结；`/api/v1/voice`（[ADR 0027](../adr/0027-transcribe-in-the-cloud-and-keep-voiceprints-on-the-device.md)） | 已实现，单元测试、Go 与 Python 的协议测试、OpenAPI 契约测试覆盖，见[语音处理技术设计](voice.md)；尚未合并 |
| `internal/feedback` / `internal/feedbackapi` | 问题反馈：系统盘上 `feedback.db` 中全员共享的清单、评论与内嵌图片，以及 `/api/v1/feedback` | 已实现，单元测试与 OpenAPI 契约测试覆盖，见[问题反馈规格](../specs/feedback.md) |
| `internal/personalization` | “设置 → 个性化”的设备级设置：系统盘上 `personalization.db` 中全体账号共用的桌面壁纸（图片经转正、缩到 3840 px 并重新编码为 JPEG）与样式、本机屏幕的息屏时长，以及 `/api/v1/wallpaper`（已登录读取）与 `/api/v1/local-display`（读取无需登录）；修改仅管理员 | 已实现，单元测试与 OpenAPI 契约测试覆盖，见[个性化规格](../specs/personalization.md)；尚未合并 |
| `internal/activity` | 各模块 `audit_events` 表的统一查询：同一筛选与毫秒时间加记录 ID 的游标，合并为一页活动记录；成员只看自己的记录，管理员看不到他人个人空间中的名称 | 已实现，由 `internal/httpapi` 提供 `/api/v1/activity`，相册记录经相册 API 读取，见[日志规格](../specs/logs.md) |
| `internal/systemlog` / `internal/systemlog/journal` | A-NAS 服务日志：服务白名单、查询与 JSON 日志行解析、开发用 Fake，以及 Host Agent 中以固定参数运行 `journalctl` 的读取器（[ADR 0020](../adr/0020-read-service-logs-through-the-host-agent.md)） | 已实现，Host Agent `/v1/system-logs` 与产品 API `/api/v1/system-logs` 仅管理员可用；系统测试覆盖真实单元 |
| `internal/httpapi` | REST/JSON 路由与 DTO 映射 | 已实现并通过 OpenAPI 契约测试 |
| `api/openapi.yaml` | 客户端产品接口契约 | OpenAPI 3.1 |
| `tools/doccheck` | 文档结构和链接检查 | 开发工具 |
| `docs/specs` | 可验收的产品行为 | 见[规格索引](../specs/README.md) |
| `docs/investigations` | 复杂缺陷的证据和根因 | 见[调查索引](../investigations/README.md) |
| `docs/runbooks` | 可重复且可验证的操作 | 见[手册索引](../runbooks/README.md) |

新增模块时，应在其代码附近放置包级说明和测试；只有跨模块关系或系统不变量才更新本页。部署与实机验收进度只记录在[当前状态](../status/CURRENT.md)。

## 关联

- [成员分享技术设计](member-sharing.md)（开发分支已实现，尚未部署）
- [语音处理技术设计](voice.md)（尚未合并）与 [ADR 0027](../adr/0027-transcribe-in-the-cloud-and-keep-voiceprints-on-the-device.md)

- [领域语言](../../CONTEXT.md)
- [当前状态](../status/CURRENT.md)
- [ADR 索引](../adr/README.md)
- [Go 语言决策](../adr/0001-use-go-for-product-services.md)
- [REST/OpenAPI 决策](../adr/0002-use-rest-openapi-for-product-clients.md)
- [Web 桌面技术决策](../adr/0003-use-react-typescript-for-web-desktop.md)
- [Host Agent IPC 决策](../adr/0004-use-http-json-over-unix-socket-for-host-state.md)
- [本地控制台决策](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- [受管图库决策](../adr/0006-use-a-managed-photo-library.md)
- [相册服务身份与 Catalog 授权决策](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)
- [相册技术设计](photo-library.md)
- [相册本地 AI（M2）实施方案](photo-ai.md)
- [相册物体检测与混合检索实施方案](photo-objects.md)
- [相册人物与宠物实施方案](photo-people.md)
- [存储与文件架构](storage-and-files.md)
- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [基础存储 ADR](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- [相册与本地智能检索规格](../specs/photo-library.md)
- [影视中心规格](../specs/media-center.md)、[影视中心技术设计](media-center.md)与 [ADR 0017](../adr/0017-run-the-media-center-through-the-file-broker.md)
- [问题反馈规格](../specs/feedback.md)
- [传输中心规格](../specs/transfer-center.md)与[可续传上传决策](../adr/0019-resume-uploads-over-tus-with-broker-held-files.md)
- [日志规格](../specs/logs.md)与[服务日志读取决策](../adr/0020-read-service-logs-through-the-host-agent.md)
- [容器运行时与容器代理决策](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)
- [应用清单与安装策略决策](../adr/0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)
- [Docker 网络地址池决策](../adr/0013-allocate-docker-networks-from-an-a-nas-address-pool.md)
- [Docker 与 Caddy 软件源决策](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)
- [只读宿主机状态规格](../specs/read-only-host-state.md)
- [音乐规格](../specs/music.md)
- [音乐原地索引决策](../adr/0018-index-music-in-place-through-the-file-broker.md)
- [Web 桌面终端规格](../specs/web-terminal.md)
- [局域网 Web 入口决策](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)
- [局域网 Web 访问规格](../specs/lan-web-access.md)
- [状态栏与实时指标规格](../specs/host-metrics-status-bar.md)
