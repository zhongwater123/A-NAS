# 当前状态

更新时间：2026-11-12

本页是目录：只记当前事实、阻塞、待用户决定的事与下一步，每条一两行，细节在链接的文档中。使用者可见的变化见 [CHANGELOG](../../CHANGELOG.md)，版本范围见[发布记录](../releases/README.md)，决定的经过见[决定记录](../decisions/README.md)与 [ADR](../adr/README.md)。本页以前的内容由 Git 保存：`git log -p docs/status/CURRENT.md`。

## 当前阶段

主线是 [v1.0.1“实验 NAS 基础存储与共享闭环”](../releases/v1.0.1.md)。Experimental NAS 跟随主干部署，只用可丢弃测试数据；Web/SMB 闭环的实机验收完成前不创建 `v1.0.1` 标签。相册、影视、音乐、应用中心与 AI 助手已随主干部署，但不在 v1.0.1 的验收范围内。

## 实验 NAS 上运行什么

- 已部署：`61744597719c`（主干上 PR #441 的合并提交，未打标签，2026-11-10 安装，同日正常重启后复测通过；日志在 NAS 的 `/var/tmp/a-nas-*-61744597719c.log`），包含此前合并的全部 PR；容器代理与 AI 助手试用包同版本。最近的 RC 标签仍是 [`v1.0.1-rc.6`](../releases/v1.0.1.md#rc6-状态)。之后合入 main 的 PR：`git fetch` 后运行 `bash scripts/undeployed.sh`。
  - 宿主机软件包 `a-nas-host` 与 `a-nas-console`，版本 `0~61744597719c`（[ADR 0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)）；`ifupdown-wait-online` 已停用。
  - 相册：排序从向量索引读（Qdrant 1.20.0，#411）；读文字用 llama.cpp b11655 与 Qwen3.5-4B，更正按内容对象存（Catalog 16，[ADR 0033](../adr/0033-keep-photo-text-corrections-per-content-object-with-a-change-log.md)）；AI 运行环境 `842b388a0f1987f5`；AI Worker 的内存上限为总内存减 2.5 GiB（5150 MiB）。
  - 家庭中心以 `a-nas-family` 运行（family.db 版本 3）；网页搜索已开启，SearXNG 以 `a-nas-search` 运行；温度与风扇转速由 `nct6775` 驱动读取；FFmpeg Worker 是 `anas-ffmpeg.socket`；Host Agent 的记录在 root 专属的 `/var/lib/a-nas-host`。
  - 虚拟伙伴的动画池 `36492963e4112f55`（2026-11-10 安装，来自 A-NAS-assets 中固定的副本，#445）。
  - Caddy 是官方源的 2.11.9，另装了 `wireguard-tools`（2026-11-05，为互联网访问准备）；`/etc/a-nas/internet.env` 不存在，互联网入口关闭。
