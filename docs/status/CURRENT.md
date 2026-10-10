# 当前状态

更新时间：2026-10-10

## 当前阶段

v1.0.1“实验 NAS 基础存储与共享闭环”是当前主线。Experimental NAS 于 2026-10-10 部署主干提交 `173cbb5499ce`，在 PR #50 的 `9923c5987c0b` 之上包含相册 M2 步骤 1–6 的代码（不安装 AI Worker，不运行模型）、#53 的屏保视频池与 #56 的 Docker 版本与挂载检查。PR #50 包含文件管理三栏布局、可调栏宽、列表/图标视图、文件夹优先排序、成熟图标工具栏、当前目录上传入口、真实图片缩略图与自适应预览，以及双击下载、框选多选和右键编辑菜单：系统服务与 Kiosk 指向同一 release，Host Agent 启动自检记录 `file broker identity switch verified`，相册服务就绪，Caddy 在 80 端口提供局域网入口，产品服务仍只监听 `127.0.0.1:8080`。Docker 与应用中心的实时代理模式自 2026-10-08 起启用。OpenList 已按 `app-openlist` 的 UID/GID 30002 重建，状态 running、重启计数 0、HTTP 返回 200。ADR 0008 旧账号已按手册删除并重建为 UID/GID 20100，固定组为 GID 20000/20001，身份注册表已镜像到数据卷；`admin` 可以穿过但不能列出数据卷根并访问自己的个人空间，Product Service 账号 `a-nas` 被内核拒绝，遗留的 `a-nas-members` 组已不存在。新文件管理界面的用户级实机验收（布局交互、上传、删除、恢复）尚未执行。

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
- Web 桌面已整合 CPU/内存/网速状态栏、动态图标程序坞、可持久化图标排序、4:3 深蓝抽象壁纸和 [CRT 风格开机动画](../specs/boot-ident.md)；拖拽预览已移出滚动网格以避免右侧裁剪。本地控制台还会在登录后闲置三分钟播放外部静音屏保。视频池为每次进入屏保时随机选择一条并持续循环、退出后下次闲置重新选择、单条失败补选，直连屏幕上的轮换行为尚未核对。按 [ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)（[issue #49](https://github.com/zhongwater123/A-NAS/issues/49)），屏保视频已改为系统盘上的长期媒体，不再随 release 发布：每个视频按 SHA-256 只存一份，产品服务固定读取 `/var/lib/a-nas/screensavers/current`，更换视频走独立的暂存与安装命令，只上传缺少的视频。该机制随 `173cbb5499ce` 部署：2026-10-10 安装器把原五条视频导入为池 `53e12079d8d50c4c`，按 release 存放的旧副本已清理（释放约 3.3G），单视频时期遗留的 `computer-chip.mp4` 经暂存命令加入，当前池 `68299080c557c520` 共六条，经局域网入口请求清单与每条视频的 Range 均正常。2026-10-08 已在 `ed5368ba998e` 上验收 API、Host Agent、Kiosk、嵌入式桌面和旧兼容路由的 Range 请求，开机动画随 `946638851c7c` 部署。
- Web 相册窗口已按[相册界面规格](../specs/photo-gallery-ui.md)重做：以照片为中心的等高行时间线、年月时间轴与按月载入、连续缩放、框选与拖动勾选的批量操作、拖到相册、即时显示的上传、可缩放与幻灯片的沉浸式查看器、AI 搜图页和本地 AI 进度；新增按月时间线与 AI 标签汇总两个只读接口。搜索改为只显示与查询相符的照片：查询提到已校准的事物时按其阈值过滤，否则只给最接近的至多 20 张并说明（[依据](../research/photo-ai-label-calibration.md#搜索结果的取舍)）。人物识别（M3 切片 13）作为下一个里程碑单独规划，先核实人脸模型许可。已在本地 systemd 环境验证，尚未部署到实验 NAS。
- root Host Agent 与非特权产品服务通过 `root:a-nas 0660` UDS 通信；系统单元使用 root 所有的 `/opt/a-nas/current` 发布目录。
- rc.4 让空盘计划稳定输出数组、兼容旧 `null`、显示存储操作进度并为桌面窗口增加错误边界，见[蓝屏调查](../investigations/2026-10-07-blank-disk-plan-ui-crash.md)。
- rc.4 使用 root 管理的 Chromium policy 禁止保存密码、通行密钥和同步；更广的 Kiosk 约束仍见[开放调查](../investigations/2026-10-06-kiosk-browser-confinement.md)。
- rc.4 已创建 `/dev/sda1` Btrfs 数据卷（UUID `09e275fe-794a-458d-8200-b6e67c55cc22`）并挂载到 `/srv/a-nas/data`；卷 marker 与 UUID 一致，API、Host Agent、Kiosk 和 Samba 服务均 active。
- 文件闭环权限根因与修复边界已记录在[数据卷空间权限调查](../investigations/2026-10-07-data-volume-space-permissions.md)；父目录、ACL、共用回收站和启动自愈的最小 Go 回归已由红转绿。该 rc.5 方案已被 ADR 0008 取代。
- [ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md) 六个实现步骤均已完成；实验 NAS 的旧 `admin` 已按[手册](../runbooks/provision-v1.0.1-experimental-storage.md#升级到统一身份adr-0008)重建为统一 Linux 身份，ACL、身份镜像与 Product Service 隔离已实机验证；issue #38 的修复已随 `9923c5987c0b` 部署且 Host Agent 启动自检通过，Web 文件的用户级实机验收尚未执行。Samba 凭据需管理员与成员各自改密后重建，之后再做客户端验收。
- `173cbb5499ce` 在 WSL ext4 副本中通过全部门禁（前端 81 项），发布二进制在 Debian 12 容器中构建（WSL 的 glibc 2.43 新于 Debian 13 的 2.41），Debian 13 systemd 系统测试 69 项通过；API SHA-256 为 `ba62a6e8…a5d118`，Host Agent 为 `3f1b6e07…130754`，容器代理为 `47c1de04…64d97a`。实验 NAS 切换后两个 current 链接一致，八个相关服务 active，身份切换日志与相册服务就绪已核对；屏保池 `53e12079d8d50c4c` 的五条哈希通过，容器代理探针通过，局域网健康检查返回 `ok`。
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
4. 管理员修改自己的密码（已部署的 `173cbb5499ce` 在“账号管理”，主干在“设置 → 我的账号”）以同时重建 Web 与 Samba 凭据，确认 `pdbedit -L -u admin` 出现凭据，再从能到达 NAS TCP 445 的 Windows 电脑（见外部条件）用新密码连接 SMB；成员的 SMB 凭据需本人登录改密后重建。
5. 完成个人与 Shared 的 Web/Windows SMB 双向读写、大文件哈希、Web 先删后 SMB 删除、恢复、快照、正常重启、SMART、容量、服务和审计证据。全部通过后才创建 `v1.0.1` 标签。
6. 在直连屏幕按[运行手册](../runbooks/operate-local-kiosk.md#验收)验证每次进入屏保只循环一条、重新闲置时再次随机选择、单条失败补选与首次输入唤醒；回退到原五条视频的池执行 `install-screensavers.sh 53e12079d8d50c4c`。
7. 后续补齐安全移除、运行中 SATA 热拔插、同盘重新接入和自动恢复；相册的用户功能验收与后续切片在基础闭环稳定后继续。
8. 相册 M1 的切片 1–6 已实现并通过测试，[相册服务](../../internal/photoservice/photoservice.go)的专用身份、Btrfs 子卷、IPC 与隔离边界已随 `ed5368ba998e` 部署；运行期数据卷故障（离线、只读、重新挂载）、上传中途强制终止与导入中途卷满的处理已通过本地故障注入（均尚未部署）；下一步先跑通整体闭环：部署后按[验收相册 M1](../runbooks/accept-photo-library-m1.md)完成桌面上传、跨成员隔离、管理员查看、强制终止与断电；20,000 张规模与缩略图积压测试暂缓。M2 按[实施方案](../architecture/photo-ai.md)只在开发机推进，步骤 1–6 已合并（[#51](https://github.com/zhongwater123/A-NAS/pull/51)、[#54](https://github.com/zhongwater123/A-NAS/pull/54)，主干 `58f0ca1d2d85`）：随主干部署时不安装 AI Worker，NAS 上不运行模型，相册搜索只按文件名与用户标签匹配、不显示 AI 标签，直到部署包含 AI 的版本（不做 OCR；家庭照片人工标注暂缓，其质量门禁保持未通过）。步骤 3 的标签词表 v1（330 个）与 70 视觉 token 的阈值已冻结：73 个标签可展示，COCO 测试集上显示的标签 89.4% 正确，中文检索 COCO-CN R@1 71.3%（[校准报告](../research/photo-ai-label-calibration.md)）；步骤 4 的语义搜索与步骤 5 的 AI 标签展示、按标签筛选已在本地 systemd 环境中用真实模型验证。140 token 与 1024 px 输入的对比没有明显收益，保持 70 token 与 512 px 缩略图。步骤 6 的相册分组、用户标签与 AI 纠错不依赖 AI，已随 `173cbb5499ce` 部署。2026-10-10 决定 AI 随系统内置安装、默认开启，模型由开发机经 SSH 传到设备，设备不联网获取（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）；下一步是据此实现步骤 7 的内置打包、整理进度与隐私说明，以及证明 Worker 无法联网的系统测试。部署包含 AI 的版本、在 NAS 上运行模型需另行确认。USB 存储（[#25](https://github.com/zhongwater123/A-NAS/issues/25)）、账号删除（[#26](https://github.com/zhongwater123/A-NAS/issues/26)）和备份（[#27](https://github.com/zhongwater123/A-NAS/issues/27)）是相册部分验收的前置能力。

## 外部条件与限制

- Experimental NAS 当前在线并运行主干提交 `173cbb5499ce`；统一身份、权限链、Docker、容器代理与相册服务边界实机检查已通过，Samba 凭据尚未重建，新文件管理界面、文件和共享用户闭环及相册用户功能验收仍未完成。
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
