# 闲时检测只用了约 1 个核

状态：open
更新时间：2026-10-13

## 症状与影响

`bb1ec71bff65` 部署到 Experimental NAS 后，697 个照片对象的检测从 17:57:25 跑到 18:35:56，约 38.5 分钟，平均 3.3 秒/张。资源策略要求闲时用满可用资源，只给突发请求留余量（[资源策略](../architecture/photo-objects.md#资源策略)），Worker 的额度是 6 个核。实际上 Worker 平均只用了约 1 个核，整机 8 个逻辑核大部分时间闲着。

按这个速度，2 万张照片光检测就要约 18 小时的空闲时间。

## 最小复现

1. Experimental NAS 运行 `bb1ec71bff65`，Catalog 中有未完成的 `objects/v1` 任务，相册 5 分钟无人使用。
2. 让检测跑完，期间本地控制台照常开着。
3. 实际：Worker 的 CPU 时间约等于墙钟时间，检测被反复取消。期望：检测期间用到接近 6 个核，只在别人真的需要资源时让出。

## 观察事实

数据来自用户以 root 运行的只读检查 `/root/check-ai-bb1ec71bff65.sh`（`/tmp/anas-ai-check.txt`，19:25），以及 `ssh a-nas` 只读读取的 cgroup 文件（19:28–19:32）。

- **Worker**：
  - 那次运行（17:57:23–18:45:56，最后 10 分钟是空闲等待退出）systemd 记录 CPU 时间 38 分 42 秒（2,322 秒），内存峰值 1 GiB。
  - 2,322 秒除以 38.5 分钟检测时间约等于 1.0 个核，合 3.3 CPU 秒/张，其中包括 17 次被取消的推理。
  - 日志写明以 6 个线程检测。
- **相册服务**：17:51:22 启动以来共用 530 CPU 秒，其中包括为 697 张照片渲染 1600 px 检测输入，平均不超过 0.76 CPU 秒/张。
  - 自己的压力：CPU 等待 21 秒，I/O 等待 15 秒，内存等待 0.01 秒。
  - 页缓存（读原图留下的）把它推到 `MemoryMax=2G` 689 次，但内存等待几乎为零，可以排除。
- **取消的时间**：17 次取消都在 18:04:52 到 18:33:35 之间，前 7.5 分钟没有取消。
  - 间隔多数为 76–78 秒，其次是 67–68 秒或 134–152 秒，最后一段为 266 秒。
  - 也就是说间隔几乎都是 60 秒的整数倍，再加 7–32 秒。
- **代码路径**：
  - 门控关闭时，`processDetection` 取消检测并退回任务（[`ai.go`](../../internal/photos/ai.go)）。
  - 之后 `RunAI` 等 `min(1 分钟, 安静期)` 才再看门控（[`photoservice.go`](../../internal/photoservice/photoservice.go)）。
  - 压力到时仍高于上限的一半，回滞会让它再等 1 分钟。
  - 因此 17 次取消至少空等了 17 分钟，按间隔推算约 17–24 分钟，占 38.5 分钟的一半左右。
- **本地控制台**：Kiosk Chromium（`user.slice` 下的 `session-c29.scope`）17:51:33 起平均用 0.72 个核（GPU 进程约 22%、渲染进程约 47%）。
  - 它在这段时间共等了 42.5 秒 CPU；检测结束后它的 `avg10` 为 0。
  - `user.slice` 开机以来平均 0.55 个核，这是设备上最大的常驻负载。
- **Host Agent**：17:51:22 以来等了 146 秒磁盘 I/O，约占 2.4% 的时间，19:32 时的 `avg10` 为 4.05%。
  - 这就是 `system.slice` 空闲时 2.3–2.9% 的 I/O 压力底噪，离重新打开门控的 5% 只差 2 个百分点。
  - 数据卷 `/dev/sda1` 是机械盘（ST500DM002）；Catalog 与原图都在这块盘上。
- **权重**：`anas.slice`、`anas-ai.slice`、`system.slice`、`user.slice` 的 `cpu.weight` 都是 100。
  - cgroup v2 只在兄弟之间比较权重。`anas-ai.service` 的 `CPUWeight=10` 在 `anas-ai.slice` 里没有兄弟，因此不起作用。
  - 在根这一层，AI 与控制台、与系统服务同权。
  - #83 之前 Worker 在 `system.slice` 里，`CPUWeight=10` 至少低于相册服务的 20 与其他服务的 100。
- **磁盘调度器**：`sda` 用 `mq-deadline`。按内核文档，`io.weight` 只在 BFQ 或 `io.cost` 下生效，所以 `IOWeight` 在这块盘上不起作用，包括相册服务的 `IOWeight=20`。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 每次压力暂停后固定等 1 分钟，是墙钟时间的最大损失 | 读代码；对照取消间隔 | 确认：间隔约为 60 秒的整数倍加一段压力回升时间 |
| 关门的是控制台 Chromium 等 CPU（`user.slice`） | 每次暂停记录原因、cgroup 与数值，在 NAS 上复测 | pending；支持证据：控制台在这段时间等了 42.5 秒 CPU，空闲时为 0；根层级 AI 与它同权 |
| 关门的是 `system.slice` 的 I/O（Host Agent 的底噪加上相册读原图、写 Catalog，都在同一块机械盘） | 同上 | pending |
| 工作期间也只用到 2–3 个核：渲染输入与推理串行，渲染时 6 个推理线程空闲 | 每个任务记录渲染与推理耗时 | pending；按 1 分钟等待推算，工作期间 Worker 约用 1.8–2.7 个核 |
| ONNX Runtime 线程自旋（默认开启）占着核，加重控制台的等待，也虚增了 CPU 时间 | 开发机上对比 `session.intra_op.allow_spinning` 为 0 与 1 时每张的 CPU 秒与墙钟 | pending |

## 根因

未完全确定。已确认的是：门控每次因压力关闭后，AI 至少空等 1 分钟。哪一种压力触发了关闭，还要靠日志确认。

## 修复与回归证据

- 修复：待定
- 测试：待定

## 后续工作

按预期收益排序：

1. 压力暂停后按 PSI 的刷新间隔（约 2–5 秒）复查门控，不再等 1 分钟；“有人使用”之后的 5 分钟安静期不变。
2. 门控每次关闭与重新打开时记一条日志，写明原因、cgroup 与数值，作为调上限的依据。
3. 让 AI 的低权重在根这一层生效，例如给 `anas.slice` 设 `CPUWeight=10`。架构文档中“争用时前台立即占优”和 `IOWeight` 的说法要按实情改写。
4. 当前检测推理时就开始渲染下一张的输入，让两步重叠。
5. 在开发机上测量 ONNX Runtime 线程自旋的代价，再决定是否关闭。
6. 修好后在 NAS 上重排一批检测任务复测（需要 root，由用户执行），对比每张的墙钟时间、Worker 平均核数与控制台的 CPU 等待。
7. 另行查明两件事：控制台为什么空闲时也用 0.7 个核；Host Agent 为什么持续等磁盘。

## 关联

- 规格：[相册](../specs/photo-library.md)
- ADR：不涉及
- 架构：[物体检测与混合检索的资源策略](../architecture/photo-objects.md#资源策略)
- 运行手册：[启用相册物体检测](../runbooks/enable-photo-objects.md#首次实机部署证据)
- 代码：[`ai.go`](../../internal/photos/ai.go)（`RunAI`、`processDetection`）、[`gate.go`](../../internal/photoservice/gate.go)、[`photoservice.go`](../../internal/photoservice/photoservice.go)、[`anas-ai.service`](../../deploy/systemd/system/anas-ai.service)、[`providers.py`](../../ai/anas_ai/providers.py)
- 调查：[AI Worker 留着被取消的检测会话](2026-10-13-ai-worker-kept-cancelled-detector-sessions.md)
