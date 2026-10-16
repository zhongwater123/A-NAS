# Host Agent 每次请求都读 SMART，读盘超时被杀

状态：fixed locally；Experimental NAS verification pending
更新时间：2026-10-15

## 症状与影响

2026-10-15 的问题反馈：Host Agent 读硬盘信息间歇超时、进程被杀，24 小时内 49 次；每次请求约 1 秒。

影响不止硬盘信息。文件服务在每个文件请求之前都确认数据卷，这一步也走同一条读盘路径：

- 列目录、下载、缩略图与预览、开始和提交上传，每次都多等约 1 秒。
- 读盘超过 3 秒时，这个文件请求返回 `volume_unavailable`，界面显示“数据卷当前不可用”。
- 定时快照拍摄前也做这一步，超时就记为失败，15 分钟后重试。
- 本地控制台每 10 秒读一次主机状态，每次都对数据盘发 SMART 命令，机械盘永远进不了休眠。

## 最小复现

1. Experimental NAS 运行 `6b907d773f28`，数据盘是机械盘，本地控制台开着。
2. 在 Web 文件管理中打开一个有几十张图片的文件夹，让缩略图同时加载。
3. 实际：每个请求都要等 Host Agent 跑完 lsblk 和每块盘的 `smartctl --all`；几个请求同时到达时，SMART 命令在同一块机械盘上排队，超过产品服务的 3 秒超时后，smartctl 随请求一起被杀。期望：文件请求不等待硬盘信息；硬盘信息在后台更新，调用方放弃也不杀 smartctl。

## 观察事实

- Host Agent 的 `/v1/state` 每次依次运行 `lsblk` 和每块盘的 `smartctl --json --all`，用的是这次请求的 context（`internal/hoststate/linux/reader.go`）。产品服务调用 Host Agent 的超时是 3 秒（`internal/hoststate/agent/agent.go` 的 `requestTimeout`）。调用方超时或取消时，`exec.CommandContext` 杀掉正在运行的命令。
- 产品服务的数据卷守卫取预期 UUID 时调用 `storage.ListVolumes`（`cmd/anas-api/main.go`），后者读一次完整主机状态。守卫在 `files.Service.authorizedSpace` 中运行，覆盖列目录、下载、预览、上传开始与提交，以及定时快照拍摄前的检查。
- Web 桌面的 `useHostState` 每 10 秒请求 `/api/v1/host-state`，设置窗口的存储页也读 `/api/v1/volumes`。
- Experimental NAS（2026-10-15 18:37 以 `ssh a-nas` 只读查看）：`/opt/a-nas/current` 指向 `6b907d773f28`；数据盘 `sda` 是希捷 ST500DM002（500 GB 机械盘，SATA），系统盘是 NVMe；`lsblk` 用同样的参数运行三次，各 4–7 ms；负载约 2.7。
- `guoyi` 读不到系统日志、不能运行 `smartctl`，所以 49 次的日志原文与 smartctl 在 `sda` 上的实际耗时尚未亲见。“约 1 秒”与 lsblk 的耗时对比，说明时间几乎都花在机械盘的 smartctl 上。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| Host Agent 自身崩溃或被 systemd 杀 | `systemctl show anas-host-agent -p NRestarts` | 否：重启次数 0，自 10:29 起持续运行；被杀的是子进程 |
| lsblk 慢 | 在 NAS 上计时 | 否：4–7 ms |
| 每个文件请求都读一次 SMART | 阅读守卫的调用链 | 是：`authorizedSpace` → 守卫 → `ListVolumes` → `/v1/state` → smartctl |
| 并发请求在机械盘上排队超过 3 秒 | NAS 日志与 smartctl 计时 | 待实机确认 |

## 根因

硬盘信息与文件请求共用一条同步读盘路径。SMART 变化很慢，却在每次状态请求、每个文件请求里重新读取，而且绑在调用方的 3 秒超时上。机械盘忙或几个请求同时到达时，SMART 命令排队超时，smartctl 被杀，文件请求随之报告数据卷不可用。

## 修复与回归证据

- 修复：
  - 数据卷守卫的预期 UUID 改为直接取已创建数据卷的记录（`storage.Service.DataVolumeFilesystemUUID`），不再读主机状态。挂载点是 Btrfs、卷标记的 UUID 与记录一致，这两项检查不变；Host Agent 一侧的文件代理照旧自己检查卷。
  - Host Agent 的状态读取只运行 lsblk，SMART 结果来自缓存（`internal/hoststate/linux/smart.go`）。缓存超过 10 分钟时，读取在后台另起一次刷新，单块盘限时 30 秒，不随请求取消，同时只有一次刷新在跑。Host Agent 启动时先刷新一次。
  - smartctl 改为 `--json --nocheck=standby --health --attributes`：只读健康结论和温度，不再读错误日志和自检日志；盘在待机时不唤醒，沿用上次结果，超过 24 小时没有新结果时回到“未知”。待机按 smartctl 的退出码第 1 位加 “Device is in … mode” 消息判断，与 Netdata 的 smartctl 采集器相同。
- 测试：
  - `TestStateReadsDoNotWaitForSMART`：smartctl 阻塞时状态读取仍立即返回，取消请求不取消 smartctl，完成后的读取带上结果。旧实现中这个读取会一直等到 smartctl 返回。
  - `TestSMARTIsReadAtMostOnceAnInterval`、`TestSleepingDiskKeepsItsLastReadingForADay`、`TestParseSMARTTellsASleepingDiskFromAFailedOpen`。
  - `TestDataVolumeFilesystemUUIDNeedsNoHostRead`：建好数据卷后，Host Agent 读不通时仍能拿到 UUID，且没有读主机状态。
  - 本地 Debian 13 镜像中的 smartctl 7.4 接受这组参数。

## 后续工作

- 部署后在实机上对比：`journalctl -u anas-host-agent -u anas-api` 中被杀与超时的条数、`time smartctl --json --nocheck=standby --health --attributes /dev/sda`、打开大文件夹时的请求耗时，以及数据盘能否进入待机。
- SMART 缓存使 SMART 异常与 Btrfs 校验错误的提醒变得便宜，属于[备份中心评审](../research/backup-center-review-2026-10.md)中的失败通知，另行决定。

## 关联

- 规格：[基础存储与共享](../specs/basic-storage-and-sharing.md#服务与错误)
- ADR：[ADR 0004](../adr/0004-use-http-json-over-unix-socket-for-host-state.md)
- 运行手册：不涉及
- 代码：[`internal/hoststate/linux/smart.go`](../../internal/hoststate/linux/smart.go)、[`smart_test.go`](../../internal/hoststate/linux/smart_test.go)、[`internal/storage/service.go`](../../internal/storage/service.go)、[`cmd/anas-api/main.go`](../../cmd/anas-api/main.go)
