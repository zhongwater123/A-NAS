# 0015：Docker 与 Caddy 跟随上游官方软件源，最低能力由代码检查

状态：accepted；迁移步骤见[安装 Docker 与容器代理](../runbooks/install-container-agent.md#从-debian-dockerio-迁移到官方软件源)与[启用局域网 Web 访问](../runbooks/enable-lan-web-access.md)。

Experimental NAS 原先使用 Debian 13 自带的 `docker.io` 26.1.5、`docker-compose` 2.26.1 与 `caddy` 2.6.2。Debian 在 trixie 的生命周期内冻结这些版本，只零散移植安全修复，并把这类 Go 软件列为有限安全支持；上游已经到 Docker 29.x 与 Caddy 2.11.x。开发机运行 Docker 29.1.3 与 Compose 2.40.3，两边版本不一致：2026-10-09 应用中心在 NAS 的 Compose 2.26.1 下把 Immich 的整个应用目录与整个共享空间挂进容器，开发机上的测试却一直通过（[调查](../investigations/2026-10-09-old-compose-mounted-whole-app-volumes.md)）。我们决定：操作系统继续使用 Debian 稳定版，Docker 与 Caddy 改用各自官方为该 Debian 版本发布的 apt 源并跟随当前主线；产品依赖的最低能力由代码在运行时检查，而不是写死在文档里。

## 决定

1. **来源**：Docker Engine、CLI、containerd 与 Compose 插件来自 `download.docker.com/linux/debian` 的 trixie 源（`docker-ce`、`docker-ce-cli`、`containerd.io`、`docker-compose-plugin`、`docker-buildx-plugin`）；Caddy 来自 Caddy 官方的 Cloudsmith 源。签名密钥由 apt 的 `Signed-By` 限定到各自的源。其余软件（Samba、Chromium、系统库等）继续来自 Debian。
2. **跟随主线**：随系统的常规更新执行 `apt upgrade`；新大版本按运行手册升级，先在开发机与系统测试上验证。开发机的 Docker 主版本与 NAS 保持一致。
3. **最低能力在代码中检查**：容器代理在规划安装前检查 Compose 不低于 2.30、Engine API 不低于 1.45（卷子目录挂载所需），不满足时返回 `docker_runtime_outdated`；安装后核对每个 A-NAS 卷只挂载计划中的子目录，否则回滚（[应用中心规格](../specs/app-center.md)）。文档不再写死版本号。
4. **分开升级**：产品服务读取容器代理的响应时忽略新增字段，两者可以先后升级。

## 后果

- 获得上游的安全修复与新功能，并与开发机一致，减少"开发机通过、NAS 失败"。
- 需要信任 Docker 与 Caddy 的软件源签名密钥；这两个组件的安全更新不再由 Debian 安全团队提供，必须定期 `apt upgrade`，或把这两个源加入自动更新的允许来源。
- 切换时要卸载 Debian 的 `docker.io`、`docker-cli`、`docker-compose`、`containerd` 与 `runc`，Docker 与容器会中断几分钟；Caddy 的包名相同，原地升级并保留 A-NAS 配置。`/var/lib/docker` 中的镜像、容器与卷保留。Docker 29 新安装默认使用 containerd 镜像存储，从旧版升级则保留原有存储驱动。
- 新大版本可能带来行为变化，需要先在开发机和系统测试上验证。
- 临时手动安装在 `/usr/local/lib/docker/cli-plugins/` 的 Compose 在切换后删除，改由 `docker-compose-plugin` 包管理。

## 未选择

- 继续使用 Debian 包：版本冻结到 trixie 生命周期结束（常规支持至 2028 年，LTS 至 2030 年），而且已经因 Compose 过旧出现隔离失效。
- Debian 包加手动放置的新版 Compose：能临时解决问题，但不随 apt 更新，来源混杂。
- Debian testing 中的 `docker.io`：版本仍落后上游，与稳定版混用会带来依赖风险。
- Snap 或独立二进制：不随系统包管理更新，升级、回滚与审计都更难。

## 关联

- [Compose 2.26.1 挂载整个卷调查](../investigations/2026-10-09-old-compose-mounted-whole-app-volumes.md)
- [ADR 0009：Docker Engine 与容器代理](0009-use-docker-engine-through-a-dedicated-container-agent.md)
- [ADR 0010：应用清单与安装策略](0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)
- [ADR 0012：局域网 Web 入口](0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)
- [应用中心规格](../specs/app-center.md)
- [安装 Docker 与容器代理](../runbooks/install-container-agent.md)、[启用局域网 Web 访问](../runbooks/enable-lan-web-access.md)
