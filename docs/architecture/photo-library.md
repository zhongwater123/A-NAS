# 相册技术设计

状态：核心链路可开始本地实现；模型质量与资源参数待基准冻结

本设计落实[相册规格](../specs/photo-library.md)。受管图库的长期边界由 [ADR 0006](../adr/0006-use-a-managed-photo-library.md) 决定；模型与 Runtime 的候选证据见[本地照片 AI 研究](../research/photo-ai-model-runtime-selection.md)。

## 开发就绪结论

当前粒度足以实现目录、原图存储、导入、浏览、回收站、重复组、派生任务和 Fake AI Provider。这些能力只依赖已经确认的照片资产、图库归属、稳定 ID 与生命周期，不需要等待真实数据盘或最终模型。

以下事项作为发布闸门并行推进，不阻塞核心链路编码：

- 多用户真实登录与会话尚未完成；本地实现先使用显式 `Principal` 和 Fake Policy，不能据此宣称私有图库权限已经可发布。
- Experimental NAS 暂时离线且数据盘尚未接入；本地目录 Adapter 和临时卷只验证契约，不能替代真实数据卷、断盘和恢复验收。
- AI 模型已有基线候选，但质量、处理速度、峰值内存、温度和前台影响必须通过代表性图库基准后才能成为发布默认值。
- RAW、Live Photo 和视频的完整格式矩阵仍需样本验证；不影响 JPEG/PNG 核心切片。

## 组件与数据流

```text
Web / 文件管理图库投影 / 只读 SMB-NFS
                    │ 稳定照片资产 ID
                    ▼
             Photo Library Module（Go）
        ┌───────────┼────────────┬─────────────┐
        ▼           ▼            ▼             ▼
   Policy       SQLite Catalog  Object Store  Preview/Metadata
                    │            Adapter       Adapter
                    │ durable job
                    ▼
              AI Job Coordinator
                    │ 窄协议；不授予访问权限
                    ▼
          非特权 AI Worker（Python）
       OpenVINO Provider / ONNX Runtime 回退
                    │ versioned result
                    ▼
       Derived Result Store → FTS / Exact Vector Search
                    │
                    ▼
                 Policy 过滤
```

Photo Library Module 是权威写入边界。AI Worker 不能直接修改照片目录、权限、用户标签或原图，只能返回带版本的候选派生结果；Module 验证任务租约、照片资产状态与结果版本后再提交。

## 模块接口

首个代码入口计划放在 `internal/photos`，由一个较深的 Module 封装以下 Implementation：

- `Import`：流式校验、计算内容哈希、持久化不可变原图，并原子创建照片资产和派生任务。
- `Get`、`List`、`Search`：只接受调用方身份与稳定 ID，不接受调用方拼接的底层路径。
- `Move`、`Rename`、`CopyToAlbum`、`CopyToShared`：实施虚拟组织与独立照片资产语义。
- `Trash`、`Restore`、`Purge`：实施图库与上传者权限、15 天期限和最后引用回收。
- `RecordUserMetadata`：保存用户标签、AI 纠错、人物名称和手工位置，不与派生结果混写。

首版不为目录表、对象路径、SQLite、OpenVINO 或某个向量扩展建立公共 Interface。真正需要 Adapter 的边界只有外部环境或昂贵依赖：受管存储目录、媒体解码、AI Provider、时钟和 Policy。

## 持久化与崩溃一致性

### 目录

首版目录、权限关系、回收站期限、任务、用户元数据和派生结果使用一个位于本地数据卷的 SQLite 数据库。启用 WAL、外键和事务；不把该数据库放在 SMB/NFS 等网络文件系统，也不额外引入 PostgreSQL、Redis 或消息队列。

关键身份分为：

- `library_id`：私有或共享图库。
- `asset_id`：用户可见照片资产的稳定 ID。
- `object_id`：原文件字节 SHA-256；只用于内部对象定位和完全重复判断。
- `derivation_id`：能力、模型摘要、预处理版本和 Pipeline 版本的组合。
- `job_id`：可租约、重试和幂等执行的派生任务。

### 原图提交

1. 在目标数据卷的 staging 目录流式写入并同时计算 SHA-256，不先把整个文件读入内存。
2. 校验格式与大小，刷新文件后原子移动到按内容哈希分层的对象路径；已存在时复用对象。
3. 在单个数据库事务中创建独立照片资产、对象引用、重复组关系与待处理任务。
4. 崩溃后由对账任务删除超过安全期且没有目录引用的 staging 或孤立对象；任何自动回收都不能触碰仍有引用的对象。

对象路径不是资源身份。备份和恢复必须同时覆盖对象、目录以及二者的校验清单。

## 后台任务与资源隔离

