# 0031：宿主机依赖由 A-NAS 自己的 Debian 软件包声明

状态：proposed（2026-11-04；2026-11-05 改写决定 2）；对应 [#189](https://github.com/zhongwater123/A-NAS/issues/189) 中“发布不是一个可校验的整体”的宿主机一半。

A-NAS 在 Debian 13 上运行，依赖 Samba、Btrfs 工具、FFmpeg、Chromium 等系统软件包，也依赖几项宿主机配置。这些依赖分散在首次安装向导、安装器、系统测试镜像与几本手册中，靠人工同步，已经不一致；Experimental NAS 上还有几项只在当时手工做过。按现有文档装一台新宿主机，要逐本手册核对，而且会漏。我们决定用 Debian 自己的方式声明依赖：仓库构建两个只含依赖与少量配置的软件包，宿主机用 apt 一次装齐；安装器、首次安装向导、手册与系统测试镜像都以这两个包为准。

## 背景

2026-11-04 核对的依赖来源：

| 位置 | 内容 | 问题 |
|---|---|---|
| 首次安装向导 [`provision-v1.0.1-rc.sh`](../../scripts/provision-v1.0.1-rc.sh) | 一行 `apt-get install`，12 个包 | 缺 `libgomp1` 与 `libssl3t64`（相册读文字的 llama-server 链接它们），按它装的新机器会在安装器的库检查处以退出码 3 停下 |
| [实验存储手册](../runbooks/provision-v1.0.1-experimental-storage.md#安装系统服务) | 同一行的副本 | 同上 |
| [系统服务安装器](../../scripts/install-v1.0.1-system-services.sh) | 检查 18 个命令与 5 个库，缺少时退出码 3 | 只检查、不安装；与上面两份各自维护 |
| [系统测试镜像](../../scripts/build-system-test-image.sh) | `debootstrap --include` 的清单 | 最全，但混有只供测试用的 `jq`、`yq`、`curl` |
| [本地控制台手册](../runbooks/operate-local-kiosk.md)、[容器代理手册](../runbooks/install-container-agent.md)、[局域网入口手册](../runbooks/enable-lan-web-access.md)、助手试用的 [`setup.sh`](../../ai/assistant-spike/setup.sh) | 各自的 `apt install` | 本地控制台的 VA-API 驱动是 2026-10-29 补装的 |

只在 Experimental NAS 上手工做过、仓库里没有的：

- 改用 Debian 的 Caddy 并删除官方源（Cloudsmith 返回 `402`）；
- 每次重启后 `systemctl restart smbd`：smbd 以 `bind interfaces only` 启动时，`enp2s0`（`allow-hotplug`）还没从 DHCP 拿到地址，所以它只监听回环（[调查](../investigations/2026-11-04-smbd-starts-before-dhcp-address.md)）。

## 决定

1. **两个软件包，构建自仓库。** 包的定义放在 `deploy/debian/` 下，由 `dpkg-deb --build --root-owner-group` 构建，架构为 `all`，不含产品的二进制。版本取 release 的产品版本（预发布写作 `1.0.1~rc7`，按 Debian 的规则排在 `1.0.1` 之前）。
   - **`a-nas-host`**：每台 A-NAS 都装。`Depends` 列出各服务、Worker 与安装器在宿主机上运行或链接的全部软件包：`acl`、`btrfs-progs`、`parted`、`smartmontools`、`samba`、`iproute2`、`udev`、`systemd`、`python3`、`ffmpeg`、`heif-thumbnailer` 与 `libheif-plugin-libde265`，以及 MediaPipe 与 llama-server 链接的 `libegl1`、`libgles2`、`libgomp1`、`libssl3t64`。清单按 Go 代码执行的命令、release 二进制的 `ldd` 与安装器原来的检查核对过：Go 服务内置 SQLite、只链接 libc，所以去掉了 `libsqlite3-0`。各有启用手册的可选功能写在 `Suggests`，不随包安装，与现在一样由各自的手册装：
     - Caddy：局域网入口装了 Caddy 才开放（[局域网 Web 访问](../specs/lan-web-access.md)）；
     - Docker（`docker-ce`、`docker-compose-plugin`）：来自 Docker 的官方源（[ADR 0015](0015-take-docker-and-caddy-from-their-upstream-repositories.md)），应用中心照旧在运行时检查最低能力；
     - 助手沙箱的 `tini`、`socat`、`ripgrep`：由助手试用脚本安装。
   - **`a-nas-console`**：只给有本机屏幕的设备（[ADR 0005](0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)）。`Depends` 为同一版本的 `a-nas-host`、`cage`、`chromium`、`chromium-sandbox`、`chromium-l10n`、`fonts-noto-cjk`、`wlr-randr`、`libinput-tools`、`vainfo`，以及 VA-API 驱动 `intel-media-va-driver | mesa-va-drivers`：默认装 Intel 的，AMD 显卡的设备先装 `mesa-va-drivers`。驱动不写成 `Recommends`，因为本项目的安装命令都带 `--no-install-recommends`。没有屏幕的设备不装它，系统测试的镜像也不装。
2. **smbd 等局域网接口有了地址再启动，由安装器配置。** smbd 按 `bind interfaces only` 只监听启动时已有的地址；Debian 安装程序把网卡写成 `allow-hotplug`、用 DHCP 取地址，开机时地址可能晚于 smbd 到达。安装器知道 SMB 接口，为 smbd 写入附加配置 `/etc/systemd/system/smbd.service.d/a-nas-lan-address.conf`：
   - 启动前最多等 20 秒，等这个接口有 IPv4 地址；
   - 等不到时这次启动失败，systemd 5 秒后再试，直到有地址，所以路由器比 NAS 晚启动时 SMB 也会恢复；
   - 排在 smbd 之后的 Host Agent 在第一次失败后启动，没有网络时产品最多晚 20 秒可用；smbd 没有运行时，Host Agent 的 `smbcontrol all reload-config` 照常成功；
   - `Restart=on-failure` 同时让崩溃的 smbd 自动重启。

   它只用 `iproute2` 的 `ip`（列入 `a-nas-host` 的 `Depends`），与 ifupdown、dhcpcd 等网络工具无关。安装时 smbd 若已在运行，监听不变，配置在它下次启动时生效。两个包都只声明依赖，没有维护脚本；单元、环境文件、Chromium 策略与传感器模块等宿主机配置都由安装器按 release 写入。
3. **release 带上这两个包，安装前先装包。** 暂存的 release 目录含 `a-nas-host.deb` 与 `a-nas-console.deb`，版本写在包内。`deploy-dev.ps1` 与二进制一起输出它们的 SHA-256，root 先与二进制一起核对：包的维护脚本以 root 运行，暂存账号能改动暂存目录，这与 root 运行暂存目录中的安装器是同一层信任（[#171](https://github.com/zhongwater123/A-NAS/issues/171)）。然后 root 执行 `apt install --no-install-recommends --allow-downgrades <release>/a-nas-host.deb`（有屏幕时连同控制台包），再运行安装器。版本只标明 release、不表示先后：未打标签的提交按哈希排序，标签之后的提交也排在标签之前，所以升级在 apt 看来可能是降级，安装一律允许降级。依赖没有变化时 apt 不需要联网。安装器把两个包复制进 `/opt/a-nas/releases/<ID>`，回滚时从那里装回旧版本。
4. **安装器只核对包的版本。** 安装器不再逐个检查命令与库，改为核对 `a-nas-host` 已安装且版本等于本 release 的版本；不满足时以退出码 3 结束并打印要执行的 `apt install` 命令。依赖是否齐全由 dpkg 保证。
5. **一处声明，其余引用。** 首次安装向导与各本手册改为安装这两个包，删除各自的清单。系统测试镜像的产品依赖取自 `a-nas-host` 的 `Depends`，只另列系统测试打开的可选功能与测试用的工具，镜像缓存的键包含包的定义；系统测试再以 `dpkg -i` 装上 release 的 `a-nas-host`，它不下载缺少的依赖而是失败。这样系统测试每次都在验证这份清单：包与镜像对不上时，测试就会失败。
6. **产品本身暂不打包。** 二进制、单元、系统账号、ACL 修复与各项迁移仍由安装器完成。把产品也做成软件包，交由 dpkg 记录文件、校验（`dpkg --verify`）、升级与回滚，是 #189 的下一步，另立 ADR。

## 实施计划

实现放在一个 PR 中，按步骤分提交；之后由负责部署的会话在 Experimental NAS 上安装。ADR 在 [PR #381](https://github.com/zhongwater123/A-NAS/pull/381) 中合并，第 1–3 步在 [PR #385](https://github.com/zhongwater123/A-NAS/pull/385) 中，第 4 步随下一个 release。决定 2 原为由 `a-nas-host` 的 `postinst` 启用 `ifupdown-wait-online.service`；2026-11-04 的实机重启证明它修不好 smbd（见“未选择”），2026-11-05 改为安装器写入的附加配置。

1. **包定义与构建**：`deploy/debian/a-nas-host/`、`deploy/debian/a-nas-console/` 的 `DEBIAN/control`，由 `scripts/build-host-packages.sh` 构建；Makefile 的 `host-packages` 在 `build-binaries` 之后构建，属于 `make check`；`deploy-dev.ps1` 把包与二进制一起暂存并输出哈希。
2. **系统测试镜像**：`build-system-test-image.sh` 从 `a-nas-host` 的 `Depends` 取产品依赖，测试工具单独列出；系统测试检查安装器在没有包、以及包版本不符时以退出码 3 结束、不改变系统，再以 `dpkg -i` 装上 release 的包。系统测试还在一个暂时没有地址的接口上检查 smbd 的附加配置：smbd 不启动，到时再试，接口有了地址就启动；DHCP 下的效果在第 4 步的实机重启中核对。
3. **安装器、向导与手册**：安装器改为核对包版本；向导、实验存储手册与本地控制台手册改为安装这两个包；Docker、Caddy 与助手工具是 `Suggests`，各自的启用手册不变；`CURRENT.md` 写明 smbd 的修复所在，第 4 步实机核对后再删去这项阻塞。
4. **Experimental NAS**：随下一个 release 安装两个包，再运行安装器。之后正常重启一次，核对 smbd 监听 `172.18.45.48:445`，这同时是 v1.0.1 验收的第 6 步。

## 后果

- 新宿主机的步骤变为：安装 Debian 13，`apt install` 两个包，运行安装器。系统测试证明这几步装出的系统可用。局域网入口、应用中心与助手仍按各自的手册打开。
- apt 记录每个软件包为什么在：要删除 Samba 时，apt 会提示 `a-nas-host` 依赖它，不会悄悄删掉。
- 部署多出一条 `apt install` 命令；依赖有变化时需要能访问 Debian 镜像源。Experimental NAS 现在可以访问。
- 回滚时安装旧版本的包（`--allow-downgrades`）。新版本多装的软件包会留在系统上，不影响旧版本运行；`apt autoremove` 只移除没有其他包需要的自动安装项。
- 开发机与 CI 需要 `dpkg-deb`：Ubuntu 的 WSL 与 GitHub 的 Ubuntu 运行器都自带。
- Experimental NAS 上已经手工安装的软件包保持“手动安装”的标记，装上 `a-nas-host` 后不会被移除。
- 没有网络时，smbd 每 25 秒左右记一次启动失败，直到接口有了地址。

## 未选择

- **一份共用的清单文件**，由向导、安装器与镜像各自读取：能去掉副本，但 apt 仍不知道这些包为什么在，表达不了“Intel 或 AMD 驱动二选一”，也没有版本；它只是用脚本模仿 `Depends`。
- **配置管理工具（Ansible 等）**：需要控制端与主机清单，适合管理一批机器；这里大部分宿主机配置随 release 变化，本来就由安装器写入。
- **整机系统镜像（mkosi、debootstrap 镜像）**：最适合装全新机器，但对原地升级没有帮助；以后可以在这两个包之上构建。
- **现在就把产品一起打包**：维护脚本要接手安装器的账号、ACL、控制台迁移与 AI 运行环境等逻辑，改动大得多，放在下一步。
- **继续靠手册**：即现状，已经漂移。
- **smbd 的其他修法**：
  - 启用 ifupdown 的 `ifupdown-wait-online.service`（决定 2 原来的做法）：它的默认方法只等 ifupdown 记下接口，不等地址；2026-11-04 在 Experimental NAS 上重启，等待比 DHCP 地址早约 10 秒结束（[调查](../investigations/2026-11-04-smbd-starts-before-dhcp-address.md)）。改用它的 `route` 方法要修改 ifupdown 的配置文件 `/etc/default/networking`，ifupdown 升级时会冲突，而且只适用于由 ifupdown 管理网络的宿主机；
  - 改为 `bind interfaces only = no`：smbd 会在 Docker 网桥等所有地址上监听，扩大了暴露面；
  - 在接口拿到地址时重启 smbd 的 dhcpcd 或 ifupdown 钩子：能处理之后的地址变化，但会断开已有会话。若以后需要应对 DHCP 换地址，再作为补充。

## 关联

- [#189](https://github.com/zhongwater123/A-NAS/issues/189)：发布不是一个可校验的整体
- [ADR 0005：本地控制台](0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- [ADR 0015：Docker 与 Caddy 跟随上游官方软件源](0015-take-docker-and-caddy-from-their-upstream-repositories.md)
- [ADR 0016：相册 AI 随系统内置安装](0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- [配置并验收 v1.0.1 实验数据卷](../runbooks/provision-v1.0.1-experimental-storage.md)、[运行本地控制台](../runbooks/operate-local-kiosk.md)、[安装 Docker 与容器代理](../runbooks/install-container-agent.md)
- [当前状态](../status/CURRENT.md)
