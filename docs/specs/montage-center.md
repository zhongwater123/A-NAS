# 决策记录：剪辑中心首版如何接入 A-NAS

状态：proposed（待内部评审；批准后据此拆实施票）
更新时间：2026-11-17

本记录回答 [#514](https://github.com/zhongwater123/A-NAS/issues/514) 要求先交的几件事：
- 首版迁移哪些功能、哪些延后；
- 用原仓库的哪个提交；
- 主线要扩展哪些接口，数据归谁；
- 分几批交付、怎么验收；
- 哪些事还待决定。

用户行为以 #514 为准，本记录不重复，只补技术方案，以及 2026-11-17 用户对 #514 留给开发评估的几项所做的决定。

## 问题

### 需求与约束

[#514](https://github.com/zhongwater123/A-NAS/issues/514) 要求把视频剪辑原型接进 A-NAS，成为和家庭中心、影视中心并列的正式应用“剪辑中心”，以独立服务运行。

首版要在所选范围内形成真实的出片闭环。以下几条是接入边界，不随功能裁剪而取消：
- 相册是原片的事实源；
- 规划走成员自己的 VNAS；
- 复用主线已有的模型和索引；
- 持久任务可恢复。

其他约束：
- **数据外发**（[#295](https://github.com/zhongwater123/A-NAS/issues/295) 共同约束，2026-10-29 用户确认）：
  - 画面只交给本机的 FFmpeg 和本地模型；
  - 文字（用户指令、卡片描述、拍摄时间等元数据）可以发给云端大模型。
- **资源**（[#341](https://github.com/zhongwater123/A-NAS/issues/341)）：宿主机是实验 NAS，i3-12100（4 核 8 线程），8 GB 内存，只有核显。#514 不设性能门槛，但剪辑不能拖垮文件共享和前台媒体使用。

### 原型现状

原型是第八版：个人仓库 [`f48d501`](https://github.com/liangjianshu/anas-video-montage/tree/f48d501)，对应 A-NAS 分支 `feat/video-montage-v8` 的 `19f2d6db`。2026-11-17 经 [#516](https://github.com/zhongwater123/A-NAS/pull/516) 并入 `feat/video-montage-v5`，即 PR [#454](https://github.com/zhongwater123/A-NAS/pull/454) 的分支。相对 #514 的评估基线 `9103e21`，差异记在 [#515](https://github.com/zhongwater123/A-NAS/issues/515)。

它能跑通“上传 → 剪三版 → 挑一版 → 精修”，但下面这些做法不能直接进产品：
- 网页服务没有登录；
- 原片存在服务自己的目录里；
- 规划直接调用 Claude 命令行或 API；
- 窗口向量由服务自己调 EmbeddingGemma 算；
- 任务存在一张 SQLite 表里，一个线程执行。

### 主线现状

2026-11-17 核对 `origin/main`，原型接进来缺这些能力：

| 原型需要 | 主线现状 | 证据 |
|---|---|---|
| 剪辑服务后台读原片 | 相册每个请求都要成员会话（`X-A-NAS-Session`），没有服务身份；只有 `/internal/photos/lookup` 和 `/internal/photos/activity` 两个内部接口 | `internal/photoservice`、[ADR 0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md) |
| 视频 2 秒窗口向量 | 视频不生成向量、不进任何 AI；视频分段集合标为“计划中” | [相册视频](photo-video.md#非目标)、[向量索引](../architecture/vector-index.md) |
| 素材进回收站或被删除时知道 | 没有事件或变更流；`lookup` 只返回可见、未进回收站的资产，不说缺失原因 | `photosapi.LookupRequest` |
| 多帧取帧、按时间线渲染 | FFmpeg Worker 每条消息只收一个描述符，只有 probe、frame、image、subtitle、stream、audio 六种操作；`frame` 一次取一帧 | `internal/ffmpegworker/protocol.go`、`internal/unixmsg` |
| 4B 看多张关键帧写描述 | AI Worker 一次只收一张图；`read_text` 不收自定义提示词 | `ai/anas_ai/protocol.py` |
| 后台 AI 准入 | `photoservice.Gate` 在相册进程内存里，别的服务加入不了 | `internal/photoservice/gate.go` |
| 让 VNAS 规划并交回结构化结果 | 产品服务只能凭成员在线会话连 Hermes；JSON-RPC 白名单里没有 Schema 参数；MCP 工具按对端 UID 认成员 | `internal/assistantapi/rpc.go`、`ai/assistant-spike/anas_mcp.py` |
| 通用持久任务 | 没有；最接近的是相册的 `jobs` 表（租约 5 分钟、最多 3 次、指数退避） | `internal/photos/jobs.go` |

### 2026-11-17 用户对开放项的决定

| 开放项 | 决定 |
|---|---|
| 剪法 | 用第八版 `f48d501`。第六版分支上后来的改动（默认不按拍摄顺序、白天夜里不穿插、补位先从别的卡片补）不移植 |
| 配乐来源 | 用已经分析过段落的 10 首歌。音源和结构文件放在设备的数据目录里，不进仓库，不随软件包发布，只用于内部测试 |
| 规划外发 | 只发文字描述、不发画面，符合 #295 |
| 成片回存相册 | 首版不回存（偏离 #514 的首版闭环，见“风险”） |
| 不支持的要求 | 简单提示一下 |
| 后台读相册的授权 | 相册持有项目授权（见“相册项目授权”） |
| VNAS 规划的接法 | MCP 工具加后台回合（见“规划：VNAS 与 MCP 工具”） |
| 视频分析归属 | 相册只算窗口向量；画质、卡片和描述归剪辑中心 |

### 目标

成员打开剪辑中心，可以：
1. 上传一批视频，视频进本人的个人图库；也可以从相册里选视频。
2. 选一首配乐，写一句要求，点“开始”。
3. 得到一条跟着音乐剪的成片，能播放、能下载。

其间的约束：
- 关掉网页、服务重启，任务都能接着做；
- 取消立即生效；
- 素材被移进回收站时暂停，恢复后继续；素材被永久删除时明确报告缺了哪些。

### 非目标（首版）

- 成片回存相册，排在下一批。接口已按幂等回存设计好。
- 一次出三版、挑一版、快捷微调；精修（换镜头、拖动、删除、锁定重洗、预览、导出镜头清单）。
- 竖屏、指定时长、多首歌、字幕、原声。渲染固定为 1920×1080、30 帧，时长跟着音乐段落走。
- 日常、定时剪辑，VNAS 自主剪辑，扫描整个图库。
- 用户上传歌曲，在 NAS 上运行 allin1。
- 断点续传：沿用相册的 multipart 上传，单个视频最大 4 GiB。
- RK3588。

## 方案

### 变更总览

```text
浏览器（剪辑中心窗口）
  │ ① 上传视频到个人图库（现有相册上传）        ④ 在成员的 VNAS 里开一个后台回合（现有助手网关）
  │ ② 新建项目、选歌、写要求、开始 ──────────┐          │
  ▼                                          ▼          ▼
anas-api ──转发 /api/v1/montage──▶ anas-montage（Go，新服务）      Hermes（成员的 VNAS）
                                   │  项目、版本、持久任务、校验          │ ⑤ 调 montage MCP 工具
                                   │  ③ 用当次请求的会话在相册登记项目授权 ◀┘   读目录、交大纲、交镜头
                                   ├──▶ 相册：按授权查状态、拿原片只读描述符、请求窗口向量
                                   ├──▶ FFmpeg Worker：多帧取帧、逐段渲染、拼接配乐（新操作）
                                   ├──▶ AI Worker：多图卡片描述（新操作，共享 AI 准入）
                                   └──▶ 剪辑计算 Worker（Python）：卡片、画质、填槽（第八版 domain/）
```

改后的行为：
- **建项目**：浏览器先用现有接口把视频上传进个人图库，再把资产 ID 交给剪辑中心。剪辑中心在这次请求里，用转发来的成员会话请相册登记一条项目授权，之后不再保存这个会话。
- **开始**：剪辑中心建一个持久任务，依次执行：
  1. 请求窗口向量；
  2. 切卡片；
  3. 量画质；
  4. 写描述；
  5. 读歌曲；
  6. 规划；
  7. 填槽并校验；
  8. 渲染。

  每一步的结果按输入摘要缓存。
- **规划**：任务走到规划时，由浏览器在成员的 VNAS 里开一个后台回合。VNAS 调用 montage MCP 工具读卡片目录，交回两步的规划；剪辑中心按 Schema 和剪辑规则校验后才收下。
- **成片**：存在剪辑中心自己的子卷里，可以播放、下载。

几个重要的失败行为：
- VNAS 不可用时，任务停在“等待规划”，上传、浏览、播放照常。
- 素材进回收站时任务暂停，恢复后继续；素材被永久删除时报告缺了哪些，已有成片保留。
- 取消后不再调度新步骤，并终止正在进行的取帧、描述和渲染。

关键选择：
1. **服务形态**：内置独立服务，照家庭中心的做法接线，不做可移除模块（理由见“考虑过的替代方案”）。
2. **语言分工**：Go 持有权威状态；Python 只做纯计算；FFmpeg 和 4B 只经受限的类型化操作调用。符合 [ADR 0001](../adr/0001-use-go-for-product-services.md)。
3. **剪辑规则**：沿用第八版的领域层，行为迁移和缺陷修复分开提交。修复的缺陷见“原型缺陷的修复”。

### 影响范围

| 模块 | 变化 | 生产者 / 消费者 | 数据归属 |
|---|---|---|---|
| `cmd/anas-api`（新增子命令 `montage-service`）、`internal/montageservice`、`internal/montageapi`、`internal/montage` | 新服务：项目、批次、版本、任务、校验、成片、MCP 工具后端 | 浏览器经 anas-api 转发；Hermes 经 MCP | 剪辑中心：项目、版本、时间线、任务、卡片与描述缓存、成片 |
| `internal/photos`、`internal/photoservice`、`internal/photosapi` | 新增：项目授权、按授权查询状态与交出原片描述符、按项目请求的视频窗口分析（`windows/v1`） | 剪辑中心 | 相册：资产、授权、窗口向量（Catalog 与相册的 Qdrant） |
| `internal/ffmpegworker`、`internal/unixmsg` | 新增三个操作；一条消息可带最多 2 个描述符 | 剪辑中心（以后相册的窗口分析也用 `frames`） | 不持有数据 |
| `ai/anas_ai`、`internal/aiworker` | 新增 `describe_frames`（多图、固定提示词模板） | 剪辑中心 | 不持有数据 |
| `internal/photoservice/gate.go` → 新包 `internal/aiadmission` | AI 准入改成跨进程共享 | 相册、剪辑中心 | 不持有数据 |
| `ai/anas_montage`（新） | 第八版 `domain/` 的纯计算，以 Worker 形式运行 | 剪辑中心 | 不持有数据 |
| `ai/assistant-spike`（`managed-config.yaml`、新增 `anas_montage_mcp.py`） | 新增 MCP 服务器 | Hermes | 不持有数据 |
| `api/paths/montage.yaml`、`api/components/montage.yaml`、相册授权的契约 | 新接口 | 浏览器 | — |
| `web/src/montage/`、`web/src/desktop/apps.tsx` | 新窗口“剪辑中心” | 成员 | — |
| `deploy/`、`scripts/`、`Makefile`、`cmd/anas-host-agent`、`internal/hostops/linux` | 新单元、账号、子卷、会话查询、包脚本、ops-check、system-test | 安装与升级 | — |
| 文档 | 新增剪辑中心架构文档；修订相册视频规格（加入窗口分析）、[相册 AI](../architecture/photo-ai.md)、[向量索引](../architecture/vector-index.md)、[架构总览](../architecture/OVERVIEW.md)、`CONTEXT.md`；批准后写 ADR | — | — |

### 用户流程与原型功能取舍

原型的每一块拆成三类：首版做、延后、不做。

| 原型功能 | 首版 | 产品里的做法 |
|---|---|---|
| 首页（循环背景片） | 不做 | 窗口打开就是项目列表 |
| 上传本机视频（拖文件、拖文件夹） | 做 | 浏览器逐个用现有接口 `POST /api/v1/photos/libraries/{个人图库}/uploads` 上传，进本人的个人图库；全部成功后建批次和项目 |
| 从 NAS 文件夹整组、按天、单段、Shift 连选 | 做，改为从相册选 | 用相册的选择器选本人能看到的视频，明确选入项目 |
| 追加素材 | 做 | 上传完二选一：“新建项目”或“加入已有项目”。加入后由用户决定是否重新生成，已有成片保留 |
| 配乐页（曲库、试听） | 做 | 列出曲库里通过校验的歌，可以试听 |
| 剪辑要求页（一句话、顺序） | 做 | 一句话最多 200 字；“按拍摄时间”开关沿用第八版的 `order`。不支持的要求提示一下（见“不支持的要求”） |
| 处理中（步骤、关键帧、可离开） | 做，精简 | 显示当前步骤、进度、排队位置；离开再回来，从服务端恢复。“正在看的关键帧”和逐字描述动画延后 |
| 取消剪辑 | 做 | 见“持久任务” |
| 三版并排、挑一版 | 延后 | 每次生成一个版本；项目里可以保留多个版本（重新生成就多一个，旧版不覆盖） |
| 快捷微调、说一句重剪 | 延后 | — |
| 精修：换镜头、拖动、删除、锁定重洗、撤销、预览、导出镜头清单 | 延后 | 需要服务端保存编辑记录，并让修改进入导出；文字搜镜头还依赖窗口向量 |
| 确认、完成 | 改为“成片就绪” | 播放、下载；回存相册排在下一批 |
| 后台确认记录 | 不做 | — |
| 没有登录、局域网都能上传 | 必须改 | 按成员会话鉴权，两个成员互相看不到 |

### 服务形态与部署

剪辑中心照家庭中心（[ADR 0029](../adr/0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)）的方式接：

- **进程**：`/usr/lib/a-nas/anas-api montage-service`，单元 `anas-montage.service`。
  - 系统账号 `a-nas-montage`，固定 UID 31003（sysusers 里下一个空闲号），附加组 `a-nas-photos`，用来连相册、FFmpeg Worker 和 AI Worker 的套接字。
  - 沙箱：`PrivateNetwork`、`ProtectSystem=strict`、`RestrictAddressFamilies=AF_UNIX`、`ReadWritePaths=/srv/a-nas/data`，和家庭中心相同。
  - 资源：`MemoryMax=512M`、`CPUWeight=20`、`IOWeight=20`。重计算都在 Worker 里做，服务自己只管状态。
- **入口**：anas-api 用 `mount("/api/v1/montage", apiroute.Session, ...)` 把请求转到 `/run/a-nas-montage/montage.sock`，只转 `X-A-NAS-Session`。服务拿这个会话去问 Host Agent 的会话查询套接字 `/run/a-nas-montage-sessions/montage.sock`，确认是谁。
- **存储**：Host Agent 建 `montage/` 子卷，0700，属于 `a-nas-montage`，每 15 分钟修复一次漂移。数据卷离线、只读或容量不足时，服务整体回答 `montage_unavailable`，不在系统盘建替代目录。
- **计算 Worker**：`anas-montage-compute.socket` 加 `anas-montage-compute@.service`。
  - 运行环境：DynamicUser，没有网络，看不到数据卷，用设备上已有的 AI 运行环境（`/usr/lib/a-nas/ai/runtime`）运行 `python -m anas_montage.worker`。
  - 资源：`MemoryMax=1G`，`CPUWeight=20`。
- **跟着改的地方**：家庭中心那一套接线约 30 处，逐项照做。包括：
  - `config.go` 的套接字默认值；
  - `httpapi` 的路由；
  - 系统日志白名单；
  - 账号删除步骤（`accounts.ForgetStep("montage")`）；
  - 系统更新回滚时要停的服务与要恢复的数据库；
  - preinst 备份 `montage.db`；
  - postinst 重启服务、prerm 停服务；
  - `build-packages.sh` 的 wants；
  - `a-nas-check.sh` 与 `make ops-check`；
  - system-test 一节；
  - Fake 模式在进程内装配。

### 数据模型与存储

SQLite 数据库 `montage/montage.db`，使用 WAL 和 `synchronous=FULL`，迁移版本记在 `user_version`。所有表都带 `owner_user_id`，按成员隔离。

| 表 | 主要列 | 说明 |
|---|---|---|
| `batches` | id、owner、created_at、request_key | 一次主动导入，`request_key` 唯一，重复提交返回第一次的结果 |
| `projects` | id、owner、name、grant_id、song_id、ask、order、state、created_at | 一个项目绑一条相册授权 |
| `project_assets` | project_id、asset_id、content_sha256、batch_id、added_at | 素材范围固定到内容摘要（相册原图的强 ETag） |
| `versions` | id、project_id、input_key、timeline_key、film_path、film_bytes、state、created_at | 每次生成一个版本；失败的版本不覆盖旧版 |
| `jobs` | id、owner、project_id、version_id、state、step、attempts、not_before、lease_until、heartbeat_at、cancel_requested、wait_reason、error_class、error | 见“持久任务” |
| `steps` | job_id、name、input_key、state、result_ref、attempt_token、started_at、finished_at | 每步的输入摘要和结果引用 |
| `analysis` | owner、content_sha256、kind、algo_version、result_ref | 卡片、画质、描述的缓存，按成员分区，跨项目复用 |
| `plans` | version_id、step（outline / shots）、attempt_token、answer_json、accepted_at | VNAS 交回的规划，每个版本每一步只收一份 |

文件放在子卷里：
```text
montage/
  montage.db
  cache/<owner>/<sha256>/{cards.json,quality.json,frames/*.jpg,descriptions.json}
  films/<owner>/<project>/<version>.mp4
  songs/<song-id>/{audio.*,structure.json,manifest.json}     （手动放置，见“曲库”）
  tmp/                                                        （渲染中间文件，重启时清空）
```

数据量（以品杰一组为例：106 段、47 分钟、118 张卡片、432 张关键帧）：
- **窗口向量**：存在相册，不在剪辑中心。2,820 s ÷ 2 s = 1,410 个窗口，每个 768 × 4 B = 3,072 B，共 4,331,520 B ≈ 4.13 MiB。
- **关键帧**：长边 512 的 JPEG，开发机实测平均约 39 KB。432 张 × 39 KB ≈ 16.5 MiB。
- **描述**：每张卡片约 1 KB 的 JSON，118 张约 0.12 MiB。
- **成片**：2026-11-17 重剪的 5 条是 77–95 秒、65–140 MiB。最大一条 147,217,037 B，按 95.2 s 折算码率 ≈ 12.4 Mbit/s。按渲染上限算单条最多 16 Mbit/s × 150 s ÷ 8 = 300,000,000 B ≈ 286 MiB（150 s 的来源见“校验”）。

**清理**：
- 删除项目时，删掉项目记录、版本和成片，撤销相册授权，不碰相册资产。
- `analysis` 缓存在本人已经没有项目引用后保留 7 天，再删除。
- `tmp/` 在服务启动时和每个任务结束时清空。
- 删除账号时，清掉该成员的全部数据。

### 相册项目授权

剪辑中心后台读原片、请求分析时，成员可能早已离线。#514 又不允许长期保存明文会话令牌。本方案改由相册持有一条“项目授权”。思路类似 Google Drive 的 `drive.file` 逐文件授权：成员在场时登记，之后服务凭授权访问，权限仍由相册每次重新判断。

**登记**
- 剪辑中心处理“建项目”和“加入素材”请求时，用 anas-api 转发来的成员会话调用相册内部接口 `POST /internal/photos/grants`，请求体是 `{grantee:"montage", assetIds, purpose}`。
- 相册按这位成员的 Policy 检查：每个资产都是本人能看到、未进回收站的视频。通过后写入 `grants(id, owner_user_id, grantee, created_at, revoked_at)` 和 `grant_assets(grant_id, asset_id)`，返回 `grantId`。
- 会话只在这次请求里用，剪辑中心不保存。

**使用**

剪辑中心连到相册的授权套接字 `/run/a-nas-photos/grants.sock`。相册用 `SO_PEERCRED` 核对对端 UID 是 31003，再只按授权回答：

| 操作 | 回答 |
|---|---|
| `lookup(grantId, assetIds)` | 每个资产的状态：`available`、`trashed`、`gone`、`forbidden`，以及原图 ETag、时长、宽高、拍摄时间 |
| `open(grantId, assetId)` | 原片的只读描述符，经 `SCM_RIGHTS` 传出。剪辑中心把它原样转给 FFmpeg Worker，不复制文件 |
| `requestWindows(grantId, assetIds)`、`windows(grantId, assetId)` | 见“视频窗口分析” |

`forbidden` 表示成员已经看不到这个资产，比如被移出共享图库，或者账号被禁用。

**每次都重新判断**
- 相册每次都按授权的 `owner_user_id` 重新走 Policy。
- 授权已撤销、成员账号已停用、资产已不可见，就拒绝。
- 授权不会变大：后来加入的素材要再登记一次（同一个 `grantId` 追加）。

**撤销**：项目删除或账号删除时，剪辑中心调用 `revoke(grantId)`，相册标记 `revoked_at`。授权没有自动过期时间，它的生命周期跟着项目走。被攻破的剪辑服务只能读到成员授过权的资产，读不到别人的。

### 素材生命周期

相册没有事件可以订阅，剪辑中心改为在下面这些时点调用 `lookup` 检查：
- 每一步开始前；
- 规划前；
- 渲染前；
- 执行中的任务每 10 分钟一次。

| 查到的状态 | 任务 | 项目页 |
|---|---|---|
| 全部 `available` | 照常执行 | — |
| 有 `trashed` | 进入 `waiting`（原因 `asset_trashed`）。每 10 分钟再查，全部恢复后自动继续 | “有 N 个视频在回收站，恢复后继续” |
| 有 `gone` 或 `forbidden` | 进入 `needs_input` | 列出缺失的视频，由用户选“去掉这些继续”或“取消”；不靠隐藏的原片副本重剪 |

内容变化：相册里资产的内容不可变（[ADR 0006](../adr/0006-use-a-managed-photo-library.md)）。但为了不依赖这一点，每次 `open` 都核对 ETag 和 `project_assets.content_sha256`，不一致就当作 `gone` 处理。

已有成片不受素材变化影响，一直保留到用户删掉。

### 视频窗口分析（相册）

[相册视频](photo-video.md#非目标)现在把视频排除在 AI 之外。按 #514，本方案只新增“按项目请求”的分析，不改变“视频不进相册 AI 功能”这条承诺：
- 窗口向量不进相册的搜索、聚合、找相似；
- 助手仍看不到视频。

具体做法：
- **派生名** `windows/v1`。相册 `jobs` 表的主键是 `(object_id, derivation)`，同一视频只会排一个任务，所以两个项目请求同一视频只算一次。
- **窗口**：沿用视频索引器的 2s1f 布局，每 2 秒取窗口正中一帧，长边 512。帧来自 FFmpeg Worker 新增的 `frames` 操作，一个连接取多帧。
- **编码**：AI Worker 的 `embed_image`，模型和图片向量相同：EmbeddingGemma 2 740M、70 个视觉 token、L2 归一化、768 维。
- **存储**：Catalog 新表 `video_windows(object_id, index, start_ms, end_ms, flat, model)`；向量写入相册自己的 Qdrant 新集合 `video_windows_v1_<模型摘要>_<nonce>`，点 ID 按 `(object_id, index)` 生成，经现有 `vector_outbox` 同步。
- **分析身份**：`model` 字段除了模型字符串，还记下 `win=2s1f,edge=512,rot=display,codec=jpeg82,dim=768,l2`。任何一项变化都视为另一份分析，旧结果在换模型时由现有的 `requeueStale` 重排。
- **准入**：由相册的任务和 AI 准入调度，优先级与图片 AI 相同，受前台空闲 5 分钟和内存余量约束。
- **读取**：`windows(grantId, assetId)` 返回 `{state: pending|ready|failed, model, windows:[{start_ms,end_ms,flat}], vectors}`，`vectors` 是小端 float32 的二进制块。剪辑中心把它交给计算 Worker 切卡片。
- **不扫描整个图库**：只有在授权里、被请求过的视频才排任务。

### 剪辑计算 Worker（Python）

第八版的领域层只用标准库和 NumPy，没有输入输出。本方案把它原样放到 `ai/anas_montage/domain/`（来源提交 `19f2d6db`），外面加一个 Worker 入口，协议和 AI Worker 相同：4 字节长度加 JSON，二进制数据经 memfd 描述符传入。

| 操作 | 输入 | 输出 | 来自第八版 |
|---|---|---|---|
| `cards` | 窗口时间、是否纯色、向量（memfd） | 卡片和关键帧时刻 | `domain/cards.py` |
| `quality` | 每张卡片的灰度帧序列（memfd）与 blurdetect 数值 | 模糊、抖动、遮挡、失焦，以及不可用的原因 | `domain/picture.py`、`domain/quality.py` |
| `catalog` | 卡片、描述、选项 | 第一步的卡片目录文本 | `domain/catalog.py`、`domain/prompts.py` |
| `music` | 结构 JSON、响度序列、目标秒数 | 选段、音乐段落、小节槽 | `domain/music.py` |
| `fill` | 大纲、镜头、卡片、槽 | 时间线和没填满的槽 | `domain/fill.py` |

Go 侧负责取数据、调 Worker、校验结果、写缓存，Worker 不读写任何文件。

迁移按两步提交：
1. 原样搬进来，用第八版的结果文件做黄金比对：同样的输入，镜头表逐个相同；
2. 修复缺陷，每项单独提交，见“原型缺陷的修复”。

### 媒体：FFmpeg Worker 新增操作

第八版的渲染本来就是“逐段渲染，再拼接、配乐”（`adapters/ffmpeg.py` 的 `part_args`、`render`），正好适合现在一个连接、一个输入的协议。新增三个操作，参数都由 Worker 自己拼：

| 操作 | 输入 | 参数 | 输出 | 上限 |
|---|---|---|---|---|
| `frames` | 1 个原片描述符 | 时刻列表（≤ 600 个，或给起止和帧率）、长边（64–1920）、格式 `jpeg` 或 `gray` | 一串带长度前缀的帧 | 单次输出 ≤ 64 MiB；1 分钟 |
| `segment` | 1 个原片描述符 | 起止、旋转、是否放慢（≤ 2.5 倍）、是否非 16:9（模糊背景）、结尾淡出 | MPEG-TS：1920×1080、30 帧、H.264（固定预设和码率上限 16 Mbit/s），无声 | 单段 ≤ 12 s；5 分钟 |
| `assemble` | 2 个描述符：拼好的 TS、歌曲音源 | 音乐选段起点、成片秒数、淡入 0.3 s、结尾淡出 2.5 s | MP4（faststart） | 成片 ≤ 150 s、≤ 300 MB；30 分钟 |

- `segment` 输出的都是 MPEG-TS，所以剪辑中心把各段按顺序直接首尾相接写进一个文件就能拼起来，不需要 concat 解复用器读多个文件。`assemble` 只需要两个输入，因此 `unixmsg` 扩展为一条消息最多带 2 个描述符。
- blurdetect：`frames` 的 `gray` 模式附带每帧的 blurdetect 数值，替代第八版对 JPEG 单独跑 blurdetect。
- 准入：都用 `Background` 优先级，也就是最多同时 2 个，不挤占播放。

输出上限的推导：
- `frames`：600 帧 × 长边 512 的 JPEG ≈ 600 × 40 KB ≈ 23 MiB；取 64 MiB，留出灰度帧的余量。灰度帧每帧最多 256×144 = 36,864 B，600 × 36,864 B ≈ 21 MiB。
- `segment`：最长一段是“非副歌两小节”，Forever 等慢歌一小节约 3.2 s，两小节约 6.4 s；放慢 2.5 倍前的原片段不超过 6 s，`TAKE_MOST = 6.0`。上限取 12 s 留余量。
- `assemble`：16 Mbit/s × 150 s ÷ 8 = 300,000,000 B。

### AI：卡片描述与共享准入

**新操作 `describe_frames`**
- 输入：一张卡片的关键帧（3–20 张 JPEG，拼进一个 memfd，附偏移表）和固定参数（体裁、机位列表）。
- 提示词：不收调用方的提示词，模板固定在 Worker 里，版本记作 `montage-card/v1`，内容来自第八版 `domain/prompts.py` 的 `PROMPT` 与 `CAMERAS`。
- 输出：按第八版 `domain/describe.py` 的 Schema 校验（节拍、过程、场景、机位），不合格记 `invalid_result`。
- 运行：复用现有的 `llama-server`（Qwen3.5-4B Q4_K_M + mmproj），不另起一套模型。

**上下文长度：沿用第八版（2026-11-17 用户决定“按之前的来”）**
- 一张卡片的关键帧全部交给模型，不抽帧；回答上限 1,800 个 token，与第八版 `adapters/vision.py` 相同。
- 现有 OCR 的 `llama-server` 是 `-c 8192 -np 1`，描述复用它，不另开内存档位。
- 放得下的推导：
  - 一张关键帧横拍约 146 个 token，竖拍约 450 个；每次请求的文字约 969 个；
  - 2026-11-17 统计品杰、新西兰、川西、潜水（拼接）、Insta360（拼接）共 481 张卡片，关键帧最多 11 张，没有一张超过；
  - 11 张全按竖拍算：11 × 450 + 969 + 1,800 = 7,719 ≤ 8,192。
- 规则上一张卡片最多可以有 20 张关键帧（`MOST = 20`）。超出上下文时，和第八版一样当作这张卡片描述失败（`invalid_result`），卡片不进目录，不改成抽帧。

**共享 AI 准入**

把 `photoservice.Gate` 移到 `internal/aiadmission`，相册和剪辑中心都用。三个判断：
- **前台使用**：每个服务在有用户请求时，更新 `/run/a-nas-ai/foreground/<服务>` 的修改时间。准入看所有标记，最近 5 分钟内有过就不放行。
- **压力**和**内存余量**：沿用现有阈值。PSI 的 cpu、io 超过 10%、memory 超过 5% 时暂停；可用内存低于 1 GiB 加上待加载模型的内存时暂停。这两项本来就按整机算，换个进程算结果一样。
- **模型槽**：照 FFmpeg 的 flock 槽位做法，`/run/a-nas-ai/slot` 一把锁。同一时刻只有一个服务驱动 AI Worker，避免相册和剪辑中心交替加载、卸载模型。

长时间渲染产生的 CPU 压力会让准入暂停相册 AI。这是预期行为：剪辑本身也是后台工作。

### 曲库

- **来源**：2026-11-17 用户决定，用已分析过段落的 10 首歌（开发机 `~/Music/QQ音乐`，共 306 MB）。其中 9 首的副歌已由用户逐首听过确认（[第六版研究](https://github.com/zhongwater123/A-NAS/blob/feat/montage-highlight-labels/docs/research/video-montage-v6.md#副歌识别)）。
- **放置**：每首一个目录 `montage/songs/<song-id>/`，放音源、allin1 的 `structure.json` 和 `manifest.json`（歌名、歌手、音源摘要、结构模型、用途“仅内部测试”）。`song-id` 取音源字节的 SHA-256 前 16 位。由运行手册指导手动拷到设备上，不进仓库，不随软件包发布。
- **校验**：服务启动和曲库变化时逐首校验，不通过的歌不出现在配乐页，并在服务日志里写明原因。
  - 音源能被 FFmpeg Worker 的 `audio` 操作解码；
  - 结构里有节拍、重拍，有效小节 ≥ 16；
  - 识别得出副歌；
  - 按目标 90 秒能选出段落，选段在 30–150 秒之间。
- **响度**：由 `audio` 操作解码，计算 Worker 按第八版的同一算法算，结果按 `song-id` 缓存。
- **以后换授权曲库**：曲库只是一个目录约定。换曲库、加用户上传，都不改剪辑流程。

### 规划：VNAS 与 MCP 工具

**MCP 服务器**

新增 `anas_montage_mcp.py`，在 `managed-config.yaml` 里固定启用，`trust: untrusted`。它连剪辑中心的助手套接字 `/run/a-nas-montage-assistant/montage.sock`，像相册给助手开的套接字一样挂进成员的沙箱。剪辑中心用 `SO_PEERCRED` 核对对端 UID，认出是哪位成员，只回答这位成员自己的任务。

| 工具 | 输入 | 输出（outputSchema） |
|---|---|---|
| `montage_brief` | `jobId` | 第一步的任务说明（第八版两步规划的 system 和 user 原文）、卡片目录、音乐段落、回答 Schema、`attemptToken` |
| `montage_submit_outline` | `jobId`、`attemptToken`、大纲 JSON | `accepted` 或校验错误（`isError: true` 并说明原因，让模型改了再交） |
| `montage_shots_brief` | `jobId`、`attemptToken` | 第二步的任务说明、可选节拍、回答 Schema |
| `montage_submit_shots` | `jobId`、`attemptToken`、镜头 JSON | 同上 |

**触发**
1. 任务走到规划时进入 `waiting`，原因 `vnas`。
2. 剪辑中心窗口打开时，看到这个状态就用现有的助手网关（`/api/ws` 上的 `session.create`、`prompt.submit`）在成员的 VNAS 里开一个后台回合。提示词是固定模板：“为剪辑任务 `<jobId>` 规划：先调 montage_brief……”。
3. 这一轮出现在成员的助手历史里，标题是“剪辑：<项目名>”。之后成员关掉网页，回合由 Hermes 在后台继续。
4. 如果用户点“开始”时窗口一直开着，规划会紧接着开始；如果中途离开，下次打开窗口时开始。
5. 剪辑中心自己不连 Hermes，也不保存会话。

**去重与结果关联**
- `attemptToken` 在发起这一轮时生成，记进 `steps.attempt_token`。
- 每一步只收第一份通过校验的答案，后来的提交回答“已收下”，不覆盖。
- 回合结束还没交齐答案、或者 30 分钟没有提交时，这一步进入 `needs_input`（原因 `vnas_unknown`），页面显示“VNAS 没有交回规划，重新规划？”。只有用户点了，才开下一轮，换一个新的 `attemptToken`。规划过程中这一步不会自动再次调用模型。

**VNAS 不可用**：未启用、实例起不来、网关报错时，任务停在 `waiting(vnas)`，项目页写明原因。上传、浏览、播放已有成片都不受影响。

**用量**：继承 VNAS 的模型配置与用量统计。2026-11-17 用开发机直连 Claude 重剪 5 组：每版两步合计 0.25–0.43 美元，第一步输入约 4.8–18.5 千 token，第二步约 6.9–15.2 千 token，都在 VNAS 128 千的上下文以内。

**和第八版的差异**：第八版用独立的 system prompt 直接调用 Claude。改由 VNAS 代为规划后，模型前面多了 Hermes 自己的系统提示和工具调用，答案质量可能不同。验收时在固定样本上对比，见“验收标准”。

### 持久任务

剪辑中心在自己的 SQLite 里实现任务：一张 `jobs` 表加一张 `steps` 表。做法借鉴相册的 `jobs` 表、goqite 的租约，以及 DBOS 的“每步结果先落库再往下走”。不引入 River，它的 SQLite 驱动官方仍称早期；也不引入 Temporal 一类外部服务。

**状态**：`queued` → `running` →
- `done`
- `failed`
- `cancelled`
- `waiting`（原因 `resource`、`asset_trashed`、`vnas`）
- `needs_input`（原因 `asset_gone`、`vnas_unknown`、`no_usable_footage`、`music_invalid`）

**执行**
- 一个调度协程，同一时刻只执行一个任务：描述和渲染会占满 CPU，和第八版相同。
- 领取任务时写 `lease_until = now + 5 min`，执行中每 30 秒续一次。服务重启或进程崩溃后，租约过期的 `running` 任务回到 `queued`，下次从第一个没完成的步骤接着做。

**步骤与缓存键**：每一步先算 `input_key`，`steps` 或 `analysis` 里有同样的键就直接复用。

| 步骤 | `input_key` 的组成 |
|---|---|
| 窗口向量 | 不缓存，读相册（相册按 `object_id` + 分析身份复用） |
| 卡片 | 内容 SHA-256、窗口分析身份、`cards` 算法版本 |
| 画质 | 卡片键、`quality` 算法版本、`frames` 参数 |
| 描述 | 卡片键、`montage-card/v1`、模型摘要、上下文长度与回答上限 |
| 歌曲 | `song-id`、`music` 算法版本、目标秒数 |
| 第一步规划 | 卡片目录文本的摘要、要求、顺序、歌曲键、提示词版本 |
| 第二步规划 | 第一步答案的摘要、提示词版本 |
| 时间线 | 两步答案的摘要、`fill` 算法版本 |
| 渲染 | 时间线的摘要、渲染预设版本、`song-id` |

**失败分类**

| 类别 | 例子 | 处理 |
|---|---|---|
| 资源等待 | AI 准入没放行、FFmpeg 槽满 | `waiting(resource)`，不计次数，退避后重试 |
| 可重试故障 | Worker 退出、超时、OOM、I/O 错误 | 最多 3 次，退避 1、2、4 分钟，用完后 `failed` |
| 需要修改输入 | 素材缺失、没有可用画面、歌曲不合格 | `needs_input`，等用户处理 |
| VNAS 结果未知 | 回合结束没有交回规划 | `needs_input(vnas_unknown)`，见上节 |

**取消**
- 置 `cancel_requested`。排队中的立即变成 `cancelled`。执行中的：关闭正在进行的 FFmpeg、AI、计算 Worker 的连接，Worker 在对端挂断时终止子进程；之后不再领取新步骤。
- 发布成片前再检查一次：已取消就丢弃成片，不写进 `versions`。
- 规划中的回合不能由剪辑中心中断。窗口还开着时，前端顺手调一次助手网关的 `session.interrupt`。之后交来的答案一律回答“任务已取消”。

### 校验

| 对象 | 规则 | 不通过 |
|---|---|---|
| 素材范围 | 项目的素材集合非空；只用 `project_assets` 里的视频；勾选为空不会退回“整组” | 拒绝请求（400） |
| 歌曲 | 见“曲库” | 这首歌不可选 |
| 第一步答案 | 符合 Schema；段落数与音乐段落一致；引用的卡片都在本次目录里 | 用 `isError` 退回给 VNAS 改；连续 3 次不合格则 `needs_input` |
| 第二步答案 | 符合 Schema；节拍属于所引卡片 | 同上 |
| 时间线 | 每个镜头的起止有限、起点小于终点、不超出原片时长；引用属于本项目；槽位不重叠；各镜头时长之和等于音乐选段时长，误差不超过 1 帧（1/30 s）；没有空槽 | `failed`，不渲染，不报成功 |
| 成片 | 时长与时间线一致（误差 ≤ 0.1 s）；1920×1080；30 帧；有音轨 | `failed`，丢弃文件 |

### 不支持的要求

“一句话”提交前，用一张固定的词表做简单匹配，命中时在输入框下面提示一下，不拦着继续：
- 竖屏、9:16、方形；
- 几秒、几分钟；
- 两首歌、换歌；
- 字幕；
- 原声、配音。

提示的意思是：“首版固定横屏 1920×1080，时长跟着音乐，约 90 秒，单曲配乐，不加字幕、不保留原声”。规划的答案里第八版本来就有 `unmet` 字段（素材里找不到的内容），成片页原样显示。

### 接口

新增 `api/paths/montage.yaml`，挂在 `/api/v1/montage`。错误统一用 `ProductError`；创建类请求在请求体里带 `requestKey`，沿用家庭中心的去重做法；长任务用“202 加轮询”，和应用中心安装、系统更新一致。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/projects` | 本人的项目，游标分页 |
| POST | `/projects` | `{requestKey, name, assetIds}`，建批次、项目和相册授权 |
| GET、PATCH、DELETE | `/projects/{id}` | 详情（素材状态、版本）、改名和设置（歌、要求、顺序）、删除 |
| POST | `/projects/{id}/assets` | `{requestKey, assetIds}`，加入已有项目 |
| POST | `/projects/{id}/generate` | 202，返回任务 |
| GET | `/jobs/{id}` | 状态、步骤、进度、排队位置、等待原因 |
| POST | `/jobs/{id}/cancel` | 取消 |
| GET | `/songs`、`/songs/{id}/audio` | 曲库列表，试听（支持 Range） |
| GET | `/versions/{id}/film` | 成片（支持 Range；带 `?download=1` 时作为附件下载） |

### 网页

- 在 `web/src/desktop/apps.tsx` 里登记 `montage`：标题“剪辑中心”，代码按窗口懒加载，不计入首屏 JS 预算。
- 页面：项目列表 → 新建（上传或从相册选）→ 配乐 → 要求 → 进度 → 成片。
- 数据获取用 TanStack Query（#271）。任务执行中每 1.5 秒轮询，空闲时每 10 秒。
- 视频播放复用 `VideoPlayer`。
- 主线没有通用的桌面通知中心，首版只在剪辑中心窗口和启动台图标上显示进度。

### 原型缺陷的修复

#514 列出的审计问题，在第八版代码里都找到了实例。每项都单独提交，并补回归测试：

| 缺陷 | 第八版里的位置 | 修法 |
|---|---|---|
| 勾选为空时用了整组素材 | `CutRequest.options`：`videos` 为空即 `None`，`None` 表示全部 | 素材范围只来自 `project_assets`，空集合拒绝 |
| 上传冲突误判 | `service/uploads.py`：同名同大小当作已经传过 | 上传交给相册，按内容摘要识别，剪辑中心不再自己存原片 |
| 输入变了仍复用旧结果 | `pipeline.reused` 只比对描述文件名 | 用上面的 `input_key` |
| 没填满的槽只写日志 | `pipeline.cut` 把 `short` 写进日志后照常渲染 | 时间线校验把空槽当作失败 |
| Worker 卡死在执行中 | 单线程、无租约；线程异常退出后任务停在 `running` | 租约加心跳，租约过期由调度回收 |
| 取消后仍成功 | 取消只在两步之间生效 | 终止 Worker 连接，发布前再检查一次 |

### 实施与交付

按批次分别开 PR，直接进 main，不开长期 feature 分支。剪辑中心的启动台入口在出片闭环完成前保持隐藏（`launcher.visible`），所以前几批合并后用户看不到半成品。相册、FFmpeg、AI 的扩展都是新增，不改变现有行为。

| 票 | 内容 | 依赖 | 不能拆开的义务 |
|---|---|---|---|
| M0 | 本记录批准；写 ADR（剪辑中心以独立服务运行，经相册项目授权读素材）；修订相册视频规格 | — | ADR 与规格修订一起提交 |
| M1 | 服务骨架：账号、单元、子卷、会话查询、转发、空的项目接口、隐藏的窗口 | M0 | 单元、sysusers、ops-check、system-test、`a-nas-check`、回滚列表、账号删除步骤同一个 PR |
| M2 | 相册项目授权：登记、查询、交出描述符、撤销 | M0 | 授权套接字、Policy 复核、`SO_PEERCRED` 测试同一个 PR |
| M3 | 项目、批次、素材范围、追加、生命周期检查 | M1、M2 | — |
| M4 | FFmpeg Worker：`frames`、`segment`、`assemble`，以及 `unixmsg` 的双描述符 | — | 协议、命令构造、上限与超时、准入同一个 PR |
| M5 | 相册 `windows/v1`：任务、Catalog 表、Qdrant 集合、读取接口 | M2、M4 | 分析身份、失效与 outbox 同一个 PR |
| M6 | `internal/aiadmission` 与 `describe_frames` | M4 | 相册改用新准入要证明行为不变 |
| M7 | `ai/anas_montage` 计算 Worker：先原样迁移并做黄金比对，再逐项修缺陷 | — | 原样迁移和修复分开提交 |
| M8 | 持久任务、步骤缓存、校验、渲染、成片播放与下载、曲库加载 | M3、M5、M6、M7 | — |
| M9 | montage MCP 服务器、助手套接字、前端发起后台回合 | M8 | MCP 服务器、managed-config、沙箱挂载同一个 PR |
| M10 | 网页全流程，打开启动台入口；剪辑中心架构文档；`CURRENT.md` | M8、M9 | — |

- 第八版代码在 PR #454 里还没进 main。M7 从提交 `19f2d6db` 复制 `domain/`，并在 PR 描述里写明来源。
- **整体验收**归 M10：在实验 NAS 上走完下面的“首版闭环”。各票只对自己的那部分验收负责。

## 考虑过的替代方案

**做成可移除模块（[ADR 0037](../adr/0037-plug-removable-modules-in-as-separate-services.md)）。** 模块接线少，可以完整卸载。但模块看不到数据卷以外的东西，连不上相册服务和 FFmpeg Worker，没有活动记录，也不进系统日志。剪辑中心依赖这三样，所以 #514 明确不按插件交付。代价：内置接线约 30 处，以后要移除得逐处改回。

**原样运行第八版的服务。** 改动最小，但会形成另一套产品体系：Starlette 服务、自己的 SQLite 任务表、自己调 EmbeddingGemma 和 Claude。这违反 ADR 0001 的语言分工，也违反 #514 的接入边界。

**相册做全部基础分析，包括画质、卡片和描述。** 准入天然统一，剪辑中心也更薄。但卡片和描述是剪辑专用、还在迭代的规则，放进相册的话，每次调整剪法都要改相册。2026-11-17 用户选择相册只做窗口向量。

**后台任务只在成员在线时推进。** 不用改相册授权，但关掉网页就停，和“处理中可以先去忙别的”冲突。

**剪辑服务当作可信服务，以任意成员的名义读相册。** 实现最简单，但剪辑服务被攻破时能读所有人的相册，不符合 ADR 0011 的授权模型。

**在助手网关加一个“提交提示词和 Schema、拿回 JSON”的结构化任务 RPC。** 用起来像普通函数调用。但要扩展 Hermes 的白名单，还要单独解决没有会话时怎么调用。MCP 工具沿用现有的 UID 鉴权和工具体系，MCP 2025-11 版也支持工具的 `outputSchema` 与 `structuredContent`。

**一次 filter_complex 渲染整条成片。** FFmpeg 只运行一次，但需要几十个输入描述符；内存和时间随镜头数增长；失败了要整条重来。逐段渲染每次只有一个输入，单段失败只重做那一段，第八版也已经这样做。

**上传改用 tus 断点续传。** 业内处理几个 GB 的文件通常用 tus，主线的传输中心也在用。但相册的上传是 multipart，改它超出本次范围。首版接受单文件 4 GiB、断了要重传。

**首版就出三版让用户挑。** 原型是这么做的，但三版的耗时和费用都是三倍，还要处理部分失败。#514 要求默认出一条。

## 验收标准

#514 没有单独的规格。本节同时写用户验收和技术验收，每一项都写明证据层：
- **单测**：Go 或 Python 单元测试，用替身；
- **契约**：OpenAPI 契约测试；
- **系统测试**：`scripts/system-test.sh`，真实安装和 systemd，用替身模型；
- **开发机**：真实媒体和真实模型；
- **实验 NAS**：真机。

替身的证据边界：替身只能证明控制流和校验，不能证明模型质量、性能和内存。

### 首版闭环（M10，实验 NAS）

1. 成员 A 在剪辑中心上传品杰一组里的 11 段视频（约 6 分钟），选《Symphony》，点开始。
   - 视频出现在 A 的个人图库；
   - 任务依次经过各步骤；
   - 生成一条 1920×1080、30 帧、带音乐的成片，时长等于音乐选段，误差 ≤ 0.1 s；
   - 可以播放和下载。
2. 生成途中刷新页面、关闭再打开窗口，进度从服务端恢复。
3. 生成途中执行 `systemctl restart anas-montage`：任务在租约过期（≤ 5 分钟）后从未完成的步骤接着做，已经完成的描述和规划不再重做。核对方法：描述步骤的 AI Worker 调用次数和规划回合数都不增加。
4. 记录各步骤的耗时和峰值内存。#514 不设门槛，只作为以后性能评估的基线。

### 成员隔离与授权

| 触发 | 预期 | 证据 |
|---|---|---|
| 成员 B 请求 A 的项目、任务、成片 | 404，不暴露是否存在 | 单测，系统测试 |
| B 用 A 的资产 ID 建项目 | 相册拒绝登记授权，返回 422 | 单测（相册），系统测试 |
| 剪辑中心以外的 UID 连授权套接字 | 拒绝 | 单测（`SO_PEERCRED`），系统测试 |
| 授权撤销后调用 `open` | 拒绝 | 单测 |
| A 的账号停用后，任务读原片 | 任务进入 `needs_input(asset_gone)` | 单测 |
| `a-nas` 账号 `ls` 剪辑中心的存储 | 被拒绝（0700，属主 `a-nas-montage`） | 系统测试 |

### 素材范围与生命周期

| 触发 | 预期 | 证据 |
|---|---|---|
| 新建项目 | 只含本批次的资产；相册里其他视频不进入卡片目录 | 单测（目录只引用 `project_assets`），开发机 |
| 提交空的资产列表 | 400，不退回“整组” | 单测（回归） |
| 上传完选“加入已有项目” | 素材追加，已有成片不变；不会自动重新生成 | 单测 |
| 同一个 `requestKey` 提交两次 | 只建一个项目 | 单测 |
| 任务执行中把一个素材移进回收站 | 下一次检查时任务进入 `waiting(asset_trashed)`，最迟 10 分钟；恢复后自动继续 | 单测（替身相册），实验 NAS |
| 永久删除一个素材 | `needs_input(asset_gone)`，列出缺失的视频；已有成片仍可播放 | 单测，实验 NAS |
| 删除项目 | 项目、版本、成片、项目专属缓存删除，授权撤销；相册里的原片仍在 | 单测，系统测试 |

### 视频窗口分析

| 触发 | 预期 | 证据 |
|---|---|---|
| 两个项目请求同一个视频 | 只排一个 `windows/v1` 任务 | 单测（相册） |
| 47 分钟素材分析完成 | 1,410 个窗口，向量 4,331,520 B（推导见“数据模型与存储”） | 开发机 |
| 换模型或改分析身份中的任一项 | 旧结果失效并重排 | 单测 |
| 相册的搜索、聚合、找相似、助手 | 仍然不出现视频 | 单测（现有用例保持通过） |
| 没有被授权请求过的视频 | 不排分析任务 | 单测 |

### 媒体与 AI 操作

| 触发 | 预期 | 证据 |
|---|---|---|
| `frames` 请求 601 个时刻、长边 4000 | `invalid_request` | 单测 |
| `segment` 输入竖拍视频 | 输出 1920×1080，模糊背景，没有黑边 | 开发机（抽帧比对） |
| `segment` 输出按顺序首尾相接后交给 `assemble` | 成片可播放；切点处没有花屏；时长误差 ≤ 0.1 s | 开发机 |
| 请求里出现路径或 filter 字符串 | 协议上没有这些字段，`Validate` 拒绝 | 单测 |
| `describe_frames` 收到 21 张图，或不合 Schema 的回答 | 前者 `invalid_request`，后者 `invalid_result` | 单测 |
| 11 张竖拍关键帧的卡片 | 在 8192 上下文里描述成功，回答不被截断 | 开发机（取品杰中关键帧最多的卡片） |
| 相册前台有人在用（5 分钟内） | 剪辑中心的描述停在 `waiting(resource)`，不计失败次数 | 单测（准入），开发机 |
| 相册改用 `internal/aiadmission` | 相册 AI 现有的准入测试全部通过 | 单测 |

### 剪辑行为迁移

| 触发 | 预期 | 证据 |
|---|---|---|
| 用第八版的品杰、新西兰、川西结果文件跑 `fill` | 镜头表和第八版逐个相同（行为迁移这一步） | 单测（黄金比对） |
| 同一组卡片再跑 `cards` 和 `quality` | 卡片的边界和不可用原因与第八版相同 | 开发机 |
| 第一步规划改走 VNAS | 在品杰、新西兰、川西三组上答案都通过校验，成片可看；人工看片记录和第八版直连 Claude 的差异 | 开发机（人工） |

### 规划（VNAS）

| 触发 | 预期 | 证据 |
|---|---|---|
| VNAS 未启用 | 任务停在 `waiting(vnas)`，页面说明原因；上传、浏览、播放可用 | 单测，开发机 |
| 同一个 `attemptToken` 交两次 | 第二次回答“已收下”，答案不变 | 单测 |
| 不合 Schema 的答案 | `isError` 退回；连续 3 次后 `needs_input` | 单测 |
| 回合结束没有交回 | `needs_input(vnas_unknown)`；不自动发起新回合 | 单测 |
| B 的 VNAS 调用 A 的任务 | 拒绝（对端 UID 不匹配） | 单测 |

### 任务与校验

| 触发 | 预期 | 证据 |
|---|---|---|
| 计算 Worker 或 FFmpeg 实例被 kill | 这一步重试，最多 3 次；用完后 `failed`，没有永久卡在 `running` 的任务 | 单测（替身），系统测试 |
| 时间线有空槽、越界、引用外部资产 | `failed`，不渲染 | 单测（回归） |
| 渲染中取消 | FFmpeg 实例结束；不生成版本；任务 `cancelled` | 单测，开发机 |
| 已有版本 1，再生成版本 2 失败 | 版本 1 仍可播放 | 单测 |
| 数据卷只读 | 整体回答 `montage_unavailable`；系统盘上不出现剪辑数据 | 系统测试 |
| 输入摘要中任一项变化（例如换歌） | 只重做受影响的步骤及其下游 | 单测 |

### 曲库

| 触发 | 预期 | 证据 |
|---|---|---|
| 10 首歌放进 `songs/` | 通过校验的出现在配乐页 | 开发机 |
| 结构 JSON 缺少重拍、识别不出副歌、选段不在 30–150 秒 | 这首歌不可选，服务日志写明原因 | 单测 |
| 仓库与软件包 | 不含任何音源 | `make check` 的 packages 步骤加一条检查 |

### 检查命令

- 文档：`make docs-check`。
- 代码：`make check`、`make race-test`（任务调度是并发代码）、`scripts/system-test.sh`。
- 改了契约要运行 `make api`。

## 风险

| 风险 | 后果 | 应对 | 时点 |
|---|---|---|---|
| 首版不回存相册，偏离 #514 的首版闭环 | issue 验收项不满足 | 和 issue 作者对齐范围；版本表和渲染键已按幂等回存设计，下一批加一个相册写入接口即可 | 批准前 |
| [ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)：本地模型接入前，不在存真实家庭数据的设备上为成员开启助手 | 真实设备上规划可能一直不可用 | 首版在开发机和实验 NAS 上验收；产品上线时间跟着 VNAS 的模型方案走 | 批准前确认 |
| 曲库是商业歌曲 | 不能随产品发布 | 只用于内部测试，不进仓库和软件包；正式发布前换成有授权的曲库 | 发布前 |
| 4B 在 i3-12100 上用 CPU 写描述的速度未知（开发机 Metal 约 12 秒一张卡片） | 一组素材描述可能要几十分钟 | 不设门槛；实验 NAS 上记录耗时，作为性能评估的输入 | 实施中验证 |
| 8 GB 内存：4B 约 3.5 GiB，FFmpeg 切片上限 3 GiB，加上相册、Qdrant、服务本身 | 描述和渲染同时进行时会有压力 | 剪辑中心一次只执行一个任务；描述和渲染不并行；AI 准入保留内存余量 | 实施中验证 |
| 素材里出现关键帧超过 11 张的竖拍长卡片 | 超出 8192 上下文，这张卡片描述失败、不进目录 | 和第八版行为相同；实验 NAS 上统计失败的卡片数，多了再评估加大上下文 | 实施中验证 |
| Hermes 后台回合在关闭网页后能否继续、能否稳定调用 MCP 工具 | 规划不可靠 | M9 先做探针；不行就退到“规划时窗口要开着”，在页面上提示 | 实施中验证 |
| 改由 VNAS 规划后答案质量下降 | 成片变差 | 固定样本人工对比；Hermes 的系统提示无法去掉时，把关键规则放进工具返回的任务说明 | 实施中验证 |
| [FFmpeg 槽位与内存的决定](../decisions/2026-10-21-ffmpeg-worker-slots-and-memory.md)仍是 pending | 渲染并发和内存上限可能再变 | 剪辑中心只用 `Background`，跟着最终数值走 | 不处理 |
| 相册视频规格加入窗口分析 | 改变“视频不做内容分析”的规格表述 | M0 修订规格，限定“按项目请求、只供剪辑中心”；相册的 AI 功能对视频不变 | 批准前 |
| 第八版代码在 PR #454，还没进 main | 来源不稳定 | 固定从 `19f2d6db` 复制，PR 描述写明来源；#454 以后合并与否不影响 | 不处理 |
| 单文件 4 GiB、不能断点续传 | 大文件上传失败要重传 | 页面逐个文件重试；续传另开 issue | 不处理 |

## 关联

- 需求：[#514](https://github.com/zhongwater123/A-NAS/issues/514)；原型差异 [#515](https://github.com/zhongwater123/A-NAS/issues/515)；路线与外发约束 [#295](https://github.com/zhongwater123/A-NAS/issues/295)；资源条件 [#341](https://github.com/zhongwater123/A-NAS/issues/341)；原型代码 PR [#454](https://github.com/zhongwater123/A-NAS/pull/454)
- 原型：[第八版说明](https://github.com/zhongwater123/A-NAS/blob/19f2d6db/ai/montage_v8/README.md)、[分层重构方案](https://github.com/zhongwater123/A-NAS/blob/19f2d6db/docs/specs/video-montage-architecture.md)、[第七版网页原型](https://github.com/zhongwater123/A-NAS/blob/19f2d6db/docs/research/video-montage-v7.md)
- ADR：[0001](../adr/0001-use-go-for-product-services.md)、[0006](../adr/0006-use-a-managed-photo-library.md)、[0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)、[0017](../adr/0017-run-the-media-center-through-the-file-broker.md)、[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)、[0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)、[0029](../adr/0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)、[0037](../adr/0037-plug-removable-modules-in-as-separate-services.md)
- 架构：[总览](../architecture/OVERVIEW.md)、[家庭中心](../architecture/family-center.md)、[相册 AI](../architecture/photo-ai.md)、[向量索引](../architecture/vector-index.md)
- 规格：[相册视频](photo-video.md)、[家庭中心](family-center.md)、[AI 助手](ai-assistant.md)
- 代码：`internal/familyservice/`、`internal/photos/jobs.go`、`internal/photoservice/gate.go`、`internal/ffmpegworker/`、`internal/aiworker/`、`internal/assistantapi/`、`ai/assistant-spike/anas_mcp.py`
