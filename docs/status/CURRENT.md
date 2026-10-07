# 当前状态

更新时间：2026-10-07

## 当前阶段

v1.0.1“实验 NAS 基础存储与共享闭环”是当前主线。Experimental NAS 正运行 `v1.0.1-rc.3` 系统服务，首个管理员已成功创建；已完整验证并标记 `v1.0.1-rc.4`（提交 `79851b862904`），制品已 stage 到 `/home/anas-dev/apps/a-nas/releases/79851b862904`，等待 root 安装。真实 Btrfs、SMB、文件、回收站和快照验收尚未开始；运行中 SATA 热插拔已转为后续任务。

rc.3 生成空白实验盘计划时把空签名列表编码成 `null`，导致 React 存储面板异常并只留下蓝色壁纸；journal、进程和块设备证据证明 Host Agent 没有收到执行请求，实验盘没有被格式化。同期 Chromium 弹出保存密码提示，暴露 Kiosk 缺少系统托管策略。两项修复已包含在 rc.4，自动化门禁与 stage 哈希核验通过，仍需安装后的实体显示器回归。

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
- 开发循环使用受影响测试，完整门禁为每个不可变 RC 制品只执行一次；部署按提交、版本和二进制哈希复用验证证明。
- `make check VERSION=v1.0.1-rc.4` 已通过前端类型/测试/构建、文档、运维、Go vet、Go 测试和发布二进制构建；stage 复用了该证明并核对 API SHA-256 `c92fd487…ab3359`、Host Agent SHA-256 `9a9d90be…2616b`。
- Debian `systemd-timesyncd` 在上次实机检查时为 enabled/active、`NTPSynchronized=yes`，见[时间调查](../investigations/2026-10-06-system-clock-drift-and-network-sync.md)。

## 下一步

1. 由 root 从已 stage 的不可变目录安装 rc.4，确认 `/opt/a-nas/current`、服务状态和 root 所有的 Chromium policy。
2. 在实体显示器确认密码提示不再出现，并重新生成空盘计划验证桌面不蓝屏；此前计划没有执行，不需要恢复磁盘。
3. 用户重新核对稳定磁盘 ID、型号、容量和指纹后，才执行首次真实格式化。
4. 完成 Web/Windows SMB 大文件哈希、权限矩阵、回收站、快照、正常重启恢复、SMART、容量、服务和审计证据。全部通过后才创建 `v1.0.1` 标签。
5. 后续补齐安全移除、运行中 SATA 热拔插、同盘重新接入和自动恢复；基础闭环稳定后恢复相册主线。

## 外部条件与限制

- Experimental NAS 当前在线并运行 rc.3；rc.4 仅已 stage，尚未切换 root 服务。系统服务 active 不等于存储闭环实机通过。
- v1.0.1 只使用可丢弃测试数据。单盘 Btrfs 不提供冗余、备份或家庭生产数据可靠性承诺。
- SATA 实验盘为 `ST500DM002-1BD142`，容量 500,107,862,016 字节、序列号 `Z2AYDZPB`、WWN `0x5000c500518d4994`，位于 `ata7/host6`。蓝屏后仍没有分区、文件系统、UUID 或挂载；其 `HOTPLUG=0`，运行中热插拔能力尚未实现和验收。
- Kiosk 的 URL、网络、快捷键、profile 生命周期和 VT 恢复仍未完成产品级验收。

## 验证基线

```bash
# 编辑循环按变化选择目标
go test ./internal/storage
make web-test
make ops-check

# 每个 RC 提交一次完整门禁
make check VERSION=v1.0.1-rc.N
```
