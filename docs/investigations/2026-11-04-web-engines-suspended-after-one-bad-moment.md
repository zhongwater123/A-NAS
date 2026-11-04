# 一次失败让网页分类的引擎全部暂停 5 分钟

状态：resolved（开发机上限制 SearXNG 的 CPU、断开它的网络各复现一次，改回 5 秒后都在下一次搜索恢复；11-03 当时的负载没有直接计量，见“假设”）
更新时间：2026-11-04

## 症状与影响

2026-11-03 开发机的预览运行真实的 SearXNG（容器 `searxng-preview-gy`，09:26 启动），上午搜索过，之后空闲约 11 小时：

- 20:10 搜“流浪地球”，网页分类 9 秒后仍没有一个引擎回答，只有 kukei 未响应，“还有 100 个引擎在搜索”。此后 5 分钟，每个网页引擎都回答“Suspended: timeout”。10 秒后切到图片分类，结果照常（58 个引擎中 46 个未响应）。
- 20:16 暂停期满后又搜了两次，9～11 秒内没有一个引擎回答，引擎再次被暂停；20:22 才恢复，搜狗 1 秒内回答。
- 20:49 再搜一次：98 个引擎没有回答，只有 openlibrary 有结果；随后的音乐、学术、IT 分类照常。

当时以为是“空闲很久后的第一次搜索”失败。实际上，只要 SearXNG 某一刻忙不过来或连不上外网，被问到的引擎就同时失败，并在之后 5 分钟全部被跳过。NAS 上断网、或 SearXNG 一时得不到 CPU 与内存，也会这样。

## 最小复现

1. 按预览的方式运行真实的 SearXNG：release 的运行环境、`anas-systemd:trixie` 镜像、单元中的 Granian 参数与 `deploy/search/settings.yml`，容器接在默认的 bridge 网络上；产品服务连接它的套接字（这样有 `pace.go` 的节奏）。先正常搜一次网页，确认结果在 1 秒左右开始出现。
2. CPU：`docker update --cpu-period 100000 --cpu-quota 20000 <容器>`（0.2 个核）后搜一次网页；`docker update --cpu-quota -1 <容器>` 恢复，30 秒后再搜一次。
3. 断网：`docker network disconnect bridge <容器>` 后搜一次网页；`docker network connect bridge <容器>` 恢复，10 秒后再搜一次。断网期间没有请求离开本机；被暂停的引擎 SearXNG 也不去访问。
4. 实际（暂停 300 秒）：恢复后的那次搜索里，被问到的引擎几乎都是 `Suspended: timeout` 或 `Suspended: HTTP connection error`，结果来自少数几个刚才侥幸回答的引擎，或者没有结果。期望：恢复后的搜索照常回答。

## 观察事实

- 11-03 的容器日志（`docker logs --timestamps`，WSL 重启后仍在）：
  - 20:10:16–20:10:23 约 100 条 `engine timeout`，以及同样多的 `ErrorContext(... 'curl_cffi.requests.exceptions.Timeout', None, (None, None, None))`。主机名为空，说明到期的是 SearXNG 自己的等待（`searx/network/__init__.py` 的 `future.result(timeout)`），不是 curl 的超时；curl 自己超时的错误带着请求的主机名。
  - 这些引擎的 5 秒等待在请求发出 8～15 秒后才到期，即 SearXNG 在 3～8 秒后才开始处理它们。等待到期后 4～5 秒，这些引擎的真实回答（403、JSON 页等）才陆续到达。
  - 20:16 有 253 条同样的超时，Python 记下日志到 docker 收到它最多相隔 3.5 秒；20:49 有 107 条。当天 09:30 启动后的第一次搜索也有 16 条。
