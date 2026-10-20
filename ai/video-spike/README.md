# video-spike：EmbeddingGemma 2 视频片段编码方式对比

Personal Agent 本地 demo 第 1 项（视频粗筛）的第一个实验。和相册的评估代码（`ai/eval`，走 MediaPipe 的 LiteRT 路线）分开：这里走 transformers 加 GPU，只复用相册的预处理规格。

## 要回答的问题

一个视频片段怎么变成可检索的向量，几种做法哪种在"一句话找片段，前 5 条有命中"上最好，各花多少时间：

| 方法 | 做法 | 每段存几个向量 | 需要 |
|---|---|---|---|
| native | 原生视频模式，整段抽帧一次送模型，模型内部加时间戳 | 1 | |
| mean | 每帧编成图片向量，取平均 | 1 | |
| maxsim | 保留每帧向量，查询和每帧比取最大 | 帧数 | |
| nativetext | 原生视频模式，文字描述和画面交错输入 | 1 | `--captions` |

四种方法看的是同一批抽帧（ffmpeg 按 fps 抽，缓存在 `out/frames/`），差异只来自编码和打分方式。mean 和 maxsim 共用帧向量，编码耗时相同。

抽帧按相册缩略图 `thumbnail/v1` 的规格对齐：长边 512 px、EXIF 转正、透明合成白底。预处理不一样，向量就对不上相册里的；`--edge` 可以改着试。

## 准备

1. 装 ffmpeg 并加进 PATH。
2. 装依赖（见 `requirements.txt` 里关于 5090 和 Spark 的说明）。
3. `huggingface-cli login`，EmbeddingGemma 2 的权重要先在 Hugging Face 上接受 Gemma 条款。
4. 准备片段：一个目录，每个视频文件是一个片段。几秒到一分钟都行，默认最多取 32 帧。没有 ffmpeg 的机器可以把每段预先抽成一个帧目录放进来。
5. 生成标注模板再填：

   ```bash
   python embed_bench.py --clips D:\素材\片段 --make-template queries.json
   ```

   模板末尾列了全部片段 ID，把它们填进各条查询的 `relevant`。先写 10 到 20 条，每条标 1 到 3 个片段；`relevant` 留空的查询只列 top-5 不算命中率。格式见 `queries.example.json`。
6. 可选：给片段写一句描述，格式见 `captions.example.json`，有它才跑 nativetext。

## 运行

```bash
python embed_bench.py --clips D:\素材\片段 --queries queries.json --out out
```

| 参数 | 默认 | 说明 |
|---|---|---|
| `--fps` | 1 | 抽帧率，官方视频模式默认 1 fps |
| `--max-frames` | 32 | 每段最多几帧 |
| `--edge` | 512 | 帧长边像素，对齐相册缩略图 |
| `--image-tokens` | 140 | 单帧 token 预算，70/140/280/560/1120；原生视频模式每帧固定 140 |
| `--truncate-dim` | 768 | Matryoshka 截断，可试 512 |
| `--methods` | 全部 | 逗号分隔；nativetext 没有描述时自动跳过 |
| `--captions` | 无 | 片段描述 JSON |
| `--native-from-file` | 关 | 原生模式让模型自己解码视频文件，而不是用同一批抽帧 |
| `--reuse` | 关 | 复用 `out/embeddings.npz`，只重算指标（改了标注后用） |
| `--fake` | 关 | 不加载模型，哈希向量，验证流程 |

输出在 `out/`：`report.md`（耗时表、命中率表、每条查询的前 5，命中的加粗）、`report.json`、`embeddings.npz`。耗时表里的"一小时素材估算"按抽帧数折算素材时长，是估算。模型加载和一次预热不计入编码耗时。

## 建议的实验顺序

1. 默认参数跑一遍，看四种方法的 Hit@5 和每段耗时。
2. `--image-tokens 70` 再跑，看质量掉多少、快多少。相册在 CPU 上用的就是 70。
3. `--truncate-dim 512` 看存储减一半的代价。
4. `--native-from-file` 和默认对比，看模型自己解码和我们抽帧有没有差别。
5. 耗时表里"一小时素材估算"那一列，就是需求里要记的"一小时素材全量打标耗时"的向量部分。

## 测试

```bash
python -m unittest test_embed_bench -v
```

不加载模型、不用 ffmpeg：打分、指标、预处理各自有测试，再用合成帧目录跑一遍 fake 流程（模板、四种方法、缓存复用、无描述时跳过 nativetext）。

## 已知的不确定点

- sentence-transformers 对 EmbeddingGemma 2 的多模态接口（`processing_kwargs` 里 `image.max_soft_tokens`、`video.max_frames`、`video.fps`、把 PIL 帧列表当 video 传、`<|video|>` 占位符）是按官方文档写的，还没在真机上跑过。库不接受 `processing_kwargs` 时脚本会去掉它再试一次，并把降级写进报告的"运行中的降级"一节；别的参数错误要对照 https://ai.google.dev/gemma/docs/embeddinggemma/multimodal-embeddinggemma-with-sentence-transformers 改。
- 模型只能 bf16 或 fp32，脚本固定 bf16。
- 写脚本的 Windows 机器没有 ffmpeg 也没有 GPU，只跑过单元测试和 `--fake` 干跑。
