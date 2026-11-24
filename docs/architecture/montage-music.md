# 剪辑中心的配乐

状态：M8b（[#559](https://github.com/zhongwater123/A-NAS/issues/559)）已实现选歌复制、配乐页上传、配乐分析 Worker、`music` 选段、试听与起点；allin1 的商业版权待议，待议期间配乐分析只在开发机与实验 NAS 上运行，见“版权待议期间”
更新时间：2026-11-19

本文是[剪辑中心实施方案](montage-center.md#配乐)“配乐”一节在 M8b 的落地：选歌怎样复制、allin1 在哪里跑、运行环境与权重怎样交付、内存与用时的实测。产品行为以[剪辑中心规格](../specs/montage-center.md#配乐)为准。

## 数据流

```text
浏览器 ──PUT /projects/{id}/song {trackId}──▶ anas-api
                                               │ 音乐功能播放的同一条路径：经文件代理以成员身份打开文件
                                               ▼
                                     anas-montage（流式收下）
   songs/<sha256>/audio ◀── 按内容摘要存一份；FFmpeg Worker probe：要有音频，30 秒到 8 分钟
        │ 后台，一次一首
        ├─ FFmpeg Worker audio PCM：立体声 44.1 kHz ─┐  两条管道，依次写
        ├─ FFmpeg Worker audio PCM：单声道 22050 Hz ─┤
        │                                            ▼
        │                         配乐分析 Worker（allin1）：节拍、重拍、段落与标签，响度曲线
        ▼
   剪辑计算 Worker 的 music：第八版的选段与小节槽 → songs 行：ready 或 unusable（原因）
```

- **上传**：配乐页的“上传配乐”不另开接口，把几样现成的东西接起来（[`web/src/montage/songUpload.ts`](../../web/src/montage/songUpload.ts)）：文件经传输中心的队列传进成员个人空间的“音乐”文件夹（没有时由 NAS 建，同名时另存一份），完成后读传输的 `entryId`，请音乐库扫描（扫描中再请求会在结束后再扫一遍）并等它回到空闲，曲目编号就是文件编号，出现后照常 `PUT /song`。浏览器先按音乐库认的扩展名、100 MiB 与 30 秒到 8 分钟挡一遍，读不出时长的交给剪辑服务判断。扫描后没有这首歌时，按音乐库是否自定了来源，写明“音乐”文件夹不在来源里或格式读不出。离开配乐页后上传仍在传输中心继续，但不再自动选用。
- **复制**：`montageapi.CopySong` 是 anas-api 对 `PUT /api/v1/montage/projects/{id}/song` 的专门处理（[`internal/montageapi/song.go`](../../internal/montageapi/song.go)）。它调 `music.Service.OpenTrack`，与音乐播放相同，看不到的曲目（包括别人个人空间里的）回答 404；音乐库已知的大小与时长先挡掉超限的，不复制。文件在扫描之后被删回答 404，成员已无权打开回答 403，与音乐功能播放这首歌时相同。歌曲经 `X-A-NAS-Song` 头带上标题与能否直接播放（音乐功能的 `Transcode`）；标题先截到剪辑中心保存的 200 个字符（`montage.SongTitle`），再长的标签也放得进这个头。这个头只由 anas-api 设置：转发代理先去掉浏览器发来的所有 `X-A-NAS-` 头，再放上 anas-api 生成的这一个（[`serviceproxy.WithInternalHeader`](../../internal/serviceproxy/serviceproxy.go)）。
- **收下**：剪辑服务边收边算 SHA-256，超过 100 MiB 回答 413；再请 FFmpeg Worker probe，没有音频或时长不在 30 秒到 8 分钟回答 422（上限见“用时与内存”）（[`internal/montage/songs.go`](../../internal/montage/songs.go)）。副本按内容存成 `songs/<sha256>/audio`，几位成员选同一首共用一份。
- **表**：`montage.db` 第 2 步加 `songs`（每位成员一行：摘要、标题、时长、状态、不可用的原因、等待的原因、分析结果与响度）与 `projects.song_id`、`song_start`、`song_selection`，带 Down（[`internal/montage/store.go`](../../internal/montage/store.go)）。`song_selection` 是成员拖动起点后那一段的选段，连同计算 Worker 的领域代码读的那份数据：读项目不用再算，任务也直接用成员看到的这一段，不再找计算 Worker。
- **回收**：删除项目、换歌、删除账号后，删掉没有项目再用的 `songs` 行，再删掉没有行引用的副本目录；存储打开时对一次账，清掉没收完的 `songs/.incoming/`，再开始分析（`Store.StartWorkers`）。进行中的任务开始时记下的那首歌也算引用：项目换了歌，原来的副本留到这个任务结束或取消（`unusedSongs`，[`internal/montage/songs.go`](../../internal/montage/songs.go)）。
- **分析**：同一内容只分析一次，结果写进所有在等它的行；新的一行遇到别人已经分析好的同一首，直接取用（[`internal/montage/analysis.go`](../../internal/montage/analysis.go)）。共用的分析里不写歌名：任务取选段时填上这位成员自己那一行的标题（`Store.Excerpt`），规划提示里不会出现别人的文件名。allin1 的结构先存下，算选段的计算 Worker 不在时只重试选段。
- **等待与失败**：
  - Worker 连不上或回答不能服务时，这首歌停在 `analyzing`，写明在等谁（`analyzer_unavailable`、`compute_unavailable`、`media_unavailable`），5 分钟后再试，不限次数：问一次不花什么。计算 Worker 回答不认识 `music`（设备更新途中还在运行的旧版本）也算在等它。
  - 配乐分析 Worker 接了歌却没做完（自己报错、进程被结束、超时）算一次失败的分析（`ErrWorkerFailed`）。每次失败都要占 Worker 几分钟，所以只试 3 次，之后这首歌记为不可用（`analysis_failed`）。计算 Worker 接了结构却出错或中途停止（`montagecompute.ErrFailed`）同样计一次，allin1 的结构留着，不重新分析。
  - 管道读到头，分不出是解完了还是解到一半断了。剪辑服务在两条流都写完之后，再发一帧 `{"streams":"complete"}`，Worker 收到才开始分析；解码中途失败时剪辑服务直接挂断，Worker 丢掉读到的数据，不分析也不回答。
  - 服务启动时，把上一个分析版本（`AnalysisVersion`）判为不可用的歌重新排队：新版本改掉的可能正是当初拒绝它们的地方。已经 `ready` 的歌不动。

## 选段

`music` 是剪辑计算 Worker 的一个操作（[`ai/anas_montage/music_ops.py`](../../ai/anas_montage/music_ops.py)），输入 allin1 的结构、响度曲线、目标 90 秒与起点：

- **默认选段**：原样调用第八版的 `domain.music.from_structure`。黄金比对：用第八版的 10 个 allin1 结构文件，选段与小节槽和第八版逐个相同，见“验证”。
- **没找到副歌**：`from_structure` 找不到副歌时，把两小节以上、平均响度最高的段落当作副歌再选，回答 `chorus: false`，配乐页提示“没找到副歌，请试听确认”。
- **拖动起点**：剪辑服务先把起点吸附到最近的重拍（`montage.nearest`），`music` 用第八版同一份段落整理与切槽（`domain.music` 的 `sections` 与 `parts`，默认选段也调用它们），从起点切到目标时长附近的重拍，没那么长就到音乐结束。成片从新起点开始，时长等于选段。
- **拖回默认起点**：起点落在默认起点所在的重拍上时，项目不再记起点，回到默认选段。默认选段在段落边界结束，从同一个起点按固定时长切出来的那一段和它不一样。默认起点保留两位小数，重拍保留三位，所以两者按重拍比较，任务判断“是不是默认选段”用同一条规则。
- **不可用**：有效小节少于 16 个（`few_bars`）；选段不在 30–150 秒（`no_excerpt`）；最长的槽超过 12 秒（`long_slot`，`segment` 的上限）；解码失败或 allin1 拒收（`undecodable`）；分析失败 3 次，或计算 Worker 读不了 allin1 给的结构（`analysis_failed`）。
- **登记**：`worker.py` 的 `OPS` 里有 `"music": music_ops.op_music`，测试经 Worker 的正常入口调用它。Go 侧的调用在 [`internal/montagecompute/music.go`](../../internal/montagecompute/music.go)。

## 配乐分析 Worker

[`ai/anas_montage_music/worker.py`](../../ai/anas_montage_music/worker.py) 把 `allin1.analyze` 的步骤留在内存里做，不写文件：

1. Demucs `htdemucs` 分离四个音轨，参数与 `demucs.separate` 的默认相同（shifts 1、overlap 0.25、分块、溢出时按比例缩小、写成 16 位）。Demucs 的随机平移固定种子，同一首歌每次结果相同；开发机上的结构文件当年没有固定种子，所以对照时允许误差。
2. 每个音轨按 madmom 读 16 位 WAV 的方式混成单声道，算 allin1 的对数滤波谱。
3. 8 个分折模型的集成推理，madmom 的 DBN 找重拍。
4. 响度是第八版的 RMS 曲线（2048 点一帧、每 512 点，22050 Hz），用运行环境里的 `librosa.feature.rms` 算，居中与补零写明在参数里；与第八版手写的算式逐位相同。

单元 [`anas-montage-music.socket`](../../deploy/montage-music/anas-montage-music.socket) 与 [`.service`](../../deploy/montage-music/anas-montage-music.service)：

- 套接字 `/run/a-nas-montage-music/music.sock`，`SocketUser=a-nas-montage`、0600，只有剪辑服务能连；
- DynamicUser，`PrivateNetwork`，`InaccessiblePaths=-/srv/a-nas`，`HF_HUB_OFFLINE=1`；歌曲只经两条管道进来；
- 放在 `anas.slice` 之下；空闲 5 分钟退出，下一首再由套接字启动；
- 权重按清单的 SHA-256 逐个核对后才加载（`checked_files`）。
- 省内存，数值不变：htdemucs 是只有一个模型、权重为 1 的集成，Worker 直接对它做同一次随机平移与分块，输出留作视图，原地还原归一化，少三份分离结果的拷贝；四个音轨先转成 16 位单声道，再放掉浮点数据，然后才算谱。

## 运行环境与交付

2026-11-18 用户选定：照 AI 运行环境与模型的做法（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)、[ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)）做成内容包，NAS 上不跑 pip、不联网。考虑过的另两种：运行环境自带一份 Python 3.12 以直接用 NATTEN 的预编译轮子（多一份解释器要单独跟安全更新）；待议期间只写实验 NAS 的联网安装脚本（偏离“NAS 不跑 pip”，交付要做两次）。

- **锁文件**：[`deploy/montage-music/requirements.in`](../../deploy/montage-music/requirements.in) 由 `scripts/build-montage-music-runtime.sh --lock` 解析成按哈希锁定的 `requirements.txt`，目标同 AI 运行环境：Debian 13 的 Python 3.13、x86_64。PyTorch 取 PyTorch 自己的 CPU 版索引（`torch==2.6.0+cpu`）。Hydra 与 OmegaConf 限定新版本，否则解析器会退到 Hydra 0.11；它们依赖的 antlr4 运行时只有源码包，是纯 Python，锁文件按源码包的哈希装。
- **没有轮子的两个包**：NATTEN 与 madmom 没有 Python 3.13 的轮子，[`deploy/montage-music/sources.json`](../../deploy/montage-music/sources.json) 固定它们的源码与 SHA-256，构建时下载、核对、编译：
  - NATTEN 用 0.17.1（开发机的 allin1 环境就是这一版）。0.17.5 在导入时就向 CUDA 查询设备，只有 CPU 的 PyTorch 上直接失败。NATTEN 的 CPU 算子按构建机的处理器编译，所以 NAS 用的运行环境要在 x86_64 的机器上构建；单个源文件编译时约占 2 GiB，8 GB 的机器上开 4 个并行会被内存不足杀掉，默认 2 个。
  - madmom 用 GitHub 上一个提交的压缩包。它的模型与数据文件（CC BY-NC-SA 4.0）是 git 子模块，压缩包里没有；构建写一个空的 `madmom/models/__init__.py` 占位，最后检查运行环境里没有任何这类文件。allin1 只用 madmom 的谱处理与 DBN，不加载这些文件。
- **运行环境**：`scripts/build-montage-music-runtime.sh` 在 Debian 13 的构建镜像里建虚拟环境、按锁文件装、编出两个包，压成 `build/montage-music-runtime/<id>.tar.gz`。ID 由锁文件、`sources.json` 与 Python 版本算出（`scripts/lib/content.sh` 的 `montage_music_runtime_id`）。`scripts/build-content-packages.sh --montage-music` 把它打成内容包 `a-nas-montage-music-runtime-<id>`，装到 `/opt/a-nas/montage-music-runtimes/<id>`。
- **权重**：清单 [`allin1-harmonix-all.json`](../../deploy/models/allin1-harmonix-all.json)（8 个分折模型，每个按 `parts` 记大小与 SHA-256，整体的 SHA-256 是这 8 行摘要清单的摘要）与 [`htdemucs.json`](../../deploy/models/htdemucs.json)，各自固定 Hugging Face 的提交。同一脚本把它们打成模型内容包，装到 `/opt/a-nas/models/<sha256>/`。Worker 启动时逐个核对。
- **可选软件包**：`scripts/build-packages.sh --montage-music` 另打 `a-nas-montage-music`：Worker 的代码、两个单元、清单，以及指向运行环境与权重的链接。它依赖同版本的 `a-nas` 与上面的内容包。`a-nas` 不依赖它，`make packages` 与 release 都不打它。

### 版权待议期间

许可的核对与要议的问题见[决定记录](../decisions/2026-11-17-montage-center-first-version.md#allin1-的商业版权)。

- allin1、Demucs 的权重与这个运行环境都不进任何 release，只在开发机与实验 NAS 上按[启用手册](../runbooks/enable-montage-music.md)手动装；
- 没装时剪辑中心照常运行，选的歌停在 `analyzing`，写明在等配乐分析；
- 结论是不能随产品分发时，按实施方案退回 beat_this 与色度自相似，`music` 的输入格式不变。

## 验证

**选段的黄金比对**（开发机 C，[`ai/eval/montage_music_golden.py`](../../ai/eval/montage_music_golden.py)）：第八版的 10 个 allin1 结构文件，经剪辑服务发出的 JSON 与 float32 响度交给 `op_music`，选段与小节槽和第八版的 `Allin1.music` 逐个相同（10/10）；第八版那次运行留下的 5 个 `music-*.json` 也逐个相同。

**allin1 与开发机结构文件的对照**（[`ai/eval/montage_music_allin1.py`](../../ai/eval/montage_music_allin1.py)，每首起一个 Worker，经套接字与两条管道交歌，与剪辑服务相同）。本票定的误差范围：

| 项 | 要求 | 理由 |
|---|---|---|
| 节拍、重拍 | ±70 毫秒内的 F 值 ≥ 0.99 | 镜头卡在小节线上，重拍错一个就错一个槽 |
| `music` 的选段 | 段落与槽的个数相同，每个切点相差不超过 0.05 秒 | 成片 30 帧，0.05 秒不到 2 帧 |
| 段落边界、标签 | 只记录，不设门槛 | Demucs 的随机平移让同一首歌两次的边界有出入；选段上面已经要求 |

开发机 C（macOS，Apple 芯片 10 核，开发机原有的 allin1 环境：Python 3.11、PyTorch 2.4.1、NATTEN 0.17.1），2026-11-18：

| 歌 | 时长（秒） | 节拍 F | 重拍 F | 边界 F | 标签一致 | 选段最大偏差（秒） |
|---|---|---|---|---|---|---|
| bad guy | 194.1 | 1.000 | 1.000 | 1.000 | 1.000 | 0.01 |
| you should see me in a crown | 181.0 | 1.000 | 1.000 | 1.000 | 0.999 | 0.02 |
| All My Days | 224.6 | 1.000 | 1.000 | 0.966 | 1.000 | 0.00 |
| Symphony | 202.3 | 1.000 | 1.000 | 0.963 | 0.998 | 0.03 |
| Fire | 228.5 | 1.000 | 1.000 | 1.000 | 1.000 | 0.00 |
| Apartment No. 2 | 183.5 | 1.000 | 1.000 | 0.769 | 0.918 | 0.01 |
| Alone | 170.3 | 1.000 | 1.000 | 1.000 | 1.000 | 0.01 |
| Fortnight | 229.0 | 0.999 | 1.000 | 0.957 | 1.000 | 0.00 |
| STAY | 141.8 | 1.000 | 1.000 | 1.000 | 1.000 | 0.00 |
| Forever | 171.4 | 1.000 | 1.000 | 1.000 | 1.000 | 0.01 |

10 首都在范围内：段落与槽的个数全部相同。Apartment No. 2 的边界差得最多：结尾 144–177 秒的副歌，这次在 168 秒处分出了一段尾奏（开发机文件里是副歌一直到 177 秒）。选段在 84 秒结束，不受影响。

## 用时与内存

每首歌起一个 Worker，从启动、核对权重、读入两条流到回答为止计时，峰值内存是这个进程的最大常驻内存（模型在内）。实验 NAS 的 x86 处理器还没测，按[启用手册](../runbooks/enable-montage-music.md)第 5 步补。

| 歌 | 时长（秒） | 开发机 C：用时（秒） | 开发机 C：峰值（MiB） | 虚拟机：用时（秒） | 虚拟机：峰值（MiB） |
|---|---|---|---|---|---|
| bad guy | 194.1 | 263 | 2171 | 200 | 2234 |
| you should see me in a crown | 181.0 | 261 | 2099 | 194 | 2143 |
| All My Days | 224.6 | 304 | 2513 | 240 | 2479 |
| Symphony | 202.3 | 289 | 2210 | 266 | 2309 |
| Fire | 228.5 | 312 | 2365 | 214 | 2549 |
| Apartment No. 2 | 183.5 | 377 | 1954 | 162 | 2172 |
| Alone | 170.3 | 369 | 1812 | 156 | 1949 |
| Fortnight | 229.0 | 306 | 2262 | 201 | 2556 |
| STAY | 141.8 | 188 | 2596 | 131 | 1719 |
| Forever | 171.4 | 227 | 2280 | 148 | 2015 |

- **开发机 C**：macOS，Apple 芯片 10 核，开发机原有的 allin1 环境（Python 3.11、PyTorch 2.4.1），PyTorch 用 4 个线程。
- **虚拟机**：开发机 C 上的 Colima `anas` 虚拟机（aarch64，6 核，8 GiB），Debian 13 容器里的 Python 3.13、PyTorch 2.6.0、NATTEN 0.17.1、madmom 按 `sources.json` 的提交，与 NAS 的运行环境同版本，只是处理器不同。两边的节拍、重拍、段落与标签结果逐个相同。
- **各步用时**：以虚拟机上的 Fortnight 为例，分离音轨 85 秒、谱 2 秒、8 折推理 108 秒、找重拍与段落 2 秒。推理时 NATTEN 的 CPU 算子占大头。
- **一首 4 分钟的歌**：虚拟机上约 4–4.5 分钟、峰值约 2.5 GiB，是 8 GB 预算的三分之一。
- **更长的歌**：峰值随歌长增长，主要在找重拍与段落（madmom 的 DBN）和 8 折推理两步。

| 歌（虚拟机） | 时长（秒） | 用时（秒） | 峰值（MiB） | 分离后 / 谱后 / 推理后的高水位（MiB） |
|---|---|---|---|---|
| Fortnight + Fire | 457.5 | 411 | 4166 | 改动前，没有分步数字 |
| Fortnight + Fire | 457.5 | 581 | 4379 | 2507 / 3442 / 3783（只改了分离一步） |
| Fortnight + Fire + STAY 前段 | 480.0 | 423 | 4593 | 2544 / 2955 / 3942 |

这两首长歌是测试歌接起来的，只看用时与内存。457 秒那首两次的用时差别大，原因没有查；虚拟机与其他会话共用。

**最长 8 分钟（2026-11-18，用户决定）**：按 229 到 457 秒之间每秒约 7 MiB 外推，规格原来允许的 15 分钟要约 7.3 GiB，接近 8 GB 的全部预算。可选的做法有三种：降低能分析的最长时长；在结果不变的前提下继续省内存；放宽这个 Worker 的内存上限。用户选了第一种，最长 8 分钟。

- 剪辑服务收歌的上限、`song_length` 的提示、`music` 与 Worker 能接受的长度都改为 8 分钟（`montage.MaxSongSeconds`）。
- 单元的 `MemoryMax=5632M`：8 分钟的歌实测峰值 4593 MiB，多留约五分之一。
- 两份模型清单的 `memoryBytes` 记这次实测：allin1 记整个 Worker 的峰值，htdemucs 记分离完的高水位。
- 分离与谱两步改成少留几份整首大小的数据（`separate`、谱前先放掉浮点音轨），见“配乐分析 Worker”。用 `--check-separation` 核对过分离结果与 Demucs 原写法逐位相同；STAY 的结构与改动前逐个相同。

## 关联

- 规格与方案：[剪辑中心规格的“配乐”](../specs/montage-center.md#配乐)、[实施方案的“配乐”](montage-center.md#配乐)、[决定记录“allin1 的商业版权”](../decisions/2026-11-17-montage-center-first-version.md#allin1-的商业版权)
- 需求：[#559](https://github.com/zhongwater123/A-NAS/issues/559)（M8b 配乐），父 issue [#514](https://github.com/zhongwater123/A-NAS/issues/514)
- ADR：[0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)、[0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)（运行环境与模型的交付做法）
- 手册：[在实验 NAS 上启用配乐分析](../runbooks/enable-montage-music.md)
- 代码：
  - 选歌、副本与回收、试听、起点：[`internal/montageapi/song.go`](../../internal/montageapi/song.go)、[`internal/montage/songs.go`](../../internal/montage/songs.go)
  - 分析、等待与失败：[`internal/montage/analysis.go`](../../internal/montage/analysis.go)、[`internal/montage/workers.go`](../../internal/montage/workers.go)
  - 配乐页与上传：[`web/src/montage/SongPage.tsx`](../../web/src/montage/SongPage.tsx)、[`web/src/montage/songUpload.ts`](../../web/src/montage/songUpload.ts)
  - 任务取选段：[`internal/montage/jobmusic.go`](../../internal/montage/jobmusic.go)
  - 选段：[`ai/anas_montage/music_ops.py`](../../ai/anas_montage/music_ops.py)、[`ai/anas_montage/domain/music.py`](../../ai/anas_montage/domain/music.py)、[`internal/montagecompute/music.go`](../../internal/montagecompute/music.go)
  - 配乐分析 Worker 与单元：[`ai/anas_montage_music/worker.py`](../../ai/anas_montage_music/worker.py)、[`deploy/montage-music/`](../../deploy/montage-music/anas-montage-music.service)
  - 运行环境与打包：[`scripts/build-montage-music-runtime.sh`](../../scripts/build-montage-music-runtime.sh)、[`scripts/build-content-packages.sh`](../../scripts/build-content-packages.sh)、[`scripts/build-packages.sh`](../../scripts/build-packages.sh)
  - 仓库与软件包不含歌曲的检查：[`scripts/check-no-songs.sh`](../../scripts/check-no-songs.sh)
- 测试：
  - Go：[`internal/montage/songs_test.go`](../../internal/montage/songs_test.go)、[`internal/montage/jobmusic_test.go`](../../internal/montage/jobmusic_test.go)、[`internal/montage/workers_test.go`](../../internal/montage/workers_test.go)、[`internal/montageapi/song_test.go`](../../internal/montageapi/song_test.go)、[`internal/httpapi/montage_song_test.go`](../../internal/httpapi/montage_song_test.go)、[`internal/montagecompute/music_test.go`](../../internal/montagecompute/music_test.go)
  - Web：[`web/src/montage/SongPage.test.tsx`](../../web/src/montage/SongPage.test.tsx)（上传配乐）
  - Python：[`ai/tests/test_montage_music_ops.py`](../../ai/tests/test_montage_music_ops.py)、[`ai/tests/test_montage_music_worker.py`](../../ai/tests/test_montage_music_worker.py)
  - 对照：[`ai/eval/montage_music_golden.py`](../../ai/eval/montage_music_golden.py)、[`ai/eval/montage_music_allin1.py`](../../ai/eval/montage_music_allin1.py)
  - 系统测试的选歌一节：[`scripts/system-test.sh`](../../scripts/system-test.sh)
