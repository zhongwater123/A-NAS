# 配置并验收 v1.0.1 实验数据卷

状态：active；NAS 已于 2026-10-07 恢复在线。用户已确认 500,107,862,016 字节的 SATA 盘 `ST500DM002-1BD142`（序列号 `Z2AYDZPB`，WWN `0x5000c500518d4994`）是可清除实验盘；该盘没有现有签名且尚未格式化。运行中热插拔已转为后续任务，不属于本次闭环验收。

本手册只适用于 Experimental NAS 和可丢弃测试数据。格式化动作不可回滚；不要把家庭正式数据放入本版单盘卷。

## 前提与停止条件

- 已移除 Debian 安装 U 盘；用户已确认实际实验盘型号、500 GB 标称容量、序列号和 WWN，并且产品已显示其稳定 `disk:*` ID。
- 用户已核对磁盘型号、容量、序列身份和计划列出的待清除签名。
- 若候选盘显示为系统盘、USB、removable、in-use、swap/md/dm 成员，或稳定 ID 与预期不一致，立即停止。
- 已在开发机运行 `make check`，并把同一 RC 制品上传到 `/home/anas-dev/apps/a-nas/current`。
- 明确选择仅面向可信局域网的接口名，例如 `enp3s0`；不得使用空值、`0.0.0.0` 或未经核对的无线/公网接口。

## 安装系统服务

首个 RC 推荐以 root 运行发布目录中的交互向导；它会复核稳定磁盘身份、安装依赖、停止旧用户级预览服务、安装系统服务并逐步引导产品初始化：

```bash
source_release=/home/anas-dev/apps/a-nas/releases/<GIT_SHA>

SOURCE_RELEASE="$source_release" \
RELEASE_ID=v1.0.1-rc.2-<GIT_SHA> \
SMB_INTERFACE=enp2s0 \
EXPECTED_DISK_WWN=0x5000c500518d4994 \
EXPECTED_DISK_SERIAL=Z2AYDZPB \
  "$source_release/provision-v1.0.1-rc.sh"
```

若必须逐步排障，可不用向导而执行以下等价安装步骤；把示例值替换为本次 RC：

```bash
# 仅在仓库源可达时安装一次；若 apt 失败，停止，不进入格式化流程。
apt-get update
apt-get install --no-install-recommends btrfs-progs parted smartmontools samba libsqlite3-0

source_release=/home/anas-dev/apps/a-nas/releases/<GIT_SHA>
release_id=v1.0.1-rc.2-<GIT_SHA>
smb_interface=enp3s0

readlink -f /home/anas-dev/apps/a-nas/current
ip -brief link

"$source_release/install-v1.0.1-system-services.sh" \
  "$source_release" "$release_id" "$smb_interface"
```

完成标准：`/opt/a-nas/current` 指向 root 所有的目标 release；两个系统服务 active；`/run/a-nas/host-agent.sock` 为 `root:a-nas 0660`；API 仅监听 `127.0.0.1:8080`。从 API 日志取得仅首次初始化可用的 setup code，并立即创建管理员。

## 生成和执行计划

1. 在本地 Kiosk 的“存储初始化”打开候选盘。
2. 保存页面显示的稳定 ID、型号、容量、指纹、动作、确认短语和过期时间。
3. 再次对照机箱盘位或购买记录；只有完全一致时输入确认短语。
4. 执行后等待状态 `succeeded`。任何服务重启、超时或 `needs_attention` 都停止操作，不生成第二个计划，先导出日志和 `lsblk --json --output NAME,PATH,SERIAL,WWN,MODEL,SIZE,TRAN,RM,TYPE,FSTYPE,MOUNTPOINTS`。

完成标准：`findmnt /srv/a-nas/data` 显示 Btrfs 与预期 UUID；`systemctl cat srv-a\x2dnas-data.mount` 显示安全挂载选项；`.a-nas-volume.json` 位于已挂载数据卷内。

## 功能验收

1. 创建管理员与两个成员，确认 Web 与 Windows SMB 都无法列出或读取另一成员个人空间。
2. 分别通过 Web 与 `\\<NAS-IP>\Shared` 写入大文件，双方读取并用 SHA-256 核对。
3. 双向执行创建、重命名、删除；确认 Web 回收站可见 Samba 删除并能恢复相同哈希。
4. 为个人空间和 Shared 创建只读快照；验证浏览、重名 `409`、按文件恢复和删除，不执行整卷回滚。
5. 验证禁用账号、guest/SMB1 拒绝、SMB3 加密与签名，以及无效 Samba candidate 不替换旧配置。
6. 保持数据盘连接并正常重启，验证数据卷按 UUID 自动挂载，空间和 SMB 自动恢复；本版不执行运行中热拔插。
7. 导出 `timedatectl`、SMART、温度、容量、mount、两个服务状态和审计记录作为 RC 证据。

## 回滚

在尚未执行格式化计划时，可停止系统服务、把 `/opt/a-nas/current` 切回上一 root release 并重启。执行格式化后，回滚程序不会恢复旧分区或签名；保留磁盘现状并按 `needs_attention` 调查。绝不对不确定的 `/dev/sdX` 运行手工擦除命令。

## 关联

- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [存储与文件架构](../architecture/storage-and-files.md)
- [`scripts/install-v1.0.1-system-services.sh`](../../scripts/install-v1.0.1-system-services.sh)
- [`scripts/provision-v1.0.1-rc.sh`](../../scripts/provision-v1.0.1-rc.sh)
