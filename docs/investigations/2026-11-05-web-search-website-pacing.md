# 网页搜索的隐藏暂停、跨分类绕过与并发积压

状态：fixed（代码与本地回归已修复；部署后验证由 #409 单独跟踪）
更新时间：2026-11-06

## 症状与复现

[#364](https://github.com/zhongwater123/A-NAS/issues/364) 指出：A-NAS 显示的一小时暂停到期后，SearXNG 可能仍因 Cloudflare 验证码暂停十五天；网页、图片等引擎分别计速，切分类可以继续请求同一网站；同账号可占满未来轮次，已取消搜索还在 SearXNG 中运行。

在主干 `b1f16f65` 上加入回归后执行：

```sh
go test ./internal/websearch -run 'TestChallengeSuspensions|TestInflightAnswers|TestWebsitePause' -count=1
```

结果失败：六项专用暂停未覆盖；同一时刻的第二个验证码把首次暂停变成两小时；随后到达的正常回答反而清空暂停；图片分类既不显示网站暂停，也不受网页分类已用完的节奏限制。测试固定时钟并使用内存替身，不访问公网。

## 根因与修复

1. SearXNG 的普通失败时长与验证码等专用时长是两组配置。固定版本的 [settings.yml](https://github.com/searxng/searxng/blob/1cd13d98d65a58f1126fa5bb5c77e42c0ea625f3/searx/settings.yml) 有六项 `suspended_times`，现在全部覆盖为 0。普通超时仍停 5 秒，保留[短暂失败调查](2026-11-04-web-engines-suspended-after-one-bad-moment.md)的结论。
2. `judge` 原来按每个回答更新次数，且正常回答无条件清空。现在活动暂停期间不做这两种更新，只在到期后根据新回答加倍或清零。
3. 限速器与暂停以引擎名为键。现在统一映射到网站，显示网站名，发出前复查暂停。旧数据库的同站记录在事务中合并，取最晚恢复时间和最高次数，避免清除网站记录后旧引擎记录复活。固定版本的 `/config` 不提供网址，因此维护显式引擎表；未知引擎保留独立预算，升级 SearXNG 时须同步审查映射。
4. 账号的请求速率只能保护入口，不能防止占满网站轮次。继续用现有 `x/time/rate` 预约；另在锁内约束每账号每网站最多一个等待请求，直至该请求完成。
5. 只限制尚未取消的 HTTP 请求会低估 SearXNG 的实际工作。产品服务用标准库带缓冲通道限制为 64 个搜索槽位，先取槽位再取网站轮次；缩略图与配置不占搜索槽位。已发出的请求脱离浏览器取消、保留自身 12 秒时限，等它结束再归还容量；未发出的请求仍响应取消。没有另写限流器或队列库。

## 验证与剩余边界

- `go test ./internal/websearch -count=1` 通过。并发测试用 Go `testing/synctest` 推进时间，覆盖两个账号轮次、取消前后容量、排队中网站被拦与旧记录迁移；不依赖网络或真实等待十秒。
- 新发现的暂停在本次结果中立即报告，按网站去重；成功结果仍保留。设置页显示“暂停的网站”。接口保留 `engine` 字段名以保持兼容，含义明确为网站显示名。
- 完整门禁与竞态检测结果记录于修复 PR。本次未执行真实 SearXNG 系统测试或实验 NAS 实测，64 个槽位的实际余量与真实 512 MiB 下的内存压力没有实机证据。部署后的内存、首批结果时间和多账号体验由 [#409](https://github.com/zhongwater123/A-NAS/issues/409) 按[验收步骤](../runbooks/enable-web-search.md#多账号节奏与资源验收364)跟踪。

## 关联

- 规格：[网页搜索](../specs/web-search.md#像人一样搜索)
- ADR：[0030](../adr/0030-ship-web-search-as-a-built-in-searxng-service.md)
- 代码：[网站映射](../../internal/websearch/sites.go)、[节奏与暂停](../../internal/websearch/pace.go)、[分批请求与容量](../../internal/websearch/stream.go)、[数据库](../../internal/websearch/store.go)、[SearXNG 设置](../../deploy/search/settings.yml)
- 测试：[回归测试](../../internal/websearch/issue364_test.go)、[既有节奏测试](../../internal/websearch/pace_test.go)
