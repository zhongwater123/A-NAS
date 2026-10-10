# 本地照片 AI 模型与 Runtime 选型

> 状态：候选已收敛，等待目标 NAS 实机基准后冻结版本  
> 更新日期：2026-10-07  
> 适用范围：Intel Core i3-12100、8 GB 内存、Debian 13、无独立显卡的 A-NAS 相册  
> 关联规格：[相册功能规格](../specs/photo-library.md)
>
> 2026-10-09 决定：发行使用 EmbeddingGemma 2 全模态 740M 官方包，不做 OCR；运行方式改为 MediaPipe Universal Embedder。本文其余部分保留为选型证据，现行方案见 [M2 实施方案](../architecture/photo-ai.md)。

## 结论

这套硬件可以在后台完成中文语义检索、受控标签、中文/英文 OCR 和人脸聚类。首版不需要依赖云端，也不需要为了“猫、电动车、3D 打印机”这类检索先引入目标检测器：同一个图文向量模型既可保存图片向量供自然语言检索，也可将图片与受控中文标签的文本向量比较，生成候选标签。

重新核对 2026-04-07 至 2026-10-07 的新发布后，**Chinese-CLIP RN50 和 SigLIP 2 都不再是默认目标**。当前首选是 Google DeepMind 于 2026-10-06 发布的 EmbeddingGemma 2：照片场景只需加载 440M 的文本与视觉部分，Google 同步提供 388 MB 的 LiteRT QAT 包，明显比 2B 级 VLM 更匹配 8 GB CPU NAS。腾讯微信团队 2026-08-25 发布的 WeMM-Embedding-2B 作为新质量标杆；其官方 BF16 权重为 5.44 GB，且没有官方 CPU/INT4 路线，不能直接作为发行默认。下载量不是质量结论，但它是维护和生态风险的辅助信号，不能完全忽略。

首版建议按下列组合推进；具体模型 revision、量化配方和阈值必须经过本文的实机基准后写入版本化模型清单：

| 能力 | 推荐默认 | Runtime 与精度起点 | 结论 |
|---|---|---|---|
| 中文语义检索、零样本标签 | `google/embeddinggemma-2` 的文本+视觉 440M 配置 | 官方 LiteRT QAT：文本 INT4、视觉 INT8，388 MB；CPU、batch 1 | 首选部署候选；中文门禁通过后冻结为默认 |
| 中文语义检索、零样本标签 | `tencent/WeMM-Embedding-2B` | 官方 BF16 正确性基线；暂不承诺 CPU 量化交付 | 新质量标杆；不作为 8 GB 默认 |
| 中文语义检索、零样本标签 | `Qwen/Qwen3-VL-Embedding-2B` | OpenVINO GenAI INT4、CPU、batch 1 | 上一代质量与 Runtime 对照 |
| 中英文 OCR | PP-OCRv6 `small` 检测器 + 识别器 | OpenVINO CPU；`tiny` 作低资源备选 | 默认采用 |
| 人脸检测、对齐、向量 | Open Model Zoo `face-detection-retail-0004` + `landmarks-regression-retail-0009` + `face-reidentification-retail-0095` | 原生 OpenVINO IR，优先 FP16-INT8 版本，FP32 作质量对照 | 默认采用，但必须验证家庭照片质量 |
| 人脸聚类 | 归一化向量上的 HDBSCAN 或层次聚类 | 离线、图库内聚类；阈值以“避免错误合并”为优先 | 算法与阈值待基准冻结 |
| 中文图片摘要 | 标签、场景、OCR、人物数量和时间地点元数据拼装的结构化中文摘要 | 不加载生成式模型 | 默认采用 |
| 自由生成详细中文描述 | 无预装默认模型 | 仅按需加载通过 3 GiB 内存门禁的 VLM | 首版暂缓默认启用 |

