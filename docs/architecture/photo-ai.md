# 相册本地 AI（M2）实施方案

状态：进行中（步骤 1–6 已实现并合并；步骤 7 按 [ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md) 改为随系统内置安装，正在实现。Experimental NAS 当前的版本不含 AI Worker，部署包含 AI 的版本、在 NAS 上运行模型需另行确认）
更新时间：2026-10-10

本文把[相册技术设计](photo-library.md)中 M2 的切片 8–12 细化为可实现的方案：用 Google EmbeddingGemma 2 的全模态 740M 官方包为照片生成向量，在此基础上提供 AI 标签与中文语义搜索。产品行为以[相册规格](../specs/photo-library.md)为准，模型与 Runtime 的候选证据见[本地照片 AI 研究](../research/photo-ai-model-runtime-selection.md)。

## 目标与范围

M2 交付后，成员可以：

- 在相册里用中文搜索“海边的猫”“车库里的电动车”，结果只含自己有权查看的照片；
- 在查看器里看到少量高置信 AI 标签，并能隐藏错误标签、添加自己的标签；
- 不下载、不安装、不配置任何 AI 组件：AI 随系统安装并默认开启；
- 在 AI Worker 启动中、崩溃、积压或硬件不足时照常使用 M1 的全部功能。

不在 M2 内：人脸与人物库（M3 切片 13）、按需中文描述与视频内容分析。不做 OCR（2026-10-09 决定）。

## 已确认的决定

- **模型包**：使用全模态 740M 官方包（约 485 MB，2026-10-09 决定）。相册只调用文本与图片编码；包内的音频编码器保留，不为相册单独裁剪。
- **OCR**：不做（2026-10-09 决定）。
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
- 依赖：`import mediapipe` 会加载 OpenCV 的绘图工具，图形界面版 OpenCV 需要 X 库，因此换用同版本的 `opencv-contrib-python-headless`；MediaPipe 的 C 库还链接 `libEGL.so.1` 与 `libGLESv2.so.2`，安装 Debian 的 `libegl1`、`libgles2`（只是分发库，不带 GPU 驱动）即可。完整依赖的虚拟环境约 509 MB，打包时（步骤 7）再精简。
- 按 70 token 估算，2 万张照片约需 3.4 小时的空闲 CPU 时间；实机与温度影响待部署后验证。

## 模型与运行环境的交付

按 [ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)，模型与 Python 运行环境随发行制品送达设备，按内容各存一份并由 release 引用；设备在安装、升级和运行时都不联网获取。模型文件不进仓库，来源与校验值固定在清单 [`deploy/models/embeddinggemma-2-740m.json`](../../deploy/models/embeddinggemma-2-740m.json) 中。

- **模型文件**：官方仓库 `litert-community/embeddinggemma-2-740m-litert-lm`（revision `24d962e9…`）中的通用 CPU/GPU 文件 `embeddinggemma-2-740m.litertlm`，484,622,336 字节，SHA-256 `e7a8a2204b91e0f96e92960e84a09a89212e1633dcb7575a9bf3378b4df77f4c`。同一仓库中带厂商后缀的文件（Qualcomm、MediaTek、Tensor、Intel PTL）是特定 NPU 的编译版本，不适用于 i3-12100。
- **运行环境**：MediaPipe 及其依赖（含 `opencv-contrib-python-headless`）以锁定版本与哈希的 wheel 列表固定，运行环境 ID 取该列表 SHA-256 的前 16 位十六进制。wheel 只在开发机上按列表获取并核对哈希；完整依赖约 509 MB，打包时精简。
- **开发机**：模型位于 `D:\A-NAS-models\embeddinggemma-2-740m\embeddinggemma-2-740m.litertlm`（WSL 中为 `/mnt/d/A-NAS-models/embeddinggemma-2-740m/`），在仓库之外，以只读方式挂载进测试容器；使用真实模型的可选测试从 `ANAS_AI_MODEL` 读取路径。
- **暂存**：开发机以 `anas-dev` 经 SSH 把 release 与它引用的模型、wheel 一起传到 NAS 暂存区，模型按 SHA-256 存放在 `apps/a-nas/models/<SHA-256>/`；暂存区已有的文件不再上传。
- **安装**：root 执行的安装器按 release 中的清单核对暂存的模型与 wheel，复制（不链接，暂存账号因此无法改动已校验的内容）到 `/opt/a-nas/models/<SHA-256>/<文件名>`（`root:root 0644`），并用系统 Python 与暂存的 wheel 离线创建 `/opt/a-nas/ai-runtimes/<运行环境 ID>/`，不访问 PyPI；已经安装的模型与运行环境直接复用。release 的 `ai/` 目录包含 Worker 代码和它所用模型、运行环境的清单。Experimental NAS 已有 `python3` 3.13.5、`libegl1` 与 `libgles2`，没有 `python3-venv`，因此创建运行环境不能依赖 `ensurepip`。
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

