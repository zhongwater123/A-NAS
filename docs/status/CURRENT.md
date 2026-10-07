# 当前状态

更新时间：2026-10-07

## 当前阶段

v1.0.1“实验 NAS 基础存储与共享闭环”是当前主线。Experimental NAS 已运行 `v1.0.1-rc.4`（提交 `79851b862904`），首个管理员和真实单盘 Btrfs 数据卷均已创建。格式化、UUID 挂载和卷身份标记成功，但文件、回收站与快照接口被错误的空间父目录权限共同阻断，Web/SMB 写入身份与回收站权限还存在 [Issue #9](https://github.com/zhongwater123/A-NAS/issues/9) 所述风险；闭环尚未通过。

rc.4 已消除空盘计划蓝屏并完成真实格式化。随后实机证据显示 `spaces` 为 `root:root 0770`、`spaces/private` 为 `root:root 0750`，而 Product Service 以 `a-nas` 运行，直接 `stat/find` 即得到 `Permission denied`。下一 RC 修复在独立分支开发：容器使用仅可穿过的 `0710`，个人空间使用 default POSIX ACL，共用回收站由 Host Agent 预建，并在 Host Agent 启动时幂等修复 rc.4 卷；最小回归已通过，完整门禁、不可变制品和实机验证尚未完成。

提交 `4e98e86b77c5` 已重新冻结为 `v1.0.0`。相册规格、ADR、技术设计与模型研究全部保留，实施暂停到基础存储闭环稳定后继续。

## 已就绪

- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)、[存储架构](../architecture/storage-and-files.md)、[ADR 0007](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)和 [v1.0.1 发布记录](../releases/v1.0.1.md)已建立。
- 磁盘状态包含稳定 ID、removable/in-use、文件系统、SMART、温度、数据卷资格及拒绝原因；公开接口不返回设备路径。
- 存储计划持久化于 SQLite，支持十分钟过期、确认短语、执行前身份复核、幂等成功结果和中断后 `needs_attention`。
- Linux 执行器使用固定命令创建 GPT、单盘 Btrfs、UUID mount unit、空间子卷和卷身份标记；文件服务在缺少真实 Btrfs/标记时拒绝写入。
- 本机设备启用、Argon2id、服务端会话、CSRF、管理员/成员、个人空间和唯一 Shared Policy 已实现；首次启用只要求账号和密码。
- Web 文件管理、Samba、回收站、手动快照、审计、产品 API 和 React 页面已实现并有自动化覆盖。
- Web 桌面终端已本地实现：仅管理员可通过回环同源 WebSocket 打开以产品服务用户运行的 PTY Shell，默认关闭，见[终端规格](../specs/web-terminal.md)；Experimental NAS 尚未启用。
- root Host Agent 与非特权产品服务通过 `root:a-nas 0660` UDS 通信；系统单元使用 root 所有的 `/opt/a-nas/current` 发布目录。
- rc.4 让空盘计划稳定输出数组、兼容旧 `null`、显示存储操作进度并为桌面窗口增加错误边界，见[蓝屏调查](../investigations/2026-10-07-blank-disk-plan-ui-crash.md)。
- rc.4 使用 root 管理的 Chromium policy 禁止保存密码、通行密钥和同步；更广的 Kiosk 约束仍见[开放调查](../investigations/2026-10-06-kiosk-browser-confinement.md)。
- rc.4 已创建 `/dev/sda1` Btrfs 数据卷（UUID `09e275fe-794a-458d-8200-b6e67c55cc22`）并挂载到 `/srv/a-nas/data`；卷 marker 与 UUID 一致，API、Host Agent、Kiosk 和 Samba 服务均 active。
- 文件闭环权限根因与修复边界已记录在[数据卷空间权限调查](../investigations/2026-10-07-data-volume-space-permissions.md)；父目录、ACL、共用回收站和启动自愈的最小 Go 回归已由红转绿。
- 开发循环使用受影响测试，完整门禁为每个不可变 RC 制品只执行一次；部署按提交、版本和二进制哈希复用验证证明。
- `make check VERSION=v1.0.1-rc.4` 已通过前端类型/测试/构建、文档、运维、Go vet、Go 测试和发布二进制构建；stage 复用了该证明并核对 API SHA-256 `c92fd487…ab3359`、Host Agent SHA-256 `9a9d90be…2616b`。
- Debian `systemd-timesyncd` 在上次实机检查时为 enabled/active、`NTPSynchronized=yes`，见[时间调查](../investigations/2026-10-06-system-clock-drift-and-network-sync.md)。

## 下一步

1. 完成空间权限/ACL/回收站修复的相关测试与一次完整 `make check VERSION=v1.0.1-rc.5`，提交并创建不可变 rc.5 制品；不得移动既有 RC 标签。
2. 在 NAS 安装 `acl` 运行依赖并切换 rc.5；Host Agent 启动自愈必须修复现有卷，不得重新格式化数据盘。
3. 用 `namei`、`getfacl` 和 `runuser -u a-nas` 验证父目录、个人空间、Shared 与两个回收站；确认文件/回收站/快照接口不再返回通用失败。
4. 完成个人与 Shared 的 Web/Windows SMB 双向读写、大文件哈希、Web 先删后 SMB 删除、恢复、快照、正常重启、SMART、容量、服务和审计证据。全部通过后才创建 `v1.0.1` 标签。
5. 后续补齐安全移除、运行中 SATA 热拔插、同盘重新接入和自动恢复；基础闭环稳定后恢复相册主线。

## 外部条件与限制

- Experimental NAS 当前在线并运行 rc.4；Btrfs 在线不等于文件与共享闭环通过，rc.4 当前权限模型不可用于验收数据。
- v1.0.1 只使用可丢弃测试数据。单盘 Btrfs 不提供冗余、备份或家庭生产数据可靠性承诺。
- SATA 实验盘为 `ST500DM002-1BD142`，容量 500,107,862,016 字节、序列号 `Z2AYDZPB`、WWN `0x5000c500518d4994`，位于 `ata7/host6`；现已创建 `/dev/sda1` Btrfs。其 `HOTPLUG=0`，运行中热插拔能力尚未实现和验收。
- Kiosk 的 URL、网络、快捷键、profile 生命周期和 VT 恢复仍未完成产品级验收。

## 验证基线

```bash
# 编辑循环按变化选择目标
go test ./internal/hostops/linux ./internal/files
make ops-check

# 每个 RC 提交一次完整门禁
make check VERSION=v1.0.1-rc.5
```
