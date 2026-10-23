# Qdrant 作为文本向量库：接入方式、边界与离线验证

状态：客户端、进程内退路与离线测试完成（2026-10-20）；2026-10-23 按 [#152](https://github.com/zhongwater123/A-NAS/issues/152) 审查修正了过滤器、向量空间身份、`similar()` 前缀、作用域覆盖四个已复现问题，并与真实 Qdrant 1.19.2 做了过滤语义对照；真模型未在开发机上接入这一模块。

## 问题

[EmbeddingGemma 2 文本向量](embeddinggemma-2-text-finetuning.md)的候选用途（反馈去重、容器日志按已知故障分类、文档语义搜索）都要在"一组已有文本"里找最像的。`finetune/similarity.py` 的 `rank()` / `find_duplicates()` 每次把全部候选重新算一遍向量，几十条没问题，反馈库、日志指纹攒到几千条以后就该把向量存起来：向量只算一次，查询只传一条。

## 候选与选择

| 候选 | 为什么不 / 为什么是 |
|---|---|
| 进程内暴力余弦（现状） | 零部署，但 O(n) 重算；只适合几百条 |
| SQLite + 自己存向量 | 零依赖，但没有近邻索引和过滤下推，仍是暴力扫 |
| Qdrant | 单文件二进制或一个容器就能起；REST 接口完整（不装客户端库也能用）；带 payload 过滤、Cosine 距离、持久化；单机资源占用小 |
| Chroma / Milvus / pgvector | Chroma 是 Python 库，进 Worker 运行环境要带 torch 系依赖；Milvus 组件多；pgvector 要 PostgreSQL，产品用的是 SQLite |

选 Qdrant，但把它当**可替换的后端**：调用方拿到的是 `VectorStore` 接口，没有 Qdrant 的机器退回同接口的进程内实现。这个选择可逆，所以记在研究里，不立 ADR。

## 我们做了什么

都在 `ai/finetune/`，只用标准库，不进 Worker 运行环境：

| 文件 | 内容 |
|---|---|
| [`store.py`](../../ai/finetune/store.py) | `VectorStore` 接口；`QdrantStore` 用 urllib 走 REST（`points/query`、写操作 `wait=true`、`api-key` 头）；`MemoryStore` 进程内 + 可落盘 JSON；过滤条件白名单（must / should / must_not、match value / any、range），其余写法报 `UnsupportedFilter`；集合元数据（向量空间身份、任务、作用域） |
| [`index.py`](../../ai/finetune/index.py) | `TextIndex` = embedder + 向量库：`add` / `search` / `similar` / 过滤 / 删除，文本存在 payload 里，结果直接带原文；`task`（retrieval / sts）与 `scope` 记在集合元数据里，打开与读写时校验 |
| [`vectors.py`](../../ai/finetune/vectors.py) | 命令行 `info` / `add` / `search` / `similar` / `export` / `drop` |
| [`qdrant-up.ps1`](../../ai/finetune/qdrant-up.ps1) | Windows 开发机不装 Docker 时，下载 Qdrant 单文件二进制到 `ai/finetune/qdrant/bin` 并启动，数据在 `ai/finetune/qdrant/storage` |
| [`tests/fake_qdrant.py`](../../ai/tests/fake_qdrant.py) | 标准库实现的假 Qdrant REST，复刻用到的接口和"id 必须是整数或 UUID"等校验；过滤用自己独立的求值器，集合 metadata 原样存取 |
| [`tests/test_vectorstore.py`](../../ai/tests/test_vectorstore.py) | 同一套契约用例跑进程内、落盘、假 Qdrant 三遍；设 `ANAS_QDRANT_TEST_URL` 再对真 Qdrant 跑一遍 |
| [`tests/test_review152.py`](../../ai/tests/test_review152.py) | #152 审查四个已复现问题的回归测试；`RealQdrantFilterParity` 对真实 Qdrant 逐条比对过滤结果 |

### 怎么用

```bash
# 起 Qdrant（二选一）
docker run -d --name qdrant -p 6333:6333 -v "$PWD/ai/finetune/qdrant/storage:/qdrant/storage" qdrant/qdrant
pwsh ai/finetune/qdrant-up.ps1 -Background        # Windows，无 Docker

export ANAS_QDRANT_URL=http://127.0.0.1:6333      # 不设就退回进程内向量库（落盘到 ai/finetune/data/vectors/）
export ANAS_EMBED_URL=http://127.0.0.1:8089       # 不设就退回哈希向量，只能找字面相近
cd ai
python -m finetune.vectors add feedback.jsonl --key-field id --payload-fields source,app --collection feedback
python -m finetune.vectors search "大文件上传后校验不一致" -k 5 --where source=feedback
```

```python
from finetune.embedders import build_embedder
from finetune.index import TextIndex
from finetune.store import build_store, where

idx = TextIndex(build_store({"url": "http://127.0.0.1:6333"}, collection="feedback"), build_embedder({"dim": 256}))
idx.add(["上传 50MB 文件后校验不一致"], keys=["FB-101"], payloads=[{"source": "feedback", "app": "files"}])
idx.search("大文件上传后校验失败", k=3, where=where(app="files"))

dedup = TextIndex(build_store({}, collection="feedback-sts"), build_embedder({"dim": 256}), task="sts")
dedup.similar("上传 50MB 文件后校验不一致")                 # similar 只在 sts 集合上可用
```

### 设计要点

- **点 id。** Qdrant 只收无符号整数或 UUID。任意字符串 key（反馈编号、文件路径）映射成 uuid5，原 key 存在 payload 的 `_key`，结果里原样还回来。
- **分数口径。** 集合用 Cosine，向量入库前已归一化，分数就是余弦，和 `MemoryStore`、`rank()` 可比；阈值仍按 `threshold_for` 分真模型 / 哈希两套。
- **维度绑定 embedder。** 集合维度跟 `embedder.dim` 走（Matryoshka 截到 256 维）。换维度要 `recreate=True`（命令行 `add --recreate`），会清空集合，不做静默迁移。
- **缓存键用向量空间身份。** `CachedEmbedder` 的键由 embedder 的完整身份（实现、地址、模型名、服务端权重指纹与运行时、前缀版本、维度、归一化）生成，不再只用名字和维度。
- **只用 `points/query`。** 这是 Qdrant 1.10 起推荐的查询接口，老的 `points/search` 不再兼容；要求 Qdrant ≥ 1.10。

## 按 #152 审查的修正（2026-10-23）

审查用确定性替身复现了六种情况，现在全部被拒绝或结果正确：

| 问题 | 现在的行为 | 回归测试（`tests/test_review152.py`） |
|---|---|---|
| S3 未知过滤条件当成满足，条件删除扩大为全删 | 只接受白名单子集，其余写法（nested、text、except、is_null、has_id、geo 等）报 `UnsupportedFilter`：查询读不出、条件删除删不掉任何点；`delete_where` 拒绝空过滤；`QdrantStore` 发请求前按同一白名单校验。布尔不再等于整数，数组字段按任一元素满足。假 Qdrant 改用独立求值器 | `S3UnknownFilterRejected`、`RealQdrantFilterParity` |
| R1 同维度就复用集合，缓存键只看名字和维度 | 集合元数据记录向量空间身份（MemoryStore 存在文件里，Qdrant 存在集合 metadata 里），打开与每次读写都校验，不一致拒绝；没有身份记录但已有点的旧集合也拒绝，只能 `recreate=True` 重建。`server.py` 的 `/health` 报告权重指纹与运行参数（精度、截断长度） | `R1VectorSpaceIdentity`、`R1CacheKeys` |
| R2 默认 `add` 用 document、`similar` 用 sts | `TextIndex` 的 `task` 是 retrieval（document / query）或 sts（两边 sts），写进元数据；`similar()` 只在 sts 集合可用；`add(kind=...)` 与集合任务不一致、换任务重开都拒绝 | `R2TaskPrefix` |
| S1 默认 key 是文本，Alice / Bob 同文本互相覆盖 | `TextIndex(..., scope=...)` 建分作用域集合：key 是"作用域 + 调用方 key"，同文本各存一份；搜索、按 key 读取、删除、条件删除、计数、导出都只在本作用域内；漏传 scope、payload 带 `_scope` / `_ukey` / `_key` / `text`、过滤引用这些保留字段都拒绝 | `S1Scopes` |

作用域只修研究代码自身的正确性：scope 由调用方传入，成员身份要由可信服务端绑定且调用方不能自选，这一层不在这里解决，首版不对成员开放共享索引。本模块没有源文件或照片对象的权限模型，"源对象撤权后返回原文前复核"不在这里验证。集合重建只做到"不一致就拒绝、显式 `recreate` 重建"，没有别名切换与回退。权重指纹取模型目录下全部文件的路径与大小加小配置文件内容，大小完全相同的权重替换识别不出。

副作用：缓存键改变，已有向量缓存全部失效；没有身份记录的旧集合需要重建；`TextIndex` 去掉了 `doc_kind` / `query_kind` 参数，改用 `task`，命令行的 `add --kind` 改为全局 `--task`，另加 `--scope`。

## 验证

- `make ai-test`（Windows 开发机，Python 3.12，标准库；`test_worker.py` 因 Windows 没有 `AF_UNIX` 不在其列）：除 `test_worker.py` 外 105 个用例通过、19 个跳过；设 `ANAS_QDRANT_TEST_URL` 连本机 Qdrant 1.19.2 时 105 个通过、10 个跳过（剩下的跳过是 MediaPipe 与真实模型相关用例），其中 `RealQdrantFilterParity` 用 300 条随机 payload 和 15 组过滤逐条比对，进程内实现与真实 Qdrant 的结果条数和点完全一致。
- 用假 Qdrant 单独起在 6399 端口，`finetune.vectors` 的 `add → search → search --where → similar → info → export → drop` 全流程走通；维度不一致时给出带 `--recreate` 提示的错误并退出码 1。

## 风险与已知限制

- **真 Qdrant 只在开发机上跑过。** 契约用例与过滤对照在本机 Qdrant 1.19.2 上通过；集合 metadata 需要支持该字段的 Qdrant 版本（1.19 已验证）。
- **`qdrant-up.ps1` 未在开发机实际下载运行**，只做了语法解析；它按 GitHub Releases 的资产名 `qdrant-x86_64-pc-windows-msvc*.zip` 找包，找不到会列出该版本的全部资产名。
- **过滤器只接受子集。** geo、values_count、nested、全文匹配等写法一律报错拒绝，需要时要先扩充白名单并补真实 Qdrant 对照。
- **不是产品运行时的一部分。** Worker 运行环境是 MediaPipe + LiteRT 的锁定依赖，Qdrant 进不进设备是另一个决定；这里只解决开发机上攒数据、评估和去重的需要。

## 来源

- Qdrant REST 接口：https://api.qdrant.tech/api-reference
- 点 id 规则与 filter 语法：https://qdrant.tech/documentation/concepts/points/ 、https://qdrant.tech/documentation/concepts/filtering/
- 发布与 Windows 二进制：https://github.com/qdrant/qdrant/releases
