# 当前状态

更新时间：2026-10-08

## 当前阶段

v1.0.1“实验 NAS 基础存储与共享闭环”是当前主线。Experimental NAS 已运行 release `c902acded999`：系统服务与 Kiosk 指向同一 release，Docker、containerd、Container Agent、Host Agent、API、Samba 和 Kiosk 均 active，桌面与屏保 byte-range 请求通过，Docker 与应用中心的实时代理模式已经启用。ADR 0008 旧账号已按手册删除并重建为 UID/GID 20100，固定组为 GID 20000/20001，身份注册表已镜像到数据卷；`admin` 可以穿过但不能列出数据卷根并访问自己的个人空间，Product Service 账号 `a-nas` 被内核拒绝。

升级中发现并修复了三个连续阻塞：身份同步前把不存在账号写入 ACL 导致 Host Agent 启动死锁、`build-binaries` 未先生成嵌入式 Web UI、数据卷挂载点缺少 `a-nas-users:--x`。证据与最小回归见 [ADR 0008 首次升级调查](../investigations/2026-10-08-adr0008-bootstrap-private-acl.md)。容器部署又暴露出 Debian 13 将 CLI 拆为独立推荐包，以及部署脚本误用 `/screensaver.mp4`、`/v1/containers` 两个路由；集中式只读探针修正为 `/local-console/screensaver.mp4`、`/v1/snapshot` 后已完成实机切换，详见[容器部署调查](../investigations/2026-10-08-experimental-nas-containers-disabled.md)。旧 Samba 凭据已按手册删除，需要管理员通过已部署的自助改密入口重建后才能开始 Windows SMB 双向验收。

