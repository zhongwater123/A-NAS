# 0034：产品以签名软件源中的 Debian 软件包发布，设备以 RAUC 双系统分区更新

状态：proposed（2026-11-09）；接续 [ADR 0031](0031-declare-host-dependencies-in-debian-packages.md) 决定 6 留下的“产品本身打包”，对应 [#189](https://github.com/zhongwater123/A-NAS/issues/189)、[#171](https://github.com/zhongwater123/A-NAS/issues/171)。

A-NAS 现在的发布靠脚本与人工维护，Experimental NAS 之外也没有可行的更新方式。我们决定分两层：

- 发布的单位改为 Debian 软件包，放在 A-NAS 签名的软件源中；
- 卖到家里的设备，以这些软件包构建整机镜像，用 RAUC 在两个系统分区之间更新。

近期的几个 release 仍用发布包过渡（[用发布包暂存 release](../runbooks/stage-a-release-bundle.md)）。

## 背景

### 现在怎么发布

| 环节 | 做法 | 问题 |
|---|---|---|
| 构建 | `deploy-dev.ps1` 要求仓库、WSL 与全套工具，在开发机上运行 | 只有这台开发机能发布 |
| 传送 | 经 SSH 逐个上传约 60 个文件与 4.3 GB 内容，再由 `remote-activate-release.sh` 在 NAS 上拼成 release 目录 | 文件清单在脚本、安装器、系统测试与手册中手工同步（#189）；2026-11-05 一处路径被控制字符弄坏，主干暂存失败了 4 天才被发现 |
| 校验 | 开发者给出 API、Host Agent 与两个宿主机软件包的哈希，root 逐个核对 | 安装器、单元与脚本没有签名；暂存账号能改动 root 运行的文件（#171） |
| 版本 | 未打标签的提交以 `0~<提交>` 部署 | 版本不能排序，升级在 apt 看来可能是降级（ADR 0031） |
| 安装与回滚 | 开发者每轮手写 root 的部署、回滚与重启后检查脚本，包括数据库的反向迁移，并在副本上演练 | 每轮都靠人；容器代理与助手试用包在 release 之外，各自更新 |

2026-11-09 用户的要求：Experimental NAS 将迁到另一个能上网的网络，由另一台开发机部署；发布与更新要足够规范，不能靠人工一直维护；后续需要官方推荐的发布形式与 OTA 方法，以便迭代。产品方向是先做好家庭局域网，设备放在用户家里。

### 发布与更新要做到什么

1. 一条命令从打了标签的提交构建出签名、带版本的全部产物；门禁测试的就是要发出去的那一份。
2. 升级、回滚与检查都是固定的命令，不需要每轮手写的脚本。
3. 数据库迁移能随回滚撤回，或至少在升级前留下可恢复的副本。
4. 只下载变化的部分：4.3 GB 的模型与运行环境很少变化。
5. 放在家里的设备：升级要么完整生效、要么不生效；新版本启动失败或健康检查不过时自动退回；中途断电不会让设备无法启动。
6. 开发迭代时，能把一个构建直接装到测试机上。

### 同类产品与 Debian 的做法

- Home Assistant OS 用 RAUC 更新：更新包以 X.509 证书签名并校验，系统有两个启动分区（[Home Assistant 开发文档](https://developers.home-assistant.io/docs/operating-system/update-system)）。
- TrueNAS SCALE 基于 Debian，却有意锁住 apt：升级是整个系统写成新的启动环境，保留最近两个，出问题时切回旧环境（[TrueNAS 文档](https://www.truenas.com/docs/scale/25.10/scaletutorials/systemsettings/managebootenvironscale/)）。
- Debian 对第三方软件源的建议：每个源用 `Signed-By` 只信任自己的密钥，并使用 HTTPS（[Debian Wiki](https://wiki.debian.org/DebianRepository/UseThirdParty)）。

2026-11-09 在系统测试的 Debian 13 镜像中核对过可用的软件包：

- 更新：`rauc` 1.13、`swupdate` 2024.12、`mender-client` 3.4、`ostree` 2025.2。
- 镜像构建：`mkosi` 25.3、`debos` 1.1.5。
- 软件源：`reprepro`、`aptly` 1.6.1、`apt-utils` 3.0.3。
- 启动：`systemd-boot`、`systemd-repart` 257。
- Debian 13 的 systemd 不带 `systemd-sysupdate`。

RAUC 从 1.7 起能经 HTTPS 边下载边安装，1.8 起支持按块比对、只下载变化的块（adaptive update，见[RAUC 的变更记录](https://rauc.readthedocs.io/en/v1.13/changes.html)）。

## 决定

1. **发布的单位是 Debian 软件包，放在 A-NAS 签名的软件源中。**
   - 软件包：
     - `a-nas`：产品的二进制、单元、安装逻辑、AI Worker 的代码、网页搜索的配置与容器代理；
     - 内容包：每个模型与运行环境各一个，例如 `a-nas-model-<名称>`、`a-nas-ai-runtime`、`a-nas-search-runtime`、`a-nas-qdrant`、`a-nas-llama`。内容不变时版本不变，apt 只下载变化了的；
     - `a-nas-assistant-trial`：助手试用包，`Depends` 同一版本的 `a-nas`，所以两者总是一起更新；
     - 已有的 `a-nas-host` 与 `a-nas-console`（ADR 0031），以及装有签名公钥的 `a-nas-archive-keyring`。
   - 版本：每个 release 打标签，Debian 版本由标签得出，例如 `1.0.1~rc7`、`1.0.2`；两个 release 之间的开发构建写作 `1.0.2~dev<日期>.<序号>+g<提交>`，排在该 release 之前。不再用 `0~<提交>`。
   - 软件源：由 reprepro 或 aptly 生成，`Release` 以 A-NAS 的密钥签名。宿主机以 deb822 的 `.sources` 文件引用它，`Signed-By` 指向 `a-nas-archive-keyring` 的公钥。签名代替逐个文件交接哈希：不论软件源经谁复制、放在哪里，apt 都会拒绝被改过的文件，暂存账号不再在信任链上。
   - 安装逻辑：`a-nas` 的维护脚本先调用现有的安装器，再逐步改成声明式的文件，例如 `/usr/lib/systemd/system` 中的单元、`sysusers.d` 与 `tmpfiles.d`。按宿主机而定的配置（SMB 接口、本机屏幕的输出与旋转）放在管理员所有的 `/etc/a-nas/site.conf`，不随软件包覆盖。
   - 迁移：每次数据库结构的变化都带反向步骤。apt 降级时，新版本的 `prerm` 先把数据库退回旧版本认得的结构；`preinst` 在升级前备份数据库。
   - 门禁：`make release` 构建全部软件包与签名软件源。系统测试从这个软件源用 apt 安装，并走一遍“上一 release → 本 release → 回滚 → 再升级”。
   - 命令：升级是 `apt-get update && apt-get install a-nas=<版本>`，回滚是同一条命令加 `--allow-downgrades` 与旧版本号，检查用 `a-nas` 带的只读健康检查命令。它们代替每轮手写的脚本。
2. **家里的设备以整机镜像加 RAUC 更新。**
   - 镜像：以 mkosi 从 Debian 13 与第 1 条的软件包构建，所以开发机上的测试机与家里的设备装的是同一批软件包。
   - 分区：两个系统分区与一个数据分区。用户数据在数据卷上，`/var/lib/a-nas` 等状态在数据分区上，切换系统分区不动它们。
   - 更新：RAUC 把签名的更新包写进未在使用的系统分区，经 HTTPS 边下载边安装，并只下载变化的块。启动器计数启动次数：新系统启动并通过健康检查后才标为可用，否则下次启动退回原分区。
   - 产品中的更新服务（经 Host Agent）负责检查、下载与安装更新，在“设置”中显示更新状态；apt 在设备上只读，同 TrueNAS 的做法。
   - Experimental NAS 这类由人装好 Debian 的宿主机，继续用第 1 条的 apt 更新；改用镜像要重装系统盘，时间由用户决定。

## 实施计划

1. **过渡（本 ADR 之前已做）**：发布包，见[用发布包暂存 release](../runbooks/stage-a-release-bundle.md)，用于近期几个 release。
2. **软件包与签名软件源**：在一个 PR 中按步骤分提交完成，内容如下。合并后，部署机只需复制软件源并执行 apt 命令，发布包与 `remote-activate-release.sh`、每轮的 root 脚本以及 `make ops-check` 中核对上传清单的 grep 一并删除。
   - 第 1 条的软件包与 `make release`；
   - 版本规则；
   - 维护脚本调用安装器；
   - `site.conf`；
   - 带反向步骤的迁移；
   - 从软件源安装的系统测试；
   - 部署机的手册。
3. **镜像与 RAUC 的验证**：在虚拟机中用 mkosi 构建镜像，验证以下几项，结论写回本 ADR：
   - 镜像大小与只下载变化块时的下载量；
   - 新版本启动失败时自动退回；
   - 安装中途断电后仍能启动；
   - 数据分区与数据卷在切换后不变。
4. **更新服务与界面**：Host Agent 的更新服务与“设置”中的更新页，另有规格。
5. **Experimental NAS 改用镜像**：用户决定时间后重装系统盘。数据卷在另外的硬盘上，不受影响。

## 后果

- 每个 release 都要打标签，CHANGELOG 按 release 分节。
- 多了两把要保管的密钥：软件源的签名密钥与 RAUC 的证书。保管、备份与轮换由用户决定，写在决定记录中。
- 联网的设备需要一个 HTTPS 位置放软件源与更新包，例如 GitHub Releases 或对象存储，放在哪里另行决定。ADR 0016 不变：模型随软件包与镜像发出，设备运行时不联网下载模型。
- 开发迭代：测试机加上开发构建的软件源，用 apt 装任意构建；镜像只在发布给设备时构建。
- 第 3 步之前，家里的设备没有自动退回，只能靠 apt 降级。

## 未选择

- **继续用发布包与每轮脚本**：即现在的过渡做法。没有签名、版本与固定的回滚命令，靠人维护，用户要求取代。
- **设备也只用 apt 更新（例如 `unattended-upgrades`）**：没有整机的原子回滚。升级中途断电会留下未配置完的软件包；各台设备的系统软件包会逐渐不同。TrueNAS 正是为此锁住了 apt。
- **systemd-sysupdate 与 system extension**：是 systemd 自己的镜像更新方式，但 Debian 13 的 systemd 不带 `systemd-sysupdate`，要自己构建 systemd。
- **OSTree 或 bootc**：Debian 有 `ostree` 软件包，但没有把 Debian 系统作为可启动部署的集成，这条路以 Fedora 为主，集成成本最高。
- **Mender**：Debian 的 `mender-client` 停在 3.4，完整的价值要配合 Mender 的服务端（托管或自建），超出我们的需要；RAUC 配一个 HTTPS 位置就够。
- **SWUpdate**：能力与 RAUC 相当。选 RAUC，是因为有面向家庭设备的成熟先例（Home Assistant OS），配置更简单，Debian 13 的版本也够新。
- **产品做成容器镜像**：Host Agent 以 root 运行，要管理 Samba、systemd 单元、磁盘与本机屏幕，不适合放进容器。

## 关联

- [ADR 0031：宿主机依赖由 A-NAS 自己的 Debian 软件包声明](0031-declare-host-dependencies-in-debian-packages.md)（决定 6）
- [ADR 0016：相册 AI 随系统内置安装](0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- [ADR 0014：屏保视频不随 release 发布](0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)
- [用发布包暂存 release](../runbooks/stage-a-release-bundle.md)、[配置并验收 v1.0.1 实验数据卷](../runbooks/provision-v1.0.1-experimental-storage.md)
- [#189](https://github.com/zhongwater123/A-NAS/issues/189)、[#171](https://github.com/zhongwater123/A-NAS/issues/171)
