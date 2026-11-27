# 剪辑中心首版实施方案

状态：accepted（2026-11-18 用户批准，同时接受 [ADR 0038](../adr/0038-keep-montage-media-in-the-photo-library.md) 与默认规则；2026-11-17 起草；2026-11-18 按 #514 作者的评审改了配乐、探针与 M9 的先后，又按用户决定改用 allin1 分析配乐，商业版权待议，按 P1 的结果改了“规划”；2026-11-19 按 [ADR 0039](../adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md) 加上出片后的二次编辑；2026-11-20 按用户[决定](../decisions/2026-11-20-montage-planning-as-a-vnas-session.md)把规划改为成员规划进程里的一次对话，重写“规划”一节，交接见[规划会话](videocut-planning-session.md)；首版 M0–M10 已于 2026-11-19 合入并部署到实验 NAS，实机验收的修复见“交付”；实施按 [#514](https://github.com/zhongwater123/A-NAS/issues/514) 的子 issue 分批，见“交付”；2026-11-25 按用户[决定](../decisions/2026-11-25-montage-planning-retries.md)改为规划没有完成时自动重来）
更新时间：2026-11-26

相关文档：
- 产品行为与验收以[剪辑中心规格](../specs/videocut-center.md)为准；
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
anas-api ──转发 /api/v1/videocut──▶ anas-videocut：项目、规划、时间线、任务
  │ “开始”前经文件代理               │ ⑤ 任务等规划时留下记号
  │ 登记规划授权                     ├──▶ 相册授权套接字：③ 登记授权 ④ 请求分析、读结果
  ▼                                  │                    ⑧ 取原片描述符 ⑨ 存回成片
规划发起方（root）                    ├──▶ FFmpeg Worker：⑦ 填槽取灰度帧、逐段渲染、拼接配乐
  有授权、有记号时领取一轮，          ├──▶ 剪辑计算 Worker：选段、目录与规划数据、填槽
  在规划进程里开一次对话、等它结束    └──▶ 配乐分析 Worker：allin1（节拍、重拍、段落）
     │
     ▼
Hermes 规划进程（成员的 VNAS，同一个沙箱与主目录，只有剪辑、记忆、技能工具）
  ⑥ 经剪辑的助手套接字调 videocut MCP 工具，回到 anas-videocut：读目录、交大纲、交镜头
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
- **规划**：任务走到规划时，NAS 在后台发起，成员不用开着桌面。root 的规划发起方核对成员点“开始”时登记的规划授权，在成员的规划进程里开一次对话（[规划会话](videocut-planning-session.md)）。VNAS 调 videocut MCP 工具读卡片目录，交回两步规划；剪辑中心按 Schema 和剪辑规则校验后才收下。
- **成片**：渲染结果经授权存进成员的个人图库，播放、下载都是相册里的普通视频。剪辑中心只记下“哪个版本对应哪个相册资产”。

**主要的失败行为**
- VNAS 不可用：任务停在“等待规划”，页面写明原因；上传、浏览、播放照常。
- 素材进回收站：任务暂停，恢复后继续。
- 素材被永久删除：报告缺了哪些；已存进相册的成片不受影响。
- 取消：不再调度新步骤，终止正在进行的渲染，不存回成片。

## 代码结构

