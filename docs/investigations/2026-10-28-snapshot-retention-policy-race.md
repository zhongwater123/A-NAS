# 保存快照保留设置后，后台仍按旧策略删除快照

状态：resolved（真实 Service、SQLite 与受控后端已确定性复现并回归；不依赖实机设备状态）
更新时间：2026-10-28

## 症状与影响

定时快照正在拍摄时，把保留份数从 1 改为 3，或关闭计划，保存成功后仍可能丢失上一份快照。这不是源文件被删除，但已经向用户确认的新保留要求没有生效，旧恢复点无法再使用。自主审查后创建 [#304](https://github.com/zhongwater123/A-NAS/issues/304)。

## 最小复现

1. 使用真实文件服务与 SQLite Catalog，给自己的空间设置每小时 `:15` 拍摄、保留 1 份。
2. 11:15 拍摄第一份；12:15 拍摄第二份，在 SnapshotBackend 返回文件清单之前保存“保留 3 份”，或保存关闭计划且保留份数仍为 1。
3. 让拍摄完成。修复前两个场景都只剩 1 份，首份在后端已被删除；期望两份都在。

```bash
go test ./internal/files -run 'TestScheduledSnapshotHonorsRetentionChangedDuringCapture|TestRetentionOrdersChangesAndKeeping' -count=1
```

## 观察事实与根因

基线 `cb62d69` 的 `RunDueSnapshots` 先读全部计划，`runSchedule` 拍摄、采集文件清单后仍把先前读取的计划传给 `expireScheduledSnapshots`。设置更新不受这条路径协调，旧清理决定可以晚于新设置的成功响应执行。基线运行两个用例均得到 `catalog=1, first present=false; want 2 and true`。

自动清理的快照列表与“保留此快照”也没有共同的并发顺序：仅在列表中筛选 `scheduled` 不能保护稍后才转换为手动的条目。因此只在清理前重读设置还不够。

## 修复与回归证据

每个空间的计划更新、保留操作与清理共用一个可取消的信号量，复用已有空间协调对象与 `golang.org/x/sync/semaphore`。清理持有它时重新读取设置和定时快照列表，每删除一份就放开，下一份开始前重新读取；关闭的计划不清理。拍摄与文件清单采集不持有它，其他空间有各自的信号量。

“保留”在等待结束后重新读取快照，已经过期则返回不存在，避免报告虚假的保留成功。手动删除仍按用户的显式操作执行，并与保留操作使用同一闸门。

- 两个拍摄期间修改设置的回归已由失败转为通过。
- WSL 原生副本中的 `make check-steps` 全部目标通过，包含全部 Web/Go 测试、静态检查、文档、运维和发布构建；上传前项目规范与需求符合性审查均无剩余发现。
- 后端删除被通道阻塞时，同空间设置更新与保留请求超时取消且不落库；其他空间能保存设置。释放后清理完成，后续新策略正常生效。
- 测试使用可控后端在真实服务边界上制造顺序，核对 Catalog 和后端对象；没有在实验 NAS 上制造数据删除，也不将本测试声称为真实 Btrfs 故障注入。

## 关联

- 规格：[备份中心](../specs/backup-center.md)
- 代码：[保留清理](../../internal/files/schedule.go)、[快照操作](../../internal/files/snapshot.go)、[空间协调](../../internal/files/ordering.go)
- 回归：[策略更新与清理并发](../../internal/files/schedule_concurrency_test.go)、[快照计划](../../internal/files/schedule_test.go)、[智能保留](../../internal/files/snapshot_history_test.go)
- 运行手册：[实验存储验收](../runbooks/provision-v1.0.1-experimental-storage.md#功能验收)

## 审查补充

每份删除单独持有空间闸门，下一份删除前重新读取策略与快照列表；等待中的设置更新或保留操作可以在两次删除之间生效。手动删除也持有闸门并重新授权。孤儿清扫只删除未登记完成的对象，不删除可被 KeepSnapshot 保留的已登记后端对象。
