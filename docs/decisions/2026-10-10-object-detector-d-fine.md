# 物体识别用 D-FINE（Objects365）

状态：decided（2026-10-10，用户）
更新时间：2026-11-05

## 问题

2026-10-10 部署后决定关闭由向量零样本算出的 AI 标签，向量回到纯检索，物体识别交给开源检测模型（[#66](https://github.com/zhongwater123/A-NAS/issues/66)）：选哪个模型，它负责什么。

## 选项

候选与比较见[检测与混合检索研究](../research/photo-ai-object-detection.md)。

## 决定

用户 2026-10-10 选定 D-FINE（Objects365），并给出检测器的职责：在空闲时给照片打标，框出照片里的所有物体，记下类别、置信度、框和数量这些客观数据；文搜图或以图搜图时，这些数据与向量一起用于混合检索。[实施方案](../architecture/photo-objects.md)于 2026-10-13 由用户确认。

## 关联

- [物体检测实施方案](../architecture/photo-objects.md)、[相册本地 AI](../architecture/photo-ai.md)
- [检测与混合检索研究](../research/photo-ai-object-detection.md)
