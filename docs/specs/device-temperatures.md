# 设备温度

状态：implemented
更新时间：2026-10-29

## 目标

在“设置 → 概览”与“任务管理器 → 设备运行状态”中看到 CPU、GPU（有的话）、固态硬盘与机械硬盘的温度，并标出哪一项偏高。此前只有硬盘温度：存储页每块盘后附一个读数，健康检查只在硬盘过热时提到它；CPU 温度无处可看。

## 非目标

- 不调节风扇。是否支持调速取决于主板的风扇控制芯片，见[下一步](#下一步风扇)。
- 不保存温度历史，不发送通知；任务管理器的趋势只保留页面打开期间的采样，与[任务管理器](task-manager.md)其他指标一致。
- 不为监控加载内核模块、安装软件包或改变设备权限。

## 读数从哪里来

| 部件 | 来源 | 更新 |
|---|---|---|
| CPU | hwmon 的 `coretemp`（Intel，取 `Package id N`）、`k10temp` 或 `zenpower`（AMD，取 `Tdie`，没有时取 `Tctl`）、`cpu_thermal`（ARM）；都没有时取 `x86_pkg_temp` 温区。多颗处理器取最热的一颗 | 每次读取 |
| 独立显卡 | DRM 设备自己的 hwmon：amdgpu 取 `edge`，xe 取 `pkg`，其他取编号最小的读数 | 每次读取 |
| 核显 | 没有单独的传感器，显示“同处理器”。Intel 核显固定在 PCI `00:02.0`，按此识别 | — |
| NVMe 固态 | 控制器的 hwmon `Composite`，即 SMART 报告的同一温度 | 每次读取 |
| SATA 固态、机械硬盘 | 现有的 SMART 缓存（[基础存储与共享](basic-storage-and-sharing.md#服务与错误)） | 至多每 10 分钟；待机的硬盘不唤醒，沿用上次读数，超过 24 小时为未知 |

- 读取 hwmon 只是读 sysfs 文件，不需要权限，也不唤醒设备。SATA 硬盘不用内核的 `drivetemp`：它的文档说明读取温度可能重置硬盘的休眠计时，频繁读取会让硬盘无法休眠。
- 读数取整到 1 °C。不在 −40 °C 到 150 °C 之间的读数视为传感器错误，不显示，例如未接探头时的 −128 °C。

## 偏高的界限

每个读数随附 `hotCelsius`：达到它就算偏高。界面与健康检查用同一个数，不在前端另设阈值。

- CPU 与独立显卡：传感器报告的临界温度（`temp*_crit`，到这里会强制降频或关机）减 10 °C；传感器不报告时取 85 °C。实验 NAS 的 i3-12100 临界温度为 100 °C，所以 90 °C 起偏高。
- 硬盘：沿用[系统健康](system-health.md)的界限，机械硬盘 55 °C，固态 70 °C。

界面在离界限 10 °C 以内时把读数显示为琥珀色，达到界限显示红色并标“偏高”。

## 场景

### 在概览中查看

- Given：任一账号已登录。
- When：打开“设置 → 概览”。
- Then：“健康检查”下方是“设备温度”，依次列出处理器、每块显卡与每块硬盘。每行显示读数和一条到界限之外 15 °C 的温度条，界限处有刻度。硬盘行写明它是系统盘、数据盘还是未使用，以及机械、NVMe 或 SATA 固态。核显一行写“同处理器”。分组下方说明各硬盘读数的更新频率。数据随桌面的宿主机状态每 10 秒刷新。

### 在任务管理器中查看

- Given：任一账号打开“任务管理器 → 设备运行状态”。
- When：停在“总览”。
- Then：四张指标卡下方是“温度”，每个传感器一块：CPU、每块 GPU、每块硬盘。
- When：切换到“CPU”。
- Then：使用率下方是“CPU 温度”曲线，刻度 0–100 °C，写明偏高的界限。
- When：切换到“GPU”或“硬盘”。
- Then：GPU 的指标中有“温度”（核显为“同 CPU（读数）”）；每块硬盘的标题右侧显示温度条，没有读数时写“暂无温度读数”。

### 处理器过热

- Given：CPU 达到它的偏高界限。
- When：打开概览。
- Then：系统健康为“需要注意”，健康检查列出“处理器温度 N °C，偏高”和处理办法；设备温度中处理器一行标“偏高”。

### 没有传感器或旧 Host Agent

- Given：主机没有 CPU 温度传感器，或 Host Agent 是不报告温度的旧版本。
- When：打开概览。
- Then：处理器一行分别显示“未检测到温度传感器”与“暂时无法读取”；硬盘照常显示 SMART 读数。任务管理器中缺失的读数写明原因，不显示为 0。

## 边界与失败

- 接口：`/api/v1/host-state` 与 `/api/v1/system` 的 `system.thermal` 为可选对象：`cpu`（可选）与 `gpus`（`id`、`name`、`integrated`、可选的 `temperature`），温度为 `{celsius, hotCelsius}`；没有观测到时整个字段缺失。磁盘有 `temperatureCelsius` 时同时带 `temperatureHotCelsius`。任务管理器的 `cpu`、每块 `gpu` 与每块 `disks` 项带可为 `null` 的 `temperature`，磁盘另带 `rotational`，GPU 另带 `integrated`。
- Host Agent 的状态文档在 `system.thermal` 中携带同样的内容；产品服务拒绝超出 −40～150 °C 的读数或重复的 GPU。旧版产品服务忽略这个字段。
- 健康检查增加 `cpu_hot`（注意，`value` 为摄氏度），规则写在[系统健康](system-health.md)中。
- 成员与管理员看到相同的温度，不涉及个人数据。
- 部署需同时更新 Host Agent 与产品服务；只更新产品服务时，概览的处理器一行显示“暂时无法读取”。

## 下一步：风扇

风扇转速与调速要先确定主板的风扇控制芯片（Super I/O）。实验 NAS 的精粤 B760I GAMING 没有自动加载任何风扇驱动，芯片型号也未公开；需要以 root 加载内核自带的 `nct6775`、`it87` 识别一次。识别结果决定后续做法：内核自带驱动可用时，用 lm-sensors 的 `fancontrol` 做调速循环，设置中提供“自动（主板控制）、静音、标准、全速”；需要内核外驱动时只显示转速或不做调速。调速涉及 root 写硬件，届时另写 ADR 与运行手册。

## 验收证据

- `internal/hoststate/linux/thermal_test.go`：CPU 取最热的封装与 10 °C 余量、AMD 取 `Tdie` 与默认界限、退回 `x86_pkg_temp`、丢弃不合理的读数、NVMe 实时读取而 SATA 不读 hwmon、独立显卡的 `edge` 与核显、任务管理器快照携带温度。
- `internal/hoststate/assess_test.go` 的 `TestAssessReportsAHotCPU`：界限上下的 `cpu_hot`。
- `internal/hoststate/agent/rootagent/volume_test.go` 的 `TestClientReadsTheThermalState`：温度经 Host Agent 套接字往返，拒绝 400 °C。
- `internal/httpapi` 的接口与 OpenAPI 契约测试：`thermal`、`temperatureHotCelsius` 与任务管理器字段符合契约。
- `web/src/settings/DeviceTemperatures.test.tsx` 与 `TaskManagerSection.test.tsx`：概览各行、偏高标记、核显、缺失原因，以及任务管理器的温度分区和 CPU 温度曲线。
- 实机：部署后对照实验 NAS 上 `/sys/class/hwmon/*/temp*_input` 与 `smartctl -A`，确认概览与任务管理器的读数一致。

## 关联

- 规格：[任务管理器](task-manager.md)、[系统健康](system-health.md)、[设置](settings.md)、[基础存储与共享](basic-storage-and-sharing.md)
- ADR：[ADR 0004](../adr/0004-use-http-json-over-unix-socket-for-host-state.md)
- 代码：[`internal/hoststate/linux/thermal.go`](../../internal/hoststate/linux/thermal.go)、[`internal/hoststate/state.go`](../../internal/hoststate/state.go)、[`internal/hoststate/assess.go`](../../internal/hoststate/assess.go)、[`web/src/settings/Temperature.tsx`](../../web/src/settings/Temperature.tsx)、[`web/src/settings/DeviceTemperatures.tsx`](../../web/src/settings/DeviceTemperatures.tsx)、[`web/src/settings/taskmanager/TaskManagerSection.tsx`](../../web/src/settings/taskmanager/TaskManagerSection.tsx)
- 测试：[`internal/hoststate/linux/thermal_test.go`](../../internal/hoststate/linux/thermal_test.go)、[`internal/hoststate/assess_test.go`](../../internal/hoststate/assess_test.go)
