# 相册照片中的文字（OCR）实施方案

状态：proposed（2026-10-30 起草；已确认的决定见下文）
更新时间：2026-11-10

本文把[相册规格](../specs/photo-library.md)中“做 OCR”的决定细化为可实现的方案。后台在空闲时读出照片中的文字，证件、票据、回单还要读出关键字段；文字和字段可以搜索，可见范围与照片相同。依据：
- 候选、方法与开销：[模型评估](../research/photo-ocr-model-evaluation.md)；
- 4B 级候选：[#323](https://github.com/zhongwater123/A-NAS/issues/323)；
- 内存规则：[资源策略](photo-objects.md#资源策略)。

方案复用现有的 AI Worker、持久任务、空闲门控与内置交付（[相册本地 AI](photo-ai.md)、[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）。

## 目标与范围

完成后，成员可以：

- 用照片里的文字找照片，例如“发票”“招商银行”“6222”“城南初中”；
- 在证件、银行卡、回单、发票、小票的照片上看到读出的关键字段，复制使用；
- 看到哪些字段需要自己核对，并且可以更正，更正后的值以后不会被重新读取覆盖。

不在本方案内：
- 视频中的文字；
- 文件空间里的 PDF 与文档；
- 翻译；
- 手写体的专门优化；
- 把敏感号码交给云端模型：AI 助手可以引用它们，但看不到具体的值（见“AI 助手：能用，看不到具体的值”）。

已确认的决定（经过与理由见各篇决定记录）：

- [2026-10-19：做 OCR](../decisions/2026-10-19-photo-ocr.md)，可见范围与照片相同，管理员只在“查看私有图库”授权期间只读可见。
- [2026-10-28：AI 模型不设固定内存预算](../decisions/2026-10-28-ai-memory-headroom.md)：后台只在加载后仍有足够余量时才加载模型，有人在等的请求不受限制。余量现为 1 GiB，下文的“余量”都指这个值。
- [2026-10-30：模型用 Qwen3.5-4B 的 Q4_K_M](../decisions/2026-10-30-ocr-model.md)。
- [2026-10-30：采纳本方案的五项建议](../decisions/2026-10-30-ocr-plan-defaults.md)：首批文档类型与字段、敏感号码默认遮挡（2026-11-06 改为[界面上不遮挡](../decisions/2026-11-06-ocr-no-masking-in-the-ui.md)）、开关默认开启、共享图库成员都可以更正、手动读取放到后续版本。
- [2026-11-06：点开完整号码不记录](../decisions/2026-10-30-ocr-full-number-reveal-logging.md)、[敏感字段的清单](../decisions/2026-10-30-ocr-sensitive-fields.md)：住址不算敏感，手机号不挡助手看照片。
- [2026-10-30：AI 助手能用敏感字段，但看不到具体的值](../decisions/2026-10-30-ocr-assistant-handles.md)，见“AI 助手：能用，看不到具体的值”。
- [2026-11-02：分流用照片已有的向量，目标召回 99%](../decisions/2026-11-02-ocr-routing-recall.md)。
- [2026-11-03：字段核对的单字概率取 0.99](../decisions/2026-11-03-ocr-character-confidence.md)。
- [2026-11-03：更正按内容对象存，修改记录对能看到照片的人公开](../adr/0033-keep-photo-text-corrections-per-content-object-with-a-change-log.md)（ADR 0033，修订上面五项建议中“更正记入活动记录”一项）。

## 整体结构

```text
相册服务（Go）
  ├─ 门控：空闲、压力、加载前的内存判断（余量）
  ├─ 任务：ocr/v1，按“是否有字”分流后排入
  └─ AI Worker 套接字 ──► AI Worker（Python，沙箱：无网络、看不到数据卷）
                            ├─ 向量、检测、人脸、宠物（已有）
                            └─ 文字：按需启动 llama-server 子进程
                                 ├─ 只监听 Worker 私有目录里的 UNIX 套接字
                                 └─ Qwen3.5-4B Q4_K_M + 视觉 F16
```

## 模型与运行

### 模型

- [Qwen3.5-4B](https://huggingface.co/Qwen/Qwen3.5-4B)，阿里，Apache-2.0。官方没有发布 GGUF，从固定 revision 的官方权重自己转换：
  - 语言部分转成 Q4_K_M，与 #323 实测的相同；
  - 视觉部分转成 F16，与评估中的 Qwen3.5-2B 相同。
  - 转换用 [`scripts/convert-ocr-model.sh`](../../scripts/convert-ocr-model.sh)：同一工具版本转换两次，逐字节相同才输出，再与清单核对。
- 两个模型文件各有一份清单（[`deploy/models/qwen3.5-4b-q4_k_m.json`](../../deploy/models/qwen3.5-4b-q4_k_m.json)、[`deploy/models/qwen3.5-4b-mmproj-f16.json`](../../deploy/models/qwen3.5-4b-mmproj-f16.json)），记录：来源、revision、转换命令、llama.cpp 版本、大小、SHA-256、`memoryBytes`；许可全文是 [`deploy/models/qwen3.5-4b.LICENSE`](../../deploy/models/qwen3.5-4b.LICENSE)。
- 推理设置沿用评估：
  - 非思考模式（`enable_thinking: false`），贪心解码；
  - 输入是相册服务已经为检测渲染的 1600 px 图，每张最多 1024 个视觉 token；
  - `--cache-ram 0 --ctx-checkpoints 0`，否则内存会一直涨，直到被内核结束；
  - 上下文按“视觉 token + 提示词 + 最长输出”定，先用 8192，实测后再调；输出最多 2048 个 token。

### llama.cpp

- 产品用上游的 llama-server，不自写推理封装，也不用 llama-cpp-python。原因：
  - llama-cpp-python 的多模态支持落后于上游；
  - 评估依赖 llama-server 的两项能力：逐字概率（`logprobs`）、断开连接即停止生成。
- 版本像 Qdrant 一样用清单固定（`deploy/llama/llama.json`，参照 [`deploy/qdrant/qdrant.json`](../../deploy/qdrant/qdrant.json)）：
  - 用上游发布的 Linux x64 CPU 构建 b11655：它是评估所用的 e36df60 加一个只改 Vulkan 的提交，CPU 部分相同。它按 CPU 能力在运行时选择实现，换一台 NAS 也能用。
  - 它在 Debian 13 上运行，支持 Qwen3.5 的视觉部分与 UNIX 套接字；依赖 glibc 2.34 以上、libgomp1 与 libssl3t64，安装器先检查这两个库。
  - 清单的 `members` 列出从压缩包中取的文件：llama-server、它加载的库与各 CPU 变体、许可，其他工具的库不取。开发机与 CI 用 [`scripts/fetch-llama.sh`](../../scripts/fetch-llama.sh) 取到 `build/llama/<sha256>/`；安装器核对压缩包后把这些文件放在 `/opt/a-nas/llama/<sha256>/`，release 以 `ai/llama` 链接过去，与 Qdrant 相同。
  - 换版本后先在开发机上用同一批样本核对输出，再在 NAS 上测速度。

### 在 Worker 中运行

- **启动**：Worker 在第一次读文字时，以子进程启动 llama-server：
  - 监听 Worker 私有临时目录里的 UNIX 套接字（`--host <目录>/ocr.sock`）；llama-server 地址以 `.sock` 结尾即监听 UNIX 套接字，开发机上的版本已核对。
  - 线程数为可用核数减 2。
  - 子进程继承 Worker 的沙箱：只能用 UNIX 套接字、没有网络、看不到数据卷；内存计入 Worker 的 cgroup。
  - Worker 用标准库 `http.client` 通过这个套接字发请求，只需让连接改用 UNIX 套接字。
  - 第一次启动前按清单核对两个模型文件的 SHA-256，并在 Worker 的缓存目录（`/var/cache/a-nas-ai`）按文件在磁盘上的身份（设备、inode、大小、修改与变更时间）记下核对过：Worker 闲置退出后再启动，不必重算 3.4 GB 的哈希。文件换过或改过就重新核对。
  - llama-server 的日志写在同一个私有目录；300 秒内没就绪，或启动后退出，`read_text` 答“不可用”并附日志末尾。
  - 核对哈希与等待就绪期间也响应挂断：Worker 一次只处理一个请求，否则有人搜索时要排在模型加载之后。挂断时直接结束子进程（[#365](https://github.com/zhongwater123/A-NAS/issues/365)）。
- **新操作 `read_text`**：输入一张图的描述符和读法：
  - “全文”；
  - 或“字段”，附带文档类型的 JSON 模板。

  返回模型 ID、文字（字段是 JSON 文本）、每个 token 的对数概率，以及结束原因：正常结束、达到长度上限，或出现循环（Worker 发现输出在重复时自己停下）。每个字段的最低单字概率由相册服务按 token 算（步骤 3）。相册服务挂断连接时，Worker 关闭到 llama-server 的连接，生成随即停止。
  - **循环**：回答末尾同一段内容重复至少 8 次、合计至少 128 个字符，才算循环。小票的分隔线、目录的引导点、表格的填空线只是一两行同一个字符，几行相同的“0.00”也不算（#365）。没有字母、数字或汉字的内容，例如表格的空行 `|  |  |`，要合计至少 512 个字符：表单常有几十行空行，下面的合计、日期和签名不能丢。
  - **出错**：
    - llama-server 拒收请求（4xx）答“输入无效”，这张照片不再重试；
    - llama-server 自己出错，包括 5xx、流中途报错、流结束却没说原因，答内部错误，并停掉这个 llama-server，下一次读取重新启动它。照片稍后重试，计一次尝试；
    - 三次都失败的照片记为失败，相册服务重启也不再读：失败多半出在读文字的模型读这张照片上，每次要几分钟。相册服务没见过的读文字模型才把它们重新排队，记录在相册目录的 `ai-worker.json`；
    - 相册服务也只接受三种结束原因，其他的不保存，这张照片记为结果无效（#365）。
  - 模型 ID 包含两个模型文件的哈希、llama.cpp 版本与视觉 token 上限，例如 `qwen3.5-4b-q4_k_m@<12 位>+qwen3.5-4b-mmproj-f16@<12 位>+llama.cpp-b11655+img1024`：其中任何一项变了，读出的文字都可能不同。
- **释放与退出**：`release` 时结束子进程；Worker 空闲退出或被 SIGTERM 停止时，先结束子进程再退出。
- **内存**：
  - OCR 模型的 `memoryBytes` 用扩展后的 [`ai/eval/worker_memory.py`](../../ai/eval/worker_memory.py) 测量。
  - Worker 在 `info` 中把它与现有模型分开报告（`ocrMemoryBytes`），因为读文字时不会同时加载检测类模型（见“资源策略”）。
- **测试替身**：Fake Provider 按图片哈希返回确定的文字与字段，供 Go 测试与系统测试使用。

## 哪些照片读文字

- **为什么要筛**：Qwen3.5-4B 在 NAS 上每张中位 105 秒，满负荷每小时约 34 张（步骤 1 实测）。2 万张的图库全部读一遍要 24 天的空闲 CPU，所以只读有字的照片。
- **方法：照片已有的向量**（步骤 2 评测选定，[分流评估](../research/photo-ocr-routing.md)）：
  - 相册已经为每张照片存了 EmbeddingGemma 向量。照片向量与“证件、票据、发票、收据、银行回单、截图、文档、表格、手写笔记”等 25 个中文查询和 6 个英文查询中任一个的相似度达到阈值，这张照片就排入读文字。不增加模型，不占内存，分流只是 31 次点积。
  - 查询与阈值在 [`internal/photos/textroute/route.json`](../../internal/photos/textroute/route.json)，由 [`ai/eval/ocr_route.py`](../../ai/eval/ocr_route.py) 在公开数据上校准并写出，只对校准时的模型有效；Worker 换了模型就不分流。
  - 查询按搜索的方式编码，与 AI 聚合的标签文字一起向 Worker 取一次，存进目录库。
  - 新照片在存下向量时分流；之前存下的向量（查询还没有向量时，或换了路由）每次启动后分流一次，不需要 Worker。
  - AI 状态的 `textPhotos` 报告调用者能看到的照片中被选中的张数。
- **评测结果**（公开数据，阈值在一半照片上定，在另一半上报告）：目标召回 99% 时，文档、票据、卡证选中 99.3%，普通照片 1.4%，招牌等带字的场景 2.3%。
  - PP-OCRv6 文字检测同样召回下普通照片误选 4.7–6.2%、带字的场景 12–18%，所以不用。
  - 两者组合更差。
- **截图不另设规则**：查询中已有“截图”“手机截图”“screenshot”。公开数据里没有手机截图，部署后用真实图库核对。
- **顺序**：新照片先读，最近的票据和证件最常用到。

## 读取、判型与核对

### 两步读取

1. **全文**：用评估中的转写提示词读出全部文字。
2. **判型**：按全文中的关键词用规则判定文档类型，不再调用模型。按下面的顺序试，先具体后一般：
   - 身份证正面：出现“公民身份号码”；
   - 身份证背面：同时出现“签发机关”“有效期限”；
   - 发票：出现“发票代码”或“发票号码”；
   - 银行回单：出现“回单”“交易流水”或“转账凭证”；
   - 银行卡：全文不超过 300 字，出现“银行”“银联”或 UnionPay，并有一串通过 Luhn 校验的卡号；
   - 小票：出现“合计”“实收”“收据”或“小票”。
3. **字段**：只对判出类型的照片，按该类型的 JSON 模板再读一次。写法与评估中的字段抽取相同，第二次的耗时只花在文档上。模板按下表的顺序列出字段，也就是证件上的顺序；模型按这个顺序填写。

规则、模板与核对在 [`internal/photos/textread`](../../internal/photos/textread/textread.go)，都是纯函数，有单元测试。

### 首批文档类型与字段（2026-10-30 确认）

| 类型 | 字段 |
|---|---|
| 居民身份证 | 姓名、性别、民族、出生、住址、公民身份号码；背面：签发机关、有效期限 |
| 银行卡 | 卡号、发卡行 |
| 银行回单 / 转账凭证 | 付款人、付款账号、收款人、收款账号、金额、日期、交易流水号 |
| 发票 | 发票号码、开票日期、购买方、销售方、金额、税额、价税合计 |
| 小票 / 收据 | 商户、日期、合计 |

### 核对层

评估显示，所有模型带数字的关键字段都有约两成不完全正确，其中 1%–4% 是格式合法、只错了几位的“看不出来的错”。所以字段必须核对，结果只有“通过”与“待核对”两种，从不自动改值。

- **校验位与格式**：
  - 身份证号按 GB 11643 校验位，并与“出生”“性别”一致；
  - 银行卡号用 Luhn 校验；
  - 日期合法；
  - 发票号码的长度；
  - 金额的格式；价税合计等于金额加税额。
  - 金额的大写与小写是否一致没有做：首批模板中没有大写金额字段。
- **与全文交叉核对**：字段的值必须在第一步读出的全文中出现（忽略空格与全半角）。两次读取都得到同一个值，才算通过。
  - 两次读取出自同一个模型，常错在同一处。加上单字概率 0.99 后，它几乎不再多拦下错。
  - 2026-11-10 试过换成另一个模型读的全文、整数匹配、字段名邻近核对，都没有更好（[独立全文对照](../research/photo-ocr-model-evaluation.md#独立全文对照)）。
- **单字概率**：组成值的每个 token 的概率都要达到 0.99。
  - 值在字段名之后找：别的字段的值可能含有同样的数字，例如 113.00 中含有 13.00。
  - 找不到这个值的 token、或者回答没有逐字概率时，不通过。
  - 步骤 3 在实验 NAS 上实测（[核对层实测](../research/photo-ocr-model-evaluation.md#核对层实测qwen35-4b2026-11-02实施步骤-3)）：
    - 只用前两项核对时，91% 的字段通过，其中 12% 是错的；
    - 加上 0.99 后，48% 通过，其中 2.7% 是错的，都是格式或标注不同、读得不全，没有真正读错的；
    - 取 0.9 时会放过错了一位的号码。
  - 这个值的经过见[决定记录](../decisions/2026-11-03-ocr-character-confidence.md)，实现为 `textMinConfidence`。
- **回答没有正常结束**：达到长度上限或因循环被 Worker 停下时，这次回答的字段一律待核对（原因 `unfinished`）。全文照样保存，并在 `texts.finish` 记下结束原因。
- **用户更正**：保存为用户数据。重新读取、更换模型都不会覆盖。

验收指标是“自动通过的字段中错误的比例”。按单字概率 0.99（[决定](../decisions/2026-11-03-ocr-character-confidence.md)），约一半字段自动通过，其中有 2.7% 是格式、标注不同或读得不全，公开样本中没有真正读错的；其余标为“待核对”。

## 任务与存储（Catalog 新 migration）

- **任务**：复用持久任务表，新增派生 `ocr/v1`：
  - 分流判定为有字时排入；
  - 每次读取在 `scans(object_id, derivation, model)` 留一行，模型变了就重新排队，与检测、人脸相同。
- **派生数据**以内容对象为键，副本与重复照片共用：

  ```text
  texts(object_id, model, kind, text, finish, fields, checks, created_at)
  ```

  `kind` 是判出的文档类型或“普通文字”。`finish` 是全文的结束原因：`stop`；或 `length`、`repetition`，表示全文被截断。`fields` 与 `checks` 为 JSON。
- **用户数据**以内容对象为键，所有图库共用，以最后一次修改为准（[ADR 0033](../adr/0033-keep-photo-text-corrections-per-content-object-with-a-change-log.md)，第 16 号迁移）：

  ```text
  text_corrections(object_id, field, value, account, account_name, updated_at)
  text_changes(object_id, field, old_value, new_value, restored, account, account_name, library_id, created_at)
  ```

  - `text_changes` 是修改记录，对能看到照片的成员公开，显示修改人与图库。`library_id` 不加外键，图库删除后记录仍在。
  - 第 15 号迁移按“图库 + 内容对象”存更正；没有代码写过它，第 16 号重建了这张表。

- **存放位置**：目录库在数据卷上（`/srv/a-nas/data/photos/catalog.db`），与照片本身受同样的保护，也进入同样的快照与备份。文字内容不写进日志与活动记录。
- **清除**：照片彻底删除时一并删除。每个图库有“照片中的文字”开关，做法与“人物与宠物”相同：
  - 关闭即清除这个图库独有的派生文字，保留用户更正与修改记录；关着的图库不显示、也不搜索文字与更正，同一张照片在另一个开着的图库里照常显示；
  - 重新开启时补排。
  - 照片复制或导入到读文字的图库时，如果它的向量早已存下（原先只在关了开关的图库里），按存下的向量补排。
  - 接口：`PATCH /api/v1/photos/libraries/{libraryId}` 的 `texts`，权限与人物开关相同（私有图库的主人、共享图库的管理员）；图库的 `texts` 字段报告开关状态。同一请求里的 `people` 与 `texts` 在一个事务中生效。
- **运行**（[`internal/photos/texts.go`](../../internal/photos/texts.go)）：
  - 读文字是 AI 的最后一个阶段：向量与检测、人脸、宠物都没有待做的任务时才读，最新的照片先读（按拍摄时间，没有就按导入时间）。
  - 读之前释放检测类模型，门控按读文字的内存需要判断；有新的检测任务或以图搜图要检测时，先释放读文字的模型，两者不同时加载。
  - 门控关闭时挂断，这张照片退回队列、不计尝试次数。这一轮就此结束，显示为暂停；是否释放模型按暂停原因，与检测相同（处理器或磁盘压力不释放）。
  - Worker 换了读文字的模型（新版本，服务随之重启），已读的照片重新排队。
  - 关闭人物开关原来会删掉这些照片的全部扫描记录，现在只删人脸与宠物的，不再连带清掉读文字的记录。

## 搜索

- **新的一档**：照片中的文字。分档排序为：
  1. 名称与用户标签；
  2. **照片中的文字**；
  3. 检测到的物体；
  4. 语义相似。

  文字是照片上实际写着的，可靠程度接近名称，但它是机器读出来的，界面上要标明（见“界面”）。
- **匹配方式**：查询文字出现在全文、字段值或用户更正中即命中。
  - 数字较多的查询，两边都去掉空格和连字符再比，“6222 0200”也能找到“6222020…”。
  - 只在调用者可见的照片里匹配，走现有的图库 Policy。
  - AI 助手的搜索只匹配脱敏后的文字，见“AI 助手：能用，看不到具体的值”。
- **不用 FTS5**：
  - FTS5 的 trigram 分词要求查询至少 3 个字，“发票”“回单”这类两个字的中文查询会匹配不到；
  - `mattn/go-sqlite3` 还要另加构建标签才能用 FTS5。
  - 家庭规模下有字的照片约几千张，每张约 1 KB 文字，逐行查找在毫秒级。
  - 以后变慢，再考虑 Qdrant 的全文索引（[ADR 0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)）。
- **与红线的关系**：文字由专门的 OCR 模型读出，不经过向量，与“向量模型产生的标签不参与搜索”的红线不冲突（2026-10-19 已确认）。
- **实现**（[`internal/photos/textsearch.go`](../../internal/photos/textsearch.go)）：
  - 文字一档的分数段在名称与物体之间，同一档内仍按向量相似度排；翻页的游标照旧按分数续接。
  - 搜索结果多了 `texts`：所有页中文字命中的张数，界面据此给这一档分组。
  - 只在调用者能列出的、开着“照片中的文字”的图库里找；比较前全文、字段与查询都忽略空格、全半角与大小写。
  - 找的是照片的文字显示出来的内容：全文，以及读出的文档类型的字段，读出的值与更正都算。换了文档类型后不再显示的旧更正不参与。
  - 一个字的查询不找文字：几乎每段文字都含常用字，“猫”会把含“猫粮”的小票排到真正拍到猫的照片前面。

## 接口

[OpenAPI](../../api/paths/photos.yaml) 的相册部分，代码在 [`internal/photos/textview.go`](../../internal/photos/textview.go)：

- `GET /api/v1/photos/assets/{assetId}/text`：照片的文字。
  - `status`：`read`、`queued`、`failed`、`none`，或 `off`（这个图库关了开关）。
  - 读出时还有文档类型、全文、是否完整读完，以及按证件顺序排列的字段：当前值、读出的值、核对结果、是否敏感、谁在何时更正的。
  - 成员更正过的字段算作核对通过，不再列为“待核对”。
  - 完整的值给能看到照片的成员，界面上不遮挡（[决定](../decisions/2026-11-06-ocr-no-masking-in-the-ui.md)）。
- `PUT …/text/fields/{field}`：更正一个字段，`DELETE` 同一地址恢复为读出的值。
  - 能看到照片的成员都可以，经查看授权的管理员与 AI 助手不可以；回收站里的照片不可以。
  - 照片要已读出、所在图库开着开关，字段要属于读出的文档类型。值最多 200 字，不能有控制字符。
  - 把没更正过的字段设成读出的值，就是“确认”：它也成为一条更正，界面据此不再标“待核对”。
- `GET …/text/changes`：修改记录，新的在前，含修改人、旧值、新值和从哪个图库改的。AI 助手拿不到。
- 照片详情（`GET /api/v1/photos/assets/{assetId}`）带 `text`：`read`、`queued` 或 `failed`，图库关着开关或不读文字的照片没有这一项。查看器据此只为有文字的照片请求上面的接口。

## 权限与隐私

- **可见范围**与照片相同，沿用图库 Policy：成员只能看到和搜到自己私有图库与共享图库照片中的文字；管理员在“查看私有图库”授权期间只读可见。
- **防泄漏**：文字、字段与计数都不得经列表、搜索、计数或错误信息泄漏给看不到照片的人，泄漏测试覆盖这些路径。
- **不遮挡**（2026-11-06 用户决定，[决定](../decisions/2026-11-06-ocr-no-masking-in-the-ui.md)）：能看到照片的人本来就看得到原图，字段、全文与修改记录都显示完整的值。2026-10-30 采纳的“只显示前后几位”不再实行。
- **机器识别**：界面要让人看出文字与字段是机器识别的，避免把误读当作原件内容（[选型研究](../research/photo-ai-model-runtime-selection.md#ocr)的要求）。

### AI 助手：能用，看不到具体的值

按用户的方向（[决定](../decisions/2026-10-30-ocr-assistant-handles.md)），AI 助手可以看见并调用含隐私信息的字段，但不能知道具体的值。这可以实现，做法是**引用而不暴露**：模型只拿到句柄，真实的值由 NAS 在本机替换，而且只替换到结果不会再回到模型的地方。

AI 助手现在使用云端模型，相册给它的接口里有搜索与照片详情（[assistant.go](../../internal/photoservice/assistant.go)）。以下各条都在这些接口和助手的工具上实现，不依赖模型配合。

- **敏感字段**（[清单](../decisions/2026-10-30-ocr-sensitive-fields.md)）：证件号码、银行卡号、各类账号（付款、收款）、交易流水号。姓名、住址、日期、金额、商户等不在其内。
- **助手看到的**：
  - 文档类型、字段名、核对状态，以及每个敏感字段的句柄，例如 `⟦身份证号·k7f2⟧`；
  - 号码一位也不给；
  - 全文先把敏感值替换成句柄再给助手：一是已读出的敏感字段值，二是按规则认出的号码，即校验位正确的 18 位身份证号、通过 Luhn 校验的 16–19 位卡号、11 位手机号。
- **句柄**：
  - 句柄是随机值，在服务端登记，绑定“本人 + 照片 + 字段”；
  - 不能由号码推出来：用号码的哈希做句柄的话，身份证号的取值空间有限，可以穷举反推；
  - 照片删除或本人不再能看到这张照片时，句柄失效。
- **防止试探**：助手的搜索只匹配脱敏后的文字。否则它可以用号码片段反复搜索，看有没有结果，把号码一位一位试出来。
- **使用句柄**：助手把句柄交给工具，产品服务在本机、以本人身份、按图库 Policy 再核对一次后，把它换成真实的值。只允许换到结果不会再回到模型的地方：
  - 助手窗口里显示或复制给本人；
  - 经“提交更改”写进本人空间的文件。之后助手再读这些文件时，文件代理把其中本人的敏感值替换回句柄。
- **暂不允许**：
  - 经浏览器控制填进网页表单：页面截图会发给云端模型；
  - 发往外部服务。

  以后要开放时，按 #160 的规则，属于“可能外泄的写入”，须本人在确认卡片上看到真实的值后同意。
- **边界**：成员自己在对话里打出的号码会照常发给模型，这不在本方案控制范围内。
- **步骤 4 的实现**（[`internal/photos/textview.go`](../../internal/photos/textview.go)、[`textread/sensitive.go`](../../internal/photos/textread/sensitive.go)）：
  - 助手经自己的套接字读 `GET …/text`。敏感字段的值，包括读出的值和成员的更正，都换成句柄；全文和其他字段先替换这些值，再替换按规则认出的号码。
  - 规则认出的号码：校验位正确的身份证号、通过 Luhn 校验的卡号、手机号，以及其他 15 位以上的数字串。最后一类是因为读错一位的证件号或卡号校验不过，仍然敏感。
    - 号码可以是全角数字，各组之间可以隔着任意空白、换行或连字符：搜索比较时忽略这些，这里认不出的，助手就能按片段搜到。
    - 一串数字里，从每一组起取最长的、符合某条规则的几组算一个号码；夹在这些号码之间、不符合任何规则的几组合计 15 位以上时也算。
  - 句柄登记在 `text_handles`，绑定“本人 + 照片 + 键”。键是字段（`field:卡号`），成员把字段改成别的值时读出的值另有一个（`read:卡号`），号码按它在这次读取中出现的次序（`number:2`）。
    - 不存号码的摘要：身份证号的取值空间有限，摘要可以穷举反推，关闭开关、清除文字之后也还留着。
    - 同一个值对同一个人总是同一个句柄，不同成员的助手拿到的不同。
  - 助手的搜索只在这样脱敏后的全文与字段中找。
  - 文字里有敏感值的照片，助手拿不到它的画面（`GET …/view` 答 403 `sensitive_photo`）：模型看图就能读出号码，句柄就白换了。
    - 只有手机号的照片照常给（2026-11-06 用户决定），手机截图常有手机号；手机号在文字里仍是句柄。
    - 还没读出文字的照片照常给，包括排队中的、分流漏掉的和开关关着的图库里的。
- **实现分工**：
  - 本方案的步骤 4 实现：句柄登记、助手接口的脱敏、搜索只匹配脱敏文字；
  - 在助手窗口和文件代理中把句柄换成真实的值，属于 AI 助手的工作（[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)），另开后续实现；在那之前，助手只能指出“哪张照片里有哪种号码”。

## 资源策略

- **单独阶段**：读文字只在向量、检测、人脸与宠物的队列都清空后进行。开始前释放检测类模型，读文字期间 Worker 只持有向量模型的常驻部分和 OCR 模型。
- **加载前判断**：读文字阶段的需要 = Worker 自身启动占用 + OCR 模型的 `memoryBytes`，不与检测类模型相加。门控沿用“加载后仍留出余量”的规则（`Gate.Open(ctx, need)`），只是相册服务按阶段传入不同的需要。
- **释放**：OCR 队列清空、有人使用相册、内存不足或内存压力时，结束 llama-server 子进程。
- **取消**：门控关闭时挂断连接，这张照片退回队列、不计尝试次数，与检测相同。
  - 生成阶段：llama-server 在写下一个 token 时发现连接已断，随即停止。
  - 图像编码与提示阶段：llama-server 要把整段提示算完才处理取消，期间 SIGTERM 也要等 5–10 秒。开发机上（Qwen3.5-2B，4 线程）挂断后它又忙了约 2.5 分钟。所以还没收到第一个 token 就被取消时，Worker 直接杀掉 llama-server（SIGKILL），CPU 立即空出；下次读取重新加载模型（NAS 上约 4 秒），文件不再核对哈希。NAS 上的实测见[评估报告](../research/photo-ocr-model-evaluation.md#qwen35-4b-在实验-nas-上2026-10-30实施步骤-1)。
- **CPU**：线程数为可用核数减 2，在 `anas.slice` 中低权重运行，与现有 AI 相同。
- **内存口径**（步骤 1 在 NAS 上实测，见[评估报告](../research/photo-ocr-model-evaluation.md#qwen35-4b-在实验-nas-上2026-10-30实施步骤-1)）：
  - 模型以映射方式载入（`--load-mode mmap`）。llama.cpp 把大部分权重重排进匿名内存，映射的文件页留在页缓存，内核可以回收。NAS 的可用内存最多下降 3.3 GiB，与匿名内存一致。
  - 读入内存（`--load-mode none`）或 Direct I/O（`dio`）速度与输出相同，但多占约 1 GiB，所以不用。
  - `memoryBytes` 取读文字阶段匿名内存的增量，与门控看的可用内存口径一致。
  - NAS 空闲时可用 4.4–5.9 GiB，加载后剩 1.1–2.6 GiB：1 GiB 与用户要调整的 500 MB 余量下一般都能加载。
  - 映射的文件页让 Worker 的 cgroup 一度超过 5 GB，高于上限（总内存减 2.5 GiB）；到上限时内核先回收这些页，不会结束 Worker。
  - 这些都是开源 llama.cpp 默认部署下的数字，用户计划之后继续优化内存。
- **硬件**：实验 NAS 是单条 8 GB 内存（单通道），生成速度受内存带宽限制。加一条内存组成双通道，预计生成会明显加快，未测。

## 界面

在现有相册界面上增加，视觉与交互随实现一起做、在浏览器中核对（[相册界面规格](../specs/photo-gallery-ui.md)）：

- **查看器信息面板“照片中的文字”**（[`PhotoText.tsx`](../../web/src/photos/PhotoText.tsx)，步骤 5）：
  - 标题旁是文档类型与“机器识别”；
  - 字段逐行列出，“待核对”的写明原因，可以“确认”（把读出的值设为更正）或就地更正；更正过的写明谁、何时，可以恢复识别结果；
  - 每个字段有复制按钮；值都完整显示，不遮挡；
  - 全文可以展开，没有字段的文字（如截图）直接展开；没读完的注明；
  - “修改记录”列出谁、何时、从哪个图库改了什么。“改回”回到这次修改之前：之前是另一条更正的，恢复那条更正；之前显示识别结果的（第一次更正、确认，或恢复之后的修改），去掉更正。字段已经是那样的不显示“改回”；
  - 排队中与读取失败的照片各有一行说明；不读文字的照片和关了开关的图库不显示这一栏；
  - 经查看授权看到的照片与回收站里的照片只读。
- **搜索结果**：名称匹配之后，文字命中的照片单独成组，组标题“照片中的文字包含“…””（引号里是去掉人名后的搜索词，与搜索实际匹配的一致），并注明文字由本地 AI 识别、可能有误；之后的照片另起一组（“画面相近的照片”，或“检测到 … 的照片”）。搜索结果为此多了名称匹配的张数 `names`。
- **整理进度**：照片都整理完、还有文字没读时，侧栏的 AI 卡片显示“正在读取文字 已读 / 总数”、无法识别的张数与读不出文字的张数。AI 状态为此多了 `texts`：已读、排队与失败的张数。
- **开关**：AI 聚合中的“本地 AI 设置”（原“人物与宠物设置”）每个图库列出“人物与宠物”与“照片中的文字”两个开关：
  - 私有图库由所有者、共享图库由管理员设置；
  - 关闭“照片中的文字”前确认：删除读出的文字，保留更正与修改记录。

## 交付

- **发布物**：
  - llama-server 按清单随发布交付，和 Qdrant 一样按内容存一份；
  - 两个模型文件作为新的模型角色（`ocr-model:gguf`、`ocr-mmproj:gguf`），由安装器链接为 `ai/ocr-model.gguf` 与 `ai/ocr-mmproj.gguf`；
  - `anas-ai.service` 增加对应参数。
- **体积**：首次安装多出约 3 GB。之后按内容去重，升级时不重复拷贝。
- **系统测试**：
  - 替身模型下，Worker 在沙箱内启动子进程，子进程打不开网络套接字、看不到数据卷；
  - 读一张图；
  - 挂断后子进程停止生成；
  - `release` 后子进程退出。
  - 真实模型的运行由环境变量指定模型文件，与现有模型相同。
- **回退**：目录库的第 15 号迁移加了读文字的表与图库开关。没有这一迁移的旧版本拒绝打开更新的目录库，所以退回步骤 3 之前的版本时，先停相册服务，在目录库上执行下面的语句，再切回旧 release；还要退到向量索引之前时，接着按[启用向量索引的回滚](../runbooks/enable-vector-index.md#回滚或恢复)撤销 migration 14。已读的文字与用户更正一并删除。语句以相册服务的身份执行（`setpriv --reuid=a-nas-photos --regid=a-nas-photos --clear-groups sqlite3 /srv/a-nas/data/photos/catalog.db '…'`），与 [`TestVectorIndexMigrationCanBeUndone`](../../internal/photos/migrate_test.go) 撤销的相同。

  ```sql
  DROP TABLE text_corrections;
  DROP TABLE texts;
  ALTER TABLE libraries DROP COLUMN texts;
  DELETE FROM jobs WHERE derivation = 'ocr/v1';
  DELETE FROM scans WHERE derivation = 'ocr/v1';
  PRAGMA user_version = 14;
  ```

  第 16 号迁移（步骤 4）让更正按内容对象存，并加了修改记录与助手的句柄。退回步骤 4 之前、步骤 3 之后的版本时，先执行下面的语句；还要往前退时，再接着执行上面的。更正、修改记录与句柄一并删除。[`TestTextCorrectionsMigrationCanBeUndone`](../../internal/photos/migrate_test.go) 撤销的相同。

  ```sql
  DROP TABLE text_handles;
  DROP TABLE text_changes;
  DROP TABLE text_corrections;
  CREATE TABLE text_corrections (
      library_id TEXT NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
      object_id TEXT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
      field TEXT NOT NULL,
      value TEXT NOT NULL,
      account TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      PRIMARY KEY (library_id, object_id, field)
  );
  PRAGMA user_version = 15;
  ```

## 步骤

一个 PR，每步一个提交，每步 `make check` 通过；部署到实验 NAS 另行确认。

| 步骤 | 内容 | 完成标准 |
|---|---|---|
| 1 | 交付与运行：llama-server 清单、模型转换与清单、安装器角色、Worker 子进程与 `read_text`、Fake Provider、分阶段的 `memoryBytes`；NAS 上实测 Q4_K_M 的速度、内存（映射与 `--no-mmap`）与挂断到空闲的时间 | 已完成（2026-10-30）：系统测试替身与真实模型各一次通过；NAS 数据已写进评估报告 |
| 2 | 分流：评测 PP-OCRv6 检测与向量相似度，选定方法与阈值并接入 | 已完成（2026-11-02）：选定向量相似度并已接入，目标召回 99%（[决定](../decisions/2026-11-02-ocr-routing-recall.md)） |
| 3 | 读取、判型与核对：两步读取、判型规则、核对规则、单字概率的作用、存储 migration、模型更换重排、开关与清除 | 已完成（2026-11-03）：单字概率 0.99 时 48% 的字段自动通过、其中 2.7% 是错的（没有真正读错的），取 0.99（[决定](../decisions/2026-11-03-ocr-character-confidence.md)） |
| 4 | 搜索与接口：文字一档、数字归一、OpenAPI、泄漏测试；助手接口的脱敏与句柄登记，助手搜索只匹配脱敏文字；更正按内容对象存与修改记录（ADR 0033） | 已完成（2026-11-05）：排序、翻页与泄漏测试通过；助手拿到的文字里找不到测试样本的敏感值，按号码片段搜索也搜不到 |
| 5 | 界面：查看器面板、待核对与更正、修改记录、搜索分组、进度、开关（界面上不遮挡，2026-11-06） | 已完成（2026-11-06）：前端测试通过；本地栈浏览器核对 |
| 6 | 文档与实机：规格、本方案、相册本地 AI、领域语言、当前状态；部署后在实验 NAS 上验收 | `make docs-check` 通过；实机验收记录 |

## 待用户决定

起草时列出的 8 项都已定下，见“已确认的决定”。

## 风险

- **数据**：Qwen3.5-4B 在 NAS 上每张中位 105 秒，可用内存最多下降 3.3 GiB（步骤 1）。准确率来自其他开发的测试，Claude 没有复现。
- **速度**：4B 在单通道内存上生成慢，长票据的字段读取可能要两三分钟。分流的误判会直接放大总耗时。
- **前台与内存**：读文字时可用内存一旦低于余量，会卸载全部模型，正在读的照片退回队列。频繁发生时只是变慢，不会读错。
- **上游构建**：llama.cpp 上游的发布构建可能与评估用的构建在输出上有细微差异（线程数不同就会造成）。换版本后都要在同一批样本上核对。
- **敏感数据**：证件号与账号会以文字形式存进目录库。目录库与照片同在数据卷上，照片本身已经包含这些信息，风险没有扩大到设备之外。关键在于两点：给 AI 助手的一律只是句柄，日志里不写文字（见“权限与隐私”）。
- **句柄的回流**：句柄换成真实的值后，值写进的地方如果以后又被模型读到，值就泄漏了。所以只开放结果不会再回到模型的地方，文件代理在助手读文件时替换回句柄。新开放任何用途前，都要先说明它的结果会不会被模型读到。

## 关联

- [相册规格](../specs/photo-library.md)、[相册本地 AI](photo-ai.md)、[物体检测的资源策略](photo-objects.md#资源策略)、[人物与宠物](photo-people.md)
- [模型评估](../research/photo-ocr-model-evaluation.md)、[模型与 Runtime 选型](../research/photo-ai-model-runtime-selection.md#ocr)
- [#128](https://github.com/zhongwater123/A-NAS/issues/128)（真实样本）、[#321](https://github.com/zhongwater123/A-NAS/pull/321)（内存规则）、[#323](https://github.com/zhongwater123/A-NAS/issues/323)（4B 级候选）
- [ADR 0033](../adr/0033-keep-photo-text-corrections-per-content-object-with-a-change-log.md)（更正与修改记录）
- 测试：[`internal/photos/textview_test.go`](../../internal/photos/textview_test.go)、[`internal/photos/textread/sensitive_test.go`](../../internal/photos/textread/sensitive_test.go)
- 代码：[`internal/photos/search.go`](../../internal/photos/search.go)、[`internal/photos/textsearch.go`](../../internal/photos/textsearch.go)、[`internal/photos/textview.go`](../../internal/photos/textview.go)、[`internal/photos/textread/sensitive.go`](../../internal/photos/textread/sensitive.go)、[`internal/photosapi/text.go`](../../internal/photosapi/text.go)、[`internal/photoservice/gate.go`](../../internal/photoservice/gate.go)、[`internal/photoservice/assistant.go`](../../internal/photoservice/assistant.go)、[`ai/anas_ai/worker.py`](../../ai/anas_ai/worker.py)、[`ai/eval/ocr.py`](../../ai/eval/ocr.py)、[`ai/eval/ocr_nas_bench.sh`](../../ai/eval/ocr_nas_bench.sh)
