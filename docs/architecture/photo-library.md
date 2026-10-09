# 相册技术设计

状态：M1 切片 1–6 已实现；切片 7 实机闸门、M2 与 M3 未开始；模型质量与资源参数待基准冻结。部署与验收进度见[当前状态](../status/CURRENT.md)。

本设计落实[相册规格](../specs/photo-library.md)。受管图库的长期边界由 [ADR 0006](../adr/0006-use-a-managed-photo-library.md) 决定，相册服务的身份、存储位置与授权方式由 [ADR 0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md) 决定；模型与 Runtime 的候选证据见[本地照片 AI 研究](../research/photo-ai-model-runtime-selection.md)。

## 开发就绪结论

当前粒度足以实现目录、原图存储、导入、浏览、回收站、重复组、派生任务和 Fake AI Provider。这些能力只依赖已经确认的照片资产、图库归属、稳定 ID 与生命周期，不需要等待真实数据盘或最终模型。`internal/photos` 是与进程无关的库，开发模式由产品服务进程内运行，生产由相册服务进程运行。

以下事项作为发布闸门并行推进，不阻塞核心链路编码：

- 账号、会话、角色和管理员查看授权由 [ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md) 提供，相册服务经 Host Agent 的会话查询接口（`internal/sessionlookup`）确认身份，照片资产权限由 Catalog Policy（`internal/photos/policy.go`）判定。
- 本地目录 Adapter、临时卷与系统测试只验证契约，不能替代 Experimental NAS 真实数据卷上的断盘和恢复验收（切片 7）。
- AI 模型已有基线候选，但质量、处理速度、峰值内存、温度和前台影响必须通过代表性图库基准后才能成为发布默认值。
- RAW、Live Photo 和视频的完整格式矩阵仍需样本验证；不影响 JPEG/PNG 核心切片。

## 组件与数据流

```text
Web 相册 / 文件管理图库投影
   │ 会话 Cookie + CSRF
   ▼
anas-api（a-nas）── 浏览器边界；转发 /api/v1/photos 与会话令牌
   │ UDS                          │ 导入来源：文件代理以用户本人身份打开，只读 fd
   ▼                              ▼
anas-photos（a-nas-photos）◄──────┘
   ├── 会话查询 ──► Host Agent（只返回账号、角色、状态与查看授权）
   ├── Policy（所有者、共享成员、上传者、管理员、查看授权）
   ├── SQLite Catalog ─┐
   ├── Object Store ───┼── 数据卷 photos 子卷（仅 a-nas-photos 可访问）
   ├── 媒体派生 ───────┘   缩略图、兼容预览、元数据；不等待空闲
   └── AI Job Coordinator
          │ UDS；只读 fd + 期望版本；不授予任何路径访问
          ▼
   AI Worker（Python，无数据卷权限、无网络）
          │ versioned result
          ▼
   Derived Result Store → 文本检索 / Exact Vector Search → Policy 过滤
```

`anas-photos` 中的 Photo Library Module 是权威写入边界。AI Worker 不能直接修改照片目录、权限、用户标签或原图，只能返回带版本的候选派生结果；Module 验证任务租约、照片资产状态与结果版本后再提交。只读 SMB/NFS 的发布方式尚未决定，见“开发切片”。

## 模块接口

`internal/photos` 由一个较深的 Module 封装以下 Implementation：

- `Import`：流式校验、计算内容哈希、持久化不可变原图，并原子创建照片资产和派生任务。
- `Get`、`Open`、`Thumbnail`、`Timeline`、`ListDirectory`：只接受调用方身份与稳定 ID，不接受调用方拼接的底层路径；列表按游标分页。
- `Update`、`Copy` 与目录的 `CreateDirectory`、`UpdateDirectory`、`DeleteDirectory`：实施虚拟组织与独立照片资产语义，改名与移动在一个事务中完成。
- `Trash`、`Restore`、`Purge`、`EmptyTrash`：实施图库与上传者权限、15 天期限和最后引用回收。
- M2 计划：`Search`、相册与加入相册，以及保存用户标签、AI 纠错和手工位置的用户元数据接口（切片 11），不与派生结果混写；人物名称随 M3 的人物库实现。

