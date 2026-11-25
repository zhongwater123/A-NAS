# 成员在等的后台 AI 工作要不要等相册空闲

状态：decided（2026-11-25，用户）
更新时间：2026-11-25

## 问题

相册的 AI 门控原来对所有后台 AI 工作一视同仁：有人在用相册（前台请求进行中，或最后一个结束后的 5 分钟里）就不开始，正在做的立即挂断。

2026-11-19 在实验 NAS 上第一次验收剪辑中心，剪辑任务在“等待分析”停了一个多小时（[调查](../investigations/2026-11-19-montage-job-stalls-on-the-experimental-nas.md)）：
- 剪辑中心的分析和相册自己的后台工作共用这条规则；
- 门控按全机计，不分是谁在用相册；
- 剪辑中心主页当时每 5 秒换播一段成员的视频，全部经相册，成员开着窗口等，分析就一直不开始。

修复的 [PR #605](https://github.com/zhongwater123/A-NAS/pull/605) 加了一类“成员在等的后台工作”：门控回答它时不看有没有人在用相册，只看压力与内存（定义见[门控与三类工作](../architecture/photo-ai.md#门控与三类工作)）。这是修缺陷时作的选择，没有问过用户；2026-11-25 用户评审时发现，这条规则像临时改动，没有作为规范写清楚。

要定的是：成员在等的后台工作（现在只有剪辑中心的窗口、卡片、画质与描述）要不要等相册空闲。

[ADR 0038](../adr/0038-keep-montage-media-in-the-photo-library.md) 决定 4 写的是剪辑分析“用自己的前台、压力与内存准入”，后果一节写“前台有人用时照样让路”，两处都已注明 2026-11-19 起的不同。定下后按[与 ADR 的分界](README.md#与-adr-的分界)第 2 条改写 ADR 0038 的这两处。

## 选项

- **A：不等相册空闲（2026-11-19 起的做法）**：开始下一块要压力与内存允许；正在做的一块只在内存不足时挂断，CPU、I/O 压力只挡下一块。有人在用相册时，一轮只做这类工作，相册自己的后台工作照旧等。
  - 好处：成员开着剪辑中心等，或别的成员在用相册，分析照常进行。
  - 代价一，搜索排在描述后面：前台请求不再挂断这类工作，而 AI Worker 一次只服务一个连接（[`worker.py`](../../ai/anas_ai/worker.py)）。描述卡片期间成员搜索照片，查询的编码要等这张卡片描述完。实验 NAS 上一张平均 74 秒（2026-11-19，32 张）；相册调用 Worker 默认最长等 2 分钟，超过后这次搜索只按名称与用户标签匹配。剪辑中心方案原来用“每 200 毫秒查一次准入、有人在用相册就挂断”避免这种排队（[#365](https://github.com/zhongwater123/A-NAS/issues/365) 记的是同一类问题：读文字启动期间搜索排队）。[资源策略](../architecture/photo-objects.md#资源策略)的目标是后台带来的额外等待不超过约 1 秒，达不到再给查询单独一条路径；那条路径没有做。
  - 代价二，CPU：读文字模型的 llama-server 是 Worker 的子进程，同在 `anas-ai.slice`，它占的 CPU 不计入门控看的压力，只有设备上其他工作被拖住时才挡住下一块，正在描述的那张卡片照样做完。
  - 不变的：内存余量仍保留。加载模型前要留出 1 GiB，低于余量或内存压力超限时立即挂断。
- **B：恢复为等相册空闲（2026-11-19 之前的做法）**：有人在用相册就不开始，正在做的立即挂断。
  - 好处：搜索最多等一次挂断，不排在描述后面。
  - 代价：调查里的停滞会回来。任何成员在任何地方用相册，所有人的剪辑任务都等；剪辑中心页面现在仍从相册读缩略图，成员开着页面操作时，分析要等最后一次读取之后 5 分钟。
- **C：只在前台请求进行中让路，不等 5 分钟静默期**：前台请求进行中挂断，请求结束就继续。没有实现，需要改门控。
  - 好处：搜索最多等一次挂断；成员只是开着剪辑中心页面时，分析不必再等 5 分钟。
  - 代价：被挂断的卡片描述从头再来，成员一直在浏览相册时，描述可能反复被挂断、迟迟做不完；播放视频的整个请求期间都算进行中。

## 没有测量的

- 描述进行中搜索照片实际要等多久。上面的 74 秒是描述的平均耗时，不是测出的搜索等待；实验 NAS 上没有测过描述与搜索同时进行。
- 描述占着 Worker 的线程时，不经 Worker 的前台请求（浏览、缩略图、播放）慢了多少。
- 8 GB 机型上，描述（读文字模型约 3.5 GiB）与成员的前台请求同时进行时还剩多少内存。实验 NAS 是 16 GB。
- 开着剪辑中心窗口时分析照常进行，在实验 NAS 上仍待按验收手册复验（[调查](../investigations/2026-11-19-montage-job-stalls-on-the-experimental-nas.md)的后续工作）。

## 决定

选 A：成员在等的后台 AI 工作不等相册空闲，保留 2026-11-19 起的做法。2026-11-25 用户在评审时选了“Keep option A”。

选项 A 所列的两项代价一并接受：描述卡片期间搜索照片要排在这张描述之后；读文字模型占的 CPU 不计入门控。“没有测量的”几项仍没有测。

按[与 ADR 的分界](README.md#与-adr-的分界)第 2 条，规则写在 [ADR 0038](../adr/0038-keep-montage-media-in-the-photo-library.md) 的决定 4 与后果；代码不变（[PR #649](https://github.com/zhongwater123/A-NAS/pull/649) 只改文档）。

## 关联

- 定义：[门控与三类工作](../architecture/photo-ai.md#门控与三类工作)
- 调查：[剪辑任务在实验 NAS 上先停在“等待分析”，再停在“等待 VNAS”](../investigations/2026-11-19-montage-job-stalls-on-the-experimental-nas.md)
- ADR：[0038 决定 4](../adr/0038-keep-montage-media-in-the-photo-library.md)
- 设计：[剪辑中心的调度](../architecture/montage-center.md)、[资源策略](../architecture/photo-objects.md#资源策略)
- 规格：[剪辑中心](../specs/montage-center.md)、[相册视频](../specs/photo-video.md)
- 代码：[`gate.go`](../../internal/photoservice/gate.go) 的 `OpenForMember` 与 `ContinueForMember`，[`montagewindows.go`](../../internal/photos/montagewindows.go) 的 `MemberGate`，[`airunner.go`](../../internal/photos/airunner.go) 的 `round`
- 测试：`TestTheMontageAnalysisGoesOnWhileTheLibraryIsInUse`、`TestAMembersWorkIsNotHeldBackByUseOfTheLibrary`
