# 0018：音乐原地索引普通文件，经文件代理以成员身份读取

状态：proposed（2026-10-12，随音乐 PR 提交评审）；行为见[音乐规格](../specs/music.md)。文件访问边界与影视中心相同（[ADR 0017](0017-run-the-media-center-through-the-file-broker.md)），本 ADR 记录音乐与之不同的部分。

相册把照片导入受管图库，由专用服务身份持有（[ADR 0006](0006-use-a-managed-photo-library.md)、[ADR 0011](0011-run-the-photo-library-as-a-dedicated-service-identity.md)）。音乐不同：家庭用户通常已经有一个用 SMB 拷进来、按“歌手/专辑”整理好的音乐文件夹，NAS 产品的音乐应用都是直接整理这些文件，而不是要求再导入一遍。我们决定：音乐库是个人空间与共享空间里普通文件的索引；产品服务通过文件服务列目录和打开文件，生产中由文件代理以登录成员本人的 Linux 身份读取（[ADR 0008](0008-use-unified-linux-identities-and-filesystem-acls.md)），不新增服务身份，也不获得任何文件的额外访问权。音频标签用内存安全的纯 Go 库在产品服务内解析，FFmpeg 只留给将来的转码，并且只在影视中心的媒体 Worker 中运行。

## 决定

1. **原地索引，不复制**：歌曲的身份是文件在其空间内的稳定文件 ID（文件服务按 inode 维持），改名和移动后收藏、歌单、播放记录仍然有效；文件被替换为新 inode 时视为新歌曲。音乐应用不写入、不删除、不改标签。
2. **读取以成员身份进行**：扫描（`files.Service.Tree` 与 `Lookup`）、读取封面与歌词、播放都调用 `files.Service`，由它执行空间 Policy 与数据卷检查，再由文件代理以成员身份打开文件并把文件描述符交给产品服务，因此标签解析与 Range 播放都在描述符上进行。成员读不到的文件，音乐库也读不到。
3. **扫描依附于会话**：文件代理只为有效会话工作，所以扫描在成员打开音乐窗口或要求重新扫描时于后台进行，会话结束即停止。没有成员登录时不扫描，也不为扫描保留任何长期凭据。
4. **派生数据在系统盘**：歌曲索引与封面缩略图是可重建的派生数据，与收藏、歌单等用户数据一起存放在产品服务状态目录的 `music.db` 与 `music-covers/`。共享空间的歌曲索引在成员之间共用，查询时按成员可见的空间与音乐文件夹过滤。
5. **纯 Go 解析，FFmpeg 只用于将来的转码**：标签与内嵌封面用 [dhowden/tag](https://github.com/dhowden/tag)（BSD-2-Clause），时长按格式分别用 [hajimehoshi/go-mp3](https://github.com/hajimehoshi/go-mp3)（Apache-2.0，按帧计算）、[mewkiz/flac](https://github.com/mewkiz/flac)（Unlicense）、[abema/go-mp4](https://github.com/abema/go-mp4)（MIT）、[jfreymuth/oggvorbis](https://github.com/jfreymuth/oggvorbis)（MIT）与 [go-audio/wav](https://github.com/go-audio/wav)（Apache-2.0）读取，封面缩放沿用 [imaging](https://github.com/disintegration/imaging)。只收录浏览器能直接播放的格式，暂不转码。与影视中心不同，这些库不解析视频容器与编解码数据，只读取标签头、STREAMINFO、MP4 盒结构与 MP3 帧头，并且是内存安全的 Go 代码：恶意文件最多让单次解析失败（异常被捕获、该文件被跳过）或占用有限内存，不能像 C 解码器那样被利用执行代码，因此不必为每个文件启动 Worker。ALAC、APE、WMA 等需要解码的格式若要支持，应作为媒体 Worker 的类型化转码请求实现（[ADR 0017](0017-run-the-media-center-through-the-file-broker.md) 第 4 条），不在产品服务中运行 FFmpeg。

## 后果

- 新增依赖都是纯 Go，音乐本身不依赖 FFmpeg；代价是 Opus 与 ADTS AAC 没有时长，ALAC 暂不能播放，首次扫描 MP3 需要读取整个文件。
- 解析不可信文件发生在产品服务进程内，范围限于上面这些内存安全的标签与头部解析；封面图片在解码前检查尺寸（5000 万像素、16 MB 以内），歌词只读前 1 MB。
- 共享空间的索引共用依赖“共享空间对所有成员可读”。管理员若在 A-NAS 之外收紧某个共享子文件夹的读取权限，其他成员仍可能看到该处歌曲的名称（无法播放）。
- 音乐库只在成员使用时更新：通过 SMB 加入的新歌在下次打开音乐时出现，而不是实时出现。
- 产品服务停止时音乐不可用；文件共享与 SMB 不受影响。

## 未选择

- **像相册一样导入受管音乐库**：要求用户把已有的音乐再导入一遍并占用双倍空间，SMB 中的整理也不会反映到音乐库，不符合 NAS 音乐应用的使用习惯。
- **专用音乐服务身份，靠 ACL 读取音乐文件夹**：需要给服务身份授予成员个人空间的读取权，违反个人空间只有本人可见的约束（[ADR 0008](0008-use-unified-linux-identities-and-filesystem-acls.md)）；共享空间也要为它单独维护 ACL。
- **用媒体 Worker 中的 ffprobe 读取全部元数据**：覆盖格式最多，也能给出 Opus 的时长，但每首歌都要启动一次隔离 Worker，大型音乐库首次整理要多出成千上万次进程启动；而标签读取用内存安全的 Go 库即可完成。需要转码时再使用 Worker。
- **TagLib 的 Go 绑定**：解析最完整，但 cgo 版本引入 C 依赖，WebAssembly 版本 [go-taglib](https://pkg.go.dev/go.senan.xyz/taglib) 只接受文件路径（产品服务没有路径访问权）且以 LGPL-2.1 发布。

## 关联

- [音乐规格](../specs/music.md)
- [ADR 0006：受管图库](0006-use-a-managed-photo-library.md)
- [ADR 0008：统一身份与文件 ACL](0008-use-unified-linux-identities-and-filesystem-acls.md)
- [ADR 0017：影视中心经文件代理读取媒体，FFmpeg 在隔离 Worker 中运行](0017-run-the-media-center-through-the-file-broker.md)
- [存储与文件架构](../architecture/storage-and-files.md)
- 代码：[音乐库](../../internal/music/music.go)、[文件服务的子树列表](../../internal/files/tree.go)
