# 部署 A-NAS 软件包

状态：draft（2026-11-11 在系统测试的 Debian 13 容器中验证了安装、升级、回滚与再升级；尚未在 Experimental NAS 上执行）
更新时间：2026-11-11

## 目的

A-NAS 以签名软件源中的 Debian 软件包发布（[ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)）。本手册用于：

- 开发机：在一个提交上跑完门禁，把它的软件包放进软件源；
- 部署机：把软件源目录复制到 NAS；
- NAS 上的 root：用 apt 安装、升级、回滚，用 `a-nas-check` 检查；
- 由安装器装好的 NAS（Experimental NAS）第一次换成软件包。

部署机只复制文件，不需要仓库、WSL 或构建工具。apt 按软件源的签名核对每个文件，所以复制的人与暂存账号不在信任链上。

成功结果：`a-nas-check` 全部 `PASS`，`apt-cache policy a-nas` 显示装的是要部署的版本。

## 前提与风险

- 权限：
  - 开发机：能运行 `make check` 与系统测试（Docker、Go、WSL，见[本机开发环境](../development/LOCAL_ENVIRONMENT.md)）；
  - 部署机：能以 `anas-dev` 登录 NAS（[连接手册](bootstrap-experimental-nas-ssh.md)），有 `scp` 或 `rsync`；
  - NAS 上的 root。
- 目标：Experimental NAS（主机名 `a-nas-dev`），Debian 13，已装 `a-nas-host`（[ADR 0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)）。
- 签名密钥：现在是测试密钥（开发机上的 `~/.local/share/a-nas/release-test-key`）。正式密钥由用户决定（[发布用的签名密钥](../decisions/2026-11-11-release-signing-key.md)）；换密钥后，各台机器要重新做一次第 4 步。软件源放在哪里同样待定（[软件源与更新文件放在哪里](../decisions/2026-11-11-update-hosting.md)），现在由部署机复制。
- 空间：软件源保留每个版本，内容包约 4.4 GB，每个 release 的产品包约 45 MB；NAS 的系统盘要放得下软件源的副本、`/opt/a-nas` 中的内容与 apt 的缓存。开发机上 `make release` 在 `/var/tmp` 逐个组装内容包，最大的约 2.6 GiB，用完即删；2026-11-11 用全部真实内容第一次运行，用时 22 分钟，软件源 4.1 GB。
- 服务影响：升级与回滚各重启一次 Host Agent、产品服务、相册、分享与家庭服务，几秒内进行中的文件操作、终端、上传与相册整理会断开后重连。升级前 `preinst` 停下写数据库的服务，复制数据库。
- 停止条件：
  - 第 4 步的哈希与开发者给出的不一致；
  - apt 报告签名或哈希错误（`NO_PUBKEY`、`BADSIG`、`Hash Sum mismatch`）；
  - `a-nas` 的 `preinst` 报告账号或 ID 被占用；
  - 安装后 `a-nas-check` 有 `FAIL`：按“回滚或恢复”退回上一个版本。

## 步骤

1. 开发机：在要部署的提交上跑完门禁，再写进软件源。工作区必须干净；版本由提交与它的 release 标签得出（[`scripts/release-version.sh`](../../scripts/release-version.sh)）。
   ```bash
   make check
   make system-test
   make release REPO=/mnt/d/A-NAS-releases/apt CONTENT=/mnt/d/A-NAS-releases/content
   ```
   - `CONTENT` 是内容库：模型、两个 Python 运行环境、Qdrant、llama.cpp 与助手试用的 Hermes 运行环境，目录结构见 [`scripts/build-content-packages.sh`](../../scripts/build-content-packages.sh)。软件源里已有的内容包不再构建。
   - 预期：最后三行是 `A-NAS <版本> (<提交>) is in …`、签名密钥的指纹与 `a-nas-archive-keyring` 的 SHA-256。
   - 完成标准：把版本、指纹与哈希交给部署的人。打了 release 标签的提交进 `release` 与 `dev`，其他提交只进 `dev`。
