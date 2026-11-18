# 照片与视频剪辑成片

状态：draft（助手原型中实现：照片拼接来自 [#283](https://github.com/zhongwater123/A-NAS/issues/283)，已关闭；视频片段剪辑是 [#322](https://github.com/zhongwater123/A-NAS/issues/322) 与第三版 [#326](https://github.com/zhongwater123/A-NAS/issues/326)）
更新时间：2026-10-29

## 目标

成员用一句话说明想要的视频，例如“把海边的照片做成 20 秒的视频”“用爱丁堡那段视频剪一段 30 秒的城堡和老街”，系统从本人可见的素材里找出相关的照片或视频片段，由模型排出剪辑计划，规则校验后渲染成片。

当前阶段把这条链路做进 AI 助手的 MCP 工具（[`anas_mcp.py`](../../ai/assistant-spike/anas_mcp.py)），在助手原型沙箱里用真实的 EmbeddingGemma 2 与真实模型验收。依据见[视频剪辑 baseline](../research/video-moment-retrieval.md)、[CutClaw 调研](../research/video-editing-cutclaw.md)与[FilmAgent 调研](../research/video-editing-filmagent.md)。

## 非目标

- 产品化：相册服务还没有视频片段的向量与接口（[相册视频](photo-video.md)：视频暂不参与 AI），视频检索由原型里的替身服务提供；成员在相册窗口或 VNAS 中的入口与预览界面。
- 写回图库：成片只写进助手目录，不成为照片资产。
- 配乐、节拍对齐、转场（照片的淡入淡出除外）、字幕与特效。
- 规划模型看画面：它只读文字和数字，画面由本机视觉模型看。
- 部署到 Experimental NAS。

## 组成

```
Claude Code 或 Hermes（云端模型）
   │ MCP（stdio），在成员的助手沙箱里以成员身份运行
   ▼
anas_mcp.py ── 只做转接
   ├─ search_photos、make_slideshow ──► 相册服务的助手套接字（按 UID 认人）
   │                                     → 照片视图 → FFmpeg（ai/montage/render.py）→ /nas/助手/视频
   ├─ list_video_contents ──► videos_spike.py：库里每张卡片一行（秒数、镜头起止、光线、描述、亮点、毛病）
   ├─ search_video_moments ──► videos_spike.py：AI Worker 编码探针 → 窗口打分 → 镜头卡片（带本机视觉模型的描述）
   └─ make_montage ─────────► videos_spike.py：校验计划 → 本机视觉模型逐帧核对、收窄 → FFmpeg 剪切 → MP4 → /nas/助手/视频
```

- **MCP 服务器只做转接**：照片的检索、授权与排序在相册服务；视频的向量、索引与原件只在视频检索服务里，助手读不到。
- **视频检索服务是替身**：[`videos_spike.py`](../../ai/assistant-spike/videos_spike.py) 以相册服务的身份运行（ADR 0011），是唯一能连到 AI Worker 的身份；它在自己的助手套接字上按调用方 UID 认人，只服务 `a-nas-users` 的成员。产品中这部分由相册服务提供，等视频有了向量再定接口。
- **索引离线建好**：[`eval.video_moments index`](../../ai/eval/video_moments.py) 为一组测试视频写出镜头切点（FFmpeg 场景检测，阈值 0.25，短于 1 秒的切换丢弃；切点前后各 0.5 秒两帧的向量余弦不低于 0.86 的算同一个地方，两个镜头并成一个，记在 `same_place` 里，见 [#416](https://github.com/zhongwater123/A-NAS/issues/416)）、纯色帧标记和每个窗口最接近的 3 个词表标签（[`v1.json`](../../internal/photos/labels/v1.json)，不设阈值）。窗口每 2 秒一个，向量取窗口中点的帧（[视频剪辑 baseline](../research/video-moment-retrieval.md) 的 `2s1f`）。
- **本机视觉模型**（第三版）：Qwen3-VL-2B-Instruct（Q8_0 GGUF，Apache-2.0，[模型选型](../research/photo-ai-model-runtime-selection.md)里的本地 VLM 候选），由 llama.cpp 的 `llama-server` 提供 OpenAI 兼容接口。开发机上它在 Mac 本机用 Metal 运行，沙箱里的视频检索替身经 HTTP 调用；产品里应由 AI Worker 按需加载。它给每张卡片的几帧写一句描述（画面有变化时概括全部几帧，不只写其中一张：盲评 43 张画面有变化的卡片，这样写胜 23 次、只写共同部分胜 2 次，[#416](https://github.com/zhongwater123/A-NAS/issues/416)），再答光线和画面里最吸引人的一样东西（[`eval.video_moments describe`](../../ai/eval/video_moments.py)，存为索引旁的 `captions.json`），并在出片前核对画面（[`look.py`](../../ai/montage/look.py)）。云端规划模型只拿到它写的文字。
- **运镜与画面毛病**（[`picture.py`](../../ai/montage/picture.py)）：不交给视觉模型，用 OpenCV（AI Worker 已有的依赖）每 0.2 秒跟踪相邻两帧的角点，算出画面的平移与缩放：平移是转头，放大是往前走，平移去掉 1 秒滑动平均后剩下的是抖动；同样的帧再算清晰度（拉普拉斯方差）、亮度和过白像素占比（[`eval.video_moments measure`](../../ai/eval/video_moments.py)，存为 `pictures.json`）。
- **共享代码**：[`ai/montage/`](../../ai/montage/__init__.py)。`render.py` 与 `look.py` 只用标准库，助手的 MCP 服务器在沙箱里直接导入 `render.py`；`moments.py` 用 NumPy，在有向量的进程里运行。
- **身份与同意**：两个拼接工具不标只读，Hermes 每次调用前都要成员同意。

### 工具

| 工具 | 参数 | 返回 |
|---|---|---|
| `search_photos` | `query`、`page` | 照片 ID、名称、图库、拍摄时间、标签、相册（[AI 助手](ai-assistant.md)的相册工具） |
| `make_slideshow` | `photo_ids`（1–60 个，按出现顺序）、`seconds_per_photo`（1–10 秒，默认 3）、`crossfade`（默认否） | 成片路径、总时长、照片数 |
| `list_video_contents` | `videos`（只列这些视频，可省略）、`style`（体裁：预告片、旅行、回顾，可省略） | 每条视频的每张卡片按时间一行：卡片 id、起止秒数、所在镜头的起止、光线、描述、亮点与画面毛病，供模型先看库里有什么、写故事线和检索词；计划可以直接用目录里的卡片。给了 `style` 时附上这个体裁的剪法 `style_rules` |
| `search_video_moments` | `probes`（1–8 条画面描述，指令里每个画面主题一条）、`each`（每条探针各取分数最高的几张，彼此不同组，默认 5，条数乘 `each` 不超过 40）、`videos`（只在这些视频里找，可省略）、`lights`（只要这些光线的卡片）、`words`（只要描述或亮点含其中一个词的卡片）、`when`（最多 4 条 `{is, not}`，只留更像 `is` 的窗口，如夜晚而不是白天） | 镜头卡片，`each` 按探针列出各自取到的卡片，一个主题的卡片不会被另一个主题的高分挤掉；每张带本机视觉模型的描述 `caption`、光线 `light`、亮点 `highlight`，量出来的运镜 `motion` 与画面毛病 `flaws`，以及分数参照 `threshold` |
| `make_montage` | `segments`：按播放顺序的 `{card, start, end, shows, reason}`；`shows` 是这段要呈现指令里的什么，只用指令里的词，`reason` 是一句理由；`skipped`：检索到但没用上的每张卡片各一项 `{card, why}`，`why` 一句话写为什么不用，供复盘（2026-10-30 用户要求）；`style`：按哪个体裁剪的，随成片存档 | 成片路径、总时长、段数、按画面收窄的段（`trimmed`）与每段看到的画面（`seen`）；计划、落选理由、实际剪的秒数与核对结果存在成片旁的同名 JSON 里；计划不合规则或核对不过时逐条说明 |

- 成片都是 1920×1080、30 fps 的 H.264 MP4，画面等比缩放并补黑边。照片的总时长是张数 × 每张秒数；视频的总时长是各段之和。
- 照片成片没有音轨。视频成片保留原声，不加配乐和字幕（#350，2026-11-03 与项目方确认）：
  - 每段带原片同一区间的声音，补齐或截到与画面同长，保证音画对齐；
  - 统一为 48 kHz 立体声 AAC，素材没有音轨的段补静音；
  - 每段首尾各渐变 30 毫秒，段与段之间不爆音。
- 文件名由工具生成，不覆盖已有文件；拼接失败时不留下半截文件。

### 镜头卡片与剪辑计划

- **卡片**：一个镜头里画面大致不变的一段：窗口与这段的平均画面余弦低于 0.9 就切开（离并掉的同地点切点不到一个窗口时，就切在那个切点上），每段至少 2 个窗口（4 秒），超过 20 秒的再等分。67 分钟、5 个镜头的爱丁堡散步得到 396 张，中位 10 秒；第二版按 20 秒硬切是 204 张，一张里常混着几处景。卡片有 `id`、`video`、这张卡片覆盖的 `start`–`end`、所在镜头的 `shot` 起止、各探针的最高分及出现时刻、出现最多的 5 个标签，本机视觉模型的描述 `caption`、光线 `light`（晴天、阴天、逆光、黄昏、夜晚、室内）与亮点 `highlight`（不超过 12 个字），运镜 `motion`（固定、向前走、向后退、向左转、向右转、抬头、低头：中位平移每秒超过画宽的 6% 是转头，中位缩放每秒超过 1.5% 是走动）与画面毛病 `flaws`（清晰度低于 20 为模糊、抖动高于画宽的 1% 为抖动、平均亮度低于 25 为过暗、过白像素超过 30% 为过曝，阈值按试点集的极端画面定），以及 `group`：卡片的平均向量余弦不低于 0.97 时同组，表示画面相似。纯色帧的窗口不参与打分。
- **按画面切的卡片**（`index.json` 的 `"cards": "picture"`，`video_moments index --cards picture`，[#416](https://github.com/zhongwater123/A-NAS/issues/416)，2026-11-08 用户确定）：不用 FFmpeg 的切点，只看每 2 秒一个窗口的画面：窗口与这张卡片的平均画面余弦低于 0.86 就开新卡片，除非后面 2 个窗口里有一个又像回来（全景里插一个特写不切开）；超过 120 秒的卡片在相邻窗口最不像处拆开，两边各至少 20 秒（方案 B 的做法）；纯色窗口归前一张卡片。这样一张卡片基本是一场戏，卡片的 `shot` 就是它自己的起止。用例 01、02 在 FFmpeg 镜头内的 131 处合并经用户逐对标注全部正确；按场景口径的换场标注，F1 0.57 / 0.59，按切点切是 0.32 / 0.48。用例 01 由 84 张变成 18 张（最长 96 秒），02 由 223 张变成 88 张（最长 114 秒），04 是 171 张。出片时照旧在每段首尾查切点、往里裁，不带进前一个镜头。不加这一项时卡片照旧按镜头切。
- **描述用的关键帧**（`Index.key_windows`，[#392](https://github.com/zhongwater123/A-NAS/issues/392)）：卡片第一个窗口，之后与当前这组的平均画面余弦低于 0.9 处各取一个；同一组 12 秒没有新关键帧，每 12 秒补一个与已取的最不像的窗口；至少 3 个（不足就在卡片里均匀取 3 帧），最多 20 个（最相似的相邻两个去掉后一个）。每个关键帧写一个节拍（景别、看得见的主体和动作）。均匀每 10 秒一帧漏了后视镜里的女人，每 6 秒一帧时模型把帧两两配对，先分组再按组写在 14 组时开始循环；关键帧在 112 秒、20 帧的卡片上写出 20 个不重复的节拍。描述还没接进出片流程。
- **分数参照**：`threshold` 是试点集上 `2s1f` 拒掉 80% 无匹配查询的阈值 0.6533；低于它的卡片与“库里没有”分不开，模型应当少用或不用。
- **计划的规则**（[`moments.py`](../../ai/montage/moments.py) 的 `check`，不合规则时整次拒绝，列出每一条）：
  1. 至少 1 段，段数不设上限，每段的 `card` 是检索结果里的卡片；
  2. 每段落在卡片所在镜头的 `shot` 范围内，片段不跨镜头切点（卡片的秒数取两位小数、切点三位，允许 0.005 秒误差，不到一帧）；
  3. 每段至少 1.5 秒。计划给了秒数时，每段按计划的秒数剪，不设上限；计划没给秒数时（规则版、按节奏分秒数）每段最多 6 秒，同一个画面放 8 秒就让人疲劳（2026-10-30 用户确定）。剪法22 时 6 秒的上限把上海 vlog 计划的 79 秒剪成 65 秒，2026-11-07 用户确定计划的秒数不再受它限制（剪法23，[#416](https://github.com/zhongwater123/A-NAS/issues/416)）；
  4. 同一视频的各段不重叠；
  5. 不含纯黑或纯色画面；
  6. `reason` 不超过 200 个字符；
  7. 总长不超过 180 秒；
  8. 视频检索替身开着核对时，每段的 `shows` 为 1–40 个字符；
  9. `skipped` 里每项是检索结果里的卡片、不在计划里、不重复，`why` 为 1–80 个字符。规则不检查是否列全了没用的卡片。
- **出片前看画面**（[`look.py`](../../ai/montage/look.py)）：规则通过后，每段每 0.5 秒取一帧（短边 360 像素），逐帧问本机视觉模型“画面里有没有 shows”，先答有或没有再说一句画面是什么。小模型先描述再判断、或填 JSON 判定时更容易答“有”（14 张易错画面上分别对 8 张和 11 张，这种问法对 13 张）。取最长的连续符合段，中间允许 1 帧判错：短于 1.5 秒的段整次退回，说明画面实际是什么，请模型换卡片或删掉；其余段收窄到这一截再剪（入点出点精修）。同一段同一个 `shows` 只看一次，退回后重交的计划只看改了的段。
- 选哪几段、每段多长、什么顺序由模型决定；规则只管能不能剪。工具说明另外要求（不强制）：成员说的时长是上限，切题的片段不够就剪短并说明，不为换画面或凑时长放进不切题的片段。
- **先看目录、写故事线，再检索**：模型先用 `list_video_contents` 读库里每张卡片的描述，在同一轮里先写一条故事线（成片的段落按播放顺序、每段一句内容和对应的大致秒数），再按故事线每段要的画面写检索词，不为故事线另调一次模型。只凭对指令的想象写检索词时，侦探片里 3 分钟的警局戏一张都没进候选（[#326](https://github.com/zhongwater123/A-NAS/issues/326)）。
- **体裁**：不同体裁剪法差别大，成员可以先选体裁（预告片、旅行、回顾），不选时模型按指令判断，判断不出就不定。每个体裁只写通用的结构、收尾和节奏，例如预告片后半段不按时间顺序、用最有悬念的画面收尾、不交代结局；旅行按路线走、结尾落在安静的画面。只把选中的那一种剪法随目录交给模型，不为体裁多调一次模型。按时间顺序写的故事线会让预告片停在最后一个场景，木偶、手电筒、红发女子这些最有悬念的画面反而用不上（[#326](https://github.com/zhongwater123/A-NAS/issues/326)）。
- **台词只定切点，不进规划**（剪法23，[#416](https://github.com/zhongwater123/A-NAS/issues/416)）：语音识别的词按停顿 ≥0.6 秒或句末标点断句；程序定每段的入出点时，落在句子中间的一端先往外扩到整句，前后各留 0.2 秒，以卡片的同画面范围为限，可以超过计划给的秒数；扩不了才挪到最近的停顿（方案 B 的做法）。剪法22 曾把台词写进目录、让规划在 `said` 里抄下要保留的台词：规划改为按台词挑卡片，侦探片要的“山间公路上的车”取成了有台词的车内镜头，上海的“进餐厅”跳过了门口镜头，每次规划的花费也约翻一倍，2026-11-07 用户确定撤回。目录不再列台词，计划没有 `said`。
- **目标时长由程序按分量分配**（剪法24，[#416](https://github.com/zhongwater123/A-NAS/issues/416)）：规划不写秒数，给每个段落写分量 `weight`（1–10 的整数，要交代、要停留的大，冲击、一闪而过的小）。程序先给每个镜头 1.5 秒，再把目标时长剩下的部分按分量分给各段落，总和等于目标（`moments.allocated`，仿方案 B 大纲按分量分部分时长）；段落内按画面变化分给各镜头，入出点照旧从最符合 `shows` 的窗口往两边扩。缺分量或分量不是 1–10 整数的计划退回。此前两版都由规划自己写秒数：剪法20 把目标当上限（“宁短勿凑”），规划只要了目标的 49–83%，成片 45–76 秒（目标 60 和 90）；剪法23 去掉上限，补整句后侦探片 72–73 秒、深圳冲浪 79 秒（目标 60）。2026-11-07 用户确定改由程序分配。
- **按故事线排段**：故事线是骨架，每段挑最有看点的画面，不必把每一步都交代；不要求每个检索主题都用上，搭不上故事线的整个不用；同一场景相邻的卡片可以连用，只有同组才算重复画面；`reason` 写这段属于故事线哪一段、为什么选它。早先要求“每处画面用一两段”时，模型把每个主题当成要填的格子，同场景的过渡镜头都按“同主题重复”删掉，成片在场景之间硬跳；改成要求逐段写怎么接上一段时，模型又偏向挑过渡镜头，丢了木偶、手电筒、红发女子这些最有悬念的画面。
- **条件先筛、主题再排**：条件不和主题写进同一条检索词，否则向量只匹配其中一半，“夜晚的城市”找回的是白天的城市。光线用 `lights` 按本机视觉模型给的光线筛，描述里写得出的东西（雨、办公室）用 `words` 按描述筛，筛完再按探针排序；筛完没有卡片就是库里没有。“雨”这类素材少的主题只靠向量会混进分数相近的无关画面（太空镜头 0.66–0.67，真的雨 0.69）。地点、事件这类画面看不出的条件先确定视频，作 `videos`；`lights`、`words` 表达不了的才用 `when`。
- **不用模型的规则版**：`moments.plan` 用同样的卡片做对照——过 `threshold` 的卡片按分数取，从最高分的窗口往两边扩，每段最多 6 秒，同一镜头里同一画面（余弦 ≥ 0.97）不重复，目标时长是上限，同一视频按时间排。给了 `look` 时每段先按得分最高的探针（有 `when` 时加上条件，如“夜晚的城市”）看画面：核对不过的卡片跳过、换下一张，其余收窄。

## 场景

### 照片：一句话成片

- When：成员说“把海边的照片做成 20 秒的视频”。
- Then：模型先 `search_photos`，再选定张数与每张时长，使两者之积为 20 秒，调用 `make_slideshow`；成员同意后写出成片。

### 视频：一句话成片

- Given：原型沙箱带着一组测试视频的索引启动（`SPIKE_VIDEOS`）。
- When：成员说“用爱丁堡那段视频剪一段 30 秒的城堡和老街”。
- Then：模型把要求按画面主题拆成探针调用 `search_video_moments`，每个主题一条（有地点或白天黑夜这类条件时按上面拆开），读卡片的描述、分数、标签、分组和镜头范围，排出各段，写明每段呈现什么和理由，调用 `make_montage`；出片前本机视觉模型核对画面，成员同意后写出成片，回复里说明用了哪些片段、为什么，只按描述与核对看到的画面介绍。

### 视频：核对画面不切题

- When：计划里某段的画面不是它的 `shows`（例如“湖上的小船”选到天鹅，“城市夜景”选到白天的街道）。
- Then：`make_montage` 整次退回，说明第几段、多少帧符合、画面实际是什么；模型换卡片或删掉这段再调用。部分符合的段不退回，收窄到符合的那一截。

### 视频：计划不合规则

- When：模型给出的某段越出了镜头，或短于 1.5 秒，或与另一段重叠。
- Then：`make_montage` 整次拒绝，逐条说明是第几段、哪条规则；模型照着改了再调用，不需要成员再说一遍。

### 库里没有

- When：要找的内容素材里没有。
- Then：照片检索返回空结果或只按名称匹配的说明；视频卡片的分数都低于 `threshold`。模型告诉成员没找到，不用无关画面凑时长。试点上的阈值不可靠（爱丁堡的 40 张卡片全部高于它，“冲浪”也有一张 0.66），模型还要看标签判断。

### 传入视频资产给照片拼接

- Then：`make_slideshow` 拒绝整次调用，说明哪些 ID 是视频。

## 边界与失败

- **参数越界**：照片数、每张秒数、探针条数与长度、卡片数超出范围时拒绝并给出范围，不静默截断。
- **服务不可用**：相册或视频检索连不上时返回一句说明；AI Worker 或本机视觉模型不可用时视频检索返回 503 与原因，不跳过核对就剪。
- **FFmpeg 失败或超时（600 秒）**：返回原因，删除未写完的文件。
- **隐私**：模型是云端模型时，工具返回的名称、拍摄时间、标签、相册名、镜头卡片的分数与标签会发给模型服务商（2026-10-29 用户确认文字可以发送，[#295](https://github.com/zhongwater123/A-NAS/issues/295)）。照片与视频的画面只交给本机的 FFmpeg 与本地模型（包括本机视觉模型），云端模型只拿到它写的描述与核对结果；`look_at_photo` 例外，它把照片画面交给模型。测试素材都是可丢弃的公开素材。

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

第三版还要本机视觉模型：用 Homebrew 装 llama.cpp，从 ModelScope 或 Hugging Face 下载 `Qwen/Qwen3-VL-2B-Instruct-GGUF` 的 `Qwen3VL-2B-Instruct-Q8_0.gguf` 与 `mmproj-Qwen3VL-2B-Instruct-Q8_0.gguf`（SHA-256 以 Hugging Face 上登记的为准），在 Mac 上启动，再给卡片写描述（每张约 1 秒）：

```bash
llama-server -m ~/A-NAS-models/qwen3-vl-2b-instruct/Qwen3VL-2B-Instruct-Q8_0.gguf --mmproj ~/A-NAS-models/qwen3-vl-2b-instruct/mmproj-Qwen3VL-2B-Instruct-Q8_0.gguf --host 127.0.0.1 --port 8090 -c 16384 -np 4 --no-webui
ANAS_DATASETS=~/A-NAS-datasets python3 -m eval.video_moments --set eval/video_moments_edinburgh.json describe --cache <这组视频的模型目录>
```

运镜与画面毛病用装了 OpenCV 的 Python 测量（版本与 `ai/requirements.in` 相同；开发机上可另建虚拟环境装 `opencv-contrib-python-headless`、NumPy、Pillow），每张约 1 秒：

```bash
ANAS_DATASETS=~/A-NAS-datasets <装了 OpenCV 的 python> -m eval.video_moments --set eval/video_moments_edinburgh.json measure --cache <这组视频的模型目录>
```

再带着相册的真实模型与这组视频的索引启动原型沙箱（`SPIKE_VISION` 是容器里看到的视觉模型地址，Colima 里 Mac 本机是 `192.168.5.2`；不给时不核对，`shows` 也不必填）：

```bash
HERMES_SPIKE_NAME=video-montage-sbx SPIKE_PHOTOS=1 SPIKE_PHOTO_MODELS=~/A-NAS-models \
  SPIKE_VIDEOS=~/A-NAS-datasets/cache/video-moments-edinburgh/embeddinggemma-2-740m@e7a8a2204b91+mediapipe-1.1.0+tok70+l2 \
  SPIKE_VIDEO_FILES=~/A-NAS-datasets/videos SPIKE_VISION=http://192.168.5.2:8090/v1/chat/completions ai/assistant-spike/run.sh sandbox
```

用 Claude Code 当模型时，把 [`mcp-as-member.sh`](../../ai/assistant-spike/mcp-as-member.sh) 登记为 stdio 服务器（只存入本机的 Claude Code 配置，不进仓库），它在 alice 的助手命名空间里以 alice 的身份运行 `anas_mcp.py`：

```bash
claude mcp add anas -e HERMES_SPIKE_NAME=video-montage-sbx -e DOCKER_CONTEXT=colima-anas -- <检出>/ai/assistant-spike/mcp-as-member.sh alice
```

重启视频检索替身（例如写了新描述）后，也要停掉成员的助手（`systemctl stop anas-assistant@alice`），它的命名空间里绑定的还是旧套接字。

端到端并计时：[`eval.video_pipeline`](../../ai/eval/video_pipeline.py) 把上面的步骤串成两段，就是成员遇到的两段等待。`prepare` 依次抽帧、编码、建索引，再同时写描述、测运镜；`cut` 从收到一句指令开始计时，到成片拷出来为止，并分出规划模型与工具（出片前核对加渲染）各用了多久；把 `--instruction` 换成 `--instructions <指令文件>`，就逐条剪。`run` 两段连着做。每次的耗时追加到这组视频模型目录里的 `pipeline.json`。各步从缓存续跑，要测完整的预处理时间，就给这组视频另起一个集合文件，让它用新的缓存目录。`cut` 开始前会核对沙箱服务的是不是这组视频的索引。[`video_moments_library.json`](../../ai/eval/video_moments_library.json) 是计时用的库，要凑满十条视频，现在放了爱丁堡、巴黎、侦探短片和大帆船四条（共 124 分钟）；[`video_montage_library.json`](../../ai/eval/video_montage_library.json) 是它的九条指令，其中有的跨几条视频。在 Mac 上，AI Worker 的 Python 在 `anas` 虚拟机里：

```bash
cd ai
python3 -m eval.video_pipeline --set eval/video_moments_earth_meridian.json run \
  --python "colima ssh -p anas -- env ANAS_DATASETS=$HOME/A-NAS-datasets /home/$USER.guest/anas-ai/bin/python" \
  --model ~/A-NAS-models/embeddinggemma-2-740m/embeddinggemma-2-740m.litertlm \
  --vision http://192.168.5.2:8090/v1/chat/completions --instruction 这部侦探片的预告片
```

评测（只在开发机上）：[`eval.video_planner`](../../ai/eval/video_planner.py) 让 Claude Code 只用两个视频工具为每条指令规划一次，记下调用过程与费用，并把成片与计划拷到缓存的 `montage-model3/`；[`eval.video_montage`](../../ai/eval/video_montage.py) 用 `rules` 剪规则版（加 `--vision` 是第三版，存到 `montage-rules3/`）、`score` 按 `video_montage.json` 的切题标注算各版本的指标；[`eval.video_judge`](../../ai/eval/video_judge.py) 让三个视觉模型评审（画面质量、切题、剪辑自然度，0–10 分）看每段每秒 1 帧打分，密钥从 `SILICONFLOW_API_KEY` 读。评审只用于公开的测试视频，产品里画面不出本机。

测试随 `make ai-test` 运行：渲染测试需要 `ffmpeg` 与 `ffprobe`，镜头卡片与计划校验的测试需要 NumPy，运镜与画面毛病的测试需要 OpenCV，没有时跳过。

## 验收证据

- 单元测试 [`ai/tests/test_montage.py`](../../ai/tests/test_montage.py)：淡入淡出不改变总长；规则版只取过阈值的卡片、画面不变时取得短、从不凑满目标；按视频、按 `when` 条件、按光线与描述里的词筛选，目录按时间逐张列出卡片；视频片段逐个精确定位；横竖照片与不同尺寸的视频片段都渲染成 1920×1080、时长正确；FFmpeg 失败时不留文件；镜头在画面变化处切成卡片、长时间不变的画面等分、相似的卡片同组、纯色帧不参与；描述、光线、亮点、运镜与画面毛病只挂在为它写的卡片上；画面右移是向左转、画面放大是向前走、静止画面固定且不抖、绕着不动的路径抖是抖动、模糊画面清晰度低、全白画面全部过白；合规的计划得到片段，不合规的计划列出每一条问题，计划的秒数超过 6 秒照样剪，两位小数的秒数不被三位小数的切点挡住；要核对时每段要有 `shows`；规则版跳过核对不过的卡片、按核对收窄；核对取最长的连续符合段并容许 1 帧判错，太短的段不可用；每 0.5 秒取一帧。
- 照片：#283 中 Claude 经助手工具检索 717 张真实照片，做出 20 秒与 15 秒（淡入淡出）的成片（2026-10-29）。
- 视频（2026-10-29，67 分钟的爱丁堡散步，Claude Code 经 `mcp-as-member.sh` 只用两个视频工具）：“城堡和老城石头街道 30 秒”与“公交车和热闹街头 20 秒”各一次检索、一次剪辑就得到成片，逐秒看帧分别有 5/6 与 10/10 段切题；“海边冲浪”时模型说明库里没有、不剪。模型看不到画面，会按探针名描述没看过的细节，工具说明要求它只说标签与分数支持的内容；检索选错的片段只有看画面才能发现。细节见[研究文档](../research/video-moment-retrieval.md#模型规划3222026-10-29)。
- 试点集 5 条指令（2026-10-29，规则版与模型版对比 baseline，见[研究文档](../research/video-moment-retrieval.md#三版对比3222026-10-29)）：规则版全部切题，跨镜头残帧、黑屏都是 0；模型版的切题占比 0.57–1.0，素材少时仍会加铺垫镜头。三个视觉评审逐段判断与人工标注 66 段里一致 60 段。

- 第三版（2026-10-29，[#326](https://github.com/zhongwater123/A-NAS/issues/326)，见[研究文档](../research/video-moment-retrieval.md#第三版本机视觉模型3262026-10-29)）：模型版切题占比在湖上的小船、城市夜景从 0.77、0.69 升到 1.00，美食 0.92、新加坡的美食 0.75，且不再凑满时长；核对退回过菜摊、黑场、普通建筑，模型都在一两次重写内改好。带城垛的建筑仍会被当成城堡。

## 关联

- Issue：[#283](https://github.com/zhongwater123/A-NAS/issues/283)（照片，已关闭）、[#322](https://github.com/zhongwater123/A-NAS/issues/322)（视频第二版）、[#326](https://github.com/zhongwater123/A-NAS/issues/326)（视频第三版）、[#295](https://github.com/zhongwater123/A-NAS/issues/295)（视频剪辑技术探索）
- 架构：[相册本地 AI](../architecture/photo-ai.md)、[相册技术设计](../architecture/photo-library.md)
- ADR：[0011 相册服务的专用身份](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[0024 每个成员一个沙箱化的 Hermes 智能体](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
- 规格：[相册与本地智能检索](photo-library.md)、[相册视频](photo-video.md)、[AI 助手](ai-assistant.md)
- 研究：[视频剪辑 baseline](../research/video-moment-retrieval.md)、[CutClaw 调研](../research/video-editing-cutclaw.md)、[FilmAgent 调研](../research/video-editing-filmagent.md)
- 代码：[助手的 MCP 工具](../../ai/assistant-spike/anas_mcp.py)、[视频检索替身](../../ai/assistant-spike/videos_spike.py)、[原型沙箱](../../ai/assistant-spike/run.sh)、[以成员身份运行 MCP](../../ai/assistant-spike/mcp-as-member.sh)、[渲染](../../ai/montage/render.py)、[镜头卡片与计划校验](../../ai/montage/moments.py)、[本机视觉核对](../../ai/montage/look.py)、[运镜与画面毛病](../../ai/montage/picture.py)、[模型版评测](../../ai/eval/video_planner.py)、[端到端计时](../../ai/eval/video_pipeline.py)、[索引](../../ai/eval/video_moments.py)
- 测试：[`ai/tests/test_montage.py`](../../ai/tests/test_montage.py)
