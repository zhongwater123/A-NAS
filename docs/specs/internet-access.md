# 互联网访问

状态：draft
更新时间：2026-11-05

## 目标

设备所有者与成员在公司外，用浏览器打开 `https://<域名>` 登录 A-NAS Web 桌面，使用除终端以外与局域网相同的功能；以后的手机 App 使用同一个入口。路径与取舍见 [ADR 0032](../adr/0032-reach-the-web-desktop-from-the-internet-through-an-edge-relay.md)。

## 非目标

- 不提供 SMB、SSH 或已安装应用网页的外网访问。
- 不提供“应用内配置外网访问”的界面：启用、换域名与停用由 root 按[运行手册](../runbooks/enable-internet-access.md)完成。
- 不改变局域网入口：局域网仍是 `http://<NAS 地址>`，规则见[局域网 Web 访问](lan-web-access.md)。
- 不做双因素登录、设备令牌或手机 App 的登录方式，以后单独设计。

## 场景

### 从外网登录

- Given：已按手册启用互联网访问，域名指向 IT 映射给边缘服务器的公网 IP。
- When：成员在公司外打开 `https://<域名>` 并登录。
- Then：浏览器显示受信任的证书，桌面与局域网相同；会话 12 小时有效，过期后回到登录页。“设置 → 日志”中这次登录记为“外网 · <客户端地址>”。

### 外网不能用终端

- Given：管理员已开启终端，从外网登录。
- When：打开“设置 → 终端”。
- Then：页面说明“通过外网访问时不能使用终端，请在局域网或本机屏幕上打开”，不尝试连接；直接请求终端的 WebSocket 返回 `403 terminal_internet`。

### 外网打开已安装的应用

- Given：应用中心装有带网页的应用，成员从外网登录。
- When：查看桌面上的应用图标、应用中心的详情。
- Then：桌面与启动器中的应用图标标为“仅局域网”，点击不打开网页；应用中心只显示“已安装”，不提供“打开”。应用的网页在设备地址的其他端口上，只能在局域网中打开。

### 手机换了网络

- Given：成员在手机浏览器上从外网登录。
- When：手机从 Wi-Fi 换到移动网络，公网地址随之变化。
- Then：会话继续有效，不需要重新登录。

### 两种会话不能互用

- Given：同一个人分别从外网和局域网登录过。
- When：把外网会话的 Cookie 用在局域网入口，或反过来。
- Then：返回 `401`，产品服务日志记一条“session refused from another client”警告。

### 猜密码

- Given：有人从外网反复尝试登录。
- When：同一地址（IPv6 为同一 /64）对同一账号连续失败 5 次，或对所有账号共失败 20 次；或者同一账号来自所有外网地址的连续失败超过 10 次。
- Then：之后的尝试返回 `429` 与 `Retry-After`，等待时间从 30 秒加倍到 15 分钟。按账号的等待只针对外网，同一账号在局域网与本机屏幕上照常登录；成功登录清除该地址的计数，按账号的计数在最后一次失败 24 小时后清除。

### 隧道或边缘服务器故障

- Given：互联网访问已启用。
- When：边缘服务器停机，或 NAS 到它的隧道中断。
- Then：外网打不开；局域网入口、本地控制台与 SMB 不受影响。NAS 重启时即使隧道还没建立，Caddy 也照常启动。隧道恢复后外网自动可用。

### 用其他名称访问

- Given：互联网访问已启用。
- When：有人按公网 IP 或其他域名访问 443，或访问 80。
- Then：443 上 SNI 不是该域名的连接被边缘服务器直接关闭；80 一律 301 跳转到 `https://<域名>`。

## 边界与失败

- 配置只有 `/etc/a-nas/internet.env` 一处：`ANAS_INTERNET_HOST`（域名）、`ANAS_INTERNET_RELAY_ENDPOINT`（边缘服务器的 `IPv4:端口`）、`ANAS_INTERNET_RELAY_PUBLIC_KEY`（边缘服务器的 WireGuard 公钥）与 `ANAS_INTERNET_TUNNEL_PREFIX`（隧道的 ULA /64）。安装器校验四项，任何一项不合法时以退出码 2 停止，不改动系统。
- 文件存在时，安装器还要求：
  - 已装 Caddy，且带 `caddy.listeners.proxy_protocol` 模块，即官方源的 2.7 或更新版本；
  - 已装 `wg`、`wg-quick` 与 `nft`。

  缺少时以退出码 3 停止，打印要安装的软件包。删除该文件后再运行安装器，就停用互联网入口：安装器移除 Caddy 站点、产品服务的互联网端口、防火墙表与隧道，保留 NAS 的隧道密钥。
