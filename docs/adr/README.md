# 架构决策记录

ADR 记录难以逆转、未来读者无法仅凭代码理解且存在真实权衡的决定。普通依赖升级、易撤销配置和实现细节不创建 ADR。

## 索引

- [0001：产品服务和 Host Agent 使用 Go](0001-use-go-for-product-services.md)
- [0002：客户端产品接口使用 REST 和 OpenAPI](0002-use-rest-openapi-for-product-clients.md)
- [0003：Web 桌面使用 React 和 TypeScript](0003-use-react-typescript-for-web-desktop.md)
- [0004：只读 Host Agent 状态使用 Unix Socket 上的 HTTP/JSON](0004-use-http-json-over-unix-socket-for-host-state.md)（已被 0007、0008 修订）
- [0005：本地控制台使用单应用 Wayland Kiosk](0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- [0006：相册使用受管图库与不可变内容对象](0006-use-a-managed-photo-library.md)
- [0007：基础存储使用单盘 Btrfs、SQLite 与类型化特权边界](0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- [0008：统一 Linux 身份，以文件系统 ACL 作为唯一授权来源](0008-use-unified-linux-identities-and-filesystem-acls.md)（应用容器部分由 0022 修订）
- [0009：容器使用 Docker Engine 并经专用容器代理访问](0009-use-docker-engine-through-a-dedicated-container-agent.md)
- [0010：应用中心使用内置审查清单与安装策略](0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)（运行身份段落已被 0022 取代）
- [0011：相册以专用服务身份独占受管存储，并在 Catalog 中授权](0011-run-the-photo-library-as-a-dedicated-service-identity.md)
- [0012：局域网经 Caddy 以明文 HTTP 开放 Web 桌面（HTTPS 前的过渡）](0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)
- [0013：Docker 网络只从 A-NAS 指定的地址池分配](0013-allocate-docker-networks-from-an-a-nas-address-pool.md)
- [0014：屏保视频是系统盘上的长期媒体，不随 release 发布](0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)
- [0015：Docker 与 Caddy 跟随上游官方软件源，最低能力由代码检查](0015-take-docker-and-caddy-from-their-upstream-repositories.md)
- [0016：相册 AI 随系统内置安装，设备不联网获取模型](0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- [0017：影视中心经文件代理读取媒体，FFmpeg 在隔离 Worker 中运行](0017-run-the-media-center-through-the-file-broker.md)（proposed）
- [0018：音乐原地索引普通文件，经文件代理以成员身份读取](0018-index-music-in-place-through-the-file-broker.md)（proposed）
- [0019：上传经 tus 分片到达，写入文件代理以用户身份创建的临时文件](0019-resume-uploads-over-tus-with-broker-held-files.md)（proposed）
- [0020：系统日志经 Host Agent 的白名单 journal 查询读取](0020-read-service-logs-through-the-host-agent.md)（proposed）
- 0021：本机屏幕默认是 Web 桌面，可切换到 KDE Plasma 桌面模式（draft，没有 PR：2026-10-13 起只在开发机的本地分支 `claude/home-third-party-app-download-e6f0e6` 上（提交 `aa6c684`），没有推送；编号保留给它）
- [0022：应用容器只以应用身份接触共享空间](0022-reach-shared-folders-only-as-the-app-identity.md)（修订 0008 的应用容器实现，取代 0010 的运行身份段落）

- [0023：成员分享的固定副本由独立服务持有](0023-isolate-member-share-copies-in-a-dedicated-service.md)（proposed）

- [0024：AI 助手以每个账号一个沙箱中的 Hermes Agent 运行](0024-run-a-sandboxed-hermes-agent-per-member.md)（proposed）

- [0025：数据卷以 Btrfs 自带的多盘配置跨越多块硬盘](0025-span-the-data-volume-across-disks-with-btrfs-profiles.md)（修订 0007 的单盘限制）
- 0026：VNAS 经 NAS 上的 PAIR 节点使用家里电脑的算力（draft，[PR #196](https://github.com/zhongwater123/A-NAS/pull/196)）
- 0027：语音处理在云端转写与整理，声纹只在本机计算和保存（draft，[PR #203](https://github.com/zhongwater123/A-NAS/pull/203)）

- [0028：向量索引由所属服务各自的 Qdrant 实例承担，随时可从 Catalog 重建](0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)（proposed；修订 0011 的数据位置：派生的向量索引放在系统盘）

- [0030：网页搜索由随系统安装的 SearXNG 服务提供](0030-ship-web-search-as-a-built-in-searxng-service.md)（proposed）

## 格式

文件名使用 `NNNN-short-title.md`。正文首先用一至三句话说明背景、决定和原因；只有确有价值时再增加状态、备选项或后果。状态行紧接标题、写在正文之前，以 proposed、accepted 或 superseded 开头；`make docs-check` 读这一行与索引的标记核对，状态行写在第一个小节之前的其他位置时报错。

新 ADR 取索引中最大的编号加一，占位的编号也算。ADR 写在分支上时，先以单独的文档 PR 在索引中占位：一行 `- NNNN：标题（draft，PR #N）`，不加链接；ADR 合并时把占位换成正式条目。`make docs-check` 在两篇 ADR 同号、同一编号占位两次，或编号已有 ADR 文件而占位还在时报错。

决定被替代时保留原文件，在顶部标记 `superseded` 并链接新 ADR。
