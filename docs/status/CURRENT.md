# 当前状态

更新时间：2026-10-10

## 当前阶段

v1.0.1“实验 NAS 基础存储与共享闭环”是当前主线。Experimental NAS 于 2026-10-10 部署主干提交 `6833c7637f73`，在 PR #50 的 `9923c5987c0b` 之上包含相册 M2 步骤 1–7 与随系统安装的本地 AI（#58，[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）、#53 的屏保视频池、#56 的 Docker 版本与挂载检查、#60 合并四个桌面应用而成的“设置”窗口，以及 #61 的相册界面重做与精确搜索。PR #50 包含文件管理三栏布局、可调栏宽、列表/图标视图、文件夹优先排序、成熟图标工具栏、当前目录上传入口、真实图片缩略图与自适应预览，以及双击下载、框选多选和右键编辑菜单：系统服务与 Kiosk 指向同一 release，Host Agent 启动自检记录 `file broker identity switch verified`，相册服务就绪，Caddy 在 80 端口提供局域网入口，产品服务仍只监听 `127.0.0.1:8080`。Docker 与应用中心的实时代理模式自 2026-10-08 起启用。OpenList 已按 `app-openlist` 的 UID/GID 30002 重建，状态 running、重启计数 0、HTTP 返回 200。ADR 0008 旧账号已按手册删除并重建为 UID/GID 20100，固定组为 GID 20000/20001，身份注册表已镜像到数据卷；`admin` 可以穿过但不能列出数据卷根并访问自己的个人空间，Product Service 账号 `a-nas` 被内核拒绝，遗留的 `a-nas-members` 组已不存在。新文件管理界面的用户级实机验收（布局交互、上传、删除、恢复）尚未执行。

升级中发现并修复了三个连续阻塞：身份同步前把不存在账号写入 ACL 导致 Host Agent 启动死锁、`build-binaries` 未先生成嵌入式 Web UI、数据卷挂载点缺少 `a-nas-users:--x`。证据与最小回归见 [ADR 0008 首次升级调查](../investigations/2026-10-08-adr0008-bootstrap-private-acl.md)。容器部署又暴露出 Debian 13 将 CLI 拆为独立推荐包，以及部署脚本误用 `/screensaver.mp4`、`/v1/containers` 两个路由；集中式只读探针修正为 `/local-console/screensaver.mp4`、`/v1/snapshot` 后已完成实机切换，详见[容器部署调查](../investigations/2026-10-08-experimental-nas-containers-disabled.md)。旧 Samba 凭据已按手册删除，需要管理员通过已部署的自助改密入口重建后才能开始 Windows SMB 双向验收。

