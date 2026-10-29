# 未识别块设备阻断整次宿主机观测

状态：fixed（issue #183），待实机验收
更新时间：2026-10-27

## 症状与复现

读卡器空槽、zram、eMMC boot 设备或缺少身份的硬盘与正常系统盘同时出现时，`Reader.Read` 整体失败，磁盘列表、健康状态和存储规划随之不可用。两块盘报告相同 WWN，或相同型号与序列号，也触发整次失败。

只用合成 `lsblk --json` 结果即可复现，不需要操作真实磁盘：

```bash
go test ./internal/hoststate/linux -run TestUnidentifiedDiskDoesNotBlockHostObservation -count=1
```

测试包含一块正常系统盘和一只容量为 0 的空读卡器。修复前结果：

```text
an empty card reader blocked host observation: read block devices: disk "sdb" is missing model or capacity
```

## 根因

- `parseDisks` 把型号或容量缺失、无法派生身份、身份重复当作整次读取错误。
- Host Agent 的 IPC 解码也拒绝空型号和零容量；公共 Disk 契约进一步限制型号非空、容量至少为 1。只修改 Linux Adapter 仍会在下一层失败。
- 原来的全局失败间接阻止了重复身份被操作。放宽观测后必须显式拒绝歧义解析，不能让 `ResolveDevice` 返回第一个匹配项。

## 修复边界

- 每个磁盘携带 `ObservationIssues`，正常设备保持原稳定 ID 与行为。
- 先统计整份观测中的硬件身份，再标记全部重复项。无身份或重复身份使用独立命名空间下的展示键；键不暴露设备名或序列号，不承诺跨观测稳定。
- 不为不可用观测读取或复用 SMART，避免把同一缓存读数配给两个不同设备。
- IPC 接受带对应原因的空型号、零容量，仍拒绝无原因的缺失值及未知原因码。公共 API 透出不可选原因，界面说明问题，存储规划与设备解析分别拒绝操作。
- `lsblk` 命令失败、JSON 损坏、不能唯一识别系统盘，仍是整次观测失败。没有把未知设备自动纳入数据卷，也没有调整 USB 支持范围。

## 回归覆盖

- Linux Adapter：空读卡器、zram swap、eMMC boot、SATA 缺少型号/身份、只读设备；健康盘继续解析；系统盘缺少身份仍保留角色。
- 重复 WWN / 序列号：全部设备不可用，展示键唯一，旧稳定 ID 不再解析；没有歧义 SMART 目标。
- Host Agent IPC：不可用观测经真实 Unix socket 往返保留。
- 产品 API：磁盘、健康与组合状态均成功，含空型号、零容量的响应符合契约。
- 存储规划：异常设备被拒，正常磁盘仍可创建计划。
- Web：列表说明原因，初始化候选中异常设备不可选。

## 关联

- [issue #183](https://github.com/zhongwater123/A-NAS/issues/183)
- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [Linux Reader](../../internal/hoststate/linux/reader.go)、[回归测试](../../internal/hoststate/linux/unidentified_test.go)
- [IPC](../../internal/hoststate/agent/agent.go)、[公共契约](../../api/components/system.yaml)
- [存储规划](../../internal/storage/service.go)、[存储界面](../../web/src/settings/StorageSection.tsx)
