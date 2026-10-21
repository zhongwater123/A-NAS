# 0012：局域网经 Caddy 以明文 HTTP 开放 Web 桌面（HTTPS 前的过渡）

状态：accepted；行为见[局域网 Web 访问规格](../specs/lan-web-access.md)，启用步骤见[启用局域网 Web 访问](../runbooks/enable-lan-web-access.md)。

此前 Web 只监听 `127.0.0.1`，只能经本地控制台或 SSH 隧道使用；成员账号没有 SSH 登录权限，其他部门的同事只能使用 SMB。设备在可预见的时间内只部署在公司内网，设备所有者希望内网用户用浏览器使用全部功能，而目前取得浏览器信任的证书成本较高。我们决定由 Caddy 在 80 端口以明文 HTTP 向局域网提供 Web 桌面，产品服务仍只监听回环；设备所有者明确接受明文传输的风险，作为启用 HTTPS 前的过渡。

## 决定

1. **入口**：Caddy（软件源见 [ADR 0015](0015-take-docker-and-caddy-from-their-upstream-repositories.md)）监听所有地址的 TCP 80，以 `reverse_proxy` 转发到 `127.0.0.1:8080`，不启用自动 HTTPS。绑定全部地址而不是局域网 IP，因此开机时不依赖 DHCP 先完成，不会出现 Samba `bind interfaces only` 那样只绑定回环的问题。
2. **产品服务仍只监听回环**：局域网请求全部经 Caddy 进入，并带有 Caddy 设置的 `X-Forwarded-For`。应用中心安装的第三方容器经 Docker 网桥访问不到产品服务。
3. **开放全部功能**：局域网浏览器与本地控制台使用同一 Web UI、同一产品 API 和同一组账号权限，包括管理员的账号、存储、Docker、应用中心和启用后的终端。
4. **Host 校验**：应用中心、Docker 的写请求与终端原先只接受回环 Host，现在接受 `localhost`、回环地址和任意 IP 字面量。DHCP 换址不需要改配置；DNS 重绑定页面的 Host 是攻击者的域名，仍被拒绝。按域名访问需要以后扩展允许列表。
5. **会话**：局域网登录为 12 小时会话。不过期的本机会话只授予没有转发头的直接回环连接，经 Caddy 的请求永远拿不到。
6. **可选能力**：系统服务安装器只在 `caddy` 已安装时校验并写入 A-NAS 的 Caddyfile、启用服务，首次覆盖前备份原文件。应用中心的安装策略本来就拒绝 1024 以下的端口，应用不会占用 80。
7. **应用网页与桌面同站，会话 Cookie 单独无用**（2026-10-21，[issue #161](https://github.com/zhongwater123/A-NAS/issues/161)）：已安装应用在同一地址的其他端口提供网页（[应用中心规格](../specs/app-center.md#打开与管理已安装应用)）。Cookie 不区分端口，SameSite 也不区分，打开应用时浏览器会把桌面会话 Cookie 发给应用，应用页面也能嵌入桌面。明文 HTTP 下没有只发往一个端口的 Cookie，按 IP 访问也没有另一个主机名，所以不靠站点隔离，而是：
   - 会话绑定登录时的客户端：经 Caddy 登录的只接受同一 `X-Forwarded-For` 地址（IPv6 为同一 /64）的请求，直接回环登录的（含本机会话）只接受没有转发头的回环连接。应用拿到 Cookie 后只能经 Caddy 重放，来源是容器地址；它也连不到只监听回环的产品服务。
   - 桌面页面返回 `X-Frame-Options: DENY` 与包含 `frame-ancestors 'none'` 的 CSP，接口返回 `X-Frame-Options: SAMEORIGIN`（桌面自己嵌入 PDF 预览）。CSP 的 `img-src` 只允许本机地址、`data:` 与 `blob:`，模型回答中的外部图片不会让成员的浏览器连外（[ADR 0024](0024-run-a-sandboxed-hermes-agent-per-member.md)）。
   - 应用页面在用户浏览器中发出的请求带着用户自己的 Cookie，但读不到响应：写请求要 CSRF 令牌，WebSocket 握手要求 `Origin` 与 `Host` 同源。

## 后果

- 登录密码（同时是 SMB 密码）、会话 Cookie 与文件内容在局域网明文传输，同网段或路径上的设备可以截获并冒用会话。管理员会话能管理 Docker 与应用中心，被冒用接近完全控制设备。设备所有者明确接受这些风险，本决定只适用于可信内网。
- 浏览器地址栏显示“不安全”。前端没有使用仅限安全上下文的浏览器 API，功能不受影响。
- 新增 `caddy` 系统服务及其配置的发布与安装链路。Caddy 停止时局域网 Web 不可用，本地控制台、SSH 隧道与 SMB 不受影响。
- 访问地址是 DHCP 分配的 IP，地址变化后用户需要使用新地址。
- 切换到 HTTPS 时只改 Caddyfile 的站点地址与证书，并把会话 Cookie 标记为 `Secure`；产品服务与访问路径不再迁移。此后明文 HTTP 的应用端口不再收到会话 Cookie，以 HTTPS 提供网页的应用仍会收到，决定 7 的绑定继续生效。
- 客户端地址变化（换网络、DHCP 换址）后要重新登录。同一 NAT 之后的设备共用一个地址，彼此之间没有这层保护。
- 应用仍能用同名 Cookie 覆盖或挤掉桌面会话，让用户回到登录页，但不能借此取得会话。产品服务依次尝试请求中的同名 Cookie（最多 4 个），应用设置的更长路径 Cookie 不会遮住用户自己的。

## 未选择

- 产品服务直接监听 `0.0.0.0:8080`：组件更少，但 Docker 网桥上的应用容器也能访问产品 API，本机会话失去“经代理”这一判断依据，80 端口还需给非特权服务额外能力，上 HTTPS 时还要再迁移一次。
- 立即启用 HTTPS（公司内部 CA、公司域名证书或 Caddy 内部 CA）：安全性更好，但当前取得受信任证书的成本高；Caddy 内部 CA 需要每台客户端安装根证书，否则用户会习惯忽略证书警告。
- 局域网只开放成员的文件功能：能避免管理员会话明文暴露，但设备所有者要求局域网可使用全部功能。
- 保持仅回环加 SSH 隧道：成员没有 SSH 账号，其他部门无法使用 Web。
- 由 Caddy 代理应用的网页端口并剥离会话 Cookie：应用可以把浏览器引到它发布的其他端口，或用另一个指向本机的主机名打开自己的端口，Cookie 照样到达；Caddy 配置还要随应用安装变化，跨越容器代理与入口两个权限边界。
- 应用使用独立主机名：需要局域网 DNS 或 mDNS，而用户按 IP 访问；应用端口在任何指向本机的主机名上都能访问，同样拦不住被引到桌面主机名下的应用端口。
- 会话令牌改放在页面存储、以请求头发送：图片、视频、下载链接与 WebSocket 都带不了请求头，要整体改成签名地址，令牌还会暴露给页面脚本。

## 关联

- [局域网 Web 访问规格](../specs/lan-web-access.md)
- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [Web 桌面终端规格](../specs/web-terminal.md)
- [本地控制台决策](0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- [架构总览](../architecture/OVERVIEW.md)