提交 `4e98e86b77c5` 已重新冻结为 `v1.0.0`。相册（[issue #23](https://github.com/zhongwater123/A-NAS/issues/23)）已由 [ADR 0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md) 确定专用服务身份、数据卷上的 Catalog 与按范围授权的管理员查看，并拆为 M1 基础相册、M2 本地 AI 检索与 M3 扩展三个里程碑；部署与实机验收排在基础存储闭环之后。

## 已就绪

- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)、[存储架构](../architecture/storage-and-files.md)、[ADR 0007](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)和 [v1.0.1 发布记录](../releases/v1.0.1.md)已建立。
- 磁盘状态包含稳定 ID、removable/in-use、文件系统、SMART、温度、数据卷资格及拒绝原因；公开接口不返回设备路径。
- 存储计划持久化于 SQLite，支持十分钟过期、确认短语、执行前身份复核、幂等成功结果和中断后 `needs_attention`。
- Linux 执行器使用固定命令创建 GPT、单盘 Btrfs、UUID mount unit、空间子卷和卷身份标记；文件服务在缺少真实 Btrfs/标记时拒绝写入。
- 本机设备启用、Argon2id、服务端会话、CSRF、管理员/成员、个人空间和唯一 Shared Policy 已实现；首次启用只要求账号和密码。
- Web 文件管理、Samba、回收站、手动快照、审计、产品 API 和 React 页面已实现并有自动化覆盖。
- Web 桌面终端已本地实现：仅管理员可通过回环同源 WebSocket 打开 PTY Shell，生产中由文件代理以该管理员本人的 Linux 账号运行，默认关闭，见[终端规格](../specs/web-terminal.md)；Experimental NAS 尚未启用。
- Web 桌面已整合 CPU/内存/网速状态栏、动态图标程序坞、可持久化图标排序、4:3 深蓝抽象壁纸和 [CRT 风格开机动画](../specs/boot-ident.md)；左侧[系统快捷栏](../specs/system-rail.md)只保留可用操作：可恢复窗口的“显示桌面”、可搜索的“全部应用”启动器、设置和收纳退出登录的账号菜单（尚未部署）；拖拽预览已移出滚动网格以避免右侧裁剪。本地控制台还会在登录后闲置三分钟播放外部静音屏保。视频池为每次进入屏保时随机选择一条并持续循环、退出后下次闲置重新选择、单条失败补选，直连屏幕上的轮换行为尚未核对。按 [ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)（[issue #49](https://github.com/zhongwater123/A-NAS/issues/49)），屏保视频已改为系统盘上的长期媒体，不再随 release 发布：每个视频按 SHA-256 只存一份，产品服务固定读取 `/var/lib/a-nas/screensavers/current`，更换视频走独立的暂存与安装命令，只上传缺少的视频。该机制随 `173cbb5499ce` 部署：2026-10-10 安装器把原五条视频导入为池 `53e12079d8d50c4c`，按 release 存放的旧副本已清理（释放约 3.3G），单视频时期遗留的 `computer-chip.mp4` 经暂存命令加入，当前池 `68299080c557c520` 共六条，经局域网入口请求清单与每条视频的 Range 均正常。2026-10-08 已在 `ed5368ba998e` 上验收 API、Host Agent、Kiosk、嵌入式桌面和旧兼容路由的 Range 请求，开机动画随 `946638851c7c` 部署。
- Web 相册窗口已按[相册界面规格](../specs/photo-gallery-ui.md)重做：以照片为中心的等高行时间线、年月时间轴与按月载入、连续缩放、框选与拖动勾选的批量操作、拖到相册、即时显示的上传、可缩放与幻灯片的沉浸式查看器、AI 搜图页和本地 AI 进度；新增按月时间线只读接口。已随 `6833c7637f73` 部署到实验 NAS，本地 AI 已在运行；之后合并的 #63（界面不再说 AI 关闭或不可用）随下一次部署生效。2026-10-10 用户决定搜索回归“自然语言 → 向量 → 排序”（[#66](https://github.com/zhongwater123/A-NAS/issues/66)）：AI 标签暂停（照片详情、按标签浏览与过滤、AI 标签汇总接口都已移除，已有 AI 纠错保留），搜索不再按标签过滤或截断为 20 张，结果分为“最接近”与分隔线后的“相关度较低”两段，AI 搜图页改为“可以这样搜”（[规格](../specs/photo-library.md)、[实施方案](../architecture/photo-ai.md#语义搜索)）；尚未部署。人物识别（M3 切片 13）的方案 [#64](https://github.com/zhongwater123/A-NAS/pull/64) 按用户决定暂缓。新相册界面的实机用户验收尚未执行。
- root Host Agent 与非特权产品服务通过 `root:a-nas 0660` UDS 通信；系统单元使用 root 所有的 `/opt/a-nas/current` 发布目录。
- rc.4 让空盘计划稳定输出数组、兼容旧 `null`、显示存储操作进度并为桌面窗口增加错误边界，见[蓝屏调查](../investigations/2026-10-07-blank-disk-plan-ui-crash.md)。
- rc.4 使用 root 管理的 Chromium policy 禁止保存密码、通行密钥和同步；更广的 Kiosk 约束仍见[开放调查](../investigations/2026-10-06-kiosk-browser-confinement.md)。
- rc.4 已创建 `/dev/sda1` Btrfs 数据卷（UUID `09e275fe-794a-458d-8200-b6e67c55cc22`）并挂载到 `/srv/a-nas/data`；卷 marker 与 UUID 一致，API、Host Agent、Kiosk 和 Samba 服务均 active。
- 文件闭环权限根因与修复边界已记录在[数据卷空间权限调查](../investigations/2026-10-07-data-volume-space-permissions.md)；父目录、ACL、共用回收站和启动自愈的最小 Go 回归已由红转绿。该 rc.5 方案已被 ADR 0008 取代。
- [ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md) 六个实现步骤均已完成；实验 NAS 的旧 `admin` 已按[手册](../runbooks/provision-v1.0.1-experimental-storage.md#升级到统一身份adr-0008)重建为统一 Linux 身份，ACL、身份镜像与 Product Service 隔离已实机验证；issue #38 的修复已随 `9923c5987c0b` 部署且 Host Agent 启动自检通过，Web 文件的用户级实机验收尚未执行。Samba 凭据需管理员与成员各自改密后重建，之后再做客户端验收。
- `6833c7637f73` 在 WSL ext4 副本中通过全部门禁，发布二进制在 Debian 12 容器中构建（所需 glibc 最高 2.34）；Debian 13 systemd 系统测试用替身与真实模型各跑一遍，均无失败（真实模型 81 项）。API SHA-256 为 `ca86b3d8…349300`，Host Agent 为 `69985cf3…48495f`，同一提交构建的容器代理为 `c26957c9…50bb3ba` 并已安装。安装器首次把模型与运行环境复制到 `/opt/a-nas/models` 与 `/opt/a-nas/ai-runtimes`（463 MB、460 MB），启用 `anas-ai.socket`；以相册服务身份调用 Worker 返回校准时的模型 ID，Worker 以动态身份 `a-nas-ai` 运行，网络命名空间里只有 `lo`。首次整理 185 张测试照片，至 14:12 共用 CPU 约 1,000 秒（约 5 CPU 秒/张，含标签文本与搜索），内存峰值 815 MB，没有重启。部署中发现两处问题并在后续 PR 修复：容器代理重启后探针早于其套接字运行而失败（重跑通过）；没有待处理任务时相册服务仍每分钟询问 Worker，使它无法空闲退出、常驻约 800 MB，修复随下一次部署生效。
- 开发循环使用受影响测试，完整门禁为每个不可变 RC 制品只执行一次；部署按提交、版本和二进制哈希复用验证证明。
- [系统测试](../development/LOCAL_ENVIRONMENT.md#系统测试)在 Debian 13 systemd 容器中用真实安装器、unit 与二进制按用户检查 Web、SMB、相册、服务身份的权限、局域网 Web 入口与屏保视频的独立通道，共 69 项，并作为 CI 作业运行。
- `make check VERSION=v1.0.1-rc.4` 已通过前端类型/测试/构建、文档、运维、Go vet、Go 测试和发布二进制构建；stage 复用了该证明并核对 API SHA-256 `c92fd487…ab3359`、Host Agent SHA-256 `9a9d90be…2616b`。
- `make check VERSION=v1.0.1-rc.5` 已在 WSL 原生 ext4 工作树完整通过；不可变制品的 API SHA-256 为 `350a58c1…0513`，Host Agent SHA-256 为 `94ca206e…7444`。当时 `/opt/a-nas/current` 指向 `/opt/a-nas/releases/v1.0.1-rc.5-69c97abe191f`（ADR 0008 之前的模型，已被取代）。
- 桌面“Docker”应用（容器/镜像列表、资源占用、启停重启、日志）已实现，Docker Engine 与专用容器代理的决定见 [ADR 0009](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)；Experimental NAS 于 2026-10-10 按 [ADR 0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md) 换用 Docker 官方软件源：Docker 29.9.0（API 1.56）与 Compose v5.6.0，存储驱动与地址池不变。容器代理为 `173cbb5499ce`，部署后探针通过，Immich 与 OpenList 的每个挂载都是计划中的子目录。
- 桌面“应用中心”已实现：内置 26 个通过安装策略的 CasaOS 应用，规划→确认摘要→Compose 执行，卸载保留数据，见[应用中心规格](../specs/app-center.md)；Experimental NAS 已真实拉取、安装并重建 OpenList 4.2.2。其上游清单硬编码用户与 A-NAS 应用身份冲突的问题已由计划渲染策略修复，实机证据见[OpenList 运行身份调查](../investigations/2026-10-08-openlist-runtime-identity.md)。会新建 Docker 网络的应用要求 Docker 配置专用地址池，安装后核对网络地址，不在池内即回滚（[ADR 0013](../adr/0013-allocate-docker-networks-from-an-a-nas-address-pool.md)）；实验 NAS 的地址池为 `10.96.64.0/18`，重装的 Immich 网络为 `10.96.65.0/24`。容器代理还在规划时检查 Compose 与 Engine API 的最低版本，安装后核对每个挂载只到计划中的子目录（[ADR 0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)）。
- 局域网 Web 入口已实现：安装 Caddy 后，内网浏览器经 `http://<NAS 地址>` 使用全部功能，明文 HTTP 作为 HTTPS 前的过渡（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)）。2026-10-09 已在实验 NAS 启用（Debian 的 Caddy 2.6.2），从公司 WiFi 上的开发机访问 `http://172.18.45.48` 的健康检查、首页与未登录 `401` 均正常。
- Debian `systemd-timesyncd` 在上次实机检查时为 enabled/active、`NTPSynchronized=yes`，见[时间调查](../investigations/2026-10-06-system-clock-drift-and-network-sync.md)。

## 下一步

1. 在 Web 中验收 PR #50 的文件管理三栏缩放、左右栏收起、列表/图标切换、排序、上传入口和资源文件夹跳转，再打开个人空间与 Shared 并上传、删除、恢复，完成 [issue #38](https://github.com/zhongwater123/A-NAS/issues/38) 修复的实机验收；修复已随 `9923c5987c0b` 部署且 Host Agent 启动自检通过，见[调查](../investigations/2026-10-08-host-agent-loses-setuid-under-systemd.md)。
2. 验证会话处理：本机屏幕退出并重新登录一次以取得本机会话（部署前登录的仍为 12 小时会话），之后跨夜和重启仍保持登录；局域网或 SSH 隧道浏览器登录满 12 小时后回到登录页并提示“登录已过期”，不再显示“连接中断”或需要重启，见[调查](../investigations/2026-10-09-local-console-disconnected-after-session-expiry.md)。
3. 在应用中心安装一个新应用，确认 [ADR 0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md) 的版本与挂载检查在实机上放行正常安装；Caddy 官方源恢复后按[局域网入口手册](../runbooks/enable-lan-web-access.md)第 2 步升级（见外部条件）。
4. 管理员在“设置 → 我的账号”中修改自己的密码，以同时重建 Web 与 Samba 凭据，确认 `pdbedit -L -u admin` 出现凭据，再从能到达 NAS TCP 445 的 Windows 电脑（见外部条件）用新密码连接 SMB；成员的 SMB 凭据需本人登录改密后重建。
5. 完成个人与 Shared 的 Web/Windows SMB 双向读写、大文件哈希、Web 先删后 SMB 删除、恢复、快照、正常重启、SMART、容量、服务和审计证据。全部通过后才创建 `v1.0.1` 标签。
6. 在直连屏幕按[运行手册](../runbooks/operate-local-kiosk.md#验收)验证每次进入屏保只循环一条、重新闲置时再次随机选择、单条失败补选与首次输入唤醒；回退到原五条视频的池执行 `install-screensavers.sh 53e12079d8d50c4c`。
7. 后续补齐安全移除、运行中 SATA 热拔插、同盘重新接入和自动恢复；相册的用户功能验收与后续切片在基础闭环稳定后继续。
8. 相册 M1 的切片 1–6 已实现并通过测试，[相册服务](../../internal/photoservice/photoservice.go)的专用身份、Btrfs 子卷、IPC 与隔离边界已随 `ed5368ba998e` 部署；运行期数据卷故障（离线、只读、重新挂载）、上传中途强制终止与导入中途卷满的处理已通过本地故障注入（均尚未部署）；下一步先跑通整体闭环：部署后按[验收相册 M1](../runbooks/accept-photo-library-m1.md)完成桌面上传、跨成员隔离、管理员查看、强制终止与断电；20,000 张规模与缩略图积压测试暂缓。M2 按[实施方案](../architecture/photo-ai.md)推进，步骤 1–7 已合并（不做 OCR；家庭照片人工标注暂缓，其质量门禁保持未通过）。步骤 3 冻结了 70 视觉 token 与 512 px 输入，中文检索 COCO-CN R@1 71.3%（[校准报告](../research/photo-ai-label-calibration.md)）；140 token 与 1024 px 输入的对比没有明显收益。步骤 5 的 AI 标签已于 2026-10-10 按用户决定暂停，搜索改为纯向量排序与两段式结果（#66）；固定类别的目标检测暂不做，整图向量找不到小物体的问题先在开发机评估分块向量（后台评估进行中，结论写入[实施方案](../architecture/photo-ai.md#ai-标签2026-10-10-起暂停)）；向量存储的规模实测按用户决定暂缓。步骤 6 的相册分组、用户标签与 AI 纠错不依赖 AI，已随 `173cbb5499ce` 部署。2026-10-10 决定 AI 随系统内置安装、默认开启，模型由开发机经 SSH 传到设备，设备不联网获取（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）；步骤 7 已据此实现（[#58](https://github.com/zhongwater123/A-NAS/pull/58)）：模型与 Python 运行环境按内容各存一份并由 release 引用，Worker 按需启动，系统测试证明它没有网络、读不到数据卷；整理进度与隐私说明随 #61 的相册界面实现。用户于 2026-10-10 同意在 Experimental NAS 部署本地 AI，已于同日随 `6833c7637f73` 按[启用相册本地 AI](../runbooks/enable-photo-ai.md)部署。USB 存储（[#25](https://github.com/zhongwater123/A-NAS/issues/25)）、账号删除（[#26](https://github.com/zhongwater123/A-NAS/issues/26)）和备份（[#27](https://github.com/zhongwater123/A-NAS/issues/27)）是相册部分验收的前置能力。

## 外部条件与限制

- Experimental NAS 当前在线并运行主干提交 `6833c7637f73`，本地 AI 已启用；统一身份、权限链、Docker、容器代理与相册服务边界实机检查已通过，Samba 凭据尚未重建，新文件管理界面、“设置”窗口、新相册界面、文件和共享用户闭环及相册用户功能验收仍未完成。
- v1.0.1 只使用可丢弃测试数据。单盘 Btrfs 不提供冗余、备份或家庭生产数据可靠性承诺。
- SATA 实验盘为 `ST500DM002-1BD142`，容量 500,107,862,016 字节、序列号 `Z2AYDZPB`、WWN `0x5000c500518d4994`，位于 `ata7/host6`；现已创建 `/dev/sda1` Btrfs。其 `HOTPLUG=0`，运行中热插拔能力尚未实现和验收。
- Kiosk 的 URL、网络、快捷键、profile 生命周期和 VT 恢复仍未完成产品级验收。
- 2026-10-09 重启后 smbd 早于 DHCP 获得地址启动，因 `bind interfaces only` 只监听回环，Windows 无法连接 SMB。当天部署后已重启 smbd 并确认监听 `172.18.45.48:445`，但下次重启仍会复现；根治前每次重启后需 root 执行 `systemctl restart smbd`。
- Caddy 官方的 Cloudsmith 软件源自 2026-10-09 对软件包索引返回 `402 Payment Required`（[caddyserver/caddy#8184](https://github.com/caddyserver/caddy/issues/8184)），实验 NAS 暂留 Debian 的 Caddy 2.6.2，并删除该源以免 `apt-get update` 失败。
- 2026-10-09 从公司 WiFi 上的开发机（`172.19.172.225`）到 NAS 与网关的 TCP 445 均不通，而 80 端口正常；连接期间 NAS 上没有任何来自该网段的 445 连接状态，说明拦截发生在公司网络或该电脑的安全策略，不在 NAS。跨网段访问目前使用局域网 Web；SMB 需从 `172.18.45.x` 有线网段复测，或请 IT 放行到 NAS 的 TCP 445。

## 验证基线

```bash
# 编辑循环按变化选择目标
go test ./internal/hostops/linux ./internal/files
make ops-check

# 每个 RC 提交一次完整门禁，部署前在 systemd 下验证
make check VERSION=v1.0.1-rc.6
make system-test
```
