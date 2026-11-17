# 剪辑第六版（实验脚本）

把一组旅行素材剪成一条配乐成片。调研、实验与技术路线见[剪辑第六版研究](../../../docs/research/video-montage-v6.md)；跟进 issue [#453](https://github.com/zhongwater123/A-NAS/issues/453)。渲染用产品的 [`montage.render`](../../montage/render.py)，其余脚本都在本目录，在这里运行。

## 流程

```text
原始视频 ──(A-NAS 视频索引)──▶ 2 秒窗口 + 画面向量
        │
        ├─ cards.py frames     按画面切卡片，取关键帧
        ├─ quality.py          量模糊、抖动、遮挡、夜景失焦
        ├─ cards.py describe   Qwen3.5-4B 写节拍、过程、场景（只描述画质过关的卡片）
        │
歌曲 ───├─ structure.py        allin1：段落、节拍、小节线
        ├─ music.py            选段，修正副歌（铺垫后面更响的那段才是副歌），定小节切点
        │
        └─ cut.py              第一步（Claude）写大纲 → 第二步（Claude）挑节拍 → 按小节填槽 → 渲染配乐成片
```

| 模块 | 做什么 |
|---|---|
| `cards.py` | 2 秒窗口与卡片平均画面的相似度低于 0.86 时另起一张卡片；画面变化处取关键帧（3–20 张）；调用 4B 描述 |
| `tag_beats.py` | 发给 4B 的描述请求：每张关键帧一个节拍（景别、看得见的主体和动作），然后是过程、概括和场景；规划只读这几项（2026-11-13 起不再要情绪、标签、地标、室内外、时段、画面问题、精彩度和亮点，见[精彩度试验](../../../docs/research/video-montage-highlight.md#描述里没被用到的字段)） |
| `quality.py` | 模糊（FFmpeg blurdetect）、抖动（相位相关，扣掉平滑运镜）、遮挡（从底部伸进来的大块虚焦）、夜景失焦（全分辨率下的锐利亮点数） |
| `held.py` | 填槽时截掉有人从镜头前走过的时段 |
| `structure.py` | allin1 分析歌曲结构（单独的虚拟环境） |
| `music.py` | 选段与切点：副歌每小节一个镜头，其余每两小节一个；allin1 标成副歌、但下一段响 2 dB 以上的，判为铺垫 |
| `cut.py` | 两步规划、填槽、补位、渲染 |
| `ai/montage/render.py` | FFmpeg 渲染：1920×1080、30 帧；不是 16:9 的画面放在自身的模糊背景上；结尾淡出 |
| `windows.py` | 读视频索引（窗口与画面向量） |
| `tools/orient.py` | 带 180° 旋转标记的视频，让 4B 判断方向 |
| `tools/compare_page.py` | 几条成片同步对比播放的网页 |
| `transition/` | 转场时刻的规则检测（甩镜头、遮挡、推拉、转向天空或地面）和盲标页：`detect.py` 算信号和候选，`build.py` 剪 3 秒小片并混入随机对照，`server.py` 提供页面并保存标注 |
| `tools/log_page.py` | 剪辑日志页：每条成片带音乐段落、第一步大纲、逐个镜头（关键帧、节拍、第一步要的画面、第二步的意图）和两步的完整输入 |
| `prototype/` | 第七版：挑一版与精修的网页原型（`build.py` 生成数据、`search.py` 向量搜索、`index.html`），见[剪辑第七版](../../../docs/research/video-montage-v7.md) |
| `highlight/` | 精彩度标注：品杰每个节拍由人标“精彩、可用、平淡、有问题”，用来试精彩度对规划有没有帮助（`build.py` 生成数据、`server.py` 保存标注、`index.html`），见下面的“精彩度标注”和[精彩度试验](../../../docs/research/video-montage-highlight.md) |

## 环境

| 依赖 | 用途 | 版本（开发机） |
|---|---|---|
| Python | 主流程 | 3.13，NumPy、Pillow、Requests（`ai/` 的开发环境里都有） |
| FFmpeg、ffprobe | 取帧、画质、渲染 | 9.0 |
| llama.cpp `llama-server` | 运行 Qwen3.5-4B（带视觉投影） | b11960 |
| Qwen3.5-4B Q4_K_M + mmproj-F16 | 卡片描述、方向判断 | — |
| Claude Code CLI（已登录） | 两步规划，`claude -p` 调用 Opus | 2.1 |
| librosa | `music.py`，单独的虚拟环境 | 1.0 |
| allin1 | `structure.py`，单独的虚拟环境 | 1.1（torch 2.4.1、natten 0.17.1、demucs 4.1.0、madmom 从源码装） |
| A-NAS 视频索引 | 2 秒窗口与 EmbeddingGemma 2 画面向量 | 用 A-NAS 的 `eval/video_moments.py` 生成 |

启动 4B：

```bash
llama-server -m Qwen3.5-4B-Q4_K_M.gguf --mmproj mmproj-F16.gguf --host 127.0.0.1 --port 8094 -np 2 -c 16384
```

### 数据目录

代码不带数据。数据位置在 `montage/paths.py` 里，可以用环境变量改：

| 变量 | 内容 |
|---|---|
| `ANAS_MONTAGE_VIDEOS` | 原始视频，每组一个文件夹 |
| `ANAS_MONTAGE_INDEXES` | 每组的视频索引 `video-moments-<组名>/embeddinggemma*/{index.json,frames.npz}` |
| `ANAS_MONTAGE_CARDS` | 卡片与关键帧，`quality-<组>.json`，以及人工标记：`excluded.json`（误拍时段）、`rotation.json`（方向要翻转的视频）、`reviewed.json`（画质规则误判、人工复核保留的卡片） |
| `ANAS_MONTAGE_CUTS` | 歌曲结构与切点、每次规划和剪辑的记录、成片 |

## 用法

以品杰的新西兰素材（`pj`）配 Forester 的 *Symphony* 为例：

```bash
python cards.py frames pj                              # 切卡片、取关键帧
python quality.py pj                                   # 量画质
python cards.py describe http://127.0.0.1:8094 pj      # 4B 写描述
python tools/orient.py http://127.0.0.1:8094 pinjie # 可选：检查旋转标记
```

分析歌曲（在各自的虚拟环境里）：

```bash
python structure.py ~/Music/Forester-Symphony.mp3
```

```bash
python music.py ~/Music/Forester-Symphony.mp3
```

剪辑。每一步的结果按组名和 `CUT_TAG` 保存，已有的步骤直接复用：

```bash
CUT_MUSIC=music-Forester-Symphony.json CUT_TAG=-free-1 python cut.py pj
```

默认不要求拍摄时间顺序：每张卡片标上当地的拍摄日期和钟点，第一步只要求白天和夜里的画面不来回穿插。按拍摄时间顺序剪（`CUT_ORDER=time`）：

```bash
CUT_ORDER=time CUT_MUSIC=music-Forester-Symphony.json CUT_TAG=-time-1 python cut.py pj
```

换一条用户指令（`CUT_REQUEST`，替换 `cut.CASES` 里每组的指令；顺序开关 `CUT_ORDER` 仍作用于第一步）：

```bash
CUT_REQUEST="剪辑一个有叙事性的高光集合" CUT_ORDER=free CUT_MUSIC=music-Forester-Symphony.json CUT_TAG=-story-1 python cut.py pj
```

对比几条成片：

```bash
python tools/compare_page.py 品杰对比.html pj music-Forester-Symphony.json "品杰 × Symphony" -time-1=按时间顺序 -free-1=不要求顺序
```

剪辑日志页（卡片从 `paths.CARDS` 读，换过描述时同样设 `ANAS_MONTAGE_CARDS`）：

```bash
python tools/log_page.py 品杰剪辑日志.html pj "品杰 × Symphony" -time-1=按时间顺序 -free-1=不要求顺序
```

同一页放另一组卡片的成片（比如拼接前后），写成 `组名:标签`，那组卡片的文件夹放进 `LOG_CARDS`；拼接版的镜头会标出来自哪条原片、原片里第几秒：

```bash
ANAS_MONTAGE_CARDS=~/A-NAS-datasets/剪辑对比/diving LOG_CARDS=~/A-NAS-datasets/剪辑对比/divingj \
  python tools/log_page.py 潜水拼接对比.html dv "潜水：拼接前后" -alldays-4=不拼接 dj:-alldays-1=拼接
```

新加一组素材：在 `cut.py` 的 `CASES` 里加一行（组名、索引名、剪辑要求、目标秒数、体裁、文件名），然后按上面的顺序跑。

## 精彩度标注

试验人工标的精彩度对规划有没有帮助：先由人给品杰的 432 个节拍（第二步可选的全部节拍，116 张可用卡片）逐个标档，再拿标注加进两步规划，和不加的剪法对比。

| 档 | 含义 |
|---|---|
| 3 精彩 | 值得放在开场或副歌 |
| 2 可用 | 好看，但不突出 |
| 1 平淡 | 交代、过渡或普通记录 |
| X 有问题 | 遮挡、模糊、抖动等，不该用 |

页面按拍摄时间列出卡片和节拍，左边循环播放当前节拍（短于 1.5 秒的播到 1.5 秒，`C` 换成播整张卡片），显示景别、4B 的节拍描述和整张卡片的过程；不显示 4B 打的精彩度和亮点，免得先入为主。`rotation.json` 里旋转标记写错的视频（品杰 085）在页面里翻正。

标注由 `server.py` 存在站点文件夹的 `labels-pj.json`（每次整份替换，上一份留作 `.bak`），键是 `<卡片>/<节拍>`，和 `shots-*.json` 里的节拍编号相同；浏览器本地也存一份，保存服务没开时页面会提示，下次连上时补存。

生成数据（站点文件夹在仓库外，`ANAS_MONTAGE_HIGHLIGHT`，默认 `~/A-NAS-datasets/剪辑对比/精彩度标注（2026-11-13）/`）：

```bash
cd highlight && python3 build.py
```

保存服务（anas 虚拟机，端口 8768 只对本机开放）：

```bash
R="$HOME/code/A-NAS/.claude/worktrees/montage-highlight"; P="$HOME/A-NAS-datasets/剪辑对比/精彩度标注（2026-11-13）"
docker --context colima-anas run -d --name highlight-labels -p 127.0.0.1:8768:8768 \
  -v "$R/ai/eval/montage_v5/highlight:/app:ro" -v "$P:/site" -e PYTHONDONTWRITEBYTECODE=1 anas-ai-dev:trixie \
  /opt/anas-ai/bin/python /app/server.py --labels /site/labels-pj.json --port 8768
```

页面（端口 8767，只对本机开放；`--proxy` 把 `labels` 转给保存服务），打开 <http://127.0.0.1:8767>：

```bash
cd "$HOME/A-NAS-datasets/剪辑对比/精彩度标注（2026-11-13）" && npx --yes http-server@14.1.1 -p 8767 -a 127.0.0.1 -c-1 --proxy http://127.0.0.1:8768
```

停止：`docker --context colima-anas rm -f highlight-labels`，页面服务按 Ctrl+C。

带标注剪辑（`CUT_LABELS`，其余开关照旧；`CUT_WIDE=1` 只用横拍卡片），再统计各剪法用了多少精彩、平淡、有问题的节拍，并写一个按标注给镜头着色的对比页（在 `paths.CUTS` 里，和成片放在一起）：

```bash
CUT_WIDE=1 CUT_LABELS="$C/labels-pj.json" CUT_ORDER=free CUT_MUSIC=music-Forester-Symphony.json CUT_TAG=-symphony-label-1 python3 cut.py pj
```

```bash
cd highlight && python3 report.py labels-pj.json 精彩度对比.html "精彩度试验：品杰 × Symphony" 对照:-symphony-wide-1=wide-1 加精彩度:-symphony-label-1=label-1
```

`$C` 是 `paths.CUTS`；`report.py` 的标注路径相对它。

模型打的精彩度和人标比：`rate_4b.py` 让 4B 只按四档给每个节拍打分（只发关键帧，要先启动 4B），`agree.py` 算一致程度（要 SciPy 与 scikit-learn，在 anas 虚拟机的 `anas-ai-dev:trixie` 镜像里跑）：

```bash
cd highlight && python3 rate_4b.py http://127.0.0.1:8094 "$P/4b-pj.jsonl"
```

```bash
docker --context colima-anas run --rm -v "$R/ai/eval/montage_v5/highlight:/app:ro" -v "$P:/site" -v "$HOME/A-NAS-datasets/剪辑对比/第五版:/cards:ro" \
  anas-ai-dev:trixie /opt/anas-ai/bin/python /app/agree.py /site/labels-pj.json /site/4b-pj.jsonl /cards/cards-pj.json /site/agree-pj.json
```

结果见[精彩度试验](../../../docs/research/video-montage-highlight.md)。

## 人工复核

有三类素材，规则判断不了，要人看过再定：

- **旋转标记**：`orient.py` 判断不出方向的视频会整段记进 `excluded.json`。人看截图后，正的就从 `excluded.json` 删掉，倒的写进 `rotation.json`（渲染时翻正）。
- **画质误判**：遮挡规则分不出“平静的海面、雪地”和“贴着镜头的手指”。人看过认为能用的卡片写进 `reviewed.json`，再运行一次 `cards.py describe`。
- **误拍时段**：手指挡镜头、黑屏这类时段，写进 `excluded.json`。

## 检查

在 `ai/` 下运行：`python -m unittest tests.test_montage tests.test_montage_v5`，Ruff 见 `make ai-lint`。
