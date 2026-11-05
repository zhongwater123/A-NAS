# 活动记录保留多久

状态：pending
更新时间：2026-11-05

## 问题

[日志规格](../specs/logs.md)写明活动记录永久保存在系统盘的各模块数据库中，不提供删除。[#167](https://github.com/zhongwater123/A-NAS/issues/167) 指出文件服务的审计表没有保留策略，并列出“设定保留规则”一项；修复只给审计表建了索引，没有改保留期限，因为改变它需要产品决定（[调查](../investigations/2026-10-21-whole-space-reconcile-scale.md)）。不定的话，活动记录随使用一直增长。

## 选项

- **永久保存（现状）**：管理员任何时候都能查到全部历史；记录持续占用系统盘。
- **设定保留期限**：超过期限的记录自动删除，系统盘占用有上限；更早的历史查不到。还要一并定期限多长、是否由管理员设置。

## 决定

待定。

## 关联

- [日志规格](../specs/logs.md)
- [#167](https://github.com/zhongwater123/A-NAS/issues/167)、[全空间对账的调查](../investigations/2026-10-21-whole-space-reconcile-scale.md)
