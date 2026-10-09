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
- [0008：统一 Linux 身份，以文件系统 ACL 作为唯一授权来源](0008-use-unified-linux-identities-and-filesystem-acls.md)
- [0009：容器使用 Docker Engine 并经专用容器代理访问](0009-use-docker-engine-through-a-dedicated-container-agent.md)
- [0010：应用中心使用内置审查清单与安装策略](0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)
- [0011：相册以专用服务身份独占受管存储，并在 Catalog 中授权](0011-run-the-photo-library-as-a-dedicated-service-identity.md)
- [0012：局域网经 Caddy 以明文 HTTP 开放 Web 桌面（HTTPS 前的过渡）](0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)

## 格式

文件名使用 `NNNN-short-title.md`。正文首先用一至三句话说明背景、决定和原因；只有确有价值时再增加状态、备选项或后果。

决定被替代时保留原文件，在顶部标记 `superseded` 并链接新 ADR。
