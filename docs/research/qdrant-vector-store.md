# Qdrant 作为文本向量库：接入方式、边界与离线验证

状态：客户端、进程内退路与离线测试完成（2026-10-20）；真 Qdrant 与真模型未在开发机上跑过。

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
| [`store.py`](../../ai/finetune/store.py) | `VectorStore` 接口；`QdrantStore` 用 urllib 走 REST（`points/query`、写操作 `wait=true`、`api-key` 头）；`MemoryStore` 进程内 + 可落盘 JSON；Qdrant filter 子集的求值器（must / should / must_not、match value / any / except / text、range） |
| [`index.py`](../../ai/finetune/index.py) | `TextIndex` = embedder + 向量库：`add` / `search` / `similar` / 过滤 / 删除，文本存在 payload 里，结果直接带原文 |
| [`vectors.py`](../../ai/finetune/vectors.py) | 命令行 `info` / `add` / `search` / `similar` / `export` / `drop` |
| [`qdrant-up.ps1`](../../ai/finetune/qdrant-up.ps1) | Windows 开发机不装 Docker 时，下载 Qdrant 单文件二进制到 `ai/finetune/qdrant/bin` 并启动，数据在 `ai/finetune/qdrant/storage` |
| [`tests/fake_qdrant.py`](../../ai/tests/fake_qdrant.py) | 标准库实现的假 Qdrant REST，复刻用到的接口和"id 必须是整数或 UUID"等校验 |
| [`tests/test_vectorstore.py`](../../ai/tests/test_vectorstore.py) | 同一套契约用例跑进程内、落盘、假 Qdrant 三遍；设 `ANAS_QDRANT_TEST_URL` 再对真 Qdrant 跑一遍 |

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
```

### 设计要点

- **点 id。** Qdrant 只收无符号整数或 UUID。任意字符串 key（反馈编号、文件路径）映射成 uuid5，原 key 存在 payload 的 `_key`，结果里原样还回来。
- **分数口径。** 集合用 Cosine，向量入库前已归一化，分数就是余弦，和 `MemoryStore`、`rank()` 可比；阈值仍按 `threshold_for` 分真模型 / 哈希两套。
- **维度绑定 embedder。** 集合维度跟 `embedder.dim` 走（Matryoshka 截到 256 维）。换维度要 `recreate=True`（命令行 `add --recreate`），会清空集合，不做静默迁移。
- **缓存键带维度。** `HashEmbedder.name` 原来不带维度，`CachedEmbedder` 会把 128 维和 512 维的向量串在一起；联调时撞上，已修。
- **只用 `points/query`。** 这是 Qdrant 1.10 起推荐的查询接口，老的 `points/search` 不再兼容；要求 Qdrant ≥ 1.10。

## 验证

- `make ai-test`：`tests/test_vectorstore.py` 共 35 个用例全部通过（Windows 开发机，Python 3.12，标准库），其中 6 个对真 Qdrant 的用例因未设 `ANAS_QDRANT_TEST_URL` 跳过。
- 用假 Qdrant 单独起在 6399 端口，`finetune.vectors` 的 `add → search → search --where → similar → info → export → drop` 全流程走通；维度不一致时给出带 `--recreate` 提示的错误并退出码 1。

## 风险与已知限制

- **真 Qdrant 未跑。** 假 Qdrant 只复刻了用到的接口和最常见的校验；返回体细节（如 `points/query` 的 `result.points` 形状）按 1.10+ 文档实现，第一次连真实例时要跑一遍 `ANAS_QDRANT_TEST_URL` 的契约用例。
- **`qdrant-up.ps1` 未在开发机实际下载运行**，只做了语法解析；它按 GitHub Releases 的资产名 `qdrant-x86_64-pc-windows-msvc*.zip` 找包，找不到会列出该版本的全部资产名。
- **`MemoryStore` 的过滤器是子集。** 没实现 geo、values_count、nested 等条件；用到的话假 Qdrant 和进程内退路都会当作"满足"而不是报错。
- **不是产品运行时的一部分。** Worker 运行环境是 MediaPipe + LiteRT 的锁定依赖，Qdrant 进不进设备是另一个决定；这里只解决开发机上攒数据、评估和去重的需要。

## 来源

- Qdrant REST 接口：https://api.qdrant.tech/api-reference
- 点 id 规则与 filter 语法：https://qdrant.tech/documentation/concepts/points/ 、https://qdrant.tech/documentation/concepts/filtering/
- 发布与 Windows 二进制：https://github.com/qdrant/qdrant/releases
