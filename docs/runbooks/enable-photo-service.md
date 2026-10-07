# 启用相册服务

状态：draft（未在 Experimental NAS 执行）
更新时间：2026-10-08

## 目的

在已运行统一身份（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）的 Experimental NAS 上启用相册服务（[ADR 0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)）。成功后：

- `anas-photos.service` 以 `a-nas-photos`（UID/GID 31000）运行；
- 数据卷上出现只属于该身份的 `photos` 子卷；
- 桌面“相册”不再显示“此设备暂未启用相册”。

## 前提与风险

- **权限**：管理员 root 会话。暂存制品使用日常账号 `anas-dev`。
- **目标**：
  - 主机名 `a-nas-dev`，使用前按 [SSH 运行手册](bootstrap-experimental-nas-ssh.md) 核对指纹。
  - 数据卷 UUID 与 [当前状态](../status/CURRENT.md) 记录一致，并已挂载到 `/srv/a-nas/data`。
- **前提**：设备已按 [实机配置手册](provision-v1.0.1-experimental-storage.md#升级到统一身份adr-0008) 升级到统一身份，账号 UID 在 20100–29999。
- **真实磁盘写入**：
  - Host Agent 在数据卷上创建 `photos` Btrfs 子卷，并为 `/srv/a-nas` 增加一条 `user:a-nas-photos:--x` ACL。
  - 它不分区、不格式化，也不改动任何空间。
- **风险**：
  - 产品服务单元新增 `SupplementaryGroups=a-nas-photos`，该组缺失时 `anas-api` 无法启动。
  - 实验期只上传可丢弃的测试照片；单盘 Btrfs 不是备份。
- **停止条件**（出现任一项立即停止）：
  - 主机或指纹不符，或数据卷 UUID、挂载与记录不一致。
  - UID 或 GID 31000 已被其他账号或组占用。
  - 安装器以退出码 3 结束。
  - `anas-photos` 无法以 `a-nas-photos` 启动。
  - `photos` 不是 `a-nas-photos 0700`，或产品服务账号 `a-nas` 能列出它。

## 步骤

1. 在开发机对候选提交完整运行门禁和[相册服务冒烟](../development/LOCAL_ENVIRONMENT.md#root-集成测试)，并只暂存、不激活：`scripts/deploy-dev.ps1 -StageOnly`。
   - 预期：输出 API 与 Host Agent 的 SHA-256。发布目录包含 `anas-photos-system.service` 与新的安装器。
   - 完成标准：在 NAS 上用 `sha256sum --check` 核对两个二进制，与门禁输出一致。
2. 核对固定身份未被占用。
   - 命令：`getent passwd 31000; getent group 31000; getent passwd a-nas-photos; getent group a-nas-photos`
   - 预期：四条均无输出；或者只出现 `a-nas-photos`，且 UID 与 GID 都是 31000。
   - 完成标准：没有其他名称占用 31000。否则停止，不得修改既有账号。
3. 核对数据卷与网卡。
   - 命令：`findmnt -T /srv/a-nas/data -o TARGET,SOURCE,FSTYPE,OPTIONS`、`ip -brief link show dev "$smb_interface"`
   - 完成标准：挂载为 `btrfs`，来源与记录的分区一致，网卡存在。
4. 运行系统服务安装器（参数与 [实机配置手册](provision-v1.0.1-experimental-storage.md#安装系统服务) 相同）。
   - 命令：`"$source_release/install-v1.0.1-system-services.sh" "$source_release" "$release_id" "$smb_interface"`
   - 预期：安装器依次完成以下动作：
     - 创建 `a-nas-photos`；
     - 安装 `anas-photos.service`；
     - 写入 `ANAS_PHOTOS_SOCKET`、`ANAS_PHOTO_SESSION_SOCKET` 与 `ANAS_PHOTO_SESSION_GROUP`；
     - 依次重启 Host Agent、相册服务和产品服务。
   - 预期：Debian 的 `useradd` 会提示 `uid 31000 is greater than SYS_UID_MAX`。该 UID 是有意固定的，此提示不影响安装。
   - 完成标准：`systemctl is-active anas-host-agent anas-photos anas-api` 全为 `active`。

## 验证

- **身份**：
  - `id a-nas-photos` 为 `uid=31000(a-nas-photos) gid=31000(a-nas-photos)`。
  - `id a-nas` 的组包含 `a-nas-photos`。
- **套接字**：`stat -c '%a %U:%G %n' /run/a-nas-photos /run/a-nas-photos/photos.sock /run/a-nas-sessions /run/a-nas-sessions/photos.sock` 依次输出：
  - `750 a-nas-photos:a-nas-photos`
  - `660 a-nas-photos:a-nas-photos`
  - `750 root:a-nas-photos`
  - `660 root:a-nas-photos`
- **存储**：
  - `btrfs subvolume show /srv/a-nas/data/photos` 成功。
  - `stat -c '%a %U:%G' /srv/a-nas/data/photos` 为 `700 a-nas-photos:a-nas-photos`。
  - `getfacl -p /srv/a-nas/data/photos` 只有 `user::rwx`、`group::---`、`other::---`。
  - `getfacl -p /srv/a-nas` 含 `user:a-nas-photos:--x`。
- **日志**：`journalctl -u anas-photos -b --no-pager` 出现 `photo service ready`，没有 `photo store unavailable`。
- **隔离**：
  - `setpriv --reuid=a-nas --regid=a-nas --init-groups ls /srv/a-nas/data/photos` 因权限失败。
  - `setpriv --reuid=a-nas-photos --regid=a-nas-photos --init-groups ls /srv/a-nas/data/spaces/shared` 因权限失败。
- **功能**：
  1. 管理员在本地控制台打开“相册”，上传一张测试 JPEG。约一分钟内缩略图出现，原图可以打开和下载。
  2. 另一成员登录后看不到这张私有照片。
  3. `systemctl restart anas-photos` 之后照片仍在。
  4. 后台日志没有 `photo media job failed`。
  5. 成员上传一张照片；管理员在“账号管理”中对该成员选择“查看私有图库”，填写原因并输入自己的密码。管理员在相册中看到“只读查看 · <成员>”，可以浏览和下载，但没有修改按钮。
  6. 成员下次登录看到“管理员……开启了对你私有图库的只读查看”通知；管理员点击“结束查看”后，该图库立即从列表消失。
  7. 文件管理中不会因此出现该成员的个人空间。
- **审计与对账**：`journalctl -u anas-photos` 中启动时的 `photo store reconciled` 报告只在异常停止后出现，正常重启后为空。

## 回滚或恢复

- **只停用相册**：`systemctl disable --now anas-photos.service`。产品服务的相册接口返回 `photos_unavailable`，其他功能不受影响。
- **回到上一版本**：按 [实机配置手册](provision-v1.0.1-experimental-storage.md#回滚) 恢复 `/opt/a-nas/current` 与旧单元。
  - 旧单元不引用 `a-nas-photos`，账号与组可以保留。
  - 若改为删除账号与组，必须先确认 `/etc/systemd/system/anas-api.service` 不再含 `SupplementaryGroups=a-nas-photos`，否则产品服务无法启动。
- **清除测试照片（不可逆）**：只在确认 `photos` 中只有可丢弃测试数据时执行。
  1. 停止相册服务。
  2. 执行 `btrfs subvolume delete /srv/a-nas/data/photos`。
  3. 账号仍存在时，Host Agent 会在下次启动时重建一个空的 `photos` 子卷。

## 关联

- 规格：[相册](../specs/photo-library.md)
- ADR：[0011 相册服务身份与 Catalog 授权](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[0008 统一身份](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)
- 架构：[相册技术设计](../architecture/photo-library.md)、[存储与文件](../architecture/storage-and-files.md)
- 代码：[`anas-photos.service`](../../deploy/systemd/system/anas-photos.service)、[安装器](../../scripts/install-v1.0.1-system-services.sh)、[`internal/photoservice`](../../internal/photoservice/photoservice.go)、[`internal/hostops/linux/photos.go`](../../internal/hostops/linux/photos.go)
- 测试：[root 集成测试](../../internal/hostops/linux/root_photos_integration_test.go)
- 调查：不涉及
