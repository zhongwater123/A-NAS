# 当前状态

更新时间：2026-10-28

本页只记当前事实与下一步。使用者可见的变化见 [CHANGELOG](../../CHANGELOG.md)，版本范围见[发布记录](../releases/README.md)，合并与部署的经过由 Git 与 PR 保存。

## 当前阶段

主线是 [v1.0.1“实验 NAS 基础存储与共享闭环”](../releases/v1.0.1.md)。Experimental NAS 跟随主干部署，只用可丢弃测试数据；Web/SMB 闭环的实机验收完成前不创建 `v1.0.1` 标签。相册、影视、音乐、应用中心与 AI 助手已随主干部署，但不在 v1.0.1 的验收范围内。

## 实验 NAS 上运行什么

- 已部署：`4ec62e17c3b1`（主干上 PR #298 的合并提交，2026-10-28 安装，日志在 NAS 的 `/var/tmp/a-nas-deploy-4ec62e17c3b1.log`），包含此前合并的全部 PR；容器代理与 AI 助手试用包同版本。相册服务的向量索引已启用（Qdrant 1.20.0，相册 Catalog 为 14，[#266](https://github.com/zhongwater123/A-NAS/pull/266)），AI 运行环境为 `842b388a0f1987f5`。Host Agent 的记录在 root 专属的 `/var/lib/a-nas-host`（#249）。
- AI 助手按[试用手册](../runbooks/try-ai-assistant-on-experimental-nas.md)为全部成员开启，用云端模型，仅供开发测试。
- 数据卷是两块 ST500DM002 组成的 Btrfs raid1（2026-10-20 在 #149 的早期构建上加入第二块盘）。
- 已合并、尚未部署的 PR 不在本页列出：`git fetch` 后运行 `bash scripts/undeployed.sh`，由 Git 列出“已部署”之后合入 main 的 PR。
- 本地控制台自 2026-10-28 以专用账号 `a-nas-kiosk` 运行（[#174](https://github.com/zhongwater123/A-NAS/issues/174)）。`anas-dev` 名下的旧 Chromium 配置文件还在，kiosk 至少重启过一次后按[迁移后的验证](../runbooks/operate-local-kiosk.md#迁移后的验证)删除。
- 应用中心里的 Immich、OpenList 与思源笔记装于 [#300](https://github.com/zhongwater123/A-NAS/pull/300) 之前，容器没有日志上限，卸载后重新安装才有；它们的数据可以丢弃。2026-10-28 重装 Immich 时 GHCR 的一次限流（429）让安装失败；拉取重试部署前，重装仍可能这样失败（[调查](../investigations/2026-10-28-registry-rate-limit-failed-install.md)）。

## 阻塞与外部条件

- Samba 凭据在 ADR 0008 升级时已删除，管理员与成员各自在“设置 → 我的账号”改密后才会重建；在此之前无法做 Windows SMB 验收。
- 公司 Wi-Fi 网段到 NAS 的 TCP 445 不通，而 80 端口正常；拦截发生在公司网络或电脑的安全策略。SMB 要从 NAS 所在的有线网段复测，或请 IT 放行。
- 重启后 smbd 早于 DHCP 拿到地址启动，因 `bind interfaces only` 只监听回环：`enp2s0` 为 `allow-hotplug`，`ifupdown-wait-online.service` 未启用。根治前，每次重启后由 root 执行 `systemctl restart smbd`。
- Caddy 官方的 Cloudsmith 源对软件包索引返回 `402`（[caddyserver/caddy#8184](https://github.com/caddyserver/caddy/issues/8184)），实验 NAS 暂用 Debian 的 Caddy 2.6.2，并已删除该源。
- Kiosk 的 URL、网络、快捷键、profile 生命周期与 VT 恢复尚未做产品级验收（[调查](../investigations/2026-10-06-kiosk-browser-confinement.md)）；运行中热插拔硬盘尚未实现。

## 待用户决定

- ADR [0017](../adr/0017-run-the-media-center-through-the-file-broker.md)、[0018](../adr/0018-index-music-in-place-through-the-file-broker.md)、[0019](../adr/0019-resume-uploads-over-tus-with-broker-held-files.md)、[0020](../adr/0020-read-service-logs-through-the-host-agent.md)、[0023](../adr/0023-isolate-member-share-copies-in-a-dedicated-service.md)、[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 仍为 proposed，代码已合并并部署。
- 审计记录的保留期限（规格为永久保存，#167）。
- FFmpeg Worker 的槽位数、分层与 3G 共享内存上限，目前是估计值（#168）。
- 相册视频实现时定下的默认值：格式、4 GiB 上限、封面帧、单独 MOV 的处理、幻灯片跳过视频、AI 助手看不到视频（[规格](../specs/photo-video.md)）。
- 文件管理的缩略图仍加载原图，是否改用服务端缩略图。
- 助手的模型仍会提到 Hermes 的 `/help` 与上手引导，如何处理。
- 向量索引 [ADR 0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)（proposed）：引入 Qdrant 作为各服务可重建的派生索引，推翻了 #66 “不引入独立向量库”的决定。第一阶段只做同步与影子比对；相册读路径何时切换，等实机影子比对的数据再定。
- 进行中的 PR：相册 OCR 的决定（[#141](https://github.com/zhongwater123/A-NAS/pull/141)）；局域网算力 PAIR 的隐私边界与下载（[#196](https://github.com/zhongwater123/A-NAS/pull/196)，草稿）；语音处理的云端密钥与存储桶（[#203](https://github.com/zhongwater123/A-NAS/pull/203)）。

## 下一步

1. 完成 v1.0.1 闭环：重建 Samba 凭据后，按[实机配置与验收手册](../runbooks/provision-v1.0.1-experimental-storage.md#功能验收)完成个人空间与 Shared 的 Web/SMB 双向读写、大文件哈希、删除与恢复、快照、正常重启、SMART、容量、服务与审计证据，全部通过后才打标签。
2. 按[已部署功能的实机验收手册](../runbooks/accept-features-on-experimental-nas.md)逐模块核对：Host Agent 与应用安装、文件管理与登录、备份与存储、桌面与本机屏幕、相册、影视与音乐、传输中心、应用中心、成员分享、AI 助手。每完成一节，就从手册中删去这一节。
3. 向量索引第一阶段部署后，按[启用向量索引](../runbooks/enable-vector-index.md)收集同步、对账与影子比对数据；视频分段（#207）与文件分块（#208）的索引设计以[检索功能实测](../research/qdrant-retrieval-features.md#特性取舍)为准。
4. Frank 的 Demo 样机上已安装的 OpenList 仍在用默认密码，需在应用中修改；#92 之后新装的应用密码按台随机生成。
5. 处理架构审查 [#159](https://github.com/zhongwater123/A-NAS/issues/159) 与开发实践审查 [#217](https://github.com/zhongwater123/A-NAS/issues/217) 索引中仍开放的问题。
6. 后续能力：
   - 外部备份（[#27](https://github.com/zhongwater123/A-NAS/issues/27)，依赖 USB 存储 [#25](https://github.com/zhongwater123/A-NAS/issues/25) 或第二块硬盘），是备份中心评审中最大的缺口；
   - 账号删除（[#26](https://github.com/zhongwater123/A-NAS/issues/26)）；
   - 硬盘用途 M2（缺盘降级挂载、更换、接回后恢复冗余）与 M3（更改冗余方式、移除硬盘）；
   - 运行中热插拔与自动恢复；
   - 把密码校验与会话签发移到 Host Agent（[#98](https://github.com/zhongwater123/A-NAS/issues/98) 的 E01）。