提交 `4e98e86b77c5` 已重新冻结为 `v1.0.0`。相册（[issue #23](https://github.com/zhongwater123/A-NAS/issues/23)）已由 [ADR 0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md) 确定专用服务身份、数据卷上的 Catalog 与按范围授权的管理员查看，并拆为 M1 基础相册、M2 本地 AI 检索与 M3 扩展三个里程碑；部署与实机验收排在基础存储闭环和 ADR 0008 之后。

## 已就绪

- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)、[存储架构](../architecture/storage-and-files.md)、[ADR 0007](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)和 [v1.0.1 发布记录](../releases/v1.0.1.md)已建立。
- 磁盘状态包含稳定 ID、removable/in-use、文件系统、SMART、温度、数据卷资格及拒绝原因；公开接口不返回设备路径。
- 存储计划持久化于 SQLite，支持十分钟过期、确认短语、执行前身份复核、幂等成功结果和中断后 `needs_attention`。
- Linux 执行器使用固定命令创建 GPT、单盘 Btrfs、UUID mount unit、空间子卷和卷身份标记；文件服务在缺少真实 Btrfs/标记时拒绝写入。
- 本机设备启用、Argon2id、服务端会话、CSRF、管理员/成员、个人空间和唯一 Shared Policy 已实现；首次启用只要求账号和密码。
- Web 文件管理、Samba、回收站、手动快照、审计、产品 API 和 React 页面已实现并有自动化覆盖。
- Web 桌面终端已本地实现：仅管理员可通过回环同源 WebSocket 打开 PTY Shell，生产中由文件代理以该管理员本人的 Linux 账号运行，默认关闭，见[终端规格](../specs/web-terminal.md)；Experimental NAS 尚未启用。
- Web 桌面已整合 CPU/内存/网速状态栏、动态图标程序坞、可持久化图标排序和 4:3 深蓝抽象壁纸；拖拽预览已移出滚动网格以避免右侧裁剪。本地控制台还会在登录后闲置三分钟播放外部静音视频屏保，媒体缺失或失败时保留静态壁纸。2026-10-08 已随当前 `c902acded999` 验收 API、Host Agent、Kiosk、嵌入式桌面和视频 Range 请求；仍待直连屏幕完成三分钟闲置及首次输入的人工验收。
- root Host Agent 与非特权产品服务通过 `root:a-nas 0660` UDS 通信；系统单元使用 root 所有的 `/opt/a-nas/current` 发布目录。
- rc.4 让空盘计划稳定输出数组、兼容旧 `null`、显示存储操作进度并为桌面窗口增加错误边界，见[蓝屏调查](../investigations/2026-10-07-blank-disk-plan-ui-crash.md)。
- rc.4 使用 root 管理的 Chromium policy 禁止保存密码、通行密钥和同步；更广的 Kiosk 约束仍见[开放调查](../investigations/2026-10-06-kiosk-browser-confinement.md)。
- rc.4 已创建 `/dev/sda1` Btrfs 数据卷（UUID `09e275fe-794a-458d-8200-b6e67c55cc22`）并挂载到 `/srv/a-nas/data`；卷 marker 与 UUID 一致，API、Host Agent、Kiosk 和 Samba 服务均 active。
- 文件闭环权限根因与修复边界已记录在[数据卷空间权限调查](../investigations/2026-10-07-data-volume-space-permissions.md)；父目录、ACL、共用回收站和启动自愈的最小 Go 回归已由红转绿。
- [ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md) 六个实现步骤均已完成；实验 NAS 的旧 `admin` 已按[手册](../runbooks/provision-v1.0.1-experimental-storage.md#升级到统一身份adr-0008)重建为统一 Linux 身份，ACL、身份镜像、File Broker 与 Product Service 隔离已实机验证。Samba 密码凭据等待 Web 重置后再做客户端验收。
- 开发循环使用受影响测试，完整门禁为每个不可变 RC 制品只执行一次；部署按提交、版本和二进制哈希复用验证证明。
- `make check VERSION=v1.0.1-rc.4` 已通过前端类型/测试/构建、文档、运维、Go vet、Go 测试和发布二进制构建；stage 复用了该证明并核对 API SHA-256 `c92fd487…ab3359`、Host Agent SHA-256 `9a9d90be…2616b`。
- `make check VERSION=v1.0.1-rc.5` 已在 WSL 原生 ext4 工作树完整通过；不可变制品的 API SHA-256 为 `350a58c1…0513`，Host Agent SHA-256 为 `94ca206e…7444`。`/opt/a-nas/current` 已切换到 `/opt/a-nas/releases/v1.0.1-rc.5-69c97abe191f`，API、Host Agent、Kiosk 和 Samba 均 active。
- 桌面“Docker”应用（容器/镜像列表、资源占用、启停重启、日志）已实现，Docker Engine 与专用容器代理的决定见 [ADR 0009](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)；Experimental NAS 已安装 Docker 26.1.5、独立 CLI、Compose 2.26.1 与容器代理，`c902acded999` 已在代理模式下完成切换，真实代理路径和桌面 HTTP 路径均通过实机验证。
- 桌面“应用中心”已实现：内置 26 个通过安装策略的 CasaOS 应用，规划→确认摘要→Compose 执行，卸载保留数据，见[应用中心规格](../specs/app-center.md)；本地已用真实 Compose 验证安装/卸载，镜像拉取因本机无法访问 Docker Hub 未验证。
- Debian `systemd-timesyncd` 在上次实机检查时为 enabled/active、`NTPSynchronized=yes`，见[时间调查](../investigations/2026-10-06-system-clock-drift-and-network-sync.md)。

## 下一步

1. 管理员在账号管理中修改自己的密码以同时重建 Web 与 Samba 凭据，确认 `pdbedit -L -u admin` 出现凭据，再从 Windows 用新密码连接 SMB。
2. 完成个人与 Shared 的 Web/Windows SMB 双向读写、大文件哈希、Web 先删后 SMB 删除、恢复、快照、正常重启、SMART、容量、服务和审计证据。全部通过后才创建 `v1.0.1` 标签。
3. 在直连屏幕完成三分钟闲置屏保与首次输入唤醒的人工验收。
4. 后续补齐安全移除、运行中 SATA 热拔插、同盘重新接入和自动恢复；基础闭环稳定后恢复相册主线。
5. 相册 M1 按[技术设计的开发切片](../architecture/photo-library.md#开发切片)从 Catalog 与受管存储开始，以已合并的 ADR 0008 为基线、先用本地临时目录开发，部署与实机验收排在基础存储闭环之后；M2 的模型基准可并行，先在实验 NAS 的 Debian 13 上完成 EmbeddingGemma 2 LiteRT-LM 冒烟测试，再用公开中文标注数据集设定标签初始阈值；家庭照片人工标注暂缓，其质量门禁保持未通过。USB 存储、账号删除和备份是相册部分验收的前置能力，尚未立项。

## 外部条件与限制

- Experimental NAS 当前在线并运行 `c902acded999`；统一身份、权限链、Docker 与容器代理实机检查已通过，但 Samba 凭据尚未重建，文件和共享用户闭环仍未完成。
- v1.0.1 只使用可丢弃测试数据。单盘 Btrfs 不提供冗余、备份或家庭生产数据可靠性承诺。
- SATA 实验盘为 `ST500DM002-1BD142`，容量 500,107,862,016 字节、序列号 `Z2AYDZPB`、WWN `0x5000c500518d4994`，位于 `ata7/host6`；现已创建 `/dev/sda1` Btrfs。其 `HOTPLUG=0`，运行中热插拔能力尚未实现和验收。
- Kiosk 的 URL、网络、快捷键、profile 生命周期和 VT 恢复仍未完成产品级验收。

## 验证基线

```bash
# 编辑循环按变化选择目标
go test ./internal/hostops/linux ./internal/files
make ops-check

# 每个 RC 提交一次完整门禁
make check VERSION=v1.0.1-rc.5
```