首版不为目录表、对象路径、SQLite、OpenVINO 或某个向量扩展建立公共 Interface。真正需要 Adapter 的边界只有外部环境或昂贵依赖：受管存储目录、媒体解码、AI Provider、时钟和 Policy。

## 持久化与崩溃一致性

### 目录

首版目录、权限关系、回收站期限、任务、用户元数据和派生结果使用一个位于数据卷 `photos` 子卷的 SQLite 数据库。启用 WAL、外键和事务；不把该数据库放在 SMB/NFS 等网络文件系统，也不额外引入 PostgreSQL、Redis 或消息队列。Catalog 随原图一起备份和恢复，因此 schema 使用 `PRAGMA user_version` 版本化 migration，而不是其他模块的 `CREATE TABLE IF NOT EXISTS` 加补列方式。

关键身份分为：

- `library_id`：私有或共享图库。
- `asset_id`：用户可见照片资产的稳定 ID。
- `object_id`：原文件字节 SHA-256；只用于内部对象定位和完全重复判断。
- `derivation_id`：能力、模型摘要、预处理版本和 Pipeline 版本的组合。
- `job_id`：可租约、重试和幂等执行的派生任务。

### 原图提交

1. 在 `photos` 子卷的 `staging/` 流式写入并同时计算 SHA-256，不先把整个文件读入内存；写入过程中按块检查大小上限与容量保留，前 8 字节判定 JPEG/PNG。
2. 刷新文件后只读取图片头校验格式与像素上限，再设为只读并以硬链接发布到 `objects/<2 位>/<2 位>/<SHA-256>`，随后刷新所在目录；同名对象已存在时直接复用，从不覆盖。
3. 在单个数据库事务中创建对象行与独立照片资产；重复组不单独存表，由同一图库内引用同一对象的未删除资产推导。
4. 发布对象与引用它的事务、清除最后一个引用与删除对象文件，都在同一把提交锁内成对完成，因此清除不会删掉刚被导入复用的对象。崩溃后由对账删除不在写入中的 staging 文件与没有对象行的对象文件，并报告有引用但文件缺失的原图；任何自动回收都不能触碰仍有引用的对象。

对象路径不是资源身份。备份和恢复必须同时覆盖对象、目录以及二者的校验清单。实现见 [`internal/photos`](../../internal/photos/photos.go)。

## 后台任务与资源隔离

SQLite 任务表保存输入、派生版本（如 `thumbnail/v1`）、状态、尝试次数、`lease_until`、下次可执行时间和稳定错误类别。Worker 以短租约领取单张、单能力任务；重复执行由唯一约束收敛为同一当前结果。只依赖原图字节的派生（缩略图、基础元数据，以及未来的 Embedding）以 `(object_id, derivation)` 为键，副本与重复照片共用；依赖图库设置或用户数据的派生才以照片资产为键。派生文件位于 `derived/<派生版本>/`，与原图分开统计和清除；最后一个引用被清除时随原图一起删除。

任务分为两类，共用同一张表和租约语义：

