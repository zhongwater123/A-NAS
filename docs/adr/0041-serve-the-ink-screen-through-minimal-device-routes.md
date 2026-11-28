# 0041：水墨屏只经产品服务的三条最小 GET 接口拿画面，每块屏一个家庭级设备令牌，刷新裁决由服务端一处决定

状态：proposed（2026-11-27 起草）

水墨屏（M1）是一块连在家庭局域网里、不登录、只定时来问「要不要刷」的硬件：它把服务端给的两张黑白图贴上，并回报电量、断线等状态。这带来三件难以逆转的事：要给这块硬件一种只能读屏幕画面、报刷新状态的最小凭据；凭据要在局域网明文 HTTP 上用；刷不刷、怎么刷的裁决只能有一处，屏固件只认 none/partial/full。我们决定：产品服务新增第六种路由姿态 `device`，只放行三条 GET 设备接口；每块屏一个家庭级设备令牌，只存哈希、不绑账号；刷新裁决做成服务端唯一的纯函数，屏的运行时状态与上报存产品服务自己的 `device.db`。

## 决定

1. **第六种路由姿态 `device`。** 在 `apiroute.Posture` 的 `agent` 之后新增 `device`（同步 `String()`、`checkPosture` 与 `guard` 的边界与分支）。它只认 `Authorization: Bearer` 的设备令牌，不认 Cookie、不要 CSRF；只放行 GET，且只放行三条设备路径（`/api/device/next`、`/screen.bin`、`/screen-bar.bin`），其余方法与路径由守卫直接拒绝。带 `Origin` 的请求一律拒绝；拒绝互联网请求（屏只在家庭局域网里，与 agent 允许互联网不同），`Host` 必须指向本机。产品服务先核验令牌，它自己就是这三条接口的最终处理者，没有下游服务再确认。

2. **设备令牌是家庭级凭据，不绑账号。** `control.db` 新表 `device_tokens`（在 `agent_tokens` 之后追加一个迁移步骤）只存令牌的 SHA-256 哈希、屏名、白天轮询间隔、创建与上次使用时间；没有 `user_id`。前缀 `anas_dev_`，90 天未用的令牌失效。屏是全家共用的资源，不随账号停用、重置密码或删除而撤销；只能由管理员签发、列出、撤销，令牌只返回一次，签发/列出/撤销走新的管理员接口，仿 `accountsapi` 的 agent 令牌接口。产品服务复用账号服务的令牌哈希、按客户端限流与闲时失效。

3. **三条 GET 接口由产品服务自己实现，不单独起服务。** `/api/device/next` 带屏上报的 `shown/dirty/partial/bat/mv/chg/usb/rssi/awake_ms/fw/wake/reset/fail`，返回 `action/reason/next_poll_seconds/partials_left/quiet/key/server_time/next_at/bar`，`action != none` 时再给 `bin`（模拟器用 `image`）；`/screen.bin` 是 800×480 的 1 位原始图（48000 字节，每字节高位在前，带 `X-Content-Hash`）；`/screen-bar.bin` 是 592×80 的底条（5920 字节，带 `X-Bar-Time`）。这三条就是屏能看到的一切。

4. **刷新裁决是纯函数，只在一处。** 服务端为每块屏记 `DeviceState`（局刷次数、哪天全刷、夜里是否攒着变化、有没有人按开关），裁决按顺序：没有 `shown` → 全刷「开机」；服务端没有这块屏的记录 → 全刷「第一次连上」；`dirty` → 全刷「恢复连接后补刷」；有人按开关（`force`）→ 全刷；夜里 22:00–7:00 → 不刷、变化攒到早上；内容没变 → 不刷；内容变了 → 当天第一次 / 局刷满 5 次 / 不支持局刷就全刷，否则局刷。屏固件只认 none/full（多数黑白红三色屏不支持局刷）或 partial。

5. **屏的运行时状态与上报存 `device.db`。** 每块屏的刷新状态、上报的电量、下次该来的时间、断线记录存产品服务自己的系统盘库 `device.db`（仿 `personalization.db` 的 `sqlitedb` 模式）；电量、「该来没来」（过了说好的下次时间 10 分钟）、电量低提示只在网页上显示，不上屏。

## 后果

- 多一种路由姿态与守卫、`control.db` 多一张表、系统盘多一个 `device.db`；产品服务成为这三条设备接口的最终处理者。
- 屏的令牌是家庭级凭据，在局域网上明文传输。截获它的人只能读屏幕画面、报刷新状态，碰不到 NAS 的其他功能；产品服务被攻破时能为任意屏签发令牌，与它今天能签发会话与 agent 令牌相同。
- 屏不登录，没有账号、没有个人空间，网页上对它只有电量与「该来没来」两处提示。
- 退回没有 `device_tokens` 的版本时，表被删除，屏要重新登记。
- 夜里与断线时的行为（攒变化、底条提示）由服务端裁决，屏固件无需升级即可改这些规则。

## 未选择

- **复用 agent 姿态：** agent 令牌绑账号、范围是 workspace、语义是「成员的 agent 改共享页面」，且 agent 守卫不限制 GET 与路径，也没要求拒绝互联网。屏的凭据是家庭级、只读、只在局域网，硬塞进 agent 会扩大它的权限面。
- **设备令牌绑账号：** 屏是全家共用的硬件，不该随某个成员停用、重置密码或删除而失效；它也没有「本人」的概念。
- **单独起一个设备服务：** 三条 GET 接口和刷新裁决很小，配合方案定在「产品服务」；单独服务要再多一个 systemd 单元、系统账号、子卷与会话解析，现阶段不值。
- **服务端推送（SSE/长连接）代替轮询：** 屏是电池供电、定时醒来，轮询已够；产品里还没有推送通道，长连接对屏固件也更复杂。
- **令牌不设闲时失效：** 和 agent 令牌一样设 90 天失效，避免一张被遗忘的令牌长期可读屏幕；真屏长期关机再开，重新登记即可（网页会提示它「该来没来」）。

## 关联

- [ADR 0011](0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[ADR 0012](0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)（局域网明文 HTTP 与 Host 指向本机）
- [ADR 0034](0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)（`sqlitedb` 迁移步骤）
- [ADR 0040](0040-let-members-agents-edit-shared-pages-through-agent-tokens.md)（agent 姿态与 agent_tokens 的模式，本 ADR 的 `device` 姿态与 `device_tokens` 仿此）
- 代码：`internal/apiroute`、`internal/httpapi`、`internal/accounts`、`internal/accountsapi`
