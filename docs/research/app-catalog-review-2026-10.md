# 应用目录扩充评审（2026-10）

状态：完成；入选应用尚未在实验 NAS 上实机安装
更新时间：2026-10-12

## 问题

应用中心原先内置 26 个应用，其中“下载”分类只有 qBittorrent 和 Transmission。本次从同一上游提交（CasaOS-AppStore `0909364`）的 178 个应用中挑出适合家用 A-NAS 的应用补充进内置清单，清单文件仍逐字节保持上游原样（[ADR 0010](../adr/0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)）。

## 方法

1. 下载固定提交的上游快照，确认已内置的应用（清单与图标共 60 个文件）与上游逐字节一致。
2. 用生产代码的 `appstore.Render` 与安装策略渲染其余 152 个应用：95 个通过；48 个被策略拒绝（特权、设备、宿主机网络或 PID 命名空间、`cap_add`、`/DATA` 以外的路径、Docker 套接字、低于 1024 或保留的端口、自带子网的网络）；`llama.cpp` 的目录名不是有效的应用 ID。
3. 在通过策略的 95 个中按用途排除：影视下载自动化与索引器（Sonarr、Radarr、Lidarr、Readarr、Prowlarr、Jackett、Bazarr、Sickchill、Medusa、Mylar3、Lazylibrarian、Autobrr 等）、反爬绕过（FlareSolverr）、平台内容下载器（Pinchflat、Downtify、RDTClient）、把服务暴露到公网的隧道（playit-agent）、没有网页界面而向局域网开放端口的数据库（MariaDB、MongoDB、PostgreSQL）、依赖未内置的 Plex/Emby/Jellyfin 的配套工具（Tautulli、EmbyStat、Ombi、Overseerr、Jellyseerr、Petio）、重型 AI 与开发栈，以及使用 `latest`、`main`、`master`、无标签等浮动镜像标签的应用（拉取到的版本会随时间变化）。
4. 运行身份：声明 `PUID`/`PGID` 或 `user` 的应用由渲染器改为应用账号，可以入选。只用镜像默认身份的应用，逐个阅读对应版本的 Dockerfile、入口脚本和镜像配置（本机无法访问 Docker Hub 镜像仓库，Docker Hub 镜像经 Hub 的标签层信息与 explore.ggcr.dev 查看）。按[应用中心规格](../specs/app-center.md#边界与失败)，只有全程以 root 运行、不 chown 数据目录也不降权，或者不挂载任何文件夹的镜像可以不扩展策略直接入选；固定非 root UID 或“root 初始化后降权/chown”属于第三种身份策略。

## 结果

入选 11 个，内置应用增至 37 个：

| 应用 | 分类 | 运行身份依据 | 文件夹 |
|---|---|---|---|
| Deluge | 下载 | `PUID`/`PGID`（linuxserver） | 应用数据；整个共享空间（与已内置的 qBittorrent 相同） |
| PyLoad | 下载 | `PUID`/`PGID`（linuxserver） | 应用数据；共享空间 `Downloads` |
| Resilio Sync | 文件与同步 | `PUID`/`PGID`（linuxserver） | 应用数据；共享空间 `Downloads` |
| Swing Music | 影音 | root，无 USER、无 chown（[Dockerfile](https://github.com/swingmx/swingmusic/blob/v2.1.4/Dockerfile)） | 应用数据；共享空间 `Media/Music` |
| VoceChat | 协作与自动化 | root，入口脚本中的 UID 逻辑已注释（依据镜像层，`v0.5.17` 无公开源码标签） | 应用数据 |
| BentoPDF | 实用工具 | 不挂载文件夹 | 无 |
| ConvertX | 实用工具 | root，无 USER（[Dockerfile](https://github.com/C4illin/ConvertX/blob/v0.17.0/Dockerfile)） | 应用数据 |
| Actual | 实用工具 | root：构建时创建的 `actual` 用户从未用 USER 启用（[Dockerfile](https://github.com/actualbudget/actual/blob/v26.5.2/packages/sync-server/docker/ubuntu.Dockerfile)） | 应用数据 |
| ddns-go | 网络工具 | root，入口即二进制（[Dockerfile](https://github.com/jeessy2/ddns-go/blob/v6.17.1/Dockerfile)） | 应用数据 |
| MySpeed | 网络工具 | root，未设 USER（[Dockerfile](https://github.com/gnmyt/myspeed/blob/v1.0.9/Dockerfile)） | 应用数据 |
| OpenSpeedTest | 网络工具 | UID 101，不挂载文件夹（[Dockerfile](https://github.com/openspeedtest/Docker-Image/blob/main/Dockerfile)） | 无 |

评审后不入选：

| 应用 | 原因 |
|---|---|
| Gopeed、Lyrion Music Server | 入口脚本按镜像内的 `PUID`/`PGID`（默认 0:0 或 499:100）每次启动 chown 数据目录并降权；清单未声明 `PUID`，渲染器无法改写（[Gopeed 入口](https://github.com/GopeedLab/gopeed/blob/v1.9.3/entrypoint.sh)、[Lyrion 入口](https://github.com/LMS-Community/slimserver-platforms/blob/d00b03e55730589cf6bbc4d21759cb49c41aba10/Docker/start-container.sh)） |
| PdfDing、2FAuth、ddns-updater | 固定以 UID 1000 运行，写不了属于应用账号的文件夹（[PdfDing](https://codeberg.org/mrmn/PdfDing/src/tag/v1.7.2/Dockerfile)、[2FAuth](https://github.com/Bubka/2FAuth/blob/v6.1.3/Dockerfile)、[ddns-updater](https://github.com/qdm12/ddns-updater/blob/v2.10.0/Dockerfile)） |
| Wallabag | PHP 进程以 `nobody`（65534）写入，且清单只持久化图片目录、不持久化数据库（[Dockerfile](https://github.com/wallabag/docker/blob/2.6.14/Dockerfile)） |
| Copyparty | CasaOS 构建的镜像在启动脚本中写死可读写的 `casaos`/`casaos` 账号，A-NAS 中无法修改 |
| Obsidian | 网页远程桌面未配置任何登录，局域网内任何人都能进入桌面会话 |
| Beaver Habits | 以固定的“受信任邮箱”免登录访问 |
| NZBGet、SABnzbd、SmokePing | 时区写死为 `Europe/London`；Usenet 需另购服务，国内少用 |

## 结论与待验证

- 本次评审只覆盖新入选的 11 个应用；此前内置的 26 个应用中，除 OpenList 外都没有逐个核对运行身份。2026-10-13 的 Demo 测试中 n8n（镜像固定以 UID 1000 运行）写不了应用文件夹而不断重启，此后 n8n、Grafana 与 PsiTransfer 改为以应用账号运行，安装也会在约 20 秒内发现不能持续运行的容器并回滚（[应用中心规格](../specs/app-center.md#执行安装)）。
- 入选应用的运行身份结论来自源码与镜像配置，尚未实机安装。部署后按[应用中心规格](../specs/app-center.md)逐个安装、打开并核对容器进程 UID 与写入文件的属主后再把本文改为已验证。
- 以 root 运行的应用在它的文件夹里写入 root 属主的文件；共享空间中的文件经继承的 ACL 仍可被成员读写，见[统一身份与 ACL 规格](../specs/unified-identity-and-file-acl.md)。
- PyLoad 与已内置的 FileDrop 都发布 8000 端口，不能同时安装；安装时的端口冲突检查会提示。

## 关联

- 规格：[应用中心](../specs/app-center.md)
- ADR：[0010 内置清单与安装策略](../adr/0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)、[0008 统一 Linux 身份](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)
- 代码：[内置清单](../../internal/appstore/catalog/NOTICE.md)、[分类](../../internal/appstore/categories.go)、[策略测试](../../internal/appstore/catalog_test.go)
