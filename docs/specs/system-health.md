# 系统健康

状态：implemented
更新时间：2026-10-27

## 目标

设置“概览”中的系统健康从实际观测得出，并写明原因和处理办法。此前 Linux 读取器把系统健康固定为 `unknown`，界面一直显示“健康未评估”：只读阶段不读 SMART，不能把“读取成功”说成“设备健康”；后来磁盘有了 SMART 结论，却没有人把它们汇总起来。

## 非目标

- 不检查服务、容器或网络；它们的状态由[任务管理器](task-manager.md)、Docker 与[日志](logs.md)呈现。
- 不发送通知，不保留健康历史。
- 不提供清零 Btrfs 错误计数的界面。
- 不改变磁盘自身的健康显示（存储页的“健康 / 健康未知”）和 [Web 桌面宿主机状态](web-desktop-host-state.md)中断线、过期的处理。

## 规则

系统健康取下表中最严重的一项。原因按严重程度排列，同级保持观测顺序；没有原因时为“运行正常”。只有一块磁盘都没有观测到时，系统健康为“未知”。

| 原因（`code`） | 条件 | 严重程度 |
|---|---|---|
| `disk_failing` | 任一磁盘的 SMART 自检判定失败 | 严重 |
| `volume_offline` | 已创建数据卷，但没有磁盘以数据盘身份挂载在 `/srv/a-nas/data` | 严重 |
| `volume_read_only` | 数据卷以只读挂载（Btrfs 发现错误后会自动转为只读） | 严重 |
| `volume_full` | 数据卷剩余空间少于写入保留量（总容量的 5%，至少 10 GiB），新文件会被拒绝 | 严重 |
| `disk_warning` | 任一磁盘报告了次于失败的问题 | 注意 |
| `disk_health_unknown` | 系统盘或数据盘读不到 SMART 结论；未分配的磁盘不计 | 注意 |
| `disk_hot` | 机械硬盘达到 55 °C，或固态存储达到 70 °C | 注意 |
| `volume_nearly_full` | 数据卷已用 90% 以上 | 注意 |
| `volume_errors` | Btrfs 自数据卷创建以来累计记录到读、写、刷新、校验或代际错误 | 注意 |
| `volume_reconcile_failed` | Host Agent 最近一次对账数据卷失败：没能修复空间权限、把身份清单写到数据卷或刷新 Samba 配置（[#166](https://github.com/zhongwater123/A-NAS/issues/166)）；数据卷只读时不报，由 `volume_read_only` 说明 | 注意 |

- SMART 结论与温度来自 Host Agent 的缓存，读取方式见[基础存储与共享](basic-storage-and-sharing.md#服务与错误)：待机的硬盘超过 24 小时没有新结果时为“未知”，因而出现 `disk_health_unknown`。
- 只读状态取自数据卷挂载点的 `statfs` 标志。错误计数取自 sysfs 的 `/sys/fs/btrfs/<fsid>/devinfo/*/error_stats`（Linux 5.14 起），按数据卷所在设备找到文件系统，并合计它的全部设备。计数保存在文件系统中，不会自行归零；读不到时不报告该项。
- 已用百分比与设置中的数据卷用量一致，四舍五入为整数。
- “已创建数据卷”取自产品服务保存的存储计划，不读取主机状态。
- 对账结果由 Host Agent 在状态文档中以 `reconcileFailed` 报告，只在失败时出现；对账在 Host Agent 启动后进行，失败后每 15 分钟重试，所以问题解决后最多 15 分钟消失（重启 Host Agent 立即重做）。

## 场景

### 一切正常

- Given：系统盘与数据盘的 SMART 都通过，温度正常，数据卷已挂载、可写、已用 40%，没有错误计数。
- When：打开设置的“概览”。
- Then：健康显示“运行正常”；“健康检查”只有一行“未发现问题”，说明检查了硬盘健康（SMART）与温度，以及数据卷的挂载、读写、剩余空间和错误记录。

### 有问题时写明原因

- Given：数据卷被内核转为只读，数据盘温度 57 °C。
- When：打开“概览”。
- Then：健康显示“严重故障”；“健康检查”标出“2 项需要处理”，依次列出“数据卷变成了只读”（严重）和“ST500DM002-1BD142 温度 57 °C，偏高”（注意），每项附处理办法和“查看存储”，点击切换到“存储”分区。

### 数据卷离线

- Given：已初始化数据卷，但数据盘没有挂载。
- When：读取 `GET /api/v1/host-state` 或 `GET /api/v1/system`。
- Then：`system.health` 为 `critical`，`system.healthFindings` 为 `[{"code":"volume_offline","severity":"critical"}]`。

### 数据卷对账失败

- Given：Host Agent 启动时数据卷可写，但刷新 Samba 配置失败。
- When：打开“概览”。
- Then：健康显示“需要注意”；“健康检查”列出“数据卷的权限与共享设置没能更新”，说明部分文件或共享可能无法正常使用，管理员可在“设置 → 日志”的 Host Agent 中查看原因，设备每 15 分钟重试。Web 文件、终端与日志照常可用。

### 从未初始化数据卷

- Given：新设备尚未创建数据卷，系统盘 SMART 通过。
- When：打开“概览”。
- Then：健康显示“运行正常”；尚未创建数据卷不算问题，数据卷卡片照旧显示“未初始化”。

## 边界与失败

- 概览单独显示系统盘空间：已用百分比、可用空间与总容量来自 Host Agent 对 `/var/lib` 所在文件系统的 `statfs` 观测；可用空间使用非特权进程能使用的 `Bavail`。它与硬盘标称容量、数据卷容量分别显示。未观测到时（包括旧 Host Agent）显示“暂时无法读取”，不推算为 0% 或 100%。`system.storage` 在 `/api/v1/system` 与 `/api/v1/host-state` 中均为可选对象，包含 `totalBytes` 和 `availableBytes`。
- 接口：`/api/v1/system` 与 `/api/v1/host-state` 的 `system` 增加 `healthFindings`。Host Agent 的状态文档在对账失败时带 `reconcileFailed: true`，否则不带这个字段，旧版产品服务读取正常的 Host Agent 不受影响。每项包含 `code`、`severity`（`warning` 或 `critical`），以及可选的 `diskId` 和 `value`（`disk_hot` 为摄氏度，`volume_full`、`volume_nearly_full` 为已用百分比，`volume_errors` 为错误次数）。Host Agent 的状态文档为数据盘增加 `filesystemReadOnly` 与 `filesystemErrors`，其他磁盘带上它们时，产品服务拒绝这份文档。
- 评估在产品服务中完成，Host Agent 只报告观测，因为只有产品服务知道是否创建过数据卷。Fake 读取器同样经过评估，模拟数据的健康由其中的磁盘状态决定。
- 成员与管理员看到相同的健康与原因，不涉及个人数据。
- 前端遇到不认识的 `code` 时显示通用说明；缺少 `healthFindings` 的旧响应按没有原因处理。

## 验收证据

- `TestAssessTakesTheWorstObservedFact`：各条规则、最严重优先、未分配磁盘的 SMART 未知不计、尚未创建数据卷不算离线。
- `TestAssessReportsAFailedReconciliation`、`TestClientReadsAFailedReconciliation`：对账失败成为注意项、只读时只报只读，以及 `reconcileFailed` 经 Host Agent 套接字往返。
- 系统测试：数据卷重新挂载为只读后重启 Host Agent，它照常服务并在状态中报告对账失败，概览报告 `volume_read_only`；恢复可写后对账成功、不再报告。
- `TestReaderReportsADataVolumeReadOnlyAndItsBtrfsErrors`、`TestBtrfsErrorsAreUnknownWithoutTheirCounters`：读取器从 `statfs` 与 sysfs 取得只读状态和错误计数。
- `TestClientReadsTheDataVolumeReadOnlyStateAndErrors`、`TestClientRejectsDataVolumeFactsOnAnotherDisk`：Host Agent 文档的往返与校验。
- `TestHostStateAssessesTheSystemHealth` 与 OpenAPI 契约测试：接口返回评估结果与原因。
- `SettingsPanel.test.tsx` 的“says why the system is rated as it is and leads to storage”“says what was checked when nothing is wrong”，以及 `web/src/settings/health.test.ts`。
- 实机：部署后在概览核对实验 NAS 的健康与原因，并与 `smartctl`、`findmnt`、`/sys/fs/btrfs/*/devinfo/*/error_stats` 对照。

## 关联

- 架构：[存储与文件架构](../architecture/storage-and-files.md)
- ADR：[ADR 0004](../adr/0004-use-http-json-over-unix-socket-for-host-state.md)
- 规格：[设置](settings.md)、[Web 桌面宿主机状态](web-desktop-host-state.md)、[Debian 只读宿主机状态](read-only-linux-host-state.md)
- 代码：[`internal/hoststate/assess.go`](../../internal/hoststate/assess.go)、[`internal/hoststate/linux/reader.go`](../../internal/hoststate/linux/reader.go)、[`internal/httpapi/server.go`](../../internal/httpapi/server.go)、[`web/src/settings/health.ts`](../../web/src/settings/health.ts)
- 测试：[`internal/hoststate/assess_test.go`](../../internal/hoststate/assess_test.go)、[`internal/hoststate/linux/volume_test.go`](../../internal/hoststate/linux/volume_test.go)、[`internal/httpapi/health_test.go`](../../internal/httpapi/health_test.go)
