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
| `tag_beats.py` | 发给 4B 的描述请求：每张关键帧一个节拍（景别、看得见的主体和动作），1–2 个亮点、过程、概括与场景等字段；精彩度按概率加权 |
| `quality.py` | 模糊（FFmpeg blurdetect）、抖动（相位相关，扣掉平滑运镜）、遮挡（从底部伸进来的大块虚焦）、夜景失焦（全分辨率下的锐利亮点数） |
| `held.py` | 填槽时截掉有人从镜头前走过的时段 |
| `structure.py` | allin1 分析歌曲结构（单独的虚拟环境） |
| `music.py` | 选段与切点：副歌每小节一个镜头，其余每两小节一个；allin1 标成副歌、但下一段响 2 dB 以上的，判为铺垫 |
| `cut.py` | 两步规划、填槽、补位、渲染 |
| `ai/montage/render.py` | FFmpeg 渲染：1920×1080、30 帧；不是 16:9 的画面放在自身的模糊背景上；结尾淡出 |
| `windows.py` | 读视频索引（窗口与画面向量） |
| `tools/orient.py` | 带 180° 旋转标记的视频，让 4B 判断方向 |
| `tools/compare_page.py` | 几条成片同步对比播放的网页 |
| `prototype/` | 第七版：挑一版与精修的网页原型（`build.py` 生成数据、`search.py` 向量搜索、`index.html`），见[剪辑第七版](../../../docs/research/video-montage-v7.md) |

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
CUT_MUSIC=music-Forester-Symphony.json CUT_TAG=-time-1 python cut.py pj
```

```bash
CUT_ORDER=free CUT_MUSIC=music-Forester-Symphony.json CUT_TAG=-free-1 python cut.py pj
```

对比几条成片：

```bash
python tools/compare_page.py 品杰对比.html pj music-Forester-Symphony.json "品杰 × Symphony" -time-1=按时间顺序 -free-1=不要求顺序
```

新加一组素材：在 `cut.py` 的 `CASES` 里加一行（组名、索引名、剪辑要求、目标秒数、体裁、文件名），然后按上面的顺序跑。

## 人工复核

有三类素材，规则判断不了，要人看过再定：

- **旋转标记**：`orient.py` 判断不出方向的视频会整段记进 `excluded.json`。人看截图后，正的就从 `excluded.json` 删掉，倒的写进 `rotation.json`（渲染时翻正）。
- **画质误判**：遮挡规则分不出“平静的海面、雪地”和“贴着镜头的手指”。人看过认为能用的卡片写进 `reviewed.json`，再运行一次 `cards.py describe`。
- **误拍时段**：手指挡镜头、黑屏这类时段，写进 `excluded.json`。

## 检查

在 `ai/` 下运行：`python -m unittest tests.test_montage tests.test_montage_v5`，Ruff 见 `make ai-lint`。
