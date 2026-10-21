# 局域网 Web 访问

状态：implemented（NAS 安装 Caddy 后启用）
更新时间：2026-10-21

## 目标

公司内网用户在浏览器打开 `http://<NAS 地址>` 即可登录 A-NAS Web 桌面，使用与本地控制台相同的全部功能。这是启用 HTTPS 前的过渡，明文传输的取舍见 [ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)。

## 非目标

- 不提供 HTTPS、证书管理、公网或跨互联网的远程访问。
- 不提供固定域名或局域网自动发现，用户按 IP 访问。
- 不提供与本地控制台不同的功能或权限，也不限制登录失败次数。

## 场景

### 从局域网登录

- Given：NAS 已安装 Caddy 并运行包含本功能的系统服务安装器，访问者的电脑能连接 NAS 的 TCP 80。
- When：访问者打开 `http://172.18.45.48` 并用 A-NAS 账号登录。
- Then：显示与本地控制台相同的 Web 桌面；会话 12 小时有效，过期后回到登录页并提示“登录已过期，请重新登录”。

### 使用全部功能

- Given：管理员或成员从局域网登录。
- When：使用文件、回收站、快照、相册、设置、Docker、应用中心或已启用的终端。
- Then：行为与本地控制台一致，可用功能只由账号角色决定。唯一的例外是尚未启用的设备：首个管理员只能在本机屏幕或经 SSH 隧道创建，见[基础存储与共享](basic-storage-and-sharing.md#角色与可见性)。

### 地址变化

- Given：DHCP 给 NAS 分配了新地址。
- When：访问者改用新地址打开。
- Then：无需修改任何配置即可登录和写入；仍停留在旧地址的页面需要重新打开。

### 电脑换了地址

- Given：访问者已从局域网登录。
- When：电脑换了网络，或 DHCP 给它分配了新地址（IPv6 在同一 /64 内换地址不算）。
- Then：下一个请求返回 `401`，回到登录页并提示“登录已过期，请重新登录”；重新登录即可继续。

### 打开已安装的应用

- Given：管理员或成员已登录，应用中心装有带网页的应用。
- When：打开应用的网页 `http://<NAS 地址>:<应用端口>`。
- Then：浏览器会把桌面的会话 Cookie 一并发给应用，但这个 Cookie 只在登录它的客户端上有效，应用无法用它访问 A-NAS；应用页面也不能嵌入桌面（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md) 决定 7）。

### 未安装 Caddy

- Given：NAS 没有安装 Caddy。
- When：运行系统服务安装器，或从局域网访问 80 端口。
- Then：安装器跳过 Caddy 配置；80 端口没有服务。本地控制台、SSH 隧道与 SMB 不受影响。

## 边界与失败

- Caddy 监听所有地址的 TCP 80，以明文 HTTP 转发到只监听回环的 `127.0.0.1:8080`，并为每个请求设置 `X-Forwarded-For`；配置见 [`deploy/caddy/Caddyfile`](../../deploy/caddy/Caddyfile)。
- 应用中心与 Docker 的写请求、终端握手接受 `localhost`、回环地址和任意 IP 字面量作为 `Host`，其他域名返回 403 以阻止 DNS 重绑定；浏览器 `Origin` 必须与 `Host` 同源。产品接口的写请求另需会话的 CSRF 令牌。
- 经 Caddy 的登录即使申请本机会话，也只得到 12 小时会话（[会话规则](basic-storage-and-sharing.md#角色与可见性)）。
- 会话绑定登录时的客户端：经 Caddy 登录的只接受同一 `X-Forwarded-For` 地址（IPv6 为同一 /64，链路本地地址须完全相同），直接回环登录的（本机屏幕、SSH 隧道）只接受没有转发头的回环连接；其他来源带着它请求返回 `401`，并在产品服务日志中记一条警告。这一规则生效前签发的局域网会话一律失效，本机会话视为直接回环签发。
- 同一请求带有多个 `anas_session` Cookie 时，产品服务依次尝试前 4 个，取第一个属于本客户端的。
- 桌面页面与静态资源返回 `X-Frame-Options: DENY` 与 CSP：`default-src 'self'`、`script-src 'self'`、`style-src 'self' 'unsafe-inline'`、`img-src 'self' data: blob:`、`media-src 'self' blob:`、`connect-src 'self' data: blob:`、`frame-src 'self'`、`object-src 'none'`、`base-uri 'none'`、`form-action 'self'`、`frame-ancestors 'none'`。`/api/` 与 `/healthz` 返回 `X-Frame-Options: SAMEORIGIN`，桌面仍可嵌入自己的 PDF 预览。
- 应用页面仍能覆盖或挤掉桌面的会话 Cookie，让用户回到登录页，但不能取得会话（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md) 后果）。
- 应用中心的安装策略拒绝 1024 以下的端口并保留 8080，应用不会占用入口端口（[ADR 0010](../adr/0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)）。
- 安装器在写入前用 `caddy validate` 校验配置，校验失败时停止且不改动现有配置；`/etc/caddy/Caddyfile` 第一次被替换前保存为 `/etc/caddy/Caddyfile.before-a-nas`。
- Caddy 停止或配置错误时局域网 Web 不可用，其他入口不受影响。
- 登录密码（同时是 SMB 密码）、会话 Cookie 与文件内容在局域网明文传输，只适用于设备所有者认可的可信内网。浏览器地址栏显示“不安全”，功能不受影响。

## 验收证据

- Host 校验：[`localorigin` 测试](../../internal/localorigin/localorigin_test.go)覆盖 IP 字面量放行与域名拒绝；[终端测试](../../internal/terminal/terminal_test.go)覆盖经入口以 IP 访问时可建立会话、以域名访问时被拒绝。
- 会话：[`TestProductAPIKeepsLocalConsoleSessionsUntilSignOut`](../../internal/httpapi/product_test.go)覆盖带转发头的登录只得到 12 小时会话。
- 会话绑定：[`TestProductAPIAcceptsSessionsOnlyFromTheClientThatSignedIn`](../../internal/httpapi/product_test.go)覆盖应用容器经 Caddy 重放、其他局域网地址、回环与局域网互用、本机会话经 Caddy 均返回 `401`，以及应用加的同名 Cookie 排在前面时仍用本人的会话；[账号测试](../../internal/accounts/session_client_test.go)覆盖 IPv6 /64、IPv4 映射地址与规则生效前的旧会话。
- 嵌入与 CSP：[`TestHandlerKeepsOtherOriginsFromFramingTheDesktop`](../../internal/webui/handler_test.go)覆盖桌面页面与接口的响应头；[助手 Markdown 测试](../../web/src/assistant/markdown.test.tsx)覆盖回答中的外部图片只显示为链接。
- 入口端口：[渲染测试](../../internal/appstore/render_test.go)的 `low port` 用例覆盖应用不能占用 80。
- 系统测试：[系统测试](../../scripts/system-test.sh)在装有 Caddy 的 Debian 13 容器中用真实安装器配置入口，验证经 80 端口健康检查、登录、上传与 12 小时会话，以及局域网会话不能经回环使用、本机会话不能经入口使用、经入口取得的桌面页面禁止嵌入。
- 实机：按[启用局域网 Web 访问](../runbooks/enable-lan-web-access.md)验证。

## 关联

- ADR：[0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)
- 规格：[基础存储与共享](basic-storage-and-sharing.md)、[Web 桌面终端](web-terminal.md)、[应用中心](app-center.md)
- 运行手册：[启用局域网 Web 访问](../runbooks/enable-lan-web-access.md)