2. 部署机：把软件源目录原样复制到 NAS 的暂存账号下，例如：
   ```bash
   rsync -a --delete <软件源目录>/ anas-dev@a-nas-dev:apps/a-nas/repository/
   ```
   - 没有 `rsync` 时可用 `scp -r`，或任何能保留目录结构的方式。
   - 完成标准：NAS 上有 `/home/anas-dev/apps/a-nas/repository/dists` 与 `pool`。
3. NAS（root）：把软件源复制到 root 的目录。apt 以 `_apt` 账号读取，暂存账号的主目录它读不到。
   ```bash
   install -d -m 0755 /var/local/a-nas-repository
   cp -r --remove-destination /home/anas-dev/apps/a-nas/repository/. /var/local/a-nas-repository/
   ```
   - 完成标准：`ls /var/local/a-nas-repository/dists` 列出 `dev`（打了标签的版本还有 `release`）。
4. NAS（root），只在第一次或换了签名密钥后：核对并安装签名公钥，再加上软件源。
   ```bash
   keyring=$(ls /var/local/a-nas-repository/pool/main/a/a-nas-archive-keyring/a-nas-archive-keyring_<版本>_all.deb)
   sha256sum "$keyring"
   dpkg -i "$keyring"
   sed -e 's|^Suites: .*|Suites: release dev|' /usr/share/doc/a-nas-archive-keyring/a-nas.sources > /etc/apt/sources.list.d/a-nas.sources
   ```
   - 预期：SHA-256 与第 1 步给出的一致，一致才 `dpkg -i`。测试机跟随 `release` 与 `dev`，家里的设备只跟随 `release`。
   - 完成标准：`apt-get update` 没有 A-NAS 软件源的错误。
5. NAS（root），只在新机器上：写本机设置。已由安装器装好的机器跳过这一步，第一次装包时从安装器的环境文件中取出这些值（见“由安装器装好的 NAS 第一次换成软件包”）。
   ```bash
   install -d -m 0750 /etc/a-nas
   editor /etc/a-nas/site.conf
   ```
   - 至少写 `ANAS_SMB_INTERFACE=<网卡>`。可写的键与说明见 `/usr/share/a-nas/site.conf.example`（装包后才有，内容同 [`deploy/site/site.conf`](../../deploy/site/site.conf)）。
6. NAS（root）：安装或升级。产品的几个包写同一个版本；装了本地屏幕的加 `a-nas-console`，装了助手试用的加 `a-nas-assistant-trial`。
   ```bash
   apt-get update
   apt-get install --no-install-recommends a-nas=<版本> a-nas-host=<版本>
   ```
   - apt 按 `a-nas` 的依赖装上这一版的内容包；内容没变的不下载。`a-nas` 依赖同一版本的 `a-nas-host`。
   - 预期：`preinst` 输出数据库副本的位置（升级时），`postinst` 最后运行 `a-nas-check`，全部 `PASS`。
   - 完成标准：`a-nas-check` 退出码为 0。
7. NAS（root）：本机设置改了之后（`site.conf`、`internet.env`），不用重装，运行：
   ```bash
   a-nas-site
   ```
   - 它重新生成 systemd 的配置，重启 Host Agent 与产品服务，开关互联网入口，最后运行 `a-nas-check`。

## 由安装器装好的 NAS 第一次换成软件包

Experimental NAS 现在由安装器（`install-v1.0.1-system-services.sh`）装好：程序在 `/opt/a-nas/releases/<ID>`，`/opt/a-nas/current` 指向它，单元在 `/etc/systemd/system`。第一次按第 4、6 步装包时，`a-nas` 自动接管：

- `preinst` 停下写数据库的服务，把数据库复制到 `/var/backups/a-nas/<时间>-installer-<ID>/`；
- `postinst` 从 `/etc/a-nas/host-agent.env`、`anas-api.env` 与 `kiosk.env` 取出 `ANAS_SMB_INTERFACE`、`ANAS_TERMINAL`、`ANAS_CONTAINERS_MODE` 与本地屏幕的设置，写成 `/etc/a-nas/site.conf`；
- 把安装器写的单元、附加配置、sysusers、tmpfiles、传感器模块与环境文件移到 `/var/backups/a-nas/installer-layout/`，删除 `/opt/a-nas/current`；本地控制台与容器代理原来开着的，从软件包的单元重新开启；
- 模型、运行环境、Qdrant 与 llama.cpp 留在 `/opt/a-nas` 的原处，由内容包接管；`/opt/a-nas/releases` 保留。

