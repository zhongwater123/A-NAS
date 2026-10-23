# 影视中心技术设计

状态：已实现（2026-10-10）。行为见[影视中心规格](../specs/media-center.md)，文件访问与 FFmpeg 隔离的取舍见 [ADR 0017](../adr/0017-run-the-media-center-through-the-file-broker.md)。部署与实机验收进度见[当前状态](../status/CURRENT.md)。

## 组件与数据流

```text
Web 影视中心（web/src/media）
   │ 会话 Cookie + CSRF，/api/v1/media
   ▼
anas-api（a-nas）
   ├── internal/mediaapi ── 只接收已确认的用户（mediaapi.WithUser）
   ├── internal/media
   │     ├── media.db（系统盘）：媒体库、目录、进度、收藏、合集
   │     ├── media-cache（系统盘）：截图、缩放后的海报、WebVTT 字幕
   │     ├── 扫描：files.Service.Tree ──► File Broker ──► 用户身份的文件 Worker
   │     └── 播放、探测、截图、字幕：files.Service.OpenContent 得到只读 fd
   │                 │ SCM_RIGHTS + 类型化请求
   │                 ▼
   └── media.WorkerProcessor ──► internal/mediaworker.Client ──► /run/a-nas-media/media.sock（root:a-nas 0660）
                                          │ Accept=yes，每个连接一个实例
                                          ▼
                                anas-media@.service（DynamicUser，无网络，
                                看不到 /srv/a-nas 与 /var/lib/a-nas）
                                          └── ffprobe / ffmpeg，输入为 fd:，输出到连接
```

开发模式（`ANAS_HOSTSTATE_MODE=fake`）用 `mediaworker.Local` 在进程内的 socketpair 上运行同一个服务端，协议与命令完全相同；找不到 FFmpeg 时影视中心只按文件名与 NFO 工作，所有视频都尝试直接播放。

`internal/mediaworker` 是 A-NAS 共用的 FFmpeg 边界，不引用任何应用的类型：探测返回 ffprobe 的中性结果，错误是它自己的 `ErrUnavailable`、`ErrUnreadable`、`ErrInvalid`。影视中心的 [`media.WorkerProcessor`](../../internal/media/worker.go) 把它适配为 `media.Processor`：挑选视频、音轨与字幕流，判断 HDR，并把错误换成影视中心的错误。音乐用同一个 Worker 探测与解码浏览器不能播放的音频（[ADR 0018](../adr/0018-index-music-in-place-through-the-file-broker.md)）。

## 身份与可见性