推荐运行架构是独立 Python AI worker、任务并发数 1、一次只驻留一条模型流水线。EmbeddingGemma 2 先验证 LiteRT-LM Linux x86 CPU；OpenVINO 模型使用延迟模式、单 stream，先以 2 个 CPU 线程为基线，再与 1/4 线程对比；用户活动出现后不再领取新任务。Intel 对 i3-12100 的官方规格是 [4 核 8 线程、支持 AVX2 与 Intel Deep Learning Boost](https://www.intel.com/content/www/us/en/products/sku/134584/intel-core-i312100-processor-12m-cache-up-to-4-30-ghz/specifications.html)，因此这是一台适合串行后台推理、但不适合让多个大模型同时常驻的主机。

## 近半年新模型扫描

扫描窗口固定为 2026-04-07 至 2026-10-07；只有原厂模型卡、代码仓库、论文和 Runtime 文档可作为证据。生成式 VLM 能回答图片问题，不等于它提供经过检索训练、可稳定复现的 embedding，因此不把普通 `Image-Text-to-Text` checkpoint 计入检索候选。

| 首次公开 | 模型与机构 | 许可、规模与中文 | 8 GB CPU 与 Runtime 判断 | 决策 |
|---|---|---|---|---|
| 2026-10-06 | [EmbeddingGemma 2](https://huggingface.co/google/embeddinggemma-2)，Google DeepMind | Apache-2.0；740M 全模态，照片只加载文本 270M + 视觉 170M，共 440M；768d，可截断到 512/256/128d；官方称支持 100+ 语言，但没有单列中文照片检索分数 | Google 官方 [LiteRT 包](https://huggingface.co/litert-community/embeddinggemma-2-text-vision-440m-litert-lm)对文本使用 INT4 QAT、视觉使用 INT8 QAT，下载 388 MB；官方 Linux ARM CPU 样例显示文本+视觉内存约 647 MB，但不能外推本机 x86 延迟 | **首选部署候选**；中文家庭照片门禁和 Debian 13 smoke test 通过后取代 SigLIP 2 |
| 2026-09-03 | [Core-Embed-2B](https://huggingface.co/Alibaba-NLP/core-emb-2b)，Alibaba-NLP | CC-BY-4.0；Qwen3-VL 2B embedding，专门改善颜色/属性/物体组合关系；官方仓库约 8.53 GB；没有中文专项结果 | 官方示例为 CUDA BF16/FlashAttention，没有官方 CPU/INT4 路线 | 只做“红色电动车”等组合检索专项对照，不进入 8 GB 默认决赛 |
| 2026-08-25 | [WeMM-Embedding-2B](https://huggingface.co/tencent/WeMM-Embedding-2B)，腾讯微信视觉团队 | 代码和公开权重 Apache-2.0；基于 Qwen3.5，模型页标注中文/英文；名称为 2B，但 Hugging Face 统计约 3B、BF16 权重 5.44 GB；2048d/MRL | 官方提供 Transformers、Sentence Transformers、vLLM、SGLang，示例为 CUDA BF16；没有官方 CPU/INT4 转换。官方 MMEB-v2 报告同规模得分高于 Qwen3-VL-Embedding，但不是 A-NAS 中文数据结论 | **新质量标杆**；只有后续出现可复现 CPU 量化路线才争夺默认 |
| 2026-08-03（论文）；08-13（原生 Transformers/vLLM） | [UEmbed-2B](https://huggingface.co/Alibaba-NLP/UEmbed-2B)，Alibaba-NLP | CC-BY-4.0；基于 Qwen3.5 的 2B dense+sparse embedding；BF16 主权重约 4.43 GB，另有约 754 MB sparse 权重；基础 Qwen3.5 覆盖 201 种语言，但 UEmbed 未给中文专项分数 | 官方 BF16/FlashAttention 和 vLLM 路线偏 GPU，没有官方 CPU/INT4 包 | 稀疏检索研究候选；当前图库规模不值得为它引入约 5.2 GB 权重和第二套索引 |

Google 的[发布公告](https://blog.google/innovation-and-ai/technology/developers-tools/embeddinggemma-2/)明确给出 2026-10-06 首发日期、Apache-2.0、740M 参数和本地端侧定位；[开发指南](https://developers.googleblog.com/en/embeddinggemma-2-the-developer-guide/)明确允许只加载 440M 文本+视觉编码器。[腾讯论文](https://arxiv.org/abs/2608.24053)的 v1 日期为 2026-08-25；[CORE 论文](https://arxiv.org/abs/2609.04083)和 [UEmbed 论文](https://arxiv.org/abs/2608.02583)分别记录 2026-09-03 与 2026-08-03。

### Qwen 新系列核对

截至 2026-10-07，Qwen 官方尚未发布 `Qwen3.5-VL-Embedding`、`Qwen3.8-VL-Embedding` 或同义的专用多模态 embedding checkpoint。官方 [`Qwen3.8-27B`](https://huggingface.co/Qwen/Qwen3.8-27B)和 [`Qwen3.8-Flash-Next`](https://huggingface.co/collections/Qwen/qwen38-flash-next)均标记为 `Image-Text-to-Text` 生成模型；把隐藏层自行池化不等价于检索训练，不能替代 embedding checkpoint。Qwen 官方仓库中关于 Qwen3.5 embedding 的[提问仍未得到官方答复](https://github.com/QwenLM/Qwen3.8/discussions/62)，因此也不能推断其发布时间。

近半年的 Qwen3.5 多模态 embedding 实际来自第三方团队：腾讯 WeMM、Alibaba-NLP UEmbed。它们证明新骨干可以提升质量，但不应写成“Qwen 官方新 embedding”。Qwen3-VL-Embedding 的论文在 2026-01-08 发布，虽于 4 月更新模型页面，不能仅凭页面更新时间算作近半年新模型。

## 推荐默认方案

### 中文语义检索与受控标签

#### 首选部署候选：EmbeddingGemma 2 文本+视觉 440M

[Google 官方模型卡](https://huggingface.co/google/embeddinggemma-2)明确将 EmbeddingGemma 2 用于跨模态检索、分类和聚类，采用 Apache-2.0，统一输出 768 维向量并支持 MRL。照片相册不加载音频编码器，有效规模为文本 270M + 视觉 170M，共 440M。Google 宣称模型覆盖 100+ 语言，但没有单列中文家庭照片成绩，所以“支持多语言”不能替代本项目的中文查询验收。

发行路径优先验证 [LiteRT 文本+视觉 440M QAT 包](https://huggingface.co/litert-community/embeddinggemma-2-text-vision-440m-litert-lm)：文本权重 INT4、视觉权重 INT8，文件约 388 MB。官方在 Linux ARM CPU 上报告文本+视觉工作集约 647 MB，足以证明它是 8 GB 级设备的合理候选，但该数字不能外推到 i3-12100/Debian 13。首轮索引保留 768d 作为质量基线；只有 256d 在中文检索上无显著退化时才缩短向量。

#### 新质量标杆：WeMM-Embedding-2B

[腾讯官方模型卡](https://huggingface.co/tencent/WeMM-Embedding-2B)显示 WeMM 是基于 Qwen3.5 的专用多模态 embedding，而不是生成式 VLM 隐状态拼装；支持文本、图片、视频和视觉文档，权重与代码为 Apache-2.0，模型页标注中文与英文。腾讯官方 MMEB-v2 结果中，同规模 WeMM 的综合得分高于 Qwen3-VL-Embedding，但这只是通用基准，不能证明 A-NAS 中文家庭照片也更好。

它当前不适合作为 8 GB 默认：名称为 2B，Hugging Face 实际统计约 3B 参数，BF16 权重文件约 5.44 GB；官方示例和服务路径为 CUDA BF16、vLLM 或 SGLang，没有官方 CPU/INT4 包。开发机可用它给同一批中文查询生成质量上限，目标 NAS 不为追新模型而采用来源不明的社区量化包。

#### 上一代 Runtime 对照：Qwen3-VL-Embedding-2B

[Qwen 官方模型卡](https://huggingface.co/Qwen/Qwen3-VL-Embedding-2B)和[官方仓库](https://github.com/QwenLM/Qwen3-VL-Embedding)显示，该模型面向多模态检索、聚类和跨模态理解，采用 Apache-2.0，支持包括中文在内的 33 种语言，规模为 2B、输出最多 2048 维向量。原始 BF16 权重约 4.26 GB，不能直接在 8 GB NAS 上常驻；[OpenVINO GenAI 官方基准工具](https://github.com/openvinotoolkit/openvino.genai/blob/master/tools/llm_bench/README.md)提供 INT4/INT8 转换与 embedding pipeline，因此保留它作为“较成熟的 2B CPU 量化路线”对照，而不是因为型号更新就继续担任首选。

SigLIP 2 Base 仍保留为 0.4B/F32 回归基线；若 EmbeddingGemma 2 的 Linux x86 Runtime 尚不能稳定交付，可以用它解耦模型问题和相册索引问题，但不再把它写成 2026 年的默认目标。

#### 标签落地方式

- 导入时只计算一次图片向量；查询时计算中文文本向量并做近邻检索。模型 ID、revision、精度、预处理和向量维度属于索引版本，切换模型时后台重建，不能混用向量。
- 维护版本化的中文标签词表、同义词和 prompt 模板，提前计算文本向量；“猫”“电动车”“3D 打印机”等均是词表候选，而不是写死在模型代码中。EmbeddingGemma 2 查询使用官方 `SearchQuery` prompt，图片不附加 prompt；Qwen 对照组的 instruction 使用英文、实际查询和标签保留中文。
- 标签是独立相似度判定，不把任意一组标签的 softmax 结果解释成全局置信度；每类阈值通过家庭照片验证集校准，低于阈值不自动落标。
- 图文向量擅长判断整张图的语义，不提供目标框、准确数量或小物体位置。只有产品以后需要“框出每台 3D 打印机”时，才另立目标检测模型选型。

### OCR

[PaddleOCR 官方仓库](https://github.com/PaddlePaddle/PaddleOCR)及其[许可证](https://github.com/PaddlePaddle/PaddleOCR/blob/main/LICENSE)均采用 Apache-2.0。[PP-OCRv6 官方说明](https://github.com/PaddlePaddle/PaddleOCR/blob/main/docs/version3.x/algorithm/PP-OCRv6/PP-OCRv6.en.md)提供 tiny、small、medium 三档模型，覆盖中文、英文等语言，并给出 OpenVINO 和 ONNX Runtime 的 CPU 结果；[OCR pipeline 官方文档](https://github.com/PaddlePaddle/PaddleOCR/blob/main/docs/version3.x/pipeline_usage/OCR.en.md)列出的 small 检测器和识别器文件分别约 9.6 MB、20.4 MB，适合作为质量/资源折中。

默认先运行文本检测，无文本候选时跳过识别。保存识别文本、语言、置信度、框位置和模型版本；搜索索引采用识别文本，UI 必须允许用户看到“机器识别”属性，避免将 OCR 误识别当作原始元数据。

### 人脸检测、对齐、向量与聚类

默认组合全部来自 Intel Open Model Zoo，并直接以 OpenVINO IR 运行：

- [`face-detection-retail-0004`](https://github.com/openvinotoolkit/open_model_zoo/blob/master/models/intel/face-detection-retail-0004/README.md)：官方指标为 0.588M 参数、1.067 GFLOPs，针对大于约 60×60 像素的人脸；其 [`model.yml`](https://github.com/openvinotoolkit/open_model_zoo/blob/master/models/intel/face-detection-retail-0004/model.yml)将模型许可指向仓库 Apache-2.0 LICENSE。
- [`landmarks-regression-retail-0009`](https://github.com/openvinotoolkit/open_model_zoo/blob/master/models/intel/landmarks-regression-retail-0009/README.md)：预测五个人脸关键点，用于裁剪和对齐；其 [`model.yml`](https://github.com/openvinotoolkit/open_model_zoo/blob/master/models/intel/landmarks-regression-retail-0009/model.yml)同样指向 Apache-2.0 LICENSE。
- [`face-reidentification-retail-0095`](https://github.com/openvinotoolkit/open_model_zoo/blob/master/models/intel/face-reidentification-retail-0095/README.md)：约 1.107M 参数、输出 256 维向量，以余弦距离比较，并要求输入已对齐的人脸；其 [`model.yml`](https://github.com/openvinotoolkit/open_model_zoo/blob/master/models/intel/face-reidentification-retail-0095/model.yml)同样指向 Apache-2.0 LICENSE。

[Open Model Zoo 官方仓库](https://github.com/openvinotoolkit/open_model_zoo)已进入 maintenance mode，所以这套组合的优势是小、许可链清楚、OpenVINO 直用，不代表它一定是当前家庭照片质量最优。必须覆盖婴幼儿到成年后的年龄变化、侧脸、遮挡、暗光、多人、小脸和旧照片；错误拆分可以让用户合并，错误合并会暴露或混淆家庭成员，因而聚类门槛要偏保守。

聚类只在同一可见图库边界内进行，不跨私有图库自动关联身份。系统保存的是无姓名的人脸向量和簇；命名、合并、拆分由有权限的用户确认。模型升级后重算派生数据，不静默沿用旧阈值。[scikit-learn 的 HDBSCAN 官方 API](https://scikit-learn.org/stable/modules/generated/sklearn.cluster.HDBSCAN.html)可作为首个实现候选，但最终还要与层次聚类比较，且不得为 2 万张全量照片无界地物化 N×N 距离矩阵。

2026-10-10 复核：M2 的 Worker 运行环境已包含 OpenCV 5.0（`opencv-contrib-python-headless` 5.0.0.93），其中的 `FaceDetectorYN` 与 `FaceRecognizerSF` 可以直接运行 OpenCV Zoo 的两个模型，不必再引入 OpenVINO：

- [YuNet](https://github.com/opencv/opencv_zoo/tree/main/models/face_detection_yunet)：轻量人脸检测器，同时输出 5 个关键点；官方报告 WIDER Face 验证集 AP 0.834（easy）、0.824（medium）、0.708（hard）。目录内文件以 MIT 许可发布。`face_detection_yunet_2026may.onnx` 是为 OpenCV 5.x 重新导出的动态输入尺寸版本，`2023mar` 为固定尺寸，另有 INT8 版本。
- [SFace](https://github.com/opencv/opencv_zoo/tree/main/models/face_recognition_sface)：以 SFace 损失训练的 MobileFaceNet，用 YuNet 的 5 个关键点对齐人脸后输出 128 维向量；官方评估准确率 0.9940（INT8 0.9932）。目录内文件以 Apache-2.0 许可发布；训练数据来源需在对外发行前核对。
- [InsightFace](https://github.com/deepinsight/insightface) 的预训练模型只许非商业研究使用，不作为候选。

人物识别的实施方案据此把 YuNet + SFace 列为首选、上述 Open Model Zoo 组合列为回退，见[相册人物识别](../architecture/photo-faces.md)。

### 结构化中文摘要

首版默认描述不是自由生成，而是使用经过阈值筛选的标签、场景、OCR 是否存在、人物数量和可信元数据拼装短句，例如：“室内，可能包含 3D 打印机和桌子；画面中有文字”。它不增加一个常驻大模型，结果可解释、可重建、可按字段纠错，也能直接参加中文检索。

“详细中文描述”不适合在这台 8 GB 主机上作为每张照片的默认任务，不是因为 CPU 完全不能运行 VLM，而是因为生成式 VLM 除视觉编码外还要逐 token 解码；模型权重、图像输入、KV cache、推理 Runtime 和 A-NAS 其他进程会竞争同一份 8 GB 内存。量化只降低部分内存，并不消除长延迟和工作集峰值。因此它保持按需、低优先级、可取消，并且不可阻塞标签/OCR/人脸任务。

## 语义模型比较与基准顺序

| 候选 | 机构、采用和许可 | 任务适配 | 8 GB CPU 判断 | 定位 |
|---|---|---|---|---|
| [EmbeddingGemma 2 文本+视觉 440M](https://huggingface.co/google/embeddinggemma-2) | Google DeepMind；2026-10-06；Apache-2.0 | 专门用于多模态 embedding；100+ 语言；检索、分类与聚类；中文需本地验收 | 官方 LiteRT QAT 包 388 MB；首选 CPU 候选，仍需验证 Linux x86/Debian 13 | 第一部署候选；过中文与系统门禁后冻结默认 |
| [WeMM-Embedding-2B](https://huggingface.co/tencent/WeMM-Embedding-2B) | 腾讯微信视觉；2026-08-25；Apache-2.0；基于 Qwen3.5 | 专门用于多模态 embedding；页面标注中英文；官方通用基准领先 Qwen3-VL 同规模 | 实际约 3B、BF16 权重 5.44 GB；无官方 CPU/INT4 路线 | 第一质量标杆；当前不作为 8 GB 默认 |
| [Qwen3-VL-Embedding-2B](https://huggingface.co/Qwen/Qwen3-VL-Embedding-2B) | 阿里云 Qwen；Apache-2.0；官方仓库持续更新 | 专门用于多模态 embedding；支持中文；图片分类、检索与聚类 | BF16 权重约 4.26 GB；OpenVINO GenAI INT4 路线可测 | 上一代质量与 Runtime 对照 |
| [SigLIP 2 Base/16 224](https://huggingface.co/google/siglip2-base-patch16-224) | Google；Apache-2.0 | 官方明确支持多语言、零样本分类和图文检索 | 0.4B、F32 权重约 1.50 GB；OpenVINO 转换仍需验证 | 旧回归基线与 Runtime 应急回退 |
| [GME-Qwen2-VL-2B](https://huggingface.co/Alibaba-NLP/gme-Qwen2-VL-2B-Instruct) | 阿里巴巴通义实验室；Apache-2.0；近月约 1.1 万下载 | 中文 MTEB 和多模态检索有官方结果 | 2.21B、F32 仓库约 8.85 GB，并有 `transformers` 版本/远程代码约束 | 上一代兼容性对照，不进入默认决赛 |
| [Chinese-CLIP RN50](https://huggingface.co/OFA-Sys/chinese-clip-rn50) | OFA-Sys/阿里团队；Apache-2.0 权重；GitHub 约 6k stars；HF 下载未跟踪而不是零下载 | 中文图文检索和零样本分类明确 | 77M、权重约 308 MB，资源下限最清楚；项目主要更新停留在 2023 年 | 低资源/回归控制组，不再作为默认目标 |

下载和 stars 会随时间变化，只记录本次选型时的生态信号，不作为精度证据。Chinese-CLIP 的 Hugging Face 页面因旧 `.pt` 文件没有下载统计，不能用“下载量低”单独证明模型差；将它降为控制组的主要原因是模型代际、维护活跃度和新运行时生态，而不是关注度数字本身。

语义模型按以下顺序验证：

1. **EmbeddingGemma 2 440M LiteRT smoke test**：先证明 QAT 包能在 Debian 13/x86 CPU 离线加载、分别编码中文文本和图片，并连续运行 30 分钟。
2. **中文质量决赛**：同一测试集比较 EmbeddingGemma 2、开发机上的 WeMM-Embedding-2B BF16，以及 Qwen3-VL-Embedding-2B 正确性基线；WeMM 只定义质量上限，不要求装到目标 NAS。
3. **Qwen3-VL-Embedding-2B INT4**：使用 OpenVINO GenAI 的 `EmbeddingPipeline` 与 `feature-extraction` 导出路径，判断它是否在中文质量上明显胜过 EmbeddingGemma 2且仍通过 3 GiB 门禁。
4. **SigLIP 2 与 Chinese-CLIP 控制组**：前者用于 Runtime 回归，后者给出最低内存与延迟下限；不因生态关注度直接接受或否决质量。

最终默认判定：EmbeddingGemma 2 先同时通过中文质量、峰值 RSS、无 swap、前台影响和离线安装门禁；通过即采用。若中文质量不合格或 LiteRT 在 Debian 13 不稳定，再比较 Qwen3-VL-Embedding-2B INT4；Qwen 也失败才以 SigLIP 2 暂时交付。WeMM 在没有官方可复现的 CPU 量化路径前只作质量标杆。无论哪一个胜出，模型 Adapter、索引版本和标签阈值都不得绑定模型私有 API。

## 其他必须经过实机基准的候选

| 候选 | 可能收益 | 当前不作为默认的原因 | 进入条件 |
|---|---|---|---|
| PP-OCRv6 tiny | 更小、更快 | 小字、低照度和复杂背景可能下降 | small 影响前台体验或超出内存目标，且 tiny 的 CER 可接受 |
| PP-OCRv6 medium | 可能改善复杂文本质量 | 体积和延迟更高 | small 的中文/英文 CER 不达标后才测试 |
| Open Model Zoo 人脸组合的 FP32 与 FP16-INT8 版本 | 比较量化对小脸/侧脸的影响 | 量化可能改变边界样本 | 以 FP32 作参考，FP16-INT8 达到质量门槛后作为默认 |
| [Qwen2-VL-2B-Instruct](https://huggingface.co/Qwen/Qwen2-VL-2B-Instruct) INT4 | Apache-2.0，官方模型卡支持中文；[OpenVINO 官方 notebook](https://docs.openvino.ai/2024/notebooks/qwen2-vl-with-output.html)给出 INT4 权重量化与 CPU/AUTO 推理路线 | 原始权重仓库约 4.4 GB；目标机实际峰值内存、首 token 与整句延迟未知 | 仅按需加载；worker 峰值 RSS ≤ 3 GiB，无 swap 抖动，中文事实性和延迟门禁通过 |
| [Qwen3-VL-2B-Instruct](https://huggingface.co/Qwen/Qwen3-VL-2B-Instruct) INT4 | Apache-2.0、中文能力更强的潜在新候选 | 尚未验证稳定的本机 OpenVINO 导出和 CPU 路径 | 先完成转换 smoke test，再与 Qwen2-VL-2B 同基准比较 |

自由描述的基线始终是结构化中文摘要。即使某个 VLM 通过基准，也只增加“按需生成详细描述”能力，不替换可解释的标签、OCR 和原始元数据。

## 暂缓或排除的选项

| 选项 | 结论 | 依据 |
|---|---|---|
| [SmolVLM-500M-Instruct](https://huggingface.co/HuggingFaceTB/SmolVLM-500M-Instruct) | 暂缓中文描述 | 官方模型卡显示 0.5B、Apache-2.0、单图推理约需 1.23 GB GPU RAM，但明确将 NLP 语言列为 English；小并不等于中文描述合格，也不能用 GPU 内存数字推断本机 CPU 峰值 |
| [Jina CLIP v2](https://huggingface.co/jinaai/jina-clip-v2) | 排除首版产品默认 | 官方模型卡标为 CC-BY-NC-4.0，且约 865M 参数；非商业限制和资源开销均不合适 |
| [Apple MobileCLIP](https://github.com/apple/ml-mobileclip) | 排除产品默认 | 官方[模型权重许可证](https://github.com/apple/ml-mobileclip/blob/main/LICENSE_MODELS)限制为研究用途，不能因代码仓库许可而推定权重可产品化 |
| [InsightFace 预训练权重](https://github.com/deepinsight/insightface) | 排除产品默认 | 官方仓库说明代码为 MIT，但其提供的训练数据与预训练模型仅限非商业研究；除非另行取得明确许可，不进入发行包 |
| OpenCV YuNet + SFace | 暂缓替代方案 | 即使代码或模型目录展示宽松许可证，仍需逐个冻结权重来源、修订与许可证；当前 OMZ 三件套的来源链更清楚，先做质量基准 |
| [Qwen2.5-VL-3B-Instruct](https://huggingface.co/Qwen/Qwen2.5-VL-3B-Instruct) | 排除首版产品默认 | 其官方[权重许可证](https://huggingface.co/Qwen/Qwen2.5-VL-3B-Instruct/blob/main/LICENSE)不是 Apache-2.0，包含研究用途条款；同时比 2B 候选更重 |
| 为每张照片默认生成长篇描述 | 暂缓 | 在 8 GB 共享主机上放大内存、延迟、功耗和幻觉风险，且对首版语义检索并非必要 |

许可证判断必须针对“具体权重文件”而不只是训练代码。以上是工程准入判断，不替代法律意见。发行前的模型清单应保存：上游 URL、不可变 revision、文件 SHA-256、权重许可证副本、代码/Runtime 许可证与 NOTICE。

## Runtime 与部署边界

### 默认 Runtime

EmbeddingGemma 2 首选 Google 官方 LiteRT-LM QAT 包。官方[端侧部署说明](https://developers.googleblog.com/en/google-ai-edge-with-embeddinggemma-2/)展示了本地媒体搜索、SQLite 向量保存与余弦检索，并提供 Linux CPU 路径；A-NAS 仍需验证 LiteRT-LM 的 Linux x86 Python/C++ 接口、服务重启、并发取消与离线分发，不能因为手机演示可运行就直接宣布 Debian 13 可交付。

Qwen3-VL-Embedding 对照组使用 OpenVINO GenAI 的 `EmbeddingPipeline`。[官方 `llm_bench` 文档](https://github.com/openvinotoolkit/openvino.genai/blob/master/tools/llm_bench/README.md)已列出 Qwen3-VL-Embedding 的文本、图片和视频 embedding 示例，使用 `--task feature-extraction` 导出，并支持 `--weight-format int4`。这条官方路径取代“自己拼装通用 Qwen VLM 隐状态”的实现，减少池化、chat template 和多模态输入格式不一致的风险。

SigLIP 2 先用官方 Transformers 实现建立正确性基线，再验证 ONNX/OpenVINO 导出；若 OpenVINO 路径不稳定，保留 PyTorch CPU 作为早期可部署实现，不为统一 Runtime 强行延误首版。其余视觉模型仍优先使用原生 OpenVINO Python API：它可以用 [`Core.read_model` 直接读取 ONNX](https://docs.openvino.ai/nightly/openvino-workflow/model-preparation/convert-model-onnx.html)，也可将通过验证的模型保存为 IR；[CPU 调度文档](https://docs.openvino.ai/2025/openvino-workflow/running-inference/inference-devices-and-modes/cpu-device/performance-hint-and-thread-scheduling.html)支持显式设置延迟/吞吐提示、stream 数和推理线程数。

[ONNX Runtime CPU 包](https://onnxruntime.ai/docs/install/)作为转换正确性参考和 OpenVINO 失败时的回退。首版不叠加 ONNX Runtime OpenVINO Execution Provider；[该 EP](https://onnxruntime.ai/docs/execution-providers/OpenVINO-ExecutionProvider.html)虽可使用 Intel CPU/GPU/NPU，但对本项目会增加另一层配置与兼容矩阵，而原生 OpenVINO 已能覆盖主要路径。

不要默认启用 UHD 730 iGPU。它与系统共享内存，驱动与算子覆盖也需要额外验证；首版先证明 CPU 后台处理不会影响文件服务和相册前台，再把 GPU 作为独立优化实验。

### Debian 13 风险

OpenVINO 当前[官方系统要求](https://docs.openvino.ai/2026/about-openvino/release-notes-openvino/system-requirements.html?language=en)列出 Ubuntu、RHEL、openSUSE 等发行版，但没有 Debian 13。因此“x86-64/Intel CPU 支持”不能替代操作系统支持结论。冻结 Runtime 版本前必须在与发布镜像相同的 Debian 13 上完成：安装、模型加载、一次推理、量化模型推理、连续运行、服务重启和离线安装测试。若失败，回退 ONNX Runtime CPU，或将已验证的 OpenVINO 版本与依赖封装进受控 worker 镜像；不能在安装时在线拉取未锁定 wheel。

### 资源控制

- worker 并发固定为 1；标签/OCR/人脸流水线轮转加载，不同时常驻 VLM。
- 起始配置为 `num_streams=1`、推理线程 2、batch 1；基准覆盖 1/2/4 线程，最终以“不显著拖慢前台”为优先，而不是单张最快。
- EmbeddingGemma 2 440M 的目标峰值 RSS ≤ 1.5 GiB；SigLIP 回归基线 ≤ 2 GiB；Qwen3-VL-Embedding-2B INT4 和按需生成式 VLM 的硬门禁均为 worker 峰值 RSS ≤ 3 GiB。任何方案出现持续 swap 或 OOM 均判失败；不通过扩大 swap 掩盖问题。
- 所有输入先做像素上限和损坏检查；原图不交给模型任意解码路径无限展开。
- 每项派生记录保存 pipeline、模型、权重哈希、预处理和阈值版本。升级后允许后台重建，并保留失败原因和可重试状态。

## 实机基准计划

### 数据集

从获得同意的家庭照片中建立至少 1,000 张代表性样本，另留不参与阈值调节的测试集。必须覆盖：室内/室外、猫、狗、电动车、汽车、3D 打印机、食物、风景、截图、票据、中文/英文招牌、暗光、旋转、多人、小脸、侧脸、遮挡、婴幼儿、年龄变化和老照片。不得把私人照片上传给外部服务；标注与运行均留在测试 NAS。

### 基准矩阵

1. 语义：EmbeddingGemma 2 440M LiteRT QAT（768d，并抽样比较 256d）；开发机上的 WeMM-Embedding-2B BF16 质量上限；Qwen3-VL-Embedding-2B OpenVINO INT4，并用 INT8 抽样核对量化损失；SigLIP 2 和 Chinese-CLIP 作为回归/低资源控制组。
2. OCR：PP-OCRv6 small 与 tiny；small 质量不足时再加 medium。
3. 人脸：OMZ 三件套 FP32 与 FP16-INT8；对比 HDBSCAN 和层次聚类的阈值曲线。
4. 描述：结构化中文摘要基线；Qwen2-VL-2B INT4；Qwen3-VL-2B 仅在导出 smoke test 通过后加入。
5. 每项都以 batch 1、1/2/4 线程测试冷加载、热推理和连续 30 分钟运行；全流程另做 2 万张估算，并最终用实际批处理验证估算没有被模型切换和 I/O 扭曲。

### 指标与发布门禁

| 能力 | 质量指标 | 系统指标与门禁 |
|---|---|---|
| 检索 | 中文及中英混合查询的 Recall@1/5/10、nDCG@10 | 单张编码 p50/p95、查询 p95、模型加载时间、索引体积 |
| 标签 | 每类 precision/recall/F1、误报率；特别记录“猫/电动车/3D 打印机” | 低置信度不落标；阈值必须来自保留验证集 |
| OCR | 中文 CER、英文 CER、文本检测漏检/误检率 | 无文本图片不应承担完整识别成本 |
| 人脸 | 检测召回/误报、验证 TAR/FAR、聚类 B-cubed F1 | 错误合并率为主要否决指标；量化版不能明显劣化小脸与侧脸 |
| 描述 | 中文可读性、事实错误率、幻觉率、人工偏好 | VLM 峰值 RSS ≤ 3 GiB；无 swap 抖动；输出 token 和输入像素有硬上限 |
| 整机 | 前台相册/API p95、文件服务吞吐、CPU 温度 | 后台运行时前台 p95 劣化目标 < 20%；用户活动后停止领取新任务；无 OOM、无持续 swap |

实测时记录 Runtime/模型 revision、线程、stream、batch、精度、输入尺寸、冷/热状态、CPU 时间、墙钟时间、RSS/PSS、swap、温度和失败样本。官方在其他 Xeon 或 GPU 上公布的速度只用于说明能力存在，不能换算成 i3-12100 的交付承诺。

## 冻结条件与下一步

在目标 NAS 上完成以下事项后，才把研究候选写入实现依赖：

1. Debian 13 上 OpenVINO 与 ONNX Runtime 的离线安装和 smoke test。
2. 建立同意使用的家庭照片基准集与标注规则。
3. 跑完推荐矩阵；按“EmbeddingGemma 2 过门禁则采用，否则比较 Qwen INT4，二者均失败才回退 SigLIP”的规则冻结语义模型 revision、哈希、精度、线程数、向量维度、instruction、标签阈值和人脸聚类阈值。
4. 生成模型许可证清单和 NOTICE；任何权重许可不清的候选不得下载进正式发行包。
5. 先交付中文检索、受控标签、OCR、人脸聚类和结构化摘要；自由中文描述只有在许可、中文事实性、延迟和 3 GiB 内存门禁全部通过后，才作为按需实验能力进入首版。
