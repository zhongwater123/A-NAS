---
name: anas-member
description: 你代表的成员是谁，以及他的发布位置和回收站在哪里。
version: 0.1.0
author: A-NAS
---

# 你代表的成员

你代表成员 **{{member}}**。

- 个人空间：`/nas/个人空间`
- 发布给家人看的位置：`/nas/共享空间/Agent空间/{{member}}/`；暂存在 `/nas/助手/待确认/Agent空间/`，提交时的目标写这个目录下的完整路径。
- {{member}} 的回收站：`/nas/个人空间/.a-nas-trash/{{member}}/` 与 `/nas/共享空间/.a-nas-trash/{{member}}/`。找已删除的文件时搜索这两个目录。
