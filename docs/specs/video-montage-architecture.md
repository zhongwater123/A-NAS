# 视频剪辑：分层重构与 RK3588 迁移方案

状态：第八版已按本方案实现分层（2026-11-16，[`ai/montage_v8/`](../../ai/montage_v8/README.md)），实施步骤 1–5 完成，和第六版逐镜头一致；RK3588 适配器写好、待真机调试（步骤 6）。

## 目标

1. 把视频剪辑从实验脚本整理成分层的代码：业务规则不依赖具体模型、具体硬件或数据目录。
2. 业务层能原样运行在三种平台上：
   - 开发机：macOS，Apple Silicon；
   - x86 NAS：Debian；
   - RK3588：aarch64 Linux。
   换平台时只换适配器和配置，不改业务代码。
3. 重构不改变剪辑结果：同样的描述和规划答案，重构前后剪出同样的镜头表。

不在本方案内：
- 换模型；
- 改提示词或剪辑规则；
- 产品接口与权限（剪辑装进哪个服务、按什么身份读素材，另按[架构总览](../architecture/OVERVIEW.md)的边界决定）。

## 现状

代码分在两处：
- `ai/eval/montage_v5/`：第五到第七版的实验流水线，约 1400 行；
- `ai/montage/`：早期给助手用的库，约 2500 行；第五版以后只复用了其中的 `render`。

流水线按步骤拆文件，但模型、硬件和业务规则混在一起：

| 耦合 | 在哪里 | 迁移时的问题 |
|---|---|---|
| 视觉描述直接访问 llama-server | `tag_beats.py` 里拼 HTTP 请求、JSON Schema、重试 | 换成 NPU 推理（RKLLM）就要改业务文件 |
| 规划直接调用 `claude -p` | `cut.py` 的 `ask_claude` | 换模型或换调用方式都要改 `cut.py` |
| 硬件解码写死为 VideoToolbox | `held.py`、`quality.py` 的 `-hwaccel videotoolbox` | 只能在 macOS 上运行 |
| 编码写死为 libx264 | `montage/render.py` | RK3588 上用 CPU 编码太慢，应该用 VPU |
| 音乐分析依赖 allin1（PyTorch，先做音源分离） | `structure.py`、`music.py`（另用 librosa） | 在 ARM 板上很重，也不必每次现算 |
| 窗口向量来自 A-NAS 的视频索引器（EmbeddingGemma） | `windows.py` 读 `index.json`、`frames.npz` | 只能读，算向量的那一步不在剪辑代码里 |
| 数据路径写死为开发机目录 | `paths.py` 指向 `~/A-NAS-datasets` | 换机器要设环境变量 |
| 实验开关是环境变量 | `CUT_ORDER`、`CUT_WIDE`、`CUT_LABELS`、`CUT_TAG` 等 | 配置散落，难以组合，也难以测试 |
| 一个文件管完整个流程 | `cut.py`（656 行）：过滤目录、拼提示词、两步规划、填槽、调渲染 | 规则没法单独测试，也没法复用 |
| 脚本按文件名互相引用 | `import cut`、`import held`、`sys.path` 插入 | 不是包，不能安装，也不能被产品服务引用 |

## 分层

```text
入口层     cli/（实验命令）  ·  service/（以后接产品服务）
              │ 只依赖下面三层
编排层     pipeline/：描述 → 过滤 → 规划 → 填槽 → 渲染；每一步的结果可缓存、可复用
              │ 调用领域层；经端口调用外部能力
领域层     domain/：卡片、节拍、音乐槽、剪辑计划等数据结构，以及全部剪辑规则
              │ 只依赖标准库（数值计算允许 NumPy）
端口       ports/：领域需要的外部能力，各是一个 Protocol
              ▲ 由适配器实现
适配层     adapters/：模型、视频处理、音乐、存储，每个平台一套实现
配置       profiles/：开发机、x86 NAS、RK3588 各一份，决定用哪些适配器
```

依赖只能自上而下。适配层实现端口，领域层不认识任何适配器。

### 领域层 `domain/`

纯函数和数据类，不做任何输入输出（文件、网络、子进程）。现有代码对应如下：

| 模块 | 内容 | 现在在哪里 |
|---|---|---|
| `cards.py` | 卡片、节拍、关键帧；按画面变化切卡片的规则（阈值 `CARD`、`LOOK`、`LONG`） | `cards.py` 的 `runs`、`split`、`keyframes` |
| `quality.py` | 画质规则：模糊、抖动、遮挡的判定和人工复核名单 | `quality.py` 的判定部分、`cut.poor` |
| `music.py` | 音乐段落、强度、槽的划分（用已经算好的段落和重拍，不调用 allin1） | `music.py` 的 `slots`、`cut.slotted` |
| `catalog.py` | 交给规划的卡片目录，以及各种过滤（横拍、精彩度标注、报幕） | `cut.usable_cards`、`card_mark`、`upright` |
| `prompts.py` | 两步规划和描述用的提示词模板、输出 Schema（作为数据） | `cut.py`、`tag_beats.py` 里的字符串 |
| `plan.py` | 两步规划结果的数据结构和校验 | `cut.pick`、`cut.shots` 的解析部分 |
| `fill.py` | 把节拍填进音乐槽：取几秒、放慢、不重复、连续上限 | `cut.cut_to_music` 的计算部分、`breaks_run` |
| `timeline.py` | 成片时间线（每个镜头取自哪段原片，放在哪个槽），交给渲染器 | `cut_to_music` 输出的 `clips` |

