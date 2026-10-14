# 评估把 Open Images 的大部分“有”读成了“没有”

状态：resolved（物体检测的评估与校准已重做；AI 聚合的 Open Images 部分待重做）
更新时间：2026-10-13

## 症状与影响

为物体检测校准“检测到”的分数线时，猫和狗在 COCO 上检测分 ≥ 0.6 的照片 93–96% 正确，在 Open Images 上却不管分数线定多高都只有约一半正确。查看被判为“误报”的 Open Images 照片，最高分的四张全是一眼就能认出的猫。

受影响的是开发机上的评估，产品代码和 NAS 都不读这份标注：

- **物体检测研究**（PR #80，`docs/research/photo-ai-object-detection.md`）：“COCO 之外”一节的 98 类结果及结论作废，包括“COCO 之外持平”“动物检测更弱”和“人与眼镜的标注与检测含义不一致”。
- **物体检测校准的第一版**（PR #83）：
  - 把小船、花、眼镜、西红柿判为融合后变差；
  - 猫和狗因 Open Images 上“只有一半正确”而不能说“检测到”；
  - 排除 Open Images 的“人”与“眼镜”这条规则，也是基于读错的标注。
- **AI 聚合的标签校准**（PR #69，已部署）：
  - 阈值只按 COCO 的反例定，已部署的分组不受影响；
  - 但召回与正例计数也用了 Open Images 的正例；
  - 报告中的 Open Images 难例数字是错的，其中“猫的阈值会让 Open Images 中 67% 的‘非猫’困难样本（狮子、老虎、猫形玩偶等）显示”一句不成立。

## 最小复现

1. 在 `D:\A-NAS-datasets\openimages\oidv7-val-annotations-human-imagelabels.csv` 中查 `d06bafcf99ccb263`（一只躺着的虎斑猫）的 `/m/01yrx`（Cat）。
2. 实际：该行为 `d06bafcf99ccb263,verification,/m/01yrx,1.0`，`eval.datasets.openimages_verified` 却返回 `False`。
3. 期望：返回 `True`。

## 观察事实

按来源与写法统计，这份标注共 618,184 条：

| 来源 | 写法 | 条数 |
|---|---|---|
| verification | `1.0` | 313,308 |
| verification | `0.0` | 226,679 |
| crowdsource-verification | `1` | 75,872 |
| crowdsource-verification | `1.0` | 1,617 |
| crowdsource-verification | `0.0` | 708 |

- 解析写的是 `row["Confidence"] == "1"`，只有众包的 `1` 算“有”。
- 修正后，Open Images 上检测分 ≥ 0.6 的正确率：人从 13% 变为 95%，眼镜从 10% 变为 79%，狗从 47% 变为 88%。
- 对应关系本身没问题：猫只对应 `/m/01yrx`，狗只对应 `/m/0bt9lr`。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| Open Images 的反例是模型提出的难例（玩偶、图画、狮虎） | 查看检测分最高的反例 | rejected：都是真猫 |
| Objects365 类别到 Open Images 类别的对应混入了别的类 | 列出对应的类别 ID | rejected：一一对应 |
| 读标注时把“有”读成了“没有” | 对照原始行与解析结果 | confirmed |

## 根因

Open Images V7 的人工核验标注对不同来源用了两种写法，评估代码只认其中一种。读错的方向是把“有”当成“没有”，于是真猫真狗成了“反例”，检测器认对了反而被算作误报。读错的结果看上去像是“难例很难”，与对 Open Images 难例的预期一致，所以当时没被怀疑。

## 修复与回归证据

- 修复：[`eval/datasets.py`](../../ai/eval/datasets.py) 的 `openimages_verified` 按数值读，`>= 0.5` 为“有”。
- 测试：[`test_datasets.py`](../../ai/tests/test_datasets.py) 覆盖 `1`、`1.0`、`0`、`0.0` 四种写法，纳入 `make ai-test`。
- 物体检测：用正确的标注重跑评估并重写 `v1.calibration.json`。
  - 55 个查询能说“检测到”，94.4% 正确；猫的分数线为 0.6，狗为 0.65；变差的词只剩书桌与滑雪。
  - 研究文档中“COCO 之外”一节按正确标注重算：63 类，融合 0.923、纯向量 0.879。

## 后续工作

- **AI 聚合的标签校准要按正确的标注重做 Open Images 部分**，并更正[校准报告](../research/photo-ai-label-calibration.md)中的 Open Images 数字与上述那句。
  - 修正后 `select_openimages` 选出的照片会变，其中一部分开发机上还没有，重做前要先核对；需要下载时先征得用户同意。

## 关联

- 研究：[物体检测与混合检索](../research/photo-ai-object-detection.md)、[标签校准报告](../research/photo-ai-label-calibration.md)
- 实施方案：[物体检测](../architecture/photo-objects.md#类别与启用名单)
- 规格：不涉及
- ADR：不涉及
- 运行手册：不涉及
