# 0032：经机房边缘服务器与 NAS 主动建立的隧道，从互联网开放 Web 桌面

状态：proposed（2026-11-05）；行为见[互联网访问规格](../specs/internet-access.md)，启用步骤见[启用互联网访问](../runbooks/enable-internet-access.md)。修订 [ADR 0012](0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md) 中“只适用于可信内网”的范围：局域网入口不变，另加一个只走 HTTPS 的互联网入口。

设备所有者希望自己和成员在公司外用浏览器使用 A-NAS，以后还有手机 App。实验 NAS 在公司 NAT 之后：只有 DHCP 分配的内网地址 `172.18.45.48`，出网经多条线路、按目标换公网地址，没有公网 IPv6，外面没有能连进来的地址。公司机房有一台可用的 Ubuntu 24.04 虚拟机（下称边缘服务器），IT 可以给它映射一个公网 IP；它没有外网出口，只能经堡垒机与 MFA 登录，上面运行着 IT 的监控代理。2026-11-05 实测：NAS 到边缘服务器的 UDP 能直接到达，NAS 到它的 TCP 22 被网段间的访问控制拦下。

我们决定：公网的 TCP 443 由 IT 映射到边缘服务器，边缘服务器按 SNI 原样转发到 NAS 主动建立的 WireGuard 隧道，HTTPS 在 NAS 上的 Caddy 终止，再交给产品服务专门的回环端口。外网会话可以使用除终端以外的全部功能。

## 决定

1. **路径**：互联网客户端 → `https://<域名>` → 公司防火墙（只映射 TCP 443 与 80）→ 边缘服务器的 HAProxy → WireGuard 隧道 → NAS 上 Caddy 在隧道地址监听的 443 → 产品服务的互联网监听端口 `127.0.0.1:8081`。
   - HAProxy 的 443 工作在四层：只转发 TLS 握手中 SNI 为该域名的连接，其他连接直接关闭；每个连接前加 PROXY v2 头，NAS 由此得到客户端的真实地址；每个来源地址 10 秒内最多 60 个新连接。
   - 80 端口由 HAProxy 自己回复 301 跳转到 `https://<域名>`，**从不转发**：NAS 的 80 是局域网的明文桌面。证书验证因此只用 TLS-ALPN，经 443 完成。
2. **证书与解密只在 NAS 上**。边缘服务器只看到密文；它被攻破时，攻击者能做的只是丢弃流量，或者像任何互联网客户端一样连接 NAS 的 443。Caddy 用 ACME 自动申请与续期证书，并为该站点返回 HSTS（不含子域名，域名可能还有公司的其他服务）。
3. **隧道由 NAS 主动建立**。IT 不需要开“边缘服务器访问内网”的规则；NAS 换了 DHCP 地址，隧道照常建立；以后把中转换成云服务器，NAS 这边不用改。
   - 隧道内的地址取自随机生成的 IPv6 ULA /64（边缘服务器是 `::1`，NAS 是 `::2`），不会与公司的 IPv4 网段冲突（[ADR 0013](0013-allocate-docker-networks-from-an-a-nas-address-pool.md) 的教训）。
   - WireGuard 只允许边缘服务器以 `::1` 一个地址发送；NAS 的防火墙对隧道接口只放行 TCP 443，也不转发隧道流量。即使边缘服务器被攻破，也碰不到 NAS 的 SSH、SMB 与应用端口。
4. **入口由监听端口区分，而不是请求头**。产品服务为互联网入口单独监听一个回环端口，Caddy 的互联网站点只代理到这里，所以“这个请求来自互联网”不依赖任何可以伪造或遗漏的请求头。这个端口上：
   - `Host` 必须是配置的域名，否则返回 `421`；
   - 写请求的 Host 校验接受该域名（DNS 重绑定页面的 Host 是攻击者的域名，仍被拒绝）；
   - 不签发本机会话，不能启用设备，不能打开终端。
