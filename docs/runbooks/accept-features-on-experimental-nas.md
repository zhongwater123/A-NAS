# 在实验 NAS 上验收已部署的功能

状态：draft
更新时间：2026-10-22

## 目的

功能合并并部署到 Experimental NAS 后，在真实硬件、局域网浏览器与本机屏幕上核对自动化测试与系统测试覆盖不到的行为。每节对应一个模块；一节全部通过后删掉该节，把证据写进对应 PR、规格的验收证据或调查记录，并更新[当前状态](../status/CURRENT.md)。v1.0.1 闭环（Web/SMB 双向读写、回收站、快照、重启与审计）不在本手册，见[实机配置与验收手册](provision-v1.0.1-experimental-storage.md#功能验收)。

## 前提与风险

- 权限：管理员与至少一名成员的 Web 账号；需要 root 的核对由设备所有者执行，开发机只有只读 SSH。
- 目标：Experimental NAS 当前的 release，开始前记下 `readlink /opt/a-nas/current`。
- 风险：只用可丢弃测试数据；安装应用、上传与扫描会占用数据卷空间与 CPU。
- 停止条件：数据卷离线或只读、服务反复重启、出现非预期的数据丢失时停止，按[调查模板](../investigations/_TEMPLATE.md)记录。

## 步骤

### 部署 #212–#215 与 #225 之后

部署要求见[当前状态](../status/CURRENT.md#实验-nas-上运行什么)。

- Host Agent（#166）：核对 `CapBnd`、`systemd-cgls` 中文件 Worker 与终端 Shell 各自的 scope、裸机上 `/sys` 是否只读，以及加盘后 udev 重新探测是否仍正常；运行一周后查看 `MemoryPeak` 与 `TasksCurrent`，确认上限合适（[调查](../investigations/2026-10-21-host-agent-single-failures-stop-everything.md)）。
- 应用安装（#169）：安装过程中重启容器代理，确认显示安装被中断且没有留下容器（[调查](../investigations/2026-10-21-failed-app-install-shown-as-installed.md)）。
- 媒体 Worker（#168）：相册服务日志出现 `photo jobs recovered`，此前失败的 HEIC 照片得到缩略图（[调查](../investigations/2026-10-21-media-worker-shared-budget.md)）。

### 文件管理、登录与账号

- 文件管理三栏缩放、左右栏收起、列表/图标切换、排序、上传入口与资源文件夹跳转；在个人空间与 Shared 上传、删除、恢复（[issue #38 调查](../investigations/2026-10-08-host-agent-loses-setuid-under-systemd.md)）。
- 回收站批量恢复，原文件夹已删除时恢复到空间根目录；双击预览 20 MB 照片与 PDF；容量单位为 1024 进制（[规格](../specs/size-units.md)）。核对后在“问题反馈”中把 Codex 的 6 条建议标为已处理。
- 超长、Windows/macOS 不兼容与只差大小写的名称被拒绝；Windows SMB 能打开 Web 新建的文件夹。
- 会话：本机屏幕退出并重新登录一次，之后跨夜和重启仍保持登录；局域网浏览器登录满 12 小时后回到登录页并提示“登录已过期”（[调查](../investigations/2026-10-09-local-console-disconnected-after-session-expiry.md)）。
- 会话只在登录时的客户端上有效（#205）：局域网浏览器重新登录一次、本机屏幕保持登录；打开一个应用的网页后桌面仍登录；CSP 下 PDF 预览、终端、VNAS、影视与音乐播放正常。
- 账号规则（#101）：按管理员、成员与被禁用账号核对登录、禁用、重置与改密。
- SMART 改为后台读取（#126）：日志中 smartctl 被杀与超时的条数、大文件夹的加载耗时、数据盘能否待机（[调查](../investigations/2026-10-15-host-agent-smart-on-every-request.md)）。

### 备份中心与存储

- 快照清扫（#204）：部署前后记下 `.a-nas-snapshots` 的条目数并查看清扫日志，确认大空间出现凌晨 3 点的定时快照（[调查](../investigations/2026-10-21-snapshot-listing-cut-at-one-mebibyte.md)）；各空间默认的每日快照都出现。
- 实测“找回文件 → 最近删除或移走”在真实空间上的耗时。
- 硬盘用途（#149、#198）：加盘后不重启即显示为已使用，生成计划后可以取消；按[加盘手册](add-a-disk-to-the-data-volume.md)补做 Scrub。
- 系统健康（#143）：概览的“健康检查”与 SMART、温度、挂载、只读、剩余空间和 Btrfs 错误计数一致。

### 桌面、设置与本机屏幕

- 屏保与息屏：按[本地控制台手册](operate-local-kiosk.md#验收)，含个性化的第 10、11 项。直连屏幕是 iPad mini 的 LCD，系统调不了背光，Cage 0.2.0 也没有输出电源管理，黑屏期间背光仍亮；能否切断信号另行验证。
- 程序坞悬停预览（#154）：本机屏幕上预览大文件夹或相册窗口时没有明显卡顿。
- 任务管理器（#125）：在实机硬件上核对各分页；GPU 使用率只支持 AMD amdgpu sysfs，其他驱动与 NPU 显示不可用。
- 管理员终端：root 按[开启管理员终端](enable-admin-terminal.md)执行一次后，在“设置 → 终端”打开。
- 头像与昵称（#104）：用 iPhone HEIC 照片从“NAS 文件”和“相册”各设一次头像，本机屏幕与局域网浏览器都显示新头像和昵称。
- 日志（#79）：局域网登录带客户端地址，Kiosk 登录显示为本机屏幕，系统日志能读到 `anas-host-agent` 与 `smbd` 并实时跟随。

### 相册

- 基础相册：按[验收相册 M1](accept-photo-library-m1.md)完成桌面上传、跨成员隔离、管理员查看、强制终止与断电，同时完成新相册界面的用户验收。
- 物体检测（#88）：按[复测整理速度](enable-photo-objects.md#复测整理速度可选)重排全部任务，确认闲时用上空余的核；再用家庭照片核对“检测到”与找相似。
- 人物与宠物（#119、#134）：首次扫描已有照片，记录每张耗时与 Worker 内存，再用家庭照片验收命名、合并、移出与搜索。
- 视频（#146）：用 iPhone（HEVC、竖拍、实况 MOV）、安卓与摄像机的真实文件验收导入、封面、播放与转码负载。Catalog 已到第 13 次迁移，回滚到更早的 release 前须先彻底删除视频资产。

### 影视中心与音乐

- 影视中心：直接播放与转码、字幕、成员间进度隔离、从影视中心上传、重命名片源与电视剧后片名和剧集的更新，以及转封装和转码播放时前进、后退与拖动的等待时间和 CPU 占用。
- 音乐：用真实音乐文件验收首次扫描耗时、经 SMB 加入新歌后的增量扫描，以及局域网与本机屏幕播放。

### 传输中心

经局域网 Web 入口在 Wi-Fi 上：概况与批次的上下行速度、影视中心一次上传整季剧集时的完整列表与状态栏文件名、几 GB 文件的上传速度、断开 Wi-Fi 后续传、重启产品服务后从头上传与临时文件回收，以及成员向他人个人空间上传被 ACL 拒绝。Caddy 默认不缓冲请求体，分片应直接流入产品服务。

### 应用中心与 Docker

- 安装一个新应用，确认 [ADR 0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md) 的版本与挂载检查放行正常安装；目录新增的 11 个应用逐个实机安装。
- 应用身份（[ADR 0022](../adr/0022-reach-shared-folders-only-as-the-app-identity.md)）：重新安装此前以 root 运行的 Immich，确认它以应用身份运行、手机备份可上传；安装 Navidrome 与 qBittorrent，核对只读与可写共享空间（[调查](../investigations/2026-10-14-app-containers-and-the-shared-acl.md)）。
- 安装 n8n 确认持续运行；安装 OpenList 并用应用中心显示的密码登录。
- 桌面快捷方式（#103）：管理员与成员的桌面都出现 Immich，点击打开 `:2283`，停止后标注“未运行”。

### 成员分享

在局域网 HTTP 浏览器上（不是 localhost）验收发送、接收、私聊与保存（[调查](../investigations/2026-10-16-member-sharing-http-send.md)），以及 #157 的并排窗口、接收者状态、按讨论合并的提醒和自己的消息不计未读。

### AI 助手（试用）

- 冷启动、内存、多人同时使用与开启 simple quota 的影响（[#116](https://github.com/zhongwater123/A-NAS/issues/116)）。
- VNAS 操作桌面（#156、#199）：删除、移动、改名、分享、复制、恢复、反馈与设置类的写入先问，新建、收藏与标签不问；终端、Docker 与应用中心每一步先问。
- 产品服务部署时同次更新试用 bundle（[试用手册](try-ai-assistant-on-experimental-nas.md)）。

## 验证

每完成一节，记下日期、release、结论与证据位置（PR 评论、规格的验收证据或调查记录），再删掉这一节。

## 回滚或恢复

验收发现的缺陷按[调查模板](../investigations/_TEMPLATE.md)记录；需要回到上一个 release 时按[实机配置手册](provision-v1.0.1-experimental-storage.md#回滚)执行。

## 关联

- 规格：见各节链接与[规格索引](../specs/README.md)
- ADR：[0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)、[0022](../adr/0022-reach-shared-folders-only-as-the-app-identity.md)
- 调查：见各节链接
- [当前状态](../status/CURRENT.md)