剪辑中心在每种语言里各收进一个文件夹，内部按同样的四层分目录，依赖只能往下；越层引用由检查工具在 `make check` 里拦下。重构的步骤、迁移与验收见 [#639](https://github.com/zhongwater123/A-NAS/issues/639)；本节只写结构与规则，各步完成时更新。

| 层 | 管什么 | 不能做什么 |
|---|---|---|
| 页面层 / 通信协议 | 页面、布局、用户操作；HTTP 接口与 VNAS 套接字 | 直接调接口（网页）、写业务规则 |
| 中间层 | 一个个功能（建项目、改镜头、出片、导出），把下面几层串起来 | 关心界面长什么样 |
| 底层接口 | 和外部打交道：数据库、相册、FFmpeg、计算工人、VNAS、HTTP | 写业务规则 |
| 纯算法 | 规则和计算本身 | 任何输入输出 |

页面层 → 中间层 → 底层接口，纯算法谁都能用。

| 层 | 网页 `web/src/videocut/` | 服务端 `internal/videocut/` | 算法 `ai/anas_videocut/` |
|---|---|---|---|
| 入口与组装 | `VideoCutPanel.tsx` | `videocut.go` | `worker.py` 的启动部分 |
| 页面层 / 通信协议 | `screens/`、`ui/` | `protocol/` | `worker.py`（收发消息） |
| 中间层 | `app/` | `app/` | `ops/` |
| 外部与数据 | `api/` | `db/`、`external/` | 没有：只算不存 |
| 规则与算法 | `lib/` | `domain/` | `algo/` |

**服务端（Go）**：

```text
internal/videocut/
├── AGENTS.md        局部规则
├── videocut.go      组件的 main：读配置、开数据库、接上外部能力、启动后台出片与各个监听；cmd/anas-api 只调它的 Run，开发模式在产品服务进程里跑，调 Local（两者共用 Workers 的组装）
├── assistant.go     把开着的 Store 接到助手套接字上（组装的一部分）
├── protocol/        HTTP 接口；助手套接字：监听、按对端 UID 认成员、上面的规划接口
├── app/             用例，一件事一个文件，也包括后台出片任务；ports.go 定义外部能力的接口
├── domain/          数据类型、修改稿操作的规则、共用的错误
├── db/              自己的数据库：Update(ctx, func(*db.Tx) error)，每类数据的读写是 db.DB、db.Tx 上的方法，两者都用的只写一次
└── external/        实现 app/ports.go 的接口：相册、FFmpeg、计算工人、配乐工人、规划记号
internal/videocutcompute/   计算工人的客户端与消息类型
```

- 2026-11-26 [#670](https://github.com/zhongwater123/A-NAS/issues/670) 搬完家，[#665](https://github.com/zhongwater123/A-NAS/issues/665) 从 `app/` 拆出 `domain/`、`db/`、`external/`：`app` 不再写 SQL，`db.DB`、`db.Tx` 不把 `*sql.DB`、`*sql.Tx` 交出去（测试另有 `SQLForTests`）；查不到时 `db` 答 `db.ErrNotFound`，存成文字的时间在 `db` 里解析成 `time.Time`，`app` 不引用 `database/sql`（depguard 拦）。`db` 的部分读取仍把可空的列以 `sql.Null*` 值交给 `app`。`domain` 已有修改稿的状态与节拍的名字（`draftops.go`）、成片的帧与逐槽核对（`timeline.go`）、选段的计算（`excerpt.go`）；修改稿 9 种操作的规则（`editSource` 一组）、`validateTimeline` 与歌曲行上的 `excerptStart` 还在 `app`：前两者读的 `analysedVideo` 带 `photos.Window`，等这个值类型挪进 `photosapi` 再挪；`excerptStart` 按 `app` 的歌曲状态回答。
- `protocol` → `app` → `domain`、`db`；`external` 实现 `app` 定义的接口（谁用接口谁定义，测 `app` 时直接放替身）；`db`、`external` 不引用 `app`、`protocol`；`domain` 不引用其他层；只有 `videocut.go` 认识各层的具体实现，`app` 对外部能力的接口检查（如计算工人的客户端是 `app.Compute`）也放在那里，因为 `external` 不能引用 `app`。`ports.go` 签名里出现的类型都放在 `domain` 或协议类型包里。
- 事务跨 `app`/`db`：`app` 在 `db.Update` 的回调里按业务顺序调 `db.Tx` 的方法、做判断，自己不写 SQL；修改稿“读修订号、核对、写一步、加修订号”仍由 `app` 主导。`db` 不抽成接口，测试用临时 SQLite。
- 错误到 HTTP 状态码的对应只在 `protocol` 写一处。
- `internal/videocutcompute` 留在 `internal/videocut/` 外面，因为相册服务也用它切卡片、算画质。它和 Python 工人之间的消息类型、常量与错误（`PlanBrief`、`FillRequest`、`MusicRequest`、`ErrWorkerRefused` 等）定义在它里面，它不引用 `internal/videocut/...`，相册经它不会依赖剪辑中心。

**分层检查（Go，golangci-lint 的 depguard）**：只约束内层 `domain`、`db`、`app`，规则按包的类别写；外层可以引用 HTTP、账号、服务代理等需要的包，只受 `internal/videocut` 内部的方向约束。

| 类别 | 包 | 谁能引用 |
|---|---|---|
| 协议类型包 | `internal/photosapi`、`internal/videocutcompute` | `domain`、`db`、`app` |
| FFmpeg 工人 | `internal/ffmpegworker`：客户端在 `cmd/anas-api` 建好传进 `videocut.Run`（开发模式是 `videocut.Local`），经 `app.Editing`、`app.Media` 交给 `app` | 只有 `app`；它直接用 `Client`、`Local` 这一点 depguard 拦不住，由评审核对 |
| 基础库 | `internal/sqlitedb`、`internal/atomicfile`、`internal/keylock`、`internal/capacity` | `db`、`app` |
| 相册实现包 | `internal/photos`：剪辑用到它的值类型（`Grant`、`WindowMatch` 等） | 按文件放行，清单在 `.golangci.yml`；值类型挪进 `photosapi` 后删掉 |
| 其他模块 | 以上之外的 `internal/...` | `domain`、`db`、`app` 都不能用 |

另外，`internal/videocutcompute` 与 `internal/photoservice` 不引用 `internal/videocut/...`，它们的测试也不引用：相册服务的测试只核对授权套接字回答的错误码，这些错误码到剪辑中心错误的对应由 `internal/videocut/external` 的测试核对。

规则在 `.golangci.yml` 的 `videocut-domain`、`videocut-db`、`videocut-app`、`videocut-app-photos`、`videocut-outer`、`videocut-users`，随 `make check` 的 `go-lint` 运行；除 `videocut-users` 外，测试文件不受约束（`app` 的测试会用 `external` 的替身）。`internal/photos` 放行的是 `app` 的 `draftops.go`、`editing.go`、`editjobs.go`、`ports.go`、`projects.go`、`render.go`、`steps.go`；拆层后 `domain` 已不引用它。

**网页**：只有 `api/` 调后端，类型来自生成的 `api.generated.ts`；桌面判断要不要显示剪辑中心的 `videoCutEnabled` 也在 `api/`（`status.ts`，单独一个小文件，因为桌面一开始就加载它）。`lib/` 引用 `api/` 只能是 `import type`，也不引用 React（Oxlint 的 `no-restricted-imports`）。规则看的是写出来的导入路径，分不出 `../../api` 指向剪辑的 `api/` 还是全站的 `src/api/`，所以对 `lib/` 里任何层级、指向这两个 `api/` 的相对导入都只放行类型。页面现在还直接引用 `api/`，“页面层不直接调接口”的检查等新界面把调用挪进 `app/` 后再加。

**算法（Python）**：`algo/` 只引用标准库、NumPy 和自己，`ops/` 不引用 `worker.py`，由 `ai/tests/test_videocut_layers.py` 检查。

## 数据归属

| 数据 | 归属 | 位置 |
|---|---|---|
| 原片、成片 | 相册：照片资产。上传的原片与成片在成员的个人图库，从相册选入的视频留在原来的图库 | 相册受管存储与 Catalog |
| 项目授权 | 相册 | Catalog：`montage_grants`、`montage_grant_assets` |
| 窗口向量、卡片、画质、卡片描述 | 相册：按内容对象，跨项目、跨成员复用 | Catalog（见“剪辑分析”）；窗口向量另同步到相册的 Qdrant |
| 项目、批次、素材范围、任务、规划、时间线、版本与成片资产的对应 | 剪辑中心 | `videocut/videocut.db` |
| 配乐的副本与分析 | 剪辑中心：成员选歌时经文件代理复制的一份；原文件仍在成员的空间里，由音乐功能索引 | `videocut/songs/<sha256>/`，没有项目再用时删除 |
| 渲染中间文件 | 剪辑中心 | `videocut/tmp/<job-id>/`，任务结束时删除（见“清理”） |

剪辑中心不保存原片、分析结果或成片的副本；卡片为什么归相册、怎样理解 #514 的“项目特定的卡片分组”，见 [ADR 0038](../adr/0038-keep-montage-media-in-the-photo-library.md) 决定 2。

## 服务形态与部署

- **进程**：`/usr/lib/a-nas/anas-api videocut-service`，单元 `anas-videocut.service`。
  - 系统账号 `a-nas-videocut`，固定 UID 31005，是 sysusers 里下一个空闲号。附加组 `a-nas-photos`，用来连 FFmpeg Worker 与剪辑计算 Worker 的套接字。这个组也让它能连上相册的产品接口与 AI Worker 的套接字，和家庭中心一样（见“项目授权”的“被攻破时”）。
  - 沙箱照搬 `anas-family.service` 的全部加固项，不只是下面几项：
    - `PrivateNetwork`、`ProtectSystem=strict`、`RestrictAddressFamilies=AF_UNIX`；
    - `NoNewPrivileges`、`PrivateTmp`、`PrivateDevices`、`ProtectHome`、`ProtectKernel*`；
    - `UMask=0077`、`RuntimeDirectoryMode=0750`；
    - `ReadWritePaths=/srv/a-nas/data`。实际能写哪里由子卷的属主与 ACL 决定，和家庭中心一样。
  - 资源：`MemoryMax=512M`、`CPUWeight=20`、`IOWeight=20`。重计算都在 Worker 和相册里，服务本身只管状态。
- **入口**：anas-api 用 `mount("/api/v1/videocut", apiroute.Session, ...)` 把请求转到 `/run/a-nas-videocut/videocut.sock`。转发时去掉 Cookie、CSRF 与 Authorization 头，换成 `X-A-NAS-Session`；`X-A-NAS-Assistant` 由 anas-api 自己设置（见“规划授权”）。服务拿这个会话去问 Host Agent 的会话查询套接字 `/run/a-nas-videocut-sessions/videocut.sock`，确认来人是谁。
- **开关**：环境变量 `ANAS_VIDEOCUT_ENABLED`，M10 之前默认关闭。关闭时产品服务不挂载 `/api/v1/videocut`，桌面不显示入口。网页入口只是外观，隐藏它挡不住接口，所以开关放在服务端；先例是 `ANAS_PHOTO_ASSISTANT_LOOKUP`。
- **存储**：Host Agent 建 `videocut/` 子卷，权限 0700，属主 `a-nas-videocut`，每 15 分钟修复一次漂移。
  - 数据卷离线或只读时，整个服务回答 503 `videocut_unavailable`，不在系统盘建替代目录。
  - 容量不足只拒绝写入，不让整个服务不可用（[不变量 12](OVERVIEW.md#不变量)）：渲染写 `tmp/` 前用 `capacity.Admits` 按预计大小检查，存回时相册照常检查；不够时任务进入 `needs_input(storage_full)`，腾出空间后重试。已有项目与成片照常可看。
- **剪辑计算 Worker**：`anas-videocut-compute.socket` 加 `anas-videocut-compute.service`。
  - 单个常驻实例，逐个处理连接，免得每个连接都重新导入 NumPy。
  - DynamicUser，没有网络，看不到数据卷，用设备上已有的 AI 运行环境 `/usr/lib/a-nas/ai/runtime` 运行 `python -m anas_videocut.worker`。
  - 套接字 `SocketGroup=a-nas-photos`、`0660`，相册与剪辑中心都能连。
  - 放在 `anas.slice`（后台工作，`CPUWeight=10`）之下，`MemoryMax=1G`。画质的帧分块交来（见“卡片与画质”），内存不随卡片长度增长。
- **配乐分析 Worker**：allin1 依赖 PyTorch 等，放不进计算 Worker 用的 AI 运行环境，所以另起一个。同样 DynamicUser、没有网络、看不到数据卷、放在 `anas.slice` 之下，只有剪辑中心能连。运行环境、内存上限与交付在 M8b 定（见“配乐”）。

### 接线清单

家庭中心实际接了约 40 个文件、55 处（2026-11-17 核对）。剪辑中心逐项照做，M1 在同一个 PR 里完成：

| 区域 | 要改的地方 |
|---|---|
| 单元与账号 | 新单元；`deploy/sysusers.d/a-nas.conf`；`anas-host-agent.service`（会话套接字的环境变量、组、`RuntimeDirectory`）；`anas-api.service` 的 `SupplementaryGroups` 加 `a-nas-videocut`（否则连不上 0750 的 `/run/a-nas-videocut`） |
| 包脚本 | preinst 检查 UID 31005 冲突、停掉改名前的规划发起方 `anas-montage-planner`、备份 `videocut.db`、备份前停服务；postinst 重启服务；prerm 停服务；`build-packages.sh` 的 wants，以及安装 `ai/anas_videocut`（`ops/`、`algo/`；模板只装到 `assistant/videocut-prompts/`） |
| 体检与门禁 | `deploy/site/a-nas-check.sh` 的 UID、单元、数据库三处循环（数据库就绪看服务日志里的 `videocut service ready`）；`make ops-check` |
| 系统测试 | 服务在跑；只读重挂前后停起服务；剪辑一节；产品服务的附加组 |
| anas-api | `cmd/anas-api/videocut.go`、`config.go` 的套接字默认值、`main.go`、`deletion.go`（`accounts.ForgetStep("videocut")`） |
| Host Agent | 会话查询、`EnsureVideoCutStore` 与卷上的调用、身份常量 |
| 路由 | `httpapi/product.go`、`routes.go` |
| 日志 | 服务端白名单、`api/components/logs.yaml` 的枚举、网页的日志列表、[日志规格](../specs/logs.md) |
| 系统更新 | 回滚时要停的服务与要恢复的数据库 |
| 网页 | `apps.tsx`、图标、桌面提示、MSW 替身 |
| 文档 | [架构总览](OVERVIEW.md)；[软件包部署手册](../runbooks/deploy-a-nas-packages.md)里的手工恢复命令 |
| Fake 模式 | 在进程内装配剪辑服务；相册授权、FFmpeg、计算 Worker 用替身，保证 fake-preview 能走完网页流程 |

## 剪辑中心的数据

数据库是 SQLite 的 `videocut/videocut.db`，使用 WAL 与 `synchronous=FULL`（掉电后已提交的事务也在），迁移版本记在 `user_version`。所有表都带 `owner_user_id`，按成员隔离。

| 表 | 主要列 | 说明 |
|---|---|---|
| `batches` | id、owner_user_id、request_key、created_at | 一次主动导入；`(owner_user_id, request_key)` 唯一，重复提交返回第一次的结果 |
| `projects` | id、owner_user_id、name、grant_id、state、song_id、song_track、song_start、song_selection、ask、chronological、chosen_version_id、created_at | `song_track` 是这首歌从音乐库的哪一首复制来的（接口的 `trackId`），配乐页只给这一首打勾，同名的其他歌不打勾；`song_selection` 是成员拖动起点后的选段；`chosen_version_id` 是成员确认的那一版（#596）。`state` 依次为 `registering`、`ready`、`deleting`。先写 `registering` 的行，再以项目 ID 为幂等键登记授权；登记要用会话，所以重启时停在 `registering` 的行无法补完，启动时撤销它的授权，并在同一个事务里删掉这一行与它的批次行 |
| `project_assets` | project_id、asset_id、object_id、batch_id、added_at | 素材范围固定到内容对象 |
| `jobs` | id、owner_user_id、project_id、request_key、settings_json、state、step、attempts、not_before、lease_until、heartbeat_at、cancel_requested、wait_reason、error_class、error、missing_json、save_started、plans、plan_wanted、plan_wanted_at、plan_epoch、kind、source_version_id、draft_id、draft_revision | `kind` 是 `generate`、`edit` 或 `recut`，后三列是 `edit`、`recut` 任务的来源（#596，见“出片后的二次编辑”）。`settings_json` 是任务开始时的歌、起点、要求与是否按拍摄时间，见“持久任务”。`save_started` 是存回栅栏：存回请求发出后置 1，结果确认前取消回答 409，重启后重放同一个版本键。`plans` 是这个任务出几个方案（最多 2；升级前开始的任务是 1，照开始时的样子做完）。`plan_wanted` 与 `plan_wanted_at` 表示等一轮规划及从何时起；`plan_epoch` 在重新规划、去掉素材时加一，旧轮次随之作废 |
| `job_assets` | job_id、asset_id、object_id | 任务开始时的素材集合 |
| `steps` | job_id、name、input_key、state、result_ref、started_at、finished_at | 每一步的输入摘要和结果 |
| `rounds` | id、job_id、owner_user_id、attempt_token、run_ref、state、claimed_at、started_at、ended_at、reported_at、outcome、epoch、template、input_key、called_at、failure | 规划的每一轮，一轮就是规划进程里的一次对话。`epoch` 与 `input_key` 是领取时任务的规划代数与方案一第一步的输入键，每次工具调用都拿它们和任务现在的值比，对不上就回答“这一轮已结束”；`called_at` 是模型最近一次调用工具的时间，规划时限从它算起。`state` 依次为 `claimed`、`started`、`ended`，或 `abandoned`；`run_ref` 是对话的存储编号，成员的 VNAS 历史里能找到它。`reported_at` 是规划发起方报告结果的时间；`failure` 是没开始或出错的原因（`unreachable`、`error`），重试的等待按它算 |
| `plan_grants` | owner_user_id、username、registered_at | 最近一次经过规划授权登记的请求时间（anas-api 带来 `X-A-NAS-Assistant: on`）。只用来决定留不留记号，授权本身在文件代理那里 |
| `plan_answers` | job_id、step、input_key、round_id、answer_json、accepted_at | 工具收下的答案，按（任务、步骤、这一步的输入键）只留第一份。任务自己的步骤按输入键取出，再写进 `plans`。`step` 只有 `pick`、`shots`，方案二也写这两个值，靠输入键区分 |
| `plan_briefs` | job_id、step、input_key、brief_json | 计算 Worker 为某一步、某个输入键做的 brief，只做一次；任务的步骤先做好，工具来读时直接用 |
| `plans` | job_id、plan、step、round_id、catalog_key、answer_json、accepted_at、voided_at | `plan` 是第几个方案；答案由任务的步骤从 `plan_answers` 取来写入，`round_id` 不再填写。每个任务每个方案每步只有一份有效答案。“重新规划”从还没收下的那一步开始；去掉素材使目录变化时，已收下的答案标为作废，两步都重新规划；每次“生成”是新任务，两步都重新规划 |
| `versions` | id、project_id、number、job_id、plan、version_key、film_asset_id、kind、source_version_id、draft_revision、timeline_json、created_at | 成片存进相册后才写一行，一个任务的每个方案一行（`job_id` 与 `plan` 唯一）；失败或取消的任务不留版本，已存的方案留着。`number` 在项目内从 1 递增，是成片文件名里的版本号。`version_key` 与渲染输出的字节无关：生成与重剪的版本由任务 ID 与渲染步骤的 `input_key` 组成，存为新版本的由修改稿 ID、修订号与渲染步骤的 `input_key` 组成，同一修订重试只存一次。`kind` 按任务种类：生成任务的版本是 `plan`，存为新版本是 `edit`，重剪是 `recut`；`timeline_json` 是这一版的时间线，存回时在同一个事务里写入，修改稿、粗预览与镜头清单都从它读 |
| `drafts` | id、owner_user_id、project_id、base_version_id、revision、shots_json、updated_at | 修改稿，每个版本一份（`base_version_id` 唯一）。`shots_json` 记按成片顺序盖住这一版音乐槽的镜头；音乐槽本身从不改变，不存在这里，打开时从版本的时间线（`versions.timeline_json`）读出。每镜记：每镜的节拍、盖住第几到第几个槽（删掉一镜就是把它的槽并给前一镜或后一镜）、哪个槽是它自己的（`home`，“还原这一镜”与“已换”按它判断）、是否锁定 |
| `draft_revisions` | draft_id、revision、op、seed、back、shots_json、created_at | 每一步一行，`shots_json` 同 `drafts`，只有镜头。`back` 是撤销这一步回到的修订：普通的一步是它之前的修订，撤销的一步是被撤销那一步的 `back`，所以连续撤销一直往回走，不会变成重做。保留最近 100 个修订和撤销链上的修订，最多 101 行，所以不管其中几步是撤销，都能撤销最近 100 步 |
| `revocations` | grant_id、next_try、attempts | 撤销授权时相册不可用，就记在这里重试 |
| `songs` | id、owner_user_id、sha256、title、duration、state、reason、analysis_version、analysis_json、created_at | 成员选过的歌：副本的摘要、分析结果（节拍、重拍、段落、副歌、响度）与不可用的原因 |

子卷里的文件：
```text
videocut/
  videocut.db
  songs/<sha256>/audio      （成员选歌时复制的一份，见“配乐”）
  tmp/<job-id>/             （渲染中间文件与待存回的成片，按任务分目录）
```

**清理**
- 删除项目：先取消任务，再删掉项目、版本记录、修改稿与规划，撤销相册授权；不动相册里的原片和成片（#514）。
- `tmp/<job-id>/` 在任务结束（完成、失败或取消）时删除；服务启动时只删掉已结束任务的目录。渲染步骤记为完成、目录里的文件却不见了时，渲染步骤视为没完成，重新渲染。
- 删除账号：`ForgetStep("videocut")` 清掉这位成员的全部行。相册在自己的账号删除步骤里清掉这位成员的授权。相册那一步先执行，所以剪辑中心的撤销必须幂等。
- 配乐副本：删除项目、删除账号之后，回收已经没有任何 `songs` 行引用的 `songs/<sha256>/` 目录。`songs` 行在当前项目、未结束的任务或任何版本（经它的任务的 `settings_json`）还引用时保留：二次编辑的粗预览、存为新版本与重剪都用版本当时的歌（#596 起；在此之前项目换歌后，旧任务的歌会被回收）。同一份内容可能被几位成员共用，所以按引用回收；服务启动时也对一次账。
- 启动时对账：`videocut.db` 与相册的授权表可能被恢复到不同的时间点。剪辑中心启动时用 `list` 列出相册里自己的授权：剪辑中心有、相册没有的，项目标为素材缺失；相册有、剪辑中心不认识的，撤销。

## 项目授权

剪辑中心后台读原片、请求分析、存回成片时，成员可能早已离线；#514 又不允许长期保存明文会话令牌。所以改由相册持有一条项目授权。思路类似 Google Drive 的 `drive.file`（应用只能访问用户交给它的文件）：成员在场时登记，之后服务凭授权访问，权限仍由相册每次重新判断。

**套接字与协议**
- 由 systemd 持有：`anas-photos-grants.socket` 监听 `/run/a-nas-photos-grants/grants.sock`，`SocketUser=a-nas-videocut`、权限 0600，所在目录只有 root 能写，交给 `anas-photos` 接受连接。不用属组 0660，是因为产品服务也在 `a-nas-videocut` 组里。相册另用 `SO_PEERCRED` 核对对端 UID 是 31005。
- 协议用 `internal/unixmsg`：4 字节长度加 JSON，带描述符，已有 fuzz 测试。HTTP 传不了 `SCM_RIGHTS`，所以不用 HTTP。
- 请求与回答的 Go 类型放在 `internal/photosapi`，双方共用。按先例，内部接口不进 `api/` 契约（[API 契约](../development/api-contract.md)）。
- 这个套接字上的请求哪些算有人在用相册、推迟相册的 AI 工作，见[门控与三类工作](photo-ai.md#门控与三类工作)。

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
| `analysis(grantId, assetId)` | 返回分析结果：窗口（时间、是否纯色）、卡片、画质与描述，附各自的分析身份。JSON 只带状态、身份与计数，其余在封口的只读 memfd 里，格式与读取上限见 [`GrantAnalysis`](../../internal/photos/montagewindows.go) 与 `ReadWindows`；某个阶段在读取期间过期时只答它的状态，见[相册本地 AI](photo-ai.md#任务与调度) |
| `open(grantId, assetId)` | 原片的只读描述符，经 `SCM_RIGHTS` 传出。剪辑中心把它原样转给 FFmpeg Worker，不复制文件；同一个描述符同一时刻只用于一个请求 |
| `saveFilm(grantId, versionKey, name, fd)` | 相册把成片导入主人的个人图库，返回资产 ID，并把成片加进这条授权。幂等：先按 `(主人, versionKey)` 查有没有存过、并按这个键加锁，再暂存文件；`importStaged` 增加一个钩子，在建资产的同一个事务里写 `montage_films`（`(owner_user_id, version_key)` 唯一），所以提交与记录之间崩溃也不会多存一条。拍摄时间由 `assemble` 写进 MP4 的 `creation_time`，相册照常读取。相册照常检查容量，不够时回答 `insufficient_storage` |
| `searchWindows(grantId, texts, k)` | 二次编辑的文字搜镜头（#596，[ADR 0039](../adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md) 第 8 项）：相册用自己的 `embedQuery` 编码每段文字，只在这条授权的内容对象里检索窗口集合，回答每段文字最像的 k 个窗口（资产、窗口序号、分数）。同一个内容对象在授权里有几个资产时（同一个文件放在两个图库里），一个窗口按资产 ID 的先后给每个资产各回一条命中，每条都计入 k：剪辑中心按资产认视频，只回其中一个时，用另一个资产做的版本搜不到这段视频。一次至多 32 段文字、k 至多 200、文字段数乘 k 至多 1600，客户端把更多的文字分批发。搜哪些视频、何时回答 `model_mismatch`、何时改读 Catalog（结果相同）只写在[向量索引](vector-index.md#剪辑中心的窗口集合)的“读者”一条；AI Worker 编不出文字时回答 `ai_unavailable`（[`windowsearch.go`](../../internal/photos/windowsearch.go)）。剪辑中心把相册的回答换成给成员的错误码，候选与重洗共用（[`grantsearch.go`](../../internal/videocut/external/photos.go) 的 `GrantSearch`）：`model_mismatch` 是 409 `search_model_changed`；`ai_unavailable`、相册答不了（连不上、`photos_unavailable`、主人的账号停用）是 503 `search_unavailable`；相册不搜的文字（空白或超过 200 字，`invalid_request`）是 400 `validation_failed`；其余照其他授权调用（`grantError`），其中授权已撤销是 409 `grant_revoked`，相册回了剪辑中心不认识的错误码是 503 `photos_unavailable` |
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
| `montage-cards/v1` | 按窗口向量切卡片，定关键帧时刻和每张关键帧的节拍 | 剪辑计算 Worker `cards` |
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
- 剪辑分析是相册门控里[成员在等的后台工作](photo-ai.md#门控与三类工作)：不等相册空闲，块内每 200 毫秒问一次准入，只在内存不足时挂断。所以描述进行中成员搜索照片，要等这张卡片描述完（AI Worker 一次只服务一个连接）；2026-11-25 用户[决定](../decisions/2026-11-25-photo-ai-gate-member-work.md)接受这一点。
- 准入统计 `system.slice` 的压力，而 FFmpeg 实例也在 `system.slice` 里，分析自己取帧的负载会被算成“别的工作的压力”。所以分析中的 FFmpeg 取帧只在内存不足时挂断；压力超限时取完这一块再暂停。M5 在实验 NAS 上实测会不会反复暂停。

**保留**
- 结果跟着内容对象。内容对象被清除时（不是某个资产被删除时，同一内容对象可能还有别的资产），级联删掉 Catalog 行与 Qdrant 中的点。
- 没有任何未撤销的授权引用某个对象满 7 天后，相册删掉它的剪辑分析。
- 换模型或改分析身份时，`requeueStale` 只重排仍被未撤销的授权引用的对象，不会把曾经请求过的视频全部重算。

**Catalog 迁移**（带回滚步骤）

| 表 | 主要列 |
|---|---|
| `video_windows` | object_id、idx、start_ms、end_ms、flat、vector（BLOB，768 个 float32）、identity |
| `video_cards` | object_id、card、start_ms、end_ms、keyframes（JSON：关键帧时刻）、beats（JSON：每张关键帧的节拍，Catalog 第 23 步）、identity |
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
- **读路径**：首版不读 Qdrant，`analysis` 从 Catalog 读。窗口集合不参与搜索，所以不受[换模型](vector-index.md#换模型)时“先改为集合别名”的约束；二次编辑的文字搜镜头（`searchWindows`，#596）是第一个检索的读者，[#599](https://github.com/zhongwater123/A-NAS/issues/599) 给集合加了别名，只用来标记新集合已经写满：读写仍按集合名，换模型期间搜索回答 `model_mismatch` 或读 Catalog，不读旧集合，见[向量索引](vector-index.md#剪辑中心的窗口集合)。
- **不建 HNSW**：点数在十万级以内，精确扫描就够。内存账：一组 47 分钟的素材约 1,460 个点、4.5 MB，Catalog 与 Qdrant 各一份。
- **与 #207 的关系**：#207 的视频分段可以从这些窗口派生，由 #207 的设计决定。

### 卡片与画质

- **卡片**：调计算 Worker 的 `cards`，输入是窗口时间、是否纯色和向量，输出卡片、关键帧时刻和每张关键帧的节拍。纯色阈值与第八版相同（`FLAT=8.0`），记在分析身份里。
  - **节拍**（2026-11-26，[#676](https://github.com/zhongwater123/A-NAS/issues/676)）：关键帧照旧取（不够 3 张就均匀补到 3 张），相邻两张关键帧所在窗口的向量相似度不低于 0.93 就合成一个节拍，记在切卡身份里（`beat=0.93`）。第八版一张关键帧一个节拍，人标 1052 个边界只有 38% 该分开；合并后必须改的错从 554 降到 131。补齐的关键帧照旧全给描述模型看，常拍到本来那张看不到的动作。
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
- **提示词**：不收调用方的提示词，模板固定在 Worker 里，版本记作 `montage-card/v1`（2026-11-26 按节拍分组写，升为 `montage-card/v2`，见[相册 AI](photo-ai.md)）。
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

第八版的领域层只用标准库和 NumPy，没有输入输出。它原样放到 `ai/anas_videocut/algo/`（来源提交 `19f2d6db`），外面加一个 Worker 入口。

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
- 位置：开发机 C（macOS，见[开发环境](../development/LOCAL_ENVIRONMENT.md)）的 `~/A-NAS-datasets/`：原片在 `videos/`，窗口向量在 `cache/`，卡片与描述在 `剪辑对比/卡片`，规划与镜头表在 `剪辑对比/规划与成片`。
- 后两个目录 2026-11-23 之前叫 `第五版` 和 `第四版/剪法24（2026-11-08）`。原型的 [`mac-dev.toml`](https://github.com/zhongwater123/A-NAS/blob/19f2d6db/ai/montage_v8/profiles/mac-dev.toml) 仍写旧名，开发机 C 上的旧名是指向新目录的符号链接，删掉它们原型就找不到样本。卡片与镜头表里写着的关键帧绝对路径用的也是旧名，同样靠这两个符号链接解析；黄金比对不比较这些路径。2026-11-23 曾把 22 个文件里的路径随目录改成新名，2026-11-24 已换回原文件，内容与 M6 时逐字节相同，输入的 SHA-256 见 [#584](https://github.com/zhongwater123/A-NAS/issues/584)。
- 成片基准 `cut-<组>-recut-1.json`：`ch` 一直是第六版的结果，与第八版逐镜相同。`pj`、`ne` 原来也是第六版的，第八版的填槽在 pj 的 1 个、ne 的 4 个镜头上选得不同，比对一直失败；2026-11-26 按用户决定，用第八版原型（`19f2d6db`，`cut --free --no-render`，配乐只读已有结果）重新填槽、覆盖这两个文件，原型同时把当年没量过的 5 个遮挡区间补进 `held-pj.json`、`held-ne.json`。第六版的原文件备份在 `~/A-NAS-datasets/archive/2026-11-26-第六版pj与ne成片基准/`；新旧 SHA-256 与做法见 [#675](https://github.com/zhongwater123/A-NAS/pull/675)。
- 组名见原型的 [`cases.toml`](https://github.com/zhongwater123/A-NAS/blob/19f2d6db/ai/montage_v8/profiles/cases.toml)：`pj`（品杰，新西兰）、`ne`（新西兰）、`ch`（川西）。
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
- 新操作都用 `Background` 优先级，二次编辑的缩略图除外（#596，见“出片后的二次编辑”）。全机的后台请求一共只有 2 个槽位：剪辑中心同一时刻最多发 1 个 FFmpeg 请求，相册的剪辑分析同一时刻也最多 1 个。两者同时在跑时，缩略图、影视与音乐扫描要等，首版接受；跟着 [FFmpeg 槽位的决定](../decisions/2026-10-21-ffmpeg-worker-slots-and-memory.md)调整。
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
- 这条请求在 anas-api 里有自己的处理：它用音乐功能播放时的同一条路径，经文件代理以成员身份打开文件，流式交给剪辑中心。剪辑中心按内容摘要存成 `videocut/songs/<sha256>/audio`。
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
- 配乐页显示选中的段落，可以试听；不对就拖动起点，吸附到最近的重拍，终点按目标时长重算（[规格的默认规则](../specs/videocut-center.md#本稿补充的默认规则)）。
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

规划由成员自己的 VNAS 在后台做：root 的规划发起方把一轮交给成员的规划进程，作为一次对话，模型调 videocut MCP 工具读数据、交答案。交接的机制（规划进程、规划发起方、轮数、重试、学习）只在[剪辑中心的规划会话](videocut-planning-session.md)里写；本节写剪辑中心这一侧：工具、规则、授权的登记、去重与用量。

最初按定时任务单元跑一轮（唤醒服务、回合脚本、领取回执、`hermes cron tick`）实施，2026-11-19 的实机验收里卡在“等待 VNAS”（[调查](../investigations/2026-11-19-montage-job-stalls-on-the-experimental-nas.md)），用户 2026-11-20 [决定](../decisions/2026-11-20-montage-planning-as-a-vnas-session.md)改为对话，[#608](https://github.com/zhongwater123/A-NAS/pull/608) 实现。

### MCP 服务器

- `ai/assistant/anas_videocut_mcp.py`，配置照 `companion` 写全各个键：`trust: full`、`enabled: true`、`lazy: false`、`timeout: 120`、`tools.prompts/resources: false`（`ai/assistant/videocut-mcp.yaml`）。`untrusted` 不行：每次调用都要审批，而后台对话里没有人能批准。
- 只在规划进程的托管策略 `managed-plan/` 里，规划进程的工具组固定为 `videocut,memory,skills`。成员平时的 VNAS、定时任务与整理单元都没有这个服务器，也不绑定剪辑套接字。
- **云端看不到画面**：规划进程里没有看图、看视频、读文件、终端、相册与桌面的工具，工具只给文字与结构化数据（用户 2026-11-20 决定 3）。
- 套接字：systemd 的 `anas-videocut-assistant.socket` 持有宿主机上的 `/run/a-nas-videocut-assistant/assistant.sock`，权限 0666，所在目录只有 root 能进；只绑定进规划进程，在沙箱里是 `/run/anas-assistant-videocut/videocut.sock`。
- 剪辑中心用 `SO_PEERCRED` 取得对端 UID，经 Host Agent 按 UID 认出在用的账号，只回答这位成员自己的任务。UID 0 的对端（规划发起方）用 `X-A-NAS-Account` 指明成员。
- 套接字上有两类调用：MCP 服务器的工具（下表，`/v1/tools/*`），和规划发起方的 `claim`、`started`、`abandon`、`end`（`/v1/rounds/*`）。
- 同一个 UID 在沙箱里还能用终端（成员平时的 VNAS），所以工具调用要带着进行中那一轮的 token，套接字还限制请求大小（80 KiB）与频率（每位成员每秒 5 次，突发 20 次）。

| 工具 | 输入 | 输出 |
|---|---|---|
| `videocut_brief`（`readOnlyHint`） | `jobId`、`attemptToken` | 第一步的数据（卡片目录、音乐段落、成员的要求）与这一轮的精确 Schema |
| `videocut_submit_outline` | `jobId`、`attemptToken`、大纲 | `accepted`，附下一步该调什么；或 `rejected` 与原因，让模型改了再交 |
| `videocut_shots_brief`（`readOnlyHint`） | `jobId`、`attemptToken` | 第二步的数据（按收下的大纲生成的画面与可选节拍）与精确 Schema；大纲还没收下时回答“先交大纲” |
| `videocut_submit_shots` | `jobId`、`attemptToken`、镜头 | 同 `videocut_submit_outline`；还有方案要规划时回答 `next_plan`，全部收下时回答“做完了，只回答 [SILENT]” |

- **一轮规划两个方案**：一次生成只用掉规划授权 5 轮中的 1 轮。
  - 工具作用于“当前方案”：第一个还没存进相册、第二步还没有答案的方案；按 `plan_answers` 判断，不看 `plans`，因为模型交完方案一时任务可能还没来得及取走。方案一的镜头收下、后面还有方案时回答 `next_plan`，同一次调用重试也得到同样的回答；模板要求模型回到 `videocut_brief` 做下一版。
  - 方案二的输入键：第一步是 `planKey(方案一第一步的键, 2)`，第二步仍是 `shotsKey(本方案第一步的键, 本方案的大纲)`。任务的步骤与工具用同一对函数，所以答案存进 `plan_answers` 的键就是步骤要找的键。
  - brief：第一步的 brief 两个方案相同，存在方案一的键下，只做一次；第二步的按各自的键存。方案二的 `videocut_brief` 在返回前另拼上 `plan` 与 `earlierOutlines`（方案一收下的大纲），让它换一种讲法；这两项不存进 `plan_briefs`。
  - 一轮的身份只记方案一第一步的键（`rounds.input_key`），领取时也按它连接 `steps`。
  - 这一轮在方案二交齐之前结束：方案一照常往下做、存进相册，1 分钟后自动再要一轮，只规划方案二（2026-11-25 用户[决定](../decisions/2026-11-25-montage-planning-retries.md)；原来按 #514 停下等成员）。
- **答案的上限**写在 brief 的 Schema 里，模型交之前就知道：每个方案的场景数等于段落数（`prefixItems`，`items: false`）；文字答案每项最多 120 字，全部文字最多 1,500 字，枚举不计（[#607](https://github.com/zhongwater123/A-NAS/pull/607)）。

**规则、数据与 Schema 分开**
- 第八版的提示词把规则和数据写在一起，Schema 又按每个任务动态生成：卡片 ID 是枚举，段落数固定，第二步的画面和可选节拍取决于第一步的答案。交给 VNAS 时拆成三样：
  - **规则**：与答案无关的说明，即第八版两步的 system 文字、体裁规则、开头结尾与音乐结构的规则，写进这次对话的提示词，一开始就给出。Hermes 把所有 MCP 工具的结果都包成“不可信数据”交给模型，规则放在工具结果里会被当成数据。
  - **数据**：卡片目录、音乐段落、成员的要求、第二步的画面与节拍，由两个 brief 工具在需要时返回。
  - **精确 Schema**：这一轮的枚举与数量限制也由 brief 工具返回。submit 工具的 inputSchema 只写通用结构，精确校验在剪辑中心做。
- **规则是固定的模板**：源文件在 `ai/anas_videocut/prompts/`，按版本命名（现在是 `plan-v2.md`，一轮规划两个方案；工具名写全 `mcp__videocut__` 前缀）；软件包只把它装进规划进程读的 `/usr/lib/a-nas/assistant/videocut-prompts/`，不装进计算 Worker 的 `/usr/lib/a-nas/ai/anas_videocut/`（Worker 不读模板）。规划发起方按领取回答里的模板版本选取，只填进经过校验的几项：轮次与任务编号、token、目标秒数。所以剪辑中心决定不了提示词；它被攻破时还能经工具结果诱导模型，剩下的风险见“规划授权”。
- 拆分改变了第八版的提示词，所以规划不在黄金比对之内，靠固定样本上的人工对比验收（规格的“剪辑行为迁移”）。
- 后台对话里没有人回答审批，规划发起方对审批请求一律答拒绝。提示词仍要求不把描述写进记忆。

**校验错误怎样退回**：服务器用低层的 `Server` 实现，参数在工具里自己校验，不合格时返回普通结果 `status: "rejected"`，不用 `isError`。原因是 Hermes 把 `isError` 计入熔断：连续 3 次后，这个服务器 60 秒内的调用一律不可达（[Hermes 研究](../research/hermes-agent-runtime.md)）。剪辑服务回答 400 时，MCP 服务器也把它转成 `rejected`。

### 规划授权

成员不在线时，规划仍要在成员的 VNAS 里调用模型。[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 决定 10 的会话闸门不让后台服务凭自己的身份驱动成员的助手，所以后台规划要成员在场时留下授权，思路与“项目授权”相同。

- **登记**：成员做规格默认规则里列出的几项操作（开始、重新规划、去掉素材、重试分析、重剪，之后都可能要规划）时，anas-api 在转发之前，带着这次请求的会话调用文件代理的 `assistant_plan`。
  - 文件代理核对会话有效、账号在用、这个账号有 VNAS（Host Agent 为它准备的 `/var/lib/a-nas-host/assistant/tokens/<账号>.env` 存在）。
  - 核对通过就写 `/var/lib/a-nas-host/assistant/plans/<账号>`，内容是签发时间与轮数上限 5。只有文件代理写它，再次登记时整个替换。
  - `assistant_plan` 用正常的回答区分三种结果：已登记、设备没有开启 VNAS、这个账号没有 VNAS。错误码 `unavailable` 只留给临时故障。

    | 文件代理的回答 | anas-api 怎么做 | 剪辑中心 |
    |---|---|---|
    | 已登记 | 转发，带 `X-A-NAS-Assistant: on` | 把这个时间记进 `videocut.db` 的 `plan_grants` |
    | 设备没有开启 VNAS、这个账号没有 VNAS | 转发，带 `X-A-NAS-Assistant: off` | 任务照常建，页面写明“VNAS 没有开启”；开启后点“重新规划” |
    | `unavailable`，或连不上文件代理 | 回答 503 `assistant_unavailable`，不转发 | 页面提示稍后再试 |

  - 转发代理只去掉 Cookie、CSRF 与 Authorization 头，别的头照样转发（`internal/serviceproxy`）。所以 anas-api 在 `/api/v1/videocut` 的挂载层对所有请求都先去掉 `X-A-NAS-Assistant`，只在上面几项请求里自己设置；剪辑中心也只在这几项请求上读它。
- **按成员，不按任务**：同一成员的几个任务共用这 5 轮。一个任务通常只用 1 轮（两个方案在同一次对话里交完）。
- **计数与使用**：只有 root 的规划发起方读授权文件、按签发时间计数；剪辑中心读不到也改不了它。签发超过 7 天或领取满 5 轮就不再发起。
- **被攻破时**：
  - 剪辑服务只能让已授权的成员在有效期内跑至多 5 轮规划，没有授权的成员不受影响。
  - 它决定不了提示词，只能经 brief 的结果送数据。规划进程里没有文件、终端、相册与看图的工具，模型即使被诱导，能做的只是交回不合格的答案，或在复盘时把诱导文字写进记忆与技能。后者用户 2026-11-20 接受（决定 4），成员在“记忆”页查看与删除。
  - 产品服务只能为正在登录的成员登记授权，与 ADR 0024 决定 10 的威胁模型相同。

**等待与提示**
- 任务走到规划时进入 `waiting(vnas)`，剪辑中心写空文件 `/run/a-nas-videocut/plans/<账号>`，表示这位成员有任务在等规划；没有任务在等、或最近一次登记已超过 7 天时删掉，页面直接给“重新规划”。记号在运行目录里，启动时按 `jobs` 表重建。
- 一轮的时限：领取后 5 分钟没开始就作废；开始后距最后一次调用工具 20 分钟、或开始后 65 分钟仍没结束，就结束。等了 10 分钟还没被领取时，页面写“VNAS 还没有接手”，并给出“重新规划”。
- 重试按原因等待，页面写明在等什么、几分钟后再试（规划会话的“重试”）。
- 桌面外壳懒加载的 `VideoCutNotices` 照家庭中心的 `FamilyNotices` 做，只显示提示，不发起规划。内容见规格“本稿补充的默认规则”的桌面提示。

**取消**：剪辑中心把进行中的一轮记为已结束，之后交来的答案一律回答“任务已取消”，提示词要求见到这句就结束对话。

### 去重与结果关联

- `attemptToken` 在领取这一轮时生成，记进 `rounds`；规划发起方把它填进提示词，模型调工具时带上。没有进行中的一轮时，`brief` 和 `submit` 都拒绝。
- 每一步只收第一份通过校验的答案，后来的提交回答“已收下”，不覆盖。
- 一轮结束（报告结束、作废或超时）后，带这一轮 token 的答案回答“这一轮已结束”。
- 剪辑中心在等待规划时重启：一轮进行中就不另开新的一轮，直到它结束或超时。这样不会重复调用模型，满足 #514“调用成功但本地提交中断，不能无条件再次触发模型调用”的要求。
- token 只用来关联这一轮，不是凭据；答案还要过校验。

**用量**
- 2026-11-17 用开发机直连 Claude 重剪 5 组：每版两步合计 0.25–0.43 美元，第一步输入约 4.8–18.5 千 token，第二步约 6.9–15.2 千 token。
- 经 VNAS 后，每次请求还要带 Hermes 的系统提示、成员的记忆与技能、8 个工具定义（记忆与技能 4 个、剪辑 4 个）和逐轮累积的上下文。实验 NAS 的第一次完整出片（2026-11-20，DeepSeek）：规划 3 分 37 秒，第一次交大纲在 2 分 49 秒；复盘在结束后 49 秒报告。模型网关不记 token 用量，花费还没有实测数。

**和第八版的差异**：第八版用独立的 system prompt 直接调用 Claude；改由 VNAS 规划后，模型前面多了 Hermes 的系统提示、成员的记忆与技能和工具调用，答案质量可能不同。验收时在固定样本上对比。

## 持久任务

剪辑中心在自己的 SQLite 里实现任务：`jobs` 表加 `steps` 表。做法借鉴三处：
- 相册的 `jobs` 表；
- goqite 的租约：领取时设可见性超时，超时后重新投递；
- DBOS 的“每步结果先落库再往下走”：中断的那一步会重跑，所以每一步都要幂等。

不引入 River，它的 SQLite 驱动官方仍标为实验性；也不引入 Temporal 一类外部服务。

**状态**：`queued` → `running`，之后进入下面之一：
- `done`、`failed`、`cancelled`；规划试了 3 轮都没有完成的任务 `failed`，原因 `plan_failed`，成员点“重新规划”接着规划（见“规划”与[规划会话的重试](videocut-planning-session.md#4-重试)）；
- `waiting`，原因 `analysis`（相册分析未完成，或本地模型不可用）、`asset_trashed`、`vnas`；
- `needs_input`，原因：

| 原因 | 什么时候 |
|---|---|
| `asset_missing` | 素材被永久删除，或成员已经看不到 |
| `analysis_failed` | 相册对某段素材的分析失败（不是 I/O 类），等用户点“重试分析”或去掉这段素材 |
| `no_usable_footage` | 没有可用画面 |
| `music_invalid` | 歌曲不合格 |
| `vnas_unknown` | 规划没有交回（只有 2026-11-25 之前停下的任务；之后系统自动重来） |
| `plan_invalid` | 规划连续 3 次不合格（同上） |
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
- 方案数记在任务上（`jobs.plans`），开始时取 `app.Options.Plans`（产品默认 2，最多 2）；升级前开始的任务是 1。
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
| 规划没有完成 | 这一轮结束了没有交回规划，或答案连续 3 次不合格 | 1 分钟后自动重来，同样的输入最多 3 轮，用完后 `failed(plan_failed)`，见“规划” |

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

新增 `api/paths/videocut.yaml`，挂在 `/api/v1/videocut`。约定：
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
| POST | `/jobs/{id}/replan` | 规划没有完成而失败（`plan_failed`）、或 VNAS 还没接手时重新规划；失败的任务只在是项目最新的、项目没有别的任务在跑时接着 |
| GET | `/notices` | 桌面提示：等待规划与正在规划的任务、需要处理的任务、刚存进相册的成片、刚失败的任务 |
| PUT | `/projects/{id}/song` | `{trackId}`：从音乐库复制这首歌并开始分析（见“配乐”） |
| GET | `/projects/{id}/song`、`/projects/{id}/song/audio` | 分析进度、节拍、段落、副歌与默认选段，以及复制自音乐库的哪一首（`trackId`）；试听副本，支持 Range |
| GET | `/hero`、`/hero/poster` | 设备上的主页背景片与封面（剪辑中心数据目录下的 `hero/hero.mp4`、`hero.jpg`），不属于软件；没有时回答 404，页面改为轮播成员自己的视频 |
| POST | `/versions/{id}/confirm` | 确认这一版（#596，以下各行见“出片后的二次编辑”） |
| POST | `/versions/{id}/recut` | `{requestKey, change}`：回答 202 与 `recut` 任务；项目已有进行中的任务时回答 409 |
| GET | `/versions/{id}/outline` | 这一版成片本身的规划与镜头，用于版本的概要：只读，不建修改稿，没存的修改不在里面。`unmet` 是规划时没找到的内容 |
| GET | `/versions/{id}/draft` | 这一版的修改稿，没有就建：音乐段落与槽、每镜的节拍、锁定、粗预览用的片段、选段起点、配乐要不要转码（`songTranscode`）、修订号、能否撤销 |
| POST | `/drafts/{id}/ops` | `{baseRevision, op}`：`replace`、`move`、`delete`、`lock`、`unlock`、`shuffle`、`revert`、`reset`、`undo` 中的一步，回答改后的修改稿；校验与错误码见“出片后的二次编辑”的“修改稿” |
| GET | `/drafts/{id}/candidates` | `?shot=&q=&size=&scene=&landscape=`：候选节拍与场景的列表，`shot` 是镜号（页面手上有的是镜号）；见“出片后的二次编辑”的“候选” |
| GET | `/drafts/{id}/song` | 粗预览用的配乐副本：浏览器能播的支持 Range；不能播的即时解码成 FLAC，不能跳转，用 `?start=` 指定从哪一秒起 |
| POST | `/drafts/{id}/save` | `{requestKey}`：回答 202 与 `edit` 任务，按当前修订存为新版本；项目已有进行中的任务时回答 409 |
| GET | `/versions/{id}/shots`、`/drafts/{id}/shots` | 镜头清单（CSV） |
| GET | `/projects/{id}/frames/{assetId}` | `?t=` 毫秒：缩略图 |

成片是相册资产，播放与下载都走相册现有的接口。

规划的领取与报告（`claim`、`started`、`abandon`、`end`）只在剪辑的助手套接字上，浏览器调不到。`generate`、`replan`、`drop-assets`、`retry-analysis` 与 `recut`（#596）这五条由 anas-api 先经文件代理登记规划授权再转发（见“规划授权”），登记不了时回答 503 `assistant_unavailable`，请求不转发；`PUT /projects/{id}/song` 由 anas-api 经文件代理读出歌曲再交给剪辑中心（见“配乐”）。所以这六条在 anas-api 里有自己的处理，不只是透传。

## 网页

- **登记**：在 `web/src/desktop/apps.tsx` 登记 `videocut`，标题“剪辑中心”。服务端开关打开后显示入口，不随 VNAS 是否可用而隐藏（#514：VNAS 不可用时上传、浏览和播放仍要能用）。
- **加载**：窗口代码按窗口懒加载，`VideoCutNotices` 在桌面外壳里懒加载，都不计入首屏 JS 预算。
- **页面**：照第八版原型（[`ai/montage_v8/web/index.html`](https://github.com/zhongwater123/A-NAS/blob/feat/video-montage-v5/ai/montage_v8/web/index.html)，在 #454 的分支上，只作参考、不合入 main）的样子，文字按“只写事实和操作”从简。
  - 主页就能剪：标题区的背景先问设备有没有背景片（`/hero`），有就播它，没有就轮流播成员自己的三段视频（播放决策经 TanStack Query 按视频缓存）。下面像原型的文件夹那样列出项目，点一下选中、再点取消，箭头打开；选中项目的配乐、剪辑要求、开始剪辑、进度和两个方案都在主页。
  - 进度按“准备、方案一、方案二”三组并排；只有任务进行中才写“可以关掉窗口”。
  - 两条成片并排，都等成员点播放，不自动播放（相册 `VideoPlayer` 的 `autoPlay` 传 `false`）；更早的版本也能播放。方案一存进相册后马上显示，不等方案二。
  - 失败的任务只显示服务写给成员看的原因，其余诊断信息不上页面，只写“没有剪成”。
  - 项目页列出它的视频，可以追加；选视频用相册的 `PhotoPicker`，只列视频，可选本人或共享图库。配乐与剪辑要求各是一页，返回打开它的地方。
  - **背景片**：`/hero` 把设备上的同一个文件给每一位登录成员看，不只是放它的人；2026-11-19 用户决定保留，作为整台设备的主页背景。没有放背景片时，才轮播成员自己的视频。片子不进仓库与软件包，放法见[演示手册](../runbooks/demo-videocut-center.md)。
- **上传**：复用相册的上传队列 `web/src/photos/uploads.tsx`（并行 2 个、接入传输中心、可重试、处理实况照片），目标是本人的个人图库，按 `LibraryPicker` 的规则找出个人图库。
  - 队列跑完后列出成功与失败，失败的可以重试；只用成功的资产建项目。
  - 单独上传的 MOV 如果补进了图库里在等它的实况照片，上传接口不返回新资产，所以它不进项目（页面行为见规格）。
- **数据获取**：引入 TanStack Query（[#271](https://github.com/zhongwater123/A-NAS/issues/271)），放在懒加载的窗口代码里。执行中每 2 秒轮询一次，窗口最小化或不可见时停止（`shown.ts` 跟随窗口外框的 `hidden`）。创建类请求的 `requestKey` 用 `web/src/api/requestKey`，局域网 HTTP 下没有 `crypto.randomUUID()` 也能用。
- **播放**：成片是相册视频，直接复用 `VideoPlayer`。
- **二次编辑的界面**（#596）：2026-11-24 起按新的交互重新设计，规格随新界面改写；在那之前主线只有服务端，网页没有入口。服务端不依赖界面的布局：规则都在服务端校验，网页只负责显示和发请求，修订号冲突时重新读取修改稿。
- **提示**：主线没有通用的桌面通知中心，剪辑的提示照 `FamilyNotices` 的做法放在桌面外壳里。

## 出片后的二次编辑

确认一版、修改镜头与一句话重剪（[#596](https://github.com/zhongwater123/A-NAS/issues/596)）。为什么这样做见 [ADR 0039](../adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md)，子 issue 见 ADR 的“交付”。界面 2026-11-24 起按新的交互重新设计，规格随新界面改写，本节记的是已经实现的服务端；新界面的子 issue 随 [#632](https://github.com/zhongwater123/A-NAS/issues/632) 另定。在首版闭环（M10）之后实施。

**版本的时间线**：二次编辑都从一个版本出发，所以存回时把这一版的时间线（`fill` 的 `clips`，连同段落、画面与小节槽的划分）写进 `versions.timeline_json`，与写版本行在同一个事务里。之前的版本没有这一列，第一次读这一版（概要、修改稿、镜头清单或重剪）时从它的任务的填槽步骤结果补上；步骤结果也没有时，这一版只能播放，不能修改或重剪。
- 分析、目录与歌曲三步是任务的，两个方案共用：方案一存好后、方案二还在做时移除了视频或素材重新分析过，这几步会重做并覆盖结果，卡片重新编号，方案一的卡片号与节拍号就指向了别的画面。所以读一个版本的规划时，它所来自的规划所在任务的分析、目录、歌曲与这一方案的规划两步，有哪一步在那个方案的版本存好之后又完成过，这一版只能播放，回答 409 `not_editable`，也不能重剪。生成改成一次只出一版（2026-11-24 决定）后，存版本就是任务的最后一步，不会再出现这种情况；已经按两个方案存下的版本仍按这一条判断（[`drafts.go`](../../internal/videocut/app/drafts.go) 的 `redoneSince`）。读版本（修改稿、概要、镜头清单、重剪）与编辑任务的第一步用同一个 `loadPlan` 读这几步，分析只读每个视频的概要与窗口，不读卡片：卡片以目录为准。

**修改稿**
- `GET /versions/{id}/draft` 没有就按版本建一份：从 `timeline_json` 读出音乐段落与槽，每个槽记它首段的节拍（结尾画面的最后一个槽记末段的：结尾选的节拍收尾），修订号 0。槽按成片顺序排列，各记段落号、画面号与起止秒数；`fill` 的 `pins` 也这样指一个槽。
- `POST /drafts/{id}/ops` 分两段。先在事务外读修改稿、比对 `baseRevision`（对不上回答 409 `draft_conflict`），按 [ADR 0039](../adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md) 第 2 项与本节下面几条校验并算出新的状态（规格随新界面改写前，规则以这两处为准），不成立回答 422 `edit_refused`，`message` 是给成员看的原因；重洗的搜索与删除的 `likeness` 也在这一段，不占着事务等相册或计算 Worker。再在一个事务里写入：只在修订号仍是 `baseRevision` 时更新 `drafts`，这期间被别的请求改过就回答 409 `draft_conflict`、什么都不写；写一行 `draft_revisions`；删掉最近 100 个之外、又不在撤销链上的修订。回答新的修改稿。
- 这一版的时间线读不出来时（见上），版本概要、修改稿、每一步操作、候选、镜头清单、存为新版本与重剪都回答 409 `not_editable`。读不出来也包括时间线对不上规划与目录：镜头的卡片不在这一版任务描述过的卡片里、画面不在规划里，或镜头的节拍是它的卡片没有的。
- 打开修改稿时，逐个读卡片所在视频的分析：相册现在已做完的窗口，身份或维度与这一版任务的分析结果不同（换了预处理模型并重新分析过），回答 409 `analysis_changed`，这一版只能播放与重剪。窗口正在重做、还没做完的不算换了模型；相册里已经没有、放进回收站、不再允许授权读取，或内容对象换了的视频，都留给用到它的步骤。`POST /drafts/{id}/save` 建任务前再查一次，打开之后才重新分析的，直接回答 409 `analysis_changed`，不建任务。版本概要、每一步操作与候选不做这一步：读分析要连同全部窗口向量，每次点击都读太重。
- 修改稿每一镜的节拍都要是来源版本的视频里一张卡片的节拍：显示、搜候选、导出镜头清单与存为新版本都按它读描述与视频。有一镜不是的，打开、操作、导出与存为新版本都回答 409 `not_editable`，不建任务。
- 校验只用剪辑中心手上的数据：卡片、节拍与描述来自相册的 `analysis`，横竖来自 `lookup` 的宽高（宽高比小于 16:9 算竖拍，与渲染判断是否垫模糊背景的规则相同），已用的节拍来自修改稿本身。换镜头、拖动时节拍够不够长按 `fill` 的放慢上限（2.5 倍）算；删除一镜时“卡片盖得住两段之和”按不放慢算。
- 这里的校验不知道 `fill` 实际会从卡片的哪一段取画面，所以存为新版本时还要核对一次，见“重新填槽”。
- 删除时两边都够长，调两次计算 Worker 的 `likeness`（被删的一镜分别对前一镜、后一镜），选更像的一边，一样像时选前一镜；只有一边够长时不调。比之前要读这三镜所在视频的分析（连同全部窗口向量），这一步里每个视频只读一次：相邻的镜头多半出自同一个视频。其中有视频已从相册删除或放进回收站时回答 422 `edit_refused`，原因写明这一点；窗口正在重做、还没做完时也回答 422 `edit_refused`，原因是稍后再删；窗口已按别的模型做完时回答 409 `analysis_changed`；相册答不了回答 503 `photos_unavailable`，计算 Worker 连不上回答 503 `compute_unavailable`，修改稿都不变。
- `shuffle` 先用一次 `searchWindows` 查全部没锁画面的 `query`，再按种子挑；种子写进修订，同一种子结果相同。`query` 是规划写的，不是成员输入的：规划留空或超过 200 字的画面不去搜，那几镜保持原样，提示里写明几镜因此没换；没锁的镜头全是这样时回答 422 `edit_refused`。挑的节拍不和前一镜同一张卡片，也不和后一镜现在的卡片相同，不然两镜像没剪开：后一镜可能不变（锁定、没有可以搜索的描述，或者找不到合适的节拍），和已经挑走的节拍留给后面的镜头是同一个道理。搜不了时修改稿不变，错误码见“项目授权”里 `searchWindows` 一行。
- 粗预览的片段随修改稿一起回答：没改过的槽取版本时间线里的镜头，改过的槽取所选节拍的起点与一个槽长。没改过的槽从版本镜头的起点按原速放这一镜的长度，镜头在版本里放慢过、或并了被删镜头的槽时会比卡片剩下的长，这时只放到卡片结尾（也不过视频结尾）。镜头清单的原片起止取同一段。

**候选**：`GET /drafts/{id}/candidates?shot=&q=&size=&scene=&landscape=`
- 依次是同一张卡片里没用到的节拍、`shots` 步骤结果里这个画面的备用节拍、`searchWindows` 的结果。`q` 去掉首尾空白再算字数，和相册一样；写了 `q` 时最多列 60 个；没写 `q` 时不调搜索，按拍摄先后列出筛选下的全部节拍。搜不了时的错误码与重洗相同。
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
| `edit` | 修改稿的一个修订：素材、歌、起点与顺序取来源版本的任务快照 | `source`（读来源版本的规划、分析与目录和修改稿的 `pins`，已不在的视频的卡片退出目录）→ `fill`（带 `pins` 填槽，并在这一步里逐槽核对，见“重新填槽”）→ `render` → `save` | 修改稿 ID、修订号加渲染键 |
| `recut` | 来源版本的任务快照，加这句话 | 分析 → 目录 → 歌曲 → 规划两步（一个方案）→ 填槽 → 渲染 → 存回 | 任务 ID 加渲染键 |

- 一个项目同一时刻只有一个没结束的任务：建任务时在同一个事务里查，已有就回答 409 `job_in_progress`。原来只有 `generate` 这样做，现在三种都是。
- 一个 `requestKey` 只属于一次请求：同一种任务、同一个对象（生成的项目、存盘的修改稿、重剪的版本）再用它，回答原来那个任务；用在另一种任务或另一个对象上回答 409 `conflict`。生成用了存盘或重剪的键也是冲突，不把那个任务当成生成任务回答。
- `POST /drafts/{id}/save` 先按 `requestKey` 找：同一个键回答同一个任务。键没用过时再看这个修订有没有存出过版本：存过就回答 202 与当初存它的那个任务（已完成，带那个版本），不管这次的 `requestKey` 是什么，不建新任务，也不重新填槽、渲染。存失败或被取消的修订没有留下版本，照常新建任务。这一步在查分析和查“项目有没有进行中的任务”之前。
- `edit` 任务只看管各镜首段用到的视频。它们缺失或进了回收站时，照规格停下或暂停，`missing_json` 列出这些视频，页面据此标出用到它们的镜头；`drop-assets` 对 `edit` 任务回答 409，`recut` 任务照 `generate` 去掉后继续。
- 迁移回退：加 `kind` 的那一步，Down 自己删掉 `edit`、`recut` 任务名下的步骤、素材、轮次、规划和它们存下的版本的修改稿。回退时外键是关着的，级联不生效。
- `edit` 与 `recut` 任务不改项目的设置，也不改 `project_assets`。
- 任务的设置从来源版本的任务快照抄来，只留自己这一种的：`recut` 任务不带 `edit` 的 `pins`，改自重剪版本的 `edit` 任务也不带重剪的那句话与 `base`。所以存出的版本是“改自第 N 版”，不再显示那句重剪的话，也不把重剪的 `base` 再存一遍。
- 填槽要读窗口向量。`generate` 与 `recut` 的分析步骤先看过各视频的分析状态；`edit` 任务没有分析步骤，由第一步 `source` 照同一套规则（`analysisReady`）看目录里还有卡片的视频的窗口：没做或正在排队就请相册分析并等待（`analysis`），做失败了停下等成员（`analysis_failed`，可以重新分析；超过时长上限的是 `video_too_long`），本地模型不可用停下（`model_unavailable`）。不会一直等一个没人在做的分析。填槽时窗口又变得没做完或换了内容，按可重试故障处理，任务从第一步重新判断。`edit` 任务读到的窗口已做完、身份或维度却与来源版本的分析结果不同，直接失败，原因与 `analysis_changed` 相同，不渲染也不存回；`generate` 与 `recut` 任务照旧当作分析正在变化、按可重试故障处理。

**重剪的规划**
- `POST /versions/{id}/recut` 加进 `protocol.PlanningRoutes`，成为第五条登记规划授权的请求，行为与另外四条相同（见“规划授权”）。
- brief 多两项：`base` 是来源版本收下的大纲与镜头，`edit` 版本取它所来自的规划，手动改的镜头不带；`change` 是这句话。`base` 取两步规划交回的原文，带各部分与各画面的衔接（`link`）；上一次没找到的内容（`unmet`）不带，由新的一轮自己写。卡片号按视频在任务里的先后编号（`v{n}-{序号}`），重剪任务移除了视频、或视频重新分析过时，卡片重新编号，`base` 里的旧编号会指向别的画面。所以建任务时同时记下 `base` 用到的每张卡片在视频里的位置（`settings_json` 的 `recut.cards`：视频、起止与各节拍的时刻），到出 brief 时按这一次的目录对齐：同一个视频里同一位置的卡片还在，就换成它现在的编号；不在了（视频已移除，或重新切过、重新描述过），就从大纲各场的 `cards` 与各画面的 `beats` 里去掉，一场或一个画面一张都不剩时去掉这一项，模板写明这时照它的 `query`、`shows` 与 `link` 从这一次的卡片里重新挑，其余照 `base` 保留（[`recut.go`](../../internal/videocut/app/recut.go) 的 `alignBase`）。重剪要等素材分析做完才出 brief，所以正在分析的素材也按做完后的卡片对齐。
- 模板 `plan-v3.md` 是 `plan-v2.md` 的重剪版：两步的规则与 v2 逐字相同（[`test_videocut_worker.py`](../../ai/tests/test_videocut_worker.py) 的 `test_the_recut_template_starts_from_base_and_changes_only_what_change_asks` 断言），去掉了出两版、换一种讲法的流程（只出一版），写明 `change` 只当作剪辑要求读，并加“重剪”一节的五条：`change` 没提到的部分、画面、卡片与节拍照 `base` 保留，顺序不变；要改的地方按规则重新安排，可以调整紧挨着的一两个画面；`design` 照 `base` 写，再加一句说明改了什么；要的内容卡片里找不到时照 `base` 保留并写进 `unmet`；`base` 里不合现在规则的地方照规则改。第二步的数据照旧等大纲交回后才给。规划发起方按领取回答里的模板名加载 `<模板>.md`，v2 与 v3 都随软件包装进 `videocut-prompts`，一个任务用哪个由剪辑中心按任务种类给出（生成 v2，重剪 v3）。
- 这句话照“一句话要求”限 200 字：服务端只查它不为空、不超过 200 字（[`recut.go`](../../internal/videocut/app/recut.go)）。“不支持的要求”的词表只在网页里（[`words.ts`](../../web/src/videocut/lib/words.ts) 的 `limitsOf`，写要求的页面在输入框下提示，不拦着继续），而重剪还没有网页入口，所以现在没有哪一处拿这句话对词表。[ADR 0039](../adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md) 第 7 项要它也过词表，这一条还没做，随新界面的 ADR 定在哪一侧做（[#632](https://github.com/zhongwater123/A-NAS/issues/632)）。

**缩略图**：`GET /projects/{id}/frames/{assetId}?t=<毫秒>`
- 资产要在这个项目的素材里；经 `open` 与 FFmpeg `frames` 取一帧 JPEG，长边 320。不是这个项目的资产、相册里已经没有或进了回收站的视频、内容对象换了的视频，都回答 404 `not_found`：没有这一帧，和“不能加进项目的视频”不是一回事。授权已撤销回答 409 `grant_revoked`。
- 用 `Interactive` 优先级：成员在页面上等着看。它不计入剪辑中心同一时刻最多 1 个的后台请求，另设并发上限，初值 4，R3 实测后定。
- 回答 `Cache-Control: private, max-age=31536000, immutable`：内容对象不可变，同一时刻的帧不会变。
- 不存盘，也不经过计算 Worker，所以不会一边占着 FFmpeg 槽位一边等计算 Worker。

**粗预览**
- `GET /drafts/{id}/song` 送出来源版本任务快照里的配乐副本，支持 Range；选段起点在修改稿的回答里。浏览器不能播的歌和配乐页的试听一样即时解码，不能跳转：修改稿用 `songTranscode` 说明，网页从预览起点用 `?start=` 请求，时钟从那里算。
- 原片用相册现有的视频播放。网页用两个 `<video>` 交替，下一段提前载入并跳到起点；以音乐的播放时间为准切换，不等跳转。
- 不渲染，不占 FFmpeg 的后台槽位；相册的转码播放与缩略图用 `Streaming`、`Interactive`，和别人的播放一起排队。

**版本概要**：每一版的剪辑思路、镜头、统计与没找到的内容读 `GET /versions/{id}/outline`，是成片本身的镜头。没找到的内容取两步规划答案的 `unmet`，按先后合并、去掉重复；改过的版本取它所来自的规划。不读修改稿：改过还没存的修改稿和上面那条成片不是一回事，只是看一眼版本也不该建修改稿。

**确认这一版**：`POST /versions/{id}/confirm` 写 `projects.chosen_version_id`，可以改写；项目列表与主页优先用它。不删除任何成片（[决定记录](../decisions/2026-11-19-montage-edit-defaults.md)）。

**镜头清单**：`GET /versions/{id}/shots` 与 `GET /drafts/{id}/shots` 用 Go 的 `encoding/csv` 写，前面加 UTF-8 的 BOM；段落名、备注等文字来自模型，以 `=`、`+`、`-`、`@`、制表符或回车开头的单元格前加一个单引号，电子表格按文字显示而不当公式执行；有哪些列见[决定记录](../decisions/2026-11-19-montage-edit-defaults.md)的“镜头清单的格式”，表头在 [`editing.go`](../../internal/videocut/app/editing.go) 的 `shotListHeader`。

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

| issue | 内容 | 依赖 | 不能拆开的义务 |
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
| M9（[#560](https://github.com/zhongwater123/A-NAS/issues/560)） | videocut MCP 服务器、助手套接字与定时任务单元的挂载、规划授权（文件代理的 `assistant_plan`、anas-api 对四条请求的登记与挂载层去掉同名头）、唤醒服务的规划部分（按 `ActiveState` 判断运行、`StateDirectory` 记计数、单独的运行目录放记号、按成员退避）、`run-requests.sh take` 一并移走它、回合脚本的清理、领取与模型检查、桌面提示。动工前按转正后的布局重写“规划”一节的接线 | M8a、P1、AI 助手 V1 转正 | MCP 服务器、managed-config、定时任务单元单独的成员配置、单元挂载、规划授权、唤醒服务与 Host Agent 按 UID 查询同一个 PR；系统测试覆盖“没有授权就不启动”；把 `create_job`、删除任务与删除运行的会话加进研究文档“以后怎么升级”的核对清单 |
| M10（[#561](https://github.com/zhongwater123/A-NAS/issues/561)） | 网页全流程，打开开关；本方案的状态、`CURRENT.md` | M8a、M8b、M9、AI 助手 V1 转正 | — |
| 实机验收的修复（[#605](https://github.com/zhongwater123/A-NAS/pull/605)、[#607](https://github.com/zhongwater123/A-NAS/pull/607)） | 剪辑分析不再被相册的前台门控挡住、主页不再播放成员的视频、轮次编号的格式；接缝上的六处缺陷（答案上限写进 Schema、选段的帧、规划时限按最后一次调用算等），见[调查](../investigations/2026-11-19-montage-job-stalls-on-the-experimental-nas.md) | M10 | — |
| 规划会话（[#608](https://github.com/zhongwater123/A-NAS/pull/608)） | 规划进程、规划发起方、按原因重试；去掉 M9 的回合脚本、唤醒服务的规划部分与 `managed-cron` | 用户 2026-11-20 的[决定](../decisions/2026-11-20-montage-planning-as-a-vnas-session.md) | 系统测试带真实运行环境走一遍规划 |

- **先后**：P1、P2 已做完。M1–M8b 不碰助手，M0 之后照常推进；allin1 的商业版权待议不挡开发，挡 M10 打开开关；M9、M10 等 [AI 助手 V1 转正](../decisions/2026-11-17-assistant-v1-cloud-model.md)合并。转正之前，规划的端到端只能在系统测试里用替身模型验证。
- 第八版代码在 PR #454 的分支上（2026-11-17）。M6 从提交 `19f2d6db` 复制 `domain/`，并在 PR 描述里写明来源。
- **出片后的二次编辑**：R1–R6 是 [#596](https://github.com/zhongwater123/A-NAS/issues/596) 的子 issue，内容与依赖见 [ADR 0039 的交付](../adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md#交付)，都在 M10 之后。2026-11-24 界面改版后，服务端先合入：R2–R5，把 R2 的 `searchWindows` 接到 R3 的候选与重洗的那一步（交付表里没有这一行，实施时在 [#617](https://github.com/zhongwater123/A-NAS/pull/617) 里叫 R7），以及从 R6 拆出的版本概要、段落标签与配乐能否跳转；R1 只合入本页与术语表中描述服务端的部分，规格与 R6 的网页随新界面重做。
- **整体验收**归 M10：在实验 NAS 上走完规格里的“首版闭环”。各 issue 只对自己那部分的验收负责。

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
| 描述在 NAS 的 CPU 上很慢（按读文字中位 105 秒一张推算） | 一组素材的描述要几个小时；相册自己的 AI 积压跟着推迟；描述进行中成员搜索照片要等一张卡片描述完 | 不设门槛；不等相册空闲，内存不足或设备上其他工作被拖住时让路（2026-11-25 用户[决定](../decisions/2026-11-25-photo-ai-gate-member-work.md)）；实验 NAS 上记录耗时，作为性能评估的输入；以后可经局域网算力（[#196](https://github.com/zhongwater123/A-NAS/pull/196)）分流，只改相册一处 | 实施中验证 |
| 8 GB 内存（目标机型的上限，评审人 2026-11-18 确认）：4B 约 3.5 GiB，FFmpeg 切片上限 3 GiB，再加上相册、Qdrant 和服务本身 | 描述和渲染同时进行时有压力 | 描述在相册的准入里，加载前留出内存余量；剪辑中心一次只渲染一段。实验 NAS 是 16 GB，首版闭环记下的峰值要按 8 GB 的预算比，不能当作够用的证据。2026-11-20 第一次完整出片时，FFmpeg 切片的内存峰值到了 3 GiB 的上限 | 实施中验证 |
| 关键帧超过 12 张的竖拍长卡片 | 超出 8,192 的上下文，这张卡片描述失败、不进目录 | 和第八版行为相同；实验 NAS 上统计失败的卡片数，多了再评估加大上下文 | 实施中验证 |
| 规划会话靠 `hermes serve` 的接口（`/auth/password-login`、`/api/ws` 的 JSON-RPC、`session.title`、`review.summary`）与进程级的 `HERMES_TUI_TOOLSETS` | Hermes 升级时规划可能失效，或规划进程多出看图的工具 | 带真实运行环境的系统测试走一遍规划，并检查模型只拿到规定的工具；照[以后怎么升级](../research/hermes-agent-runtime.md#以后怎么升级)一起核对 | 升级时核对 |
| 规划进程是成员沙箱里的第二个 `hermes serve` | 8 GB 内存更紧 | 规划发起方全机同一时间只规划一个；没有连接 45 秒后进程退出；实验 NAS 上记峰值 | 实施中验证 |
| 改由 VNAS 规划后答案质量下降 | 成片变差 | 在固定样本上人工对比；关键规则放在提示词模板里 | 实施中验证 |
| 规划对话的后台复盘可能把卡片描述或诱导文字写进成员的记忆或技能 | 描述留在助手目录里；诱导在以后的对话里生效 | 用户 2026-11-20 接受（同 2026-10-15 对普通对话的决定）；成员在“记忆”页查看与删除；描述已去掉号码 | 不处理 |
| [FFmpeg 槽位与内存的决定](../decisions/2026-10-21-ffmpeg-worker-slots-and-memory.md)仍是 pending | 渲染并发和内存上限可能再变 | 剪辑只用 `Background`，跟着最终数值走 | 不处理 |
| 窗口集合首版没有读者 | 多一份同步、对账与内存 | 按用户决定照 #514 做；不建 HNSW。二次编辑的文字搜镜头是第一个读者（#599） | 不处理 |
| 剪辑渲染与剪辑分析同时占着全机仅有的 2 个后台 FFmpeg 槽位 | 缩略图、影视与音乐扫描变慢 | 各自同一时刻只发 1 个；跟着 FFmpeg 槽位的决定调整 | 实施中验证 |
| 黄金比对的样本来自真人素材，不能进公开仓库 | 新会话无法在仓库里复现比对 | 样本留在开发机，测试按环境变量读取、没有样本时跳过；PR 写明位置与摘要 | 不处理 |
| 第八版代码在 PR #454 的分支上（2026-11-17） | 来源不稳定 | 固定从 `19f2d6db` 复制，PR 描述写明来源 | 不处理 |
| 单文件 4 GiB、不能断点续传 | 大文件上传失败要重传 | 上传队列逐个重试；续传另开 issue | 不处理 |

## 验证

验收标准与证据层见[规格](../specs/videocut-center.md#验收证据)。检查命令：
- 文档：`make docs-check`；
- 代码：`make check`、`make race-test`（任务调度是并发代码）、`scripts/system-test.sh`；
- 改了契约要运行 `make api`。

### 首版闭环的基线

2026-11-20 在实验 NAS（16 GB）上第一次完整出片，规划用 DeepSeek。素材 3 段视频，共 5.9 GB、638 秒；歌曲选段约 1 分钟。分析在此之前已做完，不在下表。

| 步骤 | 用时 | 资源 |
|---|---|---|
| 规划（一次对话，两个方案） | 3 分 37 秒；第一次交大纲在 2 分 49 秒；复盘在结束后 49 秒报告 | 模型网关不记 token 用量 |
| 填槽（每个方案） | 1 分 54 秒、1 分 57 秒，几乎全在 FFmpeg 取帧 | — |
| 渲染（每个方案） | 72 秒、70 秒 | 约 30 个 FFmpeg 实例，合计约 300 秒 CPU，单个 430–545 MB；FFmpeg 切片的内存峰值到了 3 GiB 上限 |
| 存回相册 | 约 1.4 秒 | 成片 104 MB 与 94 MB |

这些是 16 GB 机器上的数，按 8 GB 的预算比还不够（见“风险”）；规格“首版闭环”的第 2、3 步（刷新恢复、重启后不重做）还没做。

## 关联

- **需求与原型**：
  - 需求 [#514](https://github.com/zhongwater123/A-NAS/issues/514)；原型差异 [#515](https://github.com/zhongwater123/A-NAS/issues/515)；路线与外发约束 [#295](https://github.com/zhongwater123/A-NAS/issues/295)；资源条件 [#341](https://github.com/zhongwater123/A-NAS/issues/341)；
  - 原型代码 PR [#454](https://github.com/zhongwater123/A-NAS/pull/454)，[第八版说明](https://github.com/zhongwater123/A-NAS/blob/19f2d6db/ai/montage_v8/README.md)。
- **决定**：
  - [剪辑中心首版的数据归属与几项做法](../decisions/2026-11-17-montage-center-first-version.md)；
  - [剪辑中心的规划怎样交给 VNAS](../decisions/2026-11-20-montage-planning-as-a-vnas-session.md)；
  - [方案中补充的默认做法](../decisions/2026-11-17-montage-center-default-rules.md)；
  - [相册放视频，视频不参与 AI](../decisions/2026-10-19-videos-in-the-photo-library.md)。
- **ADR**：
  - 本方案的 [0038](../adr/0038-keep-montage-media-in-the-photo-library.md)；
  - 相关的 [0001](../adr/0001-use-go-for-product-services.md)、[0006](../adr/0006-use-a-managed-photo-library.md)、[0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)、[0017](../adr/0017-run-the-media-center-through-the-file-broker.md)、[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)、[0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)、[0029](../adr/0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)、[0037](../adr/0037-plug-removable-modules-in-as-separate-services.md)。
- **架构**：[规划会话](videocut-planning-session.md)、[配乐](videocut-music.md)、[总览](OVERVIEW.md)、[家庭中心](family-center.md)、[相册 AI](photo-ai.md)、[OCR 方案](photo-ocr.md)、[向量索引](vector-index.md)。
- **规格**：[剪辑中心](../specs/videocut-center.md)、[相册视频](../specs/photo-video.md)、[AI 助手](../specs/ai-assistant.md)。
- **代码**：
  - 服务与授权：[`internal/familyservice`](../../internal/familyservice/service.go)、[`internal/photoservice/assistant.go`](../../internal/photoservice/assistant.go)、[`internal/photoservice/gate.go`](../../internal/photoservice/gate.go)、[`internal/photos/jobs.go`](../../internal/photos/jobs.go)；
  - Worker 与协议：[`internal/ffmpegworker`](../../internal/ffmpegworker/protocol.go)、[`internal/unixmsg`](../../internal/unixmsg/unixmsg.go)、[`internal/aiworker`](../../internal/aiworker/client.go)、[`ai/anas_ai/protocol.py`](../../ai/anas_ai/protocol.py)；
  - 助手：[`ai/assistant/managed-config.yaml`](../../ai/assistant/managed-config.yaml)、[`internal/assistantapi`](../../internal/assistantapi/rpc.go)；
  - 规划：[`videocut-planner.py`](../../ai/assistant/videocut-planner.py)、[`anas_videocut_mcp.py`](../../ai/assistant/anas_videocut_mcp.py)、[`planning.go`](../../internal/videocut/app/planning.go)、[`assistant-units.sh`](../../scripts/lib/assistant-units.sh)、[`assistant_plan.go`](../../internal/filebroker/rootbroker/assistant_plan.go)。
- **调查**：[剪辑任务在实验 NAS 上先停在“等待分析”，再停在“等待 VNAS”](../investigations/2026-11-19-montage-job-stalls-on-the-experimental-nas.md)。
