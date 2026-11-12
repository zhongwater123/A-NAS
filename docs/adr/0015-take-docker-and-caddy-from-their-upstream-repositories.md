# 0015：Docker 与 Caddy 用上游官方的软件包，最低能力由代码检查

状态：accepted；2026-11-12 按用户确认修订决定 1、2：上游的软件包由 A-NAS 的签名软件源携带，设备不再添加上游源。部署与更新步骤见[部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)。

Experimental NAS 原先使用 Debian 13 自带的 `docker.io` 26.1.5、`docker-compose` 2.26.1 与 `caddy` 2.6.2。Debian 在 trixie 的生命周期内冻结这些版本，只零散移植安全修复，并把这类 Go 软件列为有限安全支持；上游已经到 Docker 29.x 与 Caddy 2.11.x。开发机运行 Docker 29.1.3 与 Compose 2.40.3，两边版本不一致：2026-10-09 应用中心在 NAS 的 Compose 2.26.1 下把 Immich 的整个应用目录与整个共享空间挂进容器，开发机上的测试却一直通过（[调查](../investigations/2026-10-09-old-compose-mounted-whole-app-volumes.md)）。我们决定：操作系统继续使用 Debian 稳定版，Docker 与 Caddy 改用各自官方为该 Debian 版本构建的软件包并跟随当前主线；产品依赖的最低能力由代码在运行时检查，而不是写死在文档里。

## 决定

1. **来源**：Docker Engine、CLI、containerd 与 Compose 插件（`docker-ce`、`docker-ce-cli`、`containerd.io`、`docker-compose-plugin`）用 Docker 官方 `download.docker.com/linux/debian` 的 trixie 软件包，Caddy 用 Caddy 官方 Cloudsmith 源的软件包。A-NAS 的签名软件源携带这些文件的副本，[`deploy/upstream/packages.json`](../../deploy/upstream/packages.json) 按 SHA-256 固定每个文件；`a-nas-host` 依赖它们，版本不低于本 release 固定的版本。设备只从 A-NAS 软件源与 Debian 取软件，不添加上游源，也不信任上游的签名密钥。产品不用 `docker-buildx-plugin`，不再安装。其余软件（Samba、Chromium、系统库等）继续来自 Debian。
2. **跟随主线**：[`scripts/update-upstream-packages.sh`](../../scripts/update-upstream-packages.sh) 用 apt 读取上游源，按仓库中固定指纹的上游公钥（`deploy/upstream/*.asc`）核对签名，把最新版本写进 `packages.json`；经门禁与系统测试后随 A-NAS 的 release 发布。每个 release 前更新一次；上游发布安全修复时单独发一个 release。新大版本先在开发机与系统测试上验证。开发机的 Docker 主版本与 NAS 保持一致。
3. **最低能力在代码中检查**：容器代理在规划安装前检查 Compose 不低于 2.30、Engine API 不低于 1.45（卷子目录挂载所需），不满足时返回 `docker_runtime_outdated`；安装后核对每个 A-NAS 卷只挂载计划中的子目录，否则回滚（[应用中心规格](../specs/app-center.md)）。文档不再写死版本号。
4. **分开升级**：产品服务读取容器代理的响应时忽略新增字段，两者可以先后升级。

## 后果

- 获得上游的安全修复与新功能，并与开发机一致，减少“开发机通过、NAS 失败”。设备装的 Docker 与 Caddy 正是系统测试安装过的文件。
- 设备只信任 A-NAS 的签名密钥；上游的签名在更新脚本读取上游源时核对。上游公钥换了时，指纹不符，脚本停止，要人工核对后再改 `packages.json`。
- 这两个组件的安全更新不由 Debian 安全团队提供，也不会自动到达设备：要更新 `packages.json` 并发布 A-NAS。Cloudsmith 不可用时（2026-10 一度返回 `402`，2026-11-11 构建系统测试镜像时卡住）只影响更新，不影响设备安装。
- 每台设备都装着 Docker 与 Caddy（下载约 92 MB）。Caddy 一装上局域网入口就开放（[ADR 0012](0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)）；Docker 在地址池写好之前不启动（[ADR 0013](0013-allocate-docker-networks-from-an-a-nas-address-pool.md)）。
- 回滚 A-NAS 不降级 Docker 与 Caddy：依赖只要求不低于固定的版本。软件源保留每个版本，每更新一次多约 92 MB。
- 2026-10-10 的切换卸载了 Debian 的 `docker.io`、`docker-cli`、`docker-compose`、`containerd` 与 `runc`，Docker 与容器中断了几分钟；`/var/lib/docker` 中的镜像、容器与卷保留。Docker 29 新安装默认使用 containerd 镜像存储，从旧版升级则保留原有存储驱动。临时手动安装在 `/usr/local/lib/docker/cli-plugins/` 的 Compose 在切换后删除。
- 新大版本可能带来行为变化，需要先在开发机和系统测试上验证。

## 未选择

- 继续使用 Debian 包：版本冻结到 trixie 生命周期结束（常规支持至 2028 年，LTS 至 2030 年），而且已经因 Compose 过旧出现隔离失效。
- Debian 包加手动放置的新版 Compose：能临时解决问题，但不随 apt 更新，来源混杂。
- Debian testing 中的 `docker.io`：版本仍落后上游，与稳定版混用会带来依赖风险。
- Snap 或独立二进制：不随系统包管理更新，升级、回滚与审计都更难。
- 设备直接添加上游源（2026-10-10 至 2026-11-12 的做法）：设备要信任三把签名密钥，装上的版本没有经过系统测试，上游源不可用时新设备装不全。
- 用 aptly 的 `mirror` 镜像上游源：aptly 的状态只在发布机上，系统测试与 CI 拿不到同一份文件；按 SHA-256 固定的清单在任何地方都取到同样的文件。

## 关联

- [Compose 2.26.1 挂载整个卷调查](../investigations/2026-10-09-old-compose-mounted-whole-app-volumes.md)
- [ADR 0009：Docker Engine 与容器代理](0009-use-docker-engine-through-a-dedicated-container-agent.md)
- [ADR 0010：应用清单与安装策略](0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)
- [ADR 0012：局域网 Web 入口](0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)
- [ADR 0034：签名软件源中的 Debian 软件包](0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)
- [应用中心规格](../specs/app-center.md)
- [部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)、[安装 Docker 与容器代理](../runbooks/install-container-agent.md)、[启用局域网 Web 访问](../runbooks/enable-lan-web-access.md)
- 代码：[`deploy/upstream/packages.json`](../../deploy/upstream/packages.json)、[`update-upstream-packages.sh`](../../scripts/update-upstream-packages.sh)、[`fetch-upstream-packages.sh`](../../scripts/fetch-upstream-packages.sh)、[`a-nas-host`](../../deploy/debian/a-nas-host/DEBIAN/control)