SQLite 任务表保存 `capability`、输入资产、期望派生版本、状态、尝试次数、`lease_until` 和稳定错误类别。Worker 以短租约领取单张、单能力任务；重复执行必须由 `(asset_id, derivation_id)` 唯一约束收敛为同一当前结果。

默认执行策略：

- 连续 5 分钟无前台活动后才领取新任务，并发 1，每次只让一个模型族常驻。
- 检测到上传、下载、播放、交互、内存压力、I/O 压力或温度上限后立即停止领取新任务；已经开始的单张原子推理允许完成。
- 初始 systemd 预算为 `CPUQuota=200%`、`MemoryHigh=2G`、`MemoryMax=3G`、低 CPU/I/O 优先级；这些是安全起点，不是实机结论。
- OOM、进程退出和重启只会让租约到期并重试，不得影响原图与基础相册。

## AI 模型基线

模型名称属于可升级配置，不进入产品 API。首版候选如下：

| 能力 | 首选基线 | 选择理由 | 冻结条件 |
|---|---|---|---|
| 中文语义向量与开放标签 | 已选定 Google EmbeddingGemma 2 文本+视觉 440M；官方 LiteRT QAT 包 | Google DeepMind 于 2026-10-06 发布、Apache-2.0、支持 100+ 语言；文本 INT4、视觉 INT8 的官方包约 388 MB，专门面向本地多模态检索、零样本分类和聚类 | 作为首个集成目标；中文家庭照片质量、Debian x86 Runtime、RSS、查询延迟与吞吐达标后冻结为发行默认；发布过新必须保留回退 |
| OCR | PP-OCRv6 small detection + recognition | Apache-2.0，覆盖简繁中文与英文；官方定位于移动端/桌面端并提供 OpenVINO CPU 路径 | 与 tiny 对比家庭照片文字质量、方向、长图、模糊、吞吐和峰值内存 |
| 人脸检测与聚类向量 | Open Model Zoo `face-detection-retail-0004` + `landmarks-regression-retail-0009` + `face-reidentification-retail-0095` | 三段均有 Apache-2.0 模型清单、模型很小并直接运行于 OpenVINO | 在家庭合照、侧脸、儿童成长和误合并样本上校准阈值；不满足质量则不默认发布 |
| 按需中文描述 | 高置信标签、OCR、时间和地点的可追溯结构化摘要 | 无需常驻 VLM，中文稳定，可逐项说明事实来源并避免把幻觉写入检索事实 | 小型可商用 VLM 通过中文质量、3 GB 上限和响应时间基准后，才能替换为自由生成 Provider |

图文 Embedding 模型不直接输出固定类别。系统维护版本化中文标签词表，为每个标签预计算文本向量，按标签独立校准阈值，只展示超过阈值的少量候选；不能用一个全局阈值承诺识别所有物体。“猫”“电动车”“3D 打印机”即使没有形成可见标签，仍可通过文本与图片向量相似度参与语义搜索。

Google EmbeddingGemma 2 已选定为首个集成目标：它在本设计更新前一天发布，支持中文在内的 100+ 语言，并能只加载文本与图像模块；其规模与官方本地 CPU 定位都比 2B/3B 候选更符合 8 GB NAS。但“适合消费硬件”不等于已经在 Debian 13 x86 上通过 A-NAS 门禁；官方跨平台量化内存数字也不能代替本机加载、吞吐和长时间运行实测。因此 SigLIP2 Base 保留为成熟回退，EmbeddingGemma 2 通过门禁后才冻结为发行默认。

Qwen3-VL-Embedding-2B 继续作为有官方 OpenVINO INT4 路线的成熟质量与 Runtime 对照。腾讯微信视觉团队于 2026-08 发布的 WeMM-Embedding-2B 基于 Qwen3.5，并报告了高于 Qwen3-VL-Embedding-2B 的 MMEB-v2 结果，但模型仓库实际标为约 3B、BF16 权重约 5.44 GB，官方示例以 CUDA 为主，尚无适合本项目的 x86 CPU 量化交付路线，所以只在开发机上定义新一代质量上限，不要求安装到目标 NAS。Chinese-CLIP RN50 只保留为轻量中文对照。

人脸向量与聚类结果属于派生数据；人物实体、用户命名和人工合并/拆分是用户数据。重新聚类只能提出关联，不能删除或改写已经确认的人物名称。首个开发切片先保存稳定 `face_occurrence_id` 和向量，聚类算法在样本基准后冻结。

生成式 VLM 不进入 8 GB 设备的首版默认后台队列。它的推理成本与自由生成长度相关，且会引入幻觉、中文质量与模型许可问题；这与只做一次固定维度向量或小分类模型不是同一资源级别。

## Runtime 与进程边界

