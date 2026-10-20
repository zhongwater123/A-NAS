"""在 GPU 开发机上微调 EmbeddingGemma 2 的文本编码器，产出 Hugging Face 格式的权重目录。

配方综合了三份已验证的做法（docs/research/embeddinggemma-2-text-finetuning.md）：
  - 官方教程：MultipleNegativesRankingLoss，lr 2e-5，warmup 10%，bf16，只加载文本编码器；
  - sentence-transformers 的 embeddinggemma-300m-medical：CachedMNRL（小显存也能用大 batch），
    batch 128，1 epoch，10 万对，3090 上 5.5 小时，NDCG@10 +0.05；
  - ByronLeeee/EmbeddingGemma-300M-LawVault（中文法条）：MatryoshkaLoss(MNRL) 同时训 768/512/256/128，
    6.6 万条带难负例的三元组，3 epoch，Top-1 58% → 92%。
我们的数据量小得多（几百到几千条），所以默认 3 epoch、batch 32、带 Matryoshka（线上可能用 256 维）。

用法（在 ai/ 目录下）：
  pip install -r finetune/requirements.txt       # 另一个 venv；再按显卡装对应 CUDA 的 torch
  huggingface-cli login                          # 需先接受 Gemma 条款
  python -m finetune.train finetune/data/triplets.jsonl --out finetune/out/my-embeddinggemma-2
  python -m finetune.server --model finetune/out/my-embeddinggemma-2        # 用微调后的权重起服务
  python -m finetune.eval finetune/data/triplets.jsonl --embed-url http://127.0.0.1:8089   # 和基座对比

注意：
  - 产出是 HF 权重，Worker 不能直接加载：Worker 用的是 LiteRT 文件（deploy/models/*.json 校验哈希），
    要上设备得先转成 .litertlm 并走模型清单；转换路径尚未验证（见研究文档"风险"）。
  - 只能 bf16 / fp32，fp16 会出 NaN（官方和社区都踩过）。
  - 留一部分三元组做验证（--eval-ratio），训练集上的分数不说明问题。
  - 评估和线上都要带同样的任务前缀（这里用 STS 前缀，和 embedders.PROMPTS 一致）。
"""

from __future__ import annotations

import argparse
import json
import random
import sys
from pathlib import Path

STS_PROMPT = "task: sentence similarity | query: "


def main(argv: list[str]) -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("triplets")
    ap.add_argument("--base", default="google/embeddinggemma-2")
    ap.add_argument("--out", default="finetune/out/my-embeddinggemma-2")
    ap.add_argument("--epochs", type=float, default=3)
    ap.add_argument("--batch", type=int, default=32)
    ap.add_argument("--lr", type=float, default=2e-5)
    ap.add_argument("--cached", action="store_true", help="用 CachedMNRL（显存小但想用大 batch 时）")
    ap.add_argument("--no-matryoshka", action="store_true")
    ap.add_argument("--eval-ratio", type=float, default=0.15)
    ap.add_argument("--seed", type=int, default=7)
    args = ap.parse_args(argv)

    try:
        import torch
        from datasets import Dataset
        from sentence_transformers import (SentenceTransformer, SentenceTransformerTrainer,
                                           SentenceTransformerTrainingArguments)
        from sentence_transformers.evaluation import TripletEvaluator
        from sentence_transformers.losses import (CachedMultipleNegativesRankingLoss, MatryoshkaLoss,
                                                  MultipleNegativesRankingLoss)
        from sentence_transformers.training_args import BatchSamplers
    except ImportError as e:
        sys.exit(f"缺少依赖（{e}）：pip install -r finetune/requirements.txt")

    rows = [json.loads(l) for l in Path(args.triplets).read_text(encoding="utf-8").splitlines() if l.strip()]
    rows = [{"anchor": r["anchor"], "positive": r["positive"], "negative": r["negative"]} for r in rows]
    random.Random(args.seed).shuffle(rows)
    n_eval = max(1, int(len(rows) * args.eval_ratio)) if len(rows) > 10 else 0
    eval_rows, train_rows = rows[:n_eval], rows[n_eval:]
    print(f"训练 {len(train_rows)} 条，验证 {len(eval_rows)} 条")

    model = SentenceTransformer(
        args.base,
        config_kwargs={"vision_config": None, "audio_config": None},   # 只要文本编码器（270M）
        model_kwargs={"torch_dtype": torch.bfloat16},
    )
    print(model, "参数量:", sum(p.numel() for p in model.parameters()))

    base_loss = (CachedMultipleNegativesRankingLoss(model, mini_batch_size=8) if args.cached
                 else MultipleNegativesRankingLoss(model))
    loss = base_loss if args.no_matryoshka else MatryoshkaLoss(model, base_loss, matryoshka_dims=[768, 512, 256, 128])

    train_ds = Dataset.from_list(train_rows)
    evaluator = None
    if eval_rows:
        evaluator = TripletEvaluator(
            anchors=[STS_PROMPT + r["anchor"] for r in eval_rows],
            positives=[STS_PROMPT + r["positive"] for r in eval_rows],
            negatives=[STS_PROMPT + r["negative"] for r in eval_rows],
            name="triplets")
        print("微调前:", evaluator(model))

    targs = SentenceTransformerTrainingArguments(
        output_dir=args.out + "-ckpt",
        prompts=STS_PROMPT,                   # anchor/positive/negative 都加同一个前缀
        num_train_epochs=args.epochs,
        per_device_train_batch_size=args.batch,
        learning_rate=args.lr,
        warmup_ratio=0.1,
        bf16=True,
        batch_sampler=BatchSamplers.NO_DUPLICATES,   # in-batch negatives 不能撞上正例
        logging_steps=10,
        eval_strategy="epoch" if evaluator else "no",
        save_strategy="epoch",
        save_total_limit=1,
        report_to="none",
        seed=args.seed,
    )
    trainer = SentenceTransformerTrainer(model=model, args=targs, train_dataset=train_ds,
                                         loss=loss, evaluator=evaluator)
    trainer.train()
    if evaluator:
        print("微调后:", evaluator(model))
    model.save_pretrained(args.out)
    print(f"已保存到 {args.out}；起服务：python -m finetune.server --model {args.out}")


if __name__ == "__main__":
    main(sys.argv[1:])
