# 当前状态

更新时间：2026-11-24

本页是目录：只记当前事实、阻塞、待用户决定的事与下一步，每条一两行，细节在链接的文档中。使用者可见的变化见 [CHANGELOG](../../CHANGELOG.md)，版本范围见[发布记录](../releases/README.md)，决定的经过见[决定记录](../decisions/README.md)与 [ADR](../adr/README.md)。每次部署前的本页存为同目录下按安装日期命名的快照（如 [2026-11-10](2026-11-10.md)），规则见[文档系统](../README.md#写入规则)第 8 条。

## 当前阶段

主线是 [v1.0.1“实验 NAS 基础存储与共享闭环”](../releases/v1.0.1.md)。Experimental NAS 跟随主干部署，只用可丢弃测试数据；Web/SMB 闭环的实机验收完成前不创建 `v1.0.1` 标签。相册、影视、音乐、应用中心与 AI 助手已随主干部署，但不在 v1.0.1 的验收范围内。

## 实验 NAS 上运行什么

- 已部署：`a40311de890b`，软件包 `1.0.1~rc.6+dev20261119.123+ga40311de890b`（[ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)，2026-11-20 安装），apt 源是本机软件源的 `dev`，之后在[系统更新](../specs/system-update.md)中安装。最近的 RC 标签仍是 [`v1.0.1-rc.6`](../releases/v1.0.1.md#rc6-状态)。未部署的 PR：`git fetch` 后运行 `bash scripts/undeployed.sh`。
  - 宿主机依赖由 `a-nas-host` 与 `a-nas-console` 声明（[ADR 0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)）；`ifupdown-wait-online` 已停用。
  - 相册：排序从向量索引读（Qdrant 1.20.0，#411）；读文字用 llama.cpp b11655 与 Qwen3.5-4B，更正按内容对象存（Catalog 16，[ADR 0033](../adr/0033-keep-photo-text-corrections-per-content-object-with-a-change-log.md)）；AI 运行环境 `76a8ba1f3e6dfb14`；AI Worker 的内存上限为总内存减 2.5 GiB（13202 MiB）。
  - 家庭中心以 `a-nas-family` 运行（family.db 版本 3）；网页搜索已开启，SearXNG 以 `a-nas-search` 运行；温度与风扇转速由 `nct6775` 驱动读取；FFmpeg Worker 是 `anas-ffmpeg.socket`；Host Agent 的记录在 root 专属的 `/var/lib/a-nas-host`。
  - 虚拟伙伴的动画池 `36492963e4112f55`（A-NAS-assets 固定副本，#445）。
  - Caddy 2.11.9 来自 A-NAS 软件源，另装了 `wireguard-tools`；互联网入口关闭（`/etc/a-nas/internet.env` 不存在），第一版暂缓（[决定](../decisions/2026-11-09-home-network-first.md)）。
- AI 助手 V1：2026-11-19 由 `a-nas` 接管试用（[手册](../runbooks/enable-ai-assistant.md#从试用接管)），成员都能用。
- 剪辑中心由 drop-in `montage-demo.conf` 只在本机打开（[手册](../runbooks/demo-montage-center.md)）；配乐分析[手动装](../runbooks/enable-montage-music.md)，不随 release 发布。
- 照片打分模块以脚本装在本机（11-13 安装，11-17 更新，[手册](../runbooks/install-and-remove-photo-scoring.md)）。
- 数据卷是两块 ST500DM002 组成的 Btrfs raid1；一块盘坏了不再卸载整个数据卷（#441，2026-11-10 重启后核对，[调查](../investigations/2026-11-06-mirror-missing-a-disk.md)）。
- 本地控制台以专用账号 `a-nas-kiosk` 运行；`anas-dev` 名下的旧 Chromium 配置文件可以按[迁移后的验证](../runbooks/operate-local-kiosk.md#迁移后的验证)删除。屏保由 GPU 解码；停止渲染下层桌面的 [#315](https://github.com/zhongwater123/A-NAS/pull/315) 已部署，CPU 占用待复测（[调查](../investigations/2026-10-28-screensaver-video-cpu.md)）。
- 应用中心 2026-11-03 只装有思源笔记；[#300](https://github.com/zhongwater123/A-NAS/pull/300) 之前装的容器没有日志上限，重新安装才有。

## 阻塞与外部条件

- Samba 凭据在本人于“设置 → 我的账号”改密后重建：2026-11-04 核对（`pdbedit -L`），16 个账号只差 `admin`。
- 办公网段（Wi-Fi 与有线 10.24.x）到 NAS 的 TCP 445 不通、80 正常，拦截在公司网络或电脑的安全策略：SMB 从 NAS 所在的 172.18.45.0/24 测试，或请 IT 放行。
- Kiosk 的 URL、网络、快捷键、profile 生命周期与 VT 恢复尚未做产品级验收（[调查](../investigations/2026-10-06-kiosk-browser-confinement.md)）；运行中热插拔硬盘尚未实现。

## 待用户决定

逐条的清单在两个索引里，问题、选项与经过在各篇文档中：

- [决定索引](../decisions/README.md#索引)中标 `（pending）` 的决定记录。
- [ADR 索引](../adr/README.md#索引)中标 `（proposed）` 的 ADR，以及标 `（draft）` 的草稿 ADR 的 PR。

## 下一步

已部署功能的实机验收逐项写在验收手册，这里只有第 2 条指向它。

1. 完成 v1.0.1 闭环：重建 Samba 凭据后，按[实机配置与验收手册](../runbooks/provision-v1.0.1-experimental-storage.md#功能验收)完成全部验收项，全部通过后才打标签。
2. 按[已部署功能的实机验收手册](../runbooks/accept-features-on-experimental-nas.md)逐模块核对；每完成一节，就从手册中删去这一节。
3. Frank 的 Demo 样机上已安装的 OpenList 仍在用默认密码，需在应用中修改。
4. 处理架构审查 [#159](https://github.com/zhongwater123/A-NAS/issues/159) 与开发实践审查 [#217](https://github.com/zhongwater123/A-NAS/issues/217) 索引中仍开放的问题。
5. 家庭中心 M2：认领、隐藏（[规格](../specs/family-center.md)）。
6. 家庭局域网版的 NAS 侧（发现、配对、HTTPS）：等“开始做”（[决定](../decisions/2026-11-09-home-network-first.md)）。
7. 硬盘用途 M3：更改冗余方式、移除硬盘（[规格](../specs/disk-roles-and-redundancy.md)）。
8. 设备镜像：用存档密钥构建正式镜像，按[手册](../runbooks/build-device-image.md)写到真机试点；实验 NAS 改用镜像以 AI 助手 V1 验收通过为前提。
9. 剪辑中心：allin1 版权待议；二次编辑的界面按新交互重做，先写新的 ADR 与规格（[#632](https://github.com/zhongwater123/A-NAS/issues/632)），再拆 #596 的子 issue；导出与修改稿、重剪从修改稿出发两处要先定（[ADR 0039 的修订说明](../adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md#实现与后续修订2026-11-25)）；已有的服务端见[实施方案](../architecture/montage-center.md#出片后的二次编辑)。
10. 家庭待办：随下次发布部署到实验 NAS，再把验收项写进[已部署功能的验收手册](../runbooks/accept-features-on-experimental-nas.md)（[规格](../specs/family-todos.md)，#503）。
11. 墨水屏第一批（配对、客厅待办屏）：等[决定记录](../decisions/2026-11-27-eink-screens-first-batch.md)中的待定项定下再实现（[规格](../specs/eink-screens.md)，#503；定位与上线清单 #687）。
11. 共享工作区：M1（共享页面与 agent 修改）合入后部署并实机验收，再做 M2 的批注与版本对比（[规格](../specs/shared-workspace.md)）。
12. 后续能力：外部备份（[#27](https://github.com/zhongwater123/A-NAS/issues/27)，依赖 USB 存储 [#25](https://github.com/zhongwater123/A-NAS/issues/25) 或第二块硬盘）；账号数据导出（[#26](https://github.com/zhongwater123/A-NAS/issues/26)）；运行中热插拔与自动恢复；把密码校验与会话签发移到 Host Agent（[#98](https://github.com/zhongwater123/A-NAS/issues/98) 的 E01）。
