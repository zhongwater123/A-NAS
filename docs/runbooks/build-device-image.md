# 构建与安装设备镜像

状态：draft（2026-11-13 `make image-test` 在虚拟机中 23 项通过；尚未写到真机）
更新时间：2026-11-13

## 目的

家里的设备装整机镜像（[ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md) 决定 2，第 5 步）。本手册用于：

- 开发机：从一个 release 构建设备镜像与更新文件；
- 开发机：把镜像写到新设备的系统盘（[决定记录](../decisions/2026-11-13-device-image-defaults.md)：试点先用这种装法，USB 安装盘以后做）；
- 设备：取用推送来的更新文件。“设置 → 系统更新”接入镜像设备之前（第 5 步的第二个 PR），由 root 按下面的命令更新。

成功结果：新设备从系统盘启动，`a-nas-check` 全部 `PASS`，第一行写明产品版本与镜像版本。

## 镜像的组成

| 部分 | 在设备上 | 更新文件 | 何时发布 |
|---|---|---|---|
| 基础镜像：Debian 13、`a-nas-host` 与 `a-nas-console` 及其依赖 | 两个只读的 `/usr` 槽位（各 4 GiB）与 ESP 中各自的 UKI | `os/a-nas_<序号>.usr.raw.xz`、`os/a-nas_<序号>.efi` | 镜像中的软件包或配置变了时 |
| 产品：`a-nas` 软件包 | system extension，`/var/lib/extensions/a-nas-product.raw` 指向当前版本 | `product/a-nas-product_<序号>.raw` | 每个 release |
| 内容：模型、两个运行环境、Qdrant、llama.cpp，每种一个 | system extension，只合并 `/opt` | `content-<种类>/a-nas-content-<种类>_<序号>.raw` | release 固定了新的内容时 |

- **序号**是构建它的提交在 main 上的第一父提交数（`git rev-list --count --first-parent`），沿 main 只增不减；Debian 版本写在扩展的 `A_NAS_VERSION` 中。systemd-sysupdate 不接受 Debian 版本中的 `~`，换成别的字符时它的比较又把预发布排到正式版之后，所以更新文件不用 Debian 版本。镜像只从 main 上的提交构建。
- **持久的根分区**（`a-nas-state`）放 `/etc`、`/var`（含 `/var/lib/a-nas`）与各个扩展。第一次开机时它扩展到整块硬盘，切换 `/usr` 槽位时不动它。
- **启动**：systemd-boot 加载 UKI。新版本的 UKI 带三次启动机会。开机的健康检查是 `a-nas-check`，只在被计数的启动中运行，不通过就重启；三次都不通过，systemd-boot 回到上一版本。内核崩溃靠 `panic=10` 重启，内核卡死靠主板的硬件看门狗。
- **签名**：每个目录的 `SHA256SUMS` 由签软件源的同一把存档密钥签名（`scripts/apt-repository.sh sums`）。设备经 `file://` 读取推送来的目录，systemd-sysupdate 像对待下载一样核对签名与哈希，所以推送的人不在信任链上。
- **大小**（2026-11-13 的测试构建）：一次基础镜像更新约 744 MiB 的 `/usr` 加 161 MiB 的 UKI；产品 58 MiB；新设备的整盘镜像压缩后 1.1 GB，用的是替身内容，真实的模型与运行环境另加约 4 GB。构建一个镜像约 10 分钟，mkosi 每次都构建基础镜像，再比对它与上一个是否相同。

## 前提与风险

- 开发机：能运行 `make release`；有 Docker 与 `/dev/kvm`；有约 40 GB 的空间放构建（WSL 上放在 Windows 盘的 `.img` 文件中，WSL 自己的磁盘删文件后不会变小）。构建在特权容器中运行 Debian 13 的 mkosi（`scripts/image-builder.sh`）。
- 签名密钥：同 `make release`，正式发布用存档密钥（[管理发布签名密钥](manage-release-key.md)）；测试时 `ANAS_RELEASE_TEST_KEY=1`。
- 设备：x86-64，UEFI 启动，Secure Boot 关闭（[决定记录](../decisions/2026-11-13-device-image-defaults.md)）；系统盘至少 32 GB，数据卷在另外的硬盘上。
- **写系统盘会抹掉它上面的全部内容。** 只写确认过的那块盘；数据卷所在的硬盘不要接在写盘的机器上。
- 停止条件：
  - 目标盘的型号、序列号或大小与要写的那块不一致；
  - `make image` 或 `make image-test` 失败；
  - 新设备第一次开机后 `a-nas-check` 有 `FAIL` 且不是因为还没写 `site.conf`。

## 步骤

1. 开发机：在 main 上要发布的提交上运行 `make release`（[部署 A-NAS 软件包](deploy-a-nas-packages.md) 第 1 步），再构建镜像。`UPDATES` 是每次发布都沿用的更新文件目录；只有变了的部分会新增文件。
   ```bash
   make image REPO=/mnt/d/A-NAS-releases/apt UPDATES=/mnt/d/A-NAS-releases/updates WORK=/mnt/d/A-NAS-releases/image-work.img
   ```
   - 完成标准：最后一行写明 `image <序号> (<版本>): base …; product <序号>; …`。`UPDATES/install/a-nas_<序号>.raw.zst` 是新设备的整盘镜像。
