# 相册本地 AI（M2）实施方案

状态：步骤 1–7 已实现；步骤 7 按 [ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md) 随系统内置安装（[#58](https://github.com/zhongwater123/A-NAS/pull/58)）；2026-10-10 起搜索改为纯向量排序与两段式结果（[#66](https://github.com/zhongwater123/A-NAS/issues/66)），AI 标签只在 AI 聚合中展示、不参与搜索。2026-10-13 起同一个 Worker 还运行物体检测器，用于文搜图融合、“检测到”分段与以图搜图（[物体检测方案](photo-objects.md)）。Experimental NAS 首次安装带 AI 的版本按[启用相册本地 AI](../runbooks/enable-photo-ai.md)进行，实机资源数字见其中的[首次实机部署证据](../runbooks/enable-photo-ai.md#首次实机部署证据)
更新时间：2026-11-11

本文把[相册技术设计](photo-library.md)中 M2 的切片 8–12 细化为可实现的方案：用 Google EmbeddingGemma 2 的全模态 740M 官方包为照片生成向量，在此基础上提供中文自然语言搜索。产品行为以[相册规格](../specs/photo-library.md)为准，模型与 Runtime 的候选证据见[本地照片 AI 研究](../research/photo-ai-model-runtime-selection.md)。

## 目标与范围

M2 交付后，成员可以：

- 在相册里用中文搜索“海边的猫”“车库里的电动车”，结果只含自己有权查看的照片；
- 给照片添加自己的标签，并按标签找回照片；在 AI 聚合中浏览本地 AI 有把握的照片组；
- 不下载、不安装、不配置任何 AI 组件：AI 随系统安装并默认开启；
- 在 AI Worker 启动中、崩溃、积压或硬件不足时照常使用 M1 的全部功能。

不在 M2 内：人脸与人物库（M3 切片 13）、按需中文描述与视频内容分析。OCR 不在 M2 内，2026-10-19 决定另行实现（见下）。

## 已确认的决定

- **模型包**：使用全模态 740M 官方包（约 485 MB，2026-10-09 决定）。相册只调用文本与图片编码；包内的音频编码器保留，不为相册单独裁剪。
- **OCR**：做（[决定](../decisions/2026-10-19-photo-ocr.md)，推翻了 2026-10-09 的“不做”），识别出的文字与字段进入搜索，可见范围与照片相同（[规格](../specs/photo-library.md)）。模型为 Qwen3.5-4B 的 Q4_K_M（[决定](../decisions/2026-10-30-ocr-model.md)），见 [OCR 实施方案](photo-ocr.md)（proposed）与[模型评估](../research/photo-ocr-model-evaluation.md)。
- **内置**：AI 随系统安装和升级、默认开启，模型与运行环境由开发机经 SSH 传到设备，设备不联网获取（2026-10-10 决定，[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）。

## 已核实的 Runtime 事实（2026-10-08）

- EmbeddingGemma 2 由文本 270M、视觉 170M 与音频 300M 三个编码器组成，可只加载需要的部分；统一输出 768 维向量，支持截断到 512/256/128 维（截断后需重新归一化，128 维对多模态检索损失明显）。[模型卡](https://huggingface.co/google/embeddinggemma-2)
- 官方 LiteRT 包：文本 270M 165 MB、文本+视觉 440M 388 MB、全模态 740M 485 MB。440M 包的文本权重 INT4、视觉权重 INT8；官方在 Linux Arm CPU 上测得文本 105 ms、文本+视觉 825 ms、内存约 647 MB，没有 x86 数据。[LiteRT 包](https://huggingface.co/litert-community/embeddinggemma-2-text-vision-440m-litert-lm)
- MediaPipe 的 Universal Embedder 直接加载上述 `.litertlm` 文件，Python 接口提供 `embed_text` 与 `embed_image`，可选 L2 归一化、`visionTokensPerImage`、`maxInputLength` 与 CPU 后端；MediaPipe Python 包支持桌面 Linux 与 Python 3.9+。官方文档没有单列 Linux x86_64 的结果，需实测。[Universal Embedder](https://developers.google.com/edge/mediapipe/solutions/retrieval/universal_embedder/index)、[Python 指南](https://developers.google.com/edge/mediapipe/solutions/retrieval/universal_embedder/python)
- LiteRT-LM 自己的 Python API 目前只面向生成式对话，没有公开的向量接口，因此 Worker 使用 MediaPipe 而不是直接调用 LiteRT-LM。[LiteRT-LM Python](https://developers.google.com/edge/litert-lm/python)
- 模型卡元数据标注 Apache-2.0，但许可证链接与 MediaPipe 页面写的是 Gemma 许可。发行前必须核对权重文件实际附带的许可证；开发机试验不受影响。

## 开发机首轮实测（2026-10-09）

在开发机的 Debian 13 容器中（Python 3.13.5、MediaPipe 1.1.0，`--cpuset-cpus=0-3` 限定 4 个核，用以近似 i3-12100 的 4 核；实机数字仍需部署后测量）加载全模态 740M 包：

| 视觉 token | 加载 | 加载后 / 峰值内存 | 文本编码（中位） | 512 px 图片编码（中位 / 最大） | 零样本 |
|---|---|---|---|---|---|
| 70 | 5.3 s | 1231 / 1383 MiB | 73 ms | 613 / 756 ms | 4/4 |
| 140（默认） | 4.0–5.7 s | 1442 / 1779 MiB | 71–98 ms | 1698–1737 / 2513 ms | 4/4 |

- 包内只有 70 与 140 两种图片签名；请求 280 会报错 `exceeds maximum available signature length (140)`。Worker 使用 70，并把 token 数写入模型 ID，改动后旧向量自动重建；步骤 3 的评估中 140 的质量差距在 1 个百分点以内、耗时约 2.5 倍，因此保持 70。
- 输出 768 维、已归一化；`embed_image` 直接接受编码后的图片字节，因此 Worker 不需要自己的解码器，传入缩略图 JPEG 即可。无法解码的字节以 `ValueError` 报错，Worker 记为 `invalid_input`。
- 零样本只是冒烟：三幅纯色图形与 matplotlib 自带的一张人物照片，对 7 个中文标签取最高分全部正确，但最高分与次高分只差 0.07–0.15，不能据此设定阈值。三种提示词模板（裸标签、`task: search result | query: …`、`…一张…的照片`）结果相近。
- 依赖：`import mediapipe` 会加载 OpenCV 的绘图工具，图形界面版 OpenCV 需要 X 库，因此换用同版本的 `opencv-contrib-python-headless`；MediaPipe 的 C 库还链接 `libEGL.so.1` 与 `libGLESv2.so.2`，安装 Debian 的 `libegl1`、`libgles2`（只是分发库，不带 GPU 驱动）即可。完整依赖的虚拟环境约 509 MB，步骤 7 的发行运行环境去掉音频与评估依赖后约 460 MB（见“模型与运行环境的交付”）。
- 按 70 token 估算，2 万张照片约需 3.4 小时的空闲 CPU 时间；实机与温度影响待部署后验证。
- 实机（i3-12100，`CPUQuota=200%`，2026-10-10）：首次整理 185 张测试照片，连同 73 个标签文本与几次搜索共用 CPU 约 1,000 秒，约 5 CPU 秒/张，是开发机单张墙钟时间的数倍（XNNPack 多线程计入 CPU 时间）；Worker 内存峰值 815 MB，低于开发机测得的 1.2–1.4 GiB。按此估计 2 万张约需 28 CPU 小时。

## 模型与运行环境的交付

按 [ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)，模型与 Python 运行环境随发行制品送达设备，按内容各存一份并由 release 引用；设备在安装、升级和运行时都不联网获取。模型文件不进仓库，来源与校验值固定在清单 [`deploy/models/embeddinggemma-2-740m.json`](../../deploy/models/embeddinggemma-2-740m.json) 中；物体检测器同样如此（[`deploy/models/dfine-m-obj365.json`](../../deploy/models/dfine-m-obj365.json)）。每个模型有一个“角色”：`model` 是向量模型（`.litertlm`），`detector` 是检测器（`.onnx`）；清单在 release 中为 `ai/<角色>.json`，模型文件链接为 `ai/<角色>.<扩展名>`，以后的人脸模型再加一个角色即可。

- **模型文件**：官方仓库 `litert-community/embeddinggemma-2-740m-litert-lm`（revision `24d962e9…`）中的通用 CPU/GPU 文件 `embeddinggemma-2-740m.litertlm`，484,622,336 字节，SHA-256 `e7a8a2204b91e0f96e92960e84a09a89212e1633dcb7575a9bf3378b4df77f4c`。同一仓库中带厂商后缀的文件（Qualcomm、MediaTek、Tensor、Intel PTL）是特定 NPU 的编译版本，不适用于 i3-12100。
- **运行环境**：[`ai/requirements.txt`](../../ai/requirements.txt) 以版本与哈希固定 MediaPipe、检测器所用的 ONNX Runtime 及其运行所需的依赖：导入 MediaPipe 会加载其绘图工具，所以 OpenCV（headless 版）与 Matplotlib 保留；只服务音频任务的 `sounddevice` 与开发期的评估依赖不装。锁文件由 [`ai/requirements.in`](../../ai/requirements.in) 经 `uv pip compile` 解析生成，GUI 版 OpenCV 与 `sounddevice` 由 [`ai/requirements.excludes`](../../ai/requirements.excludes) 排除。[`scripts/build-ai-runtime.sh`](../../scripts/build-ai-runtime.sh) 在开发机的 Debian 13 容器中按锁文件安装成虚拟环境并打包，运行环境 ID 取锁文件与 Python 版本的 SHA-256 前 16 位十六进制。2026-10-10 的运行环境解开约 460 MB（原开发环境 664 MB），压缩包 163 MB；2026-10-13 加入 ONNX Runtime 后解开约 542 MB，压缩包 189 MB。NAS 不运行 pip、不访问 PyPI，也不需要 `python3-venv`（Experimental NAS 没有安装）：虚拟环境只引用系统的 `/usr/bin/python3`，运行环境的内容包依赖与构建时同一版本的 Debian `python3`（3.13）。
- **开发机**：模型位于 `D:\A-NAS-models\embeddinggemma-2-740m\embeddinggemma-2-740m.litertlm`（WSL 中为 `/mnt/d/A-NAS-models/embeddinggemma-2-740m/`），在仓库之外，以只读方式挂载进测试容器；使用真实模型的可选测试从 `ANAS_AI_MODEL` 读取路径。各清单固定的模型存放在私有仓库的 Release 中，开发机用 `scripts/fetch-models.sh` 拉取并核对（[拉取模型](../development/ai-development.md#拉取模型)）。
- **打包**：`make release` 从开发机的内容库（`models/<SHA-256>/<文件名>` 与 `ai-runtimes/<ID>.tar.gz`，见 [`scripts/build-content-packages.sh`](../../scripts/build-content-packages.sh)）核对清单后，把每个模型与运行环境各打成一个内容包，包名带内容的哈希或 ID，软件源中已有的不再构建（[ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)）。`a-nas` 依赖它这一版引用的内容包，并把 Worker 代码、各模型的清单与单元带在自己包中。部署机把软件源复制到 NAS，见[部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)。Experimental NAS 上 2026-10-09 按名称暂存的模型已于 2026-10-10 核对哈希后移到按哈希的位置。
- **安装**：apt 按软件源的签名核对每个包，暂存账号因此无法改动已校验的内容；内容包把文件放进 `/opt/a-nas/models/<SHA-256>/<文件名>`（`root:root 0644`）与 `/opt/a-nas/ai-runtimes/<ID>/`，已经装的不再下载，旧版本的内容留到 `apt autoremove`。`a-nas` 包的 `/usr/lib/a-nas/ai/` 包含 Worker 代码、清单，以及按角色指向这两处的链接（`model.litertlm`、`detector.onnx`、`runtime`）；单元经 `/usr/lib/a-nas/ai/` 引用它们。`python3`、`libEGL.so.1` 与 `libGLESv2.so.2`（Debian 包 `libegl1`、`libgles2`）由 `a-nas-host` 的依赖装上。
- **校验**：Worker 加载前核对文件大小与 SHA-256，不符时拒绝加载并报告 AI 不可用。

## 整体结构

```text
浏览器 ── anas-api ──► anas-photos（a-nas-photos）──UDS + 只读 fd──► anas-ai（a-nas-ai）
                          │  Catalog：任务、向量、标签、搜索索引              │  MediaPipe Universal Embedder
                          │  空闲与资源门控、Policy 过滤                      │  EmbeddingGemma 2 740M（.litertlm）
                          └──会话套接字──► Host Agent：前台活动与资源信号      └  无网络、无数据卷访问
```

相册服务仍是唯一的权威写入方：它决定何时处理哪张照片、保存结果并按 Policy 过滤。AI Worker 只把收到的图片或文字变成向量，不知道照片属于谁，也看不到数据卷。

## 组件

### AI Worker（`anas-ai`）

- 一个小型 Python 进程，内部有两个 Provider：MediaPipe Universal Embedder 加载全模态 740M `.litertlm` 产生向量；ONNX Runtime 加载 D-FINE 检测器，只在有检测任务时才创建会话（[物体检测方案](photo-objects.md#worker)）。全模态包会占用多少内存、能否只初始化文本与图片编码器，官方没有数字，由步骤 3 实测。测试用的 Fake Provider 实现同一协议，返回由输入哈希得到的确定性向量与框。
- 单元见 [`anas-ai.service`](../../deploy/systemd/system/anas-ai.service)：以 systemd 动态分配的身份 `a-nas-ai` 运行，不在系统中常驻账号；`PrivateNetwork=yes` 与 `RestrictAddressFamilies=AF_UNIX` 使它既没有网络也打不开网络套接字，`InaccessiblePaths=/srv/a-nas` 挡住数据卷。内存不设固定预算（[决定](../decisions/2026-10-28-ai-memory-headroom.md)）：后台任务加载模型前确认加载后仍余 1 GiB，`a-nas-generator` 按本机内存写入的 `MemoryMax`（总内存减 2.5 GiB）只防失控，模型不换出（`MemorySwapMax=0`），内存耗尽时内核先结束 Worker（`OOMScoreAdjust=500`，[资源策略](photo-objects.md#资源策略)）。Worker 在 `anas-ai.slice` 中运行，上一层 `anas.slice` 的 `CPUWeight=10` 让设备上的其他工作在争用时先拿到核。CPU 不再固定为 2 核（[决定](../decisions/2026-10-13-ai-uses-idle-capacity.md)：闲时有多少用多少，始终留出余量）：检测推理用可用逻辑核数减 2 个线程，`a-nas-generator` 按本机核数在 `/run/systemd/generator/anas-ai.service.d/a-nas-reserve.conf` 中写入 `CPUQuota=(N−2)×100%` 兜底（[资源策略](photo-objects.md#资源策略)）。XNNPack 的权重缓存放在 `CacheDirectory`。系统测试把探针放进 Worker 自己的单元，确认它打不开 IPv4、IPv6 与 netlink 套接字、列不出数据卷；界面的隐私说明以此为依据（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）。
- 按需启动：[`anas-ai.socket`](../../deploy/systemd/system/anas-ai.socket) 监听 `/run/a-nas-ai/ai.sock`（`root:a-nas-photos 0660`），第一次请求时由 systemd 拉起 Worker，空闲 10 分钟后自行退出并释放模型内存。任何一次连接都会拉起 Worker 并重新计时，所以相册服务在得知模型 ID 之后，只在有到期的向量任务时才联系它；模型 ID 在服务启动后的第一轮得知，更换模型随新版本而来（软件包安装时重启相册服务），搜索遇到新模型时也会让后台重新核对。软件包启用套接字；升级时 `postinst` 停止仍在运行的旧 Worker，下一次请求启动新版本的。运行环境缺少 MediaPipe 时 Worker 回答“不可用”而不是退出，避免每次连接都重启它直至 systemd 停用套接字。搜索因此可能承担一次模型加载时间，由切片 9 实测决定是否改为常驻文本编码器。
- 模型与运行环境的来源、位置与校验见“模型与运行环境的交付”。软件包随系统启用套接字，没有“未安装”状态；Worker 启动中、崩溃或模型校验失败时相册照常可用，搜索退回按名称与用户标签匹配。设备低于最低硬件（待实机测量）时不启动 Worker。这些情况下界面都不说 AI 不可用（[相册界面规格](../specs/photo-gallery-ui.md)）。

### 协议

客户端见 [`internal/aiworker`](../../internal/aiworker/client.go)，测试用的假 Worker 见 [`aiworkertest`](../../internal/aiworker/aiworkertest/aiworkertest.go)。

- 相册服务是客户端，Worker 是串行服务端（并发 1）。每次调用新建一个连接，发一帧请求、读一帧响应；帧为 4 字节大端长度加 JSON，上限 1 MiB。图片随请求以 `SCM_RIGHTS` 传入只读文件描述符（Go 侧用 `golang.org/x/sys/unix`，Python 侧用 `socket.recv_fds`），Worker 不接受路径。一条请求最多 20 个描述符（`describe_frames` 的关键帧）；超出的被内核丢弃，Worker 读完这一帧后答 `invalid_request`。
- 请求 `{"op": ...}`：`info` 返回 `model`（含权重哈希与预处理版本）与 `dimensions`，检测器可用时另有 `detector`，以及 `memoryBytes`：Worker 启动后的内存加上各可用模型清单的 `memoryBytes`，供门控在加载前核对（[资源策略](photo-objects.md#资源策略)）；`detect_objects` 返回检测器的模型与框，`release` 卸载检测会话或全部模型（[物体检测方案](photo-objects.md#worker)）；`embed_image` 返回 `vector`（小端 float32，JSON 中为 base64）；`embed_query` 把 `text`（至多 1,000 字符）当作搜索查询编码，由 Worker 加上模型自己的检索前缀（EmbeddingGemma 为 `task: search result | query: …`），向量与图片向量可直接比较。前缀属于模型的使用约定，随 Worker 与模型一起更换。`read_text` 读出图片中的文字或字段，`info` 另以 `ocrModel` 与 `ocrMemoryBytes` 报告它，单独计算内存（[照片文字识别](photo-ocr.md#在-worker-中运行)）。`describe_frames` 用同一个读文字模型描述剪辑中心的一张卡片（见下文“剪辑中心的分析”）。
- 失败响应为 `{"ok": false, "error": {"code", "message"}}`：`invalid_input` 表示这张图片无法处理，任务记为失败；`unavailable` 表示 Worker 在运行但不能服务（例如模型文件缺失或校验不符），任务等待；`invalid_request` 表示请求本身越界（描述符太多、字段不合规则）；`invalid_result` 表示模型的回答重问几次仍不合 Schema。
- 连不上 Worker 时任务等待，不消耗尝试次数；Worker 收到图片后才失败或退出（崩溃、超时、乱码响应）则消耗一次尝试，三次后记为失败，避免反复拖垮 Worker 的图片无限重试。每次调用默认最长 2 分钟，覆盖按需启动后的模型加载。

### 输入图片

AI 输入使用已有的缩略图派生 `thumbnail/v1`：长边 512 px、已按 EXIF 方向转正、透明区域合成白底，大小有界。这样 Worker 不必解码任意尺寸的原图，也不会因超大图片耗尽内存。步骤 3 比较过 1024 px 输入：同一图片的两种向量余弦相似度平均 0.984，暂不增加专用的 AI 输入派生（[校准报告](../research/photo-ai-label-calibration.md#视觉-token-与输入尺寸)）。物体检测需要更多细节，相册服务为它临时渲染长边 1600 px 的 JPEG，经密封的 `memfd` 传入，不落盘（[检测输入](photo-objects.md#检测输入)）。

### 门控与三类工作

相册服务的 AI 门控（[`photoservice/gate.go`](../../internal/photoservice/gate.go)）按谁在等，把相册里与 AI 有关的工作分成三类。三类工作只在这一节定义，其他文档链接这里；新的 AI 工作先按谁在等归入一类，再写进这里。压力与内存的阈值、模型的加载与卸载见[资源策略](photo-objects.md#资源策略)。

- **前台请求**：成员在页面上等结果的请求。
  - 现在包括：
    - 相册 API 上登录成员的请求，如浏览、搜索、以图搜图、上传、下载与播放视频，从开始到结束都算（`Gate.Use`，[`photoservice.Authenticate`](../../internal/photoservice/photoservice.go)）；读取 AI 状态（`GET /api/v1/photos/ai`）不算，窗口可以一直轮询。
    - 助手套接字上的请求，收到时算一次（`Gate.Touch`，[`assistant.go`](../../internal/photoservice/assistant.go)）。
    - 剪辑中心二次编辑的文字搜镜头 `searchWindows`：经授权套接字，从开始到结束都算（[ADR 0039](../adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md) 第 8 项，由 [PR #631](https://github.com/zhongwater123/A-NAS/pull/631) 加入）。授权套接字上的其他请求不算。
  - 不经过门控，照常加载所需的模型，不做加载前判断。
  - 前台请求进行中，以及最后一个结束后的 5 分钟（`QuietPeriod`）里，算“有人在用相册”。门控按全机计，不分是谁在用。
- **成员在等的后台工作**：成员的某个任务在等它做完，成员不必开着页面。
  - 现在只有剪辑中心的分析：窗口（`montage-windows/v1`）、卡片（`montage-cards/v1`）、画质（`montage-quality/v1`）与卡片描述（`montage-describe/v1`）。
  - 门控回答它时不看有没有人在用相册（`Gate.OpenForMember`、`ContinueForMember`，[`photos.MemberGate`](../../internal/photos/montagewindows.go)）。开始下一块前，CPU、I/O、内存的压力与内存余量都要允许；正在做的一块每 200 毫秒问一次门控，只在内存不足（低于余量、装不下要加载的模型或内存压力超限）时挂断，CPU、I/O 压力只挡下一块。
  - 每轮先做它（[`aiRunner.round`](../../internal/photos/airunner.go)）。有人在用相册时，一轮只做这一类，相册自己的后台工作照旧等。
  - 前台请求不挂断它。AI Worker 一次只服务一个连接（[`worker.py`](../../ai/anas_ai/worker.py)），所以成员这时搜索照片，查询的编码要排在 Worker 手上的这次调用之后：窗口每帧调用一次，和一张照片的向量相当；一张卡片的描述在实验 NAS 上平均 74 秒（[调查](../investigations/2026-11-19-montage-job-stalls-on-the-experimental-nas.md)）。
  - 这一类是 2026-11-19 修复[剪辑任务停在“等待分析”](../investigations/2026-11-19-montage-job-stalls-on-the-experimental-nas.md)时加的，此前剪辑中心的分析和相册自己的后台工作一样等相册空闲。2026-11-25 用户[决定](../decisions/2026-11-25-photo-ai-gate-member-work.md)保留这种做法。
- **相册自己的后台工作**：整理照片，没有人在等其中某一项。
  - 现在包括：照片的向量、物体、人脸、宠物与文字识别，标签与查询文字的编码，人物聚类，以及向量索引的同步，其中也有剪辑中心窗口向量的同步（[向量索引](vector-index.md)）。
  - 只在没有人在用相册、压力与内存余量都允许时开始（`Gate.Open`）。门控关闭时不领新任务，关闭期间至多每分钟（不长于空闲期）复查一次，因 CPU 或 I/O 压力关闭时每 5 秒复查。
  - 正在做的检测与读文字每 200 毫秒问一次门控，门控因任何原因关闭都立即取消，退回队列、不计尝试次数；向量推理不能中断，做完为止。
  - 空闲期默认 5 分钟，系统测试用 `ANAS_PHOTOS_AI_QUIET_PERIOD` 缩短。

门控的其他输入：

- **压力**：`system.slice` 与 `user.slice` 的 CPU、I/O、内存 PSI（`avg10`），经 `prometheus/procfs` 读取。Worker 在自己的 `anas-ai.slice`，它自己的等待不算。内核没有压力信息时只看前台请求与内存。
- **内存**：系统可用内存低于 1 GiB 余量时门控关闭，并请 Worker 卸载模型；后台工作要加载的模型放不下（加载后不足 1 GiB）时只等待、不卸载。检测队列清空或有人在用相册时只卸载检测会话，CPU 与 I/O 压力只取消、不卸载。
- **还没有实现的**：Host Agent 在相册会话套接字上提供只读的 `GET /v1/activity`，报告 Web 文件操作（文件代理）、磁盘与网卡吞吐（SMB 传输体现在这里）和 CPU 温度，门控把文件操作与吞吐算作有人在用设备，温度超过上限时关闭。相册服务运行在 `PrivateNetwork` 中，看不到宿主机网卡，因此不自行采样。在此之前，Web 文件与 SMB 只通过系统压力影响门控。

### 任务与调度

- 复用 M1 的持久任务表：缩略图完成后，在同一事务中为该原图对象排入 `embedding/v1` 与 `objects/v1` 任务（`v1` 是处理流程版本，模型 ID 随结果保存）；后台先做向量、再做检测。向量只依赖原图字节，以对象为键，副本与重复照片共用。缩略图与 AI 任务由两个循环分别领取。
- 缩略图任务照旧立即执行；AI 任务只在门控允许时领取，按[三类工作](#门控与三类工作)。
- 更换模型后，第一次见到新模型 ID 时把旧模型的向量全部重新排队；旧向量在新向量写入前继续保留。
- **剪辑中心的分析**（[剪辑中心实施方案](montage-center.md#剪辑分析)的“剪辑分析”）：只为剪辑中心经项目授权请求过的视频排 `montage-windows/v1`，视频仍不进 `inLocalAI`。
  - **入队**：已有当前分析身份的完整窗口就不排；两个授权请求同一个视频只排一次。分析身份是模型 ID、向量维数，加上窗口、取帧与编码的参数。相册服务启动后、AI 运行器还没问过 Worker 时，只有一种身份的完整窗口也算完成，第一轮发现模型不同再重排。
  - **调度**：每轮先做它。先用只读查询确认有到期的剪辑任务，再删掉已没有授权引用的待做任务，然后领一个。一个视频一个任务，每轮只做 30 个窗口（一块），没做完就放回队首、不耗尝试次数，下一轮先问门控；块内每 30 秒续一次租约。
  - **取帧与编码**：用 FFmpeg Worker 的 `frames`（后台优先级），按缩略图的流程转成长边 512、质量 82 的 JPEG，灰度标准差小于 8 记为纯色，再交给 `embed_image`。
  - **门控**：剪辑中心的窗口、卡片、画质与描述都是[成员在等的后台工作](#门控与三类工作)，不等相册空闲，何时开始、何时挂断见那一节。
  - **挂断**：门控对正在做的块单独回答内存（`Gate.ContinueForMember`），所以 CPU 压力同时存在也不会掩盖内存不足。挂断后本轮结束，和暂停的检测一样按原因卸载模型，照片的 AI 工作也等下一轮；已算完的窗口保留。
  - **失败**：取帧或解码失败按缩略图的规则分类；AI Worker 自己的错误按照片向量的规则记 `io`，最多试 3 次；超过 6 小时（10,800 个窗口）的视频记 `too_long`，不分析。
  - **结果**：存在 Catalog 的 `video_windows`（migration 18，带回滚），按内容对象复用。写入前确认任务还在，块进行中视频被清除或分析被删除时丢弃这一块。同步进窗口集合见[向量索引](vector-index.md#剪辑中心的窗口集合)。
  - **交给剪辑中心**：`lookup` 一次读出全部资产的分析进度；`analysis` 答一个视频的分析，JSON 只有状态、身份与计数，窗口向量与卡片放在封口的只读 memfd 里（[`unixmsg.WriteSealedFile`](../../internal/unixmsg/memfd_linux.go)），所以应答大小与视频长短无关。格式与读取上限以 [`GrantAnalysis`](../../internal/photos/montagewindows.go) 的注释和 `ReadWindows` 为准。只答当前身份的结果：读取期间窗口被重做，只答 `windowsState: queued`；描述被重试或输入变化，`descriptionsState` 改答 `queued`，不给描述。剪辑中心先按 `lookup` 判断四个阶段，读 `analysis` 时再核对一次，有一个不是 `done` 就继续等分析（`waiting(analysis)`），不拿缺了卡片或描述的结果去规划（[`analysisKey`](../../internal/montage/steps.go)，[#584](https://github.com/zhongwater123/A-NAS/issues/584)）。
  - **换模型**：只重排仍被未撤销的授权引用的对象；授权撤销后在 7 天内再次授权的，由入队时的身份判断重排。没有授权引用满 7 天的分析由每小时的维护删除。
  - 窗口完成后排 `montage-cards/v1`，再按卡片逐张排 `montage-quality/v1`。Catalog 第 19 步建 `video_cards`、`video_card_quality` 与 `montage_card_results`，第 23 步给 `video_cards` 加每张关键帧的节拍（`beats`），都记录回滚步骤。之前切的卡片没有节拍，切卡身份变了会重切；读到没有节拍的卡片时一张关键帧一个节拍。完成身份包含上游分析身份，也记录零卡片和零画质的成功结果。授权的 `analysis` 返回两项身份，`analyze(retry=true)` 重排失败的任务。
  - **卡片与画质的调度**：每轮先于 AI Worker 的门控做，不问 AI Worker。先用只读查询确认有到期的任务，再删掉已没有授权引用、也没人在做的卡片与画质任务，然后领一个；再次授权并请求分析时补排缺的任务。门控挂断或服务停止时任务退回原位、不耗尝试次数，本轮结束，按原因卸载模型。计算 Worker 或 FFmpeg Worker 连不上时任务等 1 分钟、不耗尝试次数，本轮接着做照片的 AI 工作；每次都结束本轮的话，Worker 不在期间照片的工作会一直等。计算 Worker 执行中失败或超时、读 Catalog 或原始文件出错记 `io`，最多试 3 次；请求被拒绝记 `invalid_result`，原始文件不存在记 `missing_original`，都不再重试。
  - 切卡片与量画质使用 `anas-montage-compute.socket` 上的计算 Worker，无需加载相册模型。门控挂断时（[成员在等的后台工作](#门控与三类工作)）中断当轮并归还任务。Worker 在 `anas.slice` 下运行，单实例，使用 AI 运行环境，内存上限 1 GiB。systemd 限制它访问数据卷和 IP 网络。相关实现和验证见 [相册队列](../../internal/photos/montagecards.go)、[Worker](../../ai/anas_montage/worker.py) 与 [系统测试](../../scripts/system-test.sh)。
- **卡片描述**（`describe_frames`，[`describe.py`](../../ai/anas_ai/describe.py)）：
  - 输入一张卡片的 3–20 张关键帧（每张一个描述符）、每张离卡片开头的秒数（`offsets`）、分组布局（`groups`，从 1 数的节拍，相邻相像的关键帧同一个节拍）与卡片时长（`duration`）；越界答 `invalid_request`。
  - 提示词、体裁（旅行，`TRAVEL`）与 Schema 固定在 Worker 里，版本 `montage-card/v2`，随回答的 `template` 返回：第八版（提交 `19f2d6db`）的 `PROMPT`、`CAMERAS` 与 Schema 原样搬来，加一句不要转写画面里的证件号、票号、车牌等号码；v2（[#676](https://github.com/zhongwater123/A-NAS/issues/676)）按给定的节拍写，一个节拍的几张图写成一项，相邻节拍要写出不同。
  - 用读文字的 llama-server，设置照第八版：系统提示词、`response_format` 按 Schema 约束、温度 0.2、回答上限 1,800 token、不开思考；回答不合 Schema、截断或陷入重复时依次以重复惩罚 1.1、1.2、1.3 重问，三次都不合格答 `invalid_result`。重问复用同一提示的缓存。
  - **关键帧放不下时抽帧**：上下文 8,192 token 里要给回答留足 1,800。提问前按关键帧的像素估算提示的 token（提示词约 680，v2 比 v1 用 `/tokenize` 数多约 60；每张关键帧是它的标签加每 32×32 像素一个视觉 token，512×910 的竖拍约 487，512×288 的横拍约 167），放得下的卡片原样提问。放不下的（竖拍 12 张及以上）从第一张到最后一张均匀取放得下的张数再提问，没有取到关键帧的节拍不写。回答里每个节拍的 `frames` 是它的关键帧在这张卡片全部关键帧里的序号（从 1 起），所以合成过节拍或抽过帧的卡片，节拍数少于关键帧数，使用方按 `frames` 找到节拍对应的关键帧。每次描述在日志里记下估算值与 llama-server 实际数出的提示 token 数。估算偏低时仍有兜底：提示过长，或回答被截断且实测提示 token 数已挤占 1,800 token 的回答预算时，答 `invalid_input`。
  - 和 `read_text` 一样在线程里运行、调用方挂断即停；客户端对它的超时是 15 分钟。
- **相册的 `montage-describe/v1`**（[`montagedescribe.go`](../../internal/photos/montagedescribe.go)）：在画质步骤（`montage-quality/v1`）完成的事务里排队，只描述画质结果 `reasons` 为空的卡片；重新切卡片时一并删掉旧描述。
  - 每轮一张卡片，属于读文字阶段：按 `ocrMemoryBytes` 问准入，通过后释放检测模型；只有读文字模型装不下时，本轮继续处理能运行的相册任务；没描述完的任务放回队首、不耗尝试次数，调用期间每 30 秒续租。
  - 关键帧由 FFmpeg Worker 的 `frames` 取（宽 512、质量 3，第八版的设置），取帧与描述期间每 200 毫秒问一次门控，挂断的条件见[成员在等的后台工作](#门控与三类工作)，挂断时任务退回原位。
  - 存进 `video_card_descriptions`（migration 20，带回滚）之前，用 `textread.MaskNumbers` 把每段文字里的身份证号、银行卡号、手机号与 15 位以上数字串换成 `[身份证号]` 一类标签，相册不保留原文。
  - 超出上下文（`invalid_input`）或回答不合 Schema（`invalid_result`）的卡片只把错误记在这张卡片上，同一段视频的其他卡片照常；Worker 崩溃、超时、取不到关键帧等临时故障按卡片最多尝试三次，之后记录该卡错误并继续；成员重试时重新处理失败卡片。回答的模板与相册认的不一致、号码过滤读不了回答，也只记在这张卡片上。原始文件打不开（不是“不存在”）记 `io`、耗一次尝试，三次后整个任务失败；启动时不重排失败的描述任务（一次尝试最长 15 分钟），相册没见过的读文字模型会重排。每条描述记下写入它的版本（Catalog 第 22 步，带回滚）：在别的版本下失败的卡片与过期的一样补排，所以每个新版本为仍被授权引用的视频、以及重新授权的视频，把失败的卡片各重试一次；描述成功的卡片不动，没有版本号时（开发与 Fake 模式）不重试。
  - 分析身份是读文字模型、`montage-card/v2`、体裁、关键帧设置、号码过滤版本、切卡身份与画质身份。更换读文字模型或在保留期内重新授权并请求分析时，补排缺失或过期的描述。
  - Worker 没有读文字模型时画质完成也不排描述，`lookup` 的 `descriptions` 报 `unavailable`（本地模型不可用）；画质完成且没有合格卡片时直接报完成。有了读文字模型（随新版本重启）后，第一轮为仍被授权引用、画质已完成而缺当前描述的对象补排。
  - 开发机实测（2026-11-18，Apple 芯片，Homebrew 的 llama-server b11960，本机下载的 Qwen3.5-4B Q4_K_M 与 mmproj F16，`LlamaReader` 自己的启动参数 `-c 8192 -np 1 --image-max-tokens 1024`）：把品杰样本的关键帧转成 512×910 的竖拍，11 张（样本里关键帧最多的卡片）提示 5,955 token，50 秒描述完、回答完整；12 张 6,440 token；20 张 10,320 token，llama-server 拒绝，当时这张卡片记为描述失败。每张竖拍约 485 token，与方案按第八版估的 450 相近。抽帧的估算取自这组数字；抽帧后的描述、横拍等其他画幅的估算、固定版本 b11655 与清单里的模型文件都还没有实测，留到实验 NAS 验收。

### 存储（Catalog migration 4–7）

- `embeddings(object_id, derivation, model, vector, created_at)`：每个对象一行，记录产生它的模型，768 维 float32 约 3 KB；20,000 张约 60 MB。
- `detections(object_id, derivation, model, boxes, created_at)`（migration 7）：每个对象一行检测结果，每张约 1.4 KB，格式见[物体检测方案](photo-objects.md#任务与存储catalog-migration-7)。
- `query_vectors(model, text, vector, created_at)`：Worker 为标签文本编码的向量，按模型与文本缓存，重启后无需 Worker 即可列出 AI 聚合。
- 用户元数据（migration 6，[`albums.go`](../../internal/photos/albums.go)、[`usertags.go`](../../internal/photos/usertags.go)）：`albums(id, library_id, name, created_by, created_at)` 与 `album_assets(album_id, asset_id, added_by, added_at)` 记录相册分组；`user_tags(asset_id, name, …)` 与 `ai_tag_corrections(asset_id, label_id, verdict, …)` 以照片资产为键。照片永久删除时随之删除；AI 标签的计算与派生数据的清除永远不改动它们，复制照片时一并复制标签与纠错。

### AI 聚合

已实现于 [`photos/ailabels.go`](../../internal/photos/ailabels.go)，词表与阈值随程序发布（[`photos/labels`](../../internal/photos/labels/labels.go)）。

**由来**：步骤 3 与 5 曾用同一个向量做零样本标签，显示在照片详情上，查询提到已校准的标签时还按阈值过滤搜索结果。部署后发现：
- 标签误导：航拍城市被标成“飞盘”。
- 标签过滤让 COCO 上约一半真实照片从搜索结果中消失（召回 53%）。
- 整图向量本来就找不到小物体：搜“手机”时，手机不到画面 1% 的照片只有 39% 排进前 R 名（R 为真实照片数），大手机为 100%。

#66 因此关闭了全部标签。之后用户希望把按内容分组的呈现找回来，并定下两条规则（2026-10-10）：
- **红线**：由向量模型产生的标签不参与搜索。
- **宁缺毋滥**：宁可少标，不能错标（[决定](../decisions/2026-10-10-ai-labels-as-groups.md)）；每组 80% 以上正确可以接受（[决定](../decisions/2026-10-12-ai-group-precision.md)）。

**行为**：
- 标签只以 AI 聚合呈现：照片向量与某个标签文本向量的相似度达到阈值、且这张照片没有被移出，就归入该标签的一组。
- 接口：
  - `GET /api/v1/photos/labels` 按组列出调用者可见范围内的张数与最有把握的封面；
  - `GET /api/v1/photos/labels/{id}/assets` 按把握从高到低分页列出一组；
  - `DELETE /api/v1/photos/assets/{id}/ai-labels/{label}` 把一张照片移出一组，记为该照片资产的 AI 纠错，复制与模型升级后仍然有效。
- 照片详情不列 AI 标签；搜索只用名称、用户标签和向量排序（[`TestSearchNeverUsesClusters`](../../internal/photos/ailabels_test.go) 守住这一点）。
- 标签分数在读取时算出，阈值随新版本立即生效。标签文本的向量由 AI 后台任务逐条请 Worker 编码，存入 `query_vectors`；全部就绪前不列出任何组。阈值只对校准时的模型成立：模型不符时不编码标签文本，也不列出任何组。
- 这项后台任务和照片向量一样，要等相册空闲 5 分钟才运行（除读取 AI 状态外，任何相册请求都会重新计时），所以新版本部署后分组不会马上出现。`GET /api/v1/photos/ai` 的 `clusters` 只读 Catalog，说明分组是 `ready` 还是仍在 `preparing`（模型不符时没有这一项）；AI 聚合页只轮询它，等分组为 `ready` 且没有照片待整理时才重新读取一次分组，因为读取分组本身也会让剩下的后台任务再等 5 分钟。

**校准**：按聚合的用法从严，方法与数字见[校准报告](../research/photo-ai-label-calibration.md#ai-聚合的严格校准)。
- 每组在校准半上收入的照片要“可信地”至少 80% 正确（单侧 95% Wilson 置信下限），且至少收入 10 张才启用。只看测出来的比例不够：样本只有十几张时，“桥”在校准半 13 张对 11 张，到测试半只有 44%。
- 场景标签的真值来自 COCO-Stuff。场景太常见，统一的误报率上限只能留下不到一成的真实照片，所以场景标签改为取仍然可信地达到 80% 的最宽阈值。
- 校准半除 COCO val 的一半外，还有 COCO 2017 train 中按哈希固定选出的 20,000 张（2026-10-12 加入）：只用 val 时稀有标签和多数场景只有十几张照片，看不出一组对不对。测试半仍只来自 val，不参与任何选择。
- 校准给出 59 组。其中 7 组在测试半上不足 10 张、无法独立核对，沙发在测试半上只有 71% 正确，这 8 组按[决定](../decisions/2026-10-12-ai-group-precision.md)手动留下，名单记在阈值文件的 `heldBack` 中。
- v1 阈值启用 51 组：动物 11、自然 5（雪、大海、草地、天空、树）、食物 7、交通工具 7、运动 3、物品 18。在测试半上，各组收入的照片 93.0% 正确（2,436 张）；最低的是草地 82%、天空 82%、树 83%。留下 8 组是看过测试半才定的，不受影响的数字是留下之前 59 组的 92.8%。
- 场景标签的正确率与这类场景在照片中有多常见有关：COCO 里天空约占四分之一的照片，家庭照片里室内照片更多，天空这一组的实际正确率可能低于测得的数字。
- 山、沙子、河、花、建筑、道路在这一标准下仍然不启用。

**之后**：
- 固定类别的目标检测已在开发机评估（[决定](../decisions/2026-10-10-object-detector-d-fine.md)选定 D-FINE Objects365，[检测与混合检索研究](../research/photo-ai-object-detection.md)）：小物体召回翻倍，融合后句子类查询不受影响，COCO 之外的类别需按类启用。[实施方案](photo-objects.md)于 2026-10-13 确认，实现进行中：检测器的类别不由向量模型产生，不受上面的红线约束，参与文搜图的融合与“检测到”分段；本期不组成 AI 聚合。
- 小物体也在开发机评估过“分块向量”：每张照片除整图外再按网格切块，各算一个向量，搜索取整图与各块的较高分（[`eval.tile_search`](../../ai/eval/tile_search.py)）。在同一测试上 2×2 分块只把不到画面 1% 的物体的召回从 36.0% 提到 38.0%，向量计算却是 5 倍，不进入产品。

### 语义搜索

已实现于 [`photos/search.go`](../../internal/photos/search.go)，桌面相册窗口提供搜索框。

- `GET /api/v1/photos/search?q=&viewing=&cursor=&limit=`：查询 1–200 字符。相册服务请 Worker 编码查询，再按余弦相似度给范围内未进回收站的照片排序。2026-11-06 起由[向量索引](vector-index.md#读路径)选出可能排进前面的照片，再按 Catalog 的向量精确打分，结果与精确扫描相同；向量不再常驻相册服务的内存（此前 20,000 张约 60 MB）。
- 排序（2026-10-10 决定，[#66](https://github.com/zhongwater123/A-NAS/issues/66)，取代按标签阈值过滤与“至多 20 张最接近”）：名称或用户标签包含查询（不区分大小写）的照片排在最前；其余有向量的照片全部按与查询的相似度排列，至多 500 张，不按任何标签过滤。
- 两段（方案 C）：名称与用户标签的匹配，加上相似度在最佳匹配 0.04 以内的照片，组成“最接近”的一组；其后的照片相关度较低。响应的 `closest` 给出这一组在所有页中的数量，每页相同，界面在其后画分隔线。依据见[校准报告](../research/photo-ai-label-calibration.md#搜索结果的取舍)：只排序时前 500 张中只有 15% 相关；0.04 以内约 90% 相关、召回约 33%。相似度没有能区分相关与否的分数线，图库里根本没有要找的东西时也会划出一组，所以界面只称“最接近”。2026-10-13 起提到物品的查询把检测分数融合进排序，以物体词为主的查询由“检测到”的照片组成第一段，响应另带 `objects`（[物体检测方案](photo-objects.md#搜索)）；以图搜图见同一方案。
- 分页游标记录上一页最后一项的分数与 ID；每页重新排序一次。#66 的评论中有 2 万与 10 万条的扫描实测。向量 2026-10-26 起同步到相册服务独占的[向量索引](vector-index.md)，影子比对全部一致后，2026-11-06 起排序从索引读（[ADR 0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)）；索引答不了时读 Catalog，结果不变。
- 范围按[规格](../specs/photo-library.md)：普通搜索覆盖自己的私有图库与共享图库，不混入任何查看中的成员图库；管理员在查看模式下用 `viewing` 指定所查看的成员私有图库，经有效授权校验后加入，授权过期或无权时与不存在的图库一样返回 404。候选只从范围内的图库查询，结果来自 Catalog 当前记录。
- AI 不可用（Worker 未运行、模型不符或编码失败）时只按名称与用户标签匹配，响应的 `semantic` 为 `false`，`closest` 等于结果数，窗口不另作提示（2026-10-10 决定）。
- 时间、地点与人物不是向量能回答的条件（“去年夏天”“妈妈”）：对应能力实现后，从查询中解析出这些条件再过滤，其余部分仍按向量排序。

### 界面上的整理进度与隐私说明

- 相册侧栏的 AI 卡片（[`Sidebar.tsx`](../../web/src/photos/Sidebar.tsx)）读取 AI 状态接口（`GET /api/v1/photos/ai`，读取不计入前台活动），显示整理进度、暂停原因与无法识别的数量，AI 无法运行时隐藏；AI 聚合页（[`AIHome.tsx`](../../web/src/photos/AIHome.tsx)）说明照片只在这台设备上由本地 AI 识别。界面不显示模型名称、版本或组件状态，接口返回的模型 ID（`model`，检测器为 `objectModel`）只用于诊断。
- 隐私说明以 Worker 沙箱的系统测试为前提，测试未通过的版本不得发布。
- 各图库的 AI 开关见[规格](../specs/photo-library.md)，不在步骤 7 内实现。

### 失败、版本与重建

- AI 处理状态与照片状态分离：`pending → running → ready / failed`，失败保存稳定错误类别。
- 每条向量记录模型 ID（含权重哈希与预处理版本）。更换模型或预处理后，旧结果标记为 `stale`，后台重建；重建期间旧向量是否继续参与搜索是规格中的待定项，建议继续参与直到新向量就绪。
- 删除最后一个引用时，向量与 AI 标签随对象删除；用户元数据随照片资产删除。

### 美学打分

按 [#480](https://github.com/zhongwater123/A-NAS/issues/480) 的方案一，Worker 内用 PyTorch 运行打分器，给缩略图打美学、技术两个分（1–5，不截断）。目前只做到 Worker，相册服务的任务、存储与界面未接入。

- **打分器**：Qwen3.5-4B 的冻结视觉塔和两个 MLP 头由 [#480 的推理代码](../../ai/qv_head/)加载；设备只安装推理所需的模块。
- **模型文件**：[部署清单](../../deploy/models/qwen35-4b-aesthetic-scorer.json)固定发布包 tar 的 SHA-256。内容包装入 `MANIFEST.json`、`model/` 和视觉塔原附的许可证，`ai/aesthetic-scorer.bundle` 链接到 `/opt/a-nas/models/<tar 的 SHA-256>/`。视觉塔与 OCR 的 mmproj 格式不同，安装该模型时系统盘多占约 667 MB。
- **校验与加载**：Worker 启动时验证发布清单的哈希和结构。首次打分在同一 systemd 沙箱的子进程中启动 CPU 运行时，先设置线程数，再校验权重并加载模型。后续请求复用子进程；挂断或 `release` 时回收子进程，包括首次加载期间。
- **运行环境**：CPU 版 PyTorch 和 torchvision 从官方 CPU wheel 安装，版本与哈希见[锁文件](../../ai/requirements.txt)。依赖门禁从 wheel 文件名提取上游公开版本进行公告审计。单元启用 Hugging Face 和 transformers 离线模式，并将其缓存放在 Worker 的缓存目录。
- **协议**：`score_aesthetic` 接收一个缩略图描述符，返回 `model` 与 `scores`（`aesthetic`、`technical`）。模型 ID 包含已验证的发布清单 SHA-256（覆盖视觉塔、打分头及预处理配置）与推理库版本。`info` 以 `aestheticModel` 和 `aestheticMemoryBytes` 单独报告打分器，供后续调度使用。
- **资源**：清单 `memoryBytes` 先按 2 GiB（开发机实测常驻约 1.7 GB、峰值约 1.9 GB）；推理线程与其他模型相同，为可用逻辑核数减 2。开发机 CPU 上 512 px 缩略图每张约 5.5 秒，只适合空闲时后台整理。
- **验收与分发**：默认发布、设备镜像与主包依赖均不包含打分权重。内部验收可将原始 tar 放入内容库的 `models/<SHA-256>/`，用 `scripts/build-content-packages.sh --aesthetic-scorer-only OUT CONTENT` 单独构建模型包，安装后重启 AI Worker 即可打分。对外分发仍待法务确认训练照片涉及的权利，依据与论文引用见[部署清单](../../deploy/models/qwen35-4b-aesthetic-scorer.json)。目标 NAS（i3-12100）的打分耗时与 Worker cgroup 峰值内存待实测。

## 切片与顺序

按顺序实现，每个步骤一个提交；开发期只在开发机验证，部署到 Experimental NAS 另行确认。

| 步骤 | 对应切片 | 内容 | 完成标准 |
|---|---|---|---|
| 1 | 8 | Go 侧：协议客户端、Fake Provider、`embedding` 任务、向量存储、空闲与资源门控、AI 状态接口 | 已实现：用 Fake Provider 跑通“上传 → 缩略图 → 向量”；没有 Worker 时冒烟与系统测试照常通过。Host Agent 活动信号移入后续步骤 |
| 2 | 8 | Python Worker：UDS 服务、fd 接收、MediaPipe Provider 与 Fake Provider、单元测试 | 已实现：[`ai/`](../../ai/anas_ai/worker.py) 的协议测试只需标准库并纳入 `make check`；Go 客户端与 Python Worker 的互通测试通过；真实模型测试在 Debian 13 容器中通过 |
| 3 | 9 | 已完成：70 视觉 token、512 px 输入与 v1 阈值冻结（73 个可展示标签），见[校准报告](../research/photo-ai-label-calibration.md)：使用传到开发机预留位置的全模态 740M 包，验证 Debian 13 x86 上的加载、文本与图片编码、内存与延迟、30 分钟连续运行；比较提示词模板与 512/1024 px 输入；建立标签词表 v1 与阈值校准脚本 | 形成实测报告并冻结模型 revision、维度、提示词与初始阈值；未通过则按研究文档改选候选 |
| 4 | 12 | 已实现：搜索接口、内存索引、搜索范围与桌面搜索框；Worker 增加 `embed_query` | 单元与泄漏测试覆盖范围、查看授权、回收站与分页；本地 systemd 环境中真实模型的中文搜索结果见[校准报告](../research/photo-ai-label-calibration.md#端到端搜索本地-systemd-环境) |
| 5 | 12 | 已实现，2026-10-10 起暂停（#66）：标签向量缓存、读取时计算 AI 标签、查看器标签展示、按标签筛选 | 标签只在阈值之上、且只对校准模型出现；隐藏随步骤 6 |
| 6 | 11 | 已实现：相册分组、加入与移出、用户标签、AI 纠错（手工位置随地点功能实现） | 用户元数据独立于派生数据，清除派生不影响它们；纠错与标签经重启和复制保留 |
| 7 | 10 | 已实现：内置打包（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）：锁定哈希的离线 wheel、按内容存放的模型与运行环境、`a-nas-ai` 身份与按需启动的套接字单元、安装器、经 SSH 暂存（整理进度与隐私说明已随 [#61](https://github.com/zhongwater123/A-NAS/pull/61) 的相册界面实现） | 系统测试在没有外网的环境中完成安装，Worker 经套接字按需启动并编码图片；测试证明 Worker 无法联网、无法读取数据卷；升级与回滚不重复复制模型；部署到 Experimental NAS 另行确认 |

## 待确认

1. **许可**：模型随系统再分发，对外发行前核对权重许可证（Apache-2.0 还是 Gemma 许可），并随系统提供许可证与 NOTICE。
2. **最低硬件**：运行 Worker 的内存与 CPU 下限，在 Experimental NAS 上实测后确定。
3. **小物体**：已决定由物体检测解决（[物体检测方案](photo-objects.md)），分块向量不进入产品。

## 风险

- MediaPipe 1.1.0 只发布一个通用的 `py3-none-manylinux_2_28_x86_64` wheel，已在 Debian 13 的 Python 3.13 上验证可用；升级 MediaPipe 前须重跑真实模型测试。
- 全模态包加载后约 1.2–1.4 GiB，峰值 1.4–1.8 GiB（取决于视觉 token）；检测器加载时再多约 400 MiB（实机上向量模型峰值只有 815 MB）。2026-10-28 起各模型的内存写在清单的 `memoryBytes` 中，由门控在加载前核对（[资源策略](photo-objects.md#资源策略)）。
- 官方只公布了 Arm Linux 的性能；i3-12100 的单张耗时、按需启动的加载时间与前台影响都需实测，结论只来自开发机时须注明，实机数字在部署后补充。
- 512 px 输入可能损失细节；以实测决定是否增加 AI 输入派生。
- 开发期不碰 NAS，因此规模、温度与真实前台干扰的结论只能在之后部署时得出。

## 关联

- 技术设计：[相册技术设计](photo-library.md)（切片 8–12）、[物体检测与混合检索](photo-objects.md)（切片 18）
- 规格：[相册](../specs/photo-library.md)
- 研究：[本地照片 AI 模型与 Runtime](../research/photo-ai-model-runtime-selection.md)
- ADR：[0006 受管图库](../adr/0006-use-a-managed-photo-library.md)、[0011 相册服务身份与 Catalog 授权](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[0016 AI 随系统内置安装](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- 代码：[`internal/photos/jobs.go`](../../internal/photos/jobs.go)（复用的持久任务表）