5. **外网会话只在互联网入口有效，不按 IP 绑定**。[ADR 0012](0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md) 决定 7 按客户端地址绑定局域网会话，是因为已安装应用与桌面同站、会收到会话 Cookie。经域名访问时这一前提不成立：浏览器只把这个域名的 Cookie 发给这个域名，而边缘服务器只转发 443。手机换基站、换网络地址就会变，按 IP 绑定会让人频繁掉登录，所以外网会话绑定的是“互联网入口”，不是地址；局域网会话的绑定不变，两种会话不能互用。外网的会话 Cookie 带 `Secure`。
6. **外网登录在现有限速之上再按账号限速**。现有的按客户端退避（[#175](https://github.com/zhongwater123/A-NAS/issues/175)）对外网按客户端地址计算（IPv6 按 /64）。此外，同一账号来自互联网的连续失败超过 10 次后，对这个账号的每次外网尝试都要等待，从 30 秒加倍到 15 分钟，挡住分散在许多地址上的猜测。这项限制只作用于外网登录，攻击者无法借此把账号在本机屏幕或局域网上锁住。
7. **外网能用的功能**：与局域网相同，只有终端例外（设备所有者 2026-11-05 决定）。已安装应用的网页在设备地址的其他端口上，外网到不了，所以桌面在外网不提供“打开应用网页”。SMB 与 SSH 都不对外。
8. **一处配置，安装器收敛**。设备上的 `/etc/a-nas/internet.env` 只写域名、边缘服务器的地址与公钥、隧道网段四项。存在这个文件时，系统服务安装器负责以下各项：
   - 生成 NAS 的隧道密钥（只生成一次），并打印公钥供边缘服务器使用；
   - 写入 WireGuard 配置、Caddy 的互联网站点、产品服务的环境与隧道接口的防火墙表；
   - 打开 IPv6 非本地绑定。隧道没起来时 Caddy 照样启动，局域网入口不受影响。

   互联网站点需要 Caddy 内置的 `proxy_protocol`（2.7 起），安装器在缺少它时以退出码 3 停止。所以 Caddy 必须来自官方源（[ADR 0015](0015-take-docker-and-caddy-from-their-upstream-repositories.md)），Debian 的 2.6.2 不行。边缘服务器的配置与启用手册放在仓库的 [`deploy/edge`](../../deploy/edge/setup-edge.sh)。

## 后果

- 登录页暴露在互联网上。密码同时是 SMB 密码，强度更重要；登录限速与审计日志是主要防线，日志把这些登录记为“外网”及其地址。
- 公司内的人用域名访问时，流量也会绕经边缘服务器。远程访问的速度受公司线路与边缘服务器的限制。
- 外网访问依赖 IT 的映射与备案：公司线路上没备案的域名可能用不了 80/443。边缘服务器或隧道故障时，只有互联网入口不可用，局域网入口、本地控制台与 SMB 不受影响。
- 管理边缘服务器要经堡垒机与 MFA。它上面有 IT 的监控代理，所以它的防火墙只限制来自公网地址的流量，不采用默认全部拒绝。
- 应用中心的安装策略保留端口 8081，与 8080 一样，避免应用占用产品服务的互联网监听端口。
- 产品服务多一个监听端口与一组只对它生效的规则。新增的接口在外网同样可用，除非像终端一样明确拒绝。

## 未选择

- **组网 VPN（Tailscale、Headscale 或自建 WireGuard 接入每台设备）**：不在公网暴露任何端口，产品几乎不用改。但每台设备都要装客户端，手机同时只能开一个 VPN，以后的手机 App 也得要求用户先连 VPN。
- **公司防火墙直接映射到 NAS**：少一台服务器，但 NAS 直接暴露在公网上，需要固定的 NAS 地址；IT 要开放的是内网主机，而不是机房里的一台中转。
- **在边缘服务器上终止 HTTPS**：证书更好管理，但边缘服务器会看到密码、Cookie 与文件内容。它面向互联网，比 NAS 更可能被攻破。
- **边缘服务器经公司内网直接转发到 NAS，不建隧道**：少一个组件，但要 IT 开放机房到 NAS 网段的访问，NAS 的地址也必须固定。
- **frp 等内网穿透工具**：UDP 能通，WireGuard 在内核中、配置更少；frp 留作 UDP 被封时的备选。
- **用 Caddy 设置的请求头标记外网请求**：改动更小，但入口的判断取决于每个站点的头部配置是否写对；独立端口在结构上就不会混淆。
- **外网会话也按 IP 绑定**：能挡住在别处重放的 Cookie。但在 HTTPS 与 `HttpOnly`、`SameSite=Strict` 的 Cookie 下，这种窃取的途径很少，代价却是手机用户频繁掉登录。

## 关联

- 规格：[互联网访问](../specs/internet-access.md)、[局域网 Web 访问](../specs/lan-web-access.md)、[Web 桌面终端](../specs/web-terminal.md)
- ADR：[0012](0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)、[0013](0013-allocate-docker-networks-from-an-a-nas-address-pool.md)、[0015](0015-take-docker-and-caddy-from-their-upstream-repositories.md)、[0031](0031-declare-host-dependencies-in-debian-packages.md)
- 运行手册：[启用互联网访问](../runbooks/enable-internet-access.md)
- 代码：[`internal/webentry`](../../internal/webentry/webentry.go)、[`deploy/caddy/Caddyfile.internet`](../../deploy/caddy/Caddyfile.internet)、[`deploy/edge`](../../deploy/edge/setup-edge.sh)、[系统服务安装器](../../scripts/install-v1.0.1-system-services.sh)
