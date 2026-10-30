# 网页搜索的引擎实测

状态：开发机完成（2026-10-30）；实验 NAS 上的实测待做
更新时间：2026-10-30

## 目的

[网页搜索规格](../specs/web-search.md)的默认引擎以实测为准（[ADR 0030](../adr/0030-ship-web-search-as-a-built-in-searxng-service.md)）。本页记录每个候选引擎能否返回结果、耗时多少、失败的原因，以及 SearXNG 在 A-NAS 的运行方式下占用多少资源。

## 方法

- SearXNG 固定在提交 `1cd13d98d65a58f1126fa5bb5c77e42c0ea625f3`（2026-10-30，版本 `2026.10.30+1cd13d98d`），运行环境由 [`scripts/build-search-runtime.sh`](../../scripts/build-search-runtime.sh) 在 Debian 13 容器中构建，Granian 经 Unix 套接字提供服务，设置为 [`deploy/search/settings.yml`](../../deploy/search/settings.yml)。
- 每次请求只写 `engines`，不写 `categories`。两者都写时，SearXNG 还会查询该分类的全部引擎。
- 网页 4 个查询，图片、视频、资讯各 2 个，种子 3 个。种子只用合法内容（`ubuntu`、`debian`、`big buck bunny`）。各引擎轮流请求，每次请求间隔 3 秒，所以同一引擎约 18–24 秒才被请求一次。
- 一次请求计为“成功”的条件：HTTP 200 且有结果。“未响应”取自 SearXNG 的 `unresponsive_engines`。
- 开发机与实验 NAS 都在公司网络中，出口是公司 NAT，但两者的出口地址可能不同。

## 开发机结果（2026-10-30）

测量前，第一次测量的脚本有误（同时写了 `categories`），两分钟内向百度与夸克发了二十多次请求，此后两者对这个出口一直返回验证码。下表中这两项因此不代表正常使用，要在 NAS 上按真实节奏重测。

| 分类 | 引擎 | 成功/请求 | 平均耗时 | 每次结果数 | 失败原因 |
|---|---|---|---|---|---|
| 网页 | 搜狗 | 4/4 | 0.8 s | 10.5 | |
| 网页 | 360 搜索 | 4/4 | 0.6 s | 4.2 | |
| 网页 | Yandex | 4/4 | 0.8 s | 12.5 | |
| 网页 | 百度 | 0/4 | — | — | 验证码（见上） |
| 网页 | 夸克 | 0/4 | — | — | 验证码（见上） |
| 网页 | 必应 | 0/4 | 0.2 s | 0 | 没有报错，也没有结果 |
| 图片 | 百度图片 | 2/2 | 0.2 s | 10 | |
| 图片 | 夸克图片 | 2/2 | 0.2 s | 10 | |
| 图片 | 必应图片、搜狗图片 | 0/2 | 0.3 s | 0 | 没有报错，也没有结果 |
| 图片 | Yandex 图片 | 0/2 | 1.1 s | 0 | 引擎崩溃（unexpected crash） |
| 视频 | 哔哩哔哩 | 2/2 | 0.6 s | 20 | |
| 视频 | AcFun | 2/2 | 2.7 s | 30 | |
| 视频 | 搜狗视频 | 2/2 | 0.8 s | 25 | |
| 视频 | 360 视频 | 2/2 | 0.2 s | 9 | |
| 视频 | YouTube | 2/2 | 1.2 s | 19 | |
| 视频 | Dailymotion | 2/2 | 0.7 s | 3 | |
| 视频 | 爱奇艺 | 1/2 | 0.8 s | 25 | 一次引擎崩溃 |
| 视频 | 必应视频 | 0/2 | 0.3 s | 0 | 没有报错，也没有结果 |
| 资讯 | 搜狗微信 | 2/2 | 0.3 s | 10 | |
| 资讯 | 必应资讯 | 0/2 | 0.2 s | 0 | 没有报错，也没有结果 |
| 种子 | 海盗湾 | 2/3 | 0.3 s | 61 | `ubuntu` 无结果 |
| 种子 | 1337x、nyaa、BT4G、BTDigg | 0/3 | 5 s | — | 超时（站点不可达） |
| 种子 | SolidTorrents | 0/3 | 1.9 s | — | 证书校验失败、超时 |
| 种子 | Kickass、TokyoToshokan | 0/3 | — | — | 拒绝访问 |

### 解读

- **必应全部没有结果，但没有报错。** 从国内访问时，bing.com 会跳到 cn.bing.com。SearXNG 的必应引擎没有处理这种跳转（`searx/engines/bing.py` 中留着 `what about cn.bing.com`），这是引擎本身的问题，不是封锁。在上游修好之前，必应的四个引擎都不出结果。
- **百度与夸克的验证码是请求过快的后果。** 夸克的引擎代码写明，连续约 9 次请求就会出现阿里的验证码，SearXNG 随后把它暂停 15 分钟；百度的验证码会让 SearXNG 把它暂停 1 小时（默认设置）。产品服务的限流是每个账号连续最多 10 次搜索、之后每 2 秒一次，一次搜索只问当前分类的引擎。A-NAS 不尝试绕过验证码。
- **种子站多数在网络层就不可达**：超时与拒绝访问来自国内网络或公司网关。只有海盗湾（经它的 API 镜像）可用。
- **Google 默认不打开**：上游的 Google 引擎在 2025 年 Google 强制使用 JavaScript 后一直不稳定。

## 资源

- 运行环境压缩包 57.7 MB，其中 SearXNG 源码包 6.6 MB；解开后 143 MB 左右的 Python 依赖，加上 SearXNG 本身。
- 启动到可以回答 `/healthz` 用时 3.5–5.2 秒。
- 内存（RSS）：空闲时 Granian 主进程约 28 MB，工作进程约 78 MB；测量的约 90 次请求之后工作进程约 126 MB。单元的上限 `MemoryMax=512M` 留有余量。
- 启动后空闲时只建立过一个外部 TLS 连接（Cloudflare 的地址，来源待查），不随查询以外的时间增加。要在 NAS 上确认它属于哪个引擎的初始化，再决定是否需要处理。

## 下一步

1. 在实验 NAS 上按真实使用的节奏（一次查询，隔几十秒）重测全部候选，尤其是百度与夸克，并确认 NAS 的出口。
2. 按 NAS 上的结果调整 [`internal/websearch`](../../internal/websearch/websearch.go) 的引擎表与 [`deploy/search/settings.yml`](../../deploy/search/settings.yml) 的 `keep_only`。在上游修好之前，可以考虑拿掉一直没有结果的必应引擎。

## 关联

- [网页搜索规格](../specs/web-search.md)、[ADR 0030](../adr/0030-ship-web-search-as-a-built-in-searxng-service.md)
- [SearXNG 的设置](https://docs.searxng.org/admin/settings/settings.html)、[已配置的引擎](https://docs.searxng.org/user/configured_engines.html)
