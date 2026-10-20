# EmbeddingGemma 2 文本向量：任务前缀、社区微调与我们的微调流程

> 状态：脚本与离线测试完成；基座基线与微调待在 GPU 开发机上跑，微调权重转 LiteRT 未验证  
> 更新日期：2026-10-20  
> 适用范围：EmbeddingGemma 2 的文本编码器（270M），与相册 Worker 同一模型文件 `embeddinggemma-2-740m@e7a8a2204b91`；只讨论文本对文本的用法  
> 关联：[M2 实施方案](../architecture/photo-ai.md)、[模型与 Runtime 选型](photo-ai-model-runtime-selection.md)、[标签校准与中文检索评估](photo-ai-label-calibration.md)、[`ai/finetune/`](../../ai/finetune/)、[`ai/tests/test_finetune.py`](../../ai/tests/test_finetune.py)

相册已经把 EmbeddingGemma 2 当图文检索模型用（[`MediaPipeProvider`](../../ai/anas_ai/providers.py) 只暴露 `embed_image` 与带检索前缀的 `embed_query`）。本文记录同一个模型做**文本对文本**任务（句子相似、去重、按样例分类、文档检索）时要注意的事实，整理别人微调它的做法，并给出一套在开发机上评估和微调的脚本。设备上不训练（[项目上下文](../../PROJECT_CONTEXT.md)的非目标），微调只在开发机进行。

## 结论

1. **同一模型文件够用，关键是前缀。** EmbeddingGemma 2 是带任务前缀训练的，前缀不对相似度整体偏移。Worker 的 `embed_query` 固定用检索前缀；文本对文本要用 `task: sentence similarity | query: `（对称比对）或 `title: none | text: `（文档侧）。[`ProviderEmbedder`](../../ai/finetune/embedders.py) 走 `embed_text` 自己加前缀，和 Worker 同一条 MediaPipe 路径。
2. **微调有效，但别人都用了几万条数据。** 社区案例把专业领域 Top-1 从 58% 拉到 92%，数据量 6 万条起；我们现在没有几百条以上的三元组，先用基座跑基线、攒数据。
3. **微调产出不能直接上设备。** 训练用 Hugging Face 权重（sentence-transformers），Worker 加载的是 LiteRT 文件并按 [`deploy/models/embeddinggemma-2-740m.json`](../../deploy/models/embeddinggemma-2-740m.json) 校验哈希。HF → LiteRT 的转换路径没有验证过，是把微调结果交付到 NAS 的前提。

## 模型事实（文本部分）

| 项 | 值 | 对我们的意义 |
|---|---|---|
| 规模 | 740M 全模态；文本编码器 270M（130M transformer + 140M 词表），视觉 170M，音频 300M，可按需不加载 | 训练和评估只加载文本：`config_kwargs={"vision_config": None, "audio_config": None}`，CPU 可跑 |
| 向量 | 768 维，Matryoshka 可截到 512/256/128（截断后重新归一化） | 官方：256 维几乎无损（MTEB 多语 61.36 → 60.41）；相册仍用 768 作为质量基线 |
| 上下文 | 8192 token | 日志行、反馈正文远用不满 |
| 前缀 | 检索 query `task: search result \| query: `，文档 `title: none \| text: `，句子相似 `task: sentence similarity \| query: `，聚类 `task: clustering \| query: `，分类 `task: classification \| query: ` | 必须带；相册检索已证实带前缀更好（[标签校准](photo-ai-label-calibration.md)：R@1 71.3% 对 69.5%） |
| 精度 | 只能 bf16 / fp32，fp16 会出 NaN | 训练脚本固定 bf16 |
| 许可 | Apache 2.0；权重要在 Hugging Face 接受 Gemma 条款 | 与[模型清单](../../deploy/models/embeddinggemma-2-740m.json)里的许可备注一致 |
| 基准 | MTEB 多语 61.36、代码 78.68 | 中文文本对文本没有单独数字（见"风险"） |
| 运行方式 | Worker：MediaPipe + LiteRT；训练：sentence-transformers ≥ 6.1 / transformers ≥ 4.57；llama.cpp 的 GGUF 转换会丢掉 2_Dense/3_Dense 投影层 | 对比基座和微调结果时都走 sentence-transformers，避免运行时差异混进结论 |

