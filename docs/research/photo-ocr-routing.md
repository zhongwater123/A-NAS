# 相册文字识别的分流评估

> 状态：完成（2026-11-02，开发机，公开数据集）；选用照片已有的向量判断哪些照片要读文字，用户 2026-11-02 确认目标召回 99%  
> 更新日期：2026-11-02  
> 适用范围：EmbeddingGemma 2 740M（`embeddinggemma-2-740m@e7a8a2204b91`，相册向量用的 512 px 缩略图、70 个视觉 token）；PP-OCRv6 small / tiny 文字检测（ONNX Runtime 1.31.0）  
> 关联：[实施方案](../architecture/photo-ocr.md#哪些照片读文字)、[OCR 模型评估](photo-ocr-model-evaluation.md)

## 背景

Qwen3.5-4B 在实验 NAS 上读一张照片中位 105 秒（[模型评估](photo-ocr-model-evaluation.md#qwen35-4b-在实验-nas-上2026-10-30实施步骤-1)），2 万张的图库全部读一遍要 24 天的空闲 CPU。所以只读证件、票据、截图、文档这类照片。分流要做到：

- 这类照片几乎都找到（召回接近 100%）；
- 普通照片很少误选，招牌、包装、衣服上的字也一样：每误选一张要多花一两分钟。

## 数据

全部是公开数据，只在开发机上使用。阈值在一半照片上定，下表的结果都来自另一半（按照片键的哈希固定分半）。

| 类别 | 来源 | 张数 | 其中测试用 |
|---|---|---|---|
| 要读 | CC-OCR：中文文档 zh_doc（100）、文档照片 doc_photo_chn（75）、票据 COLD_SIBR（400）、卡证与单据 COLD_CELL（600）、英文小票 CORD（100）、英文表单 FUNSD（50）、中文手写 zh_handwriting（50） | 1,375 | 683 |
| 不读：带字的场景 | CC-OCR：中文街景 zh_scene（450）、TotalText（300）、IC15（500） | 1,250 | 646 |
| 不读：普通照片 | COCO 2017 val（5,000）、标签校准用的 Open Images 子集（8,700）、Gallagher 家庭相册（589） | 14,289 | 7,016 |
| 另列：网图 | CC-OCR：ugc_laion（400）、zh_dense（50） | 450 | 237 |

CC-OCR 是 MIT 许可。“普通照片”里也有少量真正的文档：误选最多的几张中有一张身份证、一张钞票、一部拿在手里的手机屏幕。所以普通照片的误选率是上限。

## 方法

- **向量相似度**：相册已经为每张照片存了 EmbeddingGemma 向量。把“证件、身份证、银行卡、票据、发票、收据、银行回单、截图、文档、表格、手写笔记……”等 25 个中文查询和 6 个英文查询（document、form、receipt、invoice、ID card、screenshot）按搜索的方式编码，照片向量与它们的余弦相似度取最大值作为分数。不增加模型，分流几乎不花时间。
  - 也试了“减去与人物合影、风景、美食、街道、商店招牌等的最大相似度”，结果更差，不用。
- **文字检测**：PP-OCRv6 的 small（9.9 MB）与 tiny（1.8 MB）检测模型（Apache-2.0，官方 ONNX，`PaddlePaddle/PP-OCRv6_*_det_onnx`）。输入与物体检测相同的 1600 px 渲染图，再缩到长边 640 或 960 px。从概率图取三个特征：文字像素占比、文字框数、文字框面积占比，阈值沿用模型自带的 0.2 / 0.45。
- **两者之一**：向量或检测框数任一过阈值就读。
- 每种方法的阈值按“测试用之外那一半的要读照片保留 99% / 98% / 95%”来定。

## 结果

测试那一半上被选中的比例：

| 方法 | 目标 | 要读 | 普通照片 | 带字的场景 | 网图 |
|---|---|---|---|---|---|
| **向量相似度** | 99% | **99.3%** | **1.4%** | **2.3%** | 4.6% |
| 向量相似度 | 98% | 98.8% | 0.9% | 0.9% | 3.8% |
| 向量相似度 | 95% | 95.0% | 0.3% | 0.5% | 2.1% |
| 检测框数（tiny，640 px） | 99% | 99.0% | 5.3% | 13.8% | 23.2% |
| 检测框数（small，640 px） | 99% | 99.0% | 4.7% | 12.2% | 21.5% |
| 检测框数（small，960 px） | 99% | 99.1% | 6.2% | 17.8% | 21.5% |
| 文字面积占比（tiny，640 px） | 99% | 98.1% | 19.7% | 36.2% | 99.2% |

- **向量相似度在每一档都最好**：同样保留 99% 的要读照片，普通照片的误选只有检测框数的三分之一到四分之一，带字的场景只有五分之一到八分之一。
  - 文字检测分不清“一张票据”和“一条满是招牌的街”，文字面积占比尤其如此。
  - 两者之一的组合更差：检测带来的误选全部加进来，召回却没有提高。
- **向量相似度按子集**（目标 99%）：

  | 子集 | 选中 | 子集 | 选中 |
  |---|---|---|---|
  | zh_doc | 97.8% | COCO | 0.6% |
  | doc_photo_chn | 100% | Open Images | 2.0% |
  | COLD_SIBR | 99.5% | Gallagher 家庭相册 | 0% |
  | COLD_CELL | 99.1% | zh_scene | 4.8% |
  | CORD | 100% | TotalText | 2.4% |
  | FUNSD | 100% | IC15 | 0% |
  | zh_handwriting | 100% | zh_dense（密集文字网图） | 44% |

- **英文查询**：只用中文查询时，英文表单 FUNSD 只找到 74%，加入 6 个英文查询后为 100%；整体召回也从 98.7% 升到 99.3%（同为目标 99%），其他三类的误选都下降（普通照片 1.7%→1.4%，带字的场景 7.3%→2.3%）。家庭相册里也有英文票据与表单，所以保留。
- **速度**：向量相似度是 31 次点积。文字检测在开发机上每张约 0.09 s（tiny，640 px）到 0.23 s（small，960 px），含渲染；因为准确率不如向量，没有再测 NAS。

## 代价估算

按图库中 3% 是要读的照片估算（这个比例是假设，真实图库部署后由 AI 状态的 `textPhotos` 给出）：

| 目标 | 2 万张中选中 | 读完要的空闲 CPU | 每 600 张要读的照片漏掉 |
|---|---|---|---|
| 99% | 约 870 张 | 约 25 小时 | 约 4 张 |
| 98% | 约 760 张 | 约 22 小时 | 约 7 张 |
| 95% | 约 630 张 | 约 18 小时 | 约 30 张 |

这是首次读完整个图库的一次性代价；之后每天只读新照片。

## 决定与待定

- **用向量相似度分流，不加文字检测模型**：更准，没有新模型要交付，也不占内存与 CPU。
- **目标召回取 99%**（阈值 0.6926），Claude 建议，用户 2026-11-02 确认。依据：方案要求召回接近 100%；漏掉的票据比多花的几小时一次性空闲 CPU 代价更大。
- **截图不另设规则**：查询中已有“截图”“手机截图”“screenshot”。公开数据里没有手机截图，所以截图的召回没有测到；部署后用真实图库核对。
- 改目标只需重新生成 `internal/photos/textroute/route.json`（见“复现”）。

## 局限

- 都是公开数据。真实的身份证、银行回单与支付截图没有测（[#128](https://github.com/zhongwater123/A-NAS/issues/128)）。
- “普通照片”混有少量真正的文档，误选率偏高。
- 阈值只对这一个向量模型有效；换模型要重新校准，产品在模型不符时不分流。

## 复现

在开发机的 WSL 中（数据集在 `D:\A-NAS-datasets`，模型在 `D:\A-NAS-models`；镜像 `anas-ai-dev-ort:trixie` 见 [AI 开发与评估](../development/ai-development.md)）：

```bash
AI="docker run --rm -v $PWD:/src -w /src/ai -v /mnt/d/A-NAS-datasets:/datasets -v /mnt/d/A-NAS-models:/models:ro \
  -v $HOME/.cache/anas-ai-xnnpack:/xnnpack -e PYTHONDONTWRITEBYTECODE=1 anas-ai-dev-ort:trixie /opt/anas-ai/bin/python"
$AI -m eval.embed --model /models/embeddinggemma-2-740m/embeddinggemma-2-740m.litertlm --weight-cache /xnnpack \
  --tokens 70 --sets cc-ocr,gallagher --no-texts
$AI -m eval.text_detect --model /models/ocr/pp-ocrv6/tiny/inference.onnx --side 640   # 只为比较
$AI -m eval.ocr_route --model /models/embeddinggemma-2-740m/embeddinggemma-2-740m.litertlm \
  --embeddings "/datasets/cache/<模型 ID>-e512" --detections /datasets/cache/text-detect/<运行>.json \
  --report /datasets/runs/ocr-route/report.json --write /src/internal/photos/textroute/route.json --target 0.99
```

CC-OCR 的 TSV 放在 `cc-ocr/` 下，[`ai/eval/text_sets.py`](../../ai/eval/text_sets.py) 第一次使用时把图片写到 `cc-ocr/images/`。COCO、Open Images 与 Gallagher 的向量多数已在标签校准时算过。原始结果在开发机 `D:\A-NAS-datasets\runs\ocr-route\`。

## 关联

- 代码：[`ai/eval/ocr_route.py`](../../ai/eval/ocr_route.py)、[`ai/eval/text_detect.py`](../../ai/eval/text_detect.py)、[`ai/eval/text_sets.py`](../../ai/eval/text_sets.py)
- 产品：[`internal/photos/textroute`](../../internal/photos/textroute/textroute.go)、[`internal/photos/textrouting.go`](../../internal/photos/textrouting.go)
