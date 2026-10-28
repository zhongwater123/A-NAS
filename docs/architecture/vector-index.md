# 向量索引技术设计

状态：第一阶段实现中：基础设施、相册向量同步与影子比对（[ADR 0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)，proposed）
更新时间：2026-10-28

本页说明向量索引如何运行、交付、同步与验证。决定与权衡见 ADR 0028；相册的检索语义仍以[相册本地 AI](photo-ai.md#语义搜索)为准。

## 边界

```text
所属服务（例如相册，a-nas-photos；PrivateNetwork，只许 AF_UNIX）
  │ Catalog（SQLite，事实源）── 触发器 ──► vector_outbox
  │ 后台同步 / 对账 / 影子查询
  │ gRPC over Unix socket：/run/a-nas-vectors/<账号>.sock（0600，属于该账号）
  ▼
anas-vectors-proxy@<账号>.socket / .service（systemd-socket-proxyd，按连接拉起，空闲退出）
  │ 加入实例的网络命名空间，转发到 127.0.0.1:6334
  ▼
anas-vectors@<账号>.service（Qdrant，动态用户 a-nas-vec-<账号>）
  ├── 私有网络命名空间：只有 lo；看不到数据卷与 /var/lib/a-nas
  ├── 存储：/var/lib/a-nas-vectors/<账号>/（系统盘，可随时删除）
  └── 无人需要时停止（StopWhenUnneeded）
```

- 索引只是派生数据。所属服务可以在任何时候清空它，并从自己的 Catalog 重建；Catalog 的备份与恢复不涉及索引。
- 每个持有向量的服务各有一个实例，只有它自己能连（[ADR 0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md) 决定 2）。其他服务即使与它同组也连不上：套接字是 `0600`，不靠组权限。
- 索引命中不构成授权。点上只有内容 ID 与范围键，查询范围由所属服务的 Policy 给出，结果返回前仍按 Catalog 复核。

## 实例

### 单元

| 单元 | 作用 |
|---|---|
| [`anas-vectors@.service`](../../deploy/systemd/system/anas-vectors@.service) | Qdrant 本身。`DynamicUser` 加 `User=a-nas-vec-%i`，让每个实例有自己的 UID；`StateDirectory=a-nas-vectors/%i`；`PrivateNetwork`；`InaccessiblePaths` 挡住数据卷与产品状态；`Slice=anas-vectors.slice`；`LimitNOFILE=65536`；`StopWhenUnneeded=yes`，没有 `[Install]`，只能经套接字拉起 |
| [`anas-vectors.slice`](../../deploy/systemd/system/anas-vectors.slice) | 所有实例共用的 slice，位于 `anas.slice` 之下；`MemoryHigh=1280M`、`MemoryMax=1536M` 给全部实例一个总上限 |
| [`anas-vectors-proxy@.socket`](../../deploy/systemd/system/anas-vectors-proxy@.socket) | `/run/a-nas-vectors/%i.sock`，`SocketUser=%i`、`SocketMode=0600`；所属服务的单元 `Wants=` 它 |
| [`anas-vectors-proxy@.service`](../../deploy/systemd/system/anas-vectors-proxy@.service) | `systemd-socket-proxyd --exit-idle-time=2s`，`JoinsNamespaceOf=anas-vectors@%i.service`，`Requires=`/`After=` 实例，所以连接到来时先拉起实例；最后一个连接关闭 2 秒后代理退出，实例随之停止。`StartLimitIntervalSec=0`：实例启动失败时套接字不进入 failed、不被删除，所属服务退避后重试即可 |

- 实例名就是所属服务的账号，例如 `anas-vectors@a-nas-photos.service`。单元里只用 `%i`：`%I` 会把 `-` 还原成 `/`。
- 动态用户的名字默认取单元前缀，所有实例会共用一个 UID，所以显式写 `User=a-nas-vec-%i`（不超过 31 个字符）。
- 实例在 `anas-vectors.slice` 里，它位于 `anas.slice` 之下：相册的 AI 门控只看 `system.slice` 与 `user.slice` 的压力，Qdrant 建索引不会因此关掉门控。
- `ExecStart` 运行 [`start.sh`](../../deploy/qdrant/start.sh)。它只解析一次当前 release，检查完存储后 `exec` 同一个 Qdrant，安装器切换 release 的瞬间也不会“检查的是旧版、运行的是新版”。以下两种情况它先清空存储，交给所属服务重建：
  - **版本**：版本戳记录写入存储的 Qdrant 版本（`qdrant --version`）。版本相同或同一 minor 内的补丁升级，保留存储；换了 minor（单节点不能跳过 minor 版本），或比写入者更旧（降级），清空。
  - **启动失败**：连续两次启动失败也清空，例如段损坏让 Qdrant 加载时 panic（系统测试用损坏的集合配置验证）。每次启动先计为失败，`wait-ready.sh` 等到端口可连后清掉计数。
- `ExecStartPost` 运行 [`wait-ready.sh`](../../deploy/qdrant/wait-ready.sh)，等 gRPC 端口可以连接；在此之前单元不算启动完成，代理也就不会先于 Qdrant 转发。

### 配置

[`deploy/qdrant/config.yaml`](../../deploy/qdrant/config.yaml) 是所有实例与测试共用的设置。单元另用环境变量给出存储、快照、临时目录与启动标记文件的绝对路径：默认值是相对路径，在 `ProtectSystem=strict` 下写不进去。

- 只监听 `127.0.0.1`，gRPC 6334。REST 6333 关不掉，但只在实例的命名空间里，代理不转发它。
- 关闭：遥测（命令行再加 `--disable-telemetry`，以防配置没有读到）、Web 面板（它不经鉴权）、CORS、从 URL 恢复快照。
- 资源：
  - 优化与建索引各 1 个线程，`optimizer_cpu_budget: 1`：开发机上 10 万条向量建完时，文件映射的 RSS 峰值从 2.9 GB 降到 0.8 GB，代价是建得慢（[检索功能实测](../research/qdrant-retrieval-features.md)）。
  - WAL 段 1 MB：索引靠重建而不是恢复，稳定后的 WAL 从 37–45 MB 降到 2–3 MB，看不出代价。
  - 空集合的段文件都是稀疏文件，实际占用很小。
- 内存先设 `MemoryHigh=512M`、`MemoryMax=768M`，`anas-vectors.slice` 给全部实例的总上限是 1536M。
  - 这是模板单元的限额，每个实例各一份，按相册（不建图）定。
  - 开发机实测（2026-10-27，与单元相同的限额，不建图的集合，每批 256 条写入 768 维向量）：

    | 规模 | 内存峰值 | 其中匿名 / 文件映射 | `memory.events` | 精确取前 500（中位 / p95） | 实际磁盘 |
    |---|---|---|---|---|---|
    | 2 万 | 428 MiB | 106 / 300 MiB | high 0、max 0、oom 0 | 14.6 / 33.7 ms | 254 MB |
    | 10 万 | 512 MiB（到 `MemoryHigh`） | 90 / 284 MiB（之后） | high 3298、max 0、oom 0 | 53 / 115 ms | 315 MB |

    2 万张的基线在限额之内。到 10 万张，内核在 `MemoryHigh` 处反复回收文件页，没有到 `MemoryMax` 也没有 OOM，但精确检索明显变慢。所以相册超过约 5 万张时，用 drop-in 调高这一实例的限额。
  - Experimental NAS 实测（2026-10-28，Qdrant 1.20.0）：首次同步 733 个对象（写入 730、删除 3）后，实例空闲退出，`MemoryPeak` 为 187 MiB（196,009,984 字节）。
  - 建图的大集合用得更多：[#262](https://github.com/zhongwater123/A-NAS/issues/262) 在 Qdrant 1.19.2 默认配置下测得 10 万条文本常驻约 2 GB。
  - 所以文件分块接入时要单独评估它的实例，需要时用 `anas-vectors@<账号>.service.d/` 的 drop-in 调高，不改模板。
- 磁盘按实际分配的块计：Qdrant 预分配稀疏文件，表观大小是实际的 3 倍以上，小集合可达 30 倍。

### 交付

与模型同一条通道（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）：

- [`deploy/qdrant/qdrant.json`](../../deploy/qdrant/qdrant.json) 固定官方 `qdrant-x86_64-unknown-linux-musl.tar.gz` 的版本、大小与 SHA-256。
- 开发机与 CI 用 [`scripts/fetch-qdrant.sh`](../../scripts/fetch-qdrant.sh) 下载到 `build/qdrant/<sha256>/` 并核对，测试与评估都用这份二进制。
- `deploy-dev.ps1` 只在 NAS 暂存区缺这份内容时上传。安装器先复制到自己的临时目录，再核对大小与哈希，解出二进制，放到 `/opt/a-nas/qdrant/<sha256>/qdrant`。release 的 `vectors/` 目录链接它，并带上清单、配置与两个脚本。

## 客户端：`internal/vectorindex`

使用官方 Go 客户端 `github.com/qdrant/go-client`，主版本与次版本必须与清单中的服务端一致：测试会核对，Dependabot 不升级它的 minor 与 major。

- **连接**：只连 Unix 套接字。客户端只接受 host:port，所以 `Host` 写成 IP 字面量，再用 `grpc.WithContextDialer` 改为拨号套接字。
- **让实例能空闲退出**：
  - `PoolSize: 1`；
  - `KeepAliveTime: -1`，空闲时不 ping；
  - `grpc.WithIdleTimeout`，空闲约一分钟后断开连接。

  否则连接一直开着，代理不会空闲，实例也就永远不退出。实例因此在最后一次请求后约一分钟停止。代理只等 2 秒，所以客户端没有连接时，实例几乎都已经停了：影子比对记录的 `warm=false` 基本就是冷启动；只有连接刚断开的两三秒里（代理退出、实例停止的过程中）到来的查询会被这样记录，实际只需重连。
- **`Warm()` 不等待**：连接放在 `atomic.Pointer` 里，正在建立连接时 `Warm()` 也立刻返回 false，不会排在拉起实例的那次连接后面，把冷启动记成热的。
- **不在构造时联系实例**：设 `SkipCompatibilityCheck`，所属服务启动时不会把实例拉起来。第一次使用时调用 `HealthCheck`，major.minor 与固定版本不符就拒绝工作。
- **没事不连**：outbox 为空时，同步循环不联系实例；只有对账（启动后第一次空闲时与每天）和影子比对会联系。所以相册不变时，实例会停下来。
- **只提供类型化的操作**：建集合与 keyword 索引、批量 upsert/delete（`wait=true`，确认写入 WAL 后才返回）、scroll、按范围过滤的查询。不接受调用方拼的过滤条件，避免出现“不认识的条件被当成满足”这类问题（#152 审查 S3）。
- **点 ID**：键的基于名称的 UUID（`github.com/google/uuid` 的 `NewSHA1`，固定命名空间），同一个键总是同一个点。
- **超时**：每次调用最多 2 分钟，覆盖实例冷启动（单元允许 90 秒）。卡住的实例不会一直拖住所属服务，也不会让连接一直忙、实例停不下来。
- **被拒的写入**：索引拒收的写入（例如向量长度与集合不符，gRPC `InvalidArgument`）返回 `ErrRejected`，与连接故障区分开。

## 相册的同步

### outbox（Catalog migration 14）

- `vector_outbox(object_id PRIMARY KEY, version)` 不引用 `objects`：内容对象被删除时级联也会删掉 outbox 行，索引就永远不知道要删这个点。
- 触发器建在 `embeddings`（插入、更新、删除）与 `assets`（插入、删除，以及 `trashed_at`、`library_id`、`object_id` 的更新）上。外键级联删除同样触发子表的删除触发器，所以彻底删除与 Reconcile 清理孤儿也覆盖到了；将来新增的写路径也不需要记得排队。
- 触发器里写入 outbox 必须用 `INSERT … ON CONFLICT(object_id) DO UPDATE SET version = version + 1`，不能用 `INSERT OR REPLACE`：计算向量时 `embeddings` 的 upsert 会让被触发的语句按 ABORT 处理冲突，`OR REPLACE` 会失效，任务随之失败。
- `vector_index_state` 记录当前集合名。
- `embeddings_by_age` 索引让每一批取“最新向量的模型”时不必扫描整张表。
- migration 只有 DDL，不回填：没有集合名记录时，第一次同步会把全部向量排队。

### 集合

- 名称为 `images_v1_<模型 ID 的 SHA-256 前 8 位>_<随机 8 位>`，距离为点积（向量已归一化，与 Go 侧的 `dot` 一致）。payload 是 `object`（内容 ID）与 `libraries`（含未进回收站的非视频资产的图库）。`libraries` 的 keyword 索引在写入第一个点之前建好。
- 不建 HNSW 图（`m = 0`）：10 万张以内，索引里的精确扫描与进程内扫描一样快（前 500：16–20 ms 对 10–18 ms）。默认配置在约 2 万张时就开始建图，既占处理器与内存，前 500 的召回又只有 0.96–0.99，个别查询低到 0.6（[检索功能实测](../research/qdrant-retrieval-features.md)）。
- 目标模型取 Catalog 中最新一条 `embedding/v1` 的模型；新集合的维度取这个模型最常见的向量长度，而不是最新一行的，免得一行损坏的向量定下错误的维度、让其余向量全部被拒。以下情况会新建集合、删掉其他 `images_*` 集合，再把全部向量排队：
  - 目标模型变了；
  - 记录的集合不存在：实例被清空，或版本变化；
  - 名称中的随机部分与 Catalog 记录的不符：例如换了数据卷。

### 同步循环

`RunVectorSync` 在 `RunBackground` 中运行，与 AI 后台任务共用门控：

1. 读一批 outbox 行（`object_id` 与 `version`）及其目标状态：当前模型的向量，以及所在图库，即未进回收站、非视频资产所在的图库集合。读完立即结束查询。Catalog 只有一个连接，查询开着时等待 Qdrant 冷启动，会卡住整个相册 API。
2. 有向量、图库集合非空的对象 upsert；其他的删除点。模型与集合不符的向量也删除。
   - 索引拒收一批时，逐个重试，只把拒收的对象跳过。被拒的对象也移出 outbox，日志以 warn 记录拒收数，下一次对账再试，这样一个永远写不进去的点不会卡住整个队列。
3. 用一个短事务删除 `object_id` 与 `version` 都没变的 outbox 行；同步期间又有变化的行留到下一轮。

- 集合确认存在一次后，后续的批次不再每批调用 `ListCollections`；同步出错时清掉这个记忆，下一批重新确认。
- Catalog 里已经没有向量时，删除集合，并只在仍然没有向量时清空 outbox 与状态（同一事务里的条件删除）。这样与此同时写入的第一个向量不会被一起清掉。
- 连不上实例或同步出错时不影响相册其他功能，下次重试的间隔从空闲间隔起逐次加倍，最长 30 分钟。持续失败的索引因此不会被重试一直拉着不停。

### 对账

- 服务启动后第一次空闲时跑一次，之后每天在门控允许时跑一次。它先确认集合还在（不在就新建并把全部向量排队），再 scroll 出所有点的 ID 与 `libraries`，与 Catalog 计算出的目标比较，差异写回 outbox，并在日志中记录差异数。
- 集合是这一次新建的，就到此为止：全部向量已经排队，没有可比的，也不记对账日志。所以首次安装后第一条 `reconciled` 在一天后，或相册服务重启后的第一次空闲时出现。
- 它覆盖 outbox 看不到的偏差：实例因换版本清空了存储、Catalog 被恢复到旧快照、Qdrant 掉电丢失已确认的写入。它也把“同步偏差”与“近似检索的召回”分开统计。
- 它只比较点的有无与 `libraries`，不比较向量：内容对象按内容寻址，同一对象在同一模型下的向量总是相同，换模型则换集合。

### 影子比对

- 文搜图满足以下条件时，才把查询交给后台的影子 worker：
  - 是第一页；
  - 查询中没有人物名；
  - 拿到了查询向量；
  - 查询向量的模型与集合一致。
- 请求路径只做一次非阻塞发送，channel 容量为 1，忙时丢弃这次比对；请求路径不访问 Catalog，也不等待 Qdrant。交过去的是搜索自己的结果：查询向量与范围、这次排序过的内容对象，以及它在相册服务里排序用的时间（`exact_ms`）。worker 因此不必在唯一的 Catalog 连接上重读候选。
- worker 让索引在同一范围内精确取前 500（`exact`），再用内存中的向量给搜索交来的对象算点积、按对象去重排序，两边比较；分数与第 k 名相差 10⁻⁵ 以内的并列照片都算命中。两边都是精确结果，所以 overlap 应为 1，低于 1 就说明索引与 Catalog 不一致，而不是近似误差。
- 日志记录 overlap@10/100/500、索引耗时（`index_ms`）、精确排序耗时（`exact_ms`，worker 用内存中的向量给同一批对象算点积并排序的时间，与索引的精确检索做的是同一件事，口径一致），以及调用前有没有连接（`warm`，见上文“让实例能空闲退出”）。不记录查询文本与名称。
- 比对失败（例如实例坏了）后，接下来一段时间不再比对，间隔逐次加倍、最长 30 分钟，免得每次搜索都去拉起一个坏掉的实例。
- outbox 不为空（例如刚导入、回收或首次同步还在进行）时不比对，所以不是每次搜索都有一行日志。
- 这些日志是决定是否切换读路径的依据。检索功能实测给出的切换条件是：真实图库上影子比对持续为 1，同一批查询的 p95 不高于现行扫描加套接字代理的往返。10 万张以内切换的理由只会是内存与重建边界，不会是速度。切换时还要重新评估：
  - slice：`anas.slice` 的 `CPUWeight=10` 会拉长前台查询；
  - 按需与常驻实例的冷启动差别：开发机上从启动到第一次查询完成为 0.2–1.0 秒。
- 单个成员可见的照片超过约 10 万张、精确检索又超出延迟预算时，才考虑建图；取前 500 时 `hnsw_ef` 要不低于 2000。

### 关闭与回退

- 关闭：在 `anas-photos.service` 的 drop-in 中把 `ANAS_PHOTOS_VECTOR_SOCKET` 设为空，相册就不再同步与比对，其他行为不变。
- 删除索引：停止实例后删掉它的 StateDirectory，下一次同步会重建。
- 回到 migration 13 之前的版本：删除触发器与两张表，把 `user_version` 改回 13（以 `a-nas-photos` 执行）。步骤见[手册](../runbooks/enable-vector-index.md)。

## 将要接入的集合

| 所属服务 | 集合 | 内容 | 状态 |
|---|---|---|---|
| 相册（`a-nas-photos`） | `images_*` | 每个内容对象一个整图向量 | 同步与影子比对 |
| 相册（`a-nas-photos`） | 视频分段 | 每个视频多个分段向量，按最佳片段打分 | 计划中（#207） |
| 文件索引（另一个实例） | 文件分块 | 中文全文与语义融合，按文件归并 | 计划中（#208） |

人脸与宠物向量不进入任何索引：聚类需要阈值内的全部邻居，留在 Catalog 中精确计算（[人物与宠物](photo-people.md)）。视频分段与文件分块用哪些检索功能（分组、多向量 MaxSim、中文 BM25 与融合、公式重排），以[检索功能实测](../research/qdrant-retrieval-features.md#特性取舍)为准。

后续使用方接入的前提：**向量必须留在它自己的 Catalog 里**。索引随时可能被清空（换 minor 版本、启动连续失败、换模型、人工删除），只有向量在 Catalog 里，重建才只是重新写入；否则每次都要在处理器上重新编码，可能要几个小时。

后续使用方的读路径会以索引为主，还要考虑：

- **冷启动**：实例按需启动、空闲约一分钟后停止，隔一会儿的第一次搜索要等 0.2–1 秒（带全文索引 0.4–0.9 秒）。可以在成员会话活跃时保持常驻，或在打开搜索框时预热。
- **内存账按使用方算**：每个实例用自己的 drop-in，`anas-vectors.slice` 给总上限；各单元的上限不是预留，后台建索引、AI 整理与多人对话同时发生时的总内存，在实机维护窗口里压测。
- **换模型不中断搜索**：读路径已切到索引的使用方，用集合别名：先建好新集合、写满，再一次切换别名，回滚时切回。

两份实测给后续集合的选项：

- **建图与否**：按规模决定。
  - 精确扫描在十万级以内与进程内扫描一样快，召回为 1。
  - 建 HNSW 时召回靠查询宽度保住：[#262](https://github.com/zhongwater123/A-NAS/issues/262) 测得 `hnsw_ef` 128 时带过滤的召回只有 0.88–0.95，照片取前 50 要 512（0.993），50 万个未去重的视频帧要 1024（0.984）；取前 500 时要 2000 以上（检索功能实测）。
  - 视频按视频存多向量（分数逐位等于最佳片段），还是按帧存点加 HNSW（#262：50 万帧过滤到单个视频 16 ms），取决于去重后还剩多少片段，在 [#207](https://github.com/zhongwater123/A-NAS/issues/207) 的设计中定。
- **内存不够时**：向量与 HNSW 放磁盘、int8 量化副本常驻并重排（#262：50 万帧常驻 952 → 466 MB，查询 4 → 20 ms）。binary 量化在这些向量上召回只有 0.5–0.65，不用。
- **写入**：先建 payload 索引再写入（#262：不建时写入慢 7 倍，带过滤的大 k 查询慢 3 倍），每批 64–256 条。

## 验证

- `internal/vectorindex` 与相册同步的测试使用真实的固定版本二进制（`ANAS_QDRANT_BINARY`）；`ANAS_QDRANT_REQUIRED=1` 时缺少二进制即失败，CI 设置了它。
- [系统测试](../../scripts/system-test.sh)覆盖以下各项：
  - 实例只有 `lo`，代理与实例在同一个网络命名空间；
  - 实例读不到数据卷与产品状态；
  - 每个实例有自己的动态 UID；
  - 套接字只有所属账号能连；
  - 连接时拉起实例，空闲后退出；
  - 版本戳变化时清空存储；
  - 篡改过的压缩包被拒绝；
  - 导入照片后，点出现在集合中。

## 关联

- [ADR 0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)、[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)、[ADR 0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)
- [相册本地 AI](photo-ai.md)、[相册技术设计](photo-library.md)、[人物与宠物](photo-people.md)
- [启用向量索引手册](../runbooks/enable-vector-index.md)
- [Qdrant 检索特性试验](../research/qdrant-retrieval-features.md)
- [#66](https://github.com/zhongwater123/A-NAS/issues/66)、[#152](https://github.com/zhongwater123/A-NAS/issues/152)、[#207](https://github.com/zhongwater123/A-NAS/issues/207)、[#208](https://github.com/zhongwater123/A-NAS/issues/208)、[#262](https://github.com/zhongwater123/A-NAS/issues/262)（嵌入式、图 + 向量方案与 Qdrant 的选型横比）
