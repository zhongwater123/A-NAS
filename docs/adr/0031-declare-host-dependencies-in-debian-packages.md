# 0031：宿主机依赖由 A-NAS 自己的 Debian 软件包声明

状态：proposed（2026-11-04）；对应 [#189](https://github.com/zhongwater123/A-NAS/issues/189) 中“发布不是一个可校验的整体”的宿主机一半。

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
- 每次重启后 `systemctl restart smbd`：smbd 以 `bind interfaces only` 启动时，`enp2s0`（`allow-hotplug`）还没从 DHCP 拿到地址，而 `ifupdown-wait-online.service` 没有启用，所以它只监听回环。smbd 的单元已经排在 `network-online.target` 之后，只是没有服务实现这个目标。

## 决定

1. **两个软件包，构建自仓库。** 包的定义放在 `deploy/debian/` 下，由 `dpkg-deb --build --root-owner-group` 构建，架构为 `all`，不含产品的二进制。版本取 release 的产品版本（预发布写作 `1.0.1~rc7`，按 Debian 的规则排在 `1.0.1` 之前）。
   - **`a-nas-host`**：每台 A-NAS 都装。`Depends` 列出各服务、Worker 与安装器在宿主机上运行或链接的全部软件包，包括 Samba、Btrfs 与 ACL 工具、SMART、FFmpeg 与 libheif，以及 MediaPipe 与 llama-server 链接的库。没有它们照样能运行、或来自其他软件源的写在 `Recommends`，apt 默认一并安装：
     - Caddy：局域网入口装了 Caddy 才开放（[局域网 Web 访问](../specs/lan-web-access.md)）；
     - 助手沙箱的 `tini`、`socat`、`ripgrep`：现在由试用脚本自己安装；
     - Docker（`docker-ce`、`docker-compose-plugin` 等）：来自 Docker 的官方源（[ADR 0015](0015-take-docker-and-caddy-from-their-upstream-repositories.md)），没有配置这个源时 apt 跳过它们，应用中心照旧在运行时检查最低能力。
   - **`a-nas-console`**：只给有本机屏幕的设备（[ADR 0005](0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)）。`Depends` 为 `cage`、`chromium`、`chromium-sandbox`、`chromium-l10n`、`fonts-noto-cjk`、`wlr-randr`、`libinput-tools`；硬件解码驱动按显卡不同，写成 `Recommends: intel-media-va-driver | mesa-va-drivers, vainfo`，缺少时只是失去硬件解码。没有屏幕的设备不装它，系统测试的镜像也不装。
   - 清单在实施时逐项核实：以 release 二进制的 `ldd` 与安装器现有的检查为准。例如 Go 服务内置 SQLite，`libsqlite3-0` 是否还需要单独列出要核对。
2. **与 release 无关的宿主机配置随 `a-nas-host` 安装。** 第一项是启用网络等待：宿主机用 ifupdown 管理局域网接口时，`postinst` 启用 `ifupdown-wait-online.service`。它对 `allow-hotplug` 接口也等到 `ifup` 完成，smbd 因此在拿到地址之后才启动。与 release 有关的配置（单元、环境文件、Chromium 策略、传感器模块）仍由安装器按 release 写入。
3. **release 带上这两个包，安装前先装包。** 暂存的 release 目录含 `a-nas-host_<版本>_all.deb` 与 `a-nas-console_<版本>_all.deb`。部署由 root 先执行 `apt install ./a-nas-host_<版本>_all.deb`（有屏幕时连同控制台包），再运行安装器。依赖没有变化时 apt 不需要联网。
4. **安装器只核对包的版本。** 安装器不再逐个检查命令与库，改为核对 `a-nas-host` 已安装且版本等于本 release 的版本；不满足时以退出码 3 结束并打印要执行的 `apt install` 命令。依赖是否齐全由 dpkg 保证。
5. **一处声明，其余引用。** 首次安装向导与各本手册改为安装这两个包，删除各自的清单。系统测试镜像在最小的 Debian 13 上安装本次构建的 `a-nas-host`，只另列测试用的工具；CI 先构建包再构建镜像，镜像缓存的键包含包的定义。这样系统测试每次都在验证这份清单：漏掉一个依赖，测试就会失败。
6. **产品本身暂不打包。** 二进制、单元、系统账号、ACL 修复与各项迁移仍由安装器完成。把产品也做成软件包，交由 dpkg 记录文件、校验（`dpkg --verify`）、升级与回滚，是 #189 的下一步，另立 ADR。

## 实施计划

一个 PR，按步骤分提交；之后由负责部署的会话在 Experimental NAS 上安装。

1. **包定义与构建**：`deploy/debian/a-nas-host/`、`deploy/debian/a-nas-console/` 的 `DEBIAN/control` 与 `postinst`；Makefile 增加 `host-packages`，`build-binaries` 之后构建，`deploy-dev.ps1` 把包与二进制一起暂存；`make ops-check` 用 `dpkg-deb --info` 与 `--contents` 核对两个包可以解析、属主为 root。
2. **系统测试镜像**：`build-system-test-image.sh` 在 debootstrap 的最小系统中安装 `a-nas-host`，测试工具单独列出；系统测试检查安装器在包版本不符时以退出码 3 结束、不改变系统。容器中的网络不由 ifupdown 管理，网络等待的效果只能在第 4 步的实机重启中核对。
3. **安装器、向导与手册**：安装器改为核对包版本；向导、实验存储手册、本地控制台手册与容器代理手册改为安装这两个包；`CURRENT.md` 删去 smbd 的阻塞。
4. **Experimental NAS**：随下一个 release 安装两个包，再运行安装器。之后正常重启一次，核对 smbd 监听 `172.18.45.48:445`，这同时是 v1.0.1 验收的第 6 步。

## 后果

- 新宿主机的步骤变为：安装 Debian 13，按 ADR 0015 配置 Docker 的源，`apt install` 两个包，运行安装器。系统测试证明这几步装出的系统可用。
- apt 记录每个软件包为什么在：要删除 Samba 时，apt 会提示 `a-nas-host` 依赖它，不会悄悄删掉。
- 部署多出一条 `apt install` 命令；依赖有变化时需要能访问 Debian 镜像源。Experimental NAS 现在可以访问。
- 回滚时安装旧版本的包（`--allow-downgrades`）。新版本多装的软件包会留在系统上，不影响旧版本运行；`apt autoremove` 只移除没有其他包需要的自动安装项。
- 开发机与 CI 需要 `dpkg-deb`：Ubuntu 的 WSL 与 GitHub 的 Ubuntu 运行器都自带。
- Experimental NAS 上已经手工安装的软件包保持“手动安装”的标记，装上 `a-nas-host` 后不会被移除。

## 未选择

- **一份共用的清单文件**，由向导、安装器与镜像各自读取：能去掉副本，但 apt 仍不知道这些包为什么在，表达不了“Intel 或 AMD 驱动二选一”，也没有版本；它只是用脚本模仿 `Depends`。
- **配置管理工具（Ansible 等）**：需要控制端与主机清单，适合管理一批机器；这里大部分宿主机配置随 release 变化，本来就由安装器写入。
- **整机系统镜像（mkosi、debootstrap 镜像）**：最适合装全新机器，但对原地升级没有帮助；以后可以在这两个包之上构建。
- **现在就把产品一起打包**：维护脚本要接手安装器的账号、ACL、控制台迁移与 AI 运行环境等逻辑，改动大得多，放在下一步。
- **继续靠手册**：即现状，已经漂移。
- **smbd 的另外两种修法**：
  - 改为 `bind interfaces only = no`：smbd 会在 Docker 网桥等所有地址上监听，扩大了暴露面；
  - 在接口启动时重启 smbd 的 ifupdown 钩子：能处理之后的地址变化，但会断开已有会话。若以后需要应对 DHCP 换地址，再作为补充。

## 关联

- [#189](https://github.com/zhongwater123/A-NAS/issues/189)：发布不是一个可校验的整体
- [ADR 0005：本地控制台](0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- [ADR 0015：Docker 与 Caddy 跟随上游官方软件源](0015-take-docker-and-caddy-from-their-upstream-repositories.md)
- [ADR 0016：相册 AI 随系统内置安装](0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- [配置并验收 v1.0.1 实验数据卷](../runbooks/provision-v1.0.1-experimental-storage.md)、[运行本地控制台](../runbooks/operate-local-kiosk.md)、[安装 Docker 与容器代理](../runbooks/install-container-agent.md)
- [当前状态](../status/CURRENT.md)
