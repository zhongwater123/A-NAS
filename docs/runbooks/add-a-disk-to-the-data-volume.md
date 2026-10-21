# 把硬盘加入数据卷

状态：draft（实验 NAS 待执行）
更新时间：2026-10-20

## 目的

把一块新硬盘加入已在使用的数据卷，并让数据卷在线转换为镜像（或扩容、条带），文件不搬家、服务不停。行为见[硬盘用途与数据卷冗余](../specs/disk-roles-and-redundancy.md)，取舍见 [ADR 0025](../adr/0025-span-the-data-volume-across-disks-with-btrfs-profiles.md)。

完成后：“设置 → 存储”的数据卷卡片显示“Btrfs · 镜像（RAID 1）· 2 块硬盘”、状态“在线”，没有转换进度；Btrfs 的数据、元数据和系统块都只在 RAID1 中；Scrub 没有错误；重启后两块盘一起挂载。

## 前提与风险

- 权限：A-NAS 管理员账号；查看用 `ssh a-nas`（只读）；Scrub 与重启由有 root 的人执行。
- 版本：实验 NAS 运行的 release 已包含本功能，Host Agent 与产品服务同一版本。
- 目标（2026-10-20 实验 NAS）：
  - 新盘：`ST500DM002-1BD142`，序列号 `S2AE0PVW`，WWN `0x5000c50060f85237`，当时为 `/dev/sda`；`sda1` 带有其他设备留下的 `linux_raid_member` 签名，卷标 `OrangePi3B:0`。
  - 现有数据盘：`ST500DM002-1BD142`，序列号 `Z2AYDZPB`，WWN `0x5000c500518d4994`，当时为 `/dev/sdb`，不得出现在计划中。
- 风险：
  - 清除新盘上的全部内容，包括那份旧阵列签名所属的数据。
  - 转换会重写数据卷上的全部数据，时长约为已用空间 ÷ 100 MB/s（2026-10-20 已用约 21 GB，几分钟）；期间可以照常使用，但磁盘一直繁忙。实验 NAS 没有备份功能，开始前把不可替代的文件另存一份。
  - 转换完成前，坏一块硬盘仍可能丢文件。
- 停止条件：
  - 新盘被挂载、正被 md 阵列、LVM 或加密卷使用（`lsblk` 中它下面有 `raid*`、`lvm`、`crypt` 子设备）。
  - 任一块盘的 SMART 不是“健康”，或“概览”的健康检查有 `volume_errors`、数据卷只读或离线。
  - 计划中的硬盘不是上面的新盘，或者计划里出现了两块盘。

## 步骤

1. 只读确认新盘身份与状态。

   ```bash
   ssh a-nas 'lsblk -o NAME,SIZE,TYPE,TRAN,MODEL,SERIAL,WWN,FSTYPE,LABEL,MOUNTPOINTS; ls /sys/block/sda/sda1/holders/; cat /proc/mdstat 2>/dev/null'
   ```

   - 预期：序列号 `S2AE0PVW` 的盘没有挂载点，`holders` 为空，`/proc/mdstat` 不存在或不含这块盘。
   - 完成标准：新盘与数据盘的设备名、序列号、WWN 与“目标”一致。设备名可能在重启后互换，以序列号和 WWN 为准。

2. 在“设置 → 概览”核对健康检查，在“设置 → 存储”核对磁盘列表。
   - 预期：新盘显示“未使用”和“有旧数据”，两块盘都显示“健康”；数据卷“在线”。存储页出现“发现 1 块未使用的硬盘”。
   - 完成标准：没有命中任何停止条件。

3. 生成并核对计划：点“设置用途…”，用途选“加入数据卷”，勾选新盘，“下一步”，冗余方式选“镜像”，“生成计划”。
   - 预期：操作“把硬盘加入数据卷”，冗余方式“镜像（RAID 1）”，可用容量约“466 GiB → 466 GiB”；目标硬盘只有一块 `ST500DM002-1BD142`，“将清除：linux_raid_member（OrangePi3B:0）”，并有“硬盘上有其他系统留下的数据”的提示。
   - 完成标准：目标硬盘的 ID 短码与存储页中“未使用”那块盘相同。不一致时点“取消计划”，不要确认。