- 同一个 WSL（14 核、7.6 GB）中，其他会话当时在做检查：20:08–20:24 一个会话的副本写入约 22,000 个文件（`npm ci`），另两个副本写入 `tools/`；20:40–20:52 又有一个新副本，预览自己也在 20:49 前重新构建。WSL 在 11-04 早晨挂起，重启后才恢复。
- WSL 重启后（11-04，空闲），同样的运行环境与设置，SearXNG 启动后的第一次网页搜索：第一批 1.0 秒；约 100 个请求在 0.09 秒内全部开始处理；curl 的 DNS 中位 0.16 秒、P90 0.51 秒，TLS 完成中位 0.46 秒，首字节中位 1.16 秒；SearXNG 用不到 1 个核。超时的都是 curl 自己的，例如连不上的站点“Connection timed out after 5001 milliseconds”。
- 上游（固定的提交 `1cd13d98d`）：
  - SearXNG 经 curl_cffi 0.17.0 访问引擎，其中是 curl 8.23.0（libcurl-impersonate 2.3.1，DNS 用 c-ares），已经没有 httpx 的 `keepalive_expiry`。
  - curl 不复用空闲超过 118 秒的连接（`lib/url.c` 的 `conn_max_idle_ms = 118 * 1000`，`lib/conncache.c` 的 `cpool_conn_too_old`）。服务器已关闭连接时，`searx/network/network.py` 换一个客户端重试一次。
  - 超时与连接错误由 `searx/search/processors/online.py` 交给 `handle_exception(..., suspend=True)`，引擎暂停 `min(max_ban_time_on_fail, ban_time_on_fail)` 秒（`searx/search/processors/abstract.py`），默认 5 与 120。
- 长连接替身：本机的 curl 8.21（连接池代码相同）连接一个保持连接的 HTTP 服务。中间的网关在流空闲 30 秒后不再转发，也不断开。空闲 60 秒后，curl 复用旧连接，5 秒后报“Operation timed out after 5004 milliseconds with 0 bytes received”。空闲 133 秒后，curl 报“Too old connection (133325 ms idle, max idle is 118000 ms), disconnect it”，新建连接，12 毫秒回答。
- 复现（同一个容器，两次复现之间重启 SearXNG，每个引擎都按产品服务的节奏询问）。不受限时，第一次网页搜索有 319 条结果，43 个引擎未响应。限 0.2 个核时，两种设置下都有 70～75 个引擎未响应，只有 7～17 条结果；curl 的 DNS 中位数为 0.5～1.8 秒，TLS 完成的中位数为 2.4～4.0 秒（不受限时为 0.16 与 0.46 秒）。之后：

  | 情形 | 暂停 300 秒（原设置） | 暂停 5 秒（修复） |
  |---|---|---|
  | 恢复 CPU 30 秒后搜索 | 58 条结果，61 个引擎未响应（暂停中），结果只来自约 6 个限 CPU 时回答过的引擎 | 第一批 1.0 秒，263 条结果，23 个引擎未响应 |
  | 断网后 10 秒搜索 | 0 条结果，79 个引擎未响应，`Suspended: HTTP connection error` | 第一批 1.03 秒，182 条结果，17 个引擎未响应 |

  断网没有触发产品服务自己的暂停。恢复后的搜索里，有几个引擎的网站返回 403，产品服务照规则暂停了它们。
- 实验 NAS（11-04 11:34 只读查看）：`anas-search` 运行了 23 小时，`MemoryCurrent` 509 MiB，上限 `MemoryMax=512M`；匿名内存 498 MiB，文件页只剩 139 KB，25 MB 在交换区，`memory.events` 中 `max` 为 6，`memory.pressure` 累计约 1 毫秒。本机的 SearXNG 工作进程一次网页搜索（106 个引擎）后从约 78 MB 涨到 180 MB，约每个用过的引擎 1 MB。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| curl_cffi 复用了被 NAT 悄悄丢弃的长连接，空闲后的第一个请求挂到超时 | 读 curl 8.23 的连接池代码；替身网关实验 | 否定：空闲超过 118 秒的连接不再复用。只有网关在 118 秒内丢弃连接才会这样，而那与空闲多久无关 |
| 空闲很久后 DNS 缓存全冷，约 100 个查询同时超时 | Windows 上冷查询 20 个引擎域名；WSL 重启后第一次搜索的 curl 分段耗时 | 否定：冷查询多在 0.5 秒内；冷启动时 DNS 的 P90 为 0.51 秒 |
| SearXNG 进程得不到 CPU，同一个 WSL 中其他会话在构建 | 11-03 的日志；同时段其他会话的文件写入；限制容器 CPU 复现 | 与证据一致：处理推迟 3～8 秒，SearXNG 自己的等待先到期，日志晚 3.5 秒写出，限 CPU 时得到同样的大面积超时。当时的负载没有计量 |
| 300 秒的暂停把一刻的失败放大成 5 分钟 | 限 CPU 与断网复现，300 秒与 5 秒对比 | 证实 |

