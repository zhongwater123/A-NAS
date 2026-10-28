# 配置并验收 v1.0.1 实验数据卷

状态：active（ADR 0008 之后的系统服务部署）。Experimental NAS 保留已确认的 500,107,862,016 字节 SATA 实验盘 `ST500DM002-1BD142`（序列号 `Z2AYDZPB`，WWN `0x5000c500518d4994`）及其 Btrfs 数据卷；当前部署与验收进度见[当前状态](../status/CURRENT.md)。运行中热插拔已转为后续任务，不属于本次闭环验收。

本手册只适用于 Experimental NAS 和可丢弃测试数据。格式化动作不可回滚；不要把家庭正式数据放入本版单盘卷。

## 前提与停止条件

- 已移除 Debian 安装 U 盘；用户已确认实际实验盘型号、500 GB 标称容量、序列号和 WWN，并且产品已显示其稳定 `disk:*` ID。
- 用户已核对磁盘型号、容量、序列身份和计划列出的待清除签名。
- 若候选盘显示为系统盘、USB、removable、in-use、swap/md/dm 成员，或稳定 ID 与预期不一致，立即停止。
- 已在开发机运行 `make check` 与 [`make system-test`](../development/testing.md#系统测试)，并以 `deploy-dev.ps1 -StageOnly` 把同一制品上传到不可变的 `/home/anas-dev/apps/a-nas/releases/<12 位提交 SHA>`；不要把尚未安装的 stage 目录误记为 `current`。
- 明确选择仅面向可信局域网的接口名，例如 `enp2s0`；不得使用空值、`0.0.0.0` 或未经核对的无线/公网接口。

## 安装系统服务

已初始化数据卷的设备升级时，不要重新执行磁盘初始化或首次配置向导，只核对暂存制品并运行系统服务安装器：

1. 在开发机对候选提交运行门禁，再只暂存不激活：`scripts/deploy-dev.ps1 -NasHost <NAS_HOST> -StageOnly`。release 目录以 12 位提交 SHA 命名，脚本会输出 API 与 Host Agent 的 SHA-256。不要省略 `-StageOnly`：没有它时脚本会安装 ADR 0008 之前的用户级服务，见[旧部署手册](deploy-web-preview-to-experimental-nas.md)。
2. 以 root 查看本地控制台以哪个账号运行：

   ```bash
   systemctl show -p User --value anas-kiosk@tty1.service
   ```

   结果是 `anas-dev` 时，这是第一次安装含 [issue #174](https://github.com/zhongwater123/A-NAS/issues/174) 的 release：安装器会把本地控制台换成 `a-nas-kiosk`。先按[本地控制台运行手册的迁移前步骤](operate-local-kiosk.md#迁移前)记下当前状态，并在本机屏幕上退出登录，再继续。
3. 以 root 核对并安装：

   ```bash
   release_id=<12 位提交 SHA>
   source_release=/home/anas-dev/apps/a-nas/releases/$release_id
   smb_interface=enp2s0

   cat "$source_release/RELEASE"
   printf '%s  %s\n' \
     <API SHA-256> "$source_release/anas-api" \
     <Host Agent SHA-256> "$source_release/anas-host-agent" |
     sha256sum --check

   findmnt -T /srv/a-nas/data -o TARGET,SOURCE,FSTYPE,OPTIONS
   ip -brief link show dev "$smb_interface"

   "$source_release/install-v1.0.1-system-services.sh" \
     "$source_release" "$release_id" "$smb_interface"
   ```

4. 安装器已重启正在运行的本地控制台：它以 `a-nas-kiosk` 运行，启动程序来自 `/opt/a-nas/current`，不再需要切换 `anas-dev` 名下的启动链接。第一次迁移后，在本机屏幕上重新登录，再按[迁移后的验证](operate-local-kiosk.md#迁移后的验证)核对。其余时候确认：

   ```bash
   test -x /opt/a-nas/current/kiosk-launcher
   systemctl show -p User --value anas-kiosk@tty1.service   # a-nas-kiosk
   ```

安装器会切换系统服务并由 Host Agent 幂等修复现有卷的目录和 ACL；它不会重新分区或格式化已经挂载的数据盘。已安装 Caddy 时，安装器同时校验并更新局域网 Web 入口，见[启用局域网 Web 访问](enable-lan-web-access.md)。如果哈希、挂载或网卡核对失败，立即停止。

屏保视频不随 release 发布（[ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)），安装器保留当前视频池。首次升级时，安装器会导入旧安装器留下的池。更换视频的步骤见[本地控制台运行手册](operate-local-kiosk.md#更换屏保视频)。

本地 AI 随每个 release 安装（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）：安装器把 release 引用的模型与运行环境按内容各存一份，已有时直接复用，并启用 `anas-ai.socket`。第一次安装带 AI 的 release 时按[启用相册本地 AI](enable-photo-ai.md)核对，第一次安装带物体检测器的 release 时按[启用相册物体检测](enable-photo-objects.md)核对。安装器还按本机逻辑核数写入 `/etc/systemd/system/anas-ai.service.d/cpu-reserve.conf`，让 AI 至多用到核数减 2，并安装 `/etc/systemd/system/anas.slice`，让后台 AI 在争用时把 CPU 让给设备上的其他工作。

安装器最后核对 Host Agent 能否以登录用户身份启动文件 Worker：它的 `CapEff` 必须含 `CAP_SETUID`/`CAP_SETGID`，日志必须出现 `file broker identity switch verified`。不满足时以退出码 5 结束，此时新版本已切换但 Web 文件与终端不可用，按[回滚](#回滚)恢复上一版本；原因见 [issue #38 调查](../investigations/2026-10-08-host-agent-loses-setuid-under-systemd.md)。

全新实验机首次安装才使用发布目录中的交互向导；它会复核稳定磁盘身份、安装依赖、停止旧用户级预览服务、安装系统服务并逐步引导产品初始化：

```bash
source_release=/home/anas-dev/apps/a-nas/releases/<GIT_SHA>

SOURCE_RELEASE="$source_release" \
RELEASE_ID=<GIT_SHA> \
SMB_INTERFACE=enp2s0 \
EXPECTED_DISK_WWN=0x5000c500518d4994 \
EXPECTED_DISK_SERIAL=Z2AYDZPB \
  "$source_release/provision-v1.0.1-rc.sh"
```

若必须逐步排障，可不用向导而执行以下等价安装步骤；把示例值替换为本次 RC：

```bash
# 仅在仓库源可达时安装一次；若 apt 失败，停止，不进入格式化流程。
apt-get update
apt-get install --no-install-recommends acl btrfs-progs parted smartmontools samba libsqlite3-0 python3 libegl1 libgles2 ffmpeg heif-thumbnailer libheif-plugin-libde265

source_release=/home/anas-dev/apps/a-nas/releases/<GIT_SHA>
release_id=<GIT_SHA>
smb_interface=enp2s0

readlink -f /opt/a-nas/current
ip -brief link

"$source_release/install-v1.0.1-system-services.sh" \
  "$source_release" "$release_id" "$smb_interface"
```

影视中心的媒体 Worker 使用 Debian 的 FFmpeg（[ADR 0017](../adr/0017-run-the-media-center-through-the-file-broker.md)），安装器在缺少 `ffmpeg`、`ffprobe` 或 `heif-thumbnailer` 时以退出码 3 结束而不改变系统；已部署过的设备升级前以 root 执行一次 `apt-get install --no-install-recommends ffmpeg heif-thumbnailer libheif-plugin-libde265`。文件管理的 HEIC/HEIF 预览使用 libheif；编码工具不属于产品运行依赖。

完成标准：`/opt/a-nas/current` 指向 root 所有的目标 release；`anas-host-agent`、`anas-photos` 与 `anas-api` 均 active，`anas-media.socket` 已启用且 `/run/a-nas-media/media.sock` 为 `root:a-nas 0660`，安装器没有以退出码 5 结束；`/run/a-nas/host-agent.sock` 为 `root:a-nas 0660`；API 仅监听 `127.0.0.1:8080`；`/etc/chromium/policies/managed/a-nas.json` 为 root 所有并关闭密码管理器。在本地控制台只输入账号和密码完成设备启用；首个账号成为 A-NAS 产品管理员，不是 Linux root。

### 移入 Host Agent 的记录

Host Agent 的五个记录（`space-registry`、`identity-registry`、`viewing-grants`、`app-identities` 与 `volume-operations`，均为 `.json`）自 [issue #176](https://github.com/zhongwater123/A-NAS/issues/176) 起在 `/var/lib/a-nas-host`，Host Agent 启动时把旧版本留在 `/var/lib/a-nas` 的记录移过来，但只移动 root 所有、只有一个链接的普通文件。安装器在改动任何东西之前核对：某个记录在新位置还没有，旧位置的又不符合时，以退出码 3 停止并给出 `stat` 结果，否则重启后这个记录会停用（日志 `a Host Agent record cannot be used`）。产品服务的 `StateDirectory=a-nas` 在目录本身属主不对时会把整个目录递归改归 `a-nas`，这是记录不归 root 的可能原因。处理时先停服务，再逐个核对：

```bash
systemctl stop anas-api anas-host-agent
record=/var/lib/a-nas/app-identities.json   # 安装器报出的文件
stat -c '%U:%G %h %F %y' -- "$record"
cat -- "$record"
```

- 是普通文件、内容可以确认是 Host Agent 写的（与 `getent passwd`、空间目录和数据卷的 `.a-nas-identities.json` 对照）时，换成 root 的单链接副本：`install -o root -g root -m 0600 -- "$record" "$record.new" && mv -f -- "$record.new" "$record"`。
- 符号链接、硬链接，或无法确认的内容：移出目录留作证据，例如 `mv -- "$record" /root/`。Host Agent 对这个记录从空开始，后果见[回滚](#回滚)中对空记录的说明。

然后重新运行安装器。

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

在尚未执行格式化计划时，可以回到上一个已安装的 release。只切换 `/opt/a-nas/current` 不会恢复 `/etc/systemd/system` 中的单元，而安装器拒绝重复安装已存在的 release（退出码 4），所以单元要从上一 release 的暂存目录恢复：

```bash
previous=<上一 release 的 12 位提交 SHA>
stage=/home/anas-dev/apps/a-nas/releases/$previous
test -d "/opt/a-nas/releases/$previous"
for unit in anas-host-agent anas-api anas-photos; do
  install -o root -g root -m 0644 "$stage/$unit-system.service" "/etc/systemd/system/$unit.service"
done
if [[ -f "$stage/anas-ai-system.service" ]]; then
  install -o root -g root -m 0644 "$stage/anas-ai-system.service" /etc/systemd/system/anas-ai.service
  grep -Fq -- --detector /etc/systemd/system/anas-ai.service ||
    rm -f /etc/systemd/system/anas-ai.service.d/cpu-reserve.conf
  systemctl stop anas-ai.service
fi
# Releases before issue #176 read the Host Agent's records from /var/lib/a-nas.
if ! grep -Fqx 'StateDirectory=a-nas-host' "$stage/anas-host-agent-system.service"; then
  systemctl stop anas-host-agent.service
  for record in space-registry identity-registry viewing-grants app-identities volume-operations; do
    if [[ -f "/var/lib/a-nas-host/$record.json" ]]; then
      mv -f -- "/var/lib/a-nas-host/$record.json" "/var/lib/a-nas/$record.json"
    fi
  done
fi
# The local console's unit and PAM stack come from the same release: before
# issue #174 they start the launcher in anas-dev's current link as anas-dev.
install -o root -g root -m 0644 "$stage/anas-kiosk@.service" /etc/systemd/system/anas-kiosk@.service
install -o root -g root -m 0644 "$stage/a-nas-kiosk.pam" /etc/pam.d/a-nas-kiosk
ln -sfn -- "/opt/a-nas/releases/$previous" /opt/a-nas/current
runuser -u anas-dev -- ln -sfn "$stage" /home/anas-dev/apps/a-nas/current.next
runuser -u anas-dev -- mv -Tf /home/anas-dev/apps/a-nas/current.next /home/anas-dev/apps/a-nas/current
systemctl daemon-reload
systemctl restart anas-host-agent anas-photos anas-api anas-kiosk@tty1
```

回滚到 issue #174 之前的 release 时，kiosk 回到 `anas-dev`，Chromium 用的是该账号下的旧配置文件：迁移后删掉了它的话，要在本机屏幕上重新登录。

AI Worker 的单元写明 release 的模型参数，旧 Worker 不认识新单元的参数（例如 `--detector`），所以带 AI 的 release 要一并恢复 `anas-ai.service`，并停止正在运行的 Worker，下一次连接启动旧 release 的。物体检测器之前的 release 在单元里自带 CPU 上限，没有安装器写的 `anas-ai.service.d/cpu-reserve.conf`，恢复后删掉它（[启用相册物体检测](enable-photo-objects.md#回滚或恢复)）。旧版本也拒绝打开比它新的相册 Catalog：回滚跨过 Catalog 迁移时，先按该版本的手册把 Catalog 退回，例如物体检测的 migration 7。

Host Agent 的记录（空间、身份、查看授权、应用身份与数据卷操作）在 `/var/lib/a-nas-host`，只有 root 能打开（[issue #176](https://github.com/zhongwater123/A-NAS/issues/176)）。新版本启动时把旧版本留在 `/var/lib/a-nas` 的记录移过来：先写入并刷盘新副本，再删除原件；不移动符号链接、硬链接或不归 root 所有的文件，安装器遇到它们会先停下（见[移入 Host Agent 的记录](#移入-host-agent-的记录)）。旧版本只在 `/var/lib/a-nas` 读这些记录，所以回滚到它之前时，上面的脚本先停止 Host Agent，再把记录移回去。漏掉这一步时，旧二进制以空记录启动：不修复任何空间的目录与 ACL，快照与管理员查看找不到空间，应用身份从空表分配、与已有账号冲突而报错，新版本发出的查看授权到期也不撤销（再次升级后由新版本撤销）；它写下的新记录留在 `/var/lib/a-nas`。再次升级时，新版本只用 `/var/lib/a-nas-host` 中已有的记录，删除与之相同的旧副本，并在日志中报告内容不同的旧记录（`differs from the one in use`），由管理员核对。按脚本移回的记录会在再次升级时重新移入。

`/etc/a-nas/*.env` 由安装器按固定内容生成，旧版本忽略新增的变量，不需要恢复。回滚也不改变屏保视频池：环境文件仍把独立于 release 的 `/var/lib/a-nas/screensavers/current` 交给产品服务；回滚媒体按[本地控制台运行手册](operate-local-kiosk.md#回滚)。另外：

- 不支持回滚到 ADR 0008 之前（rc.5 及更早）：旧账号已删除重建，旧版本的启动修复还会重新授予 `a-nas` 访问空间的 ACL。
- 回滚到 ADR 0011 之前的版本时，先 `systemctl disable --now anas-photos.service`：旧二进制没有 `photo-service` 子命令。

执行格式化后，回滚程序不会恢复旧分区或签名；保留磁盘现状并按 `needs_attention` 调查。绝不对不确定的 `/dev/sdX` 运行手工擦除命令。

## 开发机暂存注意事项

- 先按[测试与验证](../development/testing.md)验证候选提交，再用 `scripts/deploy-dev.ps1 -NasHost a-nas-dev -StageOnly -WslDistribution Ubuntu -WslUser <WSL 用户>` 暂存（开发机 B）；开发机 A 使用脚本默认的 WSL 发行版与用户。SSH 配置见[连接手册](bootstrap-experimental-nas-ssh.md)。
- `deploy-dev.ps1` 仅在 `build/.validated-build` 与提交、产品版本和二进制哈希一致时复用门禁结果；该清单由 Makefile 的 `check` 目标生成。无 Git 的 ext4 副本运行 `check-steps` 只提供测试证据，不生成部署清单。
- 屏保视频不随 release 暂存，更换时使用[本地控制台手册](operate-local-kiosk.md#更换屏保视频)中的独立上传流程。早于 [ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md) 的旧安装器分支应先合入主干，再部署。
- 在 PowerShell 5.1 中运行 `deploy-dev.ps1` 不要加 `2>&1`：Windows OpenSSH 的 `close - IO is still pending` 会变成错误记录，使脚本在 `ErrorActionPreference = 'Stop'` 下中止。

## 关联

- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [存储与文件架构](../architecture/storage-and-files.md)
- [`scripts/install-v1.0.1-system-services.sh`](../../scripts/install-v1.0.1-system-services.sh)
- [`scripts/provision-v1.0.1-rc.sh`](../../scripts/provision-v1.0.1-rc.sh)
