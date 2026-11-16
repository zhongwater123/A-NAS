# 视频剪辑第八版

第六版流水线（`ai/eval/montage_v5/`）按分层重构，业务层能原样跑在开发机、x86 NAS 和 RK3588 上。设计见 [分层重构与 RK3588 迁移方案](../../docs/specs/video-montage-architecture.md)。剪法本身不变：同样的描述和两步规划，第八版和第六版剪出的镜头表逐个相同（见下面的“和第六版比对”）。

## 分层

| 层 | 位置 | 内容 | 依赖 |
|---|---|---|---|
| 领域层 | `domain/` | 卡片、画质、音乐槽、两步规划的请求和回答、填槽、画面分析、提示词 | 只用标准库和 NumPy |
| 端口 | `ports.py` | 取帧、视觉描述、规划、音乐分析、渲染的接口 | |
| 适配层 | `adapters/` | FFmpeg、OpenAI 兼容的视觉服务、Claude（命令行或 API）、allin1 或预算好的音乐、文件存储 | 按平台 |
| 编排层 | `pipeline.py` | 切卡片 → 量画质 → 描述 → 第一步 → 第二步 → 填槽 → 渲染，结果可复用 | 领域层和端口 |
| 配置 | `profiles/` | `mac-dev`、`x86-nas`、`rk3588` 各一份；`cases.toml` 是各组素材 | |
| 入口 | `__main__.py`、`doctor.py`、`golden.py` | 命令行、平台自检、和第六版比对 | |

领域层不许做输入输出：Ruff 的 TID251 禁止它引用 `subprocess`、`os`、`requests`、`PIL` 等（`ai/ruff.toml`）；`tests/test_montage_v8.py` 还检查它只引用标准库、NumPy 和它自己。

| 模块 | 来自第六版 |
|---|---|
| `domain/cards.py` | `cards.py` 的切卡片、取关键帧；`windows.py` |
| `domain/quality.py` | `cut.poor` 和阈值 |
| `domain/picture.py` | `held.py`、`quality.py` 里对灰度帧的计算 |
| `domain/music.py` | `music.py` 的选段和槽、`cut.music_note`、`cut.slotted`（响度不再用 librosa，`adapters/music.py` 按同一算法自己算） |
| `domain/prompts.py` | `cut.py`、`tag_beats.py` 的提示词原文 |
| `domain/plan.py` | `cut.pick`、`cut.shots` 拼请求和解析回答；`cut.units` |
| `domain/fill.py` | `cut.cut_to_music` 的填槽、`cut.breaks_run` |
| `domain/describe.py` | `tag_beats.describe_v2` 拼请求和检查回答 |
| `adapters/ffmpeg.py` | `montage/render.py` 和各处的 FFmpeg 调用，硬件解码和编码器由配置给 |
| `adapters/planner.py` | `cut.ask_claude`；另加 Claude API |

结果文件的格式和第六版相同（`cards-*.json`、`pick-*.json`、`shots-*.json`、`cut-*.json`、`held-*.json`），旧结果能直接复用，`eval/montage_v5/tools/log_page.py` 等网页工具照样能读。成片文件名里是“第八版”。

## 运行

在 `ai/` 目录下：

```bash
python3 -m montage_v8 --profile mac-dev cut pj --music Forester-Symphony --tag=-v8-1 --free --wide
```

```bash
python3 -m montage_v8 --profile mac-dev describe pj
```

`--profile` 默认取环境变量 `MONTAGE_PROFILE`，没有就是 `mac-dev`。数据目录可以用 `ANAS_MONTAGE_CARDS` 等环境变量改（和第六版相同），比如换成精简描述的卡片目录。`--tag` 以 `-` 开头时写成 `--tag=-v8-1`。

和第六版比对（开发机，要有素材数据）：

```bash
python3 -m montage_v8 golden pj --tag=-symphony-label-1 --music Forester-Symphony
```

2026-11-16 比对结果：
- **填槽**：品杰 6 次剪辑（精简描述 3 次，带精彩度标注、只用横拍、同一地点连续各 1 次）、新西兰 2 次、川西 1 次，镜头表逐个相同。
- **请求**：两步规划的 system、user、schema 和第六版当前代码逐字相同（四种剪法）；卡片描述的请求也相同。
- **音乐槽**：三首歌用自己算的响度重算，和原来的 `music-*.json` 相同。
- **成片**：用已有规划重新渲染品杰一条，抽 5 帧和第六版成片像素相同，耗时 62 秒。

## 板子到了之后（RK3588）

配置在 `profiles/rk3588.toml`，标了“待测”的都要在真机上确认。按顺序：

1. **装依赖**：Python 3.11 以上、`numpy`、`requests`、`Pillow`、`anthropic`。FFmpeg 要支持 Rockchip MPP，例如 ffmpeg-rockchip 分支。
2. **自检**：`python3 -m montage_v8 --profile rk3588 check`。它会检查这些，不调用规划模型：
   - `rkmpp` 硬件解码和 `h264_rkmpp` 编码器在不在；
   - 解码一段 4K 测试画面、编码一个镜头；
   - 用硬件解码按区间取灰度帧；
   - 视觉模型服务和 `ANTHROPIC_API_KEY`。
3. **视频**：如果 `h264_rkmpp` 的码率参数不对，或滤镜和硬件帧配不上，只改 `[ffmpeg]` 的 `decode`、`encode`。代码里不写平台判断。
4. **拷数据**：从开发机拷一组素材的数据：原片、窗口索引、卡片（含 `frames/`）、`music-*.json`、`held-*.json`、已有的 `pick`/`shots`。先 `cut --no-render` 对镜头表，再渲染，和开发机的成片比对。
5. **视觉模型**：
   - 先在板子上起 llama-server（CPU，同一个 Qwen3.5-4B），`describe` 跑几张卡片，记下每张的秒数；
   - 再试 RKLLM（NPU）：需要在板子上起一个 OpenAI 兼容的服务包住它，配置里把 `schema_mode` 改成 `"prompt"`；
   - 两者都用 115 个人工核对过景别的节拍比质量（[精彩度与描述字段](../../docs/research/video-montage-highlight.md)）。
6. **记录**：把实测的速度、内存和选定的参数写回 `profiles/rk3588.toml` 和方案文档。

## 关联

- [分层重构与 RK3588 迁移方案](../../docs/specs/video-montage-architecture.md)
- 第六版：[`ai/eval/montage_v5/`](../eval/montage_v5/README.md)、[第六版调研](../../docs/research/video-montage-v6.md)
- 测试：[`tests/test_montage_v8.py`](../tests/test_montage_v8.py)