## 根因

SearXNG 对每个引擎分别记失败：一次超时或连接失败，这个引擎就在 `min(ban_time_on_fail, max_ban_time_on_fail)` 秒内不被询问，原因写作“Suspended: …”。#333 把这个时间从默认的 5 秒改成 300 秒：那时 SearXNG 要等最慢的引擎回答才返回，连不上的引擎会拖慢每次搜索。

SearXNG 分不出是引擎自己连不上，还是 SearXNG 进程或 NAS 的网络一时出了问题，后者让当时被问到的引擎同时失败。11-03 晚上，开发机的 WSL 中其他会话在构建，SearXNG 收到网页分类约 100 个请求后 3～8 秒才开始处理，开始后 5 秒内又没能完成，网页引擎于是全部超时，被暂停 5 分钟；暂停期满后，负载下的下一次网页搜索又把它们暂停一次。图片等分类的引擎没有在那一刻被问到，所以照常。“空闲很久后的第一次”是巧合：负载来自别的会话，与 SearXNG 空闲多久无关。

## 修复与回归证据

- 修复：[`deploy/search/settings.yml`](../../deploy/search/settings.yml) 的 `ban_time_on_fail` 与 `max_ban_time_on_fail` 改回 SearXNG 默认的 5 秒，一刻的失败只影响这一次搜索。原来的理由已不成立：
  - 自 #347 起，产品服务把每个引擎各作为一个请求发给 SearXNG，结果先到先显示（[`stream.go`](../../internal/websearch/stream.go)），连不上的引擎只让最后一批晚到；
  - 自 #349 起，每个引擎按人的节奏询问（[`pace.go`](../../internal/websearch/pace.go)），被网站拦截的由产品服务暂停，不靠 SearXNG 的暂停。
- 代价：每次搜索都会询问平时连不上的引擎。seekninja、openlibrary、360search 这类等满 10 秒的引擎，让最后一批和“更多结果”在 10 秒左右才出现；第一批结果仍在 1 秒左右显示。
- 不在产品服务中按原因跳过超时的引擎：SearXNG 自己忙不过来时，原因同样是“timeout”。要分辨“整次搜索失败”与“这个引擎连不上”，只能按一次搜索中失败的比例去猜，而公司网络下平时就有三四成引擎不回答，这个比例靠不住。
- 测试：[`settings_test.go`](../../internal/websearch/settings_test.go) 的 `TestAFailedEngineSitsOutOnlyAMoment` 检查失败后的暂停不超过 5 秒，换回 300 秒时失败。
- 复现：上表“暂停 5 秒”一列。
- 门禁：见 PR 中 `make check`、`make docs-check` 与 `ANAS_SYSTEM_TEST_SEARCH=real` 系统测试的结果。

## 后续工作

- 实验 NAS 上 SearXNG 的内存贴着 512 MiB 上限，目前没有造成等待（压力累计约 1 毫秒）。要按引擎数实测内存增长，再决定提高上限，或者减少每个引擎常驻的连接与证书状态。
- 开发机上，真实 SearXNG 的预览与其他会话的检查共用一个 WSL；测量搜索耗时前先看 `uptime`，结果才可信。

## 关联

- 规格：[网页搜索](../specs/web-search.md)
- ADR：[ADR 0030](../adr/0030-ship-web-search-as-a-built-in-searxng-service.md)
- 研究：[引擎实测](../research/web-search-engines.md)
- 运行手册：[启用网页搜索](../runbooks/enable-web-search.md)
