# 镜像缺盘的开机路径是否做成虚拟机回归测试

状态：pending
更新时间：2026-11-06

## 问题

镜像缺盘时能否继续服务，取决于 systemd、udev 与 Btrfs 的配合：开机等盘、mount unit 不随单块设备停止、Host Agent 经 `systemd-run` 降级挂载。容器版系统测试没有 udev，也不能插拔硬盘，这些行为只在 2026-11-06 的一次性虚拟机实验中验证过（[调查](../investigations/2026-11-06-mirror-missing-a-disk.md)）。

单元测试只检查 Host Agent 发出的命令，不检查 systemd 与内核真的这样做。以后有人改 mount unit、Host Agent 的沙箱，或者 Debian 升级 systemd，这条路径都可能悄悄失效，直到真的坏盘才被发现。

## 选项

- **只在实机验收时检查**：不增加测试。每次相关改动后，按[运行手册](../runbooks/replace-a-mirror-disk.md)在实验 NAS 上拔盘开机。代价是要人到现场，而且容易漏做。
- **做成虚拟机回归测试**：把这次实验的脚本整理进 `scripts/`。
  - 内容：用 QEMU/KVM 起 Debian 13 云镜像，装上构建出的 Host Agent，检查开机缺盘、运行中掉盘、接回与更换。
  - 运行：本机 WSL 有 `/dev/kvm`；GitHub Actions 的 Linux runner 也能开 KVM。每次运行约 10 分钟，并要下载约 440 MB 的云镜像。
  - 代价：多维护一套脚本，CI 时间变长。
- **做成虚拟机回归测试，但只在相关路径变化时运行**：同上，CI 只在 `internal/hostops/linux/volume*.go`、`degraded.go` 或 Host Agent 的 unit 文件变化时运行。

## 决定

待用户决定。

## 关联

- [硬盘用途与数据卷冗余规格](../specs/disk-roles-and-redundancy.md) 的 M2 验收证据
- [ADR 0025](../adr/0025-span-the-data-volume-across-disks-with-btrfs-profiles.md) 的后果
