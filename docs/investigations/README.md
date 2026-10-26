# 调查记录

调查记录保存复杂缺陷的证据链，使后续会话不必重复排查。根因不能由报错直接看出、问题跨越组件边界、曾出现错误假设或可能复发时创建记录；简单拼写和局部显然错误只由测试与提交记录覆盖。

## 索引

- [2026-10-26：未识别块设备阻断整次宿主机观测](2026-10-26-unidentified-device-blocks-host-state.md)（fixed locally，#183）
- [2026-10-06：本地 Kiosk 可进入通用 Chromium 界面](2026-10-06-kiosk-browser-confinement.md)（open, deferred for developer baseline）
- [2026-10-06：Host Agent 无法创建运行时 Socket](2026-10-06-host-agent-runtime-directory.md)（resolved）
- [2026-10-06：桌面时钟持续落后于系统时间](2026-10-06-system-clock-drift-and-network-sync.md)（resolved）
- [2026-10-07：RC 激活与回滚后用户 API 无法启动](2026-10-07-user-service-state-directory.md)（resolved）
- [2026-10-07：首次管理员创建在 Samba 系统账号边界失败](2026-10-07-samba-account-sandbox.md)（resolved）
- [2026-10-07：空白磁盘计划导致本地桌面蓝屏](2026-10-07-blank-disk-plan-ui-crash.md)（resolved）
- [2026-10-07：数据卷空间权限阻断文件闭环](2026-10-07-data-volume-space-permissions.md)（superseded by ADR 0008）
- [2026-10-08：ADR 0008 首次升级时 Host Agent 无法启动](2026-10-08-adr0008-bootstrap-private-acl.md)（resolved）
- [2026-10-08：Experimental NAS 的 Docker 与应用中心保持禁用](2026-10-08-experimental-nas-containers-disabled.md)（resolved）
- [2026-10-08：OpenList 安装后因数据目录权限反复重启](2026-10-08-openlist-runtime-identity.md)（resolved）
- [2026-10-08：Host Agent 在 systemd 沙箱下丢失 CAP_SETUID](2026-10-08-host-agent-loses-setuid-under-systemd.md)（fix in PR #39；Experimental NAS verification pending）
- [2026-10-09：本地控制台过夜后显示“连接中断”，需重启恢复](2026-10-09-local-console-disconnected-after-session-expiry.md)（fixed locally；Experimental NAS verification pending）
- [2026-10-09：安装 Immich 后内网 WiFi 客户端全部无法访问 NAS](2026-10-09-app-network-cut-off-wifi-clients.md)（resolved）
- [2026-10-09：Debian 的 Compose 2.26.1 把应用的整个卷挂进容器](2026-10-09-old-compose-mounted-whole-app-volumes.md)（resolved）
- [2026-10-13：AI Worker 留着被取消的检测会话，内存涨到上限](2026-10-13-ai-worker-kept-cancelled-detector-sessions.md)（resolved；随 `bb1ec71bff65` 部署）
- [2026-10-13：评估把 Open Images 的大部分“有”读成了“没有”](2026-10-13-open-images-positives-read-as-absent.md)（resolved for 物体检测；AI 聚合的 Open Images 部分待重做）
- [2026-10-13：闲时检测只用了约 1 个核](2026-10-13-idle-detection-used-one-core.md)（fixed locally；Experimental NAS verification pending）

- [2026-10-14：文件管理没有视频缩略图，HEIC 照片不能预览](2026-10-14-file-media-previews.md)（fixed locally；Experimental NAS verification pending）
- [2026-10-14：应用容器拿不到共享空间的组权限，root 镜像绕过 ACL](2026-10-14-app-containers-and-the-shared-acl.md)（fixed locally；Experimental NAS verification pending）

- [2026-10-14：Apple Live Photo 配对标识被 EXIF 库遗漏](2026-10-14-live-photo-identifiers.md)（fixed locally）

- [2026-10-14：视频缩略图重访重复解码与队列阻塞](2026-10-14-file-preview-cache.md)（fixed locally；Experimental NAS verification pending）
- [2026-10-20：加入数据卷的硬盘在重启前仍显示为未使用](2026-10-20-added-disk-shown-unused-until-reboot.md)（fixed locally in PR #149；Experimental NAS verification pending）

- [2026-10-15：剧集改名与旧扫描交错导致身份丢失](2026-10-15-media-rename-scan-race.md)（fixed locally）
- [2026-10-15：Host Agent 每次请求都读 SMART，读盘超时被杀](2026-10-15-host-agent-smart-on-every-request.md)（fixed locally；Experimental NAS verification pending）

- [2026-10-15：分享接入后的会话目录权限与服务恢复](2026-10-15-member-sharing-service-startup.md)（fixed；2026-10-21 部署的 `8f5b6b0b4586` 包含修复，待实机验收）

- [2026-10-16：局域网 HTTP 点击发送分享无反应](2026-10-16-member-sharing-http-send.md)（fixed locally；待部署验收）

- [2026-10-20：VNAS 自建的技能不出现在“记忆”中](2026-10-20-assistant-skills-hidden-by-name.md)（fixed locally；待部署验收）
- [2026-10-21：VNAS 长对话中窗口崩溃，回答过的确认卡片堆在输入框上方](2026-10-21-vnas-window-crash-and-stacked-cards.md)（fixed locally；崩溃的确切报错待取得，待部署验收）

- [2026-10-21：存储计划执行绑定浏览器请求，被打断的建卷无法恢复](2026-10-21-storage-plan-bound-to-browser-request.md)（fixed locally；Experimental NAS verification pending）
- [2026-10-21：过期的目录对账扫描改写回收站行，文件夹无法恢复](2026-10-21-stale-reconcile-scan-rewrote-trash-rows.md)（fixed；2026-10-21 部署，待实机验收）

- [2026-10-21：快照清单超过 1 MiB 即失败，并留下孤儿子卷](2026-10-21-snapshot-listing-cut-at-one-mebibyte.md)（fixed；2026-10-21 部署，待实机验收）
- [2026-10-21：安装失败的应用显示为“已安装”，容器代理重启打断安装](2026-10-21-failed-app-install-shown-as-installed.md)（fixed locally；Experimental NAS verification pending）
- [2026-10-21：媒体 Worker 短暂不可用让 HEIC 与视频的缩略图和 AI 永久失败](2026-10-21-media-worker-shared-budget.md)（fixed locally；Experimental NAS verification pending）

- [2026-10-21：每次列目录对账整个空间，大空间下列目录与影视扫描以十秒计](2026-10-21-whole-space-reconcile-scale.md)（fixed locally；待部署验收）

- [2026-10-26：smbstatus 的告警混进 JSON，禁用账号与启动时的身份同步失败](2026-10-26-smbstatus-warning-fails-identity-sync.md)（fixed locally in PR #261；Experimental NAS verification pending）

## 使用方式

从 [`_TEMPLATE.md`](_TEMPLATE.md) 创建 `YYYY-MM-DD-short-title.md`。调查期间持续区分观察事实和假设；解决后必须记录根因、修复机制和回归证据。
