# 镜像缺盘与更换硬盘

状态：draft（实验 NAS 待执行）
更新时间：2026-11-06

## 目的

在实验 NAS 上验收硬盘用途 M2（[规格](../specs/disk-roles-and-redundancy.md#m2镜像少了一块硬盘)）：

- 镜像缺一块盘时继续服务；
- 接回后恢复冗余；
- 有空闲硬盘时，把缺失的盘换成新盘。

真实镜像坏盘时，也按本手册的第 4–6 步处理。

完成后：
- 拔盘开机后数据卷可读写，健康为严重（`volume_degraded`）；
- 接回或更换后卡片回到“在线”，Btrfs 的数据、元数据和系统块只在 RAID1 中，Scrub 没有错误。

## 前提与风险

- 权限：A-NAS 管理员账号；查看用 `ssh a-nas`（只读）；关机、插拔硬盘与 root 命令由在现场、有 root 的人执行。
- 版本：实验 NAS 运行的 release 包含 M2，Host Agent 与产品服务同一版本。
- 目标（2026-11-06 实验 NAS）：
  - 数据卷 `09e275fe-794a-458d-8200-b6e67c55cc22`，镜像，两块 `ST500DM002-1BD142`：序列号 `S2AE0PVW`（2026-10-20 加入）与 `Z2AYDZPB`；
  - 更换用的空闲盘：容量不小于 500 GB 的 SATA 盘，盘上内容会被清除。
- 风险：
  - 缺盘期间新写入的数据只有一份，这时坏掉剩下那块盘会丢失它们。实验 NAS 没有备份功能，开始前把不可替代的文件另存一份。
  - 开机缺盘时，systemd 先等 90 秒，开机因此变慢。
  - 更换会清除新盘；重建期间磁盘一直繁忙，时长约为已用空间 ÷ 100 MB/s。
- 停止条件：
  - 第 1 步的 Scrub 有无法修复的错误，或任一块盘的 SMART 不是“健康”；
  - 拔盘开机后数据卷离线，且 Host Agent 日志写明“stays offline”：先读原因，不要再拔另一块盘；
  - 更换计划中的硬盘不是准备好的空闲盘。

## 步骤

1. 部署前确认镜像完好。由 root 执行：

   ```bash
   btrfs scrub start -B /srv/a-nas/data
   btrfs filesystem df /srv/a-nas/data
   ```

   - 预期：Scrub `no errors found` 或全部 corrected；`Data`、`Metadata`、`System` 只有 `RAID1`。
   - 完成标准：没有 uncorrectable 错误。这也补上了[加盘手册](add-a-disk-to-the-data-volume.md)欠着的 Scrub。
2. 部署包含 M2 的版本后，正常重启一次：mount unit 的新选项要在下一次挂载时才生效。重启后只读检查：

   ```bash
   ssh a-nas 'systemctl show -p StopPropagatedFrom "srv-a\x2dnas-data.mount"; grep Options /etc/systemd/system/srv-a\\x2dnas-data.mount'
   ```

   - 预期：`StopPropagatedFrom=` 为空；`Options=` 以 `x-systemd.device-bound=no` 结尾。
   - 完成标准：两项都符合。不符合时停止，否则运行中掉盘会卸载数据卷。
3. 关机，拔掉其中一块盘（记下序列号），开机。
   - 预期：
     - 开机多等约 90 秒；
     - Host Agent 日志（“设置 → 日志”的 Host Agent）有 `mounted the data volume without a missing disk`；
     - “概览”的健康为严重，“数据卷缺少 1 块硬盘”；
     - “设置 → 存储”的卡片标“缺少硬盘”，列出拔掉那块盘的型号与 ID 短码。
   - 完成标准：在文件管理中能新建并读回一个测试文件。
4. 关机，接回原来那块盘，开机。
   - 预期：
     - 正常挂载，没有 90 秒等待；
     - 卡片显示“正在恢复冗余”；
     - Host Agent 日志依次有 `converting the data volume`、`scrubbing it to repair what the returned disk missed`，最后是 `the data volume has its redundancy back`。
   - 完成标准：卡片回到“在线”，健康不再有 `volume_degraded`，第 3 步的测试文件仍在。
5. 更换（有空闲盘时）。
   1. 关机，拔掉一块数据盘，接上空闲盘，开机，确认第 3 步的预期。
   2. 在卡片上点“更换硬盘…”，选择空闲盘，核对计划：目标盘的序列号是空闲盘的，动作为清除、重建、恢复冗余。
   3. 输入确认短语后执行。
   - 预期：卡片显示“正在重建 N%”，完成后显示“正在恢复冗余”，最后回到“在线”。
   - 完成标准：
     - `btrfs filesystem show /srv/a-nas/data` 列出两块盘，没有 `MISSING`；
     - 只读的 `lsblk -f` 中新盘的分区显示 `btrfs` 与数据卷 UUID。
6. 失败时：
   - 计划显示“需要人工处理”：先读 Host Agent 日志中的原因再重试；
   - 重建停在 `replace` 一步且日志有 `-28`：剩下那块盘没有未分配空间。先清理文件、回收站或旧快照，再重试。

## 验证

```bash
ssh a-nas 'btrfs filesystem show /srv/a-nas/data 2>/dev/null || true; for d in /sys/fs/btrfs/09e275fe-794a-458d-8200-b6e67c55cc22/devinfo/*; do echo "$d missing=$(cat $d/missing) writeable=$(cat $d/writeable)"; done; ls /sys/fs/btrfs/09e275fe-794a-458d-8200-b6e67c55cc22/allocation/data/'
```

- 每个 `devinfo` 都是 `missing=0 writeable=1`；`allocation/data/` 下只有 `raid1` 一种块组。

## 回滚或恢复

- 拔掉的盘还在：关机接回后开机，按第 4 步恢复。
- 拔盘开机后数据卷离线，日志写明“the disks here are older”：接回的是缺盘期间不在的旧盘。关机，换回降级期间留下的那块盘后开机。
- 两块盘都在但数据卷仍离线：不要清除任何一块盘。由 root 只读挂载取回文件：

  ```bash
  mount -o ro,degraded UUID=09e275fe-794a-458d-8200-b6e67c55cc22 /mnt
  ```

  然后按[调查](../investigations/2026-11-06-mirror-missing-a-disk.md)判断原因。
- 回退到不含 M2 的版本：mount unit 保留新选项，旧版本照常使用它；降级记录 `/var/lib/a-nas-host/degraded-volume.json` 不被旧版本读取，回到新版本后继续生效。

## 关联

- 规格：[硬盘用途与数据卷冗余](../specs/disk-roles-and-redundancy.md)、[系统健康](../specs/system-health.md)
- ADR：[0025](../adr/0025-span-the-data-volume-across-disks-with-btrfs-profiles.md)
- 调查：[镜像少了一块硬盘时 systemd、udev 与 Btrfs 的行为](../investigations/2026-11-06-mirror-missing-a-disk.md)
- 代码：[`degraded.go`](../../internal/hostops/linux/degraded.go)、[`volume.go`](../../internal/hostops/linux/volume.go)
