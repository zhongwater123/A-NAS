# 0034：产品以签名软件源中的 Debian 软件包发布，设备以 systemd-sysupdate 双系统分区更新

状态：accepted（2026-11-10，用户在第 3 步的验证之后接受；2026-11-09 提出，2026-11-10 改为 systemd-sysupdate 优先；2026-11-11 第 2 步实施前，用户确认修订决定 1 中的内容包命名、开发版本号、软件源工具、安装逻辑与迁移）；接续 [ADR 0031](0031-declare-host-dependencies-in-debian-packages.md) 决定 6 留下的“产品本身打包”，对应 [#189](https://github.com/zhongwater123/A-NAS/issues/189)、[#171](https://github.com/zhongwater123/A-NAS/issues/171)。

A-NAS 现在的发布靠脚本与人工维护，Experimental NAS 之外也没有可行的更新方式。我们决定分两层：

- 发布的单位改为 Debian 软件包，放在 A-NAS 签名的软件源中；
- 卖到家里的设备，以这些软件包构建整机镜像，用 systemd 自带的工具链在两个系统分区之间更新：mkosi、systemd-repart、systemd-boot 与 systemd-sysupdate。

第 2 步完成前的几个 release 用发布包过渡；发布包已随第 2 步删除，部署见[部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)。

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

### 同类产品与上游的做法

- Home Assistant OS 用 RAUC 更新：更新包以 X.509 证书签名，系统有两个启动分区（[Home Assistant 开发文档](https://developers.home-assistant.io/docs/operating-system/update-system)）。
- TrueNAS SCALE 基于 Debian，却有意锁住 apt：升级是整个系统写成新的启动环境，保留最近两个，出问题时切回旧环境（[TrueNAS 文档](https://www.truenas.com/docs/scale/25.10/scaletutorials/systemsettings/managebootenvironscale/)）。
- GNOME OS 正从 OSTree 迁到 systemd-sysupdate，为此推动了可选组件与可插拔的下载后端（[All Systems Go 2024](https://media.ccc.de/v/all-systems-go-2024-285-gnome-os-systemd-sysupdate)）。上游到 systemd 262 仍没有增量下载：可插拔后端的 [#33351](https://github.com/systemd/systemd/issues/33351) 未关闭；KDE Linux 在 systemd 之外用 desync 做增量（[KDE Linux](https://community.kde.org/KDE_Linux/Delta)）。Flatcar 自 2022 年起以 system extension 发布附加组件，由 systemd-sysupdate 定时检查更新（[Flatcar sysext bakery](https://flatcar.github.io/sysext-bakery)）。
- Debian 对第三方软件源的建议：每个源用 `Signed-By` 只信任自己的密钥，并使用 HTTPS（[Debian Wiki](https://wiki.debian.org/DebianRepository/UseThirdParty)）。

### Debian 13 提供什么

2026-11-09 与 11-10 在系统测试的 Debian 13 镜像中核对：

- systemd 的工具链（257）：
  - `systemd-sysupdate` 在 `systemd-container` 软件包中，带 `systemd-sysupdate.timer`、`systemd-sysupdate-reboot.timer` 与下载用的 `systemd-pull`；
  - `systemd-repart` 与 `systemd-boot` 各有软件包；
  - 镜像构建工具 `mkosi` 25.3 也来自 systemd 项目。
- 其他更新工具：`rauc` 1.13、`swupdate` 2024.12、`mender-client` 3.4、`ostree` 2025.2；镜像构建另有 `debos` 1.1.5。
- 软件源工具：`reprepro`、`aptly` 1.6.1、`apt-utils` 3.0.3。

systemd-sysupdate 的工作方式（[`sysupdate.d`](https://www.freedesktop.org/software/systemd/man/latest/sysupdate.d.html) 与 [`systemd-sysupdate`](https://www.freedesktop.org/software/systemd/man/systemd-sysupdate.html) 手册）：

- 按版本号，从 HTTPS 下载分区镜像、文件或目录，写进未在使用的分区或位置；
- 以 `SHA256SUMS` 与它的 GPG 签名校验，下载的文件一律按清单中的 SHA-256 核对；
- 内核镜像的文件名带 systemd-boot 启动计数（启动尝试的剩余与已用次数），没有通过启动检查的新版本由 systemd-boot 退回上一个版本（[Automatic Boot Assessment](https://systemd.io/AUTOMATIC_BOOT_ASSESSMENT)）。

上游最新手册（systemd 262）仍标注它“处于实验阶段，可能随时变化，将在以后的版本中稳定并保证向后兼容”。

## 决定

1. **发布的单位是 Debian 软件包，放在 A-NAS 签名的软件源中。**
   - 软件包：
     - `a-nas`：产品的二进制、单元、安装逻辑、AI Worker 的代码、网页搜索的配置与容器代理；
     - 内容包：每个模型、两个运行环境、Qdrant 与 llama.cpp 各一个。包名带内容的标识（模型与 Qdrant、llama.cpp 取 SHA-256 的前 12 位，运行环境取它的 ID），版本固定为 1，例如 `a-nas-model-dfine-m-obj365-cd832825f883`、`a-nas-ai-runtime-842b388a0f1987f5`。`a-nas` 依赖它固定的这些名字，所以内容不变时 apt 不下载；回滚时旧内容如果还装着，只降产品包。内容仍放在 `/opt/a-nas/<种类>/<标识>`，旧的由 `apt autoremove` 删除（[`scripts/lib/content.sh`](../../scripts/lib/content.sh)）；
     - `a-nas-assistant-trial`：助手试用包，`Depends` 同一版本的 `a-nas`，所以两者总是一起更新。它的 Hermes 运行环境也是内容包，固定在 [`ai/assistant-spike/runtime.json`](../../ai/assistant-spike/runtime.json)；
     - 已有的 `a-nas-host` 与 `a-nas-console`（ADR 0031），以及装有签名公钥的 `a-nas-archive-keyring`。
     - `a-nas-host` 依赖的 Docker 与 Caddy：上游官方软件包的副本，按 SHA-256 固定（[ADR 0015](0015-take-docker-and-caddy-from-their-upstream-repositories.md)，2026-11-12）。
     - `a-nas-docker-policy`：Docker 的地址池校验与 systemd 附加配置。`a-nas-host` 要求不低于本 release 的版本，产品回滚时与 Docker 一样保留；安装命令显式选择它，避免旧宿主机包不再依赖它后被 `apt autoremove` 清理（[ADR 0013](0013-allocate-docker-networks-from-an-a-nas-address-pool.md)）。
   - 版本：每个 release 打标签，Debian 版本由标签得出，例如 `1.0.1~rc7`、`1.0.2`；两个 release 之间的开发构建写作 `1.0.2~dev<日期>.<序号>+g<提交>`，排在该 release 之前；预发布标签之后的开发构建写作 `1.0.1~rc.6+dev<日期>.<序号>+g<提交>`，排在 rc.6 与 rc.7 之间，前一种写法会排在 rc.6 之前。序号是标签之后第一父提交的个数，所以沿主干只增不减。不再用 `0~<提交>`（[`scripts/release-version.sh`](../../scripts/release-version.sh)）。
   - 软件源：由 aptly 生成，用 Debian 13 的软件包，在系统测试的镜像中运行。aptly 的 Go 模块缺少它嵌入的 `VERSION` 文件，不能用 `go tool` 固定版本；Debian 13 的 reprepro 5.3.1 不认 `Limit`，每个发布只留一个版本。软件源保留每个版本，回滚要用，`Release` 以 A-NAS 的密钥签名（[`scripts/apt-repository.sh`](../../scripts/apt-repository.sh)）。宿主机以 deb822 的 `.sources` 文件引用它，`Signed-By` 指向 `a-nas-archive-keyring` 的公钥。签名代替逐个文件交接哈希：不论软件源经谁复制、放在哪里，apt 都会拒绝被改过的文件，暂存账号不再在信任链上。
   - 安装逻辑：`a-nas` 的文件只在 `/usr`，程序在 `/usr/lib/a-nas`；单元在 `/usr/lib/systemd/system`，以包内的 `.wants` 链接启用；账号与目录用 `sysusers.d` 与 `tmpfiles.d`。按宿主机而定的配置（SMB 接口、本机屏幕的输出与旋转、终端与应用中心的开关）放在管理员所有的 `/etc/a-nas/site.conf`，不随软件包覆盖；systemd 生成器 `a-nas-generator` 与开机运行的 `anas-site.service` 按它与硬件生成 smbd 等的配置，结果不依赖维护脚本。维护脚本只做 apt 宿主机才需要的事：升级前停下服务并复制数据库、按顺序重启、运行 `a-nas-check`，以及从安装器布局的一次性接管。安装器直接删除，没有经过“维护脚本调用安装器”的过渡：那要先把它改成从 `/usr` 读文件，之后再拆一次。
   - 迁移：每次数据库结构的变化都带反向步骤。执行一步时，它的反向步骤在同一事务中写进数据库；旧版本启动时发现数据库比自己新，就按新版本留下的反向步骤退回自己认得的结构（[`internal/sqlitedb`](../../internal/sqlitedb/migrate.go)）。`prerm` 不知道旧版本认得哪个结构，systemd-boot 自动退回与换回旧的 system extension 时也不运行维护脚本，所以退回放在程序里。已发布之后的步骤与每个反向步骤按 SQLite 改表结构的做法执行：在连接上关闭外键，使重建被引用的表时 `ON DELETE CASCADE` 不删掉引用它的行，提交前核对没有多出指向不存在行的引用。`preinst` 在升级前复制数据库：系统盘的到 `/var/backups/a-nas`，数据卷上的留在数据卷的 `.a-nas-backups`，各保留最近两份；设备上由更新服务在切换前复制（第 4 步）。
   - 门禁：`make release` 构建全部软件包与签名软件源，只发布 `make check` 与系统测试通过的那一份。系统测试用同样的脚本做一个签名软件源，从中用 apt 安装，并走一遍“上一 release → 本 release → 回滚 → 再升级”；其中还装一个多了一步数据库迁移的“下一 release”，再回滚，核对数据库按反向步骤退回。
   - 命令：升级是 `apt-get update && apt-get install a-nas=<版本> a-nas-host=<版本>`（产品的几个包写同一版本，apt 不会自己降级依赖），回滚是同一条命令加 `--allow-downgrades` 与旧版本号，检查用 `a-nas` 带的只读健康检查 `a-nas-check`。它们代替每轮手写的脚本（[部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)）。
2. **家里的设备以整机镜像更新，工具链全部来自 systemd 项目，并且都在 Debian 13 中。**
   - 镜像：mkosi 从 Debian 13 与第 1 条的软件包构建，所以测试机与家里的设备装的是同一批软件包。`a-nas-host`、`a-nas-console` 等依赖装进基础镜像。
   - 分区：systemd-repart 建两个只读的 `/usr` 槽位与一个持久的根分区。`/etc` 与 `/var`（含 `/var/lib/a-nas`）在持久的根分区上，用户数据在数据卷上，切换 `/usr` 槽位不动它们。
   - 启动：systemd-boot 加载统一内核镜像，并按启动计数判断新版本。新版本启动后通过健康检查（`boot-complete.target`）才算成功，否则 systemd-boot 退回上一个版本。健康检查失败时重启（`FailureAction=reboot`），内核崩溃后也自动重启（`panic=`）；内核卡死要靠硬件看门狗。
   - 更新：systemd-sysupdate 从 HTTPS 下载带版本号的 `/usr` 镜像与内核镜像，以 `SHA256SUMS` 及其 GPG 签名校验，写进未在使用的槽位。
   - 产品层（`a-nas` 与助手试用包）做成 system extension，由 systemd-sysupdate 的 `product` 组件单独更新，不重启设备。模型等很少变化的内容也各自作为组件，只在变化时下载。基础镜像只在 Debian 或依赖变化时发布。
   - 产品中的更新服务（经 Host Agent）调用 `systemd-sysupdate --json=short` 检查、下载与安装更新，并决定何时重启，在“设置”中显示更新状态。systemd-sysupdate 自带的两个定时器在镜像中禁用。apt 在设备上只读，同 TrueNAS 的做法。
   - systemd-sysupdate 在上游仍是实验组件。第 3 步已证明 Debian 13 的 systemd 257 满足要求，有两处缺陷要在镜像中绕开，见“第 3 步的验证结论”。
   - Experimental NAS 这类由人装好 Debian 的宿主机，继续用第 1 条的 apt 更新；改用镜像要重装系统盘，时间由用户决定。

## 实施计划

1. **过渡（本 ADR 之前已做）**：发布包，用于第 2 步完成前的几个 release，已随第 2 步删除。
2. **软件包与签名软件源**：在一个 PR 中按步骤分提交完成，内容如下。合并后，部署机只需复制软件源并执行 apt 命令，发布包与 `remote-activate-release.sh`、每轮的 root 脚本以及 `make ops-check` 中核对上传清单的 grep 一并删除。
   - 第 1 条的软件包与 `make release`，布局满足“第 3 步的验证结论”中 system extension 的约束；
   - 版本规则；
   - 维护脚本调用安装器；
   - `site.conf`；
   - 带反向步骤的迁移；
   - 从软件源安装的系统测试；
   - 部署机的手册。

   2026-11-11 完成：[部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)。正式签名密钥与软件源的位置待用户决定（[签名密钥](../decisions/2026-11-11-release-signing-key.md)、[放在哪里](../decisions/2026-11-11-update-hosting.md)）。
3. **镜像与 systemd-sysupdate 的验证（2026-11-10 完成）**：在虚拟机中用 mkosi 构建镜像，以 Debian 13 的 systemd 257 验证自动退回、断电、数据不变、下载量、产品层做成 system extension，以及是否需要更新的 systemd。结论见下一节，做法与数据见[研究记录](../research/sysupdate-image-verification.md)。
4. **更新服务与界面**：Host Agent 的更新服务与“设置”中的更新页，见[系统更新规格](../specs/system-update.md)。2026-11-13 软件包设备的部分完成：独立的 `anas-update` 单元由 root 同步开发机推送的软件源、检查、下载、安装、运行 `a-nas-check` 与回退，Host Agent 只留下请求、读取状态；镜像设备随第 5 步接入。
5. **设备镜像**：把验证用的 mkosi 配置（[`deploy/sysupdate-spike/`](../../deploy/sysupdate-spike/)）改成正式的设备镜像：从第 2 步的软件源装软件包，加上下一节的两处绕法、禁用 sysupdate 的定时器、正式的签名密钥与更新地址。2026-11-13 第一部分完成：镜像与签名的更新文件（`make image`），在虚拟机中按设备的做法更新的测试（`make image-test`），[手册](../runbooks/build-device-image.md)；镜像设备接入“设置 → 系统更新”是第二部分。
6. **Experimental NAS 改用镜像**：用户决定时间后重装系统盘。数据卷在另外的硬盘上，不受影响。

## 第 3 步的验证结论（2026-11-10）

继续用 systemd-sysupdate，不改用 RAUC。六项在 Debian 13 的 systemd 257 上都通过，设备不需要更新的 systemd。做法、数据与复现见[研究记录](../research/sysupdate-image-verification.md)。

- **自动退回**：健康检查失败的版本和内核崩溃的版本各试启动 3 次，之后 systemd-boot 选回上一版本；正常的新版本在健康检查通过后被标为成功。
- **断电**：在写 `/usr` 槽位、下载内核镜像的过程中杀掉虚拟机，重启后仍从旧版本启动，写了一半的槽位保持空闲，之后的更新正常完成。
- **数据**：持久根分区上的 `/var/lib/a-nas`、`/etc/a-nas` 与数据卷，在每次切换与断电后都不变。
- **下载量**：
  - 一次系统更新下载 748 MiB（`/usr` 镜像 612 MiB + 内核镜像 136 MiB），每个版本都是全量：上游到 262 没有增量下载。
  - 没有新版本时只取 282 字节的清单与签名；模型作为单独组件，不变时不下载。
- **产品层做成 system extension**：可行，不重启即可更新。257 有两处缺陷要绕开：
  1. sysext 合并后，`/usr` 的 transfer 用 `Path=auto` 找不到磁盘（258 修复），改为由开机单元用 `bootctl -RR` 创建磁盘链接；
  2. sysext 中的服务开机不启动、sysusers 账号不建，加一个排在 systemd-sysext 之后、执行 `systemd-sysusers` 与 `daemon-reload` 并重新拉起目标的单元，同 Flatcar 的做法。
- **软件包的约束**（第 2 步的输入）：
  - 文件只在 `/usr` 与 `/opt`；单元在 `/usr/lib/systemd/system`，以软件包自带的 `.wants` 链接启用；
  - 账号与目录用 `sysusers.d` 与 `tmpfiles.d`；
  - 默认配置放 `/usr`，`/etc/a-nas/site.conf` 只放管理员的值；
  - 不依赖维护脚本的副作用；
  - 产品依赖的 Debian 软件包放进基础镜像，新增依赖先发基础镜像。
- **配置上的注意事项**：
  - 回退后 systemd-sysupdate 仍把失败的版本当作已安装的最新版本，`systemd-sysupdate reboot` 会重启进去再被退回，所以禁用 `systemd-sysupdate-reboot.timer` 与 `systemd-sysupdate.timer`，由产品的更新服务决定何时更新与重启；
  - Debian 13 没有打包 `systemd-sysupdated` 与 `updatectl`，更新服务调用 `systemd-sysupdate --json=short`。
- **更新的 systemd**：
  - 258 修复缺陷 1；
  - 260 起能把下载与安装分开，并标记下载到一半的分区；
  - 261 起 sysext 可在 initrd 中合并，缺陷 2 随之消失；
  - 262 改了单元名，重新标为实验组件，并预告还有不兼容的改动。

  在 Debian 13 上用 257 与两处绕法；换到 Debian 14 的 systemd 时重跑第 3 步的验证，去掉不再需要的绕法。

## 后果

- 每个 release 都要打标签，CHANGELOG 按 release 分节。
- 多了要保管的签名密钥：软件源与 systemd-sysupdate 的 `SHA256SUMS` 都用 GPG 签名，可以共用一套保管、备份与轮换办法，由用户决定（[决定记录](../decisions/2026-11-11-release-signing-key.md)）。
- 联网的设备需要一个 HTTPS 位置放软件源与更新文件，例如 GitHub Releases 或对象存储，放在哪里另行决定（[决定记录](../decisions/2026-11-11-update-hosting.md)）。ADR 0016 不变：模型随软件包、镜像与签名的更新组件发出，设备运行时不联网下载模型。
- 开发迭代：测试机加上开发构建的软件源，用 apt 装任意构建；镜像只在发布给设备时构建。
- 依赖上游的实验组件：systemd-sysupdate 的配置格式可能随 systemd 版本变化，升级设备上的 systemd 时要重跑第 3 步的验证。
- 每次发布基础镜像，每台设备下载约 750 MiB；产品放在 system extension 中，产品更新只下载它自己。只把设备需要的内核模块放进 initrd，内核镜像可从 136 MiB 降到 50 MiB 左右。
- 设备镜像要带两处针对 systemd 257 的绕法，并禁用 sysupdate 的定时器。
- 设备镜像做好之前，家里的设备没有自动退回，只能靠 apt 降级。

## 未选择

- **继续用发布包与每轮脚本**：即现在的过渡做法。没有签名、版本与固定的回滚命令，靠人维护，用户要求取代。
- **设备也只用 apt 更新（例如 `unattended-upgrades`）**：没有整机的原子回滚。升级中途断电会留下未配置完的软件包；各台设备的系统软件包会逐渐不同。TrueNAS 正是为此锁住了 apt。
- **RAUC**（第 3 步通过，不采用；systemd-sysupdate 将来满足不了要求时的备选）：
  - 长处：成熟；Home Assistant OS 在用；1.7 起能经 HTTPS 边下载边安装，1.8 起按块比对、只下载变化的块（[RAUC 的变更记录](https://rauc.readthedocs.io/en/v1.13/changes.html)）；Debian 13 有 1.13。
  - 不作首选，是因为它在 systemd 之外另有一套更新包格式、证书与启动器接入，而 systemd-sysupdate、systemd-repart、systemd-boot 与 mkosi 是同一个项目的一套工具，Debian 13 都已带上。
- **OSTree 或 bootc**：Debian 有 `ostree` 软件包，但没有把 Debian 系统作为可启动部署的集成，这条路以 Fedora 为主，集成成本最高；GNOME OS 也正从 OSTree 迁走。
- **Mender**：Debian 的 `mender-client` 停在 3.4，完整的价值要配合 Mender 的服务端（托管或自建），超出我们的需要。
- **SWUpdate**：能力与 RAUC 相当，同样在 systemd 之外，理由同 RAUC。
- **产品做成容器镜像**：Host Agent 以 root 运行，要管理 Samba、systemd 单元、磁盘与本机屏幕，不适合放进容器。

## 关联

- [ADR 0031：宿主机依赖由 A-NAS 自己的 Debian 软件包声明](0031-declare-host-dependencies-in-debian-packages.md)（决定 6）
- [ADR 0016：相册 AI 随系统内置安装](0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- [ADR 0014：屏保视频不随 release 发布](0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)
- [设备镜像与 systemd-sysupdate 的验证](../research/sysupdate-image-verification.md)（第 3 步）、[验证用的镜像配置与驱动脚本](../../deploy/sysupdate-spike/verify.sh)
- [部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)、[配置并验收 v1.0.1 实验数据卷](../runbooks/provision-v1.0.1-experimental-storage.md)
- [#189](https://github.com/zhongwater123/A-NAS/issues/189)、[#171](https://github.com/zhongwater123/A-NAS/issues/171)