- 一个小型 Python 进程，内部只有一个 Provider：MediaPipe Universal Embedder 加载全模态 740M `.litertlm`。全模态包会占用多少内存、能否只初始化文本与图片编码器，官方没有数字，由步骤 3 实测。测试用的 Fake Provider 实现同一协议，返回由输入哈希得到的确定性向量。
- 以专用身份 `a-nas-ai` 运行；systemd 约束沿用技术设计：`PrivateNetwork=yes`、`InaccessiblePaths=/srv/a-nas`、`MemoryMax`（起点 2G，按实测收紧）、`CPUQuota=200%`、低 `CPUWeight`/`IOWeight`；另以 `RestrictAddressFamilies=AF_UNIX` 只允许 Unix 套接字。系统测试在 Worker 的沙箱中尝试建立 IPv4/IPv6 连接与读取数据卷，二者都必须失败；界面的隐私说明以此为依据（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）。
- 按需启动：`anas-ai.socket` 监听 `/run/a-nas-ai/ai.sock`（组 `a-nas-photos`、`0660`），第一次请求时由 systemd 拉起 Worker，空闲 10 分钟后自行退出并释放模型内存。搜索因此可能承担一次模型加载时间，由切片 9 实测决定是否改为常驻文本编码器。
- 模型与运行环境的来源、位置与校验见“模型与运行环境的交付”。安装器随系统启用套接字，没有“未安装”状态；Worker 启动中、崩溃或模型校验失败时相册照常可用，搜索退回按名称与用户标签匹配。设备低于最低硬件（待实机测量）时不启动 Worker，界面说明“这台设备不支持智能识别”。

### 协议

客户端见 [`internal/aiworker`](../../internal/aiworker/client.go)，测试用的假 Worker 见 [`aiworkertest`](../../internal/aiworker/aiworkertest/aiworkertest.go)。

- 相册服务是客户端，Worker 是串行服务端（并发 1）。每次调用新建一个连接，发一帧请求、读一帧响应；帧为 4 字节大端长度加 JSON，上限 1 MiB。图片随请求以 `SCM_RIGHTS` 传入只读文件描述符（Go 侧用 `golang.org/x/sys/unix`，Python 侧用 `socket.recv_fds`），Worker 不接受路径。
- 请求 `{"op": ...}`：`info` 返回 `model`（含权重哈希与预处理版本）与 `dimensions`；`embed_image` 返回 `vector`（小端 float32，JSON 中为 base64）；`embed_query` 把 `text`（至多 1,000 字符）当作搜索查询编码，由 Worker 加上模型自己的检索前缀（EmbeddingGemma 为 `task: search result | query: …`），向量与图片向量可直接比较。前缀属于模型的使用约定，随 Worker 与模型一起更换。
- 失败响应为 `{"ok": false, "error": {"code", "message"}}`：`invalid_input` 表示这张图片无法处理，任务记为失败；`unavailable` 表示 Worker 在运行但不能服务（例如模型文件缺失或校验不符），任务等待。
- 连不上 Worker 时任务等待，不消耗尝试次数；Worker 收到图片后才失败或退出（崩溃、超时、乱码响应）则消耗一次尝试，三次后记为失败，避免反复拖垮 Worker 的图片无限重试。每次调用默认最长 2 分钟，覆盖按需启动后的模型加载。

### 输入图片

