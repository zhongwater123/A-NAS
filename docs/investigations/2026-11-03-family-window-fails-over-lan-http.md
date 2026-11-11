# 经局域网 HTTP 打开时，家庭中心添加宝宝后窗口无法显示

状态：resolved
更新时间：2026-11-03

## 症状与影响

771e5e30 部署到实验 NAS 后，在家庭中心添加宝宝，窗口随即显示“这个窗口暂时无法显示”。重新打开或刷新后，内容闪现一下，又被这句话盖住。只要家里有宝宝，家庭中心就打不开。

## 最小复现

1. 经局域网 HTTP 地址（不是 localhost）打开桌面。这是非安全上下文，浏览器不提供 `crypto.randomUUID`。
2. 打开家庭中心，进入任何一个宝宝的频道。
3. 实际：频道刚显示，窗口就报错。期望：正常显示时间线。

Fake 预览用 `*.localhost` 打开，浏览器把它当作安全上下文，所以预览里一直复现不出来。在页面加载前删除 `Crypto.prototype.randomUUID` 后，预览里同样出错。

## 观察事实

- 浏览器控制台：`TypeError: crypto.randomUUID is not a function`，出错处是 `web/src/family/api.ts` 的 `requestKey`。
- 时间线顶部的日志输入行（`QuickJournal`）每次渲染都调用 `requestKey()`，所以报错发生在渲染中，被窗口的错误边界接住。没有宝宝时不渲染这一行，所以添加第一个宝宝之前一切正常。
- 分享模块早已遇到同一问题：它的 `requestKey` 写明局域网 HTTP 是支持的入口，改用 `crypto.getRandomValues` 生成 UUID，测试把 `crypto` 换成只有 `getRandomValues` 的对象来模拟局域网 HTTP。家庭中心另写了一份，用的是 `randomUUID`。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 实机数据与 Fake 不同，某个字段触发渲染错误 | 在全新的 Fake 家庭中按实机操作建家庭、添加宝宝、添加各类记录，抓取控制台 | 不出错，排除 |
| NAS 上的服务没有更新 | 查看 `/opt/a-nas/current` 与各服务的启动时间 | 均为 771e5e30，部署时已重启，排除 |
| 局域网 HTTP 不提供 `randomUUID` | 在预览中删除 `randomUUID` 后打开窗口 | 复现，确认 |

## 根因

`crypto.randomUUID` 只在安全上下文中存在，而 A-NAS 的局域网入口是明文 HTTP（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)）。家庭中心的 `requestKey` 直接调用它，日志输入行又在渲染时调用 `requestKey`，于是频道一出现窗口就崩溃。Fake 预览走 `*.localhost`，属于安全上下文，掩盖了这个差异。

## 修复与回归证据

- 修复：分享模块的实现移到 [`web/src/api/requestKey.ts`](../../web/src/api/requestKey.ts)，家庭中心与分享共用；日志输入行只在初次渲染时生成一次请求键。
- 测试：[`web/src/family/FamilyPanel.test.tsx`](../../web/src/family/FamilyPanel.test.tsx) 每个用例都以局域网 HTTP 的条件运行（`crypto` 只有 `getRandomValues`）。用修复前的 `family/api.ts` 运行时，12 个用例中有 9 个以 `crypto.randomUUID is not a function` 失败；修复后全部通过。[`web/src/api/requestKey.test.ts`](../../web/src/api/requestKey.test.ts) 覆盖没有 `randomUUID` 时生成 v4 UUID。

## 后续工作

- 只在安全上下文中存在的浏览器能力（`randomUUID`、剪贴板写入、`getUserMedia` 等）要有局域网 HTTP 下的退路。Fake 预览走 `*.localhost`，覆盖不到这类问题：检查这类功能时，在页面加载前删除对应接口，或从局域网 IP 打开预览。

## 关联

- 规格：[家庭中心](../specs/family-center.md)
- ADR：[0012：局域网经 Caddy 以明文 HTTP 开放 Web 桌面](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)
- 运行手册：不涉及
