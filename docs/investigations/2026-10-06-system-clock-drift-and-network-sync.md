# 桌面时钟持续落后于系统时间

状态：resolved in code，Experimental NAS 部署待完成
更新时间：2026-10-06

## 症状与影响

本地控制台显示的时间停留在页面启动时刻，页面运行越久就与真实时间相差越大，表现得像 NAS 系统时钟持续漂移且无法联网同步。错误显示会误导设备所有者，但没有证据表明 Debian 系统时钟本身错误。

## 最小复现

1. 在固定为 `2026-10-06T00:00:00Z` 的浏览器时钟下渲染 Web 桌面。
2. 用 fake timer 推进 60 秒，断言 `.clock` 从本地 08:00 变为 08:01。
3. 修复前命令 `cd web && npm test -- App.test.tsx` 稳定失败：界面仍为 08:00，无法找到 08:01；其余 8 个测试通过。

## 观察事实

- [`App.tsx`](../../web/src/App.tsx)原先以空依赖 `useMemo` 只格式化一次 `new Date()`；没有状态或 timer 能触发后续时钟重渲染。
- 前台宿主机状态每 10 秒刷新，但顶部时钟不依赖该状态，刷新成功也不会让它前进。
- 经已核验的项目 SSH 密钥连接 `a-nas-dev` 后，远端 epoch 与开发机采样处于同一秒。
- Experimental NAS 的 `Timezone=Asia/Shanghai`、`CanNTP=yes`、`NTP=yes`、`NTPSynchronized=yes`；`systemd-timesyncd 257.13-1~deb13u1` 为 enabled/active。
- `timedatectl timesync-status` 显示实际使用 `2.debian.pool.ntp.org`，offset 为 `+21.311ms`、packet count 为 `7`；DNS 能解析腾讯和 Cloudflare 时间源，默认路由也可到达公网地址。
- 因现有 Debian 网络校时已经健康，不安装自定义时间源，不通过 root 修改系统时间，也不扩大 Host Agent 权限。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 顶部时钟被冻结在首次渲染值 | 固定时钟后推进 60 秒并断言下一分钟 | confirmed |
| `systemd-timesyncd` 未安装、未启用或未运行 | 检查包、`timedatectl` 属性与单元状态 | rejected：已安装、enabled/active、已同步 |
| 服务运行但 DNS、路由或 NTP 无响应 | 检查解析、路由和 `timesync-status` | rejected：server、offset 与 packet count 均有效 |
| 只是固定时区偏差 | 比较 epoch、时区和随运行时间增长的显示差值 | rejected：epoch 同秒且时区正确 |

## 根因

桌面时钟使用只执行一次的 memoized 字符串，而不是随当前时间变化的状态。Debian 系统时钟与网络时间同步均正常；“无法联网同步”是冻结的 UI 无法反映已经校正并持续前进的系统墙钟。

## 修复与回归证据

- 修复：[`useCurrentMinute`](../../web/src/App.tsx)在每个真实分钟边界重新采样墙钟；timer 延迟不会累积为显示漂移。
- 测试：新增[桌面时钟回归测试](../../web/src/App.test.tsx)；修复前 1 失败/8 通过，修复后 9/9 通过。
- 完整检查：`make check` 通过前端类型、测试、生产构建、文档、运维静态检查、Go vet、Go 测试和二进制构建。

## 后续工作

- 把包含前端修复的新 release 部署到 Experimental NAS，重启 Kiosk 使 Chromium 载入新的哈希资源。
- 在直连屏幕跨过至少两个分钟边界，确认显示持续前进；部署证据补齐后将状态改为 resolved。

## 关联

- 规格：[Web 桌面宿主机状态](../specs/web-desktop-host-state.md)
- ADR：[本地控制台](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- 运行手册：[部署 Web 桌面](../runbooks/deploy-web-preview-to-experimental-nas.md)、[运行本地控制台](../runbooks/operate-local-kiosk.md)
