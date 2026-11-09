# 分散任务把未知分配量当成完成，后台测量没有超时

状态：resolved
更新时间：2026-11-09

## 症状与影响

PR #429 的镜像分散任务在设备分配量无法解析、缺盘或设备数量不符时，可能清除已经记录的停止状态；观测恢复后，即使硬盘没有变化也会自动重试。正在运行的分散也可能提前取消 balance。

后台测量直接使用没有期限的上下文。若 `btrfs filesystem show --raw` 卡住，分散任务无法结束；即使 balance 已结束，监测 goroutine 也可能让 `watch.Wait()` 一直等待，加盘因此持续被拒绝。

## 最小复现

在 Linux 开发环境运行：

```bash
go test ./internal/hostops/linux -run 'TestUnknownAllocation|TestSpreadMeasurementTimeout' -count=1
```

- `TestUnknownAllocationPreservesTheSpreadStop`：记录三块硬盘上的停止状态，再分别提供无法解析、缺盘、设备数不完整的观测。停止标记必须保持；有效观测恢复后，原来的硬盘不能自动重新分散。
- `TestUnknownAllocationDoesNotCancelTheSpread`：保持完整 balance 运行，第一次测量返回未知，随后返回全部空间可用。只能在后一次测量后取消。
- `TestSpreadMeasurementTimeoutReleasesTheVolume`：命令替身一直等测量上下文超时；使用 Go 虚拟时钟验证后台测量在十秒内结束，分散任务释放互斥，随后可以加盘。

测试只使用命令替身和临时目录，不读写真实磁盘。

## 根因

[`VolumeDevices`](../../internal/hoststate/linux/datavolume.go) 用 `nil` 表示未知分配量；[`Unusable`](../../internal/datavolume/layout.go) 在没有设备数据时返回零。`measureSpread` 直接连接两者并返回空错误，使调用者无法区分“未知”和“已完成”，进而清除 `spreadStopped` 或取消 balance。

宿主机状态读取器已有十秒超时，但分散任务经另一条 `CommandRunner.Output` 路径执行相同命令，不经过读取器。`context.Background()` 和执行器均不提供默认期限。

## 修复与回归证据

- [`measureSpread`](../../internal/hostops/linux/volume.go) 对未知或不足两块设备的分配量返回错误；调用者保留停止记录，并跳过完成判定。
- 每次测量用 `context.WithTimeout` 限制为十秒，仍遵守调用方更短的期限；不对长时间运行的 balance 本身施加这一超时。
- 修复前，停止记录测试的三种场景均失败，记录从三块设备的键变为空字符串；超时测试的两次后台读取均缺少期限。
- 修复后，上述回归命令通过。测试位于 [`volume_internal_test.go`](../../internal/hostops/linux/volume_internal_test.go)。

## 关联

- 规格：[硬盘用途与数据卷冗余](../specs/disk-roles-and-redundancy.md#边界与失败)
- ADR：[0025：用 Btrfs profile 将数据卷跨越多块硬盘](../adr/0025-span-the-data-volume-across-disks-with-btrfs-profiles.md)
- 运行手册：[把硬盘加入数据卷](../runbooks/add-a-disk-to-the-data-volume.md)
- PR：[修复与回归测试所在的 #429](https://github.com/zhongwater123/A-NAS/pull/429)