- AI 助手按[试用手册](../runbooks/try-ai-assistant-on-experimental-nas.md)为全部成员开启，用云端模型，仅供开发测试。
- 数据卷是两块 ST500DM002 组成的 Btrfs raid1（2026-10-20 加入第二块盘）。mount unit 带 `x-systemd.device-bound=no`，2026-11-10 重启后 `StopPropagatedFrom` 为空：一块盘坏了不再卸载整个数据卷（#441）。拔盘与更换的验收见下一步 10。
- 本地控制台以专用账号 `a-nas-kiosk` 运行；`anas-dev` 名下的旧 Chromium 配置文件可以按[迁移后的验证](../runbooks/operate-local-kiosk.md#迁移后的验证)删除。屏保由 GPU 解码；停止渲染下层桌面的 [#315](https://github.com/zhongwater123/A-NAS/pull/315) 已部署，CPU 占用待复测（[调查](../investigations/2026-10-28-screensaver-video-cpu.md)）。
- 应用中心 2026-11-03 只装有思源笔记；[#300](https://github.com/zhongwater123/A-NAS/pull/300) 之前装的容器没有日志上限，重新安装才有。

## 阻塞与外部条件

- Samba 凭据在本人于“设置 → 我的账号”改密后重建：2026-11-04 核对（`pdbedit -L`），16 个账号只差 `admin`。
- 办公网段（Wi-Fi 与有线 10.24.x）到 NAS 的 TCP 445 不通、80 正常，拦截在公司网络或电脑的安全策略：SMB 从 NAS 所在的 172.18.45.0/24 测试，或请 IT 放行。
- 互联网访问（[ADR 0032](../adr/0032-reach-the-web-desktop-from-the-internet-through-an-edge-relay.md)）：等 IT 把公网 IP 的 TCP 443 与 80 映射到机房的边缘服务器 `172.25.4.8` 并确认域名备案，等用户提供域名并添加 A 记录。边缘服务器 2026-11-05 已准备好隧道与防火墙，HAProxy 关闭。
- Kiosk 的 URL、网络、快捷键、profile 生命周期与 VT 恢复尚未做产品级验收（[调查](../investigations/2026-10-06-kiosk-browser-confinement.md)）；运行中热插拔硬盘尚未实现。

## 待用户决定

只列链接，问题、选项与经过在被链接的文档中。

- [活动记录保留多久](../decisions/2026-10-22-activity-record-retention.md)
- [FFmpeg Worker 的槽位、分层与内存上限](../decisions/2026-10-21-ffmpeg-worker-slots-and-memory.md)
- [相册视频实现时选的默认做法](../decisions/2026-10-20-photo-video-defaults.md)
- [文件管理的图片缩略图是否改用服务端生成](../decisions/2026-10-13-file-manager-image-thumbnails.md)
- [镜像缺盘的开机路径是否做成虚拟机回归测试](../decisions/2026-11-06-mirror-boot-vm-test.md)
- [助手的回答提到 Hermes 的 `/help` 与上手引导怎么处理](../decisions/2026-10-15-assistant-help-and-onboarding.md)
- [助手说“下次从笔记接着聊”的措辞是否调整](../decisions/2026-10-20-assistant-notes-wording.md)
- [家庭中心规格中补充的默认规则](../decisions/2026-10-29-family-center-default-rules.md)
- 虚拟伙伴：[V1 是否不含待办与专注计时](../decisions/2026-11-06-companion-v1-no-todo-focus.md)、[旧分支中的第三方素材怎么清除](../decisions/2026-11-06-companion-old-branches.md)
- 是否接受代码已部署的 ADR：[0017](../adr/0017-run-the-media-center-through-the-file-broker.md)、[0018](../adr/0018-index-music-in-place-through-the-file-broker.md)、[0019](../adr/0019-resume-uploads-over-tus-with-broker-held-files.md)、[0020](../adr/0020-read-service-logs-through-the-host-agent.md)、[0023](../adr/0023-isolate-member-share-copies-in-a-dedicated-service.md)、[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)、[0029](../adr/0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)、[0030](../adr/0030-ship-web-search-as-a-built-in-searxng-service.md)、[0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)。
- 是否接受 [ADR 0032](../adr/0032-reach-the-web-desktop-from-the-internet-through-an-edge-relay.md)（互联网访问，实现已合并，实机待启用）。
- 发布：[软件源放在哪里](../decisions/2026-11-11-update-hosting.md)
- 草稿 ADR 的 PR：局域网算力 PAIR 的隐私边界与下载（[#196](https://github.com/zhongwater123/A-NAS/pull/196)，ADR 0026）；语音处理的云端密钥与存储桶（[#203](https://github.com/zhongwater123/A-NAS/pull/203)，ADR 0027）。

## 下一步

1. 完成 v1.0.1 闭环：重建 Samba 凭据后，按[实机配置与验收手册](../runbooks/provision-v1.0.1-experimental-storage.md#功能验收)完成全部验收项，全部通过后才打标签。
2. 按[已部署功能的实机验收手册](../runbooks/accept-features-on-experimental-nas.md)逐模块核对；每完成一节，就从手册中删去这一节。成员分享重启窗口修复 [#400](https://github.com/zhongwater123/A-NAS/pull/400) 已部署，补验重启后的首次创建和保存。
3. 向量索引：相册排序从索引读（[读路径](../architecture/vector-index.md#读路径)），2026-11-10 已部署（#411）；按[手册第 5 步](../runbooks/enable-vector-index.md#步骤)核对。视频分段（#207）与文件分块（#208）的索引设计以[检索功能实测](../research/qdrant-retrieval-features.md#特性取舍)为准。
4. 网页搜索：按[启用手册](../runbooks/enable-web-search.md)查明空闲时的外发流量；#364 的修复已部署（#402），按 [#409](https://github.com/zhongwater123/A-NAS/issues/409) 观察多账号节奏与 512 MiB 资源占用（[步骤](../runbooks/enable-web-search.md#多账号节奏与资源验收364)）。
5. Frank 的 Demo 样机上已安装的 OpenList 仍在用默认密码，需在应用中修改。
6. 处理架构审查 [#159](https://github.com/zhongwater123/A-NAS/issues/159) 与开发实践审查 [#217](https://github.com/zhongwater123/A-NAS/issues/217) 索引中仍开放的问题。
7. 相册 OCR 步骤 6：在实验 NAS 上验收（[实施方案](../architecture/photo-ocr.md)）。步骤 1–5 已部署（2026-11-10，目录库第 16 号迁移）。VNAS 用读出的文字回答（[方案](../architecture/photo-ocr.md#助手的工具)）已实现，未部署。
8. 家庭中心：数据修复（[调查](../investigations/2026-11-05-family-data-integrity.md)，#403）与从相册加入频道（[PR #395](https://github.com/zhongwater123/A-NAS/pull/395)）2026-11-10 已部署；M2：认领、隐藏（[规格](../specs/family-center.md)）。
9. 互联网访问：外部条件满足后，按[启用互联网访问](../runbooks/enable-internet-access.md)从第 3 步继续。
10. 硬盘用途 M2（镜像缺盘继续服务、接回恢复、更换硬盘）2026-11-10 已部署，重启后 mount unit 不再随单块盘停止；还要按[镜像缺盘与更换硬盘](../runbooks/replace-a-mirror-disk.md)拔盘开机、接回与更换验收。M3（更改冗余方式、移除硬盘）未做（[规格](../specs/disk-roles-and-redundancy.md)）。
11. 虚拟伙伴（大肥鱼）V1（[#404](https://github.com/zhongwater123/A-NAS/issues/404)，[规格](../specs/virtual-companion.md)）：2026-11-10 已部署，动画已装；在实验 NAS 上测闲置时伙伴与成员 Hermes 实例的 CPU 与内存，并在 Safari 上核对透明效果。
12. 用户按[管理发布签名密钥](../runbooks/manage-release-key.md)生成存档密钥；再按[部署手册](../runbooks/deploy-a-nas-packages.md)把实验 NAS 换成软件包（第 4 步删去上游源）。
13. 后续能力：外部备份（[#27](https://github.com/zhongwater123/A-NAS/issues/27)，依赖 USB 存储 [#25](https://github.com/zhongwater123/A-NAS/issues/25) 或第二块硬盘，备份中心评审中最大的缺口）；账号删除（[#26](https://github.com/zhongwater123/A-NAS/issues/26)）；运行中热插拔与自动恢复；把密码校验与会话签发移到 Host Agent（[#98](https://github.com/zhongwater123/A-NAS/issues/98) 的 E01）。