- Go 产品服务负责所有权威状态、权限、任务租约、结果提交和检索组合。
- Python AI Worker 负责模型预处理与推理，通过只在本机可用的窄协议接受 `capability`、只读输入句柄和期望版本，返回结构化结果。
- EmbeddingGemma 2 以官方 LiteRT-LM 文本+视觉 QAT 包作为首选发行路径，并用 Transformers/Sentence Transformers FP32 结果抽样核对量化正确性；人脸模型和 PP-OCRv6 优先使用 OpenVINO CPU Provider。UHD 730 仅作为后续实机对照，不把 GPU 驱动可用性作为首版前提。
- Debian 13 不在当前 OpenVINO 官方支持发行版列表中，因此必须在目标系统完成离线安装、模型加载、连续运行和服务重启测试；失败时回退 ONNX Runtime CPU，不能让 Runtime 兼容性阻塞基础相册。
- 模型文件必须随清单记录来源、许可证、SHA-256、预处理版本与输出 schema；安装后离线运行，不在推理时访问互联网。

## 搜索设计

结构化查询、文件名、用户标签、AI 标签和 OCR 使用 SQLite 与 FTS5。语义向量按 `derivation_id` 分代保存；在 20,000 张照片基线下，首版把当前代归一化向量加载到进程内，针对调用方有权访问的图库执行精确余弦扫描，不引入独立向量数据库。

以 768 维 `float32` 估算，20,000 个图片向量原始数据约 59 MiB；即使候选使用 2,048 维也约 156 MiB，仍适合以简单、可校验的精确索引起步。模型进程内存而不是向量表大小才是 8 GB 设备的主要约束。只有实测规模或延迟不达标时，才在相册 Module 内增加 ANN Adapter。

排序组合语义分数、用户标签、AI 标签、OCR、时间与地点过滤。Policy 必须在候选生成前约束图库范围，并在返回前再次验证资产可见性，索引命中本身不构成授权。

## 开发切片

1. **Catalog**：定义图库、资产、对象引用、重复组、回收站和用户元数据；实现 SQLite migration 与 Fake Policy 契约测试。
2. **Managed storage**：实现 JPEG/PNG 流式导入、崩溃对账、内容对象复用和临时目录 Adapter；不依赖真实数据盘。
3. **Read path**：列表、原图读取、缩略图、虚拟目录、相册副本和回收站 API，并更新 OpenAPI。
4. **AI contract**：实现 durable job、租约、版本化派生结果和 Fake AI Provider，先证明 AI 完全停止时基础相册仍工作。
5. **Model benchmark**：在独立原型中比较候选模型，冻结模型摘要、资源参数与标签阈值后接入本地 Worker。
6. **Search and people**：接入 FTS、精确向量搜索、OCR、人脸 occurrence 与分图库聚类。
7. **Multi-user and imports**：接入真实 Auth/Policy、共享图库、管理员查看审计、NAS 文件夹与 USB 导入。
8. **Release gates**：真实数据盘、格式矩阵、备份恢复、断盘、容量保护和 4 用户/20,000 照片/2,000 视频压力验收。

切片 1 至 4 可以立即本地开发；切片 5 与其并行。切片 7 的真实权限验收等待身份系统，但数据模型和 Fake Policy 测试无需等待。

## 模型基准与发布门槛

基准集使用至少 1,000 张具有授权的代表性家庭照片，并保留不参与阈值调节的独立测试集；至少包含 100 条中文自然语言查询、常见物体与场景标签真值、含中文文字照片以及同人跨年龄/姿态的人脸样本。

必须记录：

- 语义检索 `Recall@10`、人工可接受率和典型失败查询。
- 每个可见 AI 标签的 precision/recall 与独立阈值，重点控制错误标签展示率。
- OCR 字符准确性与无文字照片误检率。
- 人脸漏检、误检、同人拆分和异人误合并；误合并权重高于漏合并。
- 单张与整库照片/分钟、首轮建库时间、Worker 峰值 RSS、CPU、I/O、温度与功耗。
- Worker 空闲、运行、停止领取任务和完成当前原子任务时的前台 API p95 延迟。

目标 NAS 的部署门禁按 EmbeddingGemma 2、Qwen3-VL-Embedding-2B、SigLIP2 Base、Chinese-CLIP RN50 的顺序验证；开发机资源允许时另跑 WeMM-Embedding-2B BF16，定义中文质量上限但不把它当作 8 GB 部署候选。任何候选只要许可证不能确认、峰值 RSS 超过 3 GB、导致前台明显卡顿或中文质量不达标，就不成为默认模型；功能可以显示为等待兼容模型，而不是降低基础相册可靠性。

## 关联

- [架构总览](OVERVIEW.md)
- [相册规格](../specs/photo-library.md)
- [受管图库 ADR](../adr/0006-use-a-managed-photo-library.md)
- [本地照片 AI 模型与 Runtime 研究](../research/photo-ai-model-runtime-selection.md)
- [领域语言](../../CONTEXT.md)
