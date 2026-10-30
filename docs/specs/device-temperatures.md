# 设备温度与风扇

状态：implemented
更新时间：2026-10-30

## 目标

在“设置 → 概览”与“任务管理器 → 设备运行状态”中看到 CPU、GPU（有的话）、固态硬盘与机械硬盘的温度与风扇转速，并标出哪一项偏高、哪个风扇停了。此前只有硬盘温度：存储页每块盘后附一个读数，健康检查只在硬盘过热时提到它；CPU 温度无处可看。

## 非目标

- 不调节风扇转速。实验 NAS 的芯片支持调速，但用户决定只显示转速（2026-10-30）；风扇仍由主板 BIOS 的曲线控制。
- 不保存温度历史，不发送通知；任务管理器的趋势只保留页面打开期间的采样，与[任务管理器](task-manager.md)其他指标一致。
- 除风扇传感器芯片的内核自带驱动外，不为监控加载内核模块、安装软件包或改变设备权限。

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

## 风扇转速

- 来源：主板传感器芯片（Super I/O）的 hwmon `fan<N>_input`，每次读取。显卡驱动（amdgpu、radeon、nouveau、i915、xe）的风扇属于显卡，不列出。
- 驱动：Linux 不会自动加载这类芯片的驱动。安装器加载内核自带的 `nct6775` 与 `it87`，没有对应芯片的主板上它们找不到设备而加载失败；找到芯片的写入 `/etc/modules-load.d/a-nas-sensors.conf`，开机自动加载。不使用强制参数；加载只读取芯片，不改变任何风扇设置。实验 NAS 的精粤 B760I GAMING 用 Nuvoton NCT6798D，由 `nct6775` 驱动（2026-10-29 以 root 探测确认，两个接口有风扇，另五个未接）。
- 列出哪些：接口没接风扇和风扇停转时读数都是 0。Host Agent 只列出现在在转或启动以来转过的接口；转过而现在为 0 的标为“已停转”。Host Agent 重启后，重启前已停的风扇不再列出。
- 名称：芯片给接口命名时用它的名称，否则为“风扇 N”，N 是芯片的接口编号。多数芯片不命名，所以看不出哪个是 CPU 风扇。

## 偏高的界限

每个读数随附 `hotCelsius`：达到它就算偏高。界面与健康检查用同一个数，不在前端另设阈值。

- CPU 与独立显卡：传感器报告的临界温度（`temp*_crit`，到这里会强制降频或关机）减 10 °C；传感器不报告时取 85 °C。实验 NAS 的 i3-12100 临界温度为 100 °C，所以 90 °C 起偏高。
- 硬盘：沿用[系统健康](system-health.md)的界限，机械硬盘 55 °C，固态 70 °C。

界面在离界限 10 °C 以内时把读数显示为琥珀色，达到界限显示红色并标“偏高”。

## 场景

### 在概览中查看

- Given：任一账号已登录。
- When：打开“设置 → 概览”。
- Then：“健康检查”下方是“温度与风扇”，依次列出处理器、每块显卡、每块硬盘与每个风扇。每行显示读数和一条到界限之外 15 °C 的温度条，界限处有刻度。硬盘行写明它是系统盘、数据盘还是未使用，以及机械、NVMe 或 SATA 固态。核显一行写“同处理器”。风扇行显示转速（如“1,912 RPM”）。分组下方说明各硬盘读数的更新频率与风扇转速的来源。数据随桌面的宿主机状态每 10 秒刷新。

### 在任务管理器中查看

- Given：任一账号打开“任务管理器 → 设备运行状态”。
- When：停在“总览”。
- Then：四张指标卡下方是“温度与风扇”，每个传感器一块：CPU、每块 GPU、每块硬盘、每个风扇。没有风扇传感器时标题只写“温度”。
- When：切换到“CPU”。
- Then：使用率下方是“CPU 温度”曲线，刻度 0–100 °C，写明偏高的界限。
- When：切换到“GPU”或“硬盘”。
- Then：GPU 的指标中有“温度”（核显为“同 CPU（读数）”）；每块硬盘的标题右侧显示温度条，没有读数时写“暂无温度读数”。

### 处理器过热

- Given：CPU 达到它的偏高界限。
- When：打开概览。
- Then：系统健康为“需要注意”，健康检查列出“处理器温度 N °C，偏高”和处理办法；设备温度中处理器一行标“偏高”。

### 风扇停转

- Given：风扇 1 转过，之后读数为 0。
- When：打开概览或任务管理器。
- Then：概览中风扇 1 一行标“已停转”并提示检查风扇和接线，分组标题旁写“1 个风扇停转”；任务管理器中这一块写“已停转”，转速显示为红色的 0。

