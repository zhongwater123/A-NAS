# 相册本地 AI（M2）实施方案

状态：draft（方案；尚未实现，开发期只在开发机验证，不部署到 Experimental NAS）
更新时间：2026-10-09

本文把[相册技术设计](photo-library.md)中 M2 的切片 8–12 细化为可实现的方案：用 Google EmbeddingGemma 2 的全模态 740M 官方包为照片生成向量，在此基础上提供 AI 标签与中文语义搜索。产品行为以[相册规格](../specs/photo-library.md)为准，模型与 Runtime 的候选证据见[本地照片 AI 研究](../research/photo-ai-model-runtime-selection.md)。

## 目标与范围

M2 交付后，成员可以：

- 在相册里用中文搜索“海边的猫”“车库里的电动车”，结果只含自己有权查看的照片；
- 在查看器里看到少量高置信 AI 标签，并能隐藏错误标签、添加自己的标签；
- 在 AI 组件未安装、停止、崩溃或积压时照常使用 M1 的全部功能。

不在 M2 内：人脸与人物库（M3 切片 13）、按需中文描述与视频内容分析。不做 OCR（2026-10-09 决定）。

## 已确认的决定

- **模型包**：使用全模态 740M 官方包（约 485 MB，2026-10-09 决定）。相册只调用文本与图片编码；包内的音频编码器保留，不为相册单独裁剪。
- **OCR**：不做（2026-10-09 决定）。

## 已核实的 Runtime 事实（2026-10-08）

