# AI 标签只以 AI 聚合呈现

状态：decided（2026-10-10，用户）
更新时间：2026-11-05

## 问题

本地 AI 为照片算出的标签怎样给成员看。2026-10-10 在本地环境中，搜索“笔记本电脑”会在笔记本电脑之后列出其他照片（[标签校准](../research/photo-ai-label-calibration.md)）。

## 选项

未记录当时比较过的选项。

## 决定

用户 2026-10-10 决定：标签只以“AI 聚合”呈现、不参与搜索，并且宁缺毋滥，宁可少标，不能错标。每组要多准才显示，见[AI 聚合的正确率门槛](2026-10-12-ai-group-precision.md)。

## 关联

- [相册规格](../specs/photo-library.md)、[相册界面](../specs/photo-gallery-ui.md)
- [相册本地 AI 的 AI 聚合](../architecture/photo-ai.md#ai-聚合)、[标签校准](../research/photo-ai-label-calibration.md)
