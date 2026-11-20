# 剪辑中心首版实施方案

状态：accepted（2026-11-18 用户批准，同时接受 [ADR 0038](../adr/0038-keep-montage-media-in-the-photo-library.md) 与默认规则；2026-11-17 起草；2026-11-18 按 #514 作者的评审改了配乐、探针与 M9 的先后，又按用户决定改用 allin1 分析配乐，商业版权待议，按 P1 的结果改了“规划”；2026-11-19 按 [ADR 0039](../adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md) 加上出片后的二次编辑；实施票是 [#514](https://github.com/zhongwater123/A-NAS/issues/514) 的子 issue，见“交付”）
更新时间：2026-11-19

相关文档：
- 产品行为与验收以[剪辑中心规格](../specs/montage-center.md)为准；
- 服务身份、项目授权与数据归属见 [ADR 0038](../adr/0038-keep-montage-media-in-the-photo-library.md)；出片后的二次编辑见 [ADR 0039](../adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md)；
- 用户定下的事见[决定记录](../decisions/2026-11-17-montage-center-first-version.md)，本方案补充的默认做法见[另一份决定记录](../decisions/2026-11-17-montage-center-default-rules.md)；
- 需求来源 [#514](https://github.com/zhongwater123/A-NAS/issues/514)。

## 现状

### 原型

原型是第八版：个人仓库 [`f48d501`](https://github.com/liangjianshu/anas-video-montage/tree/f48d501)，对应 A-NAS 分支 `feat/video-montage-v8` 的 `19f2d6db`。2026-11-17 经 [#516](https://github.com/zhongwater123/A-NAS/pull/516) 并入 PR [#454](https://github.com/zhongwater123/A-NAS/pull/454) 的分支；相对 #514 的评估基线 `9103e21` 的差异见 [#515](https://github.com/zhongwater123/A-NAS/issues/515)。

它能跑通“上传 → 剪三版 → 挑一版 → 精修”，但以下做法不能直接进产品：
- 网页服务没有登录；
- 原片存在服务自己的目录里；
- 规划直接调用 Claude 命令行或 API；
- 窗口向量由服务自己调 EmbeddingGemma 计算；
- 任务存在一张 SQLite 表里，由一个线程执行。

### 主线缺的能力

2026-11-17 核对 `origin/main`：

| 原型需要 | 主线现状 | 证据 |
|---|---|---|
| 剪辑服务在成员离线时读原片、存回成片 | 相册的接口每个请求都要成员会话，内部的 `lookup`、`activity` 也一样；不带会话的只有账号删除，以及按对端 UID 认成员的助手套接字（助手身份看不到视频、不能导入、不能打开原片） | `internal/photoservice/photoservice.go` 的 `Authenticate`、`internal/photoservice/assistant.go`、`internal/photos/policy.go`、[ADR 0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md) |
| 视频的窗口向量、卡片、画质与描述 | 视频不生成向量、不进任何 AI；排队、检索与助手都排除视频；视频分段集合标为计划中 | `internal/photos/video.go` 的 `inLocalAI`，[相册视频](../specs/photo-video.md#非目标)，[向量索引](../architecture/vector-index.md#将要接入的集合) |
| 素材进回收站或被删除时知道 | 没有事件；`lookup` 只返回可见、未进回收站的资产，不说缺失的原因 | `internal/photos/lookup.go` |
| 多帧取帧、按时间线渲染 | FFmpeg Worker 每条消息只收一个描述符，只有 probe、frame、image、subtitle、stream、audio 六种操作；正文写到连接关闭为止 | `internal/ffmpegworker/protocol.go`、`internal/unixmsg` |
| 4B 看多张关键帧写描述 | AI Worker 每条消息最多一个描述符，一次只看一张图；`read_text` 的提示词固定 | `ai/anas_ai/protocol.py` |
| 让 VNAS 在成员离线时规划并交回结构化结果 | 产品服务只能凭成员在线的会话连 Hermes；JSON-RPC 白名单里没有 Schema 参数；MCP 工具的后端按对端 UID 认成员。成员离线时能跑的只有定时任务：唤醒服务按成员自己建的任务启动定时任务单元，“立即运行”经文件代理核对会话后交给它 | `internal/assistantapi/rpc.go`、`internal/photoservice/assistant.go`、`ai/assistant/cron-waker.py`、`internal/filebroker/rootbroker/assistant_run.go` |
| 通用持久任务 | 没有；最接近的是相册的 `jobs` 表（租约 5 分钟、最多 3 次、退避 1 与 2 分钟，启动时收回中断的任务） | `internal/photos/jobs.go` |

## 首版范围

#514 把首版迁移多少功能留给开发评估。下面是评估结论：首版只做“导入 → 确认 → 生成 → 存回相册 → 播放与下载”这一条闭环，其余功能延后。

| 原型功能 | 首版 | 理由 |
|---|---|---|
| 上传本机视频、从相册选视频、追加素材 | 做 | #514 的导入与素材范围要求 |
| 配乐页（从音乐库选或上传一首、试听选段并拖动起点）、一句话要求、按拍摄时间 | 做 | 生成一条成片的最少输入。配乐来源 2026-11-18 由评审人改定；分析用 allin1，商业版权待议（见“配乐”） |
| 处理中（步骤、进度、可离开） | 做，精简 | 进度从服务端恢复；“正在看的关键帧”与逐字描述动画只是展示，延后 |
| 取消 | 做 | #514 的可靠性要求 |
| 首页循环背景片 | 不做 | 窗口打开就是项目列表 |
| 三版并排、快捷微调 | 不做 | 耗时与模型费用都是三倍，还要处理部分失败；#514 要求默认出一条。2026-11-18 起出两个方案 |
| 挑一版、说一句重剪；精修：换镜头、拖动、删除、锁定重洗、撤销、预览、导出镜头清单 | 首版闭环之后（[#596](https://github.com/zhongwater123/A-NAS/issues/596)） | 要在服务端保存编辑记录并让修改进入导出（#514），文字搜镜头还要窗口向量的检索；工作量与闭环本身相当。做法见“出片后的二次编辑”与 ADR 0039 |
| 竖屏、指定时长、多首歌、字幕、原声 | 延后 | 第八版的渲染固定 1920×1080、30 帧、单曲配乐，时长由音乐段落决定；做这些要改选段、填槽与渲染。首版只提示，不假装支持（#514） |
| 在 NAS 上运行 allin1 | 做 | 2026-11-18 用户决定（[决定记录](../decisions/2026-11-17-montage-center-first-version.md)第 2 项）。它给出的结构与第八版用的相同，`music` 可以原样迁移。商业版权待议；不能随产品分发时退回配乐探针测过的 beat_this 与色度自相似（见“配乐”） |
| 拼接接连拍的原片（第八版 `tools/join.py`） | 延后 | 原型里是离线手动步骤，产品里要自动判断哪些原片能拼 |
| 后台确认记录 | 不做 | 只是原型的调试页 |
| RK3588 | 延后 | #514 不规定验收顺序；首版在 x86 的实验 NAS 上验收 |

## 整体结构

```text
浏览器
  剪辑中心窗口：① 上传视频（相册现有的上传队列）② 建项目、选歌、写要求、开始
     │
     ▼
anas-api ──转发 /api/v1/montage──▶ anas-montage：项目、规划、时间线、任务
  │ “开始”前经文件代理               │ ⑤ 任务等规划时留下记号
  │ 登记规划授权                     ├──▶ 相册授权套接字：③ 登记授权 ④ 请求分析、读结果
  ▼                                  │                    ⑧ 取原片描述符 ⑨ 存回成片
唤醒服务（root）                      ├──▶ FFmpeg Worker：⑦ 填槽取灰度帧、逐段渲染、拼接配乐
  有授权、有记号时启动                ├──▶ 剪辑计算 Worker：选段、目录与规划数据、填槽
                                      └──▶ 配乐分析 Worker：allin1（节拍、重拍、段落）
  成员的定时任务单元
     │
     ▼
Hermes（成员的 VNAS，在成员自己的沙箱里）
  ⑥ 经剪辑的助手套接字调 montage MCP 工具，回到 anas-montage：读目录、交大纲、交镜头
anas-photos
  ├─ 剪辑分析（只为请求过的视频）：窗口向量 → 卡片 → 画质 → 描述
  │    用 FFmpeg Worker、剪辑计算 Worker、AI Worker，受相册的 AI 准入约束
  ├─ 结果存在 Catalog；窗口向量经 outbox 同步到相册 Qdrant 的新集合
  └─ 成片作为普通视频资产存进成员的个人图库
```

**改后的流程**
- **建项目**：浏览器先用相册的上传队列把视频传进本人的个人图库，再把资产 ID 交给剪辑中心。剪辑中心在这次请求里，用转发来的会话请相册登记项目授权，并请相册开始分析。
- **开始**：剪辑中心建一个持久任务，步骤依次为：
  1. 等分析完成；
  2. 选卡片目录；
  3. 读歌曲；
  4. 规划；
  5. 填槽与校验；
  6. 渲染；
  7. 存回相册。
- **规划**：任务走到规划时，NAS 在后台发起，成员不用开着桌面。root 的唤醒服务核对成员点“开始”时登记的规划授权，在成员自己的定时任务单元里跑一轮 VNAS。VNAS 调 montage MCP 工具读卡片目录，交回两步规划；剪辑中心按 Schema 和剪辑规则校验后才收下。
- **成片**：渲染结果经授权存进成员的个人图库，播放、下载都是相册里的普通视频。剪辑中心只记下“哪个版本对应哪个相册资产”。

**主要的失败行为**
- VNAS 不可用：任务停在“等待规划”，页面写明原因；上传、浏览、播放照常。
- 素材进回收站：任务暂停，恢复后继续。
- 素材被永久删除：报告缺了哪些；已存进相册的成片不受影响。
- 取消：不再调度新步骤，终止正在进行的渲染，不存回成片。

## 数据归属

| 数据 | 归属 | 位置 |
|---|---|---|
| 原片、成片 | 相册：照片资产。上传的原片与成片在成员的个人图库，从相册选入的视频留在原来的图库 | 相册受管存储与 Catalog |
| 项目授权 | 相册 | Catalog：`montage_grants`、`montage_grant_assets` |
| 窗口向量、卡片、画质、卡片描述 | 相册：按内容对象，跨项目、跨成员复用 | Catalog（见“剪辑分析”）；窗口向量另同步到相册的 Qdrant |
| 项目、批次、素材范围、任务、规划、时间线、版本与成片资产的对应 | 剪辑中心 | `montage/montage.db` |
| 配乐的副本与分析 | 剪辑中心：成员选歌时经文件代理复制的一份；原文件仍在成员的空间里，由音乐功能索引 | `montage/songs/<sha256>/`，没有项目再用时删除 |
| 渲染中间文件 | 剪辑中心 | `montage/tmp/<job-id>/`，任务结束时删除（见“清理”） |

剪辑中心不保存原片、分析结果或成片的副本；卡片为什么归相册、怎样理解 #514 的“项目特定的卡片分组”，见 [ADR 0038](../adr/0038-keep-montage-media-in-the-photo-library.md) 决定 2。

## 服务形态与部署

- **进程**：`/usr/lib/a-nas/anas-api montage-service`，单元 `anas-montage.service`。
  - 系统账号 `a-nas-montage`，固定 UID 31003，是 sysusers 里下一个空闲号。附加组 `a-nas-photos`，用来连 FFmpeg Worker 与剪辑计算 Worker 的套接字。这个组也让它能连上相册的产品接口与 AI Worker 的套接字，和家庭中心一样（见“项目授权”的“被攻破时”）。
  - 沙箱照搬 `anas-family.service` 的全部加固项，不只是下面几项：
    - `PrivateNetwork`、`ProtectSystem=strict`、`RestrictAddressFamilies=AF_UNIX`；
    - `NoNewPrivileges`、`PrivateTmp`、`PrivateDevices`、`ProtectHome`、`ProtectKernel*`；
    - `UMask=0077`、`RuntimeDirectoryMode=0750`；
    - `ReadWritePaths=/srv/a-nas/data`。实际能写哪里由子卷的属主与 ACL 决定，和家庭中心一样。
  - 资源：`MemoryMax=512M`、`CPUWeight=20`、`IOWeight=20`。重计算都在 Worker 和相册里，服务本身只管状态。
- **入口**：anas-api 用 `mount("/api/v1/montage", apiroute.Session, ...)` 把请求转到 `/run/a-nas-montage/montage.sock`。转发时去掉 Cookie、CSRF 与 Authorization 头，换成 `X-A-NAS-Session`；`X-A-NAS-Assistant` 由 anas-api 自己设置（见“规划授权”）。服务拿这个会话去问 Host Agent 的会话查询套接字 `/run/a-nas-montage-sessions/montage.sock`，确认来人是谁。
- **开关**：环境变量 `ANAS_MONTAGE_ENABLED`，M10 之前默认关闭。关闭时产品服务不挂载 `/api/v1/montage`，桌面不显示入口。网页入口只是外观，隐藏它挡不住接口，所以开关放在服务端；先例是 `ANAS_PHOTO_ASSISTANT_LOOKUP`。
- **存储**：Host Agent 建 `montage/` 子卷，权限 0700，属主 `a-nas-montage`，每 15 分钟修复一次漂移。
  - 数据卷离线或只读时，整个服务回答 503 `montage_unavailable`，不在系统盘建替代目录。
  - 容量不足只拒绝写入，不让整个服务不可用（[不变量 12](OVERVIEW.md#不变量)）：渲染写 `tmp/` 前用 `capacity.Admits` 按预计大小检查，存回时相册照常检查；不够时任务进入 `needs_input(storage_full)`，腾出空间后重试。已有项目与成片照常可看。
- **剪辑计算 Worker**：`anas-montage-compute.socket` 加 `anas-montage-compute.service`。
  - 单个常驻实例，逐个处理连接，免得每个连接都重新导入 NumPy。
  - DynamicUser，没有网络，看不到数据卷，用设备上已有的 AI 运行环境 `/usr/lib/a-nas/ai/runtime` 运行 `python -m anas_montage.worker`。
  - 套接字 `SocketGroup=a-nas-photos`、`0660`，相册与剪辑中心都能连。
  - 放在 `anas.slice`（后台工作，`CPUWeight=10`）之下，`MemoryMax=1G`。画质的帧分块交来（见“卡片与画质”），内存不随卡片长度增长。
- **配乐分析 Worker**：allin1 依赖 PyTorch 等，放不进计算 Worker 用的 AI 运行环境，所以另起一个。同样 DynamicUser、没有网络、看不到数据卷、放在 `anas.slice` 之下，只有剪辑中心能连。运行环境、内存上限与交付在 M8b 定（见“配乐”）。

### 接线清单

家庭中心实际接了约 40 个文件、55 处（2026-11-17 核对）。剪辑中心逐项照做，M1 在同一个 PR 里完成：

| 区域 | 要改的地方 |
|---|---|
| 单元与账号 | 新单元；`deploy/sysusers.d/a-nas.conf`；`anas-host-agent.service`（会话套接字的环境变量、组、`RuntimeDirectory`）；`anas-api.service` 的 `SupplementaryGroups` 加 `a-nas-montage`（否则连不上 0750 的 `/run/a-nas-montage`） |
| 包脚本 | preinst 检查 UID 31003 冲突、备份 `montage.db`、备份前停服务；postinst 重启服务；prerm 停服务；`build-packages.sh` 的 wants，以及安装 `ai/anas_montage`（含 `domain/`） |
| 体检与门禁 | `deploy/site/a-nas-check.sh` 的 UID、单元、数据库三处循环（数据库就绪看服务日志里的 `montage service ready`）；`make ops-check` |
| 系统测试 | 服务在跑；只读重挂前后停起服务；剪辑一节；产品服务的附加组 |
| anas-api | `cmd/anas-api/montage.go`、`config.go` 的套接字默认值、`main.go`、`deletion.go`（`accounts.ForgetStep("montage")`） |
| Host Agent | 会话查询、`EnsureMontageStore` 与卷上的调用、身份常量 |
| 路由 | `httpapi/product.go`、`routes.go` |
| 日志 | 服务端白名单、`api/components/logs.yaml` 的枚举、网页的日志列表、[日志规格](../specs/logs.md) |
| 系统更新 | 回滚时要停的服务与要恢复的数据库 |
| 网页 | `apps.tsx`、图标、桌面提示、MSW 替身 |
| 文档 | [架构总览](OVERVIEW.md)；[软件包部署手册](../runbooks/deploy-a-nas-packages.md)里的手工恢复命令 |
| Fake 模式 | 在进程内装配剪辑服务；相册授权、FFmpeg、计算 Worker 用替身，保证 fake-preview 能走完网页流程 |

## 剪辑中心的数据

数据库是 SQLite 的 `montage/montage.db`，使用 WAL 与 `synchronous=FULL`（掉电后已提交的事务也在），迁移版本记在 `user_version`。所有表都带 `owner_user_id`，按成员隔离。

| 表 | 主要列 | 说明 |
|---|---|---|
| `batches` | id、owner_user_id、request_key、created_at | 一次主动导入；`(owner_user_id, request_key)` 唯一，重复提交返回第一次的结果 |
| `projects` | id、owner_user_id、name、grant_id、state、song_id、song_track、song_start、song_selection、ask、chronological、chosen_version_id、created_at | `song_track` 是这首歌从音乐库的哪一首复制来的（接口的 `trackId`），配乐页只给这一首打勾，同名的其他歌不打勾；`song_selection` 是成员拖动起点后的选段；`chosen_version_id` 是成员确认的那一版（#596）。`state` 依次为 `registering`、`ready`、`deleting`。先写 `registering` 的行，再以项目 ID 为幂等键登记授权；登记要用会话，所以重启时停在 `registering` 的行无法补完，启动时撤销它的授权，并在同一个事务里删掉这一行与它的批次行 |
| `project_assets` | project_id、asset_id、object_id、batch_id、added_at | 素材范围固定到内容对象 |
| `jobs` | id、owner_user_id、project_id、request_key、settings_json、state、step、attempts、not_before、lease_until、heartbeat_at、cancel_requested、wait_reason、error_class、error、missing_json、save_started、plans、plan_wanted、plan_wanted_at、plan_epoch、kind、source_version_id、draft_id、draft_revision | `kind` 是 `generate`、`edit` 或 `recut`，后三列是 `edit`、`recut` 任务的来源（#596，见“出片后的二次编辑”）。`settings_json` 是任务开始时的歌、起点、要求与是否按拍摄时间，见“持久任务”。`save_started` 是存回栅栏：存回请求发出后置 1，结果确认前取消回答 409，重启后重放同一个版本键。`plans` 是这个任务出几个方案（最多 2；升级前开始的任务是 1，照开始时的样子做完）。`plan_wanted` 与 `plan_wanted_at` 表示等一轮规划及从何时起；`plan_epoch` 在重新规划、去掉素材时加一，旧轮次随之作废 |
| `job_assets` | job_id、asset_id、object_id | 任务开始时的素材集合 |
| `steps` | job_id、name、input_key、state、result_ref、started_at、finished_at | 每一步的输入摘要和结果 |
| `rounds` | id、job_id、owner_user_id、attempt_token、run_ref、state、claimed_at、started_at、ended_at、reported_at、outcome、epoch、template、input_key、called_at | 规划的每一轮。`epoch` 与 `input_key` 是领取时任务的规划代数与方案一第一步的输入键，每次工具调用都拿它们和任务现在的值比，对不上就回答“这一轮已结束”；`called_at` 是模型第一次调用工具的时间，规划时限从它算起。`state` 依次为 `claimed`、`started`、`ended`，或 `abandoned`；`run_ref` 是 Hermes 任务的编号。`reported_at` 是回合脚本报告 `end` 或已清理的时间，`stale` 列出还没有它的轮次 |
| `plan_grants` | owner_user_id、username、registered_at | 最近一次经过规划授权登记的请求时间（anas-api 带来 `X-A-NAS-Assistant: on`）。只用来决定留不留记号，授权本身在文件代理那里 |
| `plan_answers` | job_id、step、input_key、round_id、answer_json、accepted_at | 工具收下的答案，按（任务、步骤、这一步的输入键）只留第一份。任务自己的步骤按输入键取出，再写进 `plans`。`step` 只有 `pick`、`shots`，方案二也写这两个值，靠输入键区分 |
| `plan_briefs` | job_id、step、input_key、brief_json | 计算 Worker 为某一步、某个输入键做的 brief，只做一次；任务的步骤先做好，工具来读时直接用 |
| `plans` | job_id、plan、step、round_id、catalog_key、answer_json、accepted_at、voided_at | `plan` 是第几个方案；答案由任务的步骤从 `plan_answers` 取来写入，`round_id` 不再填写。每个任务每个方案每步只有一份有效答案。“重新规划”从还没收下的那一步开始；去掉素材使目录变化时，已收下的答案标为作废，两步都重新规划；每次“生成”是新任务，两步都重新规划 |
| `versions` | id、project_id、number、job_id、plan、version_key、film_asset_id、kind、source_version_id、draft_revision、timeline_json、created_at | 成片存进相册后才写一行，一个任务的每个方案一行（`job_id` 与 `plan` 唯一）；失败或取消的任务不留版本，已存的方案留着。`number` 在项目内从 1 递增，是成片文件名里的版本号。`version_key` 由任务 ID 与渲染步骤的 `input_key` 组成，与渲染输出的字节无关。`kind` 同任务；`timeline_json` 是这一版的时间线，存回时在同一个事务里写入，修改稿、粗预览与镜头清单都从它读 |
| `drafts` | id、owner_user_id、project_id、base_version_id、revision、slots_json、updated_at | 修改稿，每个版本一份（`base_version_id` 唯一）。`slots_json` 记这一版的音乐槽，和按成片顺序盖住它们的镜头：每镜的节拍、盖住第几到第几个槽（删掉一镜就是把它的槽并给前一镜或后一镜）、哪个槽是它自己的（`home`，“还原这一镜”与“已换”按它判断）、是否锁定 |
| `draft_revisions` | draft_id、revision、op、seed、back、slots_json、created_at | 每一步一行。`back` 是撤销这一步回到的修订：普通的一步是它之前的修订，撤销的一步是被撤销那一步的 `back`，所以连续撤销一直往回走，不会变成重做。保留最近 100 个修订和撤销链上的修订，最多 101 行，所以不管其中几步是撤销，都能撤销最近 100 步 |
| `revocations` | grant_id、next_try、attempts | 撤销授权时相册不可用，就记在这里重试 |
| `songs` | id、owner_user_id、sha256、title、duration、state、reason、analysis_version、analysis_json、created_at | 成员选过的歌：副本的摘要、分析结果（节拍、重拍、段落、副歌、响度）与不可用的原因 |

子卷里的文件：
```text
montage/
  montage.db
  songs/<sha256>/audio      （成员选歌时复制的一份，见“配乐”）
  tmp/<job-id>/             （渲染中间文件与待存回的成片，按任务分目录）
```

**清理**
- 删除项目：先取消任务，再删掉项目、版本记录、修改稿与规划，撤销相册授权；不动相册里的原片和成片（#514）。
- `tmp/<job-id>/` 在任务结束（完成、失败或取消）时删除；服务启动时只删掉已结束任务的目录。渲染步骤记为完成、目录里的文件却不见了时，渲染步骤视为没完成，重新渲染。
- 删除账号：`ForgetStep("montage")` 清掉这位成员的全部行。相册在自己的账号删除步骤里清掉这位成员的授权。相册那一步先执行，所以剪辑中心的撤销必须幂等。
- 配乐副本：删除项目、删除账号之后，回收已经没有任何 `songs` 行引用的 `songs/<sha256>/` 目录。`songs` 行在当前项目、未结束的任务或任何版本（经它的任务的 `settings_json`）还引用时保留：二次编辑的粗预览、存为新版本与重剪都用版本当时的歌（#596 起；在此之前项目换歌后，旧任务的歌会被回收）。同一份内容可能被几位成员共用，所以按引用回收；服务启动时也对一次账。
- 启动时对账：`montage.db` 与相册的授权表可能被恢复到不同的时间点。剪辑中心启动时用 `list` 列出相册里自己的授权：剪辑中心有、相册没有的，项目标为素材缺失；相册有、剪辑中心不认识的，撤销。

## 项目授权

剪辑中心后台读原片、请求分析、存回成片时，成员可能早已离线；#514 又不允许长期保存明文会话令牌。所以改由相册持有一条项目授权。思路类似 Google Drive 的 `drive.file`（应用只能访问用户交给它的文件）：成员在场时登记，之后服务凭授权访问，权限仍由相册每次重新判断。

**套接字与协议**
- 由 systemd 持有：`anas-photos-grants.socket` 监听 `/run/a-nas-photos-grants/grants.sock`，`SocketUser=a-nas-montage`、权限 0600，所在目录只有 root 能写，交给 `anas-photos` 接受连接。不用属组 0660，是因为产品服务也在 `a-nas-montage` 组里。相册另用 `SO_PEERCRED` 核对对端 UID 是 31003。
- 协议用 `internal/unixmsg`：4 字节长度加 JSON，带描述符，已有 fuzz 测试。HTTP 传不了 `SCM_RIGHTS`，所以不用 HTTP。
- 请求与回答的 Go 类型放在 `internal/photosapi`，双方共用。按先例，内部接口不进 `api/` 契约（[API 契约](../development/api-contract.md)）。
- 这个套接字上的请求不算有人在用相册，不推迟相册的 AI 工作；助手套接字上的请求则算。例外是 `searchWindows`：成员在修改页等着结果，所以它和照片搜索一样算。

**相册用的身份**：每次都按授权主人重新构造一种新的“授权身份” Principal。它不能照搬助手身份：助手身份看不到视频、不能导入、不能打开原片（`internal/photos/policy.go`、`assets.go`）。授权身份：
- 能看到视频、打开原片，只能往主人的个人图库导入，其他写操作一律拒绝；
- 不带管理员身份，也不带管理员查看授权；
- 主人的账号状态经 Host Agent 的会话查询（在用成员列表）核对，账号停用就拒绝；
- 授权已撤销、资产已不可见，就拒绝。

| 操作 | 说明 |
|---|---|
| `remove(grantId, assetIds)` | 从授权里去掉资产（成员在任务里去掉素材时），之后剪辑中心读不到它们；不再被任何授权引用的分析按保留规则删除 |
| `register(session, projectId, assetIds)` | 登记或追加。会话只在这次请求里用，相册经 Host Agent 认出成员，再按上面的受限身份检查：每个资产都是本人能看到、未进回收站的视频。以 `(主人, projectId)` 为幂等键，重试不会多出授权。不存在、看不见、在回收站、不是视频，这四种情况的回答完全相同。一次最多 500 个资产 |
| `lookup(grantId, assetIds)` | 每个资产的状态 `available`、`trashed` 或 `missing`，以及 `objectId`、时长、宽高、拍摄时间、各项分析的进度 |
| `analyze(grantId, assetIds, retry)` | 为这些视频排剪辑分析，见“剪辑分析”；`retry` 把失败的分析改回待做 |
| `analysis(grantId, assetId)` | 返回分析结果：窗口（时间、是否纯色）、卡片、画质与描述，附各自的分析身份。窗口向量放在 memfd 里，是 `n × 768` 个小端 float32，只按长度校验 |
| `open(grantId, assetId)` | 原片的只读描述符，经 `SCM_RIGHTS` 传出。剪辑中心把它原样转给 FFmpeg Worker，不复制文件；同一个描述符同一时刻只用于一个请求 |
| `saveFilm(grantId, versionKey, name, fd)` | 相册把成片导入主人的个人图库，返回资产 ID，并把成片加进这条授权。幂等：先按 `(主人, versionKey)` 查有没有存过、并按这个键加锁，再暂存文件；`importStaged` 增加一个钩子，在建资产的同一个事务里写 `montage_films`（`(owner_user_id, version_key)` 唯一），所以提交与记录之间崩溃也不会多存一条。拍摄时间由 `assemble` 写进 MP4 的 `creation_time`，相册照常读取。相册照常检查容量，不够时回答 `insufficient_storage` |
| `searchWindows(grantId, texts, k)` | 修改页的文字搜镜头（#596，[ADR 0039](../adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md) 第 8 项）：相册用自己的 `embedQuery` 编码每段文字，只在这条授权的内容对象里检索窗口集合，回答每段文字最像的 k 个窗口（资产、窗口序号、分数）。Qdrant 不可用或集合不在时退回 Catalog 精确计算，结果相同；文字与窗口的模型不一致时回答 `model_mismatch` |
| `list()` | 列出剪辑中心的全部授权（grantId、主人、项目 ID、是否撤销），供启动对账 |
| `revoke(grantId)` | 标记撤销，幂等 |

**被攻破时**：剪辑服务在 `a-nas-photos` 组里，所以：
- 能连相册的产品接口，可以用当时收到的成员会话操作相册；
- 能直接连 AI Worker，绕过相册的准入占用模型（它看不到原片，只能送自己的数据）。

这两点与家庭中心相同（[家庭中心技术设计](family-center.md)）。项目授权约束的是成员离线后的后台访问：只能读授过权的资产，只能往授权主人的个人图库存成片。

## 素材与成片的生命周期

相册没有事件可以订阅，剪辑中心在这些时点调用 `lookup`：
- 每一步开始前；
- 规划前与渲染前；
- 执行中的任务每 10 分钟一次。

| 查到的状态 | 任务 | 项目页 |
|---|---|---|
| 全部 `available` | 照常执行 | — |
| 有 `trashed` | 进入 `waiting`（原因 `asset_trashed`），每 10 分钟再查，全部恢复后自动继续 | “有 N 个视频在回收站，恢复后继续” |
| 有 `missing` | 进入 `needs_input`（原因 `asset_missing`） | 列出缺失的视频，用户选“去掉这些继续”或“取消” |

**内容变化**：相册里资产的内容不可变（[ADR 0006](../adr/0006-use-a-managed-photo-library.md)）。但为了不依赖这一点，`open` 返回的 `objectId` 每次都与 `project_assets.object_id` 核对，不一致就当作 `missing`。

**成片**：成片是相册里的普通视频。成员在相册里删掉它，项目页的版本显示“成片已不在相册”，可以重新生成；删除剪辑项目不删成片。

## 剪辑分析

相册只为授权里请求过的视频排剪辑分析。分析不进 `completeJob` 链，也不进迁移回填；`inLocalAI` 保持不变，所以视频照旧不进相册的搜索、聚合、找相似与 AI 整理进度，助手用的相册接口也照旧看不到视频。

每个视频按下面的派生依次排队，前一项完成时，在同一个事务里排下一项：

| 派生 | 做什么 | 用到 |
|---|---|---|
| `montage-windows/v1` | 每 2 秒一个窗口，取窗口正中一帧，编码成向量 | FFmpeg `frames`，AI Worker `embed_image` |
| `montage-cards/v1` | 按窗口向量切卡片，定关键帧时刻 | 剪辑计算 Worker `cards` |
| `montage-quality/v1` | 每张卡片的模糊、抖动、遮挡、失焦与不可用原因 | FFmpeg `frames`，计算 Worker `quality` |
| `montage-describe/v1` | 每张画质合格的卡片的节拍、过程、场景、机位（第八版也只描述画质合格的卡片） | FFmpeg `frames`，AI Worker `describe_frames` |

**排队与去重**
- `analyze` 入队时带 `NOT EXISTS` 条件：已有当前分析身份的结果就不排。
- 相册任务完成时会删掉任务行，所以去重不能只靠 `jobs` 表的主键 `(object_id, derivation)`。
- 相册重启时只自动重排 I/O 类失败的任务；其他失败的行留在 `failed`，`lookup` 报告“分析失败”，剪辑任务进入 `needs_input(analysis_failed)`。用户在项目页点“重试分析”时，剪辑中心调 `analyze(..., retry)`，相册把这行改回 `pending`。
- 单张卡片描述失败（比如超出上下文）作为这张卡片的结果存下，这张卡片不进目录；不让整个视频的任务失败。

**任务粒度与租约**
- 一个视频一个任务，但按块推进：
  - 窗口每 30 个一块；
  - 画质每张卡片一块，帧再按 30 秒分批（见“卡片与画质”）；
  - 描述每张卡片一块。
- 每块落库后续租约，块之间检查准入。
- 一次描述可能要 15 分钟，超过 5 分钟的租约，所以相册的任务表增加续租：调用进行中每 30 秒续一次。
- 10 分钟的 4K 视频约 300 个窗口。按 NAS 上约 5 CPU 秒一张（[相册 AI](photo-ai.md)）算，远超 5 分钟的租约，所以必须分块。

**调度**
- 有剪辑分析时，`aiRunner.round` 先做它们，再回到相册自己的阶段，因为有用户在等。
- `workWaiting` 计入剪辑分析的任务。
- 窗口用嵌入模型的阶段；描述用读文字的阶段，和 OCR 共用 llama-server，加载需要的内存按 `ocrMemoryBytes` 算。AI Worker 没有读文字模型时，描述不排，`lookup` 报告“本地模型不可用”，剪辑任务停在 `waiting(analysis)` 并写明原因。
- 前台有人在用相册时，和检测、读文字一样每 200 毫秒查一次准入，一关就挂断，所以描述不会让成员的搜索排在 llama-server 后面（#365）。
- 准入统计 `system.slice` 的压力，而 FFmpeg 实例也在 `system.slice` 里，分析自己取帧的负载会被算成“别的工作的压力”。所以分析中的 FFmpeg 取帧只在前台使用或内存不足时挂断；压力超限时取完这一块再暂停。M5 在实验 NAS 上实测会不会反复暂停。

**保留**
- 结果跟着内容对象。内容对象被清除时（不是某个资产被删除时，同一内容对象可能还有别的资产），级联删掉 Catalog 行与 Qdrant 中的点。
- 没有任何未撤销的授权引用某个对象满 7 天后，相册删掉它的剪辑分析。
- 换模型或改分析身份时，`requeueStale` 只重排仍被未撤销的授权引用的对象，不会把曾经请求过的视频全部重算。

**Catalog 迁移**（带回滚步骤）

| 表 | 主要列 |
|---|---|
| `video_windows` | object_id、idx、start_ms、end_ms、flat、vector（BLOB，768 个 float32）、identity |
| `video_cards` | object_id、card、start_ms、end_ms、keyframes（JSON：关键帧时刻）、identity |
| `video_card_quality` | object_id、card、result（JSON）、identity |
| `video_card_descriptions` | object_id、card、result（JSON）、identity |
| `montage_grants` | id、owner_user_id、project_id、created_at、revoked_at |
| `montage_grant_assets` | grant_id、asset_id |
| `montage_films` | owner_user_id、version_key、asset_id |
| `window_outbox` | object_id、version |

### 窗口向量

- **窗口**：沿用第八版的 2s1f 布局。每段视频的窗口数是 ⌈时长 ÷ 2 秒⌉，不是总时长除以 2。
- **帧**：长边 512 的 JPEG，与相册的 `thumbnail/v1` 相同，所以窗口向量能和照片向量放在同一个向量空间里比较（[向量索引](vector-index.md#将要接入的集合)）。第八版也是长边 512，但由 PIL 缩放并编码，像素与相册不完全相同，所以迁移时的黄金比对拿第八版自己的窗口向量当输入，不拿新算的。
- **编码**：AI Worker 的 `embed_image`，与图片向量用同一个模型：EmbeddingGemma 2、70 个视觉 token、L2 归一化、768 维。
- **分析身份**：在模型字符串之外，再记 `win=2s1f,frame=edge512,jpeg82,rot=display,dim=768,l2`。任何一项变化都算另一份分析。
- **向量在 Catalog**：`video_windows.vector`。这是 [ADR 0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md) 决定 1 的接入前提。
- **同步到相册的 Qdrant**：
  - 新集合 `video_windows_v1_<模型摘要>_<nonce>`；点 ID 由 `(object_id, idx)` 生成；payload 是 `object` 与 `libraries`，和图片集合用同一个范围键。
  - 用独立的 `window_outbox` 同步。触发器建在 `video_windows` 和资产的图库变化上。现有的 `vector_outbox` 主键只有 `object_id`，同步循环也只认 `images_*`，不能复用。
  - `internal/vectorindex` 新增按 `object` 过滤删除的类型化操作，因为一个对象有多少个窗口点，删除时 Catalog 行已经没了。
  - 对账与“集合丢失就立即重建”按集合分别做。
- **读路径**：首版不读 Qdrant，`analysis` 从 Catalog 读。窗口集合不参与搜索，所以不受[换模型](vector-index.md#换模型)时“先改为集合别名”的约束；二次编辑的文字搜镜头（`searchWindows`，#596）是第一个检索的读者，在 [#599](https://github.com/zhongwater123/A-NAS/issues/599) 中满足它，见[向量索引](vector-index.md#剪辑中心的窗口集合)。
- **不建 HNSW**：点数在十万级以内，精确扫描就够。内存账：一组 47 分钟的素材约 1,460 个点、4.5 MB，Catalog 与 Qdrant 各一份。
- **与 #207 的关系**：#207 的视频分段可以从这些窗口派生，由 #207 的设计决定。

### 卡片与画质

- **卡片**：调计算 Worker 的 `cards`，输入是窗口时间、是否纯色和向量，输出卡片与关键帧时刻。纯色阈值与第八版相同（`FLAT=8.0`），记在分析身份里。
- **画质**：每张卡片要三组帧：
  - 关键帧：宽 512 的 JPEG，质量与第八版相同。模糊值按第八版的标定对**编码后的 JPEG** 跑 blurdetect；遮挡由计算 Worker 从 JPEG 缩成 256×144 的灰度图计算。
  - 抖动：覆盖整张卡片的 320×180 灰度帧，每秒 10 帧。
  - 夜空锐点：关键帧时刻宽 1920 的灰度帧，逐帧计算。
- **分两步算，保证和第八版一致**：第八版的抖动先算相邻帧的位移，再做 5 帧滑动平均、去掉两端，最后对整张卡片取中位数（`domain/picture.py` 的 `jitter`），中位数不能按块合并。
  1. `quality_part`：抖动帧按 30 秒一块交给计算 Worker（相邻块重叠一帧），只返回逐对位移；关键帧与锐点帧逐张返回各自的数值。内存不随卡片长度增长。
  2. `quality`：相册拼起整张卡片的位移和各帧数值，再调一次，算平滑、中位数与不可用原因。
- 判定规则与阈值沿用第八版的 `domain/picture.py`、`domain/quality.py`，记在分析身份里；黄金比对包括抖动值。
- **拼接素材**：第八版的拼接工具 `tools/join.py` 是离线手动步骤，首版不做。每段视频单独切卡片。

### 卡片描述

- **新操作**：AI Worker 新增 `describe_frames`。
  - 输入：一张卡片的 3–20 张关键帧，以及每张离卡片开头的秒数、分组布局、卡片时长与节拍数。
  - 关键帧放在同一条消息里，每张一个描述符，不需要偏移表，也就没有新的二进制解析。`internal/unixmsg` 与 Python 端的 `recv_fds` 改为按调用方给的上限收多个描述符（FFmpeg 2 个、AI 20 个），在 M7 补跨语言测试。
  - 体裁固定为第八版上传流程用的“旅行”（`TRAVEL`），记进分析身份。分析要跨项目、跨成员复用，所以不按项目改体裁。
- **提示词**：不收调用方的提示词，模板固定在 Worker 里，版本记作 `montage-card/v1`。
  - 模板来自第八版 `domain/prompts.py` 的 `PROMPT` 与 `CAMERAS`，只加了一句：不要转写画面里的证件号、票号、车牌等号码（[决定](../decisions/2026-11-17-montage-center-first-version.md)第 11 项）。
- **输出**：按第八版 `domain/describe.py` 的 Schema 校验，不合格记 `invalid_result`。
  - 第八版遇到重复时换重复惩罚、最多重问 3 次，这个重试放在操作内部。
  - 客户端对这个操作的超时设为 15 分钟；`aiworker` 默认的 2 分钟不够。
- **过滤**：相册把描述存进 Catalog 之前，用照片文字的号码规则（`textread.MaskNumbers`）替换可能出现的号码：校验位正确的身份证号、通过 Luhn 校验的银行卡号、手机号，以及其他 15 位以上的数字串。车牌、短票号不在规则内，只靠提示词约束。`textread` 没有规则版本号，所以剪辑分析在自己的分析身份里记一个过滤版本。描述只会交给成员的 VNAS，所以相册不保留原文。
- **运行**：复用现有的 `llama-server`（Qwen3.5-4B Q4_K_M 加 mmproj，`-c 8192 -np 1`），不另起一套模型。
- **上下文**：沿用第八版（2026-11-17 用户决定“按之前的来”）。
  - 关键帧宽 512 时，一张横拍约 146 个 token，竖拍约 450 个；每次请求的文字约 969 个 token，回答上限 1,800 个。
  - 2026-11-17 统计品杰、新西兰、川西、潜水、Insta360 共 481 张卡片，关键帧最多 11 张。11 张全按竖拍算：11 × 450 + 969 + 1,800 = 7,719 ≤ 8,192。
  - 12 张竖拍仍放得下（12 × 450 + 969 + 1,800 = 8,169）。规则上一张卡片最多 20 张关键帧（`MOST = 20`），超出上下文时，和第八版一样当作这张卡片描述失败，卡片不进目录。
- **耗时**：同一个模型在 NAS 上读一张照片的文字，中位约 105 秒（[OCR 方案](photo-ocr.md)）。描述一次要看多张图、写更多字，一组 118 张卡片估计要几个小时。M7 在实验 NAS 上实测。

## 剪辑计算 Worker

第八版的领域层只用标准库和 NumPy，没有输入输出。它原样放到 `ai/anas_montage/domain/`（来源提交 `19f2d6db`），外面加一个 Worker 入口。

| 操作 | 调用方 | 输入 | 输出 | 来自第八版 |
|---|---|---|---|---|
| `cards` | 相册 | 窗口时间、是否纯色、向量 | 卡片与关键帧时刻 | `domain/cards.py` |
| `quality_part` | 相册 | 一块抖动帧，或一张关键帧 JPEG（附模糊值），或一张锐点帧 | 逐对位移，或这一帧的遮挡、暗帧、锐点数值 | `domain/picture.py` |
| `quality` | 相册 | 一张卡片的全部位移与各帧数值 | 模糊、抖动、遮挡、失焦与不可用原因 | `domain/picture.py`、`domain/quality.py` |
| `catalog` | 剪辑中心 | 卡片、描述、画质、选项 | 可用卡片的目录文本 | `domain/catalog.py` |
| `music` | 剪辑中心 | allin1 的结构（节拍、重拍、段落与标签）、响度序列、目标秒数、起点 | 选段、音乐段落、小节槽 | `domain/music.py` |
| `plan` | 剪辑中心 | 目录、音乐段落、要求、顺序；第二步再加第一步的答案 | 一步规划的数据、这一轮的精确 Schema 与规则模板的版本；规则本身见“规划” | `domain/plan.py`（`pick_request`、`shots_request`）、`domain/prompts.py`、`domain/music.py` 的音乐说明 |
| `shots_answer` | 剪辑中心 | 第二步的答案 | 整理后的镜头 | `domain/plan.py` |
| `fill` | 剪辑中心 | 大纲、镜头、卡片、槽、窗口向量、已知的无遮挡区间；可选的 `pins`（按成片顺序列出每个槽与钉住的节拍，#596） | 时间线（每个镜头的起止帧）与没填满的槽；或者 `need`：还缺哪一段的无遮挡区间 | `domain/fill.py` |
| `likeness` | 剪辑中心 | 两个节拍各自覆盖的窗口向量 | 两组均值的余弦（#596：删除一镜时选哪一边延长） | 新增，第八版没有 |
| `clear` | 剪辑中心 | 关键帧与一段 256×144 的灰度帧 | 关键帧两侧的无遮挡区间 | `domain/picture.py` |

**协议**
- 一次连接一个请求、一个回答。请求是 4 字节长度加 JSON，数组都放在同一条消息里，每个数组一个 memfd 描述符；JSON 写明各数组的名称、类型和形状，Worker 只按长度校验。
- **`fill` 用重入的方式填槽**：第八版填槽时要按需读灰度帧，判断镜头两侧有没有东西挡住（`held` 与 `picture.clear`）。
  1. `fill` 用已知的区间往下填，遇到第一个不知道的 `held` 就返回 `need: {video, key, lo, hi, turned}`；
  2. 剪辑中心经 `open` 和 FFmpeg `frames` 取 256×144、每 0.5 秒一帧的灰度帧，调 `clear` 算出区间，加进已知区间；
  3. 带着全部已知区间重新调用 `fill`，直到返回时间线。
- `fill` 是确定的，同样的已知区间得到同样的下一个 `need`。已知区间的键与第八版的 `held` 缓存相同（`节拍@lo-hi`），记在这一步的结果里，重跑同一个任务时直接复用。
- 这样第八版的计算一行不改，Worker 也不会在等帧时被一个调用占住：相册的 `cards`、`quality` 可以插在两次 `fill` 之间执行。
- 任何调用方都不能一边占着 FFmpeg 槽位、一边等计算 Worker，否则会互相等死。

**其他约定**
- **单实例**：Worker 是单实例，逐个处理连接。每次调用都在秒级到几十秒；取消在当前调用结束时生效。
- **依赖**：NumPy 与 Pillow（遮挡与暗帧判断要把 JPEG 转成灰度，第八版用 PIL）改为 `ai/requirements.in` 的直接依赖，现在它们只是间接依赖。
- **跨语言测试**：照 `internal/aiworker/python_test.go` 的做法，用真实运行环境跑 Go 与 Python 之间的协议测试。
- **系统测试**：系统测试的 Python 环境没有 NumPy，所以用固定回答的替身计算 Worker，只证明接线与权限；真实计算的证据在开发机上取。

**迁移分两步提交**
1. 原样搬进来，用第八版的结果文件做黄金比对：同样的输入，镜头表逐个相同。
2. 修复缺陷，每项单独提交，见“原型缺陷的修复”。

**样本**：第八版的结果文件来自真人素材，不进公开的仓库。
- 位置：开发机 C（macOS，见[开发环境](../development/LOCAL_ENVIRONMENT.md)）的 `~/A-NAS-datasets/`，目录按原型的 `profiles/mac-dev.toml`：原片在 `videos/`，窗口向量在 `cache/`，卡片与描述在 `剪辑对比/第五版`，规划与镜头表在 `剪辑对比/第四版/剪法24（2026-11-08）`。
- 组名见原型的 `profiles/cases.toml`：`pj`（品杰，新西兰）、`ne`（新西兰）、`ch`（川西）。
- 黄金比对的测试从环境变量给出的目录读样本，没有样本时跳过，所以证据层是“开发机”。M6 的 PR 列出所用各文件的 SHA-256；首版闭环所用的 11 段视频清单与摘要记在 M10 的 PR 里。

`music` 的输入仍是 allin1 的结构，原样迁移：黄金比对用第八版的 allin1 结构文件跑 10 首测试歌。allin1 本身不在计算 Worker 里，见“配乐”。

`plan` 不在黄金比对之内：第八版把规则和数据写在同一段文字里（例如音乐说明里有“进入高潮段前的最后一个画面……”），交给 VNAS 时要拆开，见“规划”。

## FFmpeg Worker 的新操作

相册（取帧）和剪辑中心（填槽取帧、渲染）都用这些操作，参数都由 Worker 自己拼。新操作的请求按 `DisallowUnknownFields` 解码，请求里带路径或 filter 字符串会被拒绝。

| 操作 | 输入 | 参数 | 输出 | 上限 |
|---|---|---|---|---|
| `frames` | 1 个原片描述符 | 时刻列表，或起止加帧率；`jpeg`（宽或长边 64–1920、质量、可选 blurdetect）或 `gray`（宽 × 高） | 每帧一条 `unixmsg` 正文，最后一条 JSON 带模糊值 | 最多 1,200 帧；`gray` 的 n × 宽 × 高 ≤ 64 MiB；`jpeg` 最多 600 帧、宽不超过 1920；先按参数预检，不合规回答 `invalid_request`；运行中超限回答 `too_large`，不能是 `unreadable` |
| `segment` | 1 个原片描述符 | 起点、输出帧数、放慢倍数（≤ 2.5）、旋转、是否非 16:9（垫模糊背景）、结尾淡出、时间戳偏移（这段在时间线上的起点） | MPEG-TS：1920×1080、30 帧、H.264（CRF 23、veryfast、`-maxrate 16M -bufsize 16M`），无声 | 输出不超过 12 秒（360 帧）；5 分钟 |
| `assemble` | 2 个描述符：拼好的 TS、歌曲音源 | 音乐选段起点、成片帧数、淡入 0.3 秒、结尾淡出 2.5 秒、写进 MP4 的 `creation_time` | MP4（faststart）：画面直接复制，音频 AAC 192k 立体声 | ≤ 150 秒、≤ 512 MiB；10 分钟 |
| `audio`（扩展） | 1 个歌曲描述符 | 新增 PCM 输出：f32 原始采样，声道数与采样率按配乐分析的输入定（allin1 先用 Demucs 分离音轨，按立体声 44.1 kHz 准备；退回 beat_this 时是单声道 22050 Hz） | 原始采样 | 与现有 `audio` 相同 |

**`frames`**
- 时限按覆盖的源时长算：60 秒加上 3 倍覆盖秒数，最多 30 分钟。Worker 没有硬件解码，4K 素材在 M4 中于实验 NAS 上实测后再定。
- 分帧不手写解析：
  - FFmpeg 以 `mpjpeg` 格式输出 JPEG，每帧带边界与长度，Worker 用 Go 标准库的 `mime/multipart` 拆开；灰度帧大小固定，按宽 × 高切；
  - Worker 把每帧作为一条 `unixmsg` 正文发回，复用已有 fuzz 测试的 `WriteBody` 与 `ReadBody`。
- blurdetect 对已编码的 JPEG 再跑一遍 FFmpeg，保持第八版的标定（`BLUR_MOST=8.0` 是按 JPEG 标定的）。数值经 `metadata` 滤镜逐帧输出为文本，Worker 按行读出数值。

**`segment`**
- 按帧数而不是秒数传参：`fill` 已经算出了整数帧，按秒舍入会让 45 段累积出超过 0.1 秒的误差。
- 12 秒指输出时长。输入时长 = 输出帧数 ÷ 30 ÷ 放慢倍数，所以放慢只会让输入变短。
- 最长的一段是最长的音乐槽；配乐分析保证最长的槽不超过 12 秒。
- 每段带 `-output_ts_offset`（这段在时间线上的起点），剪辑中心把各段按顺序首尾相接写进一个文件，时间戳是连续的。
  - 第八版的做法是逐段输出 MKV 再用 concat 解复用器拼接，但那要一次读多个文件。
  - 字节拼接时如果每段时间戳都从 0 开始，播放器看到的时间戳会往回跳。

**`assemble`**
- 画面直接复制，只编码音频。
- MP4 的 faststart 要能回头改文件，所以 Worker 先写进自己的 memfd（和 `image` 操作一样经 `/proc/self/fd` 访问），写完再作为正文发回。剪辑中心边收边写进 `tmp/<job-id>/`，再交给 `saveFilm`。这份 memfd 计入 FFmpeg slice 的 3 GiB 内存上限。
- 512 MiB 的推导：
  - 画面码率上限：16 Mbit/s × 150 s ÷ 8 = 300,000,000 B；
  - VBV 余量：每段编码都从缓冲区装满约九成开始，最多再多出一个 bufsize，即 2,000,000 B。时间线校验限定镜头不超过 100 个（第八版最短镜头 1.5 秒，150 秒约 100 段，但槽尾可能留下更短的镜头，所以不靠它推导），合计最多 200,000,000 B；
  - 音频：192 kbit/s × 150 s ÷ 8 = 3,600,000 B；
  - MP4 索引约 1 MiB；
  - 合计约 5.05 亿字节，合 482 MiB，取 512 MiB。
- 2026-11-17 重剪的 5 条成片是 77–95 秒、65–140 MiB。

**准入与 CPU**
- 新操作都用 `Background` 优先级，修改页的缩略图除外（#596，见“出片后的二次编辑”）。全机的后台请求一共只有 2 个槽位：剪辑中心同一时刻最多发 1 个 FFmpeg 请求，相册的剪辑分析同一时刻也最多 1 个。两者同时在跑时，缩略图、影视与音乐扫描要等，首版接受；跟着 [FFmpeg 槽位的决定](../decisions/2026-10-21-ffmpeg-worker-slots-and-memory.md)调整。
- FFmpeg 实例都在 `system-anasffmpeg.slice` 里，实例之间 CPU 权重相同，`Background` 只限制并发数。所以：
  - `Background` 操作的 FFmpeg 进程以 nice 10 运行，调高 nice 不需要特权；
  - `segment` 与 `frames` 限制线程数，初值 4，M4 实测后定；
  - 这样渲染不会和正在播放的转码平分 CPU。
- `unixmsg` 一条消息可以带多个描述符，上限由调用方给：`assemble` 2 个，`describe_frames` 20 个。

**M4 同时更新的文档与测试**
- [架构总览](OVERVIEW.md)里的“至多一个描述符”和 FFmpeg Worker 的操作列表；
- 带真实 FFmpeg 的测试；
- 上限触发时的行为测试：报错，不截断，不回答 `unreadable`。

## 配乐

2026-11-18 评审人改定（[决定记录](../decisions/2026-11-17-montage-center-first-version.md)第 2 项）：配乐从成员自己的音乐库选，或上传一首。同日用户决定 NAS 上用 allin1 分析，商业版权待议（见下文“待议：商业版权”）。

**选歌**
- 配乐页用音乐功能现有的列表接口，列出成员在音乐库里能看到的歌，也可以上传一首。
- 上传走现有的文件上传，存进本人个人空间的“音乐”文件夹。音乐库只在打开音乐窗口或点“重新扫描”时扫描，所以配乐页上传后自己调 `POST /api/v1/music/library/scan`，再轮询音乐库的状态，扫到后从列表里选。成员自定义过音乐文件夹、又没包含“音乐”时，上传的歌不会出现，页面提示去音乐里加上这个文件夹。

**复制一份**
- 成员选定一首时（会话还在），页面调 `PUT /projects/{id}/song`，带上歌曲编号。评审人建议在点“开始”时复制；这里提前到选歌时，因为试听要先有分析结果，两者都在成员在场时。
- 这条请求在 anas-api 里有自己的处理：它用音乐功能播放时的同一条路径，经文件代理以成员身份打开文件，流式交给剪辑中心。剪辑中心按内容摘要存成 `montage/songs/<sha256>/audio`。
- 音乐功能读文件要成员在线的会话（[ADR 0018](../adr/0018-index-music-in-place-through-the-file-broker.md)），所以在成员在场时复制。剪辑中心离线时不用再读成员的空间，文件代理也不用加新的授权路径。
- 文件大小与时长的上限见规格的默认规则，超出时回答 413 或 422。
- 副本在没有项目再用、也没有进行中的任务用时删除。音乐库里的原文件不受影响，成员之后改动或删除它，也不影响已经选好的项目。

**分析**：复制完成后在后台进行，配乐页显示进度。
1. **结构**：[allin1](https://github.com/mir-aidj/all-in-one) 的 `harmonix-all` 模型给出节拍、重拍、段落与段落标签（包括副歌），与第八版读的结构文件相同。
   - 它先用 Demucs 分离音轨，再用带 NATTEN 注意力的模型推理，重拍用 madmom 的 DBN 后处理。PyTorch、Demucs、NATTEN 与 madmom 设备现有的 AI 运行环境里都没有，所以 allin1 放在单独的配乐分析 Worker 与运行环境里：没有网络、看不到数据卷，歌曲经描述符交来，一次分析一首。
   - 权重（HuggingFace `taejunkim/allinone` 的 8 个分折模型）与 Demucs 的模型按现有的模型受管交付方式下发，记下摘要。运行环境怎样构建与交付在 M8b 定。
   - 评审人记的耗时是 CPU 上一首 2–5 分钟；M8b 在实验 NAS 上实测用时与峰值内存，按 8 GB 的预算比。
2. **选段**：`music` 原样用第八版的选段与小节槽算法，输入就是 allin1 的结构。
3. **响度**：同第八版。

**待议：商业版权**：用户要求把 allin1 的商业版权记为待议点。各组成的许可与要议的问题见[决定记录](../decisions/2026-11-17-montage-center-first-version.md#allin1-的商业版权)；要点是 madmom 的模型文件为 CC BY-NC-SA 4.0，以及 allin1 与 Demucs 权重的训练数据。待议期间 allin1 只在开发机与实验 NAS 上运行；M10 打开开关之前要有结论。结论是不能随产品分发时，退回配乐探针测过的做法（[配乐研究](https://github.com/zhongwater123/A-NAS/pull/546)）：节拍与重拍用 beat_this（MIT，ONNX），过了门槛；副歌用按小节的色度自相似，只找对 9 首中的 4 首，按研究文档“副歌没过之后”的做法补。

**试听与拖动起点**：#514 要求音乐分析出错要能看到、能处理。
- 配乐页显示选中的段落，可以试听；不对就拖动起点，吸附到最近的重拍，终点按目标时长重算（[规格的默认规则](../specs/montage-center.md#本稿补充的默认规则)）。
- 试听：浏览器能直接播的格式按 Range 送出副本；ALAC、APE、WMA 一类浏览器播不了的，照音乐功能的做法，经 FFmpeg Worker 的 `audio` 解码成 FLAC 流送出（音乐功能的 `Transcode` 标记）。
- 起点记在 `projects.song_start`，任务开始时进快照。
- 没找到副歌不算不可用：按响度选段，并提示“没找到副歌，请试听确认”。

**不可用**：下面几种情况这首歌标为不可用，配乐页写明原因，可以换一首。
- 解码失败；
- 有效小节少于 16 个；
- 按目标 90 秒选不出 30–150 秒的段落；
- 最长的槽超过 12 秒，这是 `segment` 的上限。

**配乐探针（P2）**：已在开发机上做完，结果见上一段与[配乐研究](https://github.com/zhongwater123/A-NAS/pull/546)；它的三项门槛不再决定首版做法，只在退回 beat_this 时用。

**测试用的歌**：那 10 首在开发机 C 的 `~/Music/QQ音乐`，共 306 MB，其中 9 首的副歌已由用户逐首听过确认（[第六版研究](https://github.com/zhongwater123/A-NAS/blob/d83340e/docs/research/video-montage-v6.md)的“副歌识别”一节）。它们只做探针的对照与内部测试，可以暂时放进实验 NAS 上测试账号的“音乐”文件夹；不进仓库，不随软件包发布。

## 规划

本节的接线按 [AI 助手 V1 转正](../decisions/2026-11-17-assistant-v1-cloud-model.md)后的布局写（2026-11-18 合并，M9 按它实施）：
- 助手的脚本在 `ai/assistant/`，装在 `/usr/lib/a-nas/assistant/`，沙箱里同一路径只读；
- 定时任务单元 `anas-assistant-cron@` 由 `scripts/lib/assistant-units.sh` 从对话实例的单元生成；
- 成员的 Hermes 设置由 Host Agent 写在 `/var/lib/a-nas-host/assistant/members/<账号>/`。

机制与转正前的设计相同：规划授权、定时任务单元里的一轮、固定的提示词模板、这一轮只开放 `montage`。

### MCP 服务器

- 新增 `ai/assistant/anas_montage_mcp.py`，配置照 `companion` 写全各个键：`trust: full`、`enabled: true`、`lazy: false`、`timeout: 120`、`tools.prompts/resources: false`（`ai/assistant/montage-mcp.yaml`）。
  - `untrusted` 不行：每次调用都要审批，而规划跑在定时任务单元里，`approvals.cron_mode` 是 `deny`，没有人能批准。
- 服务器名不加进任何 `platform_toolsets`。规划这一轮的任务建时带 `enabled_toolsets=["montage"]`：Hermes 让任务自己的工具组优先于 `platform_toolsets.cron`，点名了某个 MCP 服务器时不再把别的服务器并进来（[Hermes 探针](https://github.com/zhongwater123/A-NAS/pull/546)的结论 1）。成员自己的定时任务和对话都看不到剪辑工具，每次请求也不多带它们的 Schema。
- **规划这一轮只开放 `montage` 工具组**，是 P1 的必过项（评审人 2026-11-18），P1 已实测通过：模型只拿到 montage 的 4 个工具，提示它调用终端，终端没有执行。现在 `platform_toolsets.cron` 有文件、终端、代码执行、委派、看图、看视频、记忆、会话搜索、技能、待办、`anas` 与 `companion`。只开放 `montage` 有两个理由：
  - 剪辑服务被攻破时，借 brief 的结果诱导的是一个没人在场的模型；它不能有终端、文件与相册工具。
  - “只发文字、不发画面”要有保证：这一轮里不能还开着相册与看图的工具。
  - 成员自己的定时任务照旧用 `platform_toolsets.cron` 的全部工具。
- **这一轮结束后不跑后台复盘**，同样是 P1 的必过项，已实测通过：Hermes 给所有定时任务的运行都固定传 `skip_background_review=True`，不用另外设置（[Hermes 探针](https://github.com/zhongwater123/A-NAS/pull/546)的结论 2）。复盘手上有记忆、技能与只读的文件工具，测试里已经把诱导文字写进过记忆（[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 的后果），所以 Hermes 升级时要核对这一点没变（见“发起与结束”的升级核对）。
- **成员的记忆会随这一轮发给模型**：Hermes 对定时任务固定传 `skip_memory=False`，成员 `MEMORY.md` 的内容进这一轮的系统提示，按任务关不掉（[Hermes 探针](https://github.com/zhongwater123/A-NAS/pull/546)的结论 3）。这一轮没有 `memory` 工具，写不了记忆。和成员自己的定时任务、对话一样，用云端模型时这些内容会发给服务商；规格的“边界与失败”写明。
- 套接字：
  - systemd 的 `anas-montage-assistant.socket` 持有宿主机上的 `/run/a-nas-montage-assistant/assistant.sock`，权限 0666，所在目录只有 root 能进；
  - 只绑定进成员的定时任务单元 `anas-assistant-cron@<账号>`，在沙箱里是 `/run/anas-assistant-montage/montage.sock`；对话实例和整理单元不绑定；
  - 成员自己的定时任务也跑在这个单元里，同一个 UID 分不清调用来自哪一次运行。所以工具调用要带着进行中那一轮的 token，影响只限于本人的任务，答案还要过校验；
  - 照相册助手套接字的做法：套接字单元随软件包默认开启，tmpfiles 建沙箱里的挂载点，`assistant-units.sh` 只在定时任务单元的那段 sed 里加这一处绑定。
- 剪辑中心用 `SO_PEERCRED` 取得对端 UID，经 Host Agent 按 UID 认出在用的账号（相册助手套接字用的同一项查询，要为剪辑中心开启），只回答这位成员自己的任务。成员不必在线。
- 套接字上有两类调用：MCP 服务器的工具（下表），和回合脚本的 `stale`、`claim`、`started`、`abandon`、`end`（见“发起与结束”）。
- **只有定时任务单元有 montage 服务器**：托管策略的 `mcp_servers` 会整段复制进成员的配置。若对话实例也带上它，就会起这个子进程，而那里没有剪辑的套接字。P1 实测这时它列零个工具，对话照常，但每个在线实例多占约 78 MiB（[Hermes 探针](https://github.com/zhongwater123/A-NAS/pull/546)的结论 6）。所以定时任务单元单独一份：
  - 软件包另装一份托管策略 `managed-cron/`，就是原托管策略加上 montage 服务器；
  - Host Agent 按它另写成员的 `config-cron.yaml`；
  - 定时任务单元的 `HERMES_MANAGED_DIR` 指向 `managed-cron`，成员配置的绑定也换成 `config-cron.yaml`；
  - 对话实例与整理单元两样都没有。
- 沙箱里的终端用的也是同一个 UID，所以套接字限制请求大小（80 KiB）与频率（每位成员每秒 5 次，突发 20 次）。

| 工具 | 输入 | 输出 |
|---|---|---|
| `montage_brief`（`readOnlyHint`） | `jobId`、`attemptToken` | 第一步的数据（卡片目录、音乐段落、成员的要求）与这一轮的精确 Schema |
| `montage_submit_outline` | `jobId`、`attemptToken`、大纲 | `{status: "accepted"}`；或 `{status: "rejected", reasons}`，让模型改了再交 |
| `montage_shots_brief`（`readOnlyHint`） | `jobId`、`attemptToken` | 第二步的数据（按收下的大纲生成的画面与可选节拍）与精确 Schema；大纲还没收下时回答“先交大纲” |
| `montage_submit_shots` | `jobId`、`attemptToken`、镜头 | 同 `montage_submit_outline`；还有方案要规划时回答 `{status: "next_plan"}` |

- **一轮规划两个方案**：一次生成只用掉规划授权 5 轮中的 1 轮。
  - 工具作用于“当前方案”：第一个还没存进相册、第二步还没有答案的方案；按 `plan_answers` 判断，不看 `plans`，因为模型交完方案一时任务可能还没来得及取走。方案一的镜头收下、后面还有方案时回答 `next_plan`，同一次调用重试也得到同样的回答；模板要求模型回到 `montage_brief` 做下一版。
  - 方案二的输入键：第一步是 `planKey(方案一第一步的键, 2)`，第二步仍是 `shotsKey(本方案第一步的键, 本方案的大纲)`。任务的步骤与工具用同一对函数，所以答案存进 `plan_answers` 的键就是步骤要找的键。
  - brief：第一步的 brief 两个方案相同，存在方案一的键下，只做一次；第二步的按各自的键存。方案二的 `montage_brief` 在返回前另拼上 `plan` 与 `earlierOutlines`（方案一收下的大纲），让它换一种讲法；这两项不存进 `plan_briefs`。
  - 一轮的身份只记方案一第一步的键（`rounds.input_key`），领取时也按它连接 `steps`。
  - 这一轮在方案二交齐之前结束：方案一照常往下做、存进相册，方案二进入 `needs_input(vnas_unknown)`，由成员决定是否重新规划，不自动再开一轮（#514：模型可能已经调用过）。

**规则、数据与 Schema 分开**
- 第八版的提示词把规则和数据写在一起，Schema 又按每个任务动态生成：卡片 ID 是枚举，段落数固定，第二步的画面和可选节拍取决于第一步的答案。交给 VNAS 时拆成三样：
  - **规则**：与答案无关的说明，即第八版两步的 system 文字、体裁规则、开头结尾与音乐结构的规则，写进这次运行的提示词，一开始就给出。Hermes 把所有 MCP 工具的结果都包成“不可信数据”交给模型，规则放在工具结果里会被当成数据。
  - **数据**：卡片目录、音乐段落、成员的要求、第二步的画面与节拍，由两个 brief 工具在需要时返回。
  - **精确 Schema**：这一轮的枚举与数量限制也由 brief 工具返回。submit 工具的 inputSchema 只写通用结构，精确校验在剪辑中心做。
- **规则是固定的模板**：源文件在 `ai/anas_montage/prompts/`，按版本命名（现在是 `plan-v2.md`，一轮规划两个方案；第一次读 brief 就带着 `plan` 与 `earlierOutlines` 时，只做数据指明的这一版）；软件包把它装进 `/usr/lib/a-nas/assistant/montage-prompts/`，沙箱里只读。回合脚本按领取回答里的模板版本选取，只填进经过校验的几项：这一轮的编号与 token（十六进制）、目标秒数（整数）。所以剪辑中心决定不了提示词；它被攻破时还能经工具结果诱导模型，剩下的风险见“规划授权”。
- 拆分改变了第八版的提示词，所以规划不在黄金比对之内，靠固定样本上的人工对比验收（规格的“剪辑行为迁移”）。
- 计算 Worker 的 `plan` 输出数据、Schema 与模板版本。两步的规则一开始就给模型（第二步的 system 文字只依赖目标时长，不依赖第一步的答案）；第一步的数据由 `montage_brief` 返回，第二步的数据等大纲收下后再生成。
- 定时任务的运行里没有人回答审批，需要确认的操作一律拒绝，`clarify` 也不给。提示词仍要求不调用终端与代码、不委派，也不把描述写进记忆。

**校验错误怎样退回**：服务器用低层的 `Server` 实现，参数在工具里自己校验，不合格时返回普通结果 `status: "rejected"`，不用 `isError`。原因是 Hermes 把 `isError` 计入熔断：连续 3 次后，这个服务器 60 秒内的调用一律不可达（[Hermes 研究](../research/hermes-agent-runtime.md)）。MCP 规范本身推荐用 `isError` 让模型自我纠正，这里不用只是因为这个熔断。

### 规划授权

成员不在线时，规划仍要在成员的 VNAS 里调用模型。[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 决定 10 的会话闸门不让后台服务凭自己的身份驱动成员的助手；定时任务能在成员离线时运行，是因为任务是成员自己建的。所以后台规划也要成员在场时留下授权，思路与“项目授权”相同。

- **登记**：成员做规格默认规则里列出的几项操作（开始、重新规划、去掉素材、重试分析、重剪，之后都可能要规划）时，anas-api 在转发之前，带着这次请求的会话调用文件代理的新操作 `assistant_plan`。
  - 文件代理照“立即运行”（`assistant_run`）核对会话有效、账号在用；另外核对这个账号有 VNAS，即 Host Agent 为它准备的 `/var/lib/a-nas-host/assistant/tokens/<账号>.env` 存在，和唤醒服务认定“有助手”的办法相同。`assistant_run` 本身只查账号名的格式，不查这一项。
  - 核对通过就写 `/var/lib/a-nas/assistant-plans/<账号>`，内容是签发时间与轮数上限 5。只有文件代理写它，再次登记时整个替换。
  - `assistant_plan` 用正常的回答区分三种结果：已登记、设备没有开启 VNAS、这个账号没有 VNAS。错误码 `unavailable` 只留给临时故障（会话目录读不了、记录写不进）。`assistant_run` 把“设备没开”也答成 `unavailable`，这里不照搬，否则 anas-api 分不清。

    | 文件代理的回答 | anas-api 怎么做 | 剪辑中心 |
    |---|---|---|
    | 已登记 | 转发，带 `X-A-NAS-Assistant: on` | 把这个时间记进 `montage.db` |
    | 设备没有开启 VNAS、这个账号没有 VNAS | 转发，带 `X-A-NAS-Assistant: off` | 任务照常建，页面写明“VNAS 没有开启”；开启后点“重新规划” |
    | `unavailable`，或连不上文件代理 | 回答 503 `assistant_unavailable`，不转发 | 页面提示稍后再试 |

  - 转发代理只去掉 Cookie、CSRF 与 Authorization 头，别的头照样转发（`internal/serviceproxy`）。所以 anas-api 在 `/api/v1/montage` 的挂载层对所有请求都先去掉 `X-A-NAS-Assistant`，只在上面几项请求里自己设置；剪辑中心也只在这几项请求上读它。
- **按成员，不按任务**：同一成员的几个任务共用这 5 轮。一个任务通常只用 1 轮（两步在同一次运行里交完）。
- **计数**：唤醒服务在自己的 `StateDirectory` 里，按签发时间记下这次签发已经被领取了几轮（见“发起与结束”第 2 步）。授权文件只有文件代理写、计数只有唤醒服务写，不会有两个进程改同一个文件。签发超过 7 天，或已经领取满 5 轮，就不再启动。
- **谁能用**：只有 root 的唤醒服务读授权文件，剪辑中心读不到也改不了它。账号停用后，唤醒服务照它现有的检查不再启动这个账号的单元。
- **被攻破时**：
  - 剪辑服务只能让已授权的成员在有效期内跑至多 5 轮规划，没有授权的成员不受影响。
  - 它决定不了提示词，只能经 brief 的结果送数据。这一轮只开放 `montage`（见“MCP 服务器”），模型即使被诱导，也碰不到文件、终端、相册与记忆，能做的只是交回不合格的答案；submit 的参数只收卡片编号、段落序号一类枚举和整数，文字字段限长（见“校验”）。
  - 这一轮结束后也不跑后台复盘（见“MCP 服务器”），诱导的内容写不进记忆与技能。
  - 产品服务只能为正在登录的成员登记授权，与 ADR 0024 决定 10 的威胁模型相同。

### 发起与结束

按[决定记录](../decisions/2026-11-17-montage-center-first-version.md)第 10 项，规划由 NAS 在后台发起。它照定时任务的做法，跑在成员自己的定时任务单元 `anas-assistant-cron@<账号>` 里（[gateway 运行方式的决定](../decisions/2026-11-10-hermes-gateway-runtime.md)）：同一个沙箱、同一条通往模型的桥，没有实例凭据，成员不登录也能跑。

1. **留记号**：任务走到规划时进入 `waiting(vnas)`，剪辑中心写空文件 `/run/a-nas-montage/plans/<账号>`，表示这位成员有任务在等规划。
   - 这一轮被领取、作废后还在退避、或这位成员没有任务在等规划时，删掉记号。
   - 记号在运行目录里，服务重启后没了，所以启动时按 `jobs` 表重建。
   - 剪辑中心把最近一次经过登记的请求时间记在 `montage.db` 里（见“规划授权”）。那次登记已超过 7 天时不留记号，页面直接给“重新规划”。
2. **唤醒**：root 的唤醒服务（`cron-waker.py`）每 30 秒看一次这个目录。
   - **判断单元在运行**：定时任务单元是 `Type=oneshot`，整轮运行期间状态都是 `activating`，`systemctl is-active` 不算它在运行。所以看 `ActiveState`：`activating`、`active`、`deactivating` 都算在运行。
   - **全机一轮**：它为规划启动的那一次运行还没结束时，不再发起规划，因为一轮要起一个 gateway（内存见“风险”）。放出后第一次看到单元在运行时记下它的 `InvocationID`；单元不在运行了，或 `InvocationID` 变了（成员的“立即运行”又启动了一次），名额就结束，不会被同一位成员一直占着。这一次运行里可能还在跑成员自己的定时任务，最长 1 小时（`TimeoutStartSec=1h`），后面的成员要等它结束。
   - **挑成员**：按记号的先后，找第一个满足全部条件的成员：账号名合法、账号在用、有 VNAS（照它对定时任务的检查）；授权签发不到 7 天、领取不满 5 轮；这位成员的单元没在运行；不在退避中。不满足的只是跳过，不挡后面的成员。
   - **放记号并启动**：在自己另一个运行目录里放 `/run/anas-assistant-montage-plans/<账号>`，再启动这位成员的单元。目录不和唤醒服务记各账号的文件放在一起，因为账号名可能就叫 `plans`。启动失败时删掉这个文件。
   - **计数**：放文件之前，先在自己的 `StateDirectory` 里记下账号、签发时间与放出时间，并把这次签发的计数加一。单元已经停了而文件还在（这次启动并进了别的一轮，或单元没起来），就删掉文件，退回这一轮。唤醒服务重启时运行目录会被清掉，放出了却不见的文件按用掉计：宁可少用一轮，也不超过 5 轮。
   - **退避**：单元结束后，剪辑中心的记号还在，说明这一轮没有领取（gateway 起不来、已有别人的 gateway）。这位成员按 2、4、8……分钟退避，最长 1 小时。
3. **领取**：`run-requests.sh take` 以 root 运行，除了“立即运行”的请求，也把唤醒服务放的文件移进这一轮的目录，名为 `montage-plan`。回合脚本（`cron_round.py` 加一段，或拆出 `montage_round.py`）：
   - **每一轮都先清理，而且在启动 gateway 之前**：gateway 就绪几秒后，它自己的调度器就会运行到期的任务（[研究](../research/hermes-agent-runtime.md#2026-11-11-在沙箱里验证-gateway)），而中途被杀的运行留下的任务很可能还是到期的。
     - 不管有没有 `montage-plan`，都调 `stale`，取剪辑中心记下的这位成员已领取、却没报告 `end` 的那些轮；删掉名字是“剪辑规划 <这一轮的编号>”的任务和它们的运行，再报告已清理，剪辑中心就不再列出它们。
     - `stale` 调不通时，先把所有名字是“剪辑规划 <编号>”的任务暂停，再启动 gateway。暂停可以恢复，下一轮再按 `stale` 删。
     - 删任务之前先删它的运行会话，做法见第 5 步。
   - **只在这一轮的目录里有 `montage-plan` 时领取**：成员自己的到期任务和“立即运行”启动的轮次不领取，所以 5 轮的上限和全机一轮不会被绕过。
   - **只在自己启动 gateway 时领取**：已有别人的 gateway 服务这个主目录（API Server 通道）时不领取，因为那个 gateway 连不到剪辑的套接字。
   - gateway 就绪后调 `claim`。剪辑中心只在这位成员的记号还在时接受：原子地为最早的等规划任务建一轮，回答 `roundId`、`attemptToken`、模板版本与目标秒数，并删掉记号。
4. **交给 Hermes**：
   - **先查模型**：经通往模型的桥 `GET /v1/models`，不调用模型、不消耗 token（P1 用替身模型实测回答 200；转正后的模型网关放行这条，见 `ai/assistant/gateway.py`）。设备上没有密钥或桥不通时，调 `abandon`，原因写明，不往下走。沙箱看不到密钥，所以只能这样查。
   - **建任务**：用 Hermes 自己的 `create_job(paused=True, enabled_toolsets=["montage"])` 建成暂停的任务，定时任务的一轮不会运行它（P1 实测）。名字是“剪辑规划 <这一轮的编号>”，提示词按模板拼出。Hermes 在运行开始时把任务的下次运行往后推一个周期，单元中途被杀后它一个周期后还会再跑（[Hermes 探针](https://github.com/zhongwater123/A-NAS/pull/546)的结论 5），所以第 3 步的清理是必需的；M9 定为周期一天、建时暂停、由回合脚本触发，不用 `repeat=1`：这一轮结束时回合脚本连同运行会话删掉它，被杀时留到下一轮的清理；`repeat=1` 跑完会被 Hermes 自己删掉，运行会话却留下，清理还是少不了。
   - 建任务、删任务与删会话的脚本以成员身份用 `python -P` 运行：助手目录里有模型网关的 `gateway.py`，脚本目录排在模块搜索路径最前面时会盖住 Hermes 的 `gateway` 包。规划这部分是 `montage_round.py`，由 `cron_round.py` 把脚本目录追加到搜索路径末尾后导入。模板要求最后只回答 `[SILENT]`，这样 A-NAS 的任务提示把这次运行记为已读，不弹出（`tasknotices.go`）。
   - **记 `started`**：带上任务编号。这一轮已作废或已结束时剪辑中心拒绝，脚本删掉任务、不再往下走；记下 `started` 之后，剪辑中心不再接受 `abandon`。
   - **使任务到期**：用 `trigger_job`，和到期的定时任务一起由 `hermes cron tick` 交出去。先记 `started` 再使任务到期，所以没有 `started` 的一轮一定没调用过模型。
   - 运行期间，这条任务会出现在成员的“定时任务”里。不按名字隐藏：任务表在成员可写的目录里，对话里的 VNAS 也能建任务，按名字隐藏会让人建出成员看不见的定时任务。
5. **结束**：Hermes 的执行记录里不再有这次运行后，回合脚本删掉这次运行的会话和这个任务，再调 `end`，带上 Hermes 记下的结束状态与错误。
   - 删运行就是删会话。运行会话的编号是 `local-<哈希>`，按 `SessionDB.list_cron_job_runs` 找，先 `SessionDB.delete_session` 删会话、再 `remove_job` 删任务，两样都不留（[Hermes 探针](https://github.com/zhongwater123/A-NAS/pull/546)的结论 4）。
   - 单元中途被杀时，留下的任务在下一轮开始时按第 3 步清理。
6. **结果**：
   - **两步答案都收下**：任务往下走。
   - **领取后、`started` 之前失败**（没有密钥、模板版本对不上、单元被杀）：回合脚本调 `abandon`；没有报告时，领取 5 分钟后按作废处理。这一轮没有调用过模型，任务回到 `waiting(vnas)` 并写明原因；剪辑中心按 1、2、4……分钟退避（最长 30 分钟）后再留记号。
   - **`started` 之后没交齐**（`end` 已报告；或模型第一次调用工具起 30 分钟没交齐；或交给 Hermes 后 65 分钟一次工具都没调用）：进入 `needs_input(vnas_unknown)`，不自动再开一轮，因为模型可能已经调用过（#514）。成员点“重新规划”时重新登记授权、再留记号。
   - **等了 10 分钟还没被领取**（没有 VNAS、授权用完或过期、单元起不来、成员自己的定时任务占着单元、别人的规划在跑）：任务仍是 `waiting(vnas)`，页面写“VNAS 还没有接手”，并给出“重新规划”，点了会刷新授权。
7. **提示**：桌面外壳懒加载的 `MontageNotices` 照家庭中心的 `FamilyNotices` 做，只显示提示，不发起规划。内容见规格“本稿补充的默认规则”的桌面提示。

**VNAS 不可用**：账号或设备没有开启 VNAS、设备上没有密钥、gateway 起不来时，任务停在 `waiting(vnas)`，项目页和提示写明原因。按 [AI 助手 V1 的决定](../decisions/2026-11-17-assistant-v1-cloud-model.md)，VNAS 默认对全部成员开启，管理员填写密钥后可用（2026-11-18 合并）。

**取消**：剪辑中心把进行中的一轮记为已结束，之后交来的答案一律回答“任务已取消”，提示词要求见到这句就结束运行。不强行停掉 gateway，因为里面可能还在跑成员自己的定时任务。Hermes 的 `remove_job` 删除正在运行的任务时会中止这次运行（读源码得知），M9 实测后可以由回合脚本在下一次 `stale` 时直接删掉被取消的这一轮。

### 去重与结果关联

- `attemptToken` 在领取这一轮时生成，记进 `rounds`；回合脚本把它填进提示词，模型调工具时带上。没有进行中的一轮时，`brief` 和 `submit` 都拒绝。
- 每一步只收第一份通过校验的答案，后来的提交回答“已收下”，不覆盖。
- 一轮结束（报告结束、作废或超时）后，带这一轮 token 的答案回答“这一轮已结束”。
- 剪辑中心在等待规划时重启：一轮进行中就不另开新的一轮，直到它结束或超时。这样不会重复调用模型，满足 #514“调用成功但本地提交中断，不能无条件再次触发模型调用”的要求。
- token 只用来关联这一轮，不是凭据。同一成员在这个单元里的其他运行也能交答案，影响只限于本人的任务，而且答案要过校验。

**用量**
- 2026-11-17 用开发机直连 Claude 重剪 5 组：每版两步合计 0.25–0.43 美元，第一步输入约 4.8–18.5 千 token，第二步约 6.9–15.2 千 token。
- 经 VNAS 后，每次请求还要带 Hermes 的系统提示（约 13,000 字符）、成员的记忆、montage 的 4 个工具定义和逐轮累积的上下文。P1 用的是替身模型，M9 在实验 NAS 上按 DeepSeek 实测。

**和第八版的差异**：第八版用独立的 system prompt 直接调用 Claude；改由 VNAS 规划后，模型前面多了 Hermes 的系统提示、定时任务的运行说明和工具调用，答案质量可能不同。验收时在固定样本上对比。

## 持久任务

剪辑中心在自己的 SQLite 里实现任务：`jobs` 表加 `steps` 表。做法借鉴三处：
- 相册的 `jobs` 表；
- goqite 的租约：领取时设可见性超时，超时后重新投递；
- DBOS 的“每步结果先落库再往下走”：中断的那一步会重跑，所以每一步都要幂等。

不引入 River，它的 SQLite 驱动官方仍标为实验性；也不引入 Temporal 一类外部服务。

**状态**：`queued` → `running`，之后进入下面之一：
- `done`、`failed`、`cancelled`；
- `waiting`，原因 `analysis`（相册分析未完成，或本地模型不可用）、`asset_trashed`、`vnas`；
- `needs_input`，原因：

| 原因 | 什么时候 |
|---|---|
| `asset_missing` | 素材被永久删除，或成员已经看不到 |
| `analysis_failed` | 相册对某段素材的分析失败（不是 I/O 类），等用户点“重试分析”或去掉这段素材 |
| `no_usable_footage` | 没有可用画面 |
| `music_invalid` | 歌曲不合格 |
| `vnas_unknown` | 规划没有交回 |
| `plan_invalid` | 规划连续 3 次不合格 |
| `storage_full` | 渲染或存回相册时空间不足 |

**执行**
- 一个调度协程，同一时刻只执行一个任务的一个步骤。处于 `waiting` 的任务不占调度，别的任务照常执行。
- 领取任务时写 `lease_until = now + 5 min`，执行中每 30 秒续一次。
- 服务只有一个实例，启动时直接把 `running` 的任务收回到 `queued`，不等租约过期（相册 `RecoverJobs` 也是这样）。收回后从第一个没完成的步骤接着做。

**步骤与输入摘要**：每一步先算 `input_key`，同一个任务里 `steps` 有同样的键就直接复用，所以重启和重试不会重做已完成的步骤。每一步的键都包含上游步骤的键，上游变了，下游跟着重做。跨任务复用的只有相册的分析与配乐的分析；每次“生成”都重新规划，所以再生成一版可能得到不同的剪法。

| 步骤 | `input_key` 的组成 |
|---|---|
| 分析 | `job_assets` 里排序后的 `objectId` 集合，与相册返回的分析身份 |
| 目录 | 分析步骤的键、`catalog` 的算法版本、选项 |
| 歌曲 | 副本的 SHA-256、allin1 与 Demucs 权重的摘要、`music` 的算法版本、目标秒数、起点 |
| 第一步规划 | 目录步骤的键、要求、顺序、歌曲步骤的键、提示词版本 |
| 第二步规划 | 第一步规划的键与答案的摘要、提示词版本 |
| 填槽 | 第二步规划的键与答案的摘要、`fill` 的算法版本 |
| 渲染 | 时间线的摘要、渲染预设版本、副本的 SHA-256 |
| 存回 | `versionKey` = 任务 ID 加渲染步骤的 `input_key`。重渲染的输出不必逐字节相同，存回也不会重复 |

**两个方案**：分析、目录、歌曲三步共用；之后每个方案各有规划两步、填槽、渲染、存回五步，方案一的步骤名照旧，方案二的加 `-2`（`pick-2` … `save-2`）。步骤表由 `steps()` 一处定义，步骤名与进度都从它得出。
- 方案数记在任务上（`jobs.plans`），开始时取 `montage.Options.Plans`（产品默认 2，最多 2）；升级前开始的任务是 1。
- 方案二的规划键见“规划”的 `planKey`；渲染键另加方案号，所以和方案一的时间线相同也各存一条。
- 方案一存回后任务接着做方案二，最后一个方案存回后任务才结束。每个方案的成片存回后就从任务目录删掉，已存的方案不再渲染。
- 存回栅栏（`jobs.save_started`）每个方案各用一次：不是最后一个方案时，版本记下就放开，任务继续；重启后重放的是第一个还没有版本的方案。
- **已存的方案是定局**：方案一存过之后“去掉这些继续”，方案一的版本和相册里的成片都不动，只按剩下的素材重做没存的方案；这时方案二的大纲数据不带方案一的大纲，因为它是按旧目录写的。

- 去掉素材后，目录的键变了，所以没存的方案已收下的规划作废（`plans.voided_at`），两步都重新规划，不会复用含已删素材卡片的旧目录。
- 任务开始时把素材集合记进 `job_assets`，把歌、起点、要求与是否按拍摄时间记进 `jobs.settings_json`。各步骤的键只从这份快照取，所以任务进行中改项目设置或追加素材，只影响下一次“生成”，重启后也不会因此重做已收下的规划。

**失败分类**

| 类别 | 例子 | 处理 |
|---|---|---|
| 等待 | 相册分析未完成或被准入推迟；FFmpeg 在排队 | 不计次数。相册分析未完成时为 `waiting(analysis)`；FFmpeg 排队时步骤显示“排队中” |
| 可重试故障 | Worker 退出、超时、OOM、I/O 错误 | 最多 3 次（首次加两次重试，间隔 1、2 分钟），用完后 `failed` |
| 需要修改输入 | 素材缺失、分析失败、没有可用画面、歌曲不合格、空间不足 | `needs_input`，等用户处理 |
| VNAS 结果未知 | 这一轮结束了没有交回规划 | `needs_input(vnas_unknown)`，见“规划” |

**取消**
- 置 `cancel_requested`；排队中的任务立即变成 `cancelled`。
- 执行中的任务：关闭正在进行的 FFmpeg 连接（Worker 在对端挂断时结束 FFmpeg），之后不再领取新步骤。计算 Worker 的操作在秒级，当前操作结束后生效。
- 相册的分析不因取消而停：它可以跨项目复用。
- 存回相册前再检查一次：已取消就丢弃成片，不调用 `saveFilm`。
- 进行中的一轮按“规划”一节结束。

## 校验

| 对象 | 规则 | 不通过 |
|---|---|---|
| 素材范围 | 项目的素材集合非空；只用 `project_assets` 里的视频；选中为空时不会退回“全部” | 请求回答 400 |
| 歌曲 | 见“配乐”的“不可用” | 这首歌不可选，写明原因 |
| 第一步答案 | 符合 Schema；段落数与音乐段落一致；引用的卡片都在本次目录里；文字字段（例如素材里找不到的内容）每项不超过 80 字，合计不超过 2 KiB | `rejected` 退回给 VNAS 改；连续 3 次不合格则 `needs_input(plan_invalid)` |
| 第二步答案 | 符合 Schema；节拍属于所引用的卡片 | 同上 |
| 时间线 | 每个镜头的起止有限、起点小于终点、不超出原片时长；引用属于本项目；槽位不重叠；各镜头帧数之和等于音乐选段的帧数；没有空槽；镜头不超过 100 个（`assemble` 上限的前提） | `failed`，不渲染，不报成功 |
| 要求 | 不超过默认规则的字数上限 | 请求回答 400 |
| 成片 | 时长与时间线一致（误差 ≤ 0.1 秒）；1920×1080；30 帧；有音轨 | `failed`，丢弃文件 |

## 不支持的要求

“一句话”提交前，用一张固定的词表做简单匹配。命中时在输入框下面提示一下，不拦着继续。词表：
- 竖屏、9:16、方形；
- 几秒、几分钟；
- 两首歌、换歌；
- 字幕；
- 原声、配音。

提示的意思是：首版固定横屏 1920×1080，时长跟着音乐走（约 90 秒），单曲配乐，不加字幕、不保留原声。第八版的规划答案本来就有 `unmet` 字段（素材里找不到的内容），成片页的显示见规格。

## 接口

新增 `api/paths/montage.yaml`，挂在 `/api/v1/montage`。约定：
- 错误统一用 `ProductError`；
- 创建类请求在请求体里带 `requestKey`，按成员限定唯一，沿用家庭中心的做法；
- 长任务用“202 加轮询”，和应用中心安装、系统更新一致；
- 别人的项目与任务都回答 404，不暴露是否存在。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/projects` | 本人的项目，游标分页；每个项目带最多 6 段视频（`previewAssetIds`，均匀取）给缩略图和主页背景用 |
| POST | `/projects` | `{requestKey, name, assetIds}`：建批次与项目，登记授权并请相册开始分析；一次最多 500 个资产 |
| GET、PATCH、DELETE | `/projects/{id}` | 详情（素材状态、分析进度、最近的任务 `job`、各任务各方案的版本与成片资产）；改名与设置（起点、要求、按拍摄时间）；删除 |
| POST | `/projects/{id}/assets` | `{requestKey, assetIds}`：加入已有项目 |
| POST | `/projects/{id}/generate` | `{requestKey}`：回答 202 与任务；项目已有进行中的任务时回答 409 |
| GET | `/jobs/{id}` | 状态、步骤、进度、排队位置、等待原因 |
| POST | `/jobs/{id}/cancel` | 取消 |
| POST | `/jobs/{id}/drop-assets` | `{assetIds}`：从这个任务去掉缺失或分析失败的素材后继续。同时改 `job_assets` 与 `project_assets`，并经 `remove` 从授权里去掉；去掉后素材为空时回答 409，只能取消 |
| POST | `/jobs/{id}/retry-analysis` | 相册分析失败后重试 |
| POST | `/jobs/{id}/retry` | 空间不足时腾出空间后，从停下的步骤重试 |
| POST | `/jobs/{id}/replan` | 规划没有交回或不合格时重新规划 |
| GET | `/notices` | 桌面提示：等待规划与正在规划的任务、需要处理的任务、刚存进相册的成片 |
| PUT | `/projects/{id}/song` | `{trackId}`：从音乐库复制这首歌并开始分析（见“配乐”） |
| GET | `/projects/{id}/song`、`/projects/{id}/song/audio` | 分析进度、节拍、段落、副歌与默认选段，以及复制自音乐库的哪一首（`trackId`）；试听副本，支持 Range |
| GET | `/hero`、`/hero/poster` | 设备上的主页背景片与封面（剪辑中心数据目录下的 `hero/hero.mp4`、`hero.jpg`），不属于软件；没有时回答 404，页面改为轮播成员自己的视频 |
| POST | `/versions/{id}/confirm` | 确认这一版（#596，以下各行见“出片后的二次编辑”） |
| POST | `/versions/{id}/recut` | `{requestKey, change}`：回答 202 与 `recut` 任务；项目已有进行中的任务时回答 409 |
| GET | `/versions/{id}/outline` | 这一版成片本身的规划与镜头，给选版页的版本卡片用：只读，不建修改稿，没存的修改不在里面。`unmet` 是规划时没找到的内容 |
| GET | `/versions/{id}/draft` | 这一版的修改稿，没有就建：音乐段落与槽、每镜的节拍、锁定、粗预览用的片段、选段起点、配乐要不要转码（`songTranscode`）、修订号、能否撤销 |
| POST | `/drafts/{id}/ops` | `{baseRevision, op}`：`replace`、`move`、`delete`、`lock`、`unlock`、`shuffle`、`revert`、`reset`、`undo`。修订号对不上回答 409 `draft_conflict`；违反规则回答 422 `edit_refused`，`message` 是给成员看的原因；重洗要搜索，搜不了回答 503 `search_unavailable`，分析模型换了回答 409 `search_model_changed` |
| GET | `/drafts/{id}/candidates` | `?shot=&q=&size=&scene=&landscape=`：候选节拍与场景的列表，`shot` 是镜号（页面手上有的是镜号）；搜索的错误码同上 |
| GET | `/drafts/{id}/song` | 粗预览用的配乐副本：浏览器能播的支持 Range；不能播的即时解码成 FLAC，不能跳转，用 `?start=` 指定从哪一秒起 |
| POST | `/drafts/{id}/save` | `{requestKey}`：回答 202 与 `edit` 任务，按当前修订存为新版本；项目已有进行中的任务时回答 409 |
| GET | `/versions/{id}/shots`、`/drafts/{id}/shots` | 镜头清单（CSV） |
| GET | `/projects/{id}/frames/{assetId}` | `?t=` 毫秒：缩略图 |

成片是相册资产，播放与下载都走相册现有的接口。

规划的领取与报告（`claim`、`started`、`abandon`、`end`）只在剪辑的助手套接字上，浏览器调不到。`generate`、`replan`、`drop-assets`、`retry-analysis` 与 `recut`（#596）这五条由 anas-api 先经文件代理登记规划授权再转发（见“规划授权”），登记不了时回答 503 `assistant_unavailable`，请求不转发；`PUT /projects/{id}/song` 由 anas-api 经文件代理读出歌曲再交给剪辑中心（见“配乐”）。所以这六条在 anas-api 里有自己的处理，不只是透传。

## 网页

- **登记**：在 `web/src/desktop/apps.tsx` 登记 `montage`，标题“剪辑中心”。服务端开关打开后显示入口，不随 VNAS 是否可用而隐藏（#514：VNAS 不可用时上传、浏览和播放仍要能用）。
- **加载**：窗口代码按窗口懒加载，`MontageNotices` 在桌面外壳里懒加载，都不计入首屏 JS 预算。
- **页面**：照第八版原型（[`ai/montage_v8/web/index.html`](https://github.com/zhongwater123/A-NAS/blob/feat/video-montage-v5/ai/montage_v8/web/index.html)，在 #454 的分支上，只作参考、不合入 main）的样子，文字按“只写事实和操作”从简。
  - 主页就能剪：标题区的背景先问设备有没有背景片（`/hero`），有就播它，没有就轮流播成员自己的三段视频（播放决策经 TanStack Query 按视频缓存）。下面像原型的文件夹那样列出项目，点一下选中、再点取消，箭头打开；选中项目的配乐、剪辑要求、开始剪辑、进度和两个方案都在主页。
  - 进度按“准备、方案一、方案二”三组并排；只有任务进行中才写“可以关掉窗口”。
  - 两条成片并排，都等成员点播放，不自动播放（相册 `VideoPlayer` 的 `autoPlay` 传 `false`）；更早的版本也能播放。方案一存进相册后马上显示，不等方案二。
  - 失败的任务只显示服务写给成员看的原因，其余诊断信息不上页面，只写“没有剪成”。
  - 项目页列出它的视频，可以追加；选视频用相册的 `PhotoPicker`，只列视频，可选本人或共享图库。配乐与剪辑要求各是一页，返回打开它的地方。
  - **背景片**：`/hero` 把设备上的同一个文件给每一位登录成员看，不只是放它的人；2026-11-19 用户决定保留，作为整台设备的主页背景。没有放背景片时，才轮播成员自己的视频。片子不进仓库与软件包，放法见[演示手册](../runbooks/demo-montage-center.md)。
- **上传**：复用相册的上传队列 `web/src/photos/uploads.tsx`（并行 2 个、接入传输中心、可重试、处理实况照片），目标是本人的个人图库，按 `LibraryPicker` 的规则找出个人图库。
  - 队列跑完后列出成功与失败，失败的可以重试；只用成功的资产建项目。
  - 单独上传的 MOV 如果补进了图库里在等它的实况照片，上传接口不返回新资产，所以它不进项目（页面行为见规格）。
- **数据获取**：引入 TanStack Query（[#271](https://github.com/zhongwater123/A-NAS/issues/271)），放在懒加载的窗口代码里。执行中每 2 秒轮询一次，窗口最小化或不可见时停止（`shown.ts` 跟随窗口外框的 `hidden`）。创建类请求的 `requestKey` 用 `web/src/api/requestKey`，局域网 HTTP 下没有 `crypto.randomUUID()` 也能用。
- **播放**：成片是相册视频，直接复用 `VideoPlayer`。
- **选版页与修改页**（#596）：照规格的[出片后的二次编辑](../specs/montage-center.md#出片后的二次编辑)。拖动用成熟的库（例如 dnd-kit），不手写；`VideoPlayer` 加起止时间，粗预览与试看共用；修改页的代码随窗口懒加载。修订号冲突时重新读取修改稿。
- **提示**：主线没有通用的桌面通知中心，剪辑的提示照 `FamilyNotices` 的做法放在桌面外壳里。

## 出片后的二次编辑

确认一版、修改镜头与一句话重剪（[#596](https://github.com/zhongwater123/A-NAS/issues/596)）。为什么这样做见 [ADR 0039](../adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md)，行为与界面以规格的[出片后的二次编辑](../specs/montage-center.md#出片后的二次编辑)为准，分票见 ADR 的“交付”。在首版闭环（M10）之后实施。

**版本的时间线**：二次编辑都从一个版本出发，所以存回时把这一版的时间线（`fill` 的 `clips`，连同段落、画面与小节槽的划分）写进 `versions.timeline_json`，与写版本行在同一个事务里。之前的版本没有这一列，第一次打开修改页时从它的任务的填槽步骤结果补上；步骤结果也没有时，这一版只能播放，不能修改或重剪。

**修改稿**
- `GET /versions/{id}/draft` 没有就按版本建一份：从 `timeline_json` 读出音乐段落与槽，每个槽记它首段的节拍，修订号 0。槽的键是段落、画面与小节槽的序号，和 `fill` 的嵌套相同。
- `POST /drafts/{id}/ops` 在一个事务里：比对 `baseRevision`，对不上回答 409 `draft_conflict`；按规格“修改的规则”校验，不成立回答 422 `edit_refused`；写一行 `draft_revisions`、更新 `drafts`；删掉最近 100 个之外、又不在撤销链上的修订。回答新的修改稿。
- 这一版的时间线读不出来时（见上），修改稿、候选与镜头清单都回答 409 `not_editable`。
- 打开修改稿时，逐个读卡片所在视频的分析：相册现在的窗口身份或维度与这一版任务的分析结果不同（换了预处理模型并重新分析过），回答 409 `analysis_changed`，这一版只能播放与重剪。相册里已经没有、或内容对象换了的视频留给用到它的步骤。版本卡片不做这一步：读分析要连同全部窗口向量，只在打开修改页时读一次。
- 校验只用剪辑中心手上的数据：卡片、节拍与描述来自相册的 `analysis`，横竖来自 `lookup` 的宽高（宽高比小于 16:9 算竖拍，与渲染判断是否垫模糊背景的规则相同），已用的节拍来自修改稿本身。换镜头、拖动时节拍够不够长按 `fill` 的放慢上限（2.5 倍）算；删除一镜时“卡片盖得住两段之和”按不放慢算。
- 这里的校验不知道 `fill` 实际会从卡片的哪一段取画面，所以存为新版本时还要核对一次，见“重新填槽”。
- 删除时两边都够长，调一次计算 Worker 的 `likeness` 选更像的一边。
- `shuffle` 先用一次 `searchWindows` 查全部没锁画面的 `query`，再按种子挑；种子写进修订，同一种子结果相同。
- 粗预览的片段随修改稿一起回答：没改过的槽取版本时间线里的镜头，改过的槽取所选节拍的起点与一个槽长。

**候选**：`GET /drafts/{id}/candidates?shot=&q=&size=&scene=&landscape=`
- 依次是同一张卡片里没用到的节拍、`shots` 步骤结果里这个画面的备用节拍、`searchWindows` 的结果。写了 `q` 时最多列 60 个；没写 `q` 时不调搜索，按拍摄先后列出筛选下的全部节拍。
- 窗口换算成节拍：节拍的分数取它覆盖的窗口里最高的一个（同原型），只用来排序，不设门槛。
- 景别取节拍描述的 `shot_size`，场景取卡片描述的 `scene`；筛选都在剪辑中心做，不进相册的检索。回答附场景的列表与个数。
- 每个候选标明够不够长、已用在第几镜、横竖。

**重新填槽**：`fill` 增加可选输入 `pins`：按成片顺序列出每个槽（段落、画面、起止）和它钉住的节拍，首尾相接铺满选段；一镜并了被删镜头的槽时，是一个更长的槽。
- 钉住的槽首段用这个节拍，不够长时照原规则补段（结尾最后一个槽照原规则放慢），无遮挡区间照旧经重入取帧。补段不用别的槽钉住的节拍。
- 带 `pins` 时槽的位置与长短照它，不再按拍摄先后重排：成员拖动过的顺序要保留。
- 前面的槽不盖过后面的槽钉住的关键帧。不然同一张卡片上相邻的两个节拍分别钉在相邻的槽里时，后一个会因为画面已经用过而被跳过。
- 卡片里实在没有地方时，这个槽由别的节拍填。剪辑中心校验时间线时逐槽核对：槽的起止与修改稿相同，槽里有它钉住的节拍。对不上或没填满，`edit` 任务失败，原因写明是第几镜、请成员换一个节拍后再存，不存出一条和修改稿不一样的成片。
- 不带 `pins` 时结果与现在逐个相同，第八版的黄金比对不变。

**任务的种类**：`jobs.kind` 决定 `steps()` 给出的步骤表。

| 种类 | 来源 | 步骤 | 版本键 |
|---|---|---|---|
| `generate` | 项目当前的设置 | 现在的步骤 | 任务 ID 加渲染键 |
| `edit` | 修改稿的一个修订：素材、歌、起点与顺序取来源版本的任务快照 | 查素材 → 带 `pins` 填槽 → 校验 → 渲染 → 存回 | 修改稿 ID、修订号加渲染键 |
| `recut` | 来源版本的任务快照，加这句话 | 分析 → 目录 → 歌曲 → 规划两步（一个方案）→ 填槽 → 渲染 → 存回 | 任务 ID 加渲染键 |

- 一个项目同一时刻只有一个没结束的任务：建任务时在同一个事务里查，已有就回答 409 `job_in_progress`。原来只有 `generate` 这样做，现在三种都是。
- `edit` 任务只看管各镜首段用到的视频。它们缺失或进了回收站时，照规格停下或暂停，`missing_json` 列出这些视频，页面据此标出用到它们的镜头；`drop-assets` 对 `edit` 任务回答 409，`recut` 任务照 `generate` 去掉后继续。
- 迁移回退：加 `kind` 的那一步，Down 自己删掉 `edit`、`recut` 任务名下的步骤、素材、轮次、规划和它们存下的版本的修改稿。回退时外键是关着的，级联不生效。
- `edit` 与 `recut` 任务不改项目的设置，也不改 `project_assets`。
- `edit` 任务填槽读窗口向量时，窗口身份或维度与来源版本的分析结果不同，直接失败，原因与 `analysis_changed` 相同，不渲染也不存回；`generate` 与 `recut` 任务照旧当作分析正在变化、按可重试故障处理。

**重剪的规划**
- `POST /versions/{id}/recut` 加进 `montageapi.PlanningRoutes`，成为第五条登记规划授权的请求，行为与另外四条相同（见“规划授权”）。
- brief 多两项：`base` 是来源版本收下的大纲与镜头，`edit` 版本取它所来自的规划，手动改的镜头不带；`change` 是这句话。`base` 取两步规划交回的原文，带各部分与各画面的衔接（`link`）；上一次没找到的内容（`unmet`）不带，由新的一轮自己写。
- 模板 `plan-v3.md` 在 `plan-v2.md` 上只加一条规则：从 `base` 出发，只改 `change` 要求的地方。第二步的数据照旧等大纲交回后才给。回合脚本同时认 v2 与 v3。
- 这句话照“一句话要求”限 200 字，也过“不支持的要求”的词表。

**缩略图**：`GET /projects/{id}/frames/{assetId}?t=<毫秒>`
- 资产要在这个项目的素材里；经 `open` 与 FFmpeg `frames` 取一帧 JPEG，长边 320。
- 用 `Interactive` 优先级：成员在页面上等着看。它不计入剪辑中心同一时刻最多 1 个的后台请求，另设并发上限，初值 4，R3 实测后定。
- 回答 `Cache-Control: private, max-age=31536000, immutable`：内容对象不可变，同一时刻的帧不会变。
- 不存盘，也不经过计算 Worker，所以不会一边占着 FFmpeg 槽位一边等计算 Worker。

**粗预览**
- `GET /drafts/{id}/song` 送出来源版本任务快照里的配乐副本，支持 Range；选段起点在修改稿的回答里。浏览器不能播的歌和配乐页的试听一样即时解码，不能跳转：修改稿用 `songTranscode` 说明，网页从预览起点用 `?start=` 请求，时钟从那里算。
- 原片用相册现有的视频播放。网页用两个 `<video>` 交替，下一段提前载入并跳到起点；以音乐的播放时间为准切换，不等跳转。
- 不渲染，不占 FFmpeg 的后台槽位；相册的转码播放与缩略图用 `Streaming`、`Interactive`，和别人的播放一起排队。

**版本卡片**：选版页每一版的缩略图条、剪辑思路、统计与没找到的内容读 `GET /versions/{id}/outline`，是成片本身的镜头。没找到的内容取两步规划答案的 `unmet`，按先后合并、去掉重复；改过的版本取它所来自的规划。不读修改稿：改过还没存的修改稿和上面那条成片不是一回事，光是看一眼选版页也不该建修改稿。

**确认这一版**：`POST /versions/{id}/confirm` 写 `projects.chosen_version_id`，可以改写；项目列表与主页优先用它。不删除任何成片（[决定记录](../decisions/2026-11-19-montage-edit-defaults.md)）。

**镜头清单**：`GET /versions/{id}/shots` 与 `GET /drafts/{id}/shots` 用 Go 的 `encoding/csv` 写，前面加 UTF-8 的 BOM，列见规格。

## 原型缺陷的修复

#514 列出的审计问题，在第八版代码里都找到了实例。每项单独提交，并补回归测试：

| 缺陷 | 第八版里的位置 | 修法 |
|---|---|---|
| 选中为空时用了整组素材 | `CutRequest.options`：`videos` 为空即 `None`，`None` 表示全部；选中的文件名一个都对不上时也会变空 | 素材范围只来自 `project_assets`，空集合拒绝 |
| 上传冲突误判 | `service/uploads.py`：同名同大小就当作传过了 | 上传交给相册的上传队列，剪辑中心不再自己存原片 |
| 输入变了仍复用旧结果 | `pipeline.reused` 只比对描述文件名 | 用上面的 `input_key` 和相册的分析身份 |
| 没填满的槽只写日志 | `pipeline.cut` 把 `short` 写进日志后照常渲染 | 时间线校验把空槽当作失败 |
| 任务卡在执行中 | 单线程执行，没有租约；某个调用卡住时整个线程被堵住 | 租约加心跳，每个外部调用都有时限 |
| 取消后仍成功 | 取消只在 `tell()` 处检查；模型调用和渲染期间不查，`cut` 完成后也不再查 | 终止 Worker 连接；存回相册前再检查一次 |

## 交付

按批次分别开 PR，直接进 main，不开长期 feature 分支。服务端开关在出片闭环完成前保持关闭，所以前几批合并后，用户既看不到入口，也调不到接口。相册、FFmpeg、AI 的扩展都是新增，不改变现有行为。

| 票 | 内容 | 依赖 | 不能拆开的义务 |
|---|---|---|---|
| P1（[#548](https://github.com/zhongwater123/A-NAS/issues/548)） | Hermes 探针（开发机，用 AI 助手 V1 转正时固定的 Hermes 提交；转正换了提交就重跑）。逐项验证：规划这一轮只开放 `montage` 工具组（必过）；这一轮结束后不跑后台复盘，或复盘也只有 `montage`（必过）；任务能建成不自己到期的、能触发、能连同运行的会话一起删；中途被杀的运行会不会被 Hermes 再次触发；运行里能调 montage 工具；拿得到结束状态；不调用模型就能查出没有密钥；Hermes 怎样对待零工具的服务器；内存峰值。2026-11-18 在开发机做完，两项必过都过了，见[Hermes 探针](https://github.com/zhongwater123/A-NAS/pull/546) | — | 结果写进 Hermes 研究文档；是接受 ADR 0038 决定 5 的前提。不通过时退回哪种做法，先问评审人 |
| P2（[#549](https://github.com/zhongwater123/A-NAS/issues/549)） | 配乐探针（开发机）：导出 beat_this 的 small 模型并与 PyTorch 对照；`beats`、`sections`、`music` 在 10 首歌上的结果对照门槛（见“配乐”）。2026-11-18 做完：节拍与重拍过了门槛，副歌 4/9 没过；同日用户决定改用 allin1，门槛只在退回 beat_this 时用 | — | 结果与门槛写进一份研究文档；不通过时怎么办，先问评审人 |
| M0（[#550](https://github.com/zhongwater123/A-NAS/issues/550)） | 本方案批准、ADR 0038 接受；修订相册视频规格、ADR 0011 决定 4、ADR 0016 决定 4、ADR 0024 决定 3、8 与 10、AI 助手规格的访问范围表与定时任务一节 | P1 | 规格与各 ADR 的修订一起提交 |
| M1（[#551](https://github.com/zhongwater123/A-NAS/issues/551)） | 服务骨架：账号、单元、子卷、会话查询、转发、开关、空的项目接口、隐藏的窗口 | M0 | “接线清单”全部各项同一个 PR |
| M2（[#552](https://github.com/zhongwater123/A-NAS/issues/552)） | 相册项目授权：套接字、登记、查状态、交出描述符、列出、撤销、授权身份、账号删除；`leak_test` 加上授权路径 | M0 | 授权套接字、Policy 复核与 `SO_PEERCRED` 测试同一个 PR |
| M3（[#553](https://github.com/zhongwater123/A-NAS/issues/553)） | 项目、批次、素材范围、追加、生命周期检查、启动对账 | M1、M2 | — |
| M4（[#554](https://github.com/zhongwater123/A-NAS/issues/554)） | FFmpeg Worker：`frames`、`segment`、`assemble`、`audio` 的 PCM 输出；`unixmsg` 支持多个描述符 | — | 协议、命令构造、上限与超时、准入、nice 与线程数、真实 FFmpeg 测试、总览文档同一个 PR |
| M5（[#555](https://github.com/zhongwater123/A-NAS/issues/555)） | 相册 `montage-windows/v1`：任务、续租、Catalog 表与迁移回滚、`window_outbox`、Qdrant 集合、按 `object` 删除、对账、保留、读取接口 | M2、M4 | 分析身份、失效、outbox 与[向量索引](vector-index.md)文档同一个 PR |
| M6（[#556](https://github.com/zhongwater123/A-NAS/issues/556)） | 剪辑计算 Worker：先原样迁移并做黄金比对，再逐项修缺陷；相册的 `montage-cards/v1` 与 `montage-quality/v1` | M5 | 原样迁移和修复分开提交；PR 写明样本位置与摘要 |
| M7（[#557](https://github.com/zhongwater123/A-NAS/issues/557)） | AI Worker 的 `describe_frames` 与多描述符的收取；相册的 `montage-describe/v1`、过滤敏感号码、准入与挂断 | M4、M6 | 跨语言测试；相册现有的准入测试全部通过 |
| M8a（[#558](https://github.com/zhongwater123/A-NAS/issues/558)） | 持久任务、步骤缓存、校验、重入式 `fill`、渲染、`saveFilm`（含相册导入事务里的钩子） | M3、M6、M7 | 开发时可先用替身的相册分析做调度与渲染 |
| M8b（[#559](https://github.com/zhongwater123/A-NAS/issues/559)） | 配乐：anas-api 经文件代理复制、配乐分析 Worker 与 allin1 的运行环境、权重的交付、`music`、试听与起点 | M4、M6 | allin1 的运行环境、权重摘要与开发机结构文件的对照测试同一个 PR |
| M9（[#560](https://github.com/zhongwater123/A-NAS/issues/560)） | montage MCP 服务器、助手套接字与定时任务单元的挂载、规划授权（文件代理的 `assistant_plan`、anas-api 对四条请求的登记与挂载层去掉同名头）、唤醒服务的规划部分（按 `ActiveState` 判断运行、`StateDirectory` 记计数、单独的运行目录放记号、按成员退避）、`run-requests.sh take` 一并移走它、回合脚本的清理、领取与模型检查、桌面提示。动工前按转正后的布局重写“规划”一节的接线 | M8a、P1、AI 助手 V1 转正 | MCP 服务器、managed-config、定时任务单元单独的成员配置、单元挂载、规划授权、唤醒服务与 Host Agent 按 UID 查询同一个 PR；系统测试覆盖“没有授权就不启动”；把 `create_job`、删除任务与删除运行的会话加进研究文档“以后怎么升级”的核对清单 |
| M10（[#561](https://github.com/zhongwater123/A-NAS/issues/561)） | 网页全流程，打开开关；本方案的状态、`CURRENT.md` | M8a、M8b、M9、AI 助手 V1 转正 | — |

- **先后**：P1、P2 已做完。M1–M8b 不碰助手，M0 之后照常推进；allin1 的商业版权待议不挡开发，挡 M10 打开开关；M9、M10 等 [AI 助手 V1 转正](../decisions/2026-11-17-assistant-v1-cloud-model.md)合并。转正之前，规划的端到端只能在系统测试里用替身模型验证。
- 第八版代码在 PR #454 的分支上（2026-11-17）。M6 从提交 `19f2d6db` 复制 `domain/`，并在 PR 描述里写明来源。
- **出片后的二次编辑**：R1–R6 是 [#596](https://github.com/zhongwater123/A-NAS/issues/596) 的子 issue，内容与依赖见 [ADR 0039 的交付](../adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md#交付)，都在 M10 之后。
- **整体验收**归 M10：在实验 NAS 上走完规格里的“首版闭环”。各票只对自己那部分的验收负责。

## 考虑过的替代方案

ADR 0038 的“未选择”列出了身份与归属层面的替代方案。实现层面另有：

- **一次 `filter_complex` 渲染整条成片**：FFmpeg 只运行一次，但要几十个输入描述符；内存和时间随镜头数增长；失败了要整条重来。逐段渲染每次只有一个输入，单段失败只重做那一段，第八版也是这样做的。
- **预先按整张卡片算好镜头的无遮挡区间，填槽时不再取帧**：`picture.clear` 往前扫描时以 `lo` 为采样起点，提前算出的区间截取后与第八版不一致。
- **`fill` 在同一个连接上一问一答地要帧**：第八版的计算同样不用改，但计算 Worker 是单实例，一次填槽要几十到上百次取帧，期间相册的 `cards`、`quality` 都得排队。
- **上传改用 tus 断点续传**：业内传几 GB 的文件通常用 tus，主线的传输中心也在用；但相册的上传是 multipart，改它超出本次范围。首版接受单文件 4 GiB、断了要重传。
- **首版就出三版让用户挑**：原型是这么做的，但三版的耗时和费用都是三倍，还要处理部分失败。#514 要求默认出一条。

## 风险

| 风险 | 后果 | 应对 | 时点 |
|---|---|---|---|
| 剪法规则成为相册分析的一部分（随[决定记录](../decisions/2026-11-17-montage-center-first-version.md)第 8 项而来） | 调整剪法要改相册、重算分析 | 分析身份带算法版本；只重算仍被授权引用的对象 | 批准前确认 |
| 实验 NAS 之外的设备上能否开启 VNAS | 规划不可用 | 已[决定](../decisions/2026-11-17-assistant-v1-cloud-model.md) V1 用云端 DeepSeek、全员默认开启；转正实施之前，验收在开发机和实验 NAS 上做 | M9 之前确认转正进度 |
| 测试用的 10 首歌是商业歌曲 | 不能随产品发布 | 只用于配乐探针和测试账号，不进仓库和软件包 | 不处理 |
| allin1 的商业版权（待议，见“配乐”） | 不能随产品分发时，首版不能对外打开配乐分析 | 待议期间只在开发机与实验 NAS 上运行；结论不利时退回 beat_this 与色度自相似，副歌按[配乐研究](https://github.com/zhongwater123/A-NAS/pull/546)的做法补 | M10 之前 |
| allin1 在 NAS 的 CPU 上慢、占内存（PyTorch 加 Demucs） | 选歌后要等几分钟才能试听；和描述、渲染同时进行时内存更紧 | 配乐分析一次一首、后台优先级；M8b 在实验 NAS 上实测，按 8 GB 的预算比 | 实施中验证 |
| 描述在 NAS 的 CPU 上很慢（按读文字中位 105 秒一张推算） | 一组素材的描述要几个小时；相册自己的 AI 积压跟着推迟 | 不设门槛；前台有人用时让路；实验 NAS 上记录耗时，作为性能评估的输入；以后可经局域网算力（[#196](https://github.com/zhongwater123/A-NAS/pull/196)）分流，只改相册一处 | 实施中验证 |
| 8 GB 内存（目标机型的上限，评审人 2026-11-18 确认）：4B 约 3.5 GiB，FFmpeg 切片上限 3 GiB，再加上相册、Qdrant 和服务本身 | 描述和渲染同时进行时有压力 | 描述在相册的准入里，加载前留出内存余量；剪辑中心一次只渲染一段。实验 NAS 是 16 GB，首版闭环记下的峰值要按 8 GB 的预算比，不能当作够用的证据 | 实施中验证 |
| 关键帧超过 12 张的竖拍长卡片 | 超出 8,192 的上下文，这张卡片描述失败、不进目录 | 和第八版行为相同；实验 NAS 上统计失败的卡片数，多了再评估加大上下文 | 实施中验证 |
| 规划用的任务靠 Hermes 的内部接口（`create_job`、`trigger_job`、执行记录、删除运行的会话），和 `cron_round.py` 一样 | 规划不可靠；Hermes 升级时可能失效 | P1 已验证这些接口在当前固定的提交上可用；内部接口变了让这一轮明确失败，照[以后怎么升级](../research/hermes-agent-runtime.md#以后怎么升级)一起核对；升级后不再可用时退回哪种做法，先问评审人 | 实施中验证 |
| 一轮规划要起一个 gateway：三个 MCP 服务器时约占 410–610 MB（[gateway 运行方式的决定](../decisions/2026-11-10-hermes-gateway-runtime.md)），P1 在开发机上实测规划这一轮（`anas`、`companion`、`montage` 三个）为 642 MiB | 8 GB 内存更紧 | 唤醒服务全机同一时刻只启动一轮规划；M9 与实验 NAS 上实测峰值 | 实施中验证 |
| 改由 VNAS 规划后答案质量下降 | 成片变差 | 在固定样本上人工对比；关键规则放在提示词模板里 | 实施中验证 |
| Hermes 的后台复盘可能把卡片描述或诱导文字写进成员的记忆或技能 | 描述留在助手目录里；诱导在以后的对话里生效 | Hermes 对定时任务的运行固定不跑复盘，P1 已实测；升级 Hermes 时核对；描述已去掉号码 | 升级时核对 |
| 定时任务单元多一个 MCP 子进程，对话实例里也多一个空闲的（P1 实测约 78 MiB） | 内存变多 | 定时任务单元单独一份成员配置，对话实例不带 montage；M9 实测 | 实施中验证 |
| [FFmpeg 槽位与内存的决定](../decisions/2026-10-21-ffmpeg-worker-slots-and-memory.md)仍是 pending | 渲染并发和内存上限可能再变 | 剪辑只用 `Background`，跟着最终数值走 | 不处理 |
| 窗口集合首版没有读者 | 多一份同步、对账与内存 | 按用户决定照 #514 做；不建 HNSW。二次编辑的文字搜镜头是第一个读者（#599） | 不处理 |
| 剪辑渲染与剪辑分析同时占着全机仅有的 2 个后台 FFmpeg 槽位 | 缩略图、影视与音乐扫描变慢 | 各自同一时刻只发 1 个；跟着 FFmpeg 槽位的决定调整 | 实施中验证 |
| 黄金比对的样本来自真人素材，不能进公开仓库 | 新会话无法在仓库里复现比对 | 样本留在开发机，测试按环境变量读取、没有样本时跳过；PR 写明位置与摘要 | 不处理 |
| 第八版代码在 PR #454 的分支上（2026-11-17） | 来源不稳定 | 固定从 `19f2d6db` 复制，PR 描述写明来源 | 不处理 |
| 单文件 4 GiB、不能断点续传 | 大文件上传失败要重传 | 上传队列逐个重试；续传另开 issue | 不处理 |

## 验证

验收标准与证据层见[规格](../specs/montage-center.md#验收证据)。检查命令：
- 文档：`make docs-check`；
- 代码：`make check`、`make race-test`（任务调度是并发代码）、`scripts/system-test.sh`；
- 改了契约要运行 `make api`。

## 关联

- **需求与原型**：
  - 需求 [#514](https://github.com/zhongwater123/A-NAS/issues/514)；原型差异 [#515](https://github.com/zhongwater123/A-NAS/issues/515)；路线与外发约束 [#295](https://github.com/zhongwater123/A-NAS/issues/295)；资源条件 [#341](https://github.com/zhongwater123/A-NAS/issues/341)；
  - 原型代码 PR [#454](https://github.com/zhongwater123/A-NAS/pull/454)，[第八版说明](https://github.com/zhongwater123/A-NAS/blob/19f2d6db/ai/montage_v8/README.md)。
- **决定**：
  - [剪辑中心首版的数据归属与几项做法](../decisions/2026-11-17-montage-center-first-version.md)；
  - [Hermes 的固定提交怎么越过 gateway 运行时](../decisions/2026-11-10-hermes-gateway-runtime.md)（定时任务单元的做法）；
  - [方案中补充的默认做法](../decisions/2026-11-17-montage-center-default-rules.md)；
  - [相册放视频，视频不参与 AI](../decisions/2026-10-19-videos-in-the-photo-library.md)。
- **ADR**：
  - 本方案的 [0038](../adr/0038-keep-montage-media-in-the-photo-library.md)；
  - 相关的 [0001](../adr/0001-use-go-for-product-services.md)、[0006](../adr/0006-use-a-managed-photo-library.md)、[0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)、[0017](../adr/0017-run-the-media-center-through-the-file-broker.md)、[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)、[0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)、[0029](../adr/0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)、[0037](../adr/0037-plug-removable-modules-in-as-separate-services.md)。
- **架构**：[总览](OVERVIEW.md)、[家庭中心](family-center.md)、[相册 AI](photo-ai.md)、[OCR 方案](photo-ocr.md)、[向量索引](vector-index.md)。
- **规格**：[剪辑中心](../specs/montage-center.md)、[相册视频](../specs/photo-video.md)、[AI 助手](../specs/ai-assistant.md)。
- **代码**：
  - 服务与授权：[`internal/familyservice`](../../internal/familyservice/service.go)、[`internal/photoservice/assistant.go`](../../internal/photoservice/assistant.go)、[`internal/photoservice/gate.go`](../../internal/photoservice/gate.go)、[`internal/photos/jobs.go`](../../internal/photos/jobs.go)；
  - Worker 与协议：[`internal/ffmpegworker`](../../internal/ffmpegworker/protocol.go)、[`internal/unixmsg`](../../internal/unixmsg/unixmsg.go)、[`internal/aiworker`](../../internal/aiworker/client.go)、[`ai/anas_ai/protocol.py`](../../ai/anas_ai/protocol.py)；
  - 助手：[`ai/assistant/managed-config.yaml`](../../ai/assistant/managed-config.yaml)、[`internal/assistantapi`](../../internal/assistantapi/rpc.go)；
  - 后台规划沿用的定时任务：[`cron-waker.py`](../../ai/assistant/cron-waker.py)、[`cron_round.py`](../../ai/assistant/cron_round.py)、[`run-requests.sh`](../../ai/assistant/run-requests.sh)、[`assistant-units.sh`](../../scripts/lib/assistant-units.sh)、[`assistant_run.go`](../../internal/filebroker/rootbroker/assistant_run.go)、[`tasks.go`](../../internal/assistantapi/tasks.go)。