### 没有传感器或旧 Host Agent

- Given：主机没有 CPU 温度传感器，或 Host Agent 是不报告温度的旧版本。
- When：打开概览。
- Then：处理器一行分别显示“未检测到温度传感器”与“暂时无法读取”；硬盘照常显示 SMART 读数；没有风扇行。任务管理器中缺失的读数写明原因，不显示为 0。

## 边界与失败

- 接口：`/api/v1/host-state` 与 `/api/v1/system` 的 `system.thermal` 为可选对象：`cpu`（可选）、`gpus`（`id`、`name`、`integrated`、可选的 `temperature`）与 `fans`（`id`、`number`、可选的 `label`、`rpm`、`stopped`），温度为 `{celsius, hotCelsius}`；没有观测到时整个字段缺失。磁盘有 `temperatureCelsius` 时同时带 `temperatureHotCelsius`。任务管理器的 `cpu`、每块 `gpu` 与每块 `disks` 项带可为 `null` 的 `temperature`，磁盘另带 `rotational`，GPU 另带 `integrated`；快照另有 `fans` 分组，没有风扇传感器时为 `absent`。
- Host Agent 的状态文档在 `system.thermal` 中携带同样的内容；产品服务拒绝超出 −40～150 °C 的读数、超过 50000 RPM 或“已停转”却有转速的风扇，以及重复的 GPU 或风扇。旧版产品服务忽略这个字段。
- 健康检查增加 `cpu_hot`（注意，`value` 为摄氏度），规则写在[系统健康](system-health.md)中。
- 成员与管理员看到相同的温度，不涉及个人数据。
- 部署需同时更新 Host Agent 与产品服务；只更新产品服务时，概览的处理器一行显示“暂时无法读取”。

## 验收证据

- `internal/hoststate/linux/thermal_test.go`：CPU 取最热的封装与 10 °C 余量、AMD 取 `Tdie` 与默认界限、退回 `x86_pkg_temp`、丢弃不合理的读数、NVMe 实时读取而 SATA 不读 hwmon、独立显卡的 `edge` 与核显、任务管理器快照携带温度。
- `internal/hoststate/assess_test.go` 的 `TestAssessReportsAHotCPU`：界限上下的 `cpu_hot`。
- `thermal_test.go` 的 `TestReaderListsTheFansThatTurned`、`TestReaderHasNoFansWithoutASensorChip`：只列出转过的风扇、停转标记、不列显卡风扇、没有传感器时为 `absent`。
- `internal/hoststate/agent/rootagent/volume_test.go` 的 `TestClientReadsTheThermalState`：温度与风扇经 Host Agent 套接字往返，拒绝 400 °C 和有转速的“已停转”。
- `internal/httpapi` 的接口与 OpenAPI 契约测试：`thermal`、`temperatureHotCelsius` 与任务管理器字段符合契约。
- `web/src/settings/DeviceTemperatures.test.tsx` 与 `TaskManagerSection.test.tsx`：概览各行、偏高与停转标记、核显、缺失原因，以及任务管理器的温度与风扇分区和 CPU 温度曲线。
- 实机：部署后确认 `/etc/modules-load.d/a-nas-sensors.conf` 含 `nct6775`、`/sys/class/hwmon` 下有 `nct6798`，并对照 `temp*_input`、`fan*_input` 与 `smartctl -A`，确认概览与任务管理器的读数一致。

## 关联

- 规格：[任务管理器](task-manager.md)、[系统健康](system-health.md)、[设置](settings.md)、[基础存储与共享](basic-storage-and-sharing.md)
- ADR：[ADR 0004](../adr/0004-use-http-json-over-unix-socket-for-host-state.md)
- 代码：[`internal/hoststate/linux/thermal.go`](../../internal/hoststate/linux/thermal.go)、[安装器](../../scripts/install-v1.0.1-system-services.sh)、[`internal/hoststate/state.go`](../../internal/hoststate/state.go)、[`internal/hoststate/assess.go`](../../internal/hoststate/assess.go)、[`web/src/settings/Temperature.tsx`](../../web/src/settings/Temperature.tsx)、[`web/src/settings/DeviceTemperatures.tsx`](../../web/src/settings/DeviceTemperatures.tsx)、[`web/src/settings/taskmanager/TaskManagerSection.tsx`](../../web/src/settings/taskmanager/TaskManagerSection.tsx)
- 测试：[`internal/hoststate/linux/thermal_test.go`](../../internal/hoststate/linux/thermal_test.go)、[`internal/hoststate/assess_test.go`](../../internal/hoststate/assess_test.go)
