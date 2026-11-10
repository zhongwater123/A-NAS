# hwmon 属性截断与异常 SMART 温度

状态：fixed
日期：2026-11-09

## 现象与复现

[#360](https://github.com/zhongwater123/A-NAS/issues/360) 记录了实验 NAS 的 `nct6798` 目录有 364 项，2 号风扇在转，但其输入不在前 256 项中。sysfs 的目录顺序与传感器编号无关，不能靠截取目录前缀发现输入。

本地回归使用 `fstest.MapFS`，在温度与风扇输入之前放 364 个无关属性；旧实现返回 `Fans.Status=absent`、CPU 温度为空，也不能记录 2 号风扇随后停转。另一组测试经 `RefreshSMART` 注入一块盘的 255°C 和另一块盘的正常读数，再读取设备快照：旧实现的 `DeviceSnapshot.Valid()` 为假，正是 Host Agent 与产品 API 返回 503 的校验条件。

```bash
go test ./internal/hoststate/linux -run 'Test(ThermalInputsAfterHundredsOfAttributes|SMARTTemperatureRange|InvalidSMARTTemperatureKeepsDeviceSnapshotAvailable)$' -count=1
```

## 原因与修复

- 同一个 `hwmonLimit=256` 同时限制设备发现与芯片目录内的全部属性，温度和风扇采集丢弃了截断标志。现在用标准库 `fs.Glob` 匹配完整目录中的 `temp*_input` 与 `fan*_input`，再校验编号、读取数值；设备发现的原有限制不变。输入匹配不再截断属性列表，因此不会把被截掉的输入误报成不存在。
- `parseSMART` 接受任意整数，但任务管理器温度契约只允许 −40–150°C。现在在进入缓存之前用 `hoststate.PlausibleCelsius` 丢弃异常温度，保留独立的 SMART 健康结论；通用温度与 NVMe SMART 分支均覆盖。系统 API 的磁盘温度契约同步为同一区间。
- 回归还覆盖边界 −40/150°C、正常与异常两块盘同时存在、CPU/内存/网络继续可用，以及已转风扇停转后仍然可见。

## 实机验证边界

本次首先验证本地采集链路与接口契约。上述 NAS 数据来自 issue 的 2026-11-03 只读记录，不代表修复已部署。部署后仍需对照 `fan2_input` 与概览、任务管理器中的 2 号风扇读数，步骤见[设备温度验收](../specs/device-temperatures.md#验收证据)。

## 关联

- 规格：[设备温度与风扇](../specs/device-temperatures.md)
- 边界：[ADR 0004](../adr/0004-use-http-json-over-unix-socket-for-host-state.md)
- 代码：[hwmon 采集](../../internal/hoststate/linux/thermal.go)、[SMART 解析](../../internal/hoststate/linux/smart.go)、[系统接口契约](../../api/components/system.yaml)
- 测试：[回归测试](../../internal/hoststate/linux/thermal_regression_test.go)
