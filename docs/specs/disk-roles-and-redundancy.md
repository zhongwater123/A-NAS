# 硬盘用途与数据卷冗余

状态：M1 implemented；M2、M3 draft
更新时间：2026-10-23

## 目标

插入新硬盘后，管理员在“设置 → 存储”中决定它的用途：加入数据卷并选择冗余方式（镜像、扩容或条带），或者在备份功能落地后用作备份盘。

此前数据卷只能建在一块硬盘上。已有数据卷时，服务端拒绝生成新计划（`409 data_volume_exists`），“存储”也就没有任何入口。2026-10-20 实验 NAS 接入第二块 ST500DM002 时，这块盘只能闲置。而且即使有入口，这块盘也选不了：它带着一份没有被组装的旧 md 阵列签名（`linux_raid_member`，卷标 `OrangePi3B:0`），现有规则把它判为“使用中”。

数据卷仍然只有一个。挂载点、文件系统 UUID、空间、相册、应用与快照都不变，变化的是它可以由多块硬盘组成。技术取舍见 [ADR 0025](../adr/0025-span-the-data-volume-across-disks-with-btrfs-profiles.md)。

## 冗余方式

| 冗余方式 | 界面名称 | Btrfs 数据 / 元数据 | 硬盘数 | 可用容量 | 坏一块硬盘时 |
|---|---|---|---|---|---|
| 单盘 | 单盘 | single / dup | 1 | 这块盘 | 丢失全部文件 |
| 镜像 | 镜像（RAID 1） | raid1 / raid1 | 2 块及以上 | 总容量的一半，且不超过“总容量减去最大的一块” | 文件不丢，数据卷继续使用，提示更换 |
| 扩容 | 扩容（不冗余） | single / raid1 | 2 块及以上 | 各盘相加 | 数据卷离线；坏盘上的文件丢失，其余文件可按手册取回 |
| 条带 | 条带（RAID 0） | raid0 / raid1 | 2 块及以上 | 各盘相加，容量不同时以 Btrfs 的估算为准 | 数据卷离线，几乎所有文件丢失 |

