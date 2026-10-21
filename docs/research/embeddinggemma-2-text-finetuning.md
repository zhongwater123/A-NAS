# EmbeddingGemma 2 文本向量：任务前缀、社区微调与我们的微调流程

> 状态：脚本与离线测试完成；基座基线已在 CPU 开发机上跑（sentence-transformers 运行时）：短文本分类 60 条、仓库文档检索 1308 段 42 问、实验 NAS 的真实反馈与日志；MediaPipe 运行时的对照、微调与微调权重转 LiteRT 未做  
> 更新日期：2026-10-21  
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
| `build_triplets.py` | 带标签文本（同标签互为正例）、标注对、`--doc-queries` 的问答对 → 三元组 JSONL；用当前模型挖难负例 | 标准库 |
| `eval.py` | 同一份三元组当检索任务：Hit@1 / Hit@5 / MRR@10 / 正负例相似度差；可评 `--model`（MediaPipe）、`--embed-url`（HF）或哈希 | 标准库（`--model` 需 Worker 运行环境） |
| `eval_docs.py` + `docs_queries.json` | 文档检索：把 `docs/` 按标题切段当语料，42 个中文问题各标明答案段落，算段落级与文件级 Hit@k / MRR；`--mode sts` 对照前缀，`--dim` 从全维缓存截断 | 标准库 |
| `eval_dedup.py` | 一批真实文本的相似度分布、各阈值下的近似重复分组、按样例分类的打标率；`--strip-template` 先剥掉反馈模板 | 标准库 |
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

数据：60 条中文带标签短文本（NAS 使用问题 8 类，各 5 条，如上传完整性、相册回收站、快照、越权、应用崩溃、相册搜索、SMB、备份、硬盘健康）加 20 条英文容器故障日志（7 类），`build_triplets.py` 生成 190 条三元组，难负例用被评估的模型自己挖；每个 anchor 在全部 60 条里检索同标签的其他文本。数据是开发机临时编写的，不进仓库。2026-10-21，CPU 开发机（无 GPU），`server.py` 加载 `google/embeddinggemma-2` 修订 `914f7f89`、只加载文本编码器、bf16。

| embedder | hit@1 | hit@5 | mrr@10 | 正负例平均差（难负例由该模型挖） |
|---|---|---|---|---|
| hash-ngram（无模型下限） | 0.550 | 0.783 | 0.650 | 0.010 |
| EmbeddingGemma 2 基座，768 维（sentence-transformers） | 0.717 | 0.950 | 0.812 | -0.004 |
| EmbeddingGemma 2 基座，256 维截断，同一运行时 | 0.667 | 0.900 | 0.781 | -0.002 |
| EmbeddingGemma 2 基座（MediaPipe，`--model`） | 待跑（需开发镜像） | | | |
| 微调后 | 待跑（需 GPU） | | | |

同一数据上的分数分布（STS 前缀，768 维）：同标签对中位 0.858（P10 0.814），异标签对中位 0.791（P90 0.833，P99 0.868）。**分数整体挤在 0.75–0.95**：无关的中文句子也有 0.8 左右，同义改写 0.98。所以"相似"不能用一个凭感觉的阈值：取异标签 P99（0.868）为阈值，同标签对只召回 39%；取 P95（0.846）召回 64%。线上要用的阈值必须从各自场景的数据里标定，`similarity.py` 里的阈值只是占位。Hit@1 的失败集中在快照、应用崩溃、SMB、硬盘健康这几类：类内说法差异大（"没有执行记录"对"目标盘已满"），类间又共享"文件""失败"这类词。256 维比 768 维低 3–5 个点，在这个规模的数据上不算无损，和官方多语基准的 1 个点差距不同。

耗时：CPU 上 5 条短文本 1.85 s（含 HTTP），单条约 0.4 s；首次请求 3 s 含预热。60 条文本的一次评估十几秒。

### 文档检索（仓库 `docs/`，1308 段，42 问）

