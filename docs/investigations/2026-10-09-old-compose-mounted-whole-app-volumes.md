# Debian 的 Compose 2.26.1 把应用的整个卷挂进容器

状态：fixed locally；Experimental NAS 已换用 Docker 官方源，包含检查的容器代理待部署
更新时间：2026-10-10

## 症状与影响

2026-10-09 19:26 配置地址池后重装 Immich，数据库容器反复重启：`initdb: error: directory "/var/lib/postgresql/data" exists but is not empty`；Immich 服务随之报 `getaddrinfo ENOTFOUND database` 并反复退出。Immich 的上传目录本应只挂载共享空间下的 `Gallery/immich`，实际挂载的是整个共享空间且可写；数据库与机器学习容器拿到的是整个 `apps/immich`。已经安装的 OpenList 同样拿到整个 `apps/openlist`，数据落在应用目录根部而不是 `data` 子目录。这违反了应用中心"应用只能访问计划中的文件夹"的隔离承诺。

## 最小复现

1. 在开发机用同一个带子目录的卷定义（`volume.subpath: sub`，`nocopy`，`local` 驱动绑定到一个含 `sub/only-in-sub.txt` 的目录）分别运行官方发布的 Compose 程序。
2. Compose 2.26.1、2.27.0、2.28.1、2.29.7 把挂载写成旧式绑定字符串 `<卷>:/mnt:rw,nocopy,sub`，`HostConfig.Mounts` 为空；本地 Docker 29 直接报 `invalid mode: rw,nocopy,sub`。
3. Compose 2.30.3、2.31.0、2.32.4、2.33.1、2.35.1、2.40.3 改用 `HostConfig.Mounts`，`VolumeOptions.Subpath` 为 `sub`，容器只看到 `only-in-sub.txt`。
4. NAS 的 Docker 26.1.5 接受了旧式字符串，忽略子目录并挂载整个卷。

## 观察事实

- NAS：Docker Engine `26.1.5+dfsg1`（API 1.45），Docker Compose `2.26.1-4`，均为 Debian 13 打包版本。
- 两个 Immich 容器的 `HostConfig.Mounts` 为 `null`；卷 `a-nas-immich_a-nas-appdata` 的 `device` 为 `/srv/a-nas/data/apps/immich`；渲染出的 Compose 文件中 `subpath` 分别为 `pgdata`、`model-cache` 与 `Gallery/immich`，渲染本身正确。
- 宿主机上的 `pgdata` 是空目录，`apps/immich` 被改为属主 999（容器内 postgres）、权限 `0700`：postgres 启动脚本把整个应用目录当成了数据目录并改了属主。
- OpenList 的 `config.json`、`data.db`、`log`、`temp` 位于 `apps/openlist` 根部，`data` 子目录为空。
- 开发机运行 Docker 29.1.3 与 Compose 2.40.3，2026-10-07 的应用中心实测在开发机上通过，见[应用中心规格](../specs/app-center.md)。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 渲染没有生成子目录 | 读取 NAS 上渲染出的 Compose 文件 | rejected：`subpath` 正确 |
| Immich 首次安装留下了残缺的数据库 | 检查宿主机 `pgdata` | rejected：目录为空 |
| 旧版 Compose 没有把子目录交给引擎 | `HostConfig.Mounts`；多版本 Compose 对照实验 | confirmed：2.29.7 及以前走旧式绑定接口并丢弃子目录 |

## 根因

Compose 2.29 及以前在卷挂载没有其他高级选项时，用旧式绑定字符串创建容器，子目录被并入挂载模式字符串而不是作为 `VolumeOptions.Subpath` 传给引擎。Docker 26.1.5 不拒绝这种字符串，于是挂载整个卷。Debian 13 的 `docker-compose` 停留在 2.26.1，NAS 因此一直运行在会丢弃子目录的版本上；开发机的 Compose 是 2.40.3，所以测试没有发现。

## 修复与回归证据

- 实机（2026-10-09）：从 docker/compose 官方发布获取 2.40.3 独立程序，SHA-256 `dba9d98e1ba5bfe11d88c99b9bd32fc4a0624a30fafe68eea34d61a3e42fd372` 与发布记录一致，安装到 `/usr/local/lib/docker/cli-plugins/docker-compose`；root 与容器代理身份下都报告 2.40.3。在 NAS 上用临时目录实测子目录挂载，容器只看到子目录中的文件。OpenList 停止后把文件移入 `data`，重建后挂载为 `"Subpath":"data"`，HTTP 返回 200 且使用原有数据库。卸载后重装的 Immich 中 postgres 正常运行，网页 `:2283` 返回 200，网络为 `10.96.65.0/24`。
- 实机（2026-10-10）：按[迁移步骤](../runbooks/install-container-agent.md#从-debian-dockerio-迁移到官方软件源)换用 Docker 官方源，得到 Docker 29.9.0（API 1.56）与 Compose v5.6.0，手动安装的 Compose 已删除，现有应用的挂载不变。
- 决定：[ADR 0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)，Docker 与 Caddy 改用官方软件源。
- 代码：[`Store.Plan`](../../internal/appstore/engine/engine.go) 在 Compose 低于 2.30 或 Engine API 低于 1.45 时返回 `docker_runtime_outdated`；安装后核对每个 A-NAS 卷的挂载都是计划中的子目录（[`ProjectMounts`](../../internal/appstore/engine/docker.go) 从挂载规格读取子目录，旧式绑定没有规格即视为整个卷），否则回滚；[`Render`](../../internal/appstore/render.go) 在计划中记录每个文件夹对应的卷与子目录；产品服务读取代理响应时忽略新增字段。
- 测试：`TestPlanRefusesADockerThatDropsVolumeSubpaths`、`TestInstallRollsBackAMountBeyondItsFolder`、`TestInstallKeepsMountsOnTheirFolders`、`TestRenderRecordsTheVolumeAndSubpathOfEachFolder`、`TestClientIgnoresFieldsANewerAgentAdds`。

## 后续工作

- 在 Experimental NAS 部署包含版本与挂载检查的容器代理。
- 首次失败安装期间 Immich 服务一直连不上数据库，未见它初始化上传目录；如需确认，检查共享空间根部是否出现 Immich 的 `library`、`upload`、`thumbs` 等目录。

## 关联

- 规格：[应用中心](../specs/app-center.md)
- ADR：[0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)、[0010](../adr/0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)
- 运行手册：[安装 Docker 与容器代理](../runbooks/install-container-agent.md)
- 调查：[应用网络切断 WiFi 客户端](2026-10-09-app-network-cut-off-wifi-clients.md)
