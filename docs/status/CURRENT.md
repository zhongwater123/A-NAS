# 当前状态

更新时间：2026-11-04

本页只记当前事实与下一步。使用者可见的变化见 [CHANGELOG](../../CHANGELOG.md)，版本范围见[发布记录](../releases/README.md)，合并与部署的经过由 Git 与 PR 保存。

## 当前阶段

主线是 [v1.0.1“实验 NAS 基础存储与共享闭环”](../releases/v1.0.1.md)。Experimental NAS 跟随主干部署，只用可丢弃测试数据；Web/SMB 闭环的实机验收完成前不创建 `v1.0.1` 标签。相册、影视、音乐、应用中心与 AI 助手已随主干部署，但不在 v1.0.1 的验收范围内。

## 实验 NAS 上运行什么

- 已部署：`70853075ca40`（主干上 PR #383 的合并提交，未打标签，2026-11-04 安装，日志在 NAS 的 `/var/tmp/a-nas-deploy-70853075ca40.log`），包含此前合并的全部 PR；容器代理与 AI 助手试用包同版本。最近的 RC 标签仍是 [`v1.0.1-rc.6`](../releases/v1.0.1.md#rc6-状态)。
  - 宿主机软件包 `a-nas-host` 与 `a-nas-console` 已装上，版本 `0~70853075ca40`（[ADR 0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)，#385）；装的时候 apt 没有下载其他软件包。
  - 相册：向量索引（Qdrant 1.20.0，[#266](https://github.com/zhongwater123/A-NAS/pull/266)）；读文字用 llama.cpp b11655 与 Qwen3.5-4B（#332、#348），后台只读被选出的照片，读出的文字存进 Catalog 15（#355）。AI 运行环境 `842b388a0f1987f5`，AI Worker 的内存上限为总内存减 2.5 GiB（#321，实验 NAS 上 5150 MiB）。
  - 家庭中心以 `a-nas-family` 运行（#329）；网页搜索已开启，SearXNG 以 `a-nas-search` 运行（#333，[启用手册](../runbooks/enable-web-search.md)）；温度与风扇转速由 `nct6775` 驱动读取（#325）。
  - 解码沙箱改名为 FFmpeg Worker（`anas-ffmpeg.socket`，#305）。Host Agent 的记录在 root 专属的 `/var/lib/a-nas-host`（#249）。
- AI 助手按[试用手册](../runbooks/try-ai-assistant-on-experimental-nas.md)为全部成员开启，用云端模型，仅供开发测试。
- 数据卷是两块 ST500DM002 组成的 Btrfs raid1（2026-10-20 在 #149 的早期构建上加入第二块盘）。
- 已合并、尚未部署的 PR 不在本页列出：`git fetch` 后运行 `bash scripts/undeployed.sh`，由 Git 列出“已部署”之后合入 main 的 PR。
- 本地控制台自 2026-10-28 以专用账号 `a-nas-kiosk` 运行（[#174](https://github.com/zhongwater123/A-NAS/issues/174)）。`anas-dev` 名下的旧 Chromium 配置文件还在；kiosk 已随 2026-11-04 的重启重新启动过，可以按[迁移后的验证](../runbooks/operate-local-kiosk.md#迁移后的验证)删除。
- 应用中心 2026-11-03 只装有思源笔记。[#300](https://github.com/zhongwater123/A-NAS/pull/300) 之前装的容器没有日志上限，卸载后重新安装才有；应用数据可以丢弃。镜像仓库限流时，安装会重试拉取（#317，[调查](../investigations/2026-10-28-registry-rate-limit-failed-install.md)），还没有在实机上遇到过。
- 本机屏幕 2026-10-29 装上 VA-API 驱动 `intel-media-va-driver`，屏保改由 GPU 解码，播放时 kiosk 会话从约 0.87 个核降到约 0.53 个核（[调查](../investigations/2026-10-28-screensaver-video-cpu.md)）。剩下的开销与黑屏时约 0.27 个核的开销，大部分来自覆盖层下仍在渲染的桌面。停止渲染下层的 [#315](https://github.com/zhongwater123/A-NAS/pull/315) 已随 rc.6 部署，待复测。

## 阻塞与外部条件

- Samba 凭据在 ADR 0008 升级时已删除，本人在“设置 → 我的账号”改密后重建。2026-11-04 核对（`pdbedit -L`）：16 个账号中 15 个已有凭据，只差 `admin`。
- 办公网段到 NAS 的 TCP 445 不通，而 80 端口正常：Wi-Fi 网段如此，2026-11-04 从有线的 10.24.x 网段复测也如此；拦截发生在公司网络或电脑的安全策略。SMB 要从 NAS 所在的 172.18.45.0/24 网段测试，或请 IT 放行。
- 重启后 smbd 仍只监听回环，v1.0.1 验收第 6 步不能通过。2026-11-04 装上 `a-nas-host`、启用 `ifupdown-wait-online.service` 后重启复测：网络等待只看 ifupdown 的接口记录，比 `enp2s0` 拿到 DHCP 地址早约 10 秒结束，smbd 随即按 `bind interfaces only` 启动（[调查](../investigations/2026-11-04-smbd-starts-before-dhcp-address.md)）。修法待定；在此之前，每次重启后由 root 执行 `systemctl restart smbd`。
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
- 网页搜索（[规格](../specs/web-search.md)、[ADR 0030](../adr/0030-ship-web-search-as-a-built-in-searxng-service.md)，proposed）：内置 SearXNG，默认开启并由管理员开关，种子分类单独开关、默认关闭，版本随 release 固定（用户 2026-10-30 决定）。已合并（ADR 编号占位 [PR #334](https://github.com/zhongwater123/A-NAS/pull/334)）；接入 SearXNG 不需要密钥、Tor 或自建服务的全部 332 个引擎，分十个分类，结果按引擎轮流排列（用户 2026-11-02 决定）；结果随引擎回答陆续显示，约 1 秒出现第一批。为了不让出口地址被搜索网站封，每个引擎按人的节奏询问，被网站拦截后暂停 1 小时起、最长 1 天（用户 2026-11-02 要求）。夸克的解析错误记在 [#340](https://github.com/zhongwater123/A-NAS/issues/340)。部署前要用 `ANAS_SYSTEM_TEST_SEARCH=real` 跑一次系统测试，再按[启用手册](../runbooks/enable-web-search.md)在实验 NAS 上实测后调整。
- 家庭中心（宝宝频道）：M1 已合并（[PR #329](https://github.com/zhongwater123/A-NAS/pull/329)）并随 rc.6 部署（[规格](../specs/family-center.md)、[技术设计](../architecture/family-center.md)）；[ADR 0029](../adr/0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md) 仍为 proposed。M2（认领、隐藏、从图库加入频道）待做。
- 宿主机依赖（[ADR 0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)，proposed）：依赖散在向导、安装器、测试镜像与几本手册中，已经不一致；改由 `a-nas-host` 与 `a-nas-console` 两个 Debian 包声明。ADR 与实现已合并（[PR #381](https://github.com/zhongwater123/A-NAS/pull/381)、[PR #385](https://github.com/zhongwater123/A-NAS/pull/385)），两个包已随 `70853075ca40` 装上。ADR 中启用网络等待来修 smbd 的部分在实机上没有生效，smbd 的修法待定（见[调查](../investigations/2026-11-04-smbd-starts-before-dhcp-address.md)中的可选做法）。
- 进行中的 PR：局域网算力 PAIR 的隐私边界与下载（[#196](https://github.com/zhongwater123/A-NAS/pull/196)，草稿）；语音处理的云端密钥与存储桶（[#203](https://github.com/zhongwater123/A-NAS/pull/203)）。

## 下一步

1. 完成 v1.0.1 闭环：重建 Samba 凭据后，按[实机配置与验收手册](../runbooks/provision-v1.0.1-experimental-storage.md#功能验收)完成个人空间与 Shared 的 Web/SMB 双向读写、大文件哈希、删除与恢复、快照、正常重启、SMART、容量、服务与审计证据，全部通过后才打标签。
2. 按[已部署功能的实机验收手册](../runbooks/accept-features-on-experimental-nas.md)逐模块核对：Host Agent 与应用安装、文件管理与登录、备份与存储、桌面与本机屏幕、相册、影视与音乐、传输中心、应用中心、成员分享、AI 助手。每完成一节，就从手册中删去这一节。
3. 向量索引第一阶段部署后，按[启用向量索引](../runbooks/enable-vector-index.md)收集同步、对账与影子比对数据；视频分段（#207）与文件分块（#208）的索引设计以[检索功能实测](../research/qdrant-retrieval-features.md#特性取舍)为准。
4. Frank 的 Demo 样机上已安装的 OpenList 仍在用默认密码，需在应用中修改；#92 之后新装的应用密码按台随机生成。
5. 处理架构审查 [#159](https://github.com/zhongwater123/A-NAS/issues/159) 与开发实践审查 [#217](https://github.com/zhongwater123/A-NAS/issues/217) 索引中仍开放的问题。
6. 相册 OCR（2026-10-19 决定做，[规格](../specs/photo-library.md)、[模型评估](../research/photo-ocr-model-evaluation.md)）：模型是 Qwen3.5-4B 的 Q4_K_M。[实施方案](../architecture/photo-ocr.md)的步骤 1（交付与运行）与步骤 2（分流，目标召回 99%）已合并。步骤 3（读取、判型、核对、存储与开关）已实现，2026-11-04 随 `70853075ca40` 部署：单字概率取 0.99（用户 2026-11-03 确认），约一半字段自动通过，其中真正读错约 0.3%。下一步是步骤 4：搜索与接口。
7. 后续能力：
   - 外部备份（[#27](https://github.com/zhongwater123/A-NAS/issues/27)，依赖 USB 存储 [#25](https://github.com/zhongwater123/A-NAS/issues/25) 或第二块硬盘），是备份中心评审中最大的缺口；
   - 账号删除（[#26](https://github.com/zhongwater123/A-NAS/issues/26)）；
   - 硬盘用途 M2（缺盘降级挂载、更换、接回后恢复冗余）与 M3（更改冗余方式、移除硬盘）；
   - 运行中热插拔与自动恢复；
   - 把密码校验与会话签发移到 Host Agent（[#98](https://github.com/zhongwater123/A-NAS/issues/98) 的 E01）。
