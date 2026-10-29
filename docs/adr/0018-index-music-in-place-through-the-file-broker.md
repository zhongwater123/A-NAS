# 0018：音乐原地索引普通文件，经文件代理以成员身份读取

状态：proposed（2026-10-12，随音乐 PR 提交评审）；行为见[音乐规格](../specs/music.md)。文件访问边界与影视中心相同（[ADR 0017](0017-run-the-media-center-through-the-file-broker.md)），本 ADR 记录音乐与之不同的部分，以及 FFmpeg Worker 由影视中心专用改为共用边界的决定。

相册把照片导入受管图库，由专用服务身份持有（[ADR 0006](0006-use-a-managed-photo-library.md)、[ADR 0011](0011-run-the-photo-library-as-a-dedicated-service-identity.md)）。音乐不同：家庭用户通常已经有一个用 SMB 拷进来、按“歌手/专辑”整理好的音乐文件夹，NAS 产品的音乐应用都是直接整理这些文件，而不是要求再导入一遍。我们决定：音乐库是个人空间与共享空间里普通文件的索引；产品服务通过文件服务列目录和打开文件，生产中由文件代理以登录成员本人的 Linux 身份读取（[ADR 0008](0008-use-unified-linux-identities-and-filesystem-acls.md)），不新增服务身份，也不获得任何文件的额外访问权。浏览器能直接播放的格式用内存安全的纯 Go 库在产品服务内读取标签；其余格式由 FFmpeg Worker 探测与解码，FFmpeg Worker 因此从影视中心的组件改为与应用无关的共用 FFmpeg 边界。

## 决定

