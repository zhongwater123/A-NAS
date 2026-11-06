# 家庭中心数据一致性与清理

状态：fixed
日期：2026-11-05
关联问题：[#363](https://github.com/zhongwater123/A-NAS/issues/363)

## 复现与原因

在主干 `b1f16f65` 上，`go test ./internal/family -run 'TestUpdatePreservesInvisiblePhotos|TestPurgeRetainsRecentUploads' -count=1` 的新增用例复现：

- 带可见照片保存时，隐藏引用被删除；只传文字时，服务器把已存的隐藏 ID 重新送入新照片验证，返回 `invalid request`。
- `Purge` 删除了刚创建的 `.upload-active`，却没有删除数据库中无对应记录的 `rec_orphan`。

另外四处边界缺口是：家庭审计未接入聚合日志且无过期清理；录音直接写盘，没有统一容量准入；留言墙固定截断 200 封；创建记录仅在事务之前查请求键，照片校验期间发生的并发重试可能返回冲突或“已经在频道中”。

## 修复边界

- 更新只替换可见引用，已存不可见引用保留角色与拍摄日期；未修改的 Web 照片列表省略。保留引用只能来自数据库，不能由客户端伪造。版本检查仍在写事务内。
- 无拍摄时间的隐藏照片以记录日期的本地中午恢复回退时间，不能把 UTC 午夜直接转成本地时区，也不能依赖夏令时切换日可能不存在的本地午夜；UTC−5 与圣保罗 2018-11-04 的回归测试覆盖显式编辑照片列表时日期提前一天的问题。
- 请求键在写事务内再次查询，早于频道去重；唯一约束用 SQLite 扩展错误码识别。
- 录音使用统一保留容量检查；活跃临时文件及待提交最终文件登记在同一进程锁保护的集合中，扫描不会误删。临时文件的过期规则处理崩溃残留，最终文件按数据库对账；单个删除失败不会阻止扫描其他文件。
- 留言墙和日志沿用已有游标及活动查询组件，保留期、权限与可见行为由[规格](../specs/family-center.md#边界与失败)定义。

## 验证

- `go test ./internal/family ./internal/familyapi ./internal/familyservice ./internal/activity ./internal/activityapi ./internal/photosapi ./internal/httpapi`。
- `cd web && npx vitest run src/family/FamilyPanel.test.tsx src/settings/logs`。
- 完整门禁与并发检查在 WSL 的独立 ext4 副本执行，结果记录于修复 PR。

## 关联

- [领域回归测试](../../internal/family/data_regression_test.go)、[聚合日志契约测试](../../internal/httpapi/activity_test.go)、[Web 测试](../../web/src/family/FamilyPanel.test.tsx)
- [照片更新](../../internal/family/records.go)、[录音与清理](../../internal/family/recordings.go)、[家庭活动](../../internal/family/activity.go)、[留言墙](../../internal/family/comments.go)
- [技术设计](../architecture/family-center.md)、[ADR 0029](../adr/0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)
