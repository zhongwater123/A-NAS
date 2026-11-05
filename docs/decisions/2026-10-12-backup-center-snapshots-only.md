# 备份中心先只做快照保护

状态：decided（2026-10-12，用户）
更新时间：2026-11-05

## 问题

把桌面上的“备份”占位与“文件快照”合并为备份中心时，范围包不包括备份到外接盘、第二块硬盘等外部备份。

## 选项

- **只做快照保护**：外部备份保持“规划中”。
- **连同外部备份**。

## 决定

用户 2026-10-12 选只做快照保护：外部备份（USB [#25](https://github.com/zhongwater123/A-NAS/issues/25)、第二块硬盘、[#27](https://github.com/zhongwater123/A-NAS/issues/27)）保持“规划中”页面，不放任何可点击的备份操作（[PR #78](https://github.com/zhongwater123/A-NAS/pull/78)）。之后的评审把外部备份列为最大的缺口（[C 端功能评审](../research/backup-center-review-2026-10.md)）。

## 关联

- [备份中心规格](../specs/backup-center.md)