1. **原地索引，不复制**：歌曲的身份是文件在其空间内的稳定文件 ID（文件服务按 inode 维持），改名和移动后收藏、歌单、播放记录仍然有效；文件被替换为新 inode 时视为新歌曲。音乐应用不写入、不删除、不改标签。
2. **读取以成员身份进行**：扫描（`files.Service.Tree` 与 `Lookup`）、读取封面与歌词、播放都调用 `files.Service`，由它执行空间 Policy 与数据卷检查，再由文件代理以成员身份打开文件并把文件描述符交给产品服务，因此标签解析、Range 播放和交给 FFmpeg Worker 的都是这个描述符。成员读不到的文件，音乐库也读不到。
3. **扫描依附于会话**：文件代理只为有效会话工作，所以扫描在成员打开音乐窗口或要求重新扫描时于后台进行，会话结束即停止。没有成员登录时不扫描，也不为扫描保留任何长期凭据。
4. **派生数据在系统盘**：歌曲索引与封面缩略图是可重建的派生数据，与收藏、歌单等用户数据一起存放在产品服务状态目录的 `music.db` 与 `music-covers/`。共享空间的歌曲索引在成员之间共用，查询时按成员可见的空间与音乐文件夹过滤。
5. **浏览器能播放的格式用纯 Go 读取**：MP3、FLAC、AAC（M4A）、OGG、Opus、WAV 的标签与内嵌封面用 [dhowden/tag](https://github.com/dhowden/tag)（BSD-2-Clause），时长按格式分别用 [hajimehoshi/go-mp3](https://github.com/hajimehoshi/go-mp3)（Apache-2.0，按帧计算）、[mewkiz/flac](https://github.com/mewkiz/flac)（Unlicense）、[abema/go-mp4](https://github.com/abema/go-mp4)（MIT）、[jfreymuth/oggvorbis](https://github.com/jfreymuth/oggvorbis)（MIT）与 [go-audio/wav](https://github.com/go-audio/wav)（Apache-2.0）读取，封面缩放沿用 [imaging](https://github.com/disintegration/imaging)。这些库只读取标签头、STREAMINFO、MP4 盒结构与 MP3 帧头，是内存安全的 Go 代码：恶意文件最多让单次解析失败（异常被捕获、该文件被跳过）或占用有限内存，不能像 C 解码器那样被利用执行代码，所以常见格式不必为每首歌启动 Worker。
6. **其余格式经 FFmpeg Worker 探测与解码**：ALAC、APE、WMA、WavPack、AIFF 与 DSD 由 FFmpeg Worker 的 `probe` 读出标签、时长与采样格式，内嵌封面由 `frame` 取出；播放时由新增的类型化 `audio` 请求把第一条音轨解码为 FLAC（有损源解码后无损封装，不再次有损编码；宽位深无损源保留 24 位，高于 192 kHz 的采样率减半到 176.4/192 kHz）。解码流不能 seek，播放器拖动时带新的起点重新请求；暂停 30 秒后放开流以归还 Worker 进程。Opus 与裸 AAC 的时长也由 `probe` 补齐。
7. **FFmpeg Worker 是共用的 FFmpeg 边界**：`internal/ffmpegworker` 只负责隔离地运行 FFmpeg——协议、描述符传递、按请求拼装参数、沙箱中的服务端与客户端——只使用自己的类型（中性的探测结果、请求结构）与错误，不引用任何应用。每个应用在自己的包里决定要什么：影视中心经 [`media.WorkerProcessor`](../../internal/media/worker.go) 适配为它的 `Processor`（选择视频、音轨与字幕流，判断 HDR，换成影视中心的错误）；音乐直接依赖 `ffmpegworker.Worker` 接口，标签含义、位深与采样率的选择留在 `internal/music`。systemd 单元、安装器与系统测试仍只有一套。

## 后果

- 常见格式不依赖 FFmpeg，开发机没有 FFmpeg 时也能整理与播放；需要解码的格式在没有 Worker 时按文件名收录、不能播放，Worker 暂时不可达时该文件留到下次扫描再读，而不是以缺少标签的状态入库。
- 每首需要解码的歌在扫描时启动一到两次 Worker（探测，有封面时再截取），播放时每次开始或拖动各启动一次；FFmpeg 解码远快于播放，浏览器通常很快缓存完整个流，Worker 随即退出。
- 解析不可信文件分两处：纯 Go 的标签与头部解析在产品服务内，封面图片在解码前检查尺寸（5000 万像素、16 MB 以内），歌词只读前 1 MB；FFmpeg 只在 FFmpeg Worker 的沙箱中解析文件。
- FFmpeg Worker 的可用性与并发上限由影视中心与音乐共享（2026-10-21 起与相册、分享一起按优先级共用 8 个槽位，见[准入与失败](../architecture/media-center.md#准入与失败)）；影视中心另有最多 2 路转换流的限制；音乐的播放器同一时间只打开一条解码流，暂停后放开。
- 共享空间的索引共用依赖“共享空间对所有成员可读”。管理员若在 A-NAS 之外收紧某个共享子文件夹的读取权限，其他成员仍可能看到该处歌曲的名称（无法播放）。
- 音乐库只在成员使用时更新：通过 SMB 加入的新歌在下次打开音乐时出现，而不是实时出现。
- 产品服务或 FFmpeg Worker 停止时音乐的相应部分不可用；文件共享与 SMB 不受影响。

## 未选择

- **像相册一样导入受管音乐库**：要求用户把已有的音乐再导入一遍并占用双倍空间，SMB 中的整理也不会反映到音乐库，不符合 NAS 音乐应用的使用习惯。
- **专用音乐服务身份，靠 ACL 读取音乐文件夹**：需要给服务身份授予成员个人空间的读取权，违反个人空间只有本人可见的约束（[ADR 0008](0008-use-unified-linux-identities-and-filesystem-acls.md)）；共享空间也要为它单独维护 ACL。
- **音乐直接调用原来的 FFmpeg Worker 客户端**：当时的 `internal/ffmpegworker` 引用 `internal/media`，返回影视中心的 `MediaInfo`、`StreamOptions` 与错误，音乐会因此依赖另一个应用的领域类型，违反职责分离。
- **音乐单独一个 Worker（`anas-music@.service`）**：与影视中心完全独立，但要复制一整套沙箱单元、安装、`ops-check`、系统测试和 FFmpeg 调用代码，两份安全边界需要分别审计且容易漂移；每个请求本来就是独立进程，故障隔离上没有额外收益。
- **在产品服务内运行 FFmpeg**：与 [ADR 0017](0017-run-the-media-center-through-the-file-broker.md) 的理由相同，被恶意文件攻破的 FFmpeg 会拿到产品服务身份、会话库与全部目录。
- **所有格式都用 ffprobe 读取元数据**：覆盖格式最多，但常见格式的每首歌都要多启动一次 Worker，大型音乐库首次整理要多出成千上万次进程启动；而这些格式的标签用内存安全的 Go 库即可读取。
- **TagLib 的 Go 绑定**：解析最完整，但 cgo 版本引入 C 依赖，WebAssembly 版本 [go-taglib](https://pkg.go.dev/go.senan.xyz/taglib) 只接受文件路径（产品服务没有路径访问权）且以 LGPL-2.1 发布。

## 关联

- [音乐规格](../specs/music.md)
- [ADR 0006：受管图库](0006-use-a-managed-photo-library.md)
- [ADR 0008：统一身份与文件 ACL](0008-use-unified-linux-identities-and-filesystem-acls.md)
- [ADR 0017：影视中心经文件代理读取媒体，FFmpeg 在隔离 Worker 中运行](0017-run-the-media-center-through-the-file-broker.md)
- [影视中心技术设计](../architecture/media-center.md)、[存储与文件架构](../architecture/storage-and-files.md)
- 代码：[音乐库](../../internal/music/music.go)、[FFmpeg Worker](../../internal/ffmpegworker/protocol.go)、[影视中心适配层](../../internal/media/worker.go)、[文件服务的子树列表](../../internal/files/tree.go)
