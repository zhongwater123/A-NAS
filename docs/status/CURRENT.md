# 当前状态

更新时间：2026-11-03

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
- 本机屏幕 2026-10-29 装上 VA-API 驱动 `intel-media-va-driver`，屏保改由 GPU 解码，播放时 kiosk 会话从约 0.87 个核降到约 0.53 个核（[调查](../investigations/2026-10-28-screensaver-video-cpu.md)）。剩下的开销与黑屏时约 0.27 个核的开销，大部分来自覆盖层下仍在渲染的桌面，要等 [#315](https://github.com/zhongwater123/A-NAS/pull/315) 部署后复测。

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
- 网页搜索（[规格](../specs/web-search.md)、[ADR 0030](../adr/0030-ship-web-search-as-a-built-in-searxng-service.md)，proposed）：内置 SearXNG，默认开启并由管理员开关，种子分类单独开关、默认关闭，版本随 release 固定（用户 2026-10-30 决定）。已合并（ADR 编号占位 [PR #334](https://github.com/zhongwater123/A-NAS/pull/334)）；接入 SearXNG 不需要密钥、Tor 或自建服务的全部 332 个引擎，分十个分类，结果按引擎轮流排列（用户 2026-11-02 决定）；结果随引擎回答陆续显示，约 1 秒出现第一批。为了不让出口地址被搜索网站封，每个引擎按人的节奏询问，被网站拦截后暂停 1 小时起、最长 1 天（用户 2026-11-02 要求）。夸克的解析错误记在 [#340](https://github.com/zhongwater123/A-NAS/issues/340)。部署前要用 `ANAS_SYSTEM_TEST_SEARCH=real` 跑一次系统测试，再按[启用手册](../runbooks/enable-web-search.md)在实验 NAS 上实测后调整。
- 家庭中心（宝宝频道）：M1 在 [PR #329](https://github.com/zhongwater123/A-NAS/pull/329) 中，尚未合并、部署（[规格](../specs/family-center.md)、[技术设计](../architecture/family-center.md)）；[ADR 0029](../adr/0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md) 为 proposed。部署要运行安装器，以新建 `a-nas-family` 身份与 `anas-family` 服务；系统测试中新增的家庭服务检查尚未运行。M2（认领、隐藏、从图库加入频道）待做。
- 进行中的 PR：局域网算力 PAIR 的隐私边界与下载（[#196](https://github.com/zhongwater123/A-NAS/pull/196)，草稿）；语音处理的云端密钥与存储桶（[#203](https://github.com/zhongwater123/A-NAS/pull/203)）。

## 下一步

1. 完成 v1.0.1 闭环：重建 Samba 凭据后，按[实机配置与验收手册](../runbooks/provision-v1.0.1-experimental-storage.md#功能验收)完成个人空间与 Shared 的 Web/SMB 双向读写、大文件哈希、删除与恢复、快照、正常重启、SMART、容量、服务与审计证据，全部通过后才打标签。
2. 按[已部署功能的实机验收手册](../runbooks/accept-features-on-experimental-nas.md)逐模块核对：Host Agent 与应用安装、文件管理与登录、备份与存储、桌面与本机屏幕、相册、影视与音乐、传输中心、应用中心、成员分享、AI 助手。每完成一节，就从手册中删去这一节。
3. 向量索引第一阶段部署后，按[启用向量索引](../runbooks/enable-vector-index.md)收集同步、对账与影子比对数据；视频分段（#207）与文件分块（#208）的索引设计以[检索功能实测](../research/qdrant-retrieval-features.md#特性取舍)为准。
4. Frank 的 Demo 样机上已安装的 OpenList 仍在用默认密码，需在应用中修改；#92 之后新装的应用密码按台随机生成。
5. 处理架构审查 [#159](https://github.com/zhongwater123/A-NAS/issues/159) 与开发实践审查 [#217](https://github.com/zhongwater123/A-NAS/issues/217) 索引中仍开放的问题。
6. 相册 OCR（2026-10-19 决定做，[规格](../specs/photo-library.md)、[模型评估](../research/photo-ocr-model-evaluation.md)）：模型是 Qwen3.5-4B 的 Q4_K_M。[实施方案](../architecture/photo-ocr.md)的步骤 1（交付与运行）已合并；步骤 2（分流）用照片已有的向量选出要读的照片（[分流评估](../research/photo-ocr-routing.md)），目标召回 99%（用户 2026-11-02 确认）。下一步是步骤 3：读取、判型与核对。
7. 后续能力：
   - 虚拟伙伴：当前分支已实现奶蛙、聊天为默认入口的紧凑小窝、聊天联动的设备内番茄钟与简单待办、桌宠单气泡与聊聊/更多操作栏、跟随角色的说话气泡、明确情绪原因、任务驱动情绪与动画衔接、网页小窝/电脑桌宠接续及复用 VNAS 的文本纪要入口、任务状态、原确认卡跳转和已确认文件预览；本机领域/API、双端接续与模拟 Agent 协议流程验证通过，处于原型阶段；音乐测试等待条件已修复，本轮聊天联动与 UI 完整 Web 652 项通过，typecheck/lint、Linux 产品构建和桌宠 renderer 构建通过；浏览器核对了聊天计时、待办追问、到点消息与桌宠弹层，无新增模型推理请求；上一轮 [PR #346](https://github.com/zhongwater123/A-NAS/pull/346) 已关闭未合并，其中保留了 Linux 产品鉴权、完整门禁、双端协议检查和跨平台试玩包；本轮已移除躲猫猫与送花，计时与待办按成员仅保存在当前设备，不等同于 NAS 后台任务；新交互只在本地 `codex/companion-experience-v2` 分支验证，尚未推送本轮 UI 变更；当前 Mac 已有 Fake 开发预览，日常玩法在本地运行；奶蛙已接通用交代框、服务端定时任务管理与每成员未读结果卡，支持新结果反馈、已读回执和另建提醒草稿；本机真实提醒与离线执行链路见[运行时记录](../research/companion-reminder-runtime-check.md)，宿主机为 Fake Adapter，真实 NAS 仍待验收；模型网关已补完整 Base URL 与回环本地无密钥模式的离线测试，Qwen Max/Flash 的 SDK 直连合成检查各四项通过（[证据与边界](../research/qwen-companion-capability-check.md)），项目网关经显式代理读取目录及双模型流式工具参数检查通过，当前 Mac 独立 Linux VM 已装固定 Hermes 运行时，两条一次性提醒均完成且各只运行一次，重新登录补报与服务端已读已核对；计数测试网关已关闭，独立手动试玩通道保持运行，测试任务无下次运行；已在 [#354](https://github.com/zhongwater123/A-NAS/issues/354) 登记实测与交接，并推送独立测试分支；本地已对齐 main `41464cad`，解决六处合并冲突、重新生成接口并修复预览回归的文件时间戳准备条件，前一验证提交 `0b3f0310` 的完整 Linux `make check` 与 `make race-test` 均已通过且强制启用 Qdrant，VM 内存已调整至 3 GiB 并恢复本地试玩；验证提交与资源条件见上述运行时记录；按用户要求先在本地完成验证，再通过 #354 补充交接，不新建 PR；之后由内网同事按[联调手册](../runbooks/accept-virtual-companion.md)验收真实模型、NAS 文件保存与 Electron 登录；触屏交互因硬件不支持列为待办（[规格](../specs/virtual-companion.md)）；
   - 外部备份（[#27](https://github.com/zhongwater123/A-NAS/issues/27)，依赖 USB 存储 [#25](https://github.com/zhongwater123/A-NAS/issues/25) 或第二块硬盘），是备份中心评审中最大的缺口；
   - 账号删除（[#26](https://github.com/zhongwater123/A-NAS/issues/26)）；
   - 硬盘用途 M2（缺盘降级挂载、更换、接回后恢复冗余）与 M3（更改冗余方式、移除硬盘）；
   - 运行中热插拔与自动恢复；
   - 把密码校验与会话签发移到 Host Agent（[#98](https://github.com/zhongwater123/A-NAS/issues/98) 的 E01）。