## 别人做过的微调

Hugging Face 上 `google/embeddinggemma-2` 下有 119 个微调、`embeddinggemma-300m` 下更多。挑和我们相关的：

| 微调 | 数据 | 做法 | 结果 | 借鉴 |
|---|---|---|---|---|
| [sentence-transformers/embeddinggemma-300m-medical](https://huggingface.co/sentence-transformers/embeddinggemma-300m-medical)（HF 官方博客示例） | 10 万对医学问答 | CachedMultipleNegativesRankingLoss，batch 128，lr 2e-5，warmup 10%，1 epoch；3090 上 5.5 小时 | NDCG@10 0.834 → 0.886 | 配方的基线；Cached 版让小显存也能用大 batch |
| [ByronLeeee/EmbeddingGemma-300M-LawVault](https://huggingface.co/ByronLeeee/EmbeddingGemma-300M-LawVault)（中文法条） | 6.6 万条三元组，**难负例 = 基座模型排错的 top 结果**，查询由 LLM 改写成口语 | MatryoshkaLoss(MNRL)，同时训 768/512/256/128；batch 24×6 累积，3 epoch，bf16 | Top-1 58% → 92%，Hit@10 85% → 98% | 唯一的中文案例。两点最有用：难负例挖掘、Matryoshka 让缩短向量不掉分 |
| [mrm8488/embeddinggemma-2-ft-typed-decision](https://huggingface.co/mrm8488/embeddinggemma-2-ft-typed-decision)（最多赞） | typed-decisions 数据集：工作流状态 + 问题 → 选项 | 向量模型上加 0.5M 参数的头，两阶段：先冻结编码器训头，再端到端（编码器 2e-5、头 1e-4） | 准确率 67.7% → 76.4% | 把 embedding 当分类器用的范式；[`similarity.classify_by_examples`](../../ai/finetune/similarity.py) 是它的零训练版 |
| [官方教程](https://ai.google.dev/gemma/docs/embeddinggemma/fine-tuning-embeddinggemma-with-sentence-transformers) | 3 条玩具三元组 | MNRL，lr 2e-5，5 epoch | 正负例差距从 0.009 拉到 0.033 | 证明极小数据也会动，但只是把正负例拉开，不代表泛化 |
| 其他：葡萄牙语、北欧语、韩语、土耳其法律、越南语；LiteRT / MLX / unsloth 的转换版下载最多 | 各 1–30 万 | 基本都是 MNRL + 语言/领域对 | — | 多语言微调普遍有效；转换版说明端侧部署是主流用法 |

共同点：全是 sentence-transformers + (Cached)MultipleNegativesRankingLoss，lr 2e-5，数据量几万起。没有人用 LoRA（模型太小没必要）。

## 我们的脚本（[`ai/finetune/`](../../ai/finetune/)，只在开发机使用）

| 文件 | 作用 | 依赖 |
|---|---|---|
| `embedders.py` | 统一接口：`ProviderEmbedder`（Worker 的 MediaPipe 路径 + 自选前缀）、`HttpEmbedder`（OpenAI 兼容 `/v1/embeddings`）、`HashEmbedder`（字符 n-gram 哈希，无模型时的分数下限）、`CachedEmbedder`（按文本落盘） | 标准库 |
| `similarity.py` | `rank`（Top-K）、`find_duplicates`（近似重复分组）、`classify_by_examples`（按样例分类） | 标准库 |
| `server.py` | 把 HF 权重（基座或微调后）起成 `/v1/embeddings` 服务，只加载文本编码器；`--fake` 用哈希向量联调 | sentence-transformers（另一个 venv，见 `requirements.txt`） |
| `build_triplets.py` | 带标签文本（同标签互为正例）和标注对 → 三元组 JSONL；用当前模型挖难负例 | 标准库 |
| `eval.py` | 同一份三元组当检索任务：Hit@1 / Hit@5 / MRR@10 / 正负例相似度差；可评 `--model`（MediaPipe）、`--embed-url`（HF）或哈希 | 标准库（`--model` 需 Worker 运行环境） |
| `train.py` | LawVault 配方：MatryoshkaLoss(MNRL)，lr 2e-5，3 epoch，batch 32，bf16，只训文本编码器，留 15% 验证 | GPU + sentence-transformers |

流程（在 `ai/` 目录下）：

```bash
python -m finetune.build_triplets --labeled finetune/data/labeled.jsonl --pairs finetune/data/pairs.jsonl --out finetune/data/triplets.jsonl
python -m finetune.eval finetune/data/triplets.jsonl --model /models/embeddinggemma-2-740m.litertlm   # 基座，Worker 同一路径（开发镜像里跑）
python -m finetune.train finetune/data/triplets.jsonl --out finetune/out/my-embeddinggemma-2          # GPU 开发机
python -m finetune.server --model finetune/out/my-embeddinggemma-2
python -m finetune.eval finetune/data/triplets.jsonl --embed-url http://127.0.0.1:8089                 # 微调后
```

`finetune/data/`、`finetune/out/`、`finetune/cache/` 不进仓库。基座的对比口径：同一份三元组先用 `--embed-url` 指向 `server.py` 加载的 `google/embeddinggemma-2`（和微调同一运行时），再用 `--model` 走 MediaPipe 看两条运行时是否一致。

### 目前的数字

| embedder | 数据 | hit@1 | hit@5 | mrr@10 | 正负例平均差 |
|---|---|---|---|---|---|
| hash-ngram（无模型下限） | 47 条容器故障日志三元组（哈希挖难负例，开发机临时数据） | 0.481 | 0.852 | 0.629 | 0.009 |
| EmbeddingGemma 2 基座（sentence-transformers） | 待跑 | | | | |
| EmbeddingGemma 2 基座（MediaPipe，`--model`） | 待跑 | | | | |
| 微调后 | 待跑 | | | | |

### 候选用途

都还没接入产品，列出来是为了攒数据时有方向：应用容器日志按已知故障分类、问题反馈去重、系统日志按语义聚类、文档语义搜索。每一项都先用 `eval.py` 在自己的数据上量基座，再决定要不要微调。

## 风险与已知限制

- **中文文本对文本没有公开数字。** 基座 MTEB 多语 61.36 是百余种语言平均；LawVault 显示基座在中文专业文本上 Top-1 只有 58%。相册的中文图文检索结果（[标签校准](photo-ai-label-calibration.md)）不能外推到文本对文本。
- **微调权重到设备的路径未验证。** 需要 HF → LiteRT 转换、重新生成模型清单与哈希、并在 Debian 13 开发镜像里重跑真实模型测试。在此之前，微调只能在开发机上证明收益。
- **哈希退路只能看字面。** `semantic=False` 时的分数不是语义分数，只用作下限和单元测试。
- **阈值是经验值。** `similarity.py` 的用例阈值参考官方示例的分数分布，没在我们的数据上调过。
- **双重前缀。** TEI 的 `/embed` 会按 `prompt_name` 自己加前缀，接那种服务时关掉 `HttpEmbedder(add_prefix=False)`。
- **本机未跑过真模型。** 写脚本的 Windows 机器没有 GPU、torch 和 MediaPipe；`server.py`、`train.py`、`eval.py --model` 只做了依赖缺失路径和 `--fake` 联调，离线测试在 [`ai/tests/test_finetune.py`](../../ai/tests/test_finetune.py)（`make ai-test`）。

## 来源

- 模型卡：https://huggingface.co/google/embeddinggemma-2
- Hugging Face 发布博客（前缀表、部署、医学微调示例）：https://huggingface.co/blog/embeddinggemma
- 官方微调教程：https://ai.google.dev/gemma/docs/embeddinggemma/fine-tuning-embeddinggemma-with-sentence-transformers
- 社区微调：https://huggingface.co/ByronLeeee/EmbeddingGemma-300M-LawVault 、https://huggingface.co/sentence-transformers/embeddinggemma-300m-medical 、https://huggingface.co/mrm8488/embeddinggemma-2-ft-typed-decision
- 微调列表（Hugging Face API）：`https://huggingface.co/api/models?filter=base_model:finetune:google/embeddinggemma-2`