以后加的规则也放在这一层，比如“精彩度最高的场景最多连续 3 次，其他场景最多 2 次”。

### 端口 `ports/`

| 端口 | 方法（示意） | 说明 |
|---|---|---|
| `FrameSource` | `frame(video, t) -> 图片`、`frames(video, ts)` | 取关键帧，可以用硬件解码 |
| `MotionProbe` | `measure(video, start, end) -> 清晰度、抖动等` | 画质测量用到的帧序列 |
| `WindowIndex` | `windows(set) -> 窗口与向量` | 现在读 A-NAS 视频索引器的结果 |
| `Describer` | `describe(frames, prompt, schema) -> dict` | 视觉模型：本机 4B、RKLLM 或云端 |
| `Planner` | `ask(system, user, schema) -> dict、用量` | 规划模型：Claude 或本地大模型 |
| `MusicAnalyzer` | `analyze(song) -> 段落、重拍、响度` | allin1，或者读预先算好的 JSON |
| `Renderer` | `render(timeline, song) -> 成片` | FFmpeg，编码器按平台选择 |
| `Store` | 读写卡片、描述、规划、成片 | 现在是数据目录里的 JSON 文件 |

`Describer` 和 `Planner` 只约定“给定输入和 Schema，返回符合 Schema 的 JSON”。重试、重复惩罚、调用开销统计都放在适配器里。

### 适配层 `adapters/`

