# 配置并验收 v1.0.1 实验数据卷

状态：active；Experimental NAS 已安装 `v1.0.1-rc.5`（`69c97abe191f`），并保留已确认的 500,107,862,016 字节 SATA 实验盘 `ST500DM002-1BD142`（序列号 `Z2AYDZPB`，WWN `0x5000c500518d4994`）及其 Btrfs 数据卷。启动自愈后的空间、ACL 与回收站权限检查已通过，Web/SMB 用户闭环尚待实机回归。运行中热插拔已转为后续任务，不属于本次闭环验收。

本手册只适用于 Experimental NAS 和可丢弃测试数据。格式化动作不可回滚；不要把家庭正式数据放入本版单盘卷。

## 前提与停止条件

- 已移除 Debian 安装 U 盘；用户已确认实际实验盘型号、500 GB 标称容量、序列号和 WWN，并且产品已显示其稳定 `disk:*` ID。
- 用户已核对磁盘型号、容量、序列身份和计划列出的待清除签名。
- 若候选盘显示为系统盘、USB、removable、in-use、swap/md/dm 成员，或稳定 ID 与预期不一致，立即停止。
- 已在开发机运行 `make check`，并把同一 RC 制品上传到不可变的 `/home/anas-dev/apps/a-nas/releases/<GIT_SHA>`；不要把尚未安装的 stage 目录误记为 `current`。
- 明确选择仅面向可信局域网的接口名，例如 `enp3s0`；不得使用空值、`0.0.0.0` 或未经核对的无线/公网接口。

## 安装系统服务

2026-10-07 当前待安装值为：

```bash
source_release=/home/anas-dev/apps/a-nas/releases/69c97abe191f
release_id=v1.0.1-rc.5-69c97abe191f
smb_interface=enp2s0
```

从 rc.4 升级时不要重新执行磁盘初始化或首次配置向导；只安装 ACL 运行依赖、核对暂存制品并运行系统服务安装器：

```bash
source_release=/home/anas-dev/apps/a-nas/releases/69c97abe191f
release_id=v1.0.1-rc.5-69c97abe191f
smb_interface=enp2s0

apt-get update
apt-get install --no-install-recommends acl

printf '%s  %s\n' \
  350a58c1f510c257fbee309ae8d4a81126db2456ccd6c3bae9cf7fb9dd270513 "$source_release/anas-api" \
  94ca206e7f4ea0aea568cbc850268bfe00b9d96da446d0f0b584ec4ac9abd744 "$source_release/anas-host-agent" |
  sha256sum --check

findmnt -T /srv/a-nas/data -o TARGET,SOURCE,FSTYPE,OPTIONS
ip -brief link show dev "$smb_interface"

"$source_release/install-v1.0.1-system-services.sh" \
  "$source_release" "$release_id" "$smb_interface"
```

安装器会切换系统服务并由 Host Agent 幂等修复现有卷的目录和 ACL；它不会重新分区或格式化已经挂载的数据盘。如果哈希、挂载或网卡核对失败，立即停止。

全新实验机首次安装才使用发布目录中的交互向导；它会复核稳定磁盘身份、安装依赖、停止旧用户级预览服务、安装系统服务并逐步引导产品初始化：

```bash
source_release=/home/anas-dev/apps/a-nas/releases/<GIT_SHA>

SOURCE_RELEASE="$source_release" \
RELEASE_ID=v1.0.1-rc.4-<GIT_SHA> \
SMB_INTERFACE=enp2s0 \
EXPECTED_DISK_WWN=0x5000c500518d4994 \
EXPECTED_DISK_SERIAL=Z2AYDZPB \
  "$source_release/provision-v1.0.1-rc.sh"
```

若必须逐步排障，可不用向导而执行以下等价安装步骤；把示例值替换为本次 RC：

```bash
# 仅在仓库源可达时安装一次；若 apt 失败，停止，不进入格式化流程。
apt-get update
apt-get install --no-install-recommends acl btrfs-progs parted smartmontools samba libsqlite3-0

source_release=/home/anas-dev/apps/a-nas/releases/<GIT_SHA>
release_id=v1.0.1-rc.4-<GIT_SHA>
smb_interface=enp3s0

readlink -f /home/anas-dev/apps/a-nas/current
ip -brief link

"$source_release/install-v1.0.1-system-services.sh" \
  "$source_release" "$release_id" "$smb_interface"
```