- 两块盘及以上时，元数据始终保留两份，所以扩容和条带坏一块盘后，文件系统结构仍然完整。
- 镜像只防硬盘损坏，不防误删、勒索或整机损坏。误删的文件靠回收站与快照找回，整机故障靠备份（[#27](https://github.com/zhongwater123/A-NAS/issues/27)）。界面在选择冗余方式处写明这一点。
- 不提供 RAID 5/6、RAID 10 和三副本，原因见[非目标](#非目标)。

## 非目标

- 第二个独立数据卷（另一个挂载点，按空间选择所在的卷）。空间、相册、应用、快照、ACL 修复和文件代理都以唯一数据卷为前提。
- RAID 5/6：Btrfs 上游把它标为 unstable，只用于测试。RAID 10 与三副本分别需要 4 块和 3 块盘，等硬件具备后再加入。
- 备份盘：用途选择中列出，标“规划中”、不可选；行为由 [#27](https://github.com/zhongwater123/A-NAS/issues/27) 定义。
- USB 硬盘（[#25](https://github.com/zhongwater123/A-NAS/issues/25)）仍不能加入数据卷。
- 加密、SSD 缓存、热备盘、定期 Scrub 计划。

## 场景

实现分三步，每步都可以单独验收。M1 让第二块盘可用；M2 让镜像真正承受掉盘；M3 补齐改变主意的路径。

### M1：发现未使用的硬盘

- Given：数据卷在线，新接入一块 SATA 或 NVMe 硬盘。
- When：管理员打开“存储”。
- Then：
  - 磁盘列表中这块盘显示“未使用”。
  - 数据卷卡片下方提示“发现 1 块未使用的硬盘”，带“设置用途…”按钮。
  - 不合格的盘显示原因，例如“USB 硬盘不能加入数据卷”“正被其他阵列使用”。
  - 成员只看到“未使用”，没有入口。
  - 磁盘列表来自宿主机快照。控制器支持热插拔时，新盘不用重启就会出现；否则重启后出现。

### M1：旧系统留下的签名

- Given：新盘上有其他系统留下的分区、文件系统或阵列签名，但没有被挂载、组装、激活或启用。例如实验 NAS 的 `sda1` 带有未组装的 `linux_raid_member`，卷标 `OrangePi3B:0`。
- When：管理员选择这块盘。
- Then：
  - 它是合格的候选盘，标“有旧数据”。
  - 核对计划时逐条列出签名类型与卷标，并写明“这块盘可能来自其他 NAS 或电脑，清除后无法恢复”。
  - 正在使用的成员仍然不可选：已组装的 md 阵列、已激活的 LVM 卷组、已打开的 LUKS 卷、启用中的 swap，以及任何挂载。

  本条取代[数据卷初始化](basic-storage-and-sharing.md#数据卷初始化)第 1 条中的“不是 swap/md/dm 成员”。

### M1：加入数据卷

- Given：数据卷是单盘，在线、可写，没有正在进行的转换；有一块合格的未使用硬盘。
- When：管理员点击“设置用途…”。
- Then：“存储”中展开向导。样式沿用初始化向导，计划同样保存在桌面会话中，切换分区或关闭窗口后仍然显示。
  1. **用途**：“加入数据卷”（默认），以及不可选的“用作备份盘（规划中）”。
  2. **冗余方式**：镜像、扩容、条带三张卡片。每张卡片写明：
     - 加入前后的可用容量，例如“465.8 GB → 465.8 GB”“465.8 GB → 931.5 GB”；
     - 坏一块盘的后果。

     两块盘时，“镜像”标“推荐”并默认选中。如果已用空间超过某种方式加入后的可用容量，这种方式不可选，并写明原因，例如“已用 620 GB，超过镜像后的 465.8 GB”。
  3. **核对计划**：显示目标盘、身份指纹、将清除的签名、所选冗余方式与加入前后的容量、逐条动作、有效期，并要求输入确认短语，与初始化相同。
  4. **执行**：清除签名、建分区并加入数据卷，只需几秒。完成后显示“硬盘已加入数据卷”；镜像和条带随即在后台开始转换。
- 转换期间：
  - 数据卷照常读写。
  - 数据卷卡片显示“正在转换为镜像 37%”、已转换和全部的数据量，以及按最近速度估算的剩余时间。
  - 卡片写明“完成前坏一块盘仍可能丢数据”。
  - 重启或断电后，转换自动继续。

### M1：没有数据卷时用多块盘初始化

- Given：尚未创建数据卷，有两块及以上合格硬盘。
- When：管理员打开“初始化数据卷…”。
- Then：
  - 第一步可以勾选多块盘。
  - 勾选两块及以上时，出现与上面相同的冗余方式选择，默认选中镜像。
  - 只勾一块时与现在相同，创建单盘数据卷。
  - 确认短语包含每块所选盘的 ID 后缀，执行时一次清除并格式化全部所选盘。

### M1：数据卷卡片

- Given：数据卷存在。
- When：打开“存储”或“概览”。
- Then：
  - 数据卷卡片显示冗余方式、组成它的硬盘（型号与 ID 短码）、已用与总容量，以及状态：在线、转换中、缺少硬盘、离线、只读。
  - 磁盘列表中，组成数据卷的每块盘的角色都是“数据盘”。

### M2：镜像少了一块硬盘

- Given：数据卷是镜像，一块盘损坏或被拔出。
- When：运行中掉盘，或开机时缺少这块盘。
- Then：
  - 数据卷继续可读写。开机时缺盘，由 Host Agent 以降级方式挂载。
  - 系统健康为严重：“数据卷缺少一块硬盘，已经没有冗余”，处理办法是“尽快更换硬盘”（新增 `volume_degraded`，见[系统健康](system-health.md)）。
  - 数据卷卡片标“缺少硬盘”，列出缺失硬盘的型号与 ID 短码。
  - 降级期间新写入的数据只有一份。

### M2：更换硬盘

- Given：镜像缺少一块硬盘，并插入了一块合格的新盘，容量不小于缺失的那块。
- When：管理员在数据卷卡片点“更换硬盘…”，选择新盘、核对计划并输入确认短语。
- Then：
  - 新盘替换缺失的硬盘并重建镜像，卡片显示“正在重建 52%”。
  - 重建完成后，降级期间只有一份的数据也转回镜像。
  - 卡片恢复“在线”，`volume_degraded` 消失。
  - 新盘比缺失的盘大时，多出的空间在重建后自动用上。

### M2：缺失的硬盘回来了

- Given：镜像降级期间写入过数据，之后原来那块盘被重新接上，例如数据线松了。
- When：重新接上后重启。
- Then：
  - 这块盘重新回到数据卷。
  - Host Agent 在后台把降级期间只有一份的数据转回镜像，再做一次校验，修复这块盘上过期的内容。
  - 卡片显示“正在恢复冗余”，完成后恢复“在线”。

### M2：扩容或条带少了一块硬盘

- Given：数据卷是扩容或条带，一块盘缺失。
- Then：
  - 数据卷离线，文件服务拒绝访问。
  - 系统健康为 `volume_offline`，并写明缺少哪块硬盘。
  - 不自动降级挂载；接回硬盘后恢复。
  - 硬盘确实损坏时，按运行手册以只读降级方式挂载并取回剩余文件；界面不提供这项操作。

### M3：更改冗余方式与移除硬盘

- Given：数据卷由两块及以上硬盘组成，状态在线，没有正在进行的转换。
- When：管理员在数据卷卡片选择“更改冗余方式…”或在某块盘上选择“移除…”。
- Then：
  - 可以在镜像、扩容、条带之间转换。已用空间放不下目标方式时，该选项不可选。
  - 移除硬盘的前提是剩下的盘放得下全部数据：先转为剩余盘数允许的方式，再移出数据并移除。
  - 两种操作都走计划、确认短语、后台转换的流程，进度显示在数据卷卡片上。
  - 从镜像改为扩容或条带时，确认页用红字写明冗余将失去。

## 边界与失败

- 只有管理员可以操作；成员直接调用接口返回 `403`。
- 向导除执行中以外的每一步都可以离开：
  - 生成计划前直接关闭；
  - 计划确认前后、执行前点“取消计划”或关闭按钮（`POST /api/v1/storage/plans/{planId}/cancel`，状态 `cancelled`），之后可以立即生成新计划；
  - 计划结束后点“完成”或“关闭”。

  取消的计划没有碰过任何硬盘。执行中可以关闭设置窗口或页面，见下文“执行在设备上进行”。
- 哪块盘属于数据卷、哪块盘正在使用，以内核为准。宿主机状态从 sysfs 的 `fs/btrfs/<uuid>/devices` 认出数据卷的每块成员盘，任何已挂载 Btrfs 的设备都算使用中。`lsblk` 的文件系统类型与 UUID 来自 udev 数据库，`btrfs device add` 之后它在重启前一直把新分区显示为空白；只按它判断，刚加入的盘会重新显示为“未使用”，并且可以被再次清除（[调查](../investigations/2026-10-20-added-disk-shown-unused-until-reboot.md)）。加盘后 Host Agent 还会让 udev 重新探测新分区。
- 同一时间只有一个有效计划：生成新计划会取消尚未执行的旧计划（页面刷新后界面不再记得它），只有执行中的计划会挡住新计划（`409 plan_state_conflict`）。数据卷正在转换、重建或缺少硬盘时，只能执行更换硬盘，其他计划返回 `409 volume_busy`。
- 执行前，Host Agent 重新枚举并核对：
  - 新盘的身份，变化时返回 `disk_changed`；
  - 数据卷的文件系统 UUID、成员与冗余方式，变化时返回 `volume_changed`。
- 新盘加入后即属于数据卷，即使后台转换随后失败（例如空间不足或 I/O 错误）。目标冗余方式记在卷标记 `.a-nas-volume.json` 的 `layout` 中，Host Agent 在启动时和每 15 分钟检查一次，只转换还不在目标方式中的数据（`soft` 过滤），所以停下的转换会接着做。
- 转换停下时，卡片显示“转换为镜像已暂停”、已转换的比例，说明系统会自动继续、停下的原因记在“日志”的系统日志中，并提供“继续转换”（`POST /api/v1/volumes/{volumeId}/conversion`）立即再试。文件不丢，冗余以实际为准。
- 执行在设备上进行（[#165](https://github.com/zhongwater123/A-NAS/issues/165)）：
  - 关闭窗口、刷新页面、断网或重启产品服务都不会打断它。执行接口返回 `202` 与进行中的计划；向导显示进行到哪一步（清除硬盘、格式化、挂载、准备空间、加入数据卷），并写明可以关闭页面。页面回来时，存储页找回进行中的计划（`GET /api/v1/storage/plans`）并接着显示。
  - 执行中 Host Agent 重启或断电时，计划进入 `needs_attention`，不自动重做。存储页显示“上次的存储操作没有完成”与停下的步骤，“查看并处理”打开向导，“重试”再次执行同一计划：设备再核对一次硬盘身份，已经完成的步骤不会重做。新卷挂载前从头清除并格式化，挂载后只补齐剩下的准备；已加入数据卷的硬盘不再清除。之后运行过的新计划取代它，提示随之消失。
  - 加盘时目标冗余方式在加入硬盘之前就记进卷标记；硬盘已经加入、进程却被杀时，Host Agent 的检查照样按它转换。
  - 转换阶段被中断时（停止 Host Agent 或关机会取消正在进行的 balance），由 Host Agent 的检查接着转换。
- 没有登记数据卷、设备上却挂载着一个（例如中断的初始化留下的）时，不能新建（`409 volume_exists`，“设备上已经有一个没有登记的数据卷……”），应重试留下它的那次计划来接管它。Host Agent 也拒绝覆盖指向其他数据卷的挂载配置：数据卷离线时用别的盘新建，会让原来的卷在下次开机时被遗忘、它的硬盘显示为未使用并可被清除。
- 容量取文件系统按冗余方式折算后的值。转换期间混合了两种方式，可用空间是估算；写入保留规则不变（容量的 5%，至少 10 GiB）。
- 容量不同的硬盘可以组合，可用容量按上表计算并在选择时显示。
- 计划、确认、执行写入活动日志；转换的开始、完成或停下写入 Host Agent 的系统日志。
- 冗余方式与容量估算在服务端决定；界面按同一公式显示，供选择时参考（[`layouts.ts`](../../web/src/settings/layouts.ts)）。
- 单盘数据卷从不记录目标冗余方式，也不显示转换：它的元数据是 `dup` 还是 `single` 取决于格式化它的 btrfs-progs 版本。

## 验收证据

M1：

- 存储服务（[`layout_test.go`](../../internal/storage/layout_test.go)）：两块盘默认镜像、冗余方式与硬盘数不符、加入硬盘的计划与容量、转换进度与 `volume_busy`、继续转换、已有文件放不进镜像、离线数据卷的成员不可清除、单盘时期存下的计划仍可读取。
- 宿主机状态（[`datavolume_test.go`](../../internal/hoststate/linux/datavolume_test.go)、[`reader_test.go`](../../internal/hoststate/linux/reader_test.go) 的 `TestDeviceInUseOnlyForActiveStorageStacks`）：按文件系统 UUID 找到数据卷的每块盘、从 sysfs 读出各 profile 的用量与正在进行的 balance、只有正在使用的阵列、卷组、加密卷或 swap 才算使用中。
- Host Agent（[`volume_test.go`](../../internal/hostops/linux/volume_test.go)、[`volume_internal_test.go`](../../internal/hostops/linux/volume_internal_test.go)、[`volume_test.go`](../../internal/hoststate/agent/volume_test.go)）：多盘格式化的命令与卷标记、新分区再清一次签名、身份变化或使用中的盘在任何命令前被拒绝、数据卷未挂载时不加盘、只转换不在目标方式中的数据、暂停的 balance 被恢复、IPC 文档的往返与校验。
- 接口契约（[`storage_test.go`](../../internal/httpapi/storage_test.go)）：两块盘的计划、确认、执行、数据卷列表与继续转换都符合 [OpenAPI](../../api/openapi.yaml)。
- React（[`App.test.tsx`](../../web/src/App.test.tsx)、[`layouts.test.ts`](../../web/src/settings/layouts.test.ts)）：发现未使用的硬盘、旧数据与不合格原因、用途与冗余方式、带旧签名的计划、加入后的完成提示、转换进度与继续转换、两块盘新建时选择扩容。
- root 集成测试（[`root_volume_integration_test.go`](../../internal/hostops/linux/root_volume_integration_test.go)）：回环设备上的真实 Btrfs 单盘加入第二块后，按卷标记转为镜像。
- 实机：按[运行手册](../runbooks/add-a-disk-to-the-data-volume.md)在实验 NAS 上把 `sda` 加入数据卷并转为镜像，Scrub 没有错误。

执行在设备上进行（[#165](https://github.com/zhongwater123/A-NAS/issues/165)，[调查](../investigations/2026-10-21-storage-plan-bound-to-browser-request.md)）：

- 存储服务（[`operation_test.go`](../../internal/storage/operation_test.go)）：请求结束后计划接着运行并被跟随到结束、产品服务重启后接着跟随、Host Agent 没收到的计划进入 `needs_attention`、停下的计划交给 Host Agent 重试并接管它建出的卷、之后运行过的计划取代停下的计划、没有登记的数据卷挡住新建。
- Host Agent（[`volume_test.go`](../../internal/hostops/linux/volume_test.go)、[`volume_internal_test.go`](../../internal/hostops/linux/volume_internal_test.go)）：请求被取消后操作照常完成；在清除、格式化、挂载、准备每一步停下后，重启的 Host Agent 报告 `interrupted` 与步骤，重试只在卷挂载前重新清除与格式化；命令被杀后重试；拒绝不是本计划建出的卷与指向它的挂载配置；接管本计划建出的卷；加盘先记下冗余方式，硬盘加入后被杀，重试跳过已加入的硬盘；同一时间只运行一个操作，日志在重启后仍在；待定的冗余方式在设备齐全后生效。
- root 集成测试（[`root_volume_integration_test.go`](../../internal/hostops/linux/root_volume_integration_test.go)）：真实 Btrfs 上先记下镜像、再加入硬盘，检查把它转为镜像。
- 接口契约（[`storage_test.go`](../../internal/httpapi/storage_test.go)）与 Host Agent IPC（[`agent_test.go`](../../internal/hoststate/agent/agent_test.go)）：执行返回 `202` 与步骤、`GET /api/v1/storage/plans`、`GET /v1/storage/operations/{planId}` 与错误码。
- React（[`App.test.tsx`](../../web/src/App.test.tsx)）：页面回来后接着显示进行中的计划直到完成；停下的计划显示步骤并重试；重试再次停下时显示新的原因。

M2、M3 另需：缺盘时的降级挂载与系统测试（[`scripts/system-test.sh`](../../scripts/system-test.sh)）、更换与移除的 root 集成测试，以及拔盘开机、接回恢复的实机验证。
- 实机：按运行手册在实验 NAS 上：
  - 把 `sda` 加入数据卷并转为镜像，Scrub 没有错误；
  - 拔掉一块盘开机，确认降级挂载与健康告警；
  - 接回后确认冗余恢复。

## 关联

- ADR：[0025](../adr/0025-span-the-data-volume-across-disks-with-btrfs-profiles.md)（修订 [0007](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md) 的单盘限制）
- 规格：[基础存储与共享](basic-storage-and-sharing.md#数据卷初始化)、[设置](settings.md)中的“存储”分区、[系统健康](system-health.md)
- 架构：[存储与文件架构](../architecture/storage-and-files.md)
- 代码：[`internal/storage/service.go`](../../internal/storage/service.go) 与 [`layout.go`](../../internal/storage/layout.go)（计划与冗余方式）、[`internal/hostops/linux/volume.go`](../../internal/hostops/linux/volume.go)（格式化、加盘、转换检查）、[`internal/hoststate/linux/datavolume.go`](../../internal/hoststate/linux/datavolume.go)（数据卷观测）、[`cmd/anas-api/devstorage.go`](../../cmd/anas-api/devstorage.go)（fake 模式的模拟）、[`web/src/settings/StorageSection.tsx`](../../web/src/settings/StorageSection.tsx)
- 运行手册：[把硬盘加入数据卷](../runbooks/add-a-disk-to-the-data-volume.md)
- 项目背景：[PROJECT_CONTEXT.md](../../PROJECT_CONTEXT.md) 的“增加第二块数据盘，用于验证镜像、掉盘、替换和重建”
