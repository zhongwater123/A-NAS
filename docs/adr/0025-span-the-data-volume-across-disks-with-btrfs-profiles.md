# 0025：数据卷以 Btrfs 自带的多盘配置跨越多块硬盘

状态：accepted（2026-10-20）；行为见[硬盘用途与数据卷冗余](../specs/disk-roles-and-redundancy.md)。本决定修订 [ADR 0007](0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md) 中的“单盘 Btrfs 数据卷”与“未选择 RAID”两处。

ADR 0007 把数据卷限定为一块硬盘。2026-10-20 实验 NAS 接入第二块硬盘后，系统没有任何办法使用它。产品要求是：插入新硬盘后，由管理员选择用途——加入数据卷并选择冗余方式，以后也可以用作备份盘。

## 决定

1. **仍然只有一个数据卷。** 新硬盘加入现有数据卷，不新建第二个卷。挂载点 `/srv/a-nas/data`、文件系统 UUID、卷标记、空间、相册、应用与快照都不变。
2. **多盘使用 Btrfs 自己的多设备配置。**
   - `btrfs device add` 把硬盘加入正在挂载的卷；
   - `btrfs balance start -dconvert=… -mconvert=…` 在线转换冗余方式；
   - `btrfs replace` 更换硬盘；
   - `btrfs device remove` 移除硬盘。
3. **提供三种冗余方式**：镜像（数据 raid1、元数据 raid1）、扩容（数据 single、元数据 raid1）、条带（数据 raid0、元数据 raid1）。两块盘及以上时元数据始终是 raid1，所以扩容和条带坏一块盘后文件系统结构仍然完整，可以按手册取回留在另一块盘上的文件。
4. **不提供 RAID 5/6。** Btrfs 上游状态页把 RAID56 标为 unstable，只用于测试。RAID 10 与三副本（raid1c3）在上游是稳定的，分别需要 4 块和 3 块盘，硬件具备时可直接加入，不需要改变本决定。
5. **转换是文件系统的状态，不是计划的状态。**
   - 新盘加入后，计划即为成功。目标冗余方式写进数据卷的卷标记，随硬盘走。
   - 转换进度由 Host Agent 从 sysfs 观测：`allocation/{data,metadata}/<profile>/used_bytes` 给出各 profile 的用量，`exclusive_operation` 说明 balance 是否在运行。
   - 停止 Host Agent 或关机会取消正在进行的 balance（进程收到信号即取消），只有卸载时暂停的 balance 由内核在下次挂载时继续。所以 Host Agent 在启动时和每 15 分钟检查一次：数据或元数据还有不在目标方式中的，就以 `soft` 过滤重新转换，只处理剩下的部分。
6. **镜像缺少一块硬盘时继续服务，由 Host Agent 决定降级挂载。**
   - mount unit 不永久带 `degraded`。否则开机时一块盘出现得慢，数据卷就会被降级挂载，并不断写出只有一份的数据块。
   - mount unit 超时失败后，如果缺少的设备不超过镜像能承受的数量，Host Agent 才以 `degraded` 挂载，并报告严重的系统健康问题。
   - 扩容与条带缺盘时不自动降级挂载，数据卷离线。
   - 降级期间，内核在无法满足 raid1 时分配 single 数据块。更换或接回硬盘后，Host Agent 以 `convert=raid1,soft` 把这些数据块转回镜像，再做一次 scrub，与上游对降级读写挂载之后的建议一致。
7. **特权边界不变。** Host Agent 的公共请求只包含稳定磁盘 ID、计划 ID 和三个冗余方式枚举值之一，不接受设备路径、balance 过滤参数或任意命令（[ADR 0007](0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)）。
8. **只有正在使用的设备才算“使用中”。** 分区、文件系统或阵列签名本身不再阻止选用这块盘。被挂载、组装、激活或启用的设备仍然拒绝。清除前，计划列出每个签名的类型与卷标。

## 后果

- ADR 0007 的“数据卷不提供冗余”只对单盘、扩容和条带成立。镜像防硬盘损坏，仍不防误删、勒索或整机损坏，不构成备份（[#27](https://github.com/zhongwater123/A-NAS/issues/27)）。
- 宿主机状态要按文件系统 UUID 认出数据卷的每一块盘，不能只看挂载点，因为 `lsblk` 只把挂载点显示在其中一块设备上。它还要报告冗余方式、缺失的设备和转换进度。“已创建数据卷”与卷的 UUID 仍取自产品服务保存的计划。
- 数据卷容量取 `statfs` 按数据配置折算后的值；转换期间混合了两种配置，可用空间是估算。
- `btrfs replace` 要求新盘不小于被替换的盘；新盘更大时，替换后要扩展文件系统才能用上多出的空间。replace 和 device remove 都要求能读出或重建全部数据，读到 I/O 错误时会中止，需要先处理损坏的数据再重试。
- 降级挂载、更换与转回镜像都必须在真实 Btrfs 上验证：root 集成测试使用回环设备，[系统测试](../../scripts/system-test.sh)覆盖重启时缺盘的挂载结果。实机上的每一步真实磁盘写入都要先写进运行手册。

## 未选择

- **md RAID1 + Btrfs**：现有数据卷直接建在分区上，改用 md 需要把数据搬到新阵列，并在此期间停用数据卷。md 也分不出两份副本中哪一份正确；Btrfs 有校验和，读到坏块会用另一份修复。在 md 上改变冗余方式同样只能重建。
- **LVM 或 mergerfs 合并多块盘**：多了一层存储栈，子卷与快照仍然要由 Btrfs 提供；mergerfs 合并出的目录不能整体做 Btrfs 快照。
- **第二个独立数据卷**：空间、相册、应用、快照、ACL 修复和文件代理都以唯一数据卷为前提，每个空间还要选择所在的卷。改动远大于收益，也不能提供冗余。
- **mount unit 永久带 `degraded`**：见决定 6。
- **镜像缺盘时停止服务、等待管理员处理**：一块盘坏了文件就全部不可用，这正是镜像要避免的情况。家用 NAS 的通常做法是降级继续运行并告警。

## 关联

- [硬盘用途与数据卷冗余规格](../specs/disk-roles-and-redundancy.md)
- [存储与文件架构](../architecture/storage-and-files.md)
- [Btrfs 功能状态](https://btrfs.readthedocs.io/en/latest/Status.html)、[卷管理](https://btrfs.readthedocs.io/en/latest/Volume-management.html)、[挂载选项 `degraded` 与 `skip_balance`](https://btrfs.readthedocs.io/en/latest/Administration.html)、[`btrfs replace`](https://btrfs.readthedocs.io/en/latest/btrfs-replace.html)
- [`internal/hostops/linux/executor.go`](../../internal/hostops/linux/executor.go)
- [`internal/hoststate/linux/reader.go`](../../internal/hoststate/linux/reader.go)
