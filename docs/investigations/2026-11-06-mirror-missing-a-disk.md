# 镜像少了一块硬盘时 systemd、udev 与 Btrfs 的行为

状态：fixed（M2，见下文“修复”），待实机验收
更新时间：2026-11-06

## 症状与影响

硬盘用途 M2（[规格](../specs/disk-roles-and-redundancy.md#m2镜像少了一块硬盘)）要求镜像缺一块盘时继续服务。动手前要先知道，Debian 13 上的 systemd、udev 与 Btrfs 在缺盘时到底怎么做。这些行为容器版系统测试测不到，因为容器里没有 udev，也不能真的插拔硬盘。实测发现，当时的 mount unit 有一处与镜像的目标直接冲突：

- 运行中拔掉数据卷挂载表里登记的那块盘，systemd 会在 0.3 秒内**卸载整个数据卷**。镜像本该用剩下的盘继续工作，结果数据卷离线了。
- 卸载后再写 `/srv/a-nas/data`，内容落在系统盘的空挂载点目录里。

实验 NAS 的 mount unit 也是这样：`systemctl show` 显示 `StopPropagatedFrom=dev-sda1.device`。

## 最小复现

1. 环境：QEMU/KVM 虚拟机，Debian 13 generic 云镜像（内核 6.12.115、systemd 257.13、btrfs-progs 6.14，与实验 NAS 相同）；数据盘是 virtio-scsi 上的 `scsi-hd`，可以用 QEMU 监视器热插拔。脚本不在仓库里：spike 只用一次，是否做成常驻回归测试由用户决定。
2. 按 Host Agent 的做法建两块盘的 raid1：每块盘一个 GPT 分区，`mkfs.btrfs --data raid1 --metadata raid1`，mount unit 写 `What=UUID=…`、`Before=local-fs.target`、`WantedBy=local-fs.target`。
3. 运行中用 `device_del` 拔掉 `findmnt` 显示的那块源设备。
4. 实际：`Unmounting srv-a\x2dnas-data.mount`，数据卷被卸载。期望：数据卷继续读写，只少一份副本。

## 观察事实

开机：

- `What=UUID=…` 会被 systemd 换成 `/dev/disk/by-uuid/…` 设备单元：mount unit 依赖它（`Requires=`、`After=`），设备单元等待 90 秒（`JobRunningTimeoutUSec=1min 30s`）。
- udev 规则 `64-btrfs.rules` 在内核认齐全部成员之前，把成员标为 `SYSTEMD_READY=0`，设备单元不就绪。
- 第二块盘在开机约 65 秒时才出现：设备单元随即就绪，正常挂载。所以盘起转慢不会导致挂载失败。
- 第二块盘一直不在：约 110 秒时设备单元 `timeout`，mount unit `failed`，结果为 `dependency`。由于 mount unit 排在 `local-fs.target` 之前，开机多等这 90 秒，之后系统照常进入 `running`。
- 中断的 replace 留下目标盘时，udev 把卷认作齐全，systemd 去挂载；内核报 `devid 2 … is missing`、`open_ctree failed`，mount unit 同样 `failed`，但结果是 `exit-code`。

运行中：

- 挂载后，systemd 按挂载表的源设备加上 `Requires=dev-sda1.device` 与 `StopPropagatedFrom=dev-sda1.device`。拔掉这块盘就会停止 mount unit，也就是卸载。
- 在 `Options=` 中加上 `x-systemd.device-bound=no` 后，`StopPropagatedFrom=` 为空。拔掉源设备后，数据卷保持挂载，写入 100 MB 成功，内核只记录这块盘的写错误（`write_errs 34`）。
- 运行中消失的盘，在 sysfs 中仍是 `devinfo/<id>/missing=0`、`writeable=1`；`devices/sda1` 链接还在，但指向的块设备已经没了。`btrfs filesystem show` 显示它为 `MISSING`。
- 对已挂载的卷改写 mount unit 并 `daemon-reload`，`StopPropagatedFrom=` 不会消失，下一次挂载才生效。

降级挂载：

- 降级挂载前 `btrfs device ready` 返回 1（未齐），之后返回 0。之后对剩下的盘 `udevadm trigger --action=change`，`ID_BTRFS_READY=1`，设备单元就绪。
- 由 systemd 以外挂上的数据卷，会被 systemd 接管为 `active`/`mounted`，20 秒后仍挂着。
  - 挂载选项里带 `x-systemd.device-bound=no` 时，libmount 把它写进 `/run/mount/utab`，systemd 据此不加 `StopPropagatedFrom=`。
  - 不带时又会出现 `StopPropagatedFrom=`。
- 降级挂载时缺的盘显示 `missing=1`、`writeable=0`，`devices/` 下没有它。降级写入的数据与元数据会新建 `single` 块组（实测 Data single 208 MiB）。
- 降级期间把缺的盘接回：内核认出它（`btrfs filesystem show` 列出路径，`missing=0`），但仍为 `writeable=0`，`devices/` 下也没有它，要重新挂载才会重新使用。
- 重启后两块盘都在：正常挂载（不带 `degraded`）。`balance -dconvert=raid1,soft -mconvert=raid1,soft` 把单份数据转回镜像（“relocate 1 out of 5 chunks”）；Scrub 修复了回来那块盘上的过期内容（corrected 76112，uncorrectable 0）。

在 Host Agent 那样的沙箱里：

- 一个 `ProtectSystem=strict`、`PrivateTmp=yes`、`RestrictNamespaces=yes`、`RestrictAddressFamilies=AF_UNIX` 的服务，通过 `systemd-run --wait --collect` 执行 `mount`。挂载发生在宿主机的挂载命名空间，并传播进服务自己的命名空间。
- 之后用 `systemd-run` 执行 `mount -o remount,rw`：宿主机上变为读写，服务自己的命名空间里仍显示 `ro`，因为 remount 不沿共享子树传播。

更换硬盘：

- 源盘已断开时，`btrfs replace start <devid> <新分区> <挂载点>` 在后台运行。
  - `btrfs replace status -1` 输出 `28.2% done, …`，结束时是 `Started on …, finished on …`。
  - 运行期间 `exclusive_operation` 为 `device replace`。
- 完成后新盘沿用缺失盘的 devid，`missing=0`、`writeable=1`，无需重启；降级期间的 `single` 块组仍要转换。udev 数据库不知道新分区已是 Btrfs（与[加盘后的情况](2026-10-20-added-disk-shown-unused-until-reboot.md)相同）。
- 2 GB 测试盘上，剩下那块盘已分配 97% 时，replace 失败：`failed setting block group ro: -28`，`btrfs_scrub_dev … failed -28`。
- replace 进行到 12.9% 时硬复位。下次降级挂载时内核自动“continuing dev_replace … @0%”，从头再来。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| `What=UUID=` 不等成员到齐，盘起转慢时挂载失败 | 第二块盘晚 25 秒接入 | 否：systemd 等 by-uuid 设备，最多 90 秒 |
| 运行中掉的是挂载表登记的那块盘，数据卷会被卸载 | 拔掉 `findmnt` 显示的源设备 | 是 |
| `x-systemd.device-bound=no` 能避免卸载 | 同上，加选项后再拔 | 是 |
| 手动降级挂载会被 systemd 卸掉 | 挂上后观察 20 秒 | 否：被接管为 `mounted` |
| Host Agent 沙箱里的 remount 能传播 | 沙箱服务内 `findmnt` | 否：须一次挂成读写 |

## 根因

mount unit 文件是 systemd 的 fragment，没有 `x-systemd.device-bound=` 选项时，systemd 让它随挂载表中的源设备停止（`mount_propagate_stop` 对 fragment 返回真）。Btrfs 多盘卷的挂载表只登记其中一块设备，这块盘一消失，整卷就被卸载。

## 修复

- mount unit 的选项加上 `x-systemd.device-bound=no`，Host Agent 启动时把旧 unit 改写过来（[`volume.go`](../../internal/hostops/linux/volume.go) 的 `mountUnit`、`EnsureMountUnit`）。旧 unit 对已挂载的卷不生效，部署后要重启一次。
- mount unit 失败、内核能看到卷的部分硬盘时，Host Agent 依次执行（[`degraded.go`](../../internal/hostops/linux/degraded.go)）：
  1. 经 `systemd-run` 以 `degraded,ro` 挂上看一眼；
  2. 按卷标记与 sysfs 决定能否继续；
  3. 卸载；
  4. 记下留下的盘；
  5. 一次挂成 `degraded` 读写，选项同样带 `x-systemd.device-bound=no`。
- 缺盘的计数（[`ReadMissingDevices`](../../internal/hoststate/linux/datavolume.go)）同时看 `missing`、`writeable` 与 `devices/` 链接是否还指向块设备，覆盖上面三种去向。缺盘期间不做冗余方式转换。
- 测试：
  - [`TestReadMissingDevicesCountsEveryWayADiskGoes`](../../internal/hoststate/linux/datavolume_test.go) 用本页观察到的 sysfs 状态；
  - [`degraded_internal_test.go`](../../internal/hostops/linux/degraded_internal_test.go) 覆盖挂载顺序、只允许镜像、只缺一块、只在上次留下的盘上运行；
  - [`TestEnsureMountUnitStopsBindingTheVolumeToOneDisk`](../../internal/hostops/linux/volume_test.go)。

## 后续工作

- 实机：部署后重启一次，确认 `StopPropagatedFrom=` 为空；再按运行手册拔盘开机、接回。
- replace 在剩下的盘没有未分配空间时会以 `ENOSPC` 失败，界面应显示失败原因。
- 是否把虚拟机脚本做成常驻回归测试（CI 上的 KVM），等用户决定。

## 关联

- 规格：[硬盘用途与数据卷冗余](../specs/disk-roles-and-redundancy.md)
- ADR：[0025](../adr/0025-span-the-data-volume-across-disks-with-btrfs-profiles.md) 决定 6
- 架构：[存储与文件架构](../architecture/storage-and-files.md)
- 上游：systemd [`mount.c`](https://github.com/systemd/systemd/blob/main/src/core/mount.c)（`mount_is_bound_to_device`、`mount_propagate_stop`）、[`64-btrfs.rules.in`](https://github.com/systemd/systemd/blob/main/rules.d/64-btrfs.rules.in)；Btrfs [sysfs](https://btrfs.readthedocs.io/en/latest/ch-sysfs.html)、[`degraded`](https://btrfs.readthedocs.io/en/latest/Administration.html)、[`btrfs replace`](https://btrfs.readthedocs.io/en/latest/btrfs-replace.html)