把仓库 `docs/` 下 125 篇 Markdown 按标题切成 1308 段（中位 366 字符，超过 1800 字符的按空行或行再切），从中抽 42 段各写一个自然语言问题（`ai/finetune/docs_queries.json`，答案段落固定，覆盖规格、架构、手册、ADR、调查、研究）。问题用检索前缀，段落用文档前缀；段落级命中要求答案那一段进前 k，文件级只要求同一篇文档的任意一段进前 k（界面按文件展示时的口径）。2026-10-21，CPU 开发机，fp32，`max_seq_length` 512（超长段截断）。

| embedder | hit@1 | hit@5 | hit@10 | mrr@10 | 文件级 hit@1 | 文件级 hit@5 |
|---|---|---|---|---|---|---|
| hash-ngram（无模型下限） | 0.190 | 0.405 | 0.524 | 0.280 | 0.333 | 0.714 |
| 基座 768 维，检索前缀 | 0.619 | 0.857 | 0.905 | 0.718 | 0.833 | 0.952 |
| 基座 256 维截断，检索前缀 | 0.548 | 0.810 | 0.905 | 0.658 | 0.833 | 0.952 |
| 基座 768 维，两边都用 STS 前缀（对照） | 0.429 | 0.857 | 0.929 | 0.611 | 0.667 | 0.929 |

- 段落级前 5 没命中的 6 问里，4 问落在同一篇文档的相邻小节（要"风险"命中"宠物"，要"修复与回归证据"命中"根因"）：模型找对了文档，没分清小节，所以文件级 hit@1 比段落级高 21 个点。
- 真正找错文档的两问都是问题措辞和答案段落用词不重叠的情况（"只改前端界面怎么起开发服务"的答案段标题是"Web 桌面"）。
- 256 维在段落级低 5–7 个点，文件级不变。
- **问句找段落要用检索前缀。** 两边都换成 STS 前缀后 Hit@1 从 0.619 掉到 0.429（文件级 0.833 → 0.667），Hit@5 持平：STS 前缀是给对称的句子比对用的，不对称的问答检索要用 query / document 前缀。
- 语料包含本文自身（研究文档不在跳过列表里），42 问没有一问以本文为答案，不影响结论。

### 真实数据：实验 NAS 的问题反馈与系统日志

2026-10-21 用测试工具只读取回实验 NAS 的 21 条问题反馈（16 条已解决）和近 7 天的 500 条系统日志（按消息去重 247 种，8 个服务），加上 2026-10-13 n8n 真实崩溃日志 13 行，用 `eval_dedup.py` 看相似度分布、近似重复分组与按样例归类。文本留在开发机，不进仓库。

| 数据 | 两两相似度 中位 / P99 / 最高 | 结论 |
|---|---|---|
| 问题反馈 20 条（STS 前缀） | 0.792 / 0.891 / 0.938；剥掉模板后 0.791 / 0.856 / 0.906 | 最像的几对全是同一模板（`【… 体验建议】… 复现：桌面→…`）下内容不同的建议，模板头部把分数撑到 0.94；剥掉后降到 0.91，仍是"回收站列表缺信息"和"快照列表缺信息"这类同类建议。两条十字以内的短句（"文件管理有问题"对"这里是问题反馈"）得 0.88。去重前必须剥模板，短文本不能只看向量。 |
| 系统日志 247 种消息 | 0.796 / 0.951 / 0.999 | 阈值 0.95：39 组、覆盖 148 条，合并的正是同一消息的不同参数（`anas-media@0/1/2`、同一堆栈帧的不同地址）；0.90 开始把不相干的并进一组（最大组 78 条），0.85 几乎全并成一组。日志按语义合并用 0.95。 |
| 系统日志按 17 条故障样例归类 | — | 健康机器的日志：阈值 0.90 只有 1 条被打标签，且确实是权限警告（`mkdir -p failed for path /.config/…`，0.907）；0.85 误报 12 条（"server running" 判成数据库类），0.80 误报 208 条。 |
| n8n 崩溃日志 13 行按样例归类 | 0.853 / 1.000 / 1.000 | `EACCES: permission denied, open '/home/node/…'` 得 0.958 归为权限类（正确）；堆栈帧行在 0.84–0.86 被归到各类（应视为无标签）。归类阈值取 0.92 上下能同时拦住健康日志和堆栈噪声。 |

