# 向量索引测试在 gRPC 发布 Ready 状态前断言预热完成

状态：resolved
更新时间：2026-11-18

## 症状与影响

PR #538 合入 main 后，提交 `5f4f1a64` 的 [CI 44255902736](https://github.com/zhongwater123/A-NAS/actions/runs/44255902736) 只有竞态检测作业失败：`TestPrepareKeepsTheInstanceRunning` 报 `not Warm after Prepare`。日志没有数据竞争报告，Project checks、systemd 系统测试和安全检查通过。

这是测试对异步状态观察顺序的错误假设；成功的健康检查已经到达实例。产品的连接、按需启动与空闲退出逻辑不变。

## 复现与排除

使用项目固定的 Qdrant，设置 `ANAS_QDRANT_REQUIRED=1`，运行：

```bash
go test -race ./internal/vectorindex -run '^TestPrepareKeepsTheInstanceRunning$' -count=100 -v
```

空闲机器上 100 次全部通过。为放大调度窗口，只在隔离的临时依赖副本中修改 gRPC v1.85.0 的 `ccBalancerWrapper.UpdateState`：在 `pickerWrapper.updatePicker(s.Picker)` 与 `csMgr.updateState(s.ConnectivityState)` 之间，对 `connectivity.Ready` 插入 50 毫秒等待；通过临时 `-modfile` 的 `replace` 使用该副本，共享模块缓存与仓库依赖不变。

原测试在这个窗口下连续失败 3 次，诊断均为 `Warm=false, connections=1`。因此排除代理连接计数滞后或没有建立连接；失败发生在首次健康检查之后、400 毫秒空闲超时之前。注入只是延迟已有的状态发布，没有改变 RPC 的结果。

## 根因与修复

gRPC 先更新 picker，让待发 RPC 可以使用已经就绪的连接，再发布供 `GetState` 读取的连接状态。快速健康检查可能先返回，此时 `Warm()` 读取的状态仍为 Connecting。测试把成功 RPC 与可观测的 Ready 当成同步事件。

`TestIndex` 在首次创建集合后也有相同假设。两处测试共用限时等待：每 10 毫秒读取 `Warm()`，最多 10 秒；连接始终没有就绪仍失败。预热测试仍单独断言首次连接存在，并持续调用 `Prepare` 超过空闲时限后检查连接存活。未放宽空闲超时，也未增加产品中的重试或等待。

修复后，带上述延迟的两个测试各连续通过 10 次。普通依赖下的包测试及完整检查结果记录在 PR 的验证部分；临时依赖副本和诊断日志不进入产品或提交。

## 关联

- 测试：[`vectorindex_test.go`](../../internal/vectorindex/vectorindex_test.go)
- 客户端：[`vectorindex.go`](../../internal/vectorindex/vectorindex.go)
- 架构：[向量索引的客户端](../architecture/vector-index.md#客户端internalvectorindex)
- 决定：[ADR 0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)
