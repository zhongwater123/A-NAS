# 视频剪辑第八版

第六版流水线按分层重构，业务层能原样跑在开发机、x86 NAS 和 RK3588 上。设计见 [分层重构与 RK3588 迁移方案](../../docs/specs/video-montage-architecture.md)。剪法本身不变：同样的描述和两步规划，第八版和第六版剪出的镜头表逐个相同（见下面的“和第六版的比对”）。第六版的实验脚本 `ai/eval/montage_v5/` 比对完后删除了，旧代码在 [e2285a30](https://github.com/zhongwater123/A-NAS/tree/e2285a30/ai/eval/montage_v5)。

## 分层

| 层 | 位置 | 内容 | 依赖 |
|---|---|---|---|
| 领域层 | `domain/` | 卡片、画质、音乐槽、两步规划的请求和回答、填槽、画面分析、提示词 | 只用标准库和 NumPy |
| 端口 | `ports.py` | 取帧、视觉描述、规划、音乐分析、渲染的接口 | |
| 适配层 | `adapters/` | FFmpeg、OpenAI 兼容的视觉服务、Claude（命令行或 API）、allin1 或预算好的音乐、文件存储 | 按平台 |
| 编排层 | `pipeline.py` | 切卡片 → 量画质 → 描述 → 第一步 → 第二步 → 填槽 → 渲染，结果可复用 | 领域层和端口 |
| 配置 | `profiles/` | `mac-dev`、`x86-nas`、`rk3588` 各一份；`cases.toml` 是各组素材 | |
| 入口层 | `service/`、`web/`、`__main__.py`、`doctor.py` | 网页服务和网页（上传、转发搜索）、向量搜索服务、命令行、平台自检 | 编排层；网页服务另要 `requirements-web.txt` |
| 工具 | `tools/` | 第七版网页的数据、剪辑日志页、多版对比页、旋转检查、精彩度试验 | 编排层和存储 |

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

结果文件的格式和第六版相同（`cards-*.json`、`pick-*.json`、`shots-*.json`、`cut-*.json`、`held-*.json`），旧结果能直接复用，`tools/` 里的网页工具照样能读。成片文件名里是“第八版”。

## 运行

在 `ai/` 目录下：

```bash
python3 -m montage_v8 --profile mac-dev cut pj --music Forester-Symphony --tag=-v8-1 --free --wide
```

```bash
python3 -m montage_v8 --profile mac-dev describe pj
```

`--profile` 默认取环境变量 `MONTAGE_PROFILE`，没有就是 `mac-dev`。数据目录可以用 `ANAS_MONTAGE_CARDS` 等环境变量改（和第六版相同），比如换成精简描述的卡片目录。`--tag` 以 `-` 开头时写成 `--tag=-v8-1`。

## 和第六版的比对

2026-11-16 用当时的 `golden.py`（随第六版一起删了，见 [e2285a30](https://github.com/zhongwater123/A-NAS/blob/e2285a30/ai/montage_v8/golden.py)）比对：
- **填槽**：品杰 6 次剪辑（精简描述 3 次，带精彩度标注、只用横拍、同一地点连续各 1 次）、新西兰 2 次、川西 1 次，镜头表逐个相同。
- **请求**：两步规划的 system、user、schema 和第六版当前代码逐字相同（四种剪法）；卡片描述的请求也相同。
- **音乐槽**：三首歌用自己算的响度重算，和原来的 `music-*.json` 相同。
- **成片**：用已有规划重新渲染品杰一条，抽 5 帧和第六版成片像素相同，耗时 62 秒。

## 网页、上传与剪辑任务

`service/` 是入口层：一个网页服务（Starlette + Uvicorn），提供第七版的网页（页面已移到 [`web/index.html`](web/index.html)），接收浏览器上传的视频，并经任务层（`jobs.py`）真的剪辑。

- **上传**：网页“添加视频”或把视频、文件夹拖进素材框后，逐个上传。
  - 存放位置：这台机器的 `<原片目录>/uploads/<文件夹名>/<文件名>`。原片目录来自平台配置的 `paths.videos`，Mac 上是 `~/A-NAS-datasets/videos/uploads/`。
  - 传输方式：请求体就是文件本身，边收边写到隐藏的临时文件，收全了才改成正式名字。中途断了不会留下半个文件。
  - 文件时间：保留文件原来的修改时间。
  - 重名：同名、同大小的不再存一次；同名、不同大小的另起名字。
  - 限制：只收 MOV、MP4、M4V、MTS、AVI、MKV，文件名和文件夹名去掉路径符号，写不到 uploads 以外；存完磁盘至少要留 1 GiB。
- **已上传的视频**：打开网页时按文件夹列出，标着“已上传到这台机器”，可以播放和拖动。
- **还不能续传**：传了一半断掉要整个重传。要支持大文件续传，换成 tus 协议（A-NAS 的上传已经在用）。
- **剪辑任务**（`jobs.py`，设计依据见[常见后端项目的做法](../../docs/research/backend-practices.md)）：
  - **发起**：网页“开始剪辑”建一个任务：勾选的那个 NAS 文件夹里勾选的视频，配乐页选的歌，剪辑要求页的顺序和补充要求，剪三版。
  - **执行**：任务存在 SQLite（`<原片目录旁>/montage-jobs/jobs.sqlite`），一个后台 Worker 一次执行一个。步骤是：检查素材、补写描述（已有的跳过）、读歌曲段落，然后每版规划一次、渲染一次。
  - **进度**：网页每 1.5 秒查一次，窗口里显示当前步骤、正在看的关键帧，排队时显示前面还有几个。
  - **结果**：剪完后，挑选页和精修页换成这次的成片。
  - **取消**：排队中的直接取消；执行中的在下一步之前停，不打断正在进行的模型调用或渲染。
  - **服务重启**：正在执行的任务回到排队，再执行时复用已做完的步骤，不会重复花钱。
  - **开销**：每版规划约 0.4 美元（Claude），三版约 1.3 美元；品杰一组每版规划加渲染约 3 分钟。
  - **上传的文件夹**也能剪：任务先给视频建窗口索引，再切卡片、检查画质，然后接着描述和规划，详见下面的“建索引”。
  - **限制**：一次只剪一个文件夹；曲库里只有分析过段落的歌能选。
  - **接口**：`POST /api/jobs`、`GET /api/jobs/<id>`、`POST /api/jobs/<id>/cancel`、`GET /api/songs`；关键帧、成片、原片、歌在 `/media/` 下。
- **建索引**（上传的视频，`pipeline.index`、[`domain/windows.py`](domain/windows.py)）：
  - **窗口**：和 A-NAS 视频索引器的 2s1f 布局相同，每 2 秒一个窗口，取窗口正中一帧，长边缩到 512、JPEG 质量 82。
  - **编码**：用 EmbeddingGemma 2 编码，灰度标准差小于 8 的帧算纯色。
  - **只建剪辑要用的**：镜头切点和标签不建。
  - **存放**：索引写在 `<索引目录>/video-moments-upload-<键>/<模型>/`。键是 `u` 加文件夹名摘要，卡片编号也用它，例如 `u5c450a-001`。
  - **视频编号**：按修改时间编成 001、002……；以后往文件夹里加视频，已有的编号不变，已编码的帧也不再编码。
  - **编码服务**：编码在 `service/search.py` 里做。它要 MediaPipe，开发机上只能在 anas 虚拟机里跑；不带 `--beats` 时只做编码，配置里是 `[embedder]`。
  - **向量差异**：开发机的 FFmpeg 取出的帧和虚拟机里的有细微差别，同一帧的向量余弦约 0.97–0.996。上传的视频只和自己的索引比，不影响剪辑。

  启动编码服务（端口 8766，只对本机开放）：

  ```bash
  R="$HOME/code/A-NAS/.claude/worktrees/montage-v8"; docker --context colima-anas run -d --name v8-embed -p 127.0.0.1:8766:8766 -v "$R/ai:/src/ai:ro" -v "$R/deploy/models:/manifest:ro" -v "$HOME/A-NAS-models/embeddinggemma-2-740m:/models:ro" -v "$HOME/A-NAS-datasets/cache/xnnpack:/xnnpack" -w /src/ai -e PYTHONPATH=/src/ai anas-ai-dev:trixie /opt/anas-ai/bin/python /src/ai/montage_v8/service/search.py --model /models/embeddinggemma-2-740m.litertlm --manifest /manifest/embeddinggemma-2-740m.json --weight-cache /xnnpack
  ```

  2026-11-16 实测：上传品杰 11 段、约 6 分钟的视频，任务建索引、切出 13 张卡片、检查画质、本机 4B 描述，规划和渲染一版。成片 95.2 秒、29 个镜头，规划 0.27 美元。
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

## 工具

在 `ai/` 目录下运行，数据目录取自 `MONTAGE_PROFILE` 的配置。2026-11-16 从第六版迁来，迁移前后生成的网页数据、标注页数据、日志页、对比页和精彩度统计逐字节相同。

| 命令 | 做什么 |
|---|---|
| `python3 -m montage_v8.tools.site` | 第七版网页的数据（`data.js`、`beats.npz`）和链接，写进网站目录（`ANAS_MONTAGE_PROTO`） |
| `service/search.py` | 网页的“搜索画面”、相似镜头和确认记录；要 MediaPipe，在 anas 虚拟机的 `anas-ai-dev:trixie` 里跑，命令见[第七版](../../docs/research/video-montage-v7.md) |
| `python3 -m montage_v8.tools.log_page 日志.html pj "标题" -tag=名字 ...` | 剪辑日志页：成片、大纲、逐个镜头、两步的完整输入 |
| `python3 -m montage_v8.tools.compare_page 对比.html pj music-歌名.json "标题" -tag=名字 ...` | 几条成片同步播放对比 |
| `python3 -m montage_v8.tools.orient http://127.0.0.1:8094 pinjie` | 检查旋转标记（`--write` 才写 `rotation.json`、`excluded.json`） |

**精彩度试验**（`tools/highlight/`，结果见[精彩度试验](../../docs/research/video-montage-highlight.md)）：

- `python3 -m montage_v8.tools.highlight.build`：生成标注网页的数据，网站目录是 `ANAS_MONTAGE_HIGHLIGHT`。
- `server.py`：保存标注。
- `rate_4b`：让本机 4B 按四档打分，要先启动 4B。
- `agree.py`：算模型和人标的一致程度。要 SciPy 和 scikit-learn，在 `anas-ai-dev:trixie` 里跑，挂载路径换成 `ai/montage_v8/tools/highlight`。
- `report`：带标注剪辑的统计页。剪辑命令加 `--labels labels-pj.json --wide`。

**人工复核**：规则判断不了的素材要人看过再定。
- `orient` 判断不出方向的视频会整段记进 `excluded.json`：人看过后，正的删掉，倒的写进 `rotation.json`。
- 遮挡规则会把平静的海面、雪地误判成遮挡：人看过认为能用的卡片写进 `reviewed.json`，再跑一次 `describe`。

**新加一组素材**：在 `profiles/cases.toml` 里加一节，然后依次运行 `cards`、`quality`、`describe`、`cut`。窗口索引仍要先由 A-NAS 的视频索引器建好。

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
- 第六版：[第六版调研](../../docs/research/video-montage-v6.md)，代码在 [e2285a30](https://github.com/zhongwater123/A-NAS/tree/e2285a30/ai/eval/montage_v5)
- [第七版网页原型](../../docs/research/video-montage-v7.md)、[精彩度试验](../../docs/research/video-montage-highlight.md)
- 测试：[`tests/test_montage_v8.py`](../tests/test_montage_v8.py)
