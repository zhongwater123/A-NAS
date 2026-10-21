# 媒体 Worker 的共享预算与后台任务的永久失败

状态：fixed locally；Experimental NAS verification pending
更新时间：2026-10-21

## 症状与影响

[#168](https://github.com/zhongwater123/A-NAS/issues/168) 来自 2026-10-20 的代码与架构审查（[#159](https://github.com/zhongwater123/A-NAS/issues/159)，基线 `9cda8a3`），严重程度 P1。2026-10-21 在 `8f5b6b0` 上按源码复核：相关代码自基线以来没有改动，问题全部仍然存在。它没有在实验 NAS 上被观察到。

- 媒体 Worker 几分钟不可用（部署或重启套接字，或连接被占满），期间导入的 HEIC 照片与视频的缩略图在 3 次尝试后永久 `failed`。缩略图是向量、检测与人脸任务的入口，这些照片因此永远不会出现在搜索与人物中；网格对它们回退到 1600 px 的 `/preview`，每次显示都经 Worker 解码，进一步加重 Worker 的负担。恢复只能手工执行 SQL。
- HEIC 的检测输入同样经 Worker 渲染，按同样的方式失败。
- 音乐中只有 Worker 能测的时长（Opus、裸 AAC）在 Worker 不可达时被记为 `failed`，此后再也不会测量，与 [ADR 0018](../adr/0018-index-music-in-place-through-the-file-broker.md) 的“留到下次扫描再读”相反；只有 Worker 能渲染的内嵌封面直接丢失。
- 各应用各自限流、互不知道对方，而 systemd 只接受 8 个连接。

## 最小复现

1. 相册服务运行 `8f5b6b0`，媒体 Worker 的套接字不在（例如部署期间），或 8 个连接都在使用，例如两路电视转码、两路手机视频、两路实况照片与一路音乐。
2. 导入一张 HEIC。
3. 实际：缩略图任务在 1 分钟、2 分钟后重试，第 3 次后以 `io` 永久失败，没有向量、检测与人脸任务。期望：任务等待 Worker、不消耗尝试次数，Worker 回来后完成，并照常排入 AI 任务。

单元测试复现同一路径：[`workerwait_test.go`](../../internal/photos/workerwait_test.go) 的 `TestThumbnailsAndAIWaitForAMediaWorkerThatIsAway` 让 Worker 连续 4 次不可用（连不上、被丢弃的连接、忙、没有解码器），比 3 次尝试更久；在修复前的代码上它在第一次就失败（`ProcessMediaJob() while the worker is away = true`）。音乐的 `TestLengthsAndCoversWaitForAWorkerThatIsAway` 在修复前得到 `length after the worker returned = 0 ms`。

## 观察事实

- 缩略图任务把 `mediaworker.ErrUnavailable`、超时和取消都归为 `io`，与读不了的文件一样计次；没有任何代码把 `failed` 改回。重新上传相同的字节也无效，因为入队是 `ON CONFLICT … DO NOTHING`。AI 输入的渲染用同样的分类。
- 相册服务自己的 2 个解码位由缩略图、AI 输入、HEIC 预览、视频海报和实况照片的动态片段共用；30 秒时限包括等待解码位的时间，动态片段在整段播放（至多 45 秒）中占着一个解码位。
- 进程在任务执行中停止时，取消的上下文让 `failJob` 写不进数据库，任务停在 `running`，下次启动后等租约（5 分钟）到期才重领，并再计一次。
- systemd 在连接数达到 `MaxConnections` 时接受连接后立即关闭；客户端写完请求后读到 EOF，映射为 `ErrUnavailable`，与 Worker 在工作中崩溃无法区分。
- Worker 回答 `failed`（例如它自己的 30 秒超时）时客户端返回普通错误，相册把它当作读不了的文件永久失败。
- 每个 `anas-media@` 实例 `MemoryMax=2G`，没有总上限；在 8 GB 的设备上 8 个实例合计最多可达 16 GB。实例的 `CPUWeight=50` 只在彼此之间比较，因为它们是同一个 slice 中的同级。
- 对照：AI 运行器遇到 `ErrAIUnavailable` 时退回任务、不计次数；AI Worker 的协议规定“连不上时等待，收到图片后失败或退出才计一次”（[相册 AI](../architecture/photo-ai.md#协议)）。影视中心在环境错误时保持 `pending`，管理员重扫时重置失败项。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 把所有 `ErrUnavailable` 都当作环境错误、永不计次就够 | 对照 AI Worker 的规则：反复让解码器耗尽内存的原图会每分钟拉起一个 2G 的 Worker，永远重试 | 否：要分清 Worker 是否接下了输入；接下后停止计一次 |
| 靠连接是否建立就能分清“被 systemd 丢弃”和“Worker 中途停止” | 套接字由 systemd 先接受再关闭，两种情况客户端都是写成功、读到 EOF | 否：请求要求 Worker 先回一帧 `accepted` |
| 每个进程各自的上限加起来不超过 8 即可 | 三个服务的需求随时间变化，静态划分让空闲的份额无法给忙的服务用 | 否：用跨进程共享的槽位，以 `flock` 实现，持有者退出时自动放开 |
| 启动时把遗留的 `running` 任务退回且不计次是安全的 | 正常停止时任务已带着次数退回，遗留的只来自崩溃；若不计次，反复拖垮进程的原图会在重启间无限重试 | 否：立即重新排队但保留已计的次数 |
| 启动时也重新排队 `interrupted` 任务 | 同上，拖垮进程的原图每次启动再拖垮 3 次，构成崩溃循环 | 否：只重新排队 `io` |
| 槽位文件需要服务对 `/run` 可写 | `flock(2)` 不需要写权限，只读打开即可，`ProtectSystem=strict` 下的只读 `/run` 足够 | 是：文件由 tmpfiles 创建，服务只读打开 |

## 根因

媒体 Worker 是三个服务共用的有限资源，但准入分散在各个应用中，真正的上限在 systemd 的套接字上，越过它的表现与“Worker 不在”相同；而后台任务把“Worker 不在”与读不了的文件一样计次、失败后永不恢复。等待解码位的时间又算进了解码时限，所以前台使用就能让后台任务超时。

## 修复与回归证据

- 修复（行为与边界见[影视中心技术设计的准入与失败](../architecture/media-center.md#准入与失败)与[相册的后台任务](../architecture/photo-library.md#后台任务与资源隔离)）：
  - `mediaworker` 统一准入：每个请求在连接前取得 `/run/a-nas-media/slots` 中 8 个槽位之一，按交互、流、后台分层（8、6、2），后台无限期等待，交互与流等 20 秒与 5 秒后返回 `ErrBusy`；时限从取得槽位起算。请求带 `ack`，`accepted` 之前的失败是 `ErrUnavailable`，之后的是 `ErrInterrupted`，Worker 无法完成是 `ErrFailed`。代码：[`admission.go`](../../internal/mediaworker/admission.go)、[`client.go`](../../internal/mediaworker/client.go)、[`server.go`](../../internal/mediaworker/server.go)。
  - 相册：去掉自己的解码位；缩略图与 AI 输入是后台工作，`decodeFailure` 按 AI Worker 的规则分类，Worker 没接下时退回、不计次、1 分钟后再试；停止时带着次数退回；`RecoverJobs` 在任务循环前立即重新排队崩溃遗留的任务（保留次数）并让 `io` 失败的任务重新开始，修复前被 Worker 缺席记为 `io` 的任务在部署后第一次启动时恢复。代码：[`jobs.go`](../../internal/photos/jobs.go)、[`aiinput.go`](../../internal/photos/aiinput.go)、[`ai.go`](../../internal/photos/ai.go)、[`photoservice.go`](../../internal/photoservice/photoservice.go)。
  - 音乐：扫描是后台工作；Worker 不可达时只有它能测的时长与只有它能渲染的封面留到下次扫描。影视中心的探测与截图是后台工作。代码：[`scan.go`](../../internal/music/scan.go)、[`process.go`](../../internal/media/process.go)。
  - 部署：安装器写入 `/etc/tmpfiles.d/a-nas-media.conf` 创建槽位；`MaxConnections=16` 为退出中的 Worker 留出余量；所有实例在 [`system-anasmedia.slice`](../../deploy/systemd/system/system-anasmedia.slice) 中共用 `MemoryMax=3G`，权重移到 slice 上。
- 测试：
  - `internal/mediaworker`：分层预留、另一进程持有全部槽位时后台等待并在槽位放开后继续、目录缺失时退回进程内、请求放开槽位、时限不含排队、被丢弃的连接与中途停止的 Worker 分别是 `ErrUnavailable` 与 `ErrInterrupted`、旧 Worker 不回 `accepted` 也能工作（[`admission_test.go`](../../internal/mediaworker/admission_test.go)）。
  - `internal/photos`：Worker 缺席比 3 次尝试更久时缩略图与检测最终完成且次数为 0；中途停止与超时各计一次，`interrupted` 不在启动时恢复而 `io` 会；停止时任务带着次数退回；崩溃遗留的任务立即重新排队并保留次数（[`workerwait_test.go`](../../internal/photos/workerwait_test.go)）。
  - `internal/music`：`TestLengthsAndCoversWaitForAWorkerThatIsAway`。
  - 修复前的代码上，相册与音乐的新测试都失败（见“最小复现”）；修复后 `go test -race` 覆盖的 `mediaworker`、`photos`、`photoservice`、`music`、`media` 全部通过。
  - 系统测试新增：槽位目录与文件的权限、三个服务账号各自能取得槽位、实例位于 `system-anasmedia.slice` 且其 `MemoryMax` 为 3G，以及在 8 个槽位都被其他进程持有时，一个 HEIC 预览等到槽位放开后返回 200（[`system-test.sh`](../../scripts/system-test.sh)）。2026-10-21 在开发机的 Debian 13 systemd 容器中运行：263 项通过，新增检查与原有的 HEIC、实况照片、影视与音乐检查全部通过；失败的 2 项是本机镜像（2026-10-14 构建）缺少 10-15 才加入的 ripgrep，与本修复无关。

## 后续工作

- 部署后在实验 NAS 上确认：`/run/a-nas-media/slots` 有 8 个文件；`journalctl -u anas-media.socket` 不再出现 “Too many incoming connections”；照片服务第一次启动的日志 `photo jobs recovered` 给出恢复的任务数，此前失败的 HEIC 照片陆续有了缩略图、向量与检测。部署须同时更新安装器的单元与 tmpfiles 条目，以及产品、相册与分享服务（三者共用槽位）。
- 8 个槽位、6 与 2 的分层和 3G 的总内存是按现有上限与常见负载估计的，没有实测。部署后在高负载下（多路转码加批量导入）观察 slice 的内存峰值与 OOM 记录再调整。
- 影视中心与音乐没有尝试次数：反复让 Worker 中途停止的文件会每轮（影视中心每分钟、音乐每次扫描）再试一次，与修复前相同。若实机出现，可按相册的做法为它们加上次数。
- Worker 真的不在时，视频与动态照片的导入仍返回 503 `media_unavailable`，这是有意的：上传需要读出视频信息才能入库。Worker 忙时它们最多等 20 秒。

## 关联

- 规格：[音乐](../specs/music.md#边界与失败)、[相册界面](../specs/photo-gallery-ui.md)（缩略图失败时的回退不变）
- ADR：[0017](../adr/0017-run-the-media-center-through-the-file-broker.md)、[0018](../adr/0018-index-music-in-place-through-the-file-broker.md)（Worker 暂时不可达时留到下次扫描）
- 架构：[影视中心技术设计的准入与失败](../architecture/media-center.md#准入与失败)、[相册技术设计的后台任务](../architecture/photo-library.md#后台任务与资源隔离)、[相册 AI 的协议](../architecture/photo-ai.md#协议)
- 运行手册：不涉及（安装器自动创建槽位与 slice）