AI 输入使用已有的缩略图派生 `thumbnail/v1`：长边 512 px、已按 EXIF 方向转正、透明区域合成白底，大小有界。这样 Worker 不必解码任意尺寸的原图，也不会因超大图片耗尽内存。步骤 3 比较过 1024 px 输入：同一图片的两种向量余弦相似度平均 0.984，暂不增加专用的 AI 输入派生（[校准报告](../research/photo-ai-label-calibration.md#视觉-token-与输入尺寸)）。

### 任务与调度

- 复用 M1 的持久任务表：缩略图完成后，在同一事务中为该原图对象排入 `embedding/v1` 任务（`v1` 是处理流程版本，模型 ID 随向量保存）。向量只依赖原图字节，以对象为键，副本与重复照片共用。缩略图与 AI 任务由两个循环分别领取。
- 缩略图任务照旧立即执行；AI 任务只在门控允许时领取：
  - 最近 5 分钟没有前台活动；
  - 系统压力低：CPU、I/O、内存 PSI（`/proc/pressure/*` 的 `avg10`）都低于阈值；
  - CPU 温度低于上限。
- 前台活动与温度由 Host Agent 提供：它已经知道 Web 文件操作（文件代理）、相册会话查询与磁盘、网卡吞吐（SMB 传输体现在这里），在相册会话套接字上增加只读的 `GET /v1/activity`。相册服务运行在 `PrivateNetwork` 中，看不到宿主机网卡，因此不自行采样。
- 已实现部分（[`photoservice/gate.go`](../../internal/photoservice/gate.go)）：相册服务自己的请求计入前台活动，但读取 AI 状态不计入，窗口可以一直轮询；CPU、I/O、内存压力经 `prometheus/procfs` 读取，内核没有压力信息时只看活动。Host Agent 的活动信号与温度尚未实现，在此之前 Web 文件与 SMB 只通过系统压力影响门控。
- 门控关闭时只停止领取新任务，进行中的单张推理允许完成；关闭期间至多每分钟（不长于空闲期）复查一次。
- 更换模型后，第一次见到新模型 ID 时把旧模型的向量全部重新排队；旧向量在新向量写入前继续保留。

### 存储（Catalog migration 4–6）

- `embeddings(object_id, derivation, model, vector, created_at)`：每个对象一行，记录产生它的模型，768 维 float32 约 3 KB；20,000 张约 60 MB。
- `query_vectors(model, text, vector, created_at)`：Worker 为标签文本编码的向量，按模型与文本缓存，重启后无需 Worker 即可显示标签。
- 不保存 AI 标签：标签分数在读取时由图片向量与标签向量算出，阈值或词表随新版本更新后立即生效，无需重算任务。
- 用户元数据（migration 6，[`albums.go`](../../internal/photos/albums.go)、[`usertags.go`](../../internal/photos/usertags.go)）：`albums(id, library_id, name, created_by, created_at)` 与 `album_assets(album_id, asset_id, added_by, added_at)` 记录相册分组；`user_tags(asset_id, name, …)` 与 `ai_tag_corrections(asset_id, label_id, verdict, …)` 以照片资产为键。照片永久删除时随之删除；AI 标签的计算与派生数据的清除永远不改动它们，复制照片时一并复制标签与纠错。

### AI 标签

已实现于 [`photos/ailabels.go`](../../internal/photos/ailabels.go)，词表与校准随程序发布（[`photos/labels`](../../internal/photos/labels/labels.go)）。

- 词表 v1（[`v1.json`](../../internal/photos/labels/v1.json)）共 330 个中文标签，每项有稳定 ID、显示名、同义词与分类，覆盖规格点名的“猫”“电动车”“3D 打印机”等物体、动物、场景、食物、文档与活动。
- 校准（[`v1.calibration.json`](../../internal/photos/labels/v1.calibration.json)）记录模型 ID、标签短语（v1 为标签名本身）与每个可展示标签的阈值；方法与结果见[标签校准报告](../research/photo-ai-label-calibration.md)。v1 有 73 个标签可展示，其余只参与搜索，宁可少标不错标。
- 标签向量：AI 后台任务在处理照片之前，逐条请 Worker 用 `embed_query` 编码可展示标签的文本，存入 `query_vectors`；全部就绪后才显示标签。
- 标签分数是照片向量与标签向量的余弦相似度。照片详情只显示达到阈值的标签，按超出阈值的幅度排序，最多 5 个，标明“本地 AI 识别”并附相似度。阈值只对校准时的模型成立：Worker 的模型 ID 与校准不符时不显示任何标签，也不编码标签文本。
- 按标签筛选：`GET /api/v1/photos/search?label=` 在搜索范围内返回显示该标签的照片，按相似度排序；查看器中点击标签即进入。
- 用户可以在查看器中隐藏错误的 AI 标签：纠错只作用于该张照片资产，按标签 ID 保存，模型升级后仍然有效，按标签筛选也不再返回它（2026-10-09 决定）。用户标签由用户添加和删除（至多 30 字），显示在 AI 标签之前；能重命名照片的人才能修改标签与纠错。
- 相册是图库内的分组（2026-10-09 决定，见[规格](../specs/photo-library.md)）：同一图库的照片加入相册不产生副本，一张照片可在多个相册中；来自另一图库的照片先复制到相册所在图库。删除相册或移出照片不删除照片，回收站中的照片暂不列出、恢复后回到相册。创建与修改权限沿用虚拟目录：能向图库添加的人可以创建相册，创建者、私有图库所有者或共享图库的管理员可以修改。

### 语义搜索

已实现于 [`photos/search.go`](../../internal/photos/search.go)，桌面相册窗口提供搜索框。

- `GET /api/v1/photos/search?q=&viewing=&cursor=&limit=`：查询 1–200 字符。相册服务请 Worker 编码查询，再对范围内未进回收站的照片做精确余弦扫描。图片向量按内容对象常驻内存（20,000 张约 60 MB）：第一次搜索时按当前模型载入，之后随新向量写入与对象清除同步更新，换模型时整体重载。
- 取舍（2026-10-10 决定，取代“只排序、不设分数下限”）：名称或用户标签包含查询（不区分大小写）的照片排在最前。之后的语义结果：
  - 查询提到已校准的标签（按名称或同义词，最长词优先，“热狗”不算“狗”）时，只保留同时显示所有这些标签的照片（达到阈值且未被隐藏），再按整句相似度排序；响应 `match` 为 `labels`，`labels` 列出所依据的标签。
  - 否则只保留相似度在最佳匹配 0.04 以内的照片，最多 20 张，`match` 为 `closest`，窗口说明这是“最接近的照片”。
  - 依据见[校准报告](../research/photo-ai-label-calibration.md#搜索结果的取舍)：只排序时 COCO 上显示的照片只有 15% 相关；按标签阈值 88% 相关、召回 53%；按最佳匹配 0.04 以内 90% 相关、召回 33%。任何不依赖逐标签校准的规则，在图库里根本没有该事物时仍会返回照片，所以后者只作“最接近”展示。
  - 语义结果最多 500 张；分页游标记录上一页最后一项的分数与 ID。
- 范围按[规格](../specs/photo-library.md)：普通搜索覆盖自己的私有图库与共享图库，不混入任何查看中的成员图库；管理员在查看模式下用 `viewing` 指定所查看的成员私有图库，经有效授权校验后加入，授权过期或无权时与不存在的图库一样返回 404。候选只从范围内的图库查询，结果来自 Catalog 当前记录。
- AI 不可用（Worker 未运行、模型不符或编码失败）时只按名称与用户标签匹配，响应的 `semantic` 为 `false`、`match` 为 `names`，窗口提示本地 AI 暂不可用。模型与校准不符时查询照常编码，但不按标签阈值过滤，按 `closest` 处理。

### 界面上的整理进度与隐私说明

- 相册侧栏的 AI 卡片（[`Sidebar.tsx`](../../web/src/photos/Sidebar.tsx)）读取 AI 状态接口（`GET /api/v1/photos/ai`，读取不计入前台活动），显示整理进度、暂停原因与无法识别的数量；AI 搜索页（[`AISearch.tsx`](../../web/src/photos/AISearch.tsx)）说明照片只在这台设备上由本地 AI 识别。界面不显示模型名称、版本或组件状态，接口返回的模型 ID 只用于诊断。
- 隐私说明以 Worker 沙箱的系统测试为前提，测试未通过的版本不得发布。
- 各图库的 AI 开关见[规格](../specs/photo-library.md)，不在步骤 7 内实现。

### 失败、版本与重建

- AI 处理状态与照片状态分离：`pending → running → ready / failed`，失败保存稳定错误类别。
- 每条向量记录模型 ID（含权重哈希与预处理版本）。更换模型或预处理后，旧结果标记为 `stale`，后台重建；重建期间旧向量是否继续参与搜索是规格中的待定项，建议继续参与直到新向量就绪。
- 删除最后一个引用时，向量与 AI 标签随对象删除；用户元数据随照片资产删除。

## 切片与顺序

按顺序实现，每个步骤一个提交；开发期只在开发机验证，部署到 Experimental NAS 另行确认。

| 步骤 | 对应切片 | 内容 | 完成标准 |
|---|---|---|---|
| 1 | 8 | Go 侧：协议客户端、Fake Provider、`embedding` 任务、向量存储、空闲与资源门控、AI 状态接口 | 已实现：用 Fake Provider 跑通“上传 → 缩略图 → 向量”；没有 Worker 时冒烟与系统测试照常通过。Host Agent 活动信号移入后续步骤 |
| 2 | 8 | Python Worker：UDS 服务、fd 接收、MediaPipe Provider 与 Fake Provider、单元测试 | 已实现：[`ai/`](../../ai/anas_ai/worker.py) 的协议测试只需标准库并纳入 `make check`；Go 客户端与 Python Worker 的互通测试通过；真实模型测试在 Debian 13 容器中通过 |
| 3 | 9 | 已完成：70 视觉 token、512 px 输入与 v1 阈值冻结（73 个可展示标签），见[校准报告](../research/photo-ai-label-calibration.md)：使用传到开发机预留位置的全模态 740M 包，验证 Debian 13 x86 上的加载、文本与图片编码、内存与延迟、30 分钟连续运行；比较提示词模板与 512/1024 px 输入；建立标签词表 v1 与阈值校准脚本 | 形成实测报告并冻结模型 revision、维度、提示词与初始阈值；未通过则按研究文档改选候选 |
| 4 | 12 | 已实现：搜索接口、内存索引、搜索范围与桌面搜索框；Worker 增加 `embed_query` | 单元与泄漏测试覆盖范围、查看授权、回收站与分页；本地 systemd 环境中真实模型的中文搜索结果见[校准报告](../research/photo-ai-label-calibration.md#端到端搜索本地-systemd-环境) |
| 5 | 12 | 已实现：标签向量缓存、读取时计算 AI 标签、查看器标签展示、按标签筛选 | 标签只在阈值之上、且只对校准模型出现；隐藏随步骤 6 |
| 6 | 11 | 已实现：相册分组、加入与移出、用户标签、AI 纠错（手工位置随地点功能实现） | 用户元数据独立于派生数据，清除派生不影响它们；纠错与标签经重启和复制保留 |
| 7 | 10 | 内置打包（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）：锁定哈希的离线 wheel、按内容存放的模型与运行环境、`a-nas-ai` 身份与按需启动的套接字单元、安装器、经 SSH 暂存（整理进度与隐私说明已随 [#61](https://github.com/zhongwater123/A-NAS/pull/61) 的相册界面实现） | 系统测试在没有外网的环境中完成安装，Worker 经套接字按需启动并编码图片；测试证明 Worker 无法联网、无法读取数据卷；升级与回滚不重复复制模型；部署到 Experimental NAS 另行确认 |

## 待确认

1. **许可**：模型随系统再分发，对外发行前核对权重许可证（Apache-2.0 还是 Gemma 许可），并随系统提供许可证与 NOTICE。
2. **最低硬件**：运行 Worker 的内存与 CPU 下限，在 Experimental NAS 上实测后确定。
3. **标签词表**：v1 共 330 个标签，其中 73 个可展示；如有必须识别的类别请补充。

## 风险

- MediaPipe 1.1.0 只发布一个通用的 `py3-none-manylinux_2_28_x86_64` wheel，已在 Debian 13 的 Python 3.13 上验证可用；升级 MediaPipe 前须重跑真实模型测试。
- 全模态包加载后约 1.2–1.4 GiB，峰值 1.4–1.8 GiB（取决于视觉 token），`MemoryMax=2G` 余量有限；打包时以长时间运行的峰值确定上限。
- 官方只公布了 Arm Linux 的性能；i3-12100 的单张耗时、按需启动的加载时间与前台影响都需实测，结论只来自开发机时须注明，实机数字在部署后补充。
- 512 px 输入可能损失细节；以实测决定是否增加 AI 输入派生。
- 开发期不碰 NAS，因此规模、温度与真实前台干扰的结论只能在之后部署时得出。

## 关联

- 技术设计：[相册技术设计](photo-library.md)（切片 8–12）
- 规格：[相册](../specs/photo-library.md)
- 研究：[本地照片 AI 模型与 Runtime](../research/photo-ai-model-runtime-selection.md)
- ADR：[0006 受管图库](../adr/0006-use-a-managed-photo-library.md)、[0011 相册服务身份与 Catalog 授权](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[0016 AI 随系统内置安装](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- 代码：[`internal/photos/jobs.go`](../../internal/photos/jobs.go)（复用的持久任务表）
