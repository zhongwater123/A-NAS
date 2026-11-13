# 部署 A-NAS 软件包

状态：draft（2026-11-11 在系统测试的 Debian 13 容器中验证了安装、升级、回滚与再升级，2026-11-12 起包括从软件源装上 Docker 与 Caddy；尚未在 Experimental NAS 上执行）
更新时间：2026-11-13

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
- 目标：Experimental NAS（主机名 `a-nas-dev`）或新机器。Debian 13，安装时启用 `non-free-firmware` 组件，安装程序按硬件装上固件与微码；能访问 Debian 的镜像源。`a-nas-host`（[ADR 0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)）依赖的 Debian 包从镜像源装，Docker、Caddy 与产品从 A-NAS 软件源装（[ADR 0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)）。
- 签名密钥：A-NAS 存档密钥，开发机 B 与开发机 A 各用自己的子密钥签名（[管理发布签名密钥](manage-release-key.md)）。测试密钥只签开发机自己的测试软件源；换了密钥的软件源，各台机器要重新做一次第 4 步。近期以局域网推送发布（[决定记录](../decisions/2026-11-13-release-by-lan-push.md)）：开发机 B 构建并签名，软件源目录经开发机 A 或开发机 B 推送到 NAS 的暂存账号（第 2 步）。GitHub 的 `zhongwater123/A-NAS-dist` 是备选，平时不发布。
- 空间：软件源保留每个版本，内容包约 4.4 GB，每个 release 的产品包约 45 MB，Docker 与 Caddy 每更新一次约 92 MB；NAS 的系统盘要放得下软件源的副本、`/opt/a-nas` 中的内容与 apt 的缓存。开发机上 `make release` 在 `/var/tmp` 逐个组装内容包，最大的约 2.6 GiB，用完即删；2026-11-11 用全部真实内容第一次运行，用时 22 分钟，软件源 4.1 GB。
- 服务影响：升级与回滚各重启一次 Host Agent、产品服务、相册、分享与家庭服务，几秒内进行中的文件操作、终端、上传与相册整理会断开后重连。升级前 `preinst` 停下写数据库的服务，复制数据库：系统盘的复制到 `/var/backups/a-nas`，数据卷上的（相册、分享与家庭的数据库）留在数据卷的 `.a-nas-backups`，与原数据库共用数据块，几乎不占空间。正在运行的容器代理不重启，要换上新程序见[容器代理手册](install-container-agent.md)。
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
   - `CONTENT` 是内容库：模型、两个 Python 运行环境、Qdrant、llama.cpp 与助手试用的 Hermes 运行环境，目录结构见 [`scripts/build-content-packages.sh`](../../scripts/build-content-packages.sh)。软件源里已有的内容包不再构建。`make release` 还把 [`packages.json`](../../deploy/upstream/packages.json) 固定的 Docker 与 Caddy 软件包取到 `CONTENT/upstream`，按 SHA-256 核对；已有的不再下载。
   - 内容库缺运行环境时，从私有仓库 [A-NAS-assets](https://github.com/zhongwater123/A-NAS-assets) 的“运行环境与源码包”中取，每个文件一个 Release，标签为 `sha256-<文件的 SHA-256>`，说明里有拉取命令。助手试用的运行环境例如：
     ```bash
     sha=$(jq -r .sha256 ai/assistant-spike/runtime.json)
     gh release download "sha256-$sha" --repo zhongwater123/A-NAS-assets --dir "<内容库>/assistant-runtimes/$sha"
     ```
     - 助手试用的 Hermes 运行环境重新构建会改变哈希，只能取 [`runtime.json`](../../ai/assistant-spike/runtime.json) 固定的这一份；重新构建并改了固定值后，新的那份也照此上传。
     - AI Worker 与网页搜索的运行环境以 `ai-runtime-<ID>`、`search-runtime-<ID>` 为标题，放进 `ai-runtimes/`、`search-runtimes/`；`make release` 缺包时报出的路径里有当前的 ID。没有这个 ID 的 Release 时用 `scripts/build-ai-runtime.sh`、`scripts/build-search-runtime.sh` 构建。2026-11-13 存有 `842b388a0f1987f5` 与 `2f4816f6ce334c0c`。
     - SearXNG 源码包也存了一份（`searxng-1cd13d98d`），因为 GitHub 为提交生成的压缩包不保证逐字节不变。`scripts/build-search-runtime.sh` 下载到的哈希与 [`searxng.json`](../../deploy/search/searxng.json) 对不上时，把这份放到 `build/search-source/<SHA-256>/searxng.tar.gz` 再运行。
   - `make release` 要存档密钥的口令（[管理发布签名密钥](manage-release-key.md)第 6 步）。
   - 预期：最后三行是 `A-NAS <版本> (<提交>) is in …`、签名密钥的指纹（主密钥与本机的子密钥）与 `a-nas-archive-keyring` 的 SHA-256。
   - 完成标准：把版本、指纹与哈希交给部署的人。打了 release 标签的提交进 `release` 与 `dev`，其他提交只进 `dev`。
   - 备选，发布到 GitHub（需要 `gh` 已登录并能写 `A-NAS-dist`；平时不做）：
     ```bash
     make publish REPO=/mnt/d/A-NAS-releases/apt CHANNEL=dev
     make publish REPO=/mnt/d/A-NAS-releases/apt CHANNEL=release   # 只在打了 release 标签时
     ```
     它把这个渠道生成为 apt 的平铺软件源（软件包以 SHA-256 命名，索引由存档密钥签名），先传新的软件包，再传索引，最后删去不再需要的；`dev` 只留最近 20 个版本。要口令。第一次上传全部模型约 4.2 GB。
     开发机的 `gh` 装在 Windows 而不在 WSL 中时，`make publish` 只生成平铺的目录，并打印要在 Git Bash 中运行的上传命令。
     完成标准：最后一行打印 `https://github.com/zhongwater123/A-NAS-dist/releases/download/apt-<渠道>/`。
     2026-11-12 用测试密钥签的一个小软件源验证过：上传到发布 `apt-probe` 后，容器中的 apt 经 GitHub 的跳转核对签名、读取索引并装上了软件包，之后删除了这个发布。
2. 部署机（开发机 B 能连到 NAS 时就是它，否则由与 NAS 同网的开发机 A 中转）：把软件源目录原样复制到 NAS 的暂存账号下。开发机 B 上用 `scripts/deploy-dev.ps1 -NasHost <NAS>` 只传新增与改过的文件；开发机 A 先从开发机 B 取得软件源目录（共享目录、`rsync` 或 U 盘均可，签名在目录里），再复制，例如：
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
4. NAS（root），只在第一次或换了签名密钥后：核对并安装签名公钥，再加上软件源。备选，从 GitHub 装的机器，先下载 keyring 包：在 `https://github.com/zhongwater123/A-NAS-dist/releases/tag/apt-release` 的 `Packages` 中找到 `a-nas-archive-keyring` 的 `Filename`（`<SHA-256>.deb`），用 `wget` 取下后照下面核对，再用 `a-nas-github.sources` 代替 `a-nas.sources`：`cp /usr/share/doc/a-nas-archive-keyring/a-nas-github.sources /etc/apt/sources.list.d/a-nas.sources`（家里的设备删去其中 `apt-dev` 一段）。
   ```bash
   keyring=$(ls /var/local/a-nas-repository/pool/main/a/a-nas-archive-keyring/a-nas-archive-keyring_<版本>_all.deb)
   sha256sum "$keyring"
   dpkg -i "$keyring"
   sed -e 's|^Suites: .*|Suites: release dev|' /usr/share/doc/a-nas-archive-keyring/a-nas.sources > /etc/apt/sources.list.d/a-nas.sources
   ```
   - 预期：SHA-256 与第 1 步给出的一致，一致才 `dpkg -i`；`gpg --show-keys /usr/share/keyrings/a-nas-archive-keyring.pgp` 列出的主密钥指纹与[决定记录](../decisions/2026-11-11-release-signing-key.md)中的一致。测试机跟随 `release` 与 `dev`，家里的设备只跟随 `release`。
   - 以前从上游源装过 Caddy 或 Docker 的机器（Experimental NAS）：删除这些源与它们的公钥，之后 Docker 与 Caddy 只从 A-NAS 软件源更新。
     ```bash
     rm -f /etc/apt/sources.list.d/caddy-stable.list /etc/apt/preferences.d/caddy /usr/share/keyrings/caddy-stable-archive-keyring.gpg \
       /etc/apt/sources.list.d/docker.sources /etc/apt/keyrings/docker.asc
     ```
   - 完成标准：`apt-get update` 没有 A-NAS 软件源的错误；`ls /etc/apt/sources.list.d` 只剩 Debian 的源与 `a-nas.sources`。
5. NAS（root），只在新机器上：写本机设置。已由安装器装好的机器跳过这一步，第一次装包时从安装器的环境文件中取出这些值（见“由安装器装好的 NAS 第一次换成软件包”）。
   ```bash
   install -d -m 0750 /etc/a-nas
   editor /etc/a-nas/site.conf
   ```
   - 至少写 `ANAS_SMB_INTERFACE=<网卡>`。可写的键与说明见 `/usr/share/a-nas/site.conf.example`（装包后才有，内容同 [`deploy/site/site.conf`](../../deploy/site/site.conf)）。
   - 由开发机推送新版本的机器另写 `ANAS_UPDATE_STAGING=/home/anas-dev/apps/a-nas/repository`（第 2 步的暂存目录）：之后的版本由“设置 → 系统更新”取用推送、安装，见下面“以后的版本”。已装好的机器随时可以加上这一行，系统更新每次运行时读取，不用运行 `a-nas-site`。
6. NAS（root）：安装或升级。产品的几个包写同一个版本；装了本地屏幕的加 `a-nas-console`，装了助手试用的加 `a-nas-assistant-trial`。
   ```bash
   apt-get update
   apt-get install --no-install-recommends -o Dpkg::Options::=--force-confold a-nas=<版本> a-nas-host=<版本> a-nas-docker-policy
   ```
   - apt 按 `a-nas` 的依赖装上这一版的内容包；内容没变的不下载。`a-nas` 依赖同一版本的 `a-nas-host`；`a-nas-console` 只在命令写出时才变，装着却不是这一版时 `a-nas-check` 报告 `FAIL`。
   - `a-nas-host` 带上本版本或更新的 `a-nas-archive-keyring`：存档密钥增加或吊销了子密钥，设备随这次升级收到。
   - 显式写出 `a-nas-docker-policy`，使其保留为手动安装的宿主机保护。它不与产品一起降级；即使回滚到尚不依赖它的历史版本，`apt autoremove` 也不会删掉它。不要为回滚而卸载此包。
   - `a-nas-host` 带上 Caddy、Docker、WireGuard 与 nftables，Caddy 与 Docker 不低于这一版固定的版本，低了 apt 就升级它们；Docker 升级时它与全部容器重启。Caddy 装上即提供局域网入口（[启用局域网 Web 访问](enable-lan-web-access.md)）；Docker 在地址池写好之前不启动，应用中心按[容器代理手册](install-container-agent.md)打开。`a-nas-check` 的 `INFO` 行列出这几项的状态。
   - `--force-confold` 保留 A-NAS 与管理员改过的配置文件，例如 `/etc/caddy/Caddyfile`；没有它时，上游包改了自带的配置文件，apt 会停下来询问。
   - 预期：`preinst` 输出数据库副本的位置（升级时），`postinst` 最后运行 `a-nas-check`，全部 `PASS`。
   - 完成标准：`a-nas-check` 退出码为 0。
7. NAS（root）：本机设置改了之后（`site.conf`、`internet.env`），不用重装，运行：
   ```bash
   a-nas-site
   ```
   - 它重新生成 systemd 的配置，重启 Host Agent 与产品服务，开关互联网入口，最后运行 `a-nas-check`。

## 以后的版本：设置 → 系统更新

装上带系统更新的版本（[规格](../specs/system-update.md)）、`site.conf` 写了 `ANAS_UPDATE_STAGING` 之后，以后的版本只做第 1、2 步：

- 更新服务约每小时把暂存目录中的软件源同步到 `/var/local/a-nas-repository`，代替第 3 步；管理员也可以在页面上点“检查更新”立即同步。
- 管理员在“设置 → 系统更新”中点“安装”，代替第 6 步：更新服务执行同一条 apt 命令、运行 `a-nas-check`，不通过时按“回滚或恢复”的第一条命令退回原版本；“回到上一版本”也是那条命令。
- 结果在页面的记录与“日志 → 活动记录”的“系统”类别中；更新服务自己的输出在 `journalctl -u anas-update`，状态在 `/var/lib/a-nas-update/state.json`。
- 手工的 apt 命令仍然可用，是更新服务出问题时的退路。页面显示“正在安装”时不要手工运行 apt：更新服务的 apt 会等手工的 apt 结束，手工的 apt 却会因锁而失败。
- 页面显示“回到原版本时出错”时，按“回滚或恢复”手工处理。

## 由安装器装好的 NAS 第一次换成软件包

Experimental NAS 现在由安装器（`install-v1.0.1-system-services.sh`）装好：程序在 `/opt/a-nas/releases/<ID>`，`/opt/a-nas/current` 指向它，单元在 `/etc/systemd/system`。第一次按第 4、6 步装包时，`a-nas` 自动接管：

- `preinst` 停下写数据库的服务，把数据库复制到 `/var/backups/a-nas/<时间>-installer-<ID>/`（数据卷上的在 `/srv/a-nas/data/.a-nas-backups/` 的同名目录）；
- `postinst` 从 `/etc/a-nas/host-agent.env`、`anas-api.env` 与 `kiosk.env` 取出 `ANAS_SMB_INTERFACE`、`ANAS_TERMINAL`、`ANAS_CONTAINERS_MODE` 与本地屏幕的设置，写成 `/etc/a-nas/site.conf`；
- 把安装器写的单元、附加配置、sysusers、tmpfiles、传感器模块与环境文件移到 `/var/backups/a-nas/installer-layout/`，删除 `/opt/a-nas/current`；本地控制台与容器代理原来开着的，从软件包的单元重新开启；安装器开启的向量索引 socket 实例与互联网入口的隧道（`wg-quick@wg-anas`）的链接从 `/etc` 删除，改由软件包的链接与 `a-nas-generator` 决定；
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

第 4 步删掉了 Caddy 与 Docker 的上游源，第 6 步按这一版固定的版本升级它们：装着的低于固定的版本时，Docker 与全部容器重启一次。

确认无误、并至少正常重启一次后，再删除安装器留下的文件与暂存账号的旧 release：

```bash
rm -rf /opt/a-nas/releases /var/backups/a-nas/installer-layout /home/anas-dev/apps/a-nas/releases
```

## 更新 Docker 与 Caddy

A-NAS 软件源中的 Docker 与 Caddy 由 [`deploy/upstream/packages.json`](../../deploy/upstream/packages.json) 按 SHA-256 固定（[ADR 0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)）。每个 release 前，以及上游发布安全修复时，在开发机上：

```bash
scripts/update-upstream-packages.sh
git diff deploy/upstream/packages.json
```

- 脚本在系统测试的镜像中用 apt 读取两家的官方源，按 `deploy/upstream/*.asc` 中的公钥核对签名；公钥的指纹与 `packages.json` 中的不符时停止。上游换了公钥时，按官方文档核对新指纹，再把公钥与指纹一起改掉。
- 看差异：版本、文件与 `depends`。`depends` 多了 Debian 包时，系统测试的镜像随之重建。
- 提交后按第 1 步跑门禁并 `make release`。设备照常按第 6 步升级到这一版。

## 验证

- `a-nas-check`：全部 `PASS`，退出码为 0。
- `apt-cache policy a-nas a-nas-host`：`Installed` 为部署的版本。
- `dpkg --verify a-nas a-nas-host`：没有输出，即每个文件都与软件包相同。
- 正常重启一次后再运行 `a-nas-check`：开机时 `a-nas-generator` 与 `anas-site.service` 重新生成本机的配置，结果应相同。

## 回滚或恢复

- 退回上一个版本：同一条命令写旧版本号，加 `--allow-downgrades`；之后运行 `a-nas-check`。
  ```bash
  apt-get install --no-install-recommends --allow-downgrades -o Dpkg::Options::=--force-confold a-nas=<旧版本> a-nas-host=<旧版本>
  ```
  - 新版本改过的数据库结构，由旧版本启动时按新版本存下的反向步骤退回（[`internal/sqlitedb/migrate.go`](../../internal/sqlitedb/migrate.go)）。
  - Docker、Caddy 与 `a-nas-docker-policy` 不降级：`a-nas-host` 只要求不低于固定的版本。旧包即使没有地址池保护，独立包仍在 Docker 启动前校验配置。
- 旧版本启动时报告 `the newer build left no reverse step`，或 `a-nas-check` 报告某个服务没有打开数据卷上的数据库：从升级前的副本恢复。系统盘的数据库复制在 `/var/backups/a-nas/<时间>-<旧版本>/`，数据卷上的复制在数据卷内 `/srv/a-nas/data/.a-nas-backups/<时间>-<旧版本>/`（同名，与原数据库共用数据块）。Host Agent 也开着 `control.db`，一并停下；先删掉现有的 `-wal` 与 `-shm`，否则 SQLite 会把新版本留下的日志写进恢复的数据库。
  ```bash
  systemctl stop anas-api anas-photos anas-shares anas-family anas-host-agent
  name=<时间>-<旧版本>
  ls /var/backups/a-nas /srv/a-nas/data/.a-nas-backups
  rm -f /var/lib/a-nas/*.db-wal /var/lib/a-nas/*.db-shm \
    /srv/a-nas/data/photos/catalog.db-{wal,shm} /srv/a-nas/data/shares/catalog.db-{wal,shm} /srv/a-nas/data/family/family.db-{wal,shm}
  cp -a "/var/backups/a-nas/$name/system-disk/." /var/lib/a-nas/
  for store in photos shares family; do
    [[ ! -d "/srv/a-nas/data/.a-nas-backups/$name/$store" ]] || cp -a "/srv/a-nas/data/.a-nas-backups/$name/$store/." "/srv/a-nas/data/$store/"
  done
  systemctl start anas-host-agent anas-photos anas-shares anas-family anas-api
  a-nas-check
  ```
  副本是升级那一刻的数据库，之后的改动会丢失。两处各保留最近两份；副本不含 `-shm`，它由 SQLite 从日志重建。
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

- ADR：[0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)、[0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)、[0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)
- 决定记录：[发布用的签名密钥](../decisions/2026-11-11-release-signing-key.md)、[近期以局域网推送发布](../decisions/2026-11-13-release-by-lan-push.md)、[软件源与更新文件放在哪里](../decisions/2026-11-11-update-hosting.md)（已被取代）
- 规格：[系统更新](../specs/system-update.md)
- 运行手册：[管理发布签名密钥](manage-release-key.md)
- 代码：[`make release`](../../scripts/release.sh)、[软件源](../../scripts/apt-repository.sh)、[软件包](../../scripts/build-packages.sh)、[内容包](../../scripts/build-content-packages.sh)、[Docker 与 Caddy 的固定版本](../../deploy/upstream/packages.json)（[更新](../../scripts/update-upstream-packages.sh)、[下载](../../scripts/fetch-upstream-packages.sh)）、[`a-nas` 的维护脚本](../../deploy/debian/a-nas/DEBIAN/postinst)、[`a-nas-check`](../../deploy/site/a-nas-check.sh)、[`a-nas-site`](../../deploy/site/a-nas-site.sh)、[系统测试](../../scripts/system-test.sh)
- 研究：[设备镜像与 systemd-sysupdate 的验证](../research/sysupdate-image-verification.md)（软件包布局的约束）
- 存档：私有仓库 [A-NAS-assets](https://github.com/zhongwater123/A-NAS-assets)（内容库中的模型、运行环境与 SearXNG 源码包）
