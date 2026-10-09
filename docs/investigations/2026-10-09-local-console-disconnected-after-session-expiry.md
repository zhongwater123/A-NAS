# 本地控制台过夜后显示“连接中断”，需重启恢复

状态：fixed locally；Experimental NAS 部署验证待完成
更新时间：2026-10-09

## 症状与影响

Experimental NAS 在公司开机过夜后，本机屏幕上的 Web 桌面显示“连接中断”且不再恢复，设备所有者判断为网络抖动掉线，只能按电源键重启；重启后需要重新登录才恢复。2026-10-06 至 10-09 的 14 次关机中有 13 次通过电源键。本地控制台在重启前不可用；没有证据表明 SSH、SMB 或其他远程访问在同一时段中断。

## 最小复现

1. 在 [`App.test.tsx`](../../web/src/App.test.tsx)中以已登录会话渲染桌面，状态与指标轮询先返回成功。
2. 让两个轮询改为返回 `401 authentication_required`（与 12 小时会话过期后的后端响应一致），推进 2 秒触发下一次指标轮询。
3. 修复前 `npx vitest run src/App.test.tsx -t "session expires"` 稳定失败：页面停留在桌面，状态栏徽章和横幅显示“连接中断”，始终找不到“登录 A-NAS”。期望回到登录页并提示登录已过期。

## 观察事实

- 网卡为 Realtek RTL8125B（`10ec:8125` rev 05），使用 Debian 6.12.111 内核自带的 `r8169`，1 Gbps 全双工；网络由 ifupdown 与 dhcpcd 10.1.0 管理，租期 86400 秒。
- 10-08 18:15 至 10-09 08:51 的那次启动中，dhcpcd 在 18:15:30 获得租约后直到关机没有任何记录，没有 `carrier lost`；整夜没有 warning 及以上日志，也没有 `NETDEV WATCHDOG`、发送超时、AER 或 OOM。日志确认 08:51:51 为 `Power key pressed short` 后的正常关机，内核与 systemd 均在运行。
- 自 10-06 起所有启动中，`enp2s0: Link is Down` 只出现在关机时；dhcpcd 从未记录运行中掉线。
- 网卡与上游端口 `LnkCtl` 均为 `ASPM Disabled`，内核提示 OS 没有 ASPM 控制权。
- Kiosk 固定打开 `http://127.0.0.1:8080/?local-console=1`（[`run-kiosk.sh`](../../scripts/run-kiosk.sh)），本机屏幕的请求不经过网卡。
- 会话自登录起固定 12 小时有效、不续期（[`Authenticate`](../../internal/accounts/service.go)）；`/api/v1/host-state` 与 `/api/v1/metrics` 经 `withSession` 校验，失效后返回 `401 authentication_required`（[`product.go`](../../internal/httpapi/product.go)）。只有登录接口以 `401 invalid_credentials` 表示密码错误；已登录后的再次认证失败为 `403 reauthentication_failed`。
- 修复前前端只在页面加载时读取一次会话；[`useHostState`](../../web/src/useHostState.ts)与[`useHostMetrics`](../../web/src/useHostMetrics.ts)把任何非 2xx 视为连接中断并继续轮询，没有任何代码处理 `401`。
- 设备所有者确认重启后本机屏幕要求重新登录。登录时刻未写入系统日志；按 18:15 开机估算，会话约在 10-09 06:15 过期。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 网络抖动后 RTL8125/r8169 链路或驱动无法恢复 | 检查所有启动的内核链路事件、dhcpcd carrier 事件和发送超时 | rejected：运行中从未掉线或报错 |
| PCIe ASPM 省电导致网卡挂起 | `lspci -vvv` 检查网卡与根端口 `LnkCtl` | rejected：两端均为 ASPM Disabled |
| DHCP 租约到期或续租失败 | 租期与 dhcpcd 日志 | rejected：24 小时租期未用完且无相关事件 |
| 整机挂起 | wtmp 与关机日志 | rejected：电源键触发正常关机 |
| 会话过期后前端把 `401` 显示为连接中断 | 代码路径、最小复现与重启后需重新登录 | confirmed |

## 根因

产品会话固定 12 小时过期。过夜运行的本地控制台在过期后，每次状态和指标轮询都收到 `401`，前端却把它当作连接失败：保留桌面并持续显示“连接中断”，永远不回到登录页。重启让 Kiosk 重新加载页面，页面发现会话失效后才显示登录页，因此看起来像是“网络掉线、重启恢复”。网络与网卡在整个过程中正常。

## 修复与回归证据

- 修复：[`apiFetch`](../../web/src/api.ts)作为产品 API 的统一 fetch，已登录时遇到 `401` 通知 [`App`](../../web/src/App.tsx)；`App` 清除会话与 CSRF 令牌，回到登录页并提示“登录已过期，请重新登录”，重新登录后清除提示。状态、指标、通用请求、Docker、应用中心和终端状态请求均经过它；屏保清单为免认证资源，不经过它。
- 测试：新增“会话过期回到登录页”回归测试，覆盖过期后显示登录页、不显示连接中断、重新登录回到桌面；修复前失败，修复后 `make web-typecheck` 通过、`make web-test` 9 个文件 66 个测试全部通过。
- 产品决定（2026-10-09，设备所有者）：本机屏幕的登录在主动退出前一直保持。[`AuthenticateLocalConsole`](../../internal/accounts/service.go)为本机会话写入永不到达的到期时间，因此产品服务、File Broker 与相册服务沿用原有到期检查；[`authenticate`](../../internal/httpapi/product.go)只对无转发头的直接回环连接授予本机会话，前端仅在 `?local-console=1` 时申请。`TestProductAPIKeepsLocalConsoleSessionsUntilSignOut` 覆盖本机会话跨越 30 天仍有效、远程与带转发头的请求仍为 12 小时、读取会话续签 Cookie、退出后立即失效。
- 规格：[基础存储与共享](../specs/basic-storage-and-sharing.md#角色与可见性)记录会话期限与过期行为，[状态栏](../specs/host-metrics-status-bar.md)区分会话失效与连接中断。

## 后续工作

- 部署后在 Experimental NAS 本机屏幕重新登录一次以取得本机会话（部署前的会话仍为 12 小时），保持过夜并重启一次，确认仍为已登录桌面；再从 SSH 隧道浏览器登录，确认 12 小时后回到登录页并提示过期。
- 同次排查发现的独立问题：重启后 smbd 早于 DHCP 启动，只监听回环地址，需另行修复。

## 关联

- 规格：[基础存储与共享](../specs/basic-storage-and-sharing.md)、[主机指标状态栏](../specs/host-metrics-status-bar.md)、[本地控制台屏保](../specs/local-console-screensaver.md)
- ADR：[本地控制台](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- 运行手册：[运行本地控制台](../runbooks/operate-local-kiosk.md)
- 测试：[`App.test.tsx`](../../web/src/App.test.tsx)