完成标准：`/opt/a-nas/current` 指向 root 所有的目标 release；两个系统服务 active；`/run/a-nas/host-agent.sock` 为 `root:a-nas 0660`；API 仅监听 `127.0.0.1:8080`；`/etc/chromium/policies/managed/a-nas.json` 为 root 所有并关闭密码管理器。在本地控制台只输入账号和密码完成设备启用；首个账号成为 A-NAS 产品管理员，不是 Linux root。

## 升级到统一身份（ADR 0008）

rc.5 及更早版本用系统区间 UID 创建了 A-NAS 账号（如 `admin`）。统一身份版本只接管 UID 20100–29999 内由 A-NAS 分配的账号；遇到旧账号时 Product Service 日志出现 `identity synchronization failed` 与 `conflicts with an existing host account`，且不会修改该账号。实验卷只含可丢弃测试数据，不迁移旧 UID 的文件。

1. 激活新版本前停止产品服务：`systemctl stop anas-api`。
2. 列出旧账号：只处理 `/var/lib/a-nas/space-registry.json` 中个人空间目录名对应、且 `getent passwd <name>` 显示 UID 小于 20000、shell 为 `/usr/sbin/nologin` 的账号。不得删除 `root`、`a-nas`、`anas-dev` 或其他系统账号。
3. 对每个旧账号执行 `smbpasswd -x <name>`、`userdel <name>`，若存在同名私有组再执行 `groupdel <name>`。
4. 激活新版本并启动服务后确认：`getent passwd <name>` 的 UID 与 GID 均在 20100–29999；`getent group a-nas-users a-nas-admins` 为 GID 20000/20001；日志不再出现身份冲突。
5. 管理员登录 Web，为自己和每个成员重置密码，以重建 Samba 凭据。

完成标准：`/var/lib/a-nas/identity-registry.json` 与数据卷 `.a-nas-identities.json` 列出全部账号，二者均为 `root:root 0600`。

## 生成和执行计划

1. 在本地 Kiosk 的“存储初始化”打开候选盘。
2. 保存页面显示的稳定 ID、型号、容量、指纹、动作、确认短语和过期时间。
3. 再次对照机箱盘位或购买记录；只有完全一致时输入确认短语。
4. 执行后等待状态 `succeeded`。任何服务重启、超时或 `needs_attention` 都停止操作，不生成第二个计划，先导出日志和 `lsblk --json --output NAME,PATH,SERIAL,WWN,MODEL,SIZE,TRAN,RM,TYPE,FSTYPE,MOUNTPOINTS`。

完成标准：`findmnt /srv/a-nas/data` 显示 Btrfs 与预期 UUID；`systemctl cat srv-a\x2dnas-data.mount` 显示安全挂载选项；`.a-nas-volume.json` 位于已挂载数据卷内。

## 功能验收

升级后先按[存储架构中的 ACL 表](../architecture/storage-and-files.md#卷与目录)验证权限链：容器只能穿过、空间与回收站目录为 `root:root` 且只授予对应用户（另含过渡条目 `user:a-nas`）：

```bash
namei -l /srv/a-nas/data/spaces/private/admin
getfacl -p \
  /srv/a-nas \
  /srv/a-nas/data/spaces \
  /srv/a-nas/data/spaces/private \
  /srv/a-nas/data/spaces/private/admin \
  /srv/a-nas/data/spaces/private/admin/.a-nas-trash/admin \
  /srv/a-nas/data/spaces/shared \
  /srv/a-nas/data/spaces/shared/.a-nas-trash
setpriv --reuid=admin --regid=admin --init-groups -- ls /srv/a-nas/data/spaces/private/admin
runuser -u a-nas -- test -r /srv/a-nas/data/spaces/private/admin
journalctl -u anas-host-agent | grep 'repaired drifted'
```

rc.5 及更早版本的 `a-nas-members` 组不再使用；确认无引用后可执行 `gpasswd -d a-nas a-nas-members` 与 `groupdel a-nas-members`。

1. 创建管理员与两个成员，确认 Web 与 Windows SMB 都无法列出或读取另一成员个人空间。
2. 在个人空间和 `\\<NAS-IP>\Shared` 中分别由 Web 与 SMB 创建文件和子目录，再由另一端读取、改名和删除；用 SHA-256 核对大文件。
3. 先由 Web 删除一个文件，再由 SMB 删除另一个文件；确认两者都进入 Web 回收站、均可恢复相同哈希，并确认 smbd journal 没有 recycle `purging`。
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
