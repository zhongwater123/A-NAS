# 加入数据卷的硬盘在重启前仍显示为未使用

状态：fixed locally（PR #149）；实验 NAS 待重新部署后复核
更新时间：2026-10-20

## 症状与影响

2026-10-20，用户在实验 NAS 上部署了 PR #149 的构建，把一块用过的硬盘加入数据卷并选择镜像。计划成功执行，但重新打开“设置 → 存储”后：

- 这块盘仍显示“未使用”；
- 页面再次出现“发现 1 块未使用的硬盘 / 设置用途…”；
- 数据卷卡片只列出原来那块盘。

重启 NAS 后一切显示正确。加盘本身是成功的，只是状态没有更新。

影响：这块盘已经属于正在使用的数据卷，界面却把它当成可以清除的空闲盘。管理员可能再为它生成清除计划，造成破坏性操作。

## 最小复现

1. 数据卷为单盘并已挂载，接入一块合格硬盘。
2. 在“存储”中把这块盘加入数据卷（任一冗余方式），执行成功。
3. 不重启，刷新“存储”：实际显示“未使用”；期望显示“数据盘”，数据卷有两块硬盘。

## 观察事实

- 重启后（只读，`ssh a-nas`）：`/sys/fs/btrfs/09e275fe-…/devices/` 列出 `sda1`、`sdb1`；`allocation/{data,metadata,system}` 下只有 `raid1`；`udevadm info /dev/sda1` 有 `ID_FS_TYPE=btrfs` 与卷的 UUID。转换已经完成。
- 宿主机状态只按 `lsblk` 的 `FSTYPE`、`UUID` 判断哪块盘属于数据卷（挂载表只列出数据卷的一块设备）。`lsblk` 的这两列来自 udev 数据库。
- 加盘的顺序：
  1. `wipefs --all` 清除新分区；
  2. `btrfs device add` 先在用户态打开、清零并关闭这块分区，再发 `BTRFS_IOC_ADD_DEV`；
  3. 内核随后写入超级块。

  udev 只在用户态关闭写入的设备时重新探测，此时分区还是空白。内核自己写超级块不会再触发事件。
- 重启后 udev 重新探测所有设备，所以显示恢复正常。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| udev 数据库在 `btrfs device add` 后停留在“空白分区” | 部署后加盘、不重启，比较 `udevadm info /dev/<新分区>` 与 `/sys/fs/btrfs/<uuid>/devices/` | 待实机复核；与“重启即恢复”、sysfs 已列出新分区、加盘时序都一致 |
| 产品服务或 Host Agent 缓存了旧的磁盘列表 | 查读取路径 | 排除：每次读取都重新运行 `lsblk`，只缓存 SMART |

## 根因

宿主机状态把 udev 数据库当作 Btrfs 成员关系的唯一来源。`btrfs device add` 之后，这个数据库在重启前都是旧的；内核在 sysfs 中的成员列表则立即正确。

## 修复与回归证据

- 修复：
  - [`datavolume.go`](../../internal/hoststate/linux/datavolume.go) 从 `/sys/fs/btrfs/<uuid>/devices/` 读出每个已挂载 Btrfs 的设备。数据卷的成员盘按内核判断，任何已挂载 Btrfs 的设备都算“使用中”。Host Agent 执行清除前用同一份判断。
  - [`volume.go`](../../internal/hostops/linux/volume.go) 在加盘后运行 `udevadm trigger --action=change` 和 `udevadm settle`，让 `lsblk`、`blkid` 在重启前也一致。它失败只记日志，状态判断不依赖它。
- 测试：[`TestADiskJustAddedToTheVolumeIsADataDiskBeforeUdevKnows`](../../internal/hoststate/linux/datavolume_test.go) 模拟 `lsblk` 显示新分区空白、sysfs 已列出它的情况：新盘应为数据盘且使用中，数据卷有两块盘；另一个已挂载 Btrfs 的设备应为使用中。修复前该盘读成未使用。

## 后续工作

- 重新部署后在实验 NAS 上复核：加盘后不重启，存储页即显示两块数据盘；记录加盘前后 `udevadm info` 的差异，确认上表第一个假设。

## 关联

- 规格：[硬盘用途与数据卷冗余](../specs/disk-roles-and-redundancy.md)
- ADR：[0025](../adr/0025-span-the-data-volume-across-disks-with-btrfs-profiles.md)
- 运行手册：[把硬盘加入数据卷](../runbooks/add-a-disk-to-the-data-volume.md)