装好后核对：

```bash
cat /etc/a-nas/site.conf
systemctl is-enabled anas-kiosk@tty1.service anas-container-agent.service
a-nas-check
```

- `site.conf` 有 `ANAS_SMB_INTERFACE=enp2s0`，开着终端与应用中心的有对应的行，本地屏幕的三项在；
- 本地控制台与容器代理为 `enabled`；
- `a-nas-check` 全部 `PASS`。

确认无误、并至少正常重启一次后，再删除安装器留下的文件与暂存账号的旧 release：

```bash
rm -rf /opt/a-nas/releases /var/backups/a-nas/installer-layout /home/anas-dev/apps/a-nas/releases
```

## 验证

- `a-nas-check`：全部 `PASS`，退出码为 0。
- `apt-cache policy a-nas a-nas-host`：`Installed` 为部署的版本。
- `dpkg --verify a-nas a-nas-host`：没有输出，即每个文件都与软件包相同。
- 正常重启一次后再运行 `a-nas-check`：开机时 `a-nas-generator` 与 `anas-site.service` 重新生成本机的配置，结果应相同。

## 回滚或恢复

- 退回上一个版本：同一条命令写旧版本号，加 `--allow-downgrades`；之后运行 `a-nas-check`。
  ```bash
  apt-get install --no-install-recommends --allow-downgrades a-nas=<旧版本> a-nas-host=<旧版本>
  ```
  - 新版本改过的数据库结构，由旧版本启动时按新版本存下的反向步骤退回（[`internal/sqlitedb/migrate.go`](../../internal/sqlitedb/migrate.go)）。
- 旧版本启动时报告 `the newer build left no reverse step`：从升级前的副本恢复。
  ```bash
  systemctl stop anas-api anas-photos anas-shares anas-family
  ls /var/backups/a-nas
  cp -a /var/backups/a-nas/<时间>-<旧版本>/system-disk/. /var/lib/a-nas/
  cp -a /var/backups/a-nas/<时间>-<旧版本>/photos/. /srv/a-nas/data/photos/
  cp -a /var/backups/a-nas/<时间>-<旧版本>/shares/. /srv/a-nas/data/shares/
  cp -a /var/backups/a-nas/<时间>-<旧版本>/family/. /srv/a-nas/data/family/
  systemctl start anas-photos anas-shares anas-family anas-api
  ```
  副本是升级那一刻的数据库，之后的改动会丢失。`/var/backups/a-nas` 保留最近两份。
- 第一次换成软件包后要回到安装器：
  ```bash
  apt-get remove a-nas
  cp -a /var/backups/a-nas/installer-layout/. /
  ln -sfn /opt/a-nas/releases/<ID> /opt/a-nas/current
  systemctl daemon-reload
  systemctl enable --now anas-ai.socket anas-ffmpeg.socket anas-vectors-proxy@a-nas-photos.socket
  systemctl enable --now anas-host-agent anas-photos anas-api anas-shares anas-family anas-search
  ```
  `apt-get remove` 保留数据库、`/etc/a-nas` 与内容包，安装器的 release 仍能用到 `/opt/a-nas` 中的内容。

## 关联

- ADR：[0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)、[0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)
- 决定记录：[发布用的签名密钥](../decisions/2026-11-11-release-signing-key.md)、[软件源与更新文件放在哪里](../decisions/2026-11-11-update-hosting.md)
- 代码：[`make release`](../../scripts/release.sh)、[软件源](../../scripts/apt-repository.sh)、[软件包](../../scripts/build-packages.sh)、[内容包](../../scripts/build-content-packages.sh)、[`a-nas` 的维护脚本](../../deploy/debian/a-nas/DEBIAN/postinst)、[`a-nas-check`](../../deploy/site/a-nas-check.sh)、[`a-nas-site`](../../deploy/site/a-nas-site.sh)、[系统测试](../../scripts/system-test.sh)
- 研究：[设备镜像与 systemd-sysupdate 的验证](../research/sysupdate-image-verification.md)（软件包布局的约束）
