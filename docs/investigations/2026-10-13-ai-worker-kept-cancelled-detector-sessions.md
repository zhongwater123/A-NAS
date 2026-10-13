# AI Worker 留着被取消的检测会话，内存涨到上限

状态：resolved（未部署；发现于本地栈）
更新时间：2026-10-13

## 症状与影响

本地 systemd 栈（真实向量模型与检测器，168 张测试照片）整理照片时，AI Worker 的内存一直上涨。监控 12 分钟：每取消一次检测，Worker 多出约 11 个线程、100–170 MB 匿名内存；37 次取消后有 299 个线程，匿名内存 1.9 GB，另有 0.8 GB 被换出。`MemoryHigh` 被触发了 25,658 次，Worker 几乎停住。

收到“全部卸载”后，Worker 记下 `released everything; exiting`，但进程迟迟不结束：一次 39 秒，一次超过 7 分钟。进程结束前 systemd 不会为新连接启动 Worker，于是这段时间的搜索一直在转圈。

部署到 NAS 后，后台整理遇到反复的压力或用户操作时会出现同样的情况：Worker 撞上 `MemoryMax` 被杀，消耗任务的尝试次数；搜索要等很久。

## 最小复现

1. 本地栈：`anas-photo-objects`（127.0.0.1:18130），开发机同时运行检测评估，CPU 与内存压力反复越过门控阈值。
2. 上传照片，等相册服务开始检测；门控因压力反复关闭又打开。
3. 每 10 秒读取 Worker 的 `/proc/<pid>/status`：线程数与 `RssAnon` 随取消次数上涨，从不回落。

## 观察事实

- Worker 的 cgroup：`anon` 2.09 GB、`file` 0、`memory.swap.current` 839 MB，`memory.events` 中 `high` 25,658，`oom_kill` 0。
- 日志：两个 Worker 进程各有几十次 `detection cancelled`，只有内存压力触发了两次“全部卸载”。
- 单独用 Worker 的 Provider 测量（同样的模型、12 个线程、预热与冷的 XNNPack 缓存、每次检测换一个线程、1600 px 输入、30 次取消、5 轮卸载与重新加载），匿名内存稳定在约 700 MiB，没有增长。这些测量都在每阶段前调用了 `gc.collect()`。
- 同一测量去掉 `gc.collect()`、改走 Worker 自己的取消路径（`worker.detected` 加挂断）后，卸载后 ONNX Runtime 会话仍然存活。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| ONNX Runtime 或 MediaPipe 本身泄漏 | 单独的 Provider 在各阶段测内存 | rejected：稳定在约 700 MiB |
| 每次检测换线程，glibc 的 malloc arena 留住图片缓冲 | 150 次检测各用新线程、1600 px 输入 | rejected：稳定在 413 MiB |
| 冷的 XNNPack 缓存多占内存 | 冷、热缓存对比 | rejected：只多 44 MiB |
| 取消路径上的引用环留住会话，循环回收器很少运行 | 关闭自动回收，弱引用观察取消并卸载后的会话 | confirmed |
| 每次门控关闭都卸载检测器 | 读 `releaseModels` 与日志中的取消与重新加载 | confirmed：CPU 压力也触发卸载 |

## 根因

两处叠加：

- **Worker 留住会话**：检测在单独的线程中运行，`worker.detected` 把它抛出的异常存进闭包的字典。异常的 traceback 引用检测线程的栈帧，栈帧又引用这个字典，形成引用环。旧代码中 `OnnxDetector.detect` 的栈帧还持有局部变量 `session`，并在 `except` 块里 `raise Cancelled() from error`，又多一个环。Worker 几乎不创建 Python 对象，循环回收器很少运行，于是 `release()` 把 `self._session` 置空后，会话和它的线程池仍然活着。
- **相册服务频繁卸载**：门控因任何原因关闭都会卸载检测器，包括 CPU 与 I/O 压力。压力反复时，每次取消后都卸载、重新加载一个会话，每次都留下一个。

进程退出慢，是因为 Python 在退出时逐个析构对象：几十个会话和已换出的页面要先换回才能释放，内存又受 `MemoryHigh` 节流。

## 修复与回归证据

- 修复：
  - [`providers.py`](../../ai/anas_ai/providers.py)：取消在 `except` 块外抛出，栈帧不再持有会话；`release()` 之后调用 `gc.collect()`，保证卸载就是真的释放。
  - [`worker.py`](../../ai/anas_ai/worker.py)：取出异常后清掉它的 traceback；退出时用 `os._exit(0)`，不逐个析构模型。
  - [`ai.go`](../../internal/photos/ai.go) 的 `releaseModels`：只在检测队列清空、有人使用设备或内存吃紧时卸载；CPU 与 I/O 压力只取消正在进行的检测。
- 测试：
  - `test_a_cancelled_detection_lets_go_of_what_it_held`（[`test_worker.py`](../../ai/tests/test_worker.py)）与真实模型的 `test_cancelled_runs_and_releases_leave_no_session_behind`（[`test_detector_model.py`](../../ai/tests/test_detector_model.py)），都关闭了自动回收：在修复前的代码上失败（会话仍然存活），修复后通过。
  - `TestProcessorPressureCancelsButKeepsTheDetector`（[`detections_test.go`](../../internal/photos/detections_test.go)）。
- 本地栈复测：同一栈换上修复后的 Worker 与相册服务，在同样的压力下监控 13 分钟。83 次取消后，Worker 一直是 56 个线程、约 586 MB 匿名内存，没有换出；修复前 37 次取消后是 299 个线程、2.7 GB。

## 后续工作

- 部署后按[启用相册物体检测](../runbooks/enable-photo-objects.md)记录整理期间 Worker 的线程数、`MemoryPeak` 与 `memory.events`。
- 复测时开发机的 CPU 压力在 20%（当时门控的上限）上下徘徊，检测约每 8 秒被取消一次。同日已修订门控：Worker 进入自己的 `anas-ai.slice`，门控只看 `system.slice` 与 `user.slice` 的压力，上限 CPU 10%、降到一半以下才重新打开（[资源策略](../architecture/photo-objects.md#资源策略)）；上限部署后实测再定。

## 关联

- 规格：[相册](../specs/photo-library.md)
- ADR：[0016 AI 随系统内置安装](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- 运行手册：[启用相册物体检测](../runbooks/enable-photo-objects.md)
- 架构：[物体检测资源策略](../architecture/photo-objects.md#资源策略)