2. 开发机：把整盘镜像写到新设备的系统盘。用 USB 硬盘盒或硬盘底座接上它，然后：
   - 识别目标：
     ```bash
     lsblk -o NAME,MODEL,SERIAL,SIZE,TRAN
     ```
     确认型号、序列号与大小就是要写的那块盘，记下它的设备名 `/dev/sdX`。不确定就停下。
   - 写入（会抹掉 `/dev/sdX` 上的全部内容）：
     ```bash
     zstd -dc /mnt/d/A-NAS-releases/updates/install/a-nas_<序号>.raw.zst | sudo dd of=/dev/sdX bs=4M conv=fsync status=progress
     ```
   - 完成标准：`sudo sfdisk -l /dev/sdX` 列出 `esp`、`a-nas_<序号>`、`_empty` 与 `a-nas-state` 四个分区。
3. 设备：装上系统盘与数据卷的硬盘，从系统盘启动。第一次开机时，根分区扩展到整块盘。以 root 登录后，写本机设置，再检查：
   ```bash
   editor /etc/a-nas/site.conf      # 至少写 ANAS_SMB_INTERFACE=<网卡>
   a-nas-site
   a-nas-check
   ```
   - 完成标准：`a-nas-check` 退出码为 0，第一行是 `PASS a-nas <版本> on the A-NAS image <序号>`。
4. 以后的版本：开发机构建后，把 `UPDATES` 中的 `os/`、`product/` 与 `content-*/` 原样推送到设备的 `/var/lib/a-nas-updates/`（同部署手册第 2 步的做法）。在“设置 → 系统更新”接入镜像设备之前，由 root 更新：
   - 产品（不重启）：先用正在运行的产品复制数据库，再换上新的产品，最后运行新产品的 `postinst`（它按顺序重启服务并运行 `a-nas-check`）：
     ```bash
     old=$(. /usr/lib/extension-release.d/extension-release.a-nas-product && echo "$A_NAS_VERSION")
     /usr/lib/a-nas/release/preinst upgrade "$old"
     /usr/lib/systemd/systemd-sysupdate --component=product update
     systemd-sysext refresh
     /usr/lib/a-nas/release/postinst configure "$old"
     ```
   - 新的内容随产品发布时，先更新内容，再更新产品：`/usr/lib/systemd/systemd-sysupdate components` 列出各种内容，`--component=content-<种类> update` 取用一种。
   - 基础镜像（要重启）：
     ```bash
     /usr/lib/systemd/systemd-sysupdate update
     systemctl reboot
     ```
     新版本开机通过健康检查后才算装好。`ls /efi/EFI/Linux` 中它的 UKI 去掉了 `+<剩余>-<已用>` 的计数。

## 回滚或恢复

- 基础镜像：新版本三次启动都没有通过健康检查时，systemd-boot 自动回到上一版本，不用操作。要手动回到上一版本，就删掉新版本的 UKI 再重启：`rm /efi/EFI/Linux/a-nas_<新序号>*.efi && systemctl reboot`。
- 产品：把链接指回上一版本，再刷新并运行它的 `postinst`；新版本改过的数据库结构，由上一版本启动时按新版本存下的反向步骤退回：
  ```bash
  old=$(. /usr/lib/extension-release.d/extension-release.a-nas-product && echo "$A_NAS_VERSION")
  /usr/lib/a-nas/release/preinst upgrade "$old"
  ln -sfn /var/lib/a-nas-sysext/a-nas-product_<上一序号>.raw /var/lib/extensions/a-nas-product.raw
  systemd-sysext refresh
  /usr/lib/a-nas/release/postinst configure "$old"
  ```
- 数据库要从副本恢复时，同部署手册“回滚或恢复”的第二条。
- 新设备装坏了：重新执行第 2 步。数据卷在另外的硬盘上，不受影响。

## 验证

- `make image-test`（`scripts/image-test.sh`）：构建四个镜像，把第一个写进虚拟机的磁盘，再像设备一样更新，检查：
  - 新设备：从写入的镜像启动，产品与各种内容已合并，根分区扩展到整块盘，`a-nas-check` 通过；
  - 产品更新不重启，数据库多出新版本的一步；回到上一版本，数据库退回；
  - 没变的内容与基础镜像不再取用；
  - 写基础镜像时断电，设备仍是原版本；新的基础镜像通过健康检查后被确认；检查不通过的基础镜像三次后回到上一版本；
  - `/etc` 与 `/var/lib/a-nas` 在每次更新后不变。

## 关联

- ADR：[0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)
- 决定记录：[设备镜像的更新来源、装机方式与 Secure Boot](../decisions/2026-11-13-device-image-defaults.md)
- 研究：[设备镜像与 systemd-sysupdate 的验证](../research/sysupdate-image-verification.md)
- 运行手册：[部署 A-NAS 软件包](deploy-a-nas-packages.md)、[管理发布签名密钥](manage-release-key.md)
- 规格：[系统更新](../specs/system-update.md)
- 代码：[镜像的 mkosi 配置](../../deploy/image/)、[`build-image.sh`](../../scripts/build-image.sh)、[构建容器](../../scripts/image-builder.sh)、[`image-test.sh`](../../scripts/image-test.sh)（[虚拟机部分](../../scripts/image-test-vm.sh)）、[`a-nas-check`](../../deploy/site/a-nas-check.sh)
