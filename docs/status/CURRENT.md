# 当前状态

更新时间：2026-10-08

## 当前阶段

v1.0.1“实验 NAS 基础存储与共享闭环”是当前主线。Experimental NAS 运行主干合并提交 `946638851c7c`（2026-10-08 部署）：系统服务与 Kiosk 指向同一 release，Docker、containerd、Container Agent、Host Agent、相册服务、API、Samba 和 Kiosk 均 active，桌面与屏保 byte-range 请求通过，Docker 与应用中心的实时代理模式已经启用。OpenList 已按 `app-openlist` 的 UID/GID 30002 重建，状态 running、重启计数 0、HTTP 返回 200。ADR 0008 旧账号已按手册删除并重建为 UID/GID 20100，固定组为 GID 20000/20001，身份注册表已镜像到数据卷；`admin` 可以穿过但不能列出数据卷根并访问自己的个人空间，Product Service 账号 `a-nas` 被内核拒绝。但该版本上 Web 文件管理不可用：Host Agent 在 systemd 下丢失 `CAP_SETUID`，File Broker 无法以用户身份启动 Worker（[issue #38](https://github.com/zhongwater123/A-NAS/issues/38)），修复在 PR #39。

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
- Web 桌面已整合 CPU/内存/网速状态栏、动态图标程序坞、可持久化图标排序、4:3 深蓝抽象壁纸和 [CRT 风格开机动画](../specs/boot-ident.md)；拖拽预览已移出滚动网格以避免右侧裁剪。本地控制台还会在登录后闲置三分钟播放外部静音屏保。视频池已在本地实现为每次进入屏保时随机选择一条并持续循环、退出后下次闲置重新选择、单条失败补选和按 release 隔离的外部资产；Experimental NAS 当前的 `946638851c7c` 仍运行原单视频实现。2026-10-08 已在 `ed5368ba998e` 上验收 API、Host Agent、Kiosk、嵌入式桌面和旧兼容路由的 Range 请求，开机动画随 `946638851c7c` 部署。
- root Host Agent 与非特权产品服务通过 `root:a-nas 0660` UDS 通信；系统单元使用 root 所有的 `/opt/a-nas/current` 发布目录。
- rc.4 让空盘计划稳定输出数组、兼容旧 `null`、显示存储操作进度并为桌面窗口增加错误边界，见[蓝屏调查](../investigations/2026-10-07-blank-disk-plan-ui-crash.md)。
- rc.4 使用 root 管理的 Chromium policy 禁止保存密码、通行密钥和同步；更广的 Kiosk 约束仍见[开放调查](../investigations/2026-10-06-kiosk-browser-confinement.md)。
- rc.4 已创建 `/dev/sda1` Btrfs 数据卷（UUID `09e275fe-794a-458d-8200-b6e67c55cc22`）并挂载到 `/srv/a-nas/data`；卷 marker 与 UUID 一致，API、Host Agent、Kiosk 和 Samba 服务均 active。
- 文件闭环权限根因与修复边界已记录在[数据卷空间权限调查](../investigations/2026-10-07-data-volume-space-permissions.md)；父目录、ACL、共用回收站和启动自愈的最小 Go 回归已由红转绿。该 rc.5 方案已被 ADR 0008 取代。
- [ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md) 六个实现步骤均已完成；实验 NAS 的旧 `admin` 已按[手册](../runbooks/provision-v1.0.1-experimental-storage.md#升级到统一身份adr-0008)重建为统一 Linux 身份，ACL、身份镜像与 Product Service 隔离已实机验证；File Broker 的用户 Worker 在正式服务单元下无法启动（issue #38），尚未实机验证。Samba 凭据需管理员与成员各自改密后重建，之后再做客户端验收。
- 开发循环使用受影响测试，完整门禁为每个不可变 RC 制品只执行一次；部署按提交、版本和二进制哈希复用验证证明。
- [系统测试](../development/LOCAL_ENVIRONMENT.md#系统测试)在 Debian 13 systemd 容器中用真实安装器、unit 与二进制按用户检查 Web、SMB、相册与服务身份的权限，共 40 项，并作为 CI 作业运行。
- `make check VERSION=v1.0.1-rc.4` 已通过前端类型/测试/构建、文档、运维、Go vet、Go 测试和发布二进制构建；stage 复用了该证明并核对 API SHA-256 `c92fd487…ab3359`、Host Agent SHA-256 `9a9d90be…2616b`。
- `make check VERSION=v1.0.1-rc.5` 已在 WSL 原生 ext4 工作树完整通过；不可变制品的 API SHA-256 为 `350a58c1…0513`，Host Agent SHA-256 为 `94ca206e…7444`。当时 `/opt/a-nas/current` 指向 `/opt/a-nas/releases/v1.0.1-rc.5-69c97abe191f`（ADR 0008 之前的模型，已被取代）。
- 桌面“Docker”应用（容器/镜像列表、资源占用、启停重启、日志）已实现，Docker Engine 与专用容器代理的决定见 [ADR 0009](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)；Experimental NAS 已安装 Docker 26.1.5、独立 CLI、Compose 2.26.1 与容器代理，`ed5368ba998e` 已在代理模式下完成切换，真实代理路径和桌面 HTTP 路径均通过实机验证。
- 桌面“应用中心”已实现：内置 26 个通过安装策略的 CasaOS 应用，规划→确认摘要→Compose 执行，卸载保留数据，见[应用中心规格](../specs/app-center.md)；Experimental NAS 已真实拉取、安装并重建 OpenList 4.2.2。其上游清单硬编码用户与 A-NAS 应用身份冲突的问题已由计划渲染策略修复，实机证据见[OpenList 运行身份调查](../investigations/2026-10-08-openlist-runtime-identity.md)。
- Debian `systemd-timesyncd` 在上次实机检查时为 enabled/active、`NTPSynchronized=yes`，见[时间调查](../investigations/2026-10-06-system-clock-drift-and-network-sync.md)。

## 下一步

1. 合并 PR #39 并按[实机配置手册](../runbooks/provision-v1.0.1-experimental-storage.md#安装系统服务)部署 [issue #38](https://github.com/zhongwater123/A-NAS/issues/38) 的修复，同时移除遗留的 `a-nas-members` 组：Experimental NAS 上 Web 文件管理（以及启用后的终端）因 Host Agent 在 systemd 下丢失 `CAP_SETUID` 而不可用，修复后的 Web 写入也不再被产品服务沙箱误判为卷不可用。安装器核对通过后在 Web 打开个人空间与 Shared 并上传、删除、恢复，见[调查](../investigations/2026-10-08-host-agent-loses-setuid-under-systemd.md)。
2. 管理员在账号管理中修改自己的密码以同时重建 Web 与 Samba 凭据，确认 `pdbedit -L -u admin` 出现凭据，再从 Windows 用新密码连接 SMB；成员的 SMB 凭据需本人登录改密后重建。
3. 完成个人与 Shared 的 Web/Windows SMB 双向读写、大文件哈希、Web 先删后 SMB 删除、恢复、快照、正常重启、SMART、容量、服务和审计证据。全部通过后才创建 `v1.0.1` 标签。
4. 合并并部署视频池后，在直连屏幕核对五条清单与 Range 请求，再验证每次进入屏保只循环一条、重新闲置时再次随机选择、单条失败补选与首次输入唤醒。
5. 后续补齐安全移除、运行中 SATA 热拔插、同盘重新接入和自动恢复；相册的用户功能验收与后续切片在基础闭环稳定后继续。
6. 相册 M1 的切片 1–6 已实现并通过测试，[相册服务](../../internal/photoservice/photoservice.go)的专用身份、Btrfs 子卷、IPC 与隔离边界已随 `ed5368ba998e` 部署；运行期数据卷故障（离线、只读、重新挂载）、上传中途强制终止与导入中途卷满的处理已通过本地故障注入，（均尚未部署）；下一步先跑通整体闭环：部署后按[验收相册 M1](../runbooks/accept-photo-library-m1.md)完成桌面上传、跨成员隔离、管理员查看、强制终止与断电；20,000 张规模与缩略图积压测试暂缓。M2 的模型基准可并行，先在实验 NAS 的 Debian 13 上完成 EmbeddingGemma 2 LiteRT-LM 冒烟测试，再用公开中文标注数据集设定标签初始阈值；家庭照片人工标注暂缓，其质量门禁保持未通过。USB 存储（[#25](https://github.com/zhongwater123/A-NAS/issues/25)）、账号删除（[#26](https://github.com/zhongwater123/A-NAS/issues/26)）和备份（[#27](https://github.com/zhongwater123/A-NAS/issues/27)）是相册部分验收的前置能力。

## 外部条件与限制

- Experimental NAS 当前在线并运行 `946638851c7c`；统一身份、权限链、Docker、容器代理与相册服务边界实机检查已通过，但 Web 文件在该版本不可用（issue #38），Samba 凭据尚未重建，`a-nas` 仍在遗留的 `a-nas-members` 组中，文件和共享用户闭环及相册用户功能验收仍未完成。
- v1.0.1 只使用可丢弃测试数据。单盘 Btrfs 不提供冗余、备份或家庭生产数据可靠性承诺。
- SATA 实验盘为 `ST500DM002-1BD142`，容量 500,107,862,016 字节、序列号 `Z2AYDZPB`、WWN `0x5000c500518d4994`，位于 `ata7/host6`；现已创建 `/dev/sda1` Btrfs。其 `HOTPLUG=0`，运行中热插拔能力尚未实现和验收。
- Kiosk 的 URL、网络、快捷键、profile 生命周期和 VT 恢复仍未完成产品级验收。

## 验证基线

```bash
# 编辑循环按变化选择目标
go test ./internal/hostops/linux ./internal/files
make ops-check

# 每个 RC 提交一次完整门禁，部署前在 systemd 下验证
make check VERSION=v1.0.1-rc.6
make system-test
```