- NAS 的隧道私钥在 `/etc/wireguard/wg-anas.key`（仅 root 可读），首次启用时生成，之后不变；安装器每次都打印对应的公钥。
- 产品服务的互联网端口只接受回环地址（`ANAS_INTERNET_HTTP_ADDR=127.0.0.1:8081`），并且必须与 `ANAS_INTERNET_HOST` 同时设置。这个端口上 Host 不是该域名的请求返回 `421 misdirected_request`。经它的登录不会得到本机会话，也不能启用设备。
- 外网会话的 Cookie 为 `HttpOnly; SameSite=Strict; Secure`；Caddy 为外网站点返回 `Strict-Transport-Security: max-age=31536000`。
- NAS 对隧道接口 `wg-anas` 只放行来自边缘服务器、发往 TCP 443 的连接，并丢弃经隧道转发的流量；Caddy 只接受来自边缘服务器隧道地址的 PROXY 头，其他来源带 PROXY 头的连接被拒绝。
- 边缘服务器的防火墙只允许公网地址访问 TCP 80 与 443。公司内网的流量（堡垒机 SSH、监控代理、NAS 的隧道）不受限制。
- 应用中心的安装策略保留端口 8080 与 8081。

## 验收证据

- 入口：[`webentry` 测试](../../internal/webentry/webentry_test.go)覆盖互联网端口标记请求、其他 Host 返回 `421`；[`localorigin` 测试](../../internal/localorigin/localorigin_test.go)覆盖写请求只在互联网入口接受该域名。
- 会话与 Cookie：[`TestProductAPIBindsInternetSessionsToTheInternetEntry`](../../internal/httpapi/product_test.go)覆盖外网会话换地址仍有效、不能在局域网或回环使用、局域网会话不能经互联网入口使用、Cookie 带 `Secure`，以及互联网端口上不签发本机会话、不能启用设备。
- 登录限速：[登录测试](../../internal/accounts/signin_test.go)覆盖外网按地址退避，以及按账号的外网退避不影响局域网登录。
- 终端：[终端测试](../../internal/terminal/terminal_test.go)覆盖互联网入口返回 `403 terminal_internet`，状态接口报告 `internet: true`。
- 配置：[配置测试](../../cmd/anas-api/config_test.go)覆盖端口与域名必须同时设置、端口只能是回环地址、域名格式。
- 界面：[设置测试](../../web/src/settings/SettingsPanel.test.tsx)覆盖外网时终端页的说明且不建立会话；[应用地址测试](../../web/src/appcenter/model.test.ts)覆盖按域名打开桌面时没有应用网址；[日志文案测试](../../web/src/settings/logs/describe.test.ts)覆盖“外网 · 地址”。
- 系统测试：[系统测试](../../scripts/system-test.sh)在容器中以 `internet.env` 运行真实安装器，生成隧道配置、防火墙表与 Caddy 站点；然后以 PROXY 头经 HTTPS 登录，验证健康检查、客户端地址、`Secure` Cookie、HSTS、终端被拒、外网会话不能经局域网入口使用，以及删除配置后停用。
- 实机：按[启用互联网访问](../runbooks/enable-internet-access.md)验证。

## 关联

- 架构：[架构总览](../architecture/OVERVIEW.md)不变量 9
- ADR：[0032](../adr/0032-reach-the-web-desktop-from-the-internet-through-an-edge-relay.md)、[0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)
- 运行手册：[启用互联网访问](../runbooks/enable-internet-access.md)
- 代码：[`internal/webentry`](../../internal/webentry/webentry.go)、[`deploy/caddy/Caddyfile.internet`](../../deploy/caddy/Caddyfile.internet)、[`deploy/internet`](../../deploy/internet/anas-internet.nft)、[`deploy/edge`](../../deploy/edge/setup-edge.sh)