- EmbeddingGemma 2 由文本 270M、视觉 170M 与音频 300M 三个编码器组成，可只加载需要的部分；统一输出 768 维向量，支持截断到 512/256/128 维（截断后需重新归一化，128 维对多模态检索损失明显）。[模型卡](https://huggingface.co/google/embeddinggemma-2)
- 官方 LiteRT 包：文本 270M 165 MB、文本+视觉 440M 388 MB、全模态 740M 485 MB。440M 包的文本权重 INT4、视觉权重 INT8；官方在 Linux Arm CPU 上测得文本 105 ms、文本+视觉 825 ms、内存约 647 MB，没有 x86 数据。[LiteRT 包](https://huggingface.co/litert-community/embeddinggemma-2-text-vision-440m-litert-lm)
- MediaPipe 的 Universal Embedder 直接加载上述 `.litertlm` 文件，Python 接口提供 `embed_text` 与 `embed_image`，可选 L2 归一化、`visionTokensPerImage`、`maxInputLength` 与 CPU 后端；MediaPipe Python 包支持桌面 Linux 与 Python 3.9+。官方文档没有单列 Linux x86_64 的结果，需实测。[Universal Embedder](https://developers.google.com/edge/mediapipe/solutions/retrieval/universal_embedder/index)、[Python 指南](https://developers.google.com/edge/mediapipe/solutions/retrieval/universal_embedder/python)
- LiteRT-LM 自己的 Python API 目前只面向生成式对话，没有公开的向量接口，因此 Worker 使用 MediaPipe 而不是直接调用 LiteRT-LM。[LiteRT-LM Python](https://developers.google.com/edge/litert-lm/python)
- 模型卡元数据标注 Apache-2.0，但许可证链接与 MediaPipe 页面写的是 Gemma 许可。发行前必须核对权重文件实际附带的许可证；开发机试验不受影响。

## 模型文件位置

模型文件不进仓库，也不进发行包；位置与校验值固定在清单 [`deploy/models/embeddinggemma-2-740m.json`](../../deploy/models/embeddinggemma-2-740m.json) 中：

- **文件**：官方仓库 `litert-community/embeddinggemma-2-740m-litert-lm`（revision `24d962e9…`）中的通用 CPU/GPU 文件 `embeddinggemma-2-740m.litertlm`，484,622,336 字节，SHA-256 `e7a8a2204b91e0f96e92960e84a09a89212e1633dcb7575a9bf3378b4df77f4c`。同一仓库中带厂商后缀的文件（Qualcomm、MediaTek、Tensor、Intel PTL）是特定 NPU 的编译版本，不适用于 i3-12100。
- **Experimental NAS 暂存**：`/home/anas-dev/apps/a-nas/models/embeddinggemma-2-740m/embeddinggemma-2-740m.litertlm`，与发行制品一样由开发机以 `anas-dev` 经 SSH 传入（目录已于 2026-10-09 创建）：`scp embeddinggemma-2-740m.litertlm anas-dev@<NAS>:apps/a-nas/models/embeddinggemma-2-740m/`，传完在 NAS 上用 `sha256sum` 核对。暂存只放文件，不启用任何 AI 组件。
- **Experimental NAS 安装（之后部署 AI 时）**：`/opt/a-nas/models/embeddinggemma-2-740m/embeddinggemma-2-740m.litertlm`，`root:root 0644`。root 执行的安装步骤先按清单核对暂存文件的大小与 SHA-256，再复制到这里；`anas-dev` 无权写入该目录。
- **开发机**：`D:\A-NAS-models\embeddinggemma-2-740m\embeddinggemma-2-740m.litertlm`（WSL 中为 `/mnt/d/A-NAS-models/embeddinggemma-2-740m/`），在仓库之外，以只读方式挂载进测试容器；使用真实模型的可选测试从 `ANAS_AI_MODEL` 读取路径。开发期的实测副本从 NAS 暂存目录复制过来。
- Worker 加载前核对文件大小与 SHA-256，不符时拒绝加载并报告 AI 不可用。

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
- 以专用身份 `a-nas-ai` 运行；systemd 约束沿用技术设计：`PrivateNetwork=yes`、`InaccessiblePaths=/srv/a-nas`、`MemoryMax`（起点 2G，按实测收紧）、`CPUQuota=200%`、低 `CPUWeight`/`IOWeight`。
- 按需启动：`anas-ai.socket` 监听 `/run/a-nas-ai/ai.sock`（组 `a-nas-photos`、`0660`），第一次请求时由 systemd 拉起 Worker，空闲 10 分钟后自行退出并释放模型内存。搜索因此可能承担一次模型加载时间，由切片 9 实测决定是否改为常驻文本编码器。
- 模型文件的位置与校验见“模型文件位置”；AI 组件可以不安装，此时套接字不存在，相册显示“智能处理不可用”。

### 协议

客户端见 [`internal/aiworker`](../../internal/aiworker/client.go)，测试用的假 Worker 见 [`aiworkertest`](../../internal/aiworker/aiworkertest/aiworkertest.go)。

- 相册服务是客户端，Worker 是串行服务端（并发 1）。每次调用新建一个连接，发一帧请求、读一帧响应；帧为 4 字节大端长度加 JSON，上限 1 MiB。图片随请求以 `SCM_RIGHTS` 传入只读文件描述符（Go 侧用 `golang.org/x/sys/unix`，Python 侧用 `socket.recv_fds`），Worker 不接受路径。
- 请求 `{"op": ...}`：`info` 返回 `model`（含权重哈希与预处理版本）与 `dimensions`；`embed_image` 返回 `vector`（小端 float32，JSON 中为 base64）。`embed_text` 随搜索步骤加入。
- 失败响应为 `{"ok": false, "error": {"code", "message"}}`：`invalid_input` 表示这张图片无法处理，任务记为失败；`unavailable` 表示 Worker 在运行但不能服务（例如模型文件缺失或校验不符），任务等待。
- 连不上 Worker 时任务等待，不消耗尝试次数；Worker 收到图片后才失败或退出（崩溃、超时、乱码响应）则消耗一次尝试，三次后记为失败，避免反复拖垮 Worker 的图片无限重试。每次调用默认最长 2 分钟，覆盖按需启动后的模型加载。

### 输入图片

AI 输入使用已有的缩略图派生 `thumbnail/v1`：长边 512 px、已按 EXIF 方向转正、透明区域合成白底，大小有界。这样 Worker 不必解码任意尺寸的原图，也不会因超大图片耗尽内存。切片 9 用同一测试集比较 512 px 与 1024 px 输入的质量，差距明显时再增加专用的 AI 输入派生。

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

### 存储（Catalog migration 4）

- `embeddings(object_id, model_id, vector, created_at)`：每个对象每个模型一行，768 维 float32 约 3 KB；20,000 张约 60 MB。
- `label_vectors(label_set, label_id, model_id, vector)`：标签词表的文本向量缓存。
- `ai_tags(object_id, label_set, label_id, score)`：由向量与标签向量在相册服务内计算（Go，无需模型），词表或阈值更新时可整体重算。
- 切片 11 的用户元数据以照片资产为键：`user_tags(asset_id, name, created_by, created_at)`、`ai_tag_corrections(asset_id, label_id, verdict, created_by, created_at)`。重算 AI 标签永远不改动它们。

### AI 标签

- 版本化的中文标签词表随代码发布（`internal/photos/labels/`）：每项有稳定 ID、显示名、同义词、分类与提示词模板，覆盖规格点名的“猫”“电动车”“3D 打印机”等常见物体、动物、场景、食物、文档与活动。
- 标签分数是照片向量与标签文本向量的余弦相似度。每个标签有自己的阈值；只展示超过阈值、且与次优标签拉开差距的最多 5 个，并标明来源为 AI。
- 阈值由切片 9 用公开中文数据集校准（COCO-CN、Flickr30K-CN 及带中文类别名的 ImageNet，只在开发机使用，不随发行包分发）。校准完成前所有阈值取保守高值，宁可少标不错标。
- 用户隐藏的 AI 标签写入纠错表，之后任何重算都不再展示；用户标签优先显示。

### 语义搜索

- `GET /api/v1/photos/search?q=&cursor=&limit=`：相册服务请 Worker 编码查询文本，再对调用方可访问图库中的照片做精确余弦扫描（向量常驻内存，20,000 张约 60 MB），与文件名、用户标签和 AI 标签的文字匹配合并排序后分页返回。
- 范围与 Policy 和时间线一致：自己的私有图库与共享图库；管理员只在查看授权有效期间加入对应成员的私有图库。候选生成前限定图库，返回前再逐条校验可见性。
- AI 不可用时，搜索退化为文件名与标签的文字匹配，并提示“语义搜索暂不可用”。

### 失败、版本与重建

- AI 处理状态与照片状态分离：`pending → running → ready / failed`，失败保存稳定错误类别。
- 每条向量记录模型 ID（含权重哈希与预处理版本）。更换模型或预处理后，旧结果标记为 `stale`，后台重建；重建期间旧向量是否继续参与搜索是规格中的待定项，建议继续参与直到新向量就绪。
- 删除最后一个引用时，向量与 AI 标签随对象删除；用户元数据随照片资产删除。

## 切片与顺序

每步一个 PR，按顺序合并；全部只在开发机验证。

| 步骤 | 对应切片 | 内容 | 完成标准 |
|---|---|---|---|
| 1 | 8 | Go 侧：协议客户端、Fake Provider、`embedding` 任务、向量存储、空闲与资源门控、AI 状态接口 | 已实现：用 Fake Provider 跑通“上传 → 缩略图 → 向量”；没有 Worker 时冒烟与系统测试照常通过。Host Agent 活动信号移入后续步骤 |
| 2 | 8 | Python Worker：UDS 服务、fd 接收、MediaPipe Provider 与 Fake Provider、单元测试 | Worker 在 Debian 13 容器中以 Fake Provider 通过协议测试 |
| 3 | 9 | 开发机模型实测：使用你传到开发机预留位置的全模态 740M 包，验证 Debian 13 x86 上的加载、文本与图片编码、内存与延迟、30 分钟连续运行；比较提示词模板与 512/1024 px 输入；建立标签词表 v1 与阈值校准脚本 | 形成实测报告并冻结模型 revision、维度、提示词与初始阈值；未通过则按研究文档改选候选 |
| 4 | 12 | 搜索接口、内存索引、Policy 过滤与桌面搜索框 | 本地 systemd 环境中用真实模型完成中文搜索，泄漏测试覆盖搜索结果 |
| 5 | 12 | AI 标签计算、查看器标签展示、按标签筛选 | 标签只在阈值之上出现；隐藏后重算不复现 |
| 6 | 11 | 用户标签、AI 纠错与相册实体 | 用户元数据独立于派生数据，清除派生不影响它们 |
| 7 | 10 | 打包：离线 wheel 与哈希、模型清单、`anas-ai` 单元与安装器、系统测试 | 系统测试在无网络下安装并运行 Worker；不部署到 NAS，等你决定 |

## 待确认

1. **许可**：发行前核对权重许可证（Apache-2.0 还是 Gemma 许可），并保存清单与 NOTICE。
2. **下载**：模型包由你从其他设备传到开发机的预留位置；步骤 3 的公开中文数据集仍需下载，体积在下载前逐个列出。
3. **标签词表**：首版约 200–300 个常用标签；如有必须识别的类别请补充。

## 风险

- MediaPipe 的 Linux x86 wheel 与 Debian 13 的 Python 版本是否匹配尚未验证；不匹配时改用 Debian 12 基础的独立 Python 运行环境，或改用研究文档中的备选 Runtime。
- 全模态包比 440M 包多 300M 的音频编码器；若 MediaPipe 总是整体加载，常驻内存与加载时间都会增加，`MemoryMax` 与按需启动策略以实测为准。
- 官方只公布了 Arm Linux 的性能；i3-12100 的单张耗时、按需启动的加载时间与前台影响都需实测，结论只来自开发机时须注明，实机数字在部署后补充。
- 512 px 输入可能损失细节；以实测决定是否增加 AI 输入派生。
- 开发期不碰 NAS，因此规模、温度与真实前台干扰的结论只能在之后部署时得出。

## 关联

- 技术设计：[相册技术设计](photo-library.md)（切片 8–12）
- 规格：[相册](../specs/photo-library.md)
- 研究：[本地照片 AI 模型与 Runtime](../research/photo-ai-model-runtime-selection.md)
- ADR：[0006 受管图库](../adr/0006-use-a-managed-photo-library.md)、[0011 相册服务身份与 Catalog 授权](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)
- 代码：[`internal/photos/jobs.go`](../../internal/photos/jobs.go)（复用的持久任务表）
