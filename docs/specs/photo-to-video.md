# 照片与视频剪辑成片

状态：draft（助手原型中实现：照片拼接来自 [#283](https://github.com/zhongwater123/A-NAS/issues/283)，已关闭；视频片段剪辑是 [#322](https://github.com/zhongwater123/A-NAS/issues/322)）
更新时间：2026-10-29

## 目标

成员用一句话说明想要的视频，例如“把海边的照片做成 20 秒的视频”“用爱丁堡那段视频剪一段 30 秒的城堡和老街”，系统从本人可见的素材里找出相关的照片或视频片段，由模型排出剪辑计划，规则校验后渲染成片。

当前阶段把这条链路做进 AI 助手的 MCP 工具（[`anas_mcp.py`](../../ai/assistant-spike/anas_mcp.py)），在助手原型沙箱里用真实的 EmbeddingGemma 2 与真实模型验收。依据见[视频剪辑 baseline](../research/video-moment-retrieval.md)、[CutClaw 调研](../research/video-editing-cutclaw.md)与[FilmAgent 调研](../research/video-editing-filmagent.md)。

## 非目标

- 产品化：相册服务还没有视频片段的向量与接口（[相册视频](photo-video.md)：视频暂不参与 AI），视频检索由原型里的替身服务提供；成员在相册窗口或 VNAS 中的入口与预览界面。
- 写回图库：成片只写进助手目录，不成为照片资产。
- 配乐、节拍对齐、转场（照片的淡入淡出除外）、字幕与特效。
- 视觉大模型给镜头写描述；模型只读文字和数字。
- 部署到 Experimental NAS。

## 组成

```
Claude Code 或 Hermes（云端模型）
   │ MCP（stdio），在成员的助手沙箱里以成员身份运行
   ▼
anas_mcp.py ── 只做转接
   ├─ search_photos、make_slideshow ──► 相册服务的助手套接字（按 UID 认人）
   │                                     → 照片视图 → FFmpeg（ai/montage/render.py）→ /nas/助手/视频
   ├─ search_video_moments ──► videos_spike.py：AI Worker 编码探针 → 窗口打分 → 镜头卡片
   └─ make_montage ─────────► videos_spike.py：校验计划 → FFmpeg 剪切 → MP4 → /nas/助手/视频
```

- **MCP 服务器只做转接**：照片的检索、授权与排序在相册服务；视频的向量、索引与原件只在视频检索服务里，助手读不到。
- **视频检索服务是替身**：[`videos_spike.py`](../../ai/assistant-spike/videos_spike.py) 以相册服务的身份运行（ADR 0011），是唯一能连到 AI Worker 的身份；它在自己的助手套接字上按调用方 UID 认人，只服务 `a-nas-users` 的成员。产品中这部分由相册服务提供，等视频有了向量再定接口。
- **索引离线建好**：[`eval.video_moments index`](../../ai/eval/video_moments.py) 为一组测试视频写出镜头切点（FFmpeg 场景检测，阈值 0.25，短于 1 秒的切换丢弃）、纯色帧标记和每个窗口最接近的 3 个词表标签（[`v1.json`](../../internal/photos/labels/v1.json)，不设阈值）。窗口每 2 秒一个，向量取窗口中点的帧（[视频剪辑 baseline](../research/video-moment-retrieval.md) 的 `2s1f`）。
- **共享代码**：[`ai/montage/`](../../ai/montage/__init__.py)。`render.py` 只用标准库，助手的 MCP 服务器在沙箱里直接导入；`moments.py` 用 NumPy，在有向量的进程里运行。
- **身份与同意**：两个拼接工具不标只读，Hermes 每次调用前都要成员同意。

### 工具

| 工具 | 参数 | 返回 |
|---|---|---|
| `search_photos` | `query`、`page` | 照片 ID、名称、图库、拍摄时间、标签、相册（[AI 助手](ai-assistant.md)的相册工具） |
| `make_slideshow` | `photo_ids`（1–60 个，按出现顺序）、`seconds_per_photo`（1–10 秒，默认 3）、`crossfade`（默认否） | 成片路径、总时长、照片数 |
| `search_video_moments` | `probes`（1–8 条画面描述）、`limit`（1–40，默认 20） | 镜头卡片，最相关的在前，以及分数参照 `threshold` |
| `make_montage` | `segments`：按播放顺序的 `{card, start, end}` | 成片路径、总时长、段数；计划不合规则时逐条说明 |

- 成片都是 1920×1080、30 fps 的 H.264 MP4，没有音轨，画面等比缩放并补黑边。照片的总时长是张数 × 每张秒数；视频的总时长是各段之和。
- 文件名由工具生成，不覆盖已有文件；拼接失败时不留下半截文件。

### 镜头卡片与剪辑计划

- **卡片**：一个镜头里最长 20 秒的一段（更长的镜头等分），有 `id`、`video`、这张卡片覆盖的 `start`–`end`、所在镜头的 `shot` 起止、各探针的最高分及出现时刻、出现最多的 5 个标签，以及 `group`：卡片的平均向量余弦不低于 0.97 时同组，表示画面相似。纯色帧的窗口不参与打分。
- **分数参照**：`threshold` 是试点集上 `2s1f` 拒掉 80% 无匹配查询的阈值 0.6533；低于它的卡片与“库里没有”分不开，模型应当少用或不用。
- **计划的规则**（[`moments.py`](../../ai/montage/moments.py) 的 `check`，不合规则时整次拒绝，列出每一条）：
  1. 1–30 段，每段的 `card` 是检索结果里的卡片；
  2. 每段落在卡片所在镜头的 `shot` 范围内，片段不跨镜头切点；
  3. 每段至少 1.5 秒；
  4. 同一视频的各段不重叠；
  5. 总长不超过 180 秒。
- 选哪几段、每段多长、什么顺序由模型决定；规则只管能不能剪。

## 场景

### 照片：一句话成片

- When：成员说“把海边的照片做成 20 秒的视频”。
- Then：模型先 `search_photos`，再选定张数与每张时长，使两者之积为 20 秒，调用 `make_slideshow`；成员同意后写出成片。

### 视频：一句话成片

- Given：原型沙箱带着一组测试视频的索引启动（`SPIKE_VIDEOS`）。
- When：成员说“用爱丁堡那段视频剪一段 30 秒的城堡和老街”。
- Then：模型把要求拆成几条画面探针调用 `search_video_moments`，读卡片的分数、标签、分组和镜头范围，排出各段，调用 `make_montage`；成员同意后写出成片，回复里说明用了哪些片段、为什么。

### 视频：计划不合规则

- When：模型给出的某段越出了镜头，或短于 1.5 秒，或与另一段重叠。
- Then：`make_montage` 整次拒绝，逐条说明是第几段、哪条规则；模型照着改了再调用，不需要成员再说一遍。

### 库里没有

- When：要找的内容素材里没有。
- Then：照片检索返回空结果或只按名称匹配的说明；视频卡片的分数都低于 `threshold`。模型告诉成员没找到，不用无关画面凑时长。

### 传入视频资产给照片拼接

- Then：`make_slideshow` 拒绝整次调用，说明哪些 ID 是视频。

## 边界与失败

- **参数越界**：照片数、每张秒数、探针条数与长度、卡片数超出范围时拒绝并给出范围，不静默截断。
- **服务不可用**：相册或视频检索连不上时返回一句说明；AI Worker 不可用时视频检索返回 503 与原因。
- **FFmpeg 失败或超时（600 秒）**：返回原因，删除未写完的文件。
- **隐私**：模型是云端模型时，工具返回的名称、拍摄时间、标签、相册名、镜头卡片的分数与标签会发给模型服务商（2026-10-29 用户确认文字可以发送，[#295](https://github.com/zhongwater123/A-NAS/issues/295)）。照片与视频的画面只交给本机的 FFmpeg 与本地模型；`look_at_photo` 例外，它把照片画面交给模型。测试素材都是可丢弃的公开素材。

## 待决定

- 视频片段的向量与检索进入产品时由哪个服务提供、接口长什么样；替身服务的形状只作参考。
- 成片放进个人空间：要 `submit_changes` 的 `copy`，原型的更改代理不支持，产品的执行方支持。
- 拼接在哪里运行：现在在助手沙箱（照片）与视频检索服务（视频）里直接调 FFmpeg；产品中应交给媒体 Worker，用它的排队与资源上限。
- `anas_mcp.py` 本身也是原型的替身：产品的 MCP 最终由产品服务提供（[#179](https://github.com/zhongwater123/A-NAS/issues/179)）。

## 运行

只在开发机上运行，不碰实验 NAS。原型沙箱的准备见 [`run.sh`](../../ai/assistant-spike/run.sh) 开头；在 Mac 上 Docker 与 Go 都在 `anas` 虚拟机里，命令在虚拟机中、从挂载的检出目录运行。

先为一组测试视频抽帧、编码、建索引（[视频剪辑 baseline](../research/video-moment-retrieval.md#复现)，`--set` 让这组视频用自己的缓存目录）：

```bash
cd ai
ANAS_DATASETS=~/A-NAS-datasets python3 -m eval.video_moments --set eval/video_moments_edinburgh.json frames
ANAS_DATASETS=~/A-NAS-datasets ~/anas-ai/bin/python -m eval.video_moments --set eval/video_moments_edinburgh.json embed --model ~/A-NAS-models/embeddinggemma-2-740m/embeddinggemma-2-740m.litertlm
ANAS_DATASETS=~/A-NAS-datasets ~/anas-ai/bin/python -m eval.video_moments --set eval/video_moments_edinburgh.json index --model ~/A-NAS-models/embeddinggemma-2-740m/embeddinggemma-2-740m.litertlm
```

再带着相册的真实模型与这组视频的索引启动原型沙箱：

```bash
HERMES_SPIKE_NAME=video-montage-sbx SPIKE_PHOTOS=1 SPIKE_PHOTO_MODELS=~/A-NAS-models \
  SPIKE_VIDEOS=~/A-NAS-datasets/cache/video-moments-edinburgh/embeddinggemma-2-740m@e7a8a2204b91+mediapipe-1.1.0+tok70+l2 \
  SPIKE_VIDEO_FILES=~/A-NAS-datasets/videos ai/assistant-spike/run.sh sandbox
```

用 Claude Code 当模型时，把 [`mcp-as-member.sh`](../../ai/assistant-spike/mcp-as-member.sh) 登记为 stdio 服务器（只存入本机的 Claude Code 配置，不进仓库），它在 alice 的助手命名空间里以 alice 的身份运行 `anas_mcp.py`：

```bash
claude mcp add anas -e HERMES_SPIKE_NAME=video-montage-sbx -e DOCKER_CONTEXT=colima-anas -- <检出>/ai/assistant-spike/mcp-as-member.sh alice
```

测试随 `make ai-test` 运行：渲染测试需要 `ffmpeg` 与 `ffprobe`，镜头卡片与计划校验的测试需要 NumPy，没有时跳过。

## 验收证据

- 单元测试 [`ai/tests/test_montage.py`](../../ai/tests/test_montage.py)：淡入淡出不改变总长；视频片段逐个精确定位；横竖照片与不同尺寸的视频片段都渲染成 1920×1080、时长正确；FFmpeg 失败时不留文件；长镜头等分成卡片、相似的卡片同组、纯色帧不参与；合规的计划得到片段，不合规的计划列出每一条问题。
- 照片：#283 中 Claude 经助手工具检索 717 张真实照片，做出 20 秒与 15 秒（淡入淡出）的成片（2026-10-29）。
- 视频（2026-10-29，67 分钟的爱丁堡散步，Claude Code 经 `mcp-as-member.sh` 只用两个视频工具）：“城堡和老城石头街道 30 秒”与“公交车和热闹街头 20 秒”各一次检索、一次剪辑就得到成片，逐秒看帧分别有 5/6 与 10/10 段切题；“海边冲浪”时模型说明库里没有、不剪。模型看不到画面，会按探针名描述没看过的细节，工具说明要求它只说标签与分数支持的内容；检索选错的片段只有看画面才能发现。细节见[研究文档](../research/video-moment-retrieval.md#模型规划3222026-10-29)。

## 关联

- Issue：[#283](https://github.com/zhongwater123/A-NAS/issues/283)（照片，已关闭）、[#322](https://github.com/zhongwater123/A-NAS/issues/322)（视频）、[#295](https://github.com/zhongwater123/A-NAS/issues/295)（视频剪辑技术探索）
- 架构：[相册本地 AI](../architecture/photo-ai.md)、[相册技术设计](../architecture/photo-library.md)
- ADR：[0011 相册服务的专用身份](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[0024 每个成员一个沙箱化的 Hermes 智能体](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
- 规格：[相册与本地智能检索](photo-library.md)、[相册视频](photo-video.md)、[AI 助手](ai-assistant.md)
- 研究：[视频剪辑 baseline](../research/video-moment-retrieval.md)、[CutClaw 调研](../research/video-editing-cutclaw.md)、[FilmAgent 调研](../research/video-editing-filmagent.md)
- 代码：[助手的 MCP 工具](../../ai/assistant-spike/anas_mcp.py)、[视频检索替身](../../ai/assistant-spike/videos_spike.py)、[原型沙箱](../../ai/assistant-spike/run.sh)、[以成员身份运行 MCP](../../ai/assistant-spike/mcp-as-member.sh)、[渲染](../../ai/montage/render.py)、[镜头卡片与计划校验](../../ai/montage/moments.py)、[索引](../../ai/eval/video_moments.py)
- 测试：[`ai/tests/test_montage.py`](../../ai/tests/test_montage.py)
