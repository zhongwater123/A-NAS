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
| 入口层 | `service/`、`web/`、`__main__.py`、`doctor.py`、`golden.py` | 网页服务和网页（上传）、命令行、平台自检、和第六版比对 | 编排层；网页服务另要 `requirements-web.txt` |

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

## 网页与上传

`service/` 是入口层：一个网页服务（Starlette + Uvicorn），提供第七版的网页（页面已移到 [`web/index.html`](web/index.html)），并接收浏览器上传的视频。剪辑还没接到网页上，三版成片仍是第七版的示例。

- **上传**：网页“添加视频”或把视频、文件夹拖进素材框后，逐个上传。
  - 存放位置：这台机器的 `<原片目录>/uploads/<文件夹名>/<文件名>`。原片目录来自平台配置的 `paths.videos`，Mac 上是 `~/A-NAS-datasets/videos/uploads/`。
  - 传输方式：请求体就是文件本身，边收边写到隐藏的临时文件，收全了才改成正式名字。中途断了不会留下半个文件。
  - 文件时间：保留文件原来的修改时间。
  - 重名：同名、同大小的不再存一次；同名、不同大小的另起名字。
  - 限制：只收 MOV、MP4、M4V、MTS、AVI、MKV，文件名和文件夹名去掉路径符号，写不到 uploads 以外；存完磁盘至少要留 1 GiB。
- **已上传的视频**：打开网页时按文件夹列出，标着“已上传到这台机器”，可以播放和拖动。
- **还不能续传**：传了一半断掉要整个重传。要支持大文件续传，换成 tus 协议（A-NAS 的上传已经在用）。
- **没有登录**：服务默认只对本机开放。用 `--host 0.0.0.0` 对局域网开放后，同一局域网里任何人都能上传，只在可信的网络里这样用。

装依赖（只需一次，开发机和板子相同）：

```bash
python3 -m venv --system-site-packages ~/.venvs/montage-v8 && ~/.venvs/montage-v8/bin/pip install -r montage_v8/requirements-web.txt
```

启动（在 `ai/` 目录下）。`--site` 是第七版 `build.py` 搭的网站目录，里面有示例成片和关键帧；`--proxy` 是第七版的向量搜索服务，没开就不填，只有搜索不能用：

```bash
~/.venvs/montage-v8/bin/python -m montage_v8 serve --site "$HOME/A-NAS-datasets/剪辑对比/剪辑原型（2026-11-11）" --host 0.0.0.0 --port 8765 --proxy http://127.0.0.1:8766
```

别的电脑打开 `http://<这台机器的局域网地址>:8765/`。板子上加 `--profile rk3588`，上传就存到板子的原片目录。

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