| 端口 | 开发机（macOS） | x86 NAS | RK3588 |
|---|---|---|---|
| `FrameSource`、`MotionProbe` | FFmpeg + VideoToolbox | FFmpeg（CPU，或 VAAPI） | FFmpeg + rkmpp 硬件解码 |
| `Describer` | llama-server（Metal） | llama-server（CPU 或 GPU） | 待测：RKLLM（NPU），或 llama.cpp（CPU），或交给别的设备 |
| `Planner` | `claude -p` | Claude API（按 [#341](https://github.com/zhongwater123/A-NAS/issues/341) 声明外部调用） | 同左 |
| `MusicAnalyzer` | allin1 | 读预先算好的 JSON | 读预先算好的 JSON |
| `Renderer` | FFmpeg + libx264 或 VideoToolbox | FFmpeg + libx264 | FFmpeg + rkmpp 硬件编码 |
| `Store` | 数据目录 | 服务自己的数据卷 | 同左 |

- 适配器按能力命名，比如 `llamacpp_http.py`、`rkllm.py`、`ffmpeg.py`（编码器、解码器作为参数），不按平台命名。
- `ffmpeg.py` 只有一份，平台差异只体现在配置里的 `-hwaccel` 和编码器参数上。

### 编排层 `pipeline/`

把现在 `cut.py` 的 `main` 和各步骤串起来：
- 每一步读上一步的结果，写自己的结果；结果已经存在就跳过。这就是现在 `reused` 的做法。
- 实验开关改成一个配置对象（排序方式、只用横拍、精彩度标注、标签名），取代散落的环境变量。命令行仍可以覆盖配置。

### 配置 `profiles/`

每个平台一个文件，写明用哪些适配器、模型地址、编码参数、并发数，例如 `mac-dev.toml`、`x86-nas.toml`、`rk3588.toml`。
- 实验用的数据目录也写进配置，取代 `paths.py` 里写死的路径。

## RK3588 迁移要点

RK3588 是 aarch64，有 8 个 CPU 核（4×A76 + 4×A55）、NPU、Mali GPU，以及硬件编解码单元（VPU）。各部分的处理：

1. **业务层**：领域层和编排层是纯 Python，只依赖标准库和 NumPy，在 aarch64 上可以直接运行。用下面“验证”一节的 arm64 容器保证这一点。
2. **视频**：
   - 解码和编码交给 VPU，用支持 Rockchip MPP 的 FFmpeg（社区的 ffmpeg-rockchip 分支提供 `rkmpp` 解码器和 `h264_rkmpp` 等编码器）；
   - 要先在板子上确认我们用到的滤镜（翻转、缩放、变速、拼接）能和硬件帧配合，或者需要转回内存处理。
3. **视觉描述**，这是最大的不确定点。三条路都要实测速度和质量：
   - RKLLM 在 NPU 上跑多模态小模型：要确认它支持的模型里有没有能替代 Qwen3.5-4B 的，景别和描述的质量要用已有的 115 个人工核对过景别的节拍复测；
   - llama.cpp 在 CPU 上跑现在的 4B：在开发机上一张卡片约 12 秒，在 RK3588 的 CPU 上会慢得多；
   - 板子上不描述，交给局域网里别的设备或云端，只是 `Describer` 的另一个适配器。
4. **音乐分析**：allin1 要 PyTorch 和音源分离，不适合放在板子上现算。音乐是有限的曲库，可以预先算好段落和重拍，随曲库一起发布；用户自带音乐的情况以后再定。
5. **窗口向量**：剪辑只读向量，算向量的是 A-NAS 的视频索引器。它在 RK3588 上怎么跑，跟着视频索引的方案走，不在本方案内。
6. **规划**：现在用云端 Claude，与平台无关；只是要把 `claude -p` 换成 API 适配器，因为板子上没有命令行客户端。
7. **内存**：RK3588 板子常见 4–32 GB 内存，各种型号都有。描述模型、FFmpeg 和产品服务要同时放得下，按目标板子的配置实测。

## 实施步骤

每一步都可以单独合并，前一步合并后剪辑结果不变。

2026-11-16 第八版：
- **已完成**：步骤 1–5 一次做完，放在新包 `ai/montage_v8/` 里，没有改第六版的代码：
  - 黄金测试是 `python3 -m montage_v8 golden`；
  - arm64 检查是在 Colima 的 aarch64 Linux 容器里跑 `tests/test_montage_v8.py`；
  - 领域层的引用限制由 Ruff TID251 守住。
- **待做**：步骤 6 等板子到了实测，步骤见 [`ai/montage_v8/README.md`](../../ai/montage_v8/README.md#板子到了之后rk3588)；步骤 7（合并两套代码）以后再做。

1. **固定基线**：用现有的规划结果（`pick-*.json`、`shots-*.json`）和描述做“黄金测试”：重跑填槽，镜头表和现在完全一致。渲染比对时长和镜头切点，不比对像素。
2. **抽出领域层**：
   - 把 `cut.py` 里的规则拆进 `domain/`，`cut.py` 只剩调用；
   - 测试改成直接测这些纯函数（扩充现有的 `tests.test_montage_v5`）。
3. **加端口和适配器**：
   - 把 llama-server、`claude -p`、FFmpeg、allin1 的调用移进 `adapters/`；
   - 开发机的行为不变，黄金测试仍然通过。
4. **配置取代环境变量**：加上 `mac-dev` 配置，环境变量作为兼容入口保留一段时间。
5. **arm64 检查**：
   - 在 linux/arm64 容器里跑领域层和编排层的测试，用假的适配器；
   - 开发机是 Apple Silicon，Colima 的虚拟机原生就是 arm64，不需要模拟；
   - 加进 `make check`。
6. **RK3588 适配器**：
   - 在真机上实测 rkmpp 解码和编码、描述模型的三条路；
   - 写 `rk3588` 配置，在板子上跑完一组素材（品杰），出片并对比。
7. **合并两套代码**：`ai/montage/` 里助手还在用的部分（`render`）并入 `adapters/`；其余的确认没人用后删除。

## 怎么保证分层不被破坏

- 领域层禁止引用 `subprocess`、`requests`、`os`、`PIL` 等做输入输出的模块。用 Ruff 已有的 `flake8-tidy-imports` 规则（TID251 banned-api）检查，不另写工具。
- 每个端口有一套假的适配器给测试用；另外有一套契约测试，真假两种适配器都要通过。这和 A-NAS 的 Fake 与 Linux Adapter 是同一个做法。
- 新的剪辑规则必须放在 `domain/`，并附单元测试。

## 待定

- **RK3588 的角色**：
  - 它就是 NAS 本身（替代 x86），还是另一台设备；
  - 这决定了描述模型能不能交给别的机器做，以及剪辑要不要装进 A-NAS 的产品服务。
- **代码的归属**：重构后放在 `ai/montage/` 下面，还是单独成一个 Python 包，供 NAS 和别的设备共用。
- **板子上的视觉模型**：选型要和基模的同学一起定。
- **规划**：是否也要有离线方案（本地大模型），还是保持用云端。

## 关联

- 当前流水线：[`ai/eval/montage_v5/README.md`](../../ai/eval/montage_v5/README.md)
- 第六版的结论与已知问题：[video-montage-v6.md](../research/video-montage-v6.md)
- 精彩度试验与描述字段精简：[video-montage-highlight.md](../research/video-montage-highlight.md)
- 早期面向助手的剪辑规格：[photo-to-video.md](photo-to-video.md)
- 系统边界：[架构总览](../architecture/OVERVIEW.md)