- **媒体任务**：缩略图、兼容预览和基础元数据，由 `anas-photos` 在上传后立即执行，不等待空闲条件，也不依赖 AI Worker；低优先级由 `anas-photos.service` 的 `CPUWeight=20` 与 `IOWeight=20` 实现，只在争用时让出 CPU 与磁盘。尺寸、EXIF 方向与拍摄时间在导入时只读文件头获得：尺寸用 Go 标准库 `image.DecodeConfig`，EXIF 用 [imagemeta](https://github.com/evanoberholster/imagemeta)（MIT，同时覆盖后续的 HEIC 与常见 RAW）；无时区偏移的拍摄时间按 NAS 本地时区解释。JPEG/PNG 缩略图用 [imaging](https://github.com/disintegration/imaging)（MIT，纯 Go）先缩放后按方向转正，再合成白底编码为 JPEG；像素上限在导入时已检查。HEIC、RAW 和视频依赖的 C 解码器（libheif、FFmpeg 等）在无网络、受内存限制的子进程中运行。媒体任务未完成或失败时，JPEG/PNG 直接显示原图。进程内解码的 panic 按 `undecodable` 记为永久失败；每次领取都计入尝试次数，租约连续 3 次到期（例如解码反复拖垮进程）后任务以 `interrupted` 失败，不再无限重领。
- **AI 任务**：Embedding、标签、人脸和描述，只由 AI Worker 执行并遵守下述空闲与资源策略。

默认执行策略：

- 连续 5 分钟无前台活动后才领取新任务，并发 1，每次只让一个模型族常驻。前台活动来自相册与文件 API 的用户请求（不含状态栏等定时轮询）；SMB 传输不经过产品服务，因此同时参考 Host Agent 的磁盘与网络吞吐。
- 检测到上传、下载、播放、交互、内存压力、I/O 压力或温度上限后立即停止领取新任务；已经开始的单张原子推理允许完成。
- 初始 systemd 预算为 `CPUQuota=200%`、`MemoryHigh=2G`、`MemoryMax=3G`、低 CPU/I/O 优先级；这些是安全起点，不是实机结论。
- OOM、进程退出和重启只会让租约到期并重试，不得影响原图与基础相册。

## AI 模型基线

模型名称属于可升级配置，不进入产品 API。首版候选如下：

| 能力 | 首选基线 | 选择理由 | 冻结条件 |
|---|---|---|---|
| 中文语义向量与开放标签 | 已选定 Google EmbeddingGemma 2 全模态 740M 官方 LiteRT 包（2026-10-09 决定） | Google DeepMind 于 2026-10-06 发布、支持 100+ 语言，许可待核对（见 [M2 实施方案](photo-ai.md)）；全模态包约 485 MB，440M 文本+视觉包约 388 MB，官方列出图片检索与图片分类用途；零样本标签依赖图文同一向量空间的相似度，官方未给出中文图文指标 | 作为首个集成目标；中文家庭照片质量、Debian x86 Runtime、RSS、查询延迟与吞吐达标后冻结为发行默认；发布过新必须保留回退 |
| 人脸检测与聚类向量 | Open Model Zoo `face-detection-retail-0004` + `landmarks-regression-retail-0009` + `face-reidentification-retail-0095` | 三段均有 Apache-2.0 模型清单、模型很小并直接运行于 OpenVINO | 在家庭合照、侧脸、儿童成长和误合并样本上校准阈值；不满足质量则不默认发布 |
| 按需中文描述 | 高置信标签、时间和地点的可追溯结构化摘要 | 无需常驻 VLM，中文稳定，可逐项说明事实来源并避免把幻觉写入检索事实 | 小型可商用 VLM 通过中文质量、3 GB 上限和响应时间基准后，才能替换为自由生成 Provider |

图文 Embedding 模型不直接输出固定类别。系统维护版本化中文标签词表，为每个标签预计算文本向量，按标签独立校准阈值，只展示超过阈值的少量候选；不能用一个全局阈值承诺识别所有物体。“猫”“电动车”“3D 打印机”即使没有形成可见标签，仍可通过文本与图片向量相似度参与语义搜索。

Google EmbeddingGemma 2 已选定为首个集成目标：它在本设计更新前一天发布，支持中文在内的 100+ 语言；2026-10-09 决定使用包含音频编码器的全模态 740M 包；其规模与官方本地 CPU 定位都比 2B/3B 候选更符合 8 GB NAS。但“适合消费硬件”不等于已经在 Debian 13 x86 上通过 A-NAS 门禁；官方跨平台量化内存数字也不能代替本机加载、吞吐和长时间运行实测。因此 SigLIP2 Base 保留为成熟回退，EmbeddingGemma 2 通过门禁后才冻结为发行默认。

Qwen3-VL-Embedding-2B 继续作为有官方 OpenVINO INT4 路线的成熟质量与 Runtime 对照。腾讯微信视觉团队于 2026-08 发布的 WeMM-Embedding-2B 基于 Qwen3.5，并报告了高于 Qwen3-VL-Embedding-2B 的 MMEB-v2 结果，但模型仓库实际标为约 3B、BF16 权重约 5.44 GB，官方示例以 CUDA 为主，尚无适合本项目的 x86 CPU 量化交付路线，所以只在开发机上定义新一代质量上限，不要求安装到目标 NAS。Chinese-CLIP RN50 只保留为轻量中文对照。

人脸向量与聚类结果属于派生数据；人物实体、用户命名和人工合并/拆分是用户数据。重新聚类只能提出关联，不能删除或改写已经确认的人物名称。首个开发切片先保存稳定 `face_occurrence_id` 和向量，聚类算法在样本基准后冻结。

生成式 VLM 不进入 8 GB 设备的首版默认后台队列。它的推理成本与自由生成长度相关，且会引入幻觉、中文质量与模型许可问题；这与只做一次固定维度向量或小分类模型不是同一资源级别。

## Runtime 与进程边界

- Go 相册服务 `anas-photos` 负责所有权威状态、权限、任务租约、结果提交和检索组合；`anas-api` 只承担浏览器边界与转发。
- Python AI Worker 负责模型预处理与推理，通过只在本机可用的窄协议接受 `capability`、经 `SCM_RIGHTS` 传入的只读文件描述符和期望版本，返回结构化结果。Worker 使用独立系统身份，systemd 起始约束为无网络（`PrivateNetwork=yes`）、不可访问数据卷、`MemoryMax=3G`、`CPUQuota=200%` 与低 CPU/I/O 优先级。
- Worker 的 Python 依赖以锁定哈希的离线 wheel 随发行制品分发，模型文件附带来源、许可证、SHA-256 与预处理版本清单；AI 组件可以未安装，此时相册显示“智能处理不可用”，基础相册不受影响。
- EmbeddingGemma 2 的全模态 740M 官方 LiteRT 包经 MediaPipe Universal Embedder 运行（LiteRT-LM 自身的 Python API 没有向量接口，见 [M2 实施方案](photo-ai.md)），并用 Transformers/Sentence Transformers 结果抽样核对量化正确性；人脸模型优先使用 OpenVINO CPU Provider。不做 OCR（2026-10-09 决定）。UHD 730 仅作为后续实机对照，不把 GPU 驱动可用性作为首版前提。
- Debian 13 不在当前 OpenVINO 官方支持发行版列表中，因此必须在目标系统完成离线安装、模型加载、连续运行和服务重启测试；失败时回退 ONNX Runtime CPU，不能让 Runtime 兼容性阻塞基础相册。
- 模型文件必须随清单记录来源、许可证、SHA-256、预处理版本与输出 schema；安装后离线运行，不在推理时访问互联网。

## 搜索设计

结构化查询、文件名、用户标签和 AI 标签使用 SQLite 与 FTS5。语义向量按 `derivation_id` 分代保存；在 20,000 张照片基线下，首版把当前代归一化向量加载到进程内，针对调用方有权访问的图库执行精确余弦扫描，不引入独立向量数据库。

以 768 维 `float32` 估算，20,000 个图片向量原始数据约 59 MiB；即使候选使用 2,048 维也约 156 MiB，仍适合以简单、可校验的精确索引起步。模型进程内存而不是向量表大小才是 8 GB 设备的主要约束。只有实测规模或延迟不达标时，才在相册 Module 内增加 ANN Adapter。

排序组合语义分数、用户标签、AI 标签、时间与地点过滤。Policy 必须在候选生成前约束图库范围，并在返回前再次验证资产可见性，索引命中本身不构成授权。

FTS5 在搜索切片中按实测决定是否采用：`mattn/go-sqlite3` 需要在所有构建、测试与 vet 目标中加入 `sqlite_fts5` 构建标签；默认 `unicode61` 分词不切分中文，`trigram` 又无法匹配“猫”“海边”这类一至两个字的查询。标签使用独立的关系表精确匹配；文件名在 2 万张规模下可以先用 Go 侧中文二元切分或直接扫描，延迟不达标时再引入 FTS5。

## 开发切片

按三个可分别验收的里程碑推进。M1 不含 AI，可以独立发布；相册代码以已合并的 ADR 0008 为基线。

**M1 基础相册（JPEG/PNG）**

1. **Catalog**：图库、资产、对象引用、重复组、虚拟目录和回收站；版本化 migration 与 Policy 表格测试（所有者、其他成员、管理员、持有查看授权的管理员 × 私有图库、共享图库中自己或他人上传的照片 × 各操作）。
2. **Managed storage**：流式导入、staging 刷新后原子发布并刷新目录、内容对象复用、崩溃对账、容量保护（沿用文件服务“保留 5% 且至少 10 GiB”）、故障注入测试和临时目录 Adapter；不依赖真实数据盘。
3. **媒体派生与任务表**：缩略图、EXIF 方向与基础元数据，以及后续 AI 共用的持久任务、租约与派生版本。
4. **Read path 与 Web**：列表、原图读取（Range）、缩略图、虚拟目录、共享图库复制、回收站与 15 天到期清除 API；更新 OpenAPI 并补充相册路由的契约测试；单文件上限 256 MiB（见规格）；Web 桌面启用现有“相册”入口。`internal/photosapi` 不自行认证，只信任包装它的一方放入的 `photos.Principal`：开发模式下产品服务从会话构造并检查 CSRF；切片 5 起由相册服务经 Host Agent 会话查询构造。原图与缩略图以 `private, no-cache` 返回，访问结束后浏览器必须重新验证；验证器是强 ETag（原图为内容哈希，缩略图另含派生版本），不用导入时间。目录列表与时间线一样按游标分页。
5. **服务化与部署**：`anas-photos` 进程与 systemd 单元、`a-nas-photos` 固定身份、Host Agent 创建 `photos` 子卷并修复漂移、会话查询接口、`anas-api` 转发、`make ops-check` 与部署手册。实现要点：
   - 相册服务是同一 `anas-api` 二进制的 `photo-service` 子命令，由 `anas-photos.service` 以 `a-nas-photos` 运行，因此发布制品仍只校验两个二进制；拒绝以 root 运行。
   - 安装器以固定 UID/GID 31000 创建 `a-nas-photos`，不接管已被占用的名称或 ID；Host Agent 只在该账号存在时创建 `photos` 子卷。存储区修复与空间对账分开执行：账号冲突等失败只记录 `photo store repair failed` 并让相册不可用，不阻止 Host Agent 启动，也不影响空间、账号与查看授权。
   - 相册服务在 `/run/a-nas-photos/photos.sock`（`0660`，目录 `0750`）提供相册 API；产品服务账号属于 `a-nas-photos` 组，只能连接该套接字，进不了存储区。
   - 产品服务在浏览器边界检查 Cookie 与 CSRF，转发时去掉 Cookie，只附带会话令牌头；相册服务把令牌交给 Host Agent 的 `/run/a-nas-sessions/photos.sock` 换取账号、角色和是否需要改密，不读取 `control.db`。
   - 存储区未就绪（数据卷未挂载或 `photos` 尚未创建）时相册服务只回答 `photos_unavailable`，从不自行创建目录；就绪后再打开 Catalog 并启动缩略图任务、对账与回收站到期。
   - 部署与回滚见[启用相册服务](../runbooks/enable-photo-service.md)。
6. **多用户权限**：私有图库管理员查看、共享图库上传者与管理员权限、跨成员重复提示，以及列表、缩略图、计数和错误信息的泄漏测试。实现要点：
   - 查看授权与个人空间共用 `viewing_grants` 表，以 `scope` 区分 `space` 与 `library`。私有图库授权不调用 Host Agent 改 ACL。
   - Host Agent 的会话查询把管理员未过期的私有图库授权随身份一起返回，相册服务据此构造只读 `Principal`；开发模式由产品服务直接读取同一张表。
   - 图库记录所有者用户名（migration 3），供查看横幅与跨成员重复提示使用。
7. **M1 实机闸门**：真实数据卷上的强制终止与断电对账、卷离线、容量不足，以及 4 名成员、20,000 张合成照片的列表与权限性能；实机步骤见[验收相册 M1](../runbooks/accept-photo-library-m1.md)。实现要点：
   - 相册服务运行期间按存储检查的同一间隔（2 秒）复查存储区：数据卷离线、只读（Btrfs 遇到设备错误后会自行转为只读）或路径指向另一个目录（卷被重新挂载）时，立即改为回答 `photos_unavailable`，停止后台任务并关闭 Catalog，记录 `photo store lost`；存储区恢复后重新打开 Catalog，并经启动对账清理中断留下的内容。已打开的 Catalog 锚定在原目录上，路径失效后也不会写到系统盘。
   - 本地冒烟在 device-mapper 设备上的 Btrfs 中以 `error` 目标注入磁盘故障，并覆盖懒卸载后重新挂载与上传中途强制终止相册服务；导入中途卷满由 Module 测试覆盖，拒绝后不留下 staging、对象或照片资产。断电与真实热拔插只能实机完成。
   - 20,000 张的实机规模与缩略图积压测试于 2026-10-08 决定暂缓，先跑通整体闭环；本地基线保留为可选测试，供恢复该闸门时使用。规模基线 `TestScaleBaseline` 经真实导入路径写入 4 个私有图库与共享图库各 4,000 张（共 20,000 张，含跨成员重复、目录与回收站），再计时桌面会发出的读取。2026-10-08 在开发机（14 核、WSL2）上所有读取的中位数都在 4 ms 内：时间线首页 0.9 ms、第 21 页 1.5 ms，根目录列表首页 3.2 ms（按名称排序整个根目录，是最慢的路径，暂不需要新索引），回收站 0.7 ms；导入 6.1 ms/张。读取不是瓶颈，实机要测的是 HDD 上的导入速率和缩略图积压：`TestScaleThumbnailCost` 中一张 12 MP JPEG 的缩略图在开发机上需 160 ms，20,000 张约 53 分钟，i3-12100 且 `CPUWeight=20` 时预计数倍于此。

**M2 本地 AI 检索**

实施方案见[相册本地 AI（M2）](photo-ai.md)；开发期只在开发机验证，以 PR 提交，不部署到 Experimental NAS。

8. **AI contract**：Worker 协议与描述符传递、Fake AI Provider、空闲与资源门控，以及“AI 未安装、停止、崩溃或积压”时的基础相册测试；可与切片 3 并行。
9. **Model benchmark**：先在开发机的 Debian 13 环境中经 MediaPipe Universal Embedder 完成模型离线加载、编码、RSS 与延迟冒烟测试，再用公开中文标注数据集设定标签初始阈值并冻结模型清单；实机数字在部署后补充。家庭照片的人工标注暂缓，见“模型基准与发布门槛”。
10. **Worker 打包**：离线依赖、模型清单与 systemd 沙箱。
11. **相册与用户元数据**：相册实体与加入相册（创建独立照片资产副本），以及用户标签、AI 纠错和手工位置的 migration、Policy、API 与 Web；人工人物名称随切片 13 的人物库实现。2026-10-08 从 M1 移出：基础相册不依赖它们，而用户标签与 AI 纠错首先服务于检索。
12. **Search**：精确向量检索、受控中文标签、用户标签与 AI 纠错、文本检索、Policy 前后过滤。

**M3 人物、格式、导入与发布**

13. 人脸 occurrence 与分图库人物库。
14. HEIC、GIF、RAW、Live Photo 与视频；视频播放兼容与是否转码需先决定。
15. 从个人空间或共享文件夹导入（经文件代理以用户身份传入描述符）与 USB 导入。
16. 文件管理图库投影与只读 SMB/NFS；后者的发布方式需另行决策。
17. 账号删除时的图库转移、导出与待删除，私有图库 AI 开关与派生数据清除，备份恢复，以及 4 名成员、20,000 张照片、2,000 个视频的完整验收。

USB 存储识别与挂载、账号删除和备份目前都不是已有产品能力，作为独立前置工作推进；它们未完成时，切片 15 的 USB 部分与切片 17 只能交付不依赖它们的部分。

## 模型基准与发布门槛

基准集使用至少 1,000 张具有授权的代表性家庭照片，并保留不参与阈值调节的独立测试集；至少包含 100 条中文自然语言查询、常见物体与场景标签真值、含中文文字照片以及同人跨年龄/姿态的人脸样本。

家庭照片的人工标注暂缓。在其完成前：

- 资源与前台影响指标（吞吐、峰值 RSS、温度、前台 API p95）使用无需标注的照片在实验 NAS 上测量。
- 中文检索与标签质量先用公开中文图文数据集（如 COCO-CN、Flickr30K-CN 及带中文类别名的 ImageNet）评估，并据此设定每个标签的初始阈值；这些数据只在开发机使用，不随发行包分发，使用前逐一核对许可。公开数据与家庭照片存在领域差异，词表中公开数据未覆盖的标签（如“3D 打印机”）不展示为 AI 标签，只参与语义检索。
- AI 标签按保守规则展示：只显示超过初始阈值、且与次优标签拉开差距的少量候选，并始终标明来源与置信度；语义检索按相似度排序，不依赖阈值。
- 本节的家庭照片质量门禁保持未通过状态，不以公开数据集结果代替。

必须记录：

- 语义检索 `Recall@10`、人工可接受率和典型失败查询。
- 每个可见 AI 标签的 precision/recall 与独立阈值，重点控制错误标签展示率。
- 人脸漏检、误检、同人拆分和异人误合并；误合并权重高于漏合并。
- 单张与整库照片/分钟、首轮建库时间、Worker 峰值 RSS、CPU、I/O、温度与功耗。
- Worker 空闲、运行、停止领取任务和完成当前原子任务时的前台 API p95 延迟。

目标 NAS 的部署门禁按 EmbeddingGemma 2、Qwen3-VL-Embedding-2B、SigLIP2 Base、Chinese-CLIP RN50 的顺序验证；开发机资源允许时另跑 WeMM-Embedding-2B BF16，定义中文质量上限但不把它当作 8 GB 部署候选。任何候选只要许可证不能确认、峰值 RSS 超过 3 GB、导致前台明显卡顿或中文质量不达标，就不成为默认模型；功能可以显示为等待兼容模型，而不是降低基础相册可靠性。

## 关联

- [架构总览](OVERVIEW.md)
- [相册规格](../specs/photo-library.md)
- [受管图库 ADR](../adr/0006-use-a-managed-photo-library.md)
- [相册服务身份与 Catalog 授权 ADR](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)
- [统一身份与文件授权规格](../specs/unified-identity-and-file-acl.md)
- [本地照片 AI 模型与 Runtime 研究](../research/photo-ai-model-runtime-selection.md)
- [领域语言](../../CONTEXT.md)
- 代码：[`internal/photos`](../../internal/photos/photos.go)；Policy 见 [`policy.go`](../../internal/photos/policy.go)，缩略图任务见 [`jobs.go`](../../internal/photos/jobs.go)，崩溃对账见 [`reconcile.go`](../../internal/photos/reconcile.go)；接口见 [`internal/photosapi`](../../internal/photosapi/handler.go) 与 [`api/openapi.yaml`](../../api/openapi.yaml)
- 测试：[权限矩阵](../../internal/photos/policy_test.go)、[生命周期](../../internal/photos/service_test.go)、[缩略图与 EXIF](../../internal/photos/media_test.go)、[迁移](../../internal/photos/migrate_test.go)、[崩溃对账](../../internal/photos/reconcile_test.go)、[跨成员泄漏与重复提示](../../internal/photos/leak_test.go)、[API](../../internal/photosapi/handler_test.go)、[端到端冒烟](../../scripts/smoke-photo-service.sh)