- 媒体库记录空间 ID、空间类型和所有者。共享空间的库对全体有效账号可见，个人空间的库只对所有者可见（[`library.go`](../../internal/media/library.go)）；不可见的资源一律按不存在处理。共享空间的库由管理员管理，个人空间的库由所有者管理。
- 同一空间内的媒体库文件夹不得互相包含，以免同一视频在首页出现两次。
- 读取视频、NFO、图片和字幕都调用 `files.Service`，在生产中由文件代理以请求者身份打开，内核 ACL 是最终裁决。后台扫描与整理通过 `accounts.Service.RememberedSessions` 借用能读取该空间的会话：个人空间用所有者的，共享空间优先用创建者的、否则任一有效会话（[`service.go`](../../internal/media/service.go)）。
- Web 上传、移动、复制、删除与恢复成功后，产品 API 调用 `ProductDependencies.FilesChanged`，影视中心以操作者身份在约 3 秒后重扫该空间的媒体库（`SpaceChanged`，同一库的连续变更合并为一次）。打开影视中心（`GET /libraries` 每 20 秒轮询一次、`GET /home`）时，距上次扫描超过 2 分钟的库以当前用户身份在后台重扫，SMB 放入的视频靠这一途径和每 15 分钟的后台循环出现；之后逐个探测、截图（[`process.go`](../../internal/media/process.go)）。扫描进行中又收到扫描请求时，当前扫描结束后再扫描一次。`POST /libraries/{id}/scan` 返回扫描中的媒体库。
- 转换播放（转封装、转码）的流是无长度、不能按范围读取的分片 MP4。浏览器直接读取这种流时只预读约 2 MB（720p 转封装实测暂停时也只领先 2.4 秒），所以播放器用 Media Source Extensions 自己读取（[`streamFeed.ts`](../../web/src/media/streamFeed.ts)）：读够即停，连接与 FFmpeg 随之等待，回落到下限再读，并保留画面后方 90 秒。预读量按播放方式定（[`Player.tsx`](../../web/src/media/Player.tsx) 的 `reach`）：转封装只复制视频流，1080p 每 5 分钟约 11 CPU 秒，所以预读 300 秒、回落到 240 秒；转码每 5 分钟约 490 CPU 秒，跳到缓冲之外时预读全部作废，所以预读 30 秒、回落到 15 秒。浏览器的 MSE 配额（Chrome 约 150 MB 视频）先到时，追加失败后先丢弃已播放的部分再等。`GET /videos/{id}/playback` 为转换播放返回流的 `mime`（编码串只写编码，档次与级别由流的初始化段给出）；浏览器不支持时回到 `<video src>` 直接读取。FFmpeg 以 `-frag_duration 1000000` 每秒至少输出一个片段，开始或跳转后不必等满一个关键帧间隔。跳到缓冲之外时播放器以新的 `start` 重新请求流，连续跳转合并为一次；服务端为新流最多等 4 秒空出的转码位置（`StreamWait`），因为被换下的流此时往往还在关闭。
- 重命名片源（`PATCH /videos/{id}`）经文件服务的 `Move` 以用户身份在原文件夹内改名，连同以原文件名开头的字幕、NFO 与图片；先检查所有目标名字都未被占用，任何一步失败就把已改的改回。随后更新 `videos.file_name` 并重新扫描该媒体库，片名按新名字重新识别；文件 ID 不变，所以进度、收藏与合集保留（[`rename.go`](../../internal/media/rename.go)）。
- 重命名电视剧（`PATCH /shows/{id}`）改剧集自己的文件夹与各集文件。扫描器给剧集分组时优先用文件名解析出的剧名，只有文件夹名与之相关时才用文件夹名，所以只改文件夹不够：各集文件名里与原剧名匹配（词间分隔符不限）的部分也换成新剧名，字幕等随片源一起改，最后改文件夹。“自己的文件夹”是各集所在文件夹（季文件夹则取其上一级）且其中没有媒体库的其他视频；共用文件夹的剧只改文件。剧集行按 `(library_id, key)` 复用 ID，所以改名前先把行的 `key` 与 `title` 改成新剧名，扫描后剧集保留 ID、收藏与合集；新 `key` 已被另一部剧占用时拒绝，不合并。
- 影视中心的上传没有自己的接口：Web 客户端用文件服务的 `POST /spaces/{id}/uploads` 与 `POST /spaces/{id}/directories` 逐个写入媒体库文件夹（[`uploads.ts`](../../web/src/media/uploads.ts)），因此授权、容量检查和上传后的重扫都与文件管理相同。可上传的扩展名与扫描器读取的视频、字幕、NFO 和图片扩展名保持一致。

- 片源和剧集的改名与扫描共用每个媒体库的锁：取得锁后重新读取目录，文件移动、目录更新和返回详情完成后释放锁，再请求一次新扫描。改名失败或回滚也会接续期间被合并的扫描请求；旧扫描不能覆盖新剧名并使原 ID 及收藏被删除。原因与复现见[改名/扫描并发调查](../investigations/2026-10-15-media-rename-scan-race.md)。

## 扫描与识别