三元组数据加入 42 个文档问答对后为 274 条（`build_triplets.py --doc-queries`）；问答对是不对称的，训练时应用 query / document 前缀，`train.py` 目前只支持单一前缀，待改。

### 运行时的两个发现

- **CPU 上不要用 bf16。** 这台开发机（Intel Core Ultra 5 225U，无 AVX-512 / AMX）上 bf16 靠软件模拟：8 段典型长度的文档 bf16 246 s、fp32 42.8 s，慢 5.7 倍。`server.py` 改为 CPU 默认 fp32、GPU 默认 bf16（`--dtype` 可指定）。官方只禁 fp16，没有提 CPU 上 bf16 的代价。
- **长段落要截断。** 加 `--max-seq-length 512` 后 1308 段在有其他任务抢 CPU 的情况下每段约 2.6 s（3 个并发任务），整套语料 55 分钟；不截断且 bf16 时每段超过 30 s。线上做文档检索时按 512 token 切段，比靠模型的 8192 上下文更省。

### 候选用途

都还没接入产品，列出来是为了攒数据时有方向：应用容器日志按已知故障分类、问题反馈去重、系统日志按语义聚类、文档语义搜索。每一项都先用 `eval.py` 在自己的数据上量基座，再决定要不要微调。

## 风险与已知限制

- **中文文本对文本没有公开数字。** 基座 MTEB 多语 61.36 是百余种语言平均；LawVault 显示基座在中文专业文本上 Top-1 只有 58%。相册的中文图文检索结果（[标签校准](photo-ai-label-calibration.md)）不能外推到文本对文本。
- **微调权重到设备的路径未验证。** 需要 HF → LiteRT 转换、重新生成模型清单与哈希、并在 Debian 13 开发镜像里重跑真实模型测试。在此之前，微调只能在开发机上证明收益。
- **哈希退路只能看字面。** `semantic=False` 时的分数不是语义分数，只用作下限和单元测试。
- **阈值是经验值。** `similarity.py` 的用例阈值参考官方示例的分数分布，没在我们的数据上调过。
- **双重前缀。** TEI 的 `/embed` 会按 `prompt_name` 自己加前缀，接那种服务时关掉 `HttpEmbedder(add_prefix=False)`。
- **只跑了一条运行时。** 基线用的是 sentence-transformers 在 CPU 上加载 Hugging Face 权重；Worker 走的 MediaPipe + LiteRT 没有在这台机器上跑（没有开发镜像），两条运行时的向量是否一致待对照。`train.py` 和 `eval.py --model` 只做了依赖缺失路径的检查，离线测试在 [`ai/tests/test_finetune.py`](../../ai/tests/test_finetune.py)（`make ai-test`）。
- **数据还小。** 分类数据 60 条，是为了把流程跑通；文档检索 42 问是真实文档但问题由人工撰写；真实反馈只有 21 条、没有人工标注的重复对，日志只有一台健康机器 7 天的。要攒各场景的真实文本（OCR 输出、对话片段、更多机器的日志、成员真实提问）和标注。

## 来源

- 模型卡：https://huggingface.co/google/embeddinggemma-2
- Hugging Face 发布博客（前缀表、部署、医学微调示例）：https://huggingface.co/blog/embeddinggemma
- 官方微调教程：https://ai.google.dev/gemma/docs/embeddinggemma/fine-tuning-embeddinggemma-with-sentence-transformers
- 社区微调：https://huggingface.co/ByronLeeee/EmbeddingGemma-300M-LawVault 、https://huggingface.co/sentence-transformers/embeddinggemma-300m-medical 、https://huggingface.co/mrm8488/embeddinggemma-2-ft-typed-decision
- 微调列表（Hugging Face API）：`https://huggingface.co/api/models?filter=base_model:finetune:google/embeddinggemma-2`
