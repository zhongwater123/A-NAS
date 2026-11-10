# 技术研究

研究文档保存尚需通过原型或实机基准验证的外部技术事实、候选比较和证据链接。可逆的依赖选择保留在研究或相关技术设计中；只有难以逆转且存在真实权衡的决定才创建 ADR。

## 索引

- [本地照片 AI 模型与 Runtime 选型](photo-ai-model-runtime-selection.md)
- [相册 AI 标签校准与中文检索评估](photo-ai-label-calibration.md)（完成；2026-10-10 起按 AI 聚合从严重新校准，2026-10-12 加入 COCO train 子集）
- [相册物体检测与混合检索可行性](photo-ai-object-detection.md)（完成；D-FINE Objects365，[实施方案](../architecture/photo-objects.md)已确认）
- [应用目录扩充评审（2026-10）](app-catalog-review-2026-10.md)（完成；入选应用待实机安装验证）
- [Hermes Agent 作为 AI 助手运行时的可行性](hermes-agent-runtime.md)（按主题整理的当前结论，2026-10-14 至 10-21 的按日记录见其中的“历史”；实验 NAS 实测待做）
- [备份中心 C 端功能评审（2026-10）](backup-center-review-2026-10.md)（完成；快照侧缺口已补齐，外部备份待 #27 决定）
- [相册人物与宠物的模型评估](photo-people-evaluation.md)（已冻结；人脸 YuNet + AuraFace，宠物个体 DINOv2-small）
- [相册证件与票据 OCR 模型评估](photo-ocr-model-evaluation.md)（完成；2026-10-30 定为 Qwen3.5-4B 的 Q4_K_M，需核对层；2026-11-09 补测 Xiaomi-OCR-0 不替换，并与业内做法对照；11-10 试过的几种额外核对都没有帮助；真实证件与银行回单未测）
- [相册文字识别的分流评估](photo-ocr-routing.md)（完成；用照片已有的向量判断哪些照片要读文字，PP-OCRv6 检测更差不用；目标召回 99%）
- [网页搜索的引擎实测](web-search-engines.md)（开发机完成；332 个引擎全部接入，各分类的用时与回答情况；百度与夸克受第一次错误测量影响；实验 NAS 待测）
- [Qdrant 检索特性试验（1.20.0）](qdrant-retrieval-features.md)（完成；开发机试验，视频片段、文件块与照片各用哪些特性，照片何时可切换）