4. 输入确认短语，“确认计划”，再点“清除硬盘并加入数据卷”。
   - 预期：几秒后显示“硬盘已加入数据卷”；点“完成”后，数据卷卡片显示 2 块硬盘和“正在转换为镜像”，磁盘列表中两块盘都是“数据盘”，不再出现“发现 1 块未使用的硬盘”。若新盘仍显示“未使用”，说明运行的构建早于[该问题的修复](../investigations/2026-10-20-added-disk-shown-unused-until-reboot.md)：不要再对它生成计划。
   - 完成标准：计划状态 `succeeded`。若为 `needs_attention`，不要重试，转到“回滚或恢复”。

5. 等待转换完成。卡片每 5 秒刷新一次进度与剩余时间；也可以只读查看：

   ```bash
   ssh a-nas 'u=$(findmnt -no UUID /srv/a-nas/data); cat /sys/fs/btrfs/$u/exclusive_operation; grep . /sys/fs/btrfs/$u/allocation/*/*/used_bytes'
   ```

   - 预期：`exclusive_operation` 为 `balance`，`single`、`dup` 的用量逐渐减少，`raid1` 增加。“设置 → 日志”的系统日志中有 Host Agent 的 `converting the data volume`。
   - 完成标准：卡片不再显示转换，状态“在线”；日志中有 `data-volume conversion finished`；`allocation` 下只剩 `raid1`（空的旧目录会被 Btrfs 自行删除）。中途关机或重启时，Host Agent 启动后会接着转换。

6. 由有 root 的人执行一次 Scrub。

   ```bash
   btrfs scrub start -B /srv/a-nas/data
   ```

   - 完成标准：`Error summary: no errors found`。

7. 重启一次。
   - 完成标准：`findmnt /srv/a-nas/data` 显示数据卷已挂载；存储页显示“镜像（RAID 1）· 2 块硬盘”“在线”；Web 与 SMB 能读写个人和共享空间。

## 验证

- 存储页：数据卷“在线”、“Btrfs · 镜像（RAID 1）· 2 块硬盘”，没有转换进度，两块盘的角色都是“数据盘”。
- 由有 root 的人执行 `btrfs filesystem df /srv/a-nas/data`：只有 `Data, RAID1`、`System, RAID1`、`Metadata, RAID1`。
- Scrub 没有错误，重启后自动挂载。

## 回滚或恢复

- 执行前都可以放弃：点“取消计划”，没有任何磁盘写入，可以立即重新开始。
- 执行进入 `needs_attention`：保留现场，在“日志”中导出 Host Agent 的系统日志，按第 1 步只读检查新盘的分区与签名。旧数据卷不受影响；新盘可能已被清除。不要生成新计划，排查后再决定。
- 转换停在中途（卡片显示“转换为镜像已暂停”）：文件不丢。原因记在系统日志中，Host Agent 每 15 分钟自动继续，也可以点“继续转换”。空间不足时先删除或移走文件。
- 新盘在转换完成前损坏：没转换的数据只在原数据盘上，已转换的在两块盘上各有一份，原数据盘上仍有全部文件。缺盘时的降级挂载与更换属于规格的 M2，尚未实现；此时停止使用，联系开发处理。
- 把数据卷退回单盘、移出这块硬盘属于规格的 M3，尚未实现；界面上没有回退入口。

## 关联

- 规格：[硬盘用途与数据卷冗余](../specs/disk-roles-and-redundancy.md)
- ADR：[0025](../adr/0025-span-the-data-volume-across-disks-with-btrfs-profiles.md)
- 调查：不涉及
- 代码：[`internal/hostops/linux/volume.go`](../../internal/hostops/linux/volume.go)
- 主手册：[配置并验收 v1.0.1 实验数据卷](provision-v1.0.1-experimental-storage.md)（部署与回滚 release）