- `files.Service.Tree` 先对账空间，再按目录前缀列出库文件夹下的所有条目。前缀比较用 SQLite 的 `length()`，因为 `substr` 按字符而 Go 的 `len` 按字节计数，中文路径会因此失配。
- 视频的身份是文件条目 ID：文件代理按 inode 维持条目，所以重命名或移动后进度、收藏与合集保留；文件消失时视频行删除，触发器同时清理收藏与合集成员。
- 文件名先去掉 `[发布组]` 等前缀，再由 [go-ptn](https://github.com/razsteinmetz/go-ptn)（MIT）切出片名、年份与画质标记；[`naming.go`](../../internal/media/naming.go) 只补充它不认识的约定：`Season N`/`S01`/`第N季` 目录、`第N集`/`EPnn`/`1x03` 标记、电视剧库中的纯数字集号，以及 `VID_`、日期等相机文件名。
- 类型：电影库全是电影，电视剧库全是剧集，其他库全是其他视频；混合库中带集标记、位于季目录或 NFO 为 `episodedetails` 的是剧集，带年份或 NFO 为 `movie` 的是电影，相机文件名与其余是其他视频。
- 剧以规范化后的剧名为键：剧名取剧集所在目录（季目录之上）的名字，除非文件名给出的剧名与之不符（如下载目录中混放多部剧）。`tvshow.nfo`、`poster.jpg`、`fanart.jpg` 从剧目录读取。
- NFO 以 `encoding/xml` 解码首个元素，按声明的字符集经 `x/text` 转码；字段与文件修改时间一起存入目录，未变化的 NFO 下次扫描不再读取。
- 外挂字幕是文件名以视频名开头的 `.srt/.ass/.ssa/.vtt`，语言由中间的标记识别。
- 首次扫描时“添加时间”取文件修改时间，之后新发现的视频取发现时间，避免第一次扫描把整个片库都显示为“最近添加”。

## 海报与剧照

同目录的海报、背景图与剧照由 anas-api 在进程内用 `imaging` 与 `x/image` 解码，按 EXIF 方向转正后缩放为 480×720、1280×720 或 640×360 的 JPEG，按条目 ID、尺寸与修改时间缓存在 `media-cache/artwork`（[`artwork.go`](../../internal/media/artwork.go)）；没有图片时用媒体 Worker 截取的画面。缓存命中不再解码。成员能往媒体库放任意图片，客户端能同时请求很多张，而 `anas-api.service` 没有 `MemoryMax`，所以解码内存有两道上限，都按头部声明的像素数计：

- **单张**：超过 5000 万像素的图片不解码，按没有图片处理。TMDB 海报原图约 600 万像素（2000×3000），4K 背景图约 830 万像素，8K 背景图约 3300 万像素，都在上限内。宽或高不为正数的头部同样拒绝。
- **合计**：正在解码的图片合计不超过 5000 万像素。解码前从加权信号量（`golang.org/x/sync/semaphore`）取得与像素数相同的份额，解码、转正、缩放与编码结束后放开，解码器 panic 时也放开。份额不够时请求排队，请求结束（客户端断开）即放弃；信号量先到先得，大图不会被不断到来的小图饿死。常见海报可以 8 张同时解码，5000 万像素的一张独占全部份额。

合计上限取单张上限，是因为内存随像素数线性增长，最坏的情况由格式决定。在 4900 万像素的图片上实测，解码、缩放与编码共分配：基线 JPEG（4:2:0）2.0 字节/像素，8 位 PNG 4.5，16 位 PNG 8.5。渐进式 JPEG 解码时还要为每个全分辨率分量保留 4 字节/像素的 DCT 系数（`image/jpeg` 的 `progCoeffs`），构造的 4:4:4 CMYK 渐进式图片因此约 20 字节/像素。所以无论多少请求同时到达，正在解码的海报普通情况下约 100 MB（JPEG）到 425 MB（16 位 PNG），构造的最坏情况约 1 GB，约为 8 GB 设备内存的八分之一；此前没有合计上限，每个并发请求都可能占这么多。Go 的垃圾回收在下一轮之前还留着已放开的缓冲，进程常驻内存可能短暂高于这个数。

不按格式估算字节数：`image.DecodeConfig` 给出的颜色模型能区分 PNG 的位深，却看不出 JPEG 是否渐进式与色度采样，为每种解码器维护估算表也不比按像素计更可靠。音乐封面用同样的单张上限，不另设预算：封面只在扫描中解码，而音乐扫描在全进程逐个进行（[`music.go`](../../internal/music/music.go) 的 `work`）。

## 播放

`Playback` 根据 ffprobe 的结果与浏览器上报的能力（`caps=hevc,av1,vp9,ac3,eac3,mkv,webm`）决定方式（[`playback.go`](../../internal/media/playback.go)）：

| 方式 | 条件 | 实现 |
|---|---|---|
| direct | 封装可直接打开（MP4/MOV，WebM，Chromium 中的 Matroska），视频与所选音轨都能解码，未选择更低画质，使用默认音轨 | `GET /videos/{id}/file`，`http.ServeContent` 支持 Range |
| remux | 视频是 8 位 H.264（或浏览器支持的 HEVC），但封装、音频或音轨不满足 | FFmpeg 复制视频，音频为 AAC 时复制，否则转为 AAC 立体声 |
| transcode | 其余情况，或选择了低于原片的画质 | libx264 `veryfast` CRF 22，按画质限制高度与码率 |

- 转换后的流是分片 MP4（`frag_keyframe+empty_moov+default_base_moof`），不能随机访问。播放器在拖动时用新的 `start` 重新请求，并把该起点加到 `<video>` 的时钟上；进度、字幕与进度条都使用这个绝对时间。
- 直接播放出错时，播放器自动改为转码一次。
- 转换流最多同时 2 路（`Options.MaxStreams`），超出返回 `media_busy`；连接断开时 Worker 立即结束 FFmpeg。
- 字幕：外挂文件非 UTF-8 时按 GB18030 交给 FFmpeg；内嵌的文本字幕按流索引抽取。转换结果按文件版本缓存。浏览器只把字幕载入隐藏的 `<track>`，由播放器按绝对时间显示，使它在转码重启后仍然对齐。
- 进度每 10 秒、暂停与关闭时保存；不足 30 秒不记位置，达到 90% 记为已看并清除位置。

## Worker 协议

- 帧格式与文件代理相同：4 字节大端长度加 JSON，请求的长度前缀上附带 `SCM_RIGHTS` 文件描述符（[`protocol.go`](../../internal/mediaworker/protocol.go)）。
- 请求只有五种：`probe`、`frame`（时间点与宽度）、`subtitle`（格式与字符集，或内嵌流索引）、`stream`（起点、复制或转码、高度、码率、音轨）、`audio`（起点、采样率、16 或 24 位，把第一条音轨解码为 FLAC，供音乐使用）。每个字段先校验范围，FFmpeg 参数由 [`command.go`](../../internal/mediaworker/command.go) 拼装，输入固定为标准输入上的 `fd:`（FFmpeg 6.1 起支持，可在普通文件上 seek），输出固定为标准输出。
- 有限输出的操作缓冲后一次返回，并限制大小与时间；`stream` 与 `audio` 在 FFmpeg 产出第一块数据后才回复成功，失败时带回 stderr 最后一行。对端不会在请求后再写入，所以连接上任何读取返回都表示对端已断开，Worker 随即杀掉 FFmpeg。
- 客户端在请求中带 `ack`，Worker 读到请求与描述符后先回一帧 `accepted`，再开始工作并回复结果。没有 `ack` 的请求（旧客户端）直接得到结果，新客户端遇到旧 Worker 也把第一帧当作结果，所以部署时新旧进程短暂混用不会出错。

## 准入与失败

产品服务（影视中心、音乐、文件预览）、相册服务与分享服务共用同一个媒体 Worker。原先各自限流、互不知道对方，超过 `MaxConnections=8` 的连接被 systemd 接受后立即关闭，调用方读到 EOF 便当作 Worker 不在，后台任务因此永久失败（[#168](https://github.com/zhongwater123/A-NAS/issues/168)，[调查](../investigations/2026-10-21-media-worker-shared-budget.md)）。

- **共享槽位**：每个请求在连接前先取得 8 个槽位之一，应答或流关闭时放开（[`admission.go`](../../internal/mediaworker/admission.go)）。槽位是 `/run/a-nas-media/slots/0`…`7` 八个文件，由安装器的 tmpfiles 条目创建（目录 `root:a-nas-photos 0750`，文件 `0440`）；三个服务的账号都在 `a-nas-photos` 组中，以只读方式打开并 `flock(2)` 独占其一，所以 `ProtectSystem=strict` 下只读的 `/run` 就够。持有者无论怎样退出，内核都会放开它的锁。没有这个目录时（开发、测试）槽位只在本进程内。
- **优先级**：交互请求（预览、海报、导入时的探测、字幕、实况照片的动态片段）可用全部 8 个；流（视频转换、音乐解码）最多 6 个，始终给交互留 2 个；后台工作（相册缩略图与 AI 输入、影视中心的探测与截图、音乐扫描）最多 2 个。每种优先级取自己范围内编号最高的空槽，把低编号留给其他优先级。后台请求一直等到有空槽或服务停止，不会因为忙而失败；交互等 20 秒、流等 5 秒，仍没有空槽时返回 `ErrBusy`（也是 `ErrUnavailable`，界面照旧提示稍后再试）。本进程放开的槽位立即唤醒等待者，其他进程放开的由轮询发现（交互 50 ms、流 100 ms、后台 1 秒）。调用方用 `WithPriority` 标记后台工作；`stream` 与 `audio` 默认是流，其余默认是交互。
- **时限从取得槽位起算**：调用方用 `WithTimeout` 给出的时限不包括排队时间，例如 HEIC 解码与视频帧 30 秒、导入时的探测 15 秒、动态片段 45 秒。原先相册的 30 秒包括等待自己仅有的 2 个解码位，而播放中的实况照片整段占着其中一个，前台播放会让后台任务超时。
- **连接上限与内存**：`anas-media.socket` 的 `MaxConnections=16` 高于槽位数，多出的部分留给调用方已放开槽位、仍在退出的 Worker，正常情况下 systemd 不再丢弃连接。所有 Worker 实例在 [`system-anasmedia.slice`](../../deploy/systemd/system/system-anasmedia.slice) 中共用 `MemoryMax=3G`（每个实例仍为 2G），超出时内核先回收、再结束这个 slice 中的 Worker，而不是设备上的其他进程；slice 位于 `system.slice` 之下而不是后台的 `anas.slice`，因为播放有人在看，`CPUWeight=50`、`IOWeight=50` 让它在争用时让出 CPU 与磁盘。权重只在同级之间比较，所以写在 slice 上，而不是彼此同级的实例上。
- **失败分类**：Worker 回 `accepted` 之前的失败（连不上、被 systemd 丢弃、没有对应的解码器、等不到槽位）是 `ErrUnavailable`，同一请求稍后可能成功；之后连接断开是 `ErrInterrupted`（也是 `ErrUnavailable`），表示 Worker 接下了输入却停了，例如解码器耗尽内存，可能与输入有关；Worker 回答超时等无法完成是 `ErrFailed`；读不了文件是 `ErrUnreadable`。后台工作按此决定是否计入尝试：相册遵循与 AI Worker 相同的规则，见[相册的后台任务](photo-library.md#后台任务与资源隔离)；影视中心与音乐没有尝试次数，`ErrUnavailable`（包括中途停止）的文件留到下一轮，影视中心每分钟一轮，音乐在下次扫描，可读出的错误才记为失败。

## 关联

- [影视中心规格](../specs/media-center.md)、[ADR 0017](../adr/0017-run-the-media-center-through-the-file-broker.md)、[存储与文件架构](storage-and-files.md)
- 代码：[`internal/media`](../../internal/media)、[`internal/mediaworker`](../../internal/mediaworker)、[`internal/mediaapi`](../../internal/mediaapi)、[`files.Service.Tree`](../../internal/files/tree.go)、[`web/src/media`](../../web/src/media)
- 部署：[`anas-media.socket`](../../deploy/systemd/system/anas-media.socket)、[`anas-media@.service`](../../deploy/systemd/system/anas-media@.service)、[`system-anasmedia.slice`](../../deploy/systemd/system/system-anasmedia.slice)、[系统服务安装器](../../scripts/install-v1.0.1-system-services.sh)、[系统测试](../../scripts/system-test.sh)
- 调查：[媒体 Worker 的共享预算与后台任务的永久失败](../investigations/2026-10-21-media-worker-shared-budget.md)
