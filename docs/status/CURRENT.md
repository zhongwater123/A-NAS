# 当前状态

更新时间：2026-10-07

## 当前阶段

v1.0.1“实验 NAS 基础存储与共享闭环”是当前主线。本地代码已实现账号、单盘 Btrfs 初始化计划、文件、SMB、回收站、手动快照、磁盘健康、产品 API、Web UI 与系统级部署边界；Experimental NAS 已恢复在线，SATA 实验盘已在冷启动后被发现并由用户确认。真实 Btrfs、Samba、systemd、文件、回收站和快照验收仍是 RC 发布门禁，运行中热插拔已转为后续任务。

提交 `4e98e86b77c5` 当前仍由 NAS 运行，Kiosk、两个用户级服务、`/healthz` 与网络校时正常，已重新冻结为 `v1.0.0`。相册规格、ADR、技术设计与模型研究全部保留，实施暂停到基础存储闭环稳定后继续。

## 已就绪

- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)、[存储架构](../architecture/storage-and-files.md)、[ADR 0007](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)和 [v1.0.1 发布记录](../releases/v1.0.1.md)已建立。
- 磁盘状态包含稳定 ID、removable/in-use、文件系统、SMART、温度、数据卷资格及拒绝原因；公开接口不返回设备路径。
- 存储计划持久化于 SQLite，支持十分钟过期、确认短语、执行前身份复核、幂等成功结果和中断后 `needs_attention`。
- Linux 执行器使用固定命令创建 GPT、单盘 Btrfs、UUID mount unit、空间子卷和卷身份标记；文件服务在缺少真实 Btrfs/标记时拒绝写入。
- 首次初始化、Argon2id、服务端会话、CSRF、管理员/成员、个人空间和唯一 Shared Policy 已实现；普通浏览不能发现其他成员个人空间。
- Web 文件管理支持目录、流式上传、Range 下载、移动/重命名、复制和删除；Web 与 Samba 回收区可对账，默认保留 30 天。
- 个人/共享手动只读快照支持浏览、冲突拒绝、按文件恢复和删除，不暴露给 SMB，不提供整卷回滚。
- Samba 配置要求 SMB3、加密和签名，禁用 guest/SMB1/Unix extensions，只绑定显式接口；candidate 通过 `testparm` 后才原子替换。
- root Host Agent 与非特权产品服务通过 `root:a-nas 0660` UDS 通信；系统单元使用 root 所有的 `/opt/a-nas/current` 发布目录。
- OpenAPI 已扩展到会话、成员、卷、计划、操作、空间、文件、回收站和快照；Web 桌面启用对应工作流，相册继续显示为规划中。
- 系统时间调查已关闭：Debian `systemd-timesyncd` 在上次实机检查时为 enabled/active、`NTPSynchronized=yes`，桌面墙钟按真实分钟推进，见[调查记录](../investigations/2026-10-06-system-clock-drift-and-network-sync.md)。

## 下一步

1. `v1.0.1-rc.1` 因用户级状态目录权限缺口被拒绝；完成修复检查并部署不可变的 `v1.0.1-rc.2` 制品。
2. 严格按[实机配置与验收手册](../runbooks/provision-v1.0.1-experimental-storage.md)安装系统服务，由用户核对计划后才格式化实验盘。
3. 完成 Web/Windows SMB 大文件哈希、权限矩阵、回收站、快照、正常重启恢复、SMART、容量、服务和审计证据。全部通过后才创建 `v1.0.1` 标签。
4. 后续补齐安全移除、运行中 SATA 热拔插、同盘重新接入和自动恢复；该项不阻塞 v1.0.1。
5. 基础闭环稳定后，从已保留的[相册技术设计](../architecture/photo-library.md)恢复 Catalog、受管存储和 Fake AI 工作。

## 外部条件与限制

- Experimental NAS 当前在线；仅完成只读主机探测，不得把静态或 Fake 测试描述为实机通过。
- v1.0.1 只使用可丢弃测试数据。单盘 Btrfs 不提供冗余、备份或家庭生产数据可靠性承诺。
- 2026-10-07 冷启动后发现 `ST500DM002-1BD142` SATA 盘，容量 500,107,862,016 字节、序列号 `Z2AYDZPB`、WWN `0x5000c500518d4994`，位于 `ata7/host6`，没有分区、文件系统或签名且未被占用；用户已明确确认这是可清除实验盘。其 `HOTPLUG=0`，运行中热插拔能力尚未实现和验收。
- Kiosk 的浏览器约束和 VT 恢复仍待验收，见[开放调查](../investigations/2026-10-06-kiosk-browser-confinement.md)。

## 验证基线

```bash
bash scripts/check-dev-env.sh
make check
```
