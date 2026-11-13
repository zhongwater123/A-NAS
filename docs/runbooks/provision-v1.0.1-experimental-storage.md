# 配置并验收 v1.0.1 实验数据卷

状态：active（ADR 0008 之后的系统服务部署）。Experimental NAS 保留已确认的 500,107,862,016 字节 SATA 实验盘 `ST500DM002-1BD142`（序列号 `Z2AYDZPB`，WWN `0x5000c500518d4994`）及其 Btrfs 数据卷；当前部署与验收进度见[当前状态](../status/CURRENT.md)。运行中热插拔已转为后续任务，不属于本次闭环验收。

本手册只适用于 Experimental NAS 和可丢弃测试数据。格式化动作不可回滚；不要把家庭正式数据放入本版单盘卷。

## 前提与停止条件

- 已移除 Debian 安装 U 盘；用户已确认实际实验盘型号、500 GB 标称容量、序列号和 WWN，并且产品已显示其稳定 `disk:*` ID。
- 用户已核对磁盘型号、容量、序列身份和计划列出的待清除签名。
- 若候选盘显示为系统盘、USB、removable、in-use、swap/md/dm 成员，或稳定 ID 与预期不一致，立即停止。
- 已按[部署 A-NAS 软件包](deploy-a-nas-packages.md)第 1–4 步，在开发机对同一提交运行 `make check` 与 [`make system-test`](../development/testing.md#系统测试) 后把软件包放进软件源，复制到 NAS，并在 NAS 上加好软件源。
- 明确选择仅面向可信局域网的接口名，例如 `enp2s0`，写在 `/etc/a-nas/site.conf` 的 `ANAS_SMB_INTERFACE`；不得使用空值、`0.0.0.0` 或未经核对的无线/公网接口。

## 安装系统服务

已初始化数据卷的设备升级时，不要重新执行磁盘初始化或首次配置向导，只按[部署 A-NAS 软件包](deploy-a-nas-packages.md)安装或升级软件包。安装前以 root 核对数据卷与网卡，任一不符时停止：

```bash
findmnt -T /srv/a-nas/data -o TARGET,SOURCE,FSTYPE,OPTIONS
grep '^ANAS_SMB_INTERFACE=' /etc/a-nas/site.conf
ip -brief link show dev <上一行的网卡>
```

有本机屏幕的设备在部署手册第 6 步的命令中加上 `a-nas-console=<版本>`，本地控制台见[本地控制台运行手册](operate-local-kiosk.md)。

安装或升级会重启 Host Agent，由它幂等修复现有卷的目录和 ACL；软件包不会重新分区或格式化已经挂载的数据盘。`anas-site.service` 同时校验并更新局域网 Web 入口（Caddy 随 `a-nas-host` 安装），见[启用局域网 Web 访问](enable-lan-web-access.md)。

屏保视频不随软件包发布（[ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)），安装与升级保留当前视频池。更换视频的步骤见[本地控制台运行手册](operate-local-kiosk.md#更换屏保视频)。

本地 AI 随每个版本安装（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）：模型与运行环境各是一个内容包，按内容各存一份，已装的不再下载；`a-nas` 启用 `anas-ai.socket`。核对方法见[启用相册本地 AI](enable-photo-ai.md#验证)与[启用相册物体检测](enable-photo-objects.md#验证)。`a-nas-generator` 按本机逻辑核数写入 `/run/systemd/generator/anas-ai.service.d/a-nas-reserve.conf`，让 AI 至多用到核数减 2；`anas.slice` 让后台 AI 在争用时把 CPU 让给设备上的其他工作。

`a-nas-check` 核对 Host Agent 能否以登录用户身份启动文件 Worker：它的 `CapEff` 必须含 `CAP_SETUID`/`CAP_SETGID`，日志必须出现 `file broker identity switch verified`。不满足时这一项为 `FAIL`，Web 文件与终端不可用，按[回滚](#回滚)退回上一版本；原因见 [issue #38 调查](../investigations/2026-10-08-host-agent-loses-setuid-under-systemd.md)。

宿主机需要的 Debian 软件包由 `a-nas-host` 声明，本地控制台的由 `a-nas-console` 声明（[ADR 0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)），例如影视中心 FFmpeg Worker 用的 Debian FFmpeg（[ADR 0017](../adr/0017-run-the-media-center-through-the-file-broker.md)）与文件管理 HEIC/HEIF 预览用的 libheif；`a-nas` 依赖同一版本的 `a-nas-host`。smbd 只监听启动时已有的地址，所以 `a-nas-generator` 按 `ANAS_SMB_INTERFACE` 写入 `/run/systemd/generator/smbd.service.d/a-nas-lan-address.conf`：smbd 等该接口有了 IPv4 地址才启动，每次最多等 20 秒，等不到就 5 秒后再试；重启后它不再只监听回环（[调查](../investigations/2026-11-04-smbd-starts-before-dhcp-address.md)）。安装时 smbd 若已在运行，这项配置在它下次启动时生效；没有运行且接口还没有地址时，安装照常完成，`a-nas-check` 报告 `smbd.service` 不是 active。

全新实验机首次安装使用交互向导 [`scripts/provision-v1.0.1-rc.sh`](../../scripts/provision-v1.0.1-rc.sh)。先按部署手册第 2–4 步加好软件源；向导不在软件包中，从仓库复制到 NAS 后以 root 运行。它复核稳定磁盘身份与网卡，写入 `ANAS_SMB_INTERFACE`，安装 `a-nas`、`a-nas-host` 与 `a-nas-console`，再逐步引导产品初始化：

```bash
VERSION=<版本> \
SMB_INTERFACE=enp2s0 \
EXPECTED_DISK_WWN=0x5000c500518d4994 \
EXPECTED_DISK_SERIAL=Z2AYDZPB \
  bash provision-v1.0.1-rc.sh
```

若必须逐步排障，可不用向导：按部署手册第 5–6 步安装，在本地控制台创建首个管理员，再按[生成和执行计划](#生成和执行计划)初始化数据卷。apt 失败时停止，不进入格式化流程。

完成标准：`a-nas-check` 全部 `PASS`；`/run/a-nas-ffmpeg/ffmpeg.sock` 为 `root:a-nas-photos 0660`；`/run/a-nas/host-agent.sock` 为 `root:a-nas 0660`；API 仅监听 `127.0.0.1:8080`；`/etc/chromium/policies/managed/a-nas.json` 链接到 `/usr/share/a-nas/chromium-policy.json` 并关闭密码管理器。在本地控制台只输入账号和密码完成设备启用；首个账号成为 A-NAS 产品管理员，不是 Linux root。

### 移入 Host Agent 的记录

Host Agent 的五个记录（`space-registry`、`identity-registry`、`viewing-grants`、`app-identities` 与 `volume-operations`，均为 `.json`）自 [issue #176](https://github.com/zhongwater123/A-NAS/issues/176) 起在 `/var/lib/a-nas-host`，Host Agent 启动时把旧版本留在 `/var/lib/a-nas` 的记录移过来，但只移动 root 所有、只有一个链接的普通文件。软件包安装时不核对这些记录：某个记录在新位置还没有，旧位置的又不符合时，Host Agent 启动后停用这个记录，日志 `a Host Agent record cannot be used` 给出文件名。产品服务的 `StateDirectory=a-nas` 在目录本身属主不对时会把整个目录递归改归 `a-nas`，这是记录不归 root 的可能原因。处理时先停服务，再逐个核对：

```bash
systemctl stop anas-api anas-host-agent
record=/var/lib/a-nas/app-identities.json   # 日志报出的文件
stat -c '%U:%G %h %F %y' -- "$record"
cat -- "$record"
```

- 是普通文件、内容可以确认是 Host Agent 写的（与 `getent passwd`、空间目录和数据卷的 `.a-nas-identities.json` 对照）时，换成 root 的单链接副本：`install -o root -g root -m 0600 -- "$record" "$record.new" && mv -f -- "$record.new" "$record"`。
- 符号链接、硬链接，或无法确认的内容：移出目录留作证据，例如 `mv -- "$record" /root/`。Host Agent 对这个记录从空开始：不修复任何空间的目录与 ACL，快照与管理员查看找不到空间，应用身份从空表分配、与已有账号冲突而报错。

然后执行 `systemctl start anas-host-agent anas-api`。

## 升级到统一身份（ADR 0008）

rc.5 及更早版本用系统区间 UID 创建了 A-NAS 账号（如 `admin`）。统一身份版本只接管 UID 20100–29999 内由 A-NAS 分配的账号；遇到旧账号时 Product Service 日志出现 `identity synchronization failed` 与 `conflicts with an existing host account`，且不会修改该账号。实验卷只含可丢弃测试数据，不迁移旧 UID 的文件。

1. 激活新版本前停止产品服务：`systemctl stop anas-api`。
2. 列出旧账号：只处理 `/var/lib/a-nas/space-registry.json` 中个人空间目录名对应、且 `getent passwd <name>` 显示 UID 小于 20000、shell 为 `/usr/sbin/nologin` 的账号。不得删除 `root`、`a-nas`、`anas-dev` 或其他系统账号。
3. 对每个旧账号执行 `smbpasswd -x <name>`、`userdel <name>`，若存在同名私有组再执行 `groupdel <name>`。
4. 激活新版本并启动服务后确认：`getent passwd <name>` 的 UID 与 GID 均在 20100–29999；`getent group a-nas-users a-nas-admins` 为 GID 20000/20001；日志不再出现身份冲突。
5. 管理员在“设置 → 我的账号”（`173cbb5499ce` 及更早版本为“账号管理”）中修改自己的密码，以重建自己的 Samba 凭据。成员的 Samba 凭据只能由本人重建：管理员为成员重置密码后，该成员下次登录必须自己改密，在此之前 SMB 保持停用。
6. 确认没有引用后，移除 rc.5 及更早版本遗留的 `a-nas-members` 组：

   ```bash
   getfacl -R -p /srv/a-nas/data 2>/dev/null | grep -c a-nas-members
   gpasswd -d a-nas a-nas-members
   groupdel a-nas-members
   ```

   完成标准：第一条命令输出 `0`，之后 `id a-nas` 不再含 `a-nas-members`。输出不为 `0` 时停止，先找出仍引用该组的目录。

完成标准：`/var/lib/a-nas-host/identity-registry.json` 与数据卷 `.a-nas-identities.json` 列出全部账号，二者均为 `root:root 0600`。

## 生成和执行计划

1. 在本地 Kiosk 的“设置 → 存储 → 初始化数据卷”（`173cbb5499ce` 及更早版本为“存储初始化”）打开候选盘。
2. 保存页面显示的稳定 ID、型号、容量、指纹、动作、确认短语和过期时间。
3. 再次对照机箱盘位或购买记录；只有完全一致时输入确认短语。
4. 执行后等待状态 `succeeded`。执行在设备上进行，关闭页面或重启产品服务不会打断它，向导显示进行到哪一步。Host Agent 重启、超时或 `needs_attention` 时停止操作，不生成第二个计划，先导出日志和 `lsblk --json --output NAME,PATH,SERIAL,WWN,MODEL,SIZE,TRAN,RM,TYPE,FSTYPE,MOUNTPOINTS`；核对是计划中的硬盘后，在向导中重试同一计划，它从停下处继续，并接管它已经建出的数据卷（[#165](https://github.com/zhongwater123/A-NAS/issues/165)）。

完成标准：`findmnt /srv/a-nas/data` 显示 Btrfs 与预期 UUID；`systemctl cat srv-a\x2dnas-data.mount` 显示安全挂载选项；`.a-nas-volume.json` 位于已挂载数据卷内。

## 功能验收

升级后先按[存储架构中的 ACL 表](../architecture/storage-and-files.md#卷与目录)验证权限链：容器只能穿过、空间与回收站目录为 `root:root` 且只授予对应用户；Product Service 账号 `a-nas` 必须被拒绝，Web 文件操作经 `/run/a-nas/file-broker.sock` 以用户身份执行：

```bash
namei -l /srv/a-nas/data/spaces/private/admin
getfacl -p \
  /srv/a-nas \
  /srv/a-nas/data \
  /srv/a-nas/data/spaces \
  /srv/a-nas/data/spaces/private \
  /srv/a-nas/data/spaces/private/admin \
  /srv/a-nas/data/spaces/private/admin/.a-nas-trash/admin \
  /srv/a-nas/data/spaces/shared \
  /srv/a-nas/data/spaces/shared/.a-nas-trash
setpriv --reuid=admin --regid=admin --init-groups -- ls /srv/a-nas/data/spaces/private/admin
! runuser -u a-nas -- test -r /srv/a-nas/data/spaces/private/admin
ls -l /run/a-nas/file-broker.sock
journalctl -u anas-host-agent | grep -E 'repaired drifted|file worker'
```

1. 创建管理员与两个成员，确认 Web 与 Windows SMB 都无法列出或读取另一成员个人空间。
2. 在个人空间和 `\\<NAS-IP>\Shared` 中分别由 Web 与 SMB 创建文件和子目录，再由另一端读取、改名和删除；用 SHA-256 核对大文件。
3. 先由 Web 删除一个文件，再由 SMB 删除另一个文件；确认两者都进入 Web 回收站、均可恢复相同哈希，并确认 smbd journal 没有 recycle `purging`。
4. 在备份中心为个人空间和 Shared 创建只读快照；验证浏览、重名 `409`、按文件与按文件夹恢复、全部恢复到新文件夹、找回文件和删除，不执行整卷回滚。为个人空间开启每小时定时快照并保留 2 份，经过三个整点后确认只剩最新 2 份定时快照、手动快照仍在；再改为智能保留“每小时 3 份、每天 1 份”并保存，下一个整点拍摄成功后确认计划仍为智能保留，定时快照为最新 3 份（各保留规则的组合由 Go 测试覆盖）。从 Windows SMB 直接删除一个已在快照中的文件，确认它出现在“找回文件”的“最近删除或移走”中，快照浏览把它标为“已不在空间中”。在文件管理中对一个改过的文件打开“历史版本”并还原较早的版本：当前文件进入回收站，还原出的文件在 Windows 资源管理器中显示快照里的修改时间。
5. 验证禁用账号、guest/SMB1 拒绝、SMB3 加密与签名，以及无效 Samba candidate 不替换旧配置。
6. 保持数据盘连接并正常重启，验证数据卷按 UUID 自动挂载，空间和 SMB 自动恢复；本版不执行运行中热拔插。
7. 导出 `timedatectl`、SMART、温度、容量、mount、`anas-host-agent`/`anas-photos`/`anas-api` 服务状态和审计记录作为 RC 证据。

## 回滚

在尚未执行格式化计划时，可以退回上一个版本：按[部署 A-NAS 软件包的回滚](deploy-a-nas-packages.md#回滚或恢复)执行，之后运行 `a-nas-check`。第一次换成软件包后要回到安装器的 release，也按该节执行。

- 单元与程序同在 `a-nas` 包中，一起退回。AI Worker 的单元写明模型参数，`postinst` 停止正在运行的 Worker，下一次连接启动退回版本的 Worker。
- Host Agent 的记录（空间、身份、查看授权、应用身份与数据卷操作）在 `/var/lib/a-nas-host`，只有 root 能打开（[issue #176](https://github.com/zhongwater123/A-NAS/issues/176)）。软件包的各个版本都在这里读这些记录，退回时不用移动它们。issue #176 之前的 release 在 `/var/lib/a-nas` 读，新版本启动时把留在那里的记录移过来，见[移入 Host Agent 的记录](#移入-host-agent-的记录)。
- 回滚不改变屏保视频池：产品服务的单元把独立于版本的 `/var/lib/a-nas/screensavers/current` 交给产品服务；回滚媒体按[本地控制台运行手册](operate-local-kiosk.md#回滚)。
- 不支持回滚到 ADR 0008 之前（rc.5 及更早）：旧账号已删除重建，旧版本的启动修复还会重新授予 `a-nas` 访问空间的 ACL。

执行格式化后，回滚程序不会恢复旧分区或签名；保留磁盘现状并按 `needs_attention` 调查。绝不对不确定的 `/dev/sdX` 运行手工擦除命令。

## 开发机暂存注意事项

- 先按[测试与验证](../development/testing.md)验证候选提交，再按[部署 A-NAS 软件包](deploy-a-nas-packages.md)第 1–2 步发布并复制到 NAS。SSH 配置见[连接手册](bootstrap-experimental-nas-ssh.md)。
- 开发机连不到 NAS 时，把软件源目录交给部署机原样复制（部署手册第 2 步）；apt 按签名核对，复制的人不在信任链上。
- `make release` 只发布通过了门禁与系统测试的软件包：`build/.validated-build` 由 Makefile 的 `check` 目标按提交、产品版本与二进制哈希写出，`build/.system-tested` 由通过的系统测试按软件包哈希写出。无 Git 的 ext4 副本运行 `check-steps` 只提供测试证据，不生成 `build/.validated-build`。
- 屏保视频不随软件包发布，更换时使用[本地控制台手册](operate-local-kiosk.md#更换屏保视频)中的独立上传流程。
- 在 PowerShell 5.1 中运行 `deploy-dev.ps1` 不要加 `2>&1`：Windows OpenSSH 的 `close - IO is still pending` 会变成错误记录，使脚本在 `ErrorActionPreference = 'Stop'` 下中止。

## 关联

- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [存储与文件架构](../architecture/storage-and-files.md)
- [部署 A-NAS 软件包](deploy-a-nas-packages.md)
- [`scripts/build-packages.sh`](../../scripts/build-packages.sh)
- [`scripts/provision-v1.0.1-rc.sh`](../../scripts/provision-v1.0.1-rc.sh)
