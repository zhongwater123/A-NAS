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
   └── internal/mediaworker.Client ──► /run/a-nas-media/media.sock（root:a-nas 0660）
                                          │ Accept=yes，每个连接一个实例
                                          ▼
                                anas-media@.service（DynamicUser，无网络，
                                看不到 /srv/a-nas 与 /var/lib/a-nas）
                                          └── ffprobe / ffmpeg，输入为 fd:，输出到连接
```

开发模式（`ANAS_HOSTSTATE_MODE=fake`）用 `mediaworker.Local` 在进程内的 socketpair 上运行同一个服务端，协议与命令完全相同；找不到 FFmpeg 时影视中心只按文件名与 NFO 工作，所有视频都尝试直接播放。

## 身份与可见性

- 媒体库记录空间 ID、空间类型和所有者。共享空间的库对全体有效账号可见，个人空间的库只对所有者可见（[`library.go`](../../internal/media/library.go)）；不可见的资源一律按不存在处理。共享空间的库由管理员管理，个人空间的库由所有者管理。
- 同一空间内的媒体库文件夹不得互相包含，以免同一视频在首页出现两次。
- 读取视频、NFO、图片和字幕都调用 `files.Service`，在生产中由文件代理以请求者身份打开，内核 ACL 是最终裁决。后台扫描与整理通过 `accounts.Service.RememberedSessions` 借用能读取该空间的会话：个人空间用所有者的，共享空间优先用创建者的、否则任一有效会话（[`service.go`](../../internal/media/service.go)）。
- 打开影视中心（`GET /libraries`、`GET /home`）时，距上次扫描超过 10 分钟的库以当前用户身份在后台重扫；后台循环每 15 分钟扫描全部库，之后逐个探测、截图（[`process.go`](../../internal/media/process.go)）。

## 扫描与识别

- `files.Service.Tree` 先对账空间，再按目录前缀列出库文件夹下的所有条目。前缀比较用 SQLite 的 `length()`，因为 `substr` 按字符而 Go 的 `len` 按字节计数，中文路径会因此失配。
- 视频的身份是文件条目 ID：文件代理按 inode 维持条目，所以重命名或移动后进度、收藏与合集保留；文件消失时视频行删除，触发器同时清理收藏与合集成员。
- 文件名先去掉 `[发布组]` 等前缀，再由 [go-ptn](https://github.com/razsteinmetz/go-ptn)（MIT）切出片名、年份与画质标记；[`naming.go`](../../internal/media/naming.go) 只补充它不认识的约定：`Season N`/`S01`/`第N季` 目录、`第N集`/`EPnn`/`1x03` 标记、电视剧库中的纯数字集号，以及 `VID_`、日期等相机文件名。
- 类型：电影库全是电影，电视剧库全是剧集，其他库全是其他视频；混合库中带集标记、位于季目录或 NFO 为 `episodedetails` 的是剧集，带年份或 NFO 为 `movie` 的是电影，相机文件名与其余是其他视频。
- 剧以规范化后的剧名为键：剧名取剧集所在目录（季目录之上）的名字，除非文件名给出的剧名与之不符（如下载目录中混放多部剧）。`tvshow.nfo`、`poster.jpg`、`fanart.jpg` 从剧目录读取。
- NFO 以 `encoding/xml` 解码首个元素，按声明的字符集经 `x/text` 转码；字段与文件修改时间一起存入目录，未变化的 NFO 下次扫描不再读取。
- 外挂字幕是文件名以视频名开头的 `.srt/.ass/.ssa/.vtt`，语言由中间的标记识别。
- 首次扫描时“添加时间”取文件修改时间，之后新发现的视频取发现时间，避免第一次扫描把整个片库都显示为“最近添加”。

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
- 请求只有四种：`probe`、`frame`（时间点与宽度）、`subtitle`（格式与字符集，或内嵌流索引）、`stream`（起点、复制或转码、高度、码率、音轨）。每个字段先校验范围，FFmpeg 参数由 [`command.go`](../../internal/mediaworker/command.go) 拼装，输入固定为标准输入上的 `fd:`（FFmpeg 6.1 起支持，可在普通文件上 seek），输出固定为标准输出。
- 有限输出的操作缓冲后一次返回，并限制大小与时间；`stream` 在 FFmpeg 产出第一块数据后才回复成功，失败时带回 stderr 最后一行。对端不会在请求后再写入，所以连接上任何读取返回都表示对端已断开，Worker 随即杀掉 FFmpeg。

## 关联

- [影视中心规格](../specs/media-center.md)、[ADR 0017](../adr/0017-run-the-media-center-through-the-file-broker.md)、[存储与文件架构](storage-and-files.md)
- 代码：[`internal/media`](../../internal/media)、[`internal/mediaworker`](../../internal/mediaworker)、[`internal/mediaapi`](../../internal/mediaapi)、[`files.Service.Tree`](../../internal/files/tree.go)、[`web/src/media`](../../web/src/media)
- 部署：[`anas-media.socket`](../../deploy/systemd/system/anas-media.socket)、[`anas-media@.service`](../../deploy/systemd/system/anas-media@.service)、[系统服务安装器](../../scripts/install-v1.0.1-system-services.sh)、[系统测试](../../scripts/system-test.sh)
