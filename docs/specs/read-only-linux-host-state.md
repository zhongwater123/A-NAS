# Debian 只读宿主机状态

状态：implemented
更新时间：2026-10-06

## 目标

Host Agent 内的 Linux Adapter 在 Debian 13 上通过既有 `hoststate.Reader` seam 返回真实系统与磁盘状态，同时隐藏原始机器 ID、WWN、序列号和设备路径，并且不修改宿主机状态。

## 非目标

- 不把 Linux Adapter 直接注入产品服务。
- 不决定或实现 Host Agent IPC。
- 不执行 SMART、NVMe 自检、温度探测、挂载、分区、格式化或任何写操作。
- 不安装系统软件，不为缺少稳定硬件身份的磁盘伪造路径型 ID。

## 场景

### 读取 Debian 系统状态

- Given：可读的 `os-release`、machine-id、uptime、主机名和固定观测时钟。
- When：调用 Linux Adapter 的 `Read(ctx)`。
- Then：一次返回 Debian 名称与版本、Go 规范架构名、整秒运行时长、稳定且不暴露 machine-id 的主机 ID，以及同一 UTC 观测时间。

### 发现并分类磁盘

- Given：`lsblk --json` 返回拥有 WWN 的磁盘及其分区树，其中一个后代挂载到 `/`。
- When：调用 `Read(ctx)`。
- Then：只返回 `type=disk` 的顶层磁盘；包含根文件系统后代的磁盘角色为 `system`，其余为 `unassigned`，并按稳定资源 ID 排序。

### 保持磁盘身份稳定且不泄露硬件标识

- Given：同一 WWN 在重启后获得不同的内核设备名或挂载点。
- When：分别读取两次状态。
- Then：公开磁盘 ID 保持一致，且不包含 WWN、序列号、`/dev` 路径或内核设备名。

### 健康信息尚未评估

- Given：本轮不执行 SMART 或设备温度探测。
- When：返回系统和磁盘状态。
- Then：健康状态为 `unknown`，磁盘温度为空，不把“读取成功”误报为“设备健康”。

### 拒绝不稳定或矛盾的输入

- Given：磁盘缺少 WWN 和序列号、两个磁盘生成重复身份，或系统/`lsblk` 输入无法解析。
- When：调用 `Read(ctx)`。
- Then：整个观测失败并返回内部错误；产品接口仍按既有契约映射为不泄露细节的 `503 state_unavailable`。

## 边界与失败

- 外部 seam 保持为 `hoststate.Reader.Read(ctx)`，不增加 Linux 专用方法。
- Adapter 只读取固定文件，并以固定参数直接执行 `lsblk`；不通过 Shell 拼接命令。
- 稳定资源 ID 使用带版本域的 SHA-256 派生值并截取 128 位；原始身份只在单次读取的内存中存在。
- 上下文取消、文件读取失败或命令失败都使整个观测失败，不返回真假混合的部分状态。
- M2 实机验收只读取系统盘和安装 U 盘；当前未检测到的 512 GB 机械盘不得因此被推断为不存在或被格式化。

## 验收证据

- Linux Adapter 的 Reader seam 测试覆盖健康读取、身份稳定、排序、系统盘分类、失败输入和上下文取消。
- Fake 与 Linux Adapter 均满足同一 `hoststate.Reader` 接口。
- `make check` 通过。
- 在实验 NAS 上运行只读探针时，不需要 root，且不产生系统状态变化。
- 2026-10-06：静态 Linux/amd64 测试制品在 Debian 13 实验 NAS 上运行 `TestReaderReadsLocalLinuxHost` 通过。

## 关联

- 架构：[架构总览](../architecture/OVERVIEW.md)
- ADR：[只读 Host Agent 状态使用 Unix Socket 上的 HTTP/JSON](../adr/0004-use-http-json-over-unix-socket-for-host-state.md)
- 代码：[宿主机状态接口](../../internal/hoststate/state.go)、[Linux Adapter](../../internal/hoststate/linux/reader.go)
- 测试：[Reader seam 测试](../../internal/hoststate/linux/reader_test.go)、[Debian 集成测试](../../internal/hoststate/linux/reader_integration_test.go)
