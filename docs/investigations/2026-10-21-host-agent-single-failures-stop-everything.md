# 一处故障让 Host Agent 的全部入口失效

状态：fixed（[#166](https://github.com/zhongwater123/A-NAS/issues/166)，[PR #212](https://github.com/zhongwater123/A-NAS/pull/212)），待实机验收
更新时间：2026-10-27

## 症状与影响

2026-10-20 的架构审查（[#159](https://github.com/zhongwater123/A-NAS/issues/159)，基线 `9cda8a3`）从源码中发现以下问题，列为 P1。2026-10-21 在主干 `8f5b6b0` 上复核，四项仍然存在：

- **启动对账出错，Host Agent 就退出。** 对账依次修复空间权限、把身份清单写到数据卷、刷新 Samba 配置，任何一步出错都会让进程退出。可能的原因包括：Btrfs 把数据卷转为只读，写清单时得到 `EROFS`；身份注册表无法解析；`testparm` 或 `smbcontrol` 失败。单元配置为 `Restart=on-failure`，所以它每 2 秒重启一次，直到命中启动次数限制。退出期间，以下功能同时不可用：
  - Web 文件；
  - 管理员终端；
  - `/v1/system-logs`；
  - 健康页依赖的宿主机状态。

  这几项正是排查数据卷问题要用的手段。
- **启动时先撤销到期的查看授权，再提供服务。** 撤销要逐级遍历整个个人空间的 ACL，大空间上需要数分钟，其间 Web 文件不可用。`Type=simple` 不等待启动完成，所以 `TimeoutStartSec=30min` 不起作用。
- **一次会话查询出错，共享句柄就被关闭。** 文件代理、终端、助手与相册、分享的会话查询共用一个只读的 `*sql.DB`。任何一次查询出错，它都会被关掉，例如 SQLite 忙，或调用方放弃了请求。同时在途的查询因此失败，报 `sql: database is closed`。文件代理又把任何错误都当作会话无效，于是：
  - 并发的文件操作得到“拒绝”；
  - 管理员终端被挂断；
  - 助手连接被切断。
- **Shell 与 Worker 共用 Host Agent 的 cgroup。** 管理员终端 Shell 与每个用户的文件 Worker 都在 Host Agent 的 cgroup 中运行，与 root 代理共享资源。Shell 里用 `logger` 或 `systemd-cat` 写的内容，在“设置 → 日志”中显示为 Host Agent 的日志。
- **单元缺少常见加固项。** 没有能力上限，没有 `ProtectKernel*`，也没有内存与任务上限。

## 最小复现

1. 在[系统测试](../../scripts/system-test.sh)的 Debian 13 systemd 容器中，停止相册与分享服务（它们在数据卷上开着数据库）。执行 `mount -o remount,ro /srv/a-nas/data`，再 `systemctl restart anas-host-agent`。
   - 修复前：按 [`main.go`](https://github.com/zhongwater123/A-NAS/blob/9cda8a3/cmd/anas-host-agent/main.go#L82-L85) 的代码路径，写身份清单得到 `EROFS` 后进程退出。没有在容器中运行旧版本复现。
   - 修复后：Host Agent 保持运行并报告对账失败，Web 文件、终端和日志都可用。
2. 会话句柄：8 个协程用有效会话反复查询，另一个协程用已取消的上下文反复查询。
   - 修复前：有效会话的查询失败，报 `read session directory: sql: database is closed`。在旧代码上运行 `TestOneFailedSessionLookupLeavesTheOthersWorking` 得到此结果。
   - 修复后：全部成功。
3. 终端：Shell 运行中，让会话查询失败两三次再恢复。
   - 修复前：在第一次复查时就被挂断。在旧代码上运行 `TestTerminalOutlivesAPassingSessionDirectoryError` 失败。
   - 修复后：Shell 继续运行。

## 观察事实

- 测试镜像为 systemd 257（`257.13-1~deb13u1`）。在其中用 `systemd-run` 实测：
  - `ProtectClock=yes` 后仍能读 loop 设备。`DeviceAllow=/dev/null rw` 或 `DevicePolicy=closed` 时读取被拒，说明容器里设备策略是生效的。所以在 `DevicePolicy=auto`、没有 `DeviceAllow=` 的单元中，`ProtectClock` 不会切断磁盘。它只在已有设备策略时追加 `char-rtc`。
  - `ProtectKernelTunables=yes` 使 `/proc/sys` 只读，但容器中的 `/sys` 仍可写。加上 `ReadWritePaths=/sys/devices`、`ProtectSystem=strict` 后，向 `/sys/class/block/<dev>/uevent` 写入照常成功。裸机上 `/sys` 是否变为只读尚未确认。
  - 有 `RestrictNamespaces=yes` 时，`unshare -U` 被拒（`Operation not permitted`）。
  - 设置 12 项能力的 `CapabilityBoundingSet` 后，`CapBnd` 为 `00000000012a00ff`，`setpriv` 仍能切换到 `nobody`。单元没有 `User=`，所以不触发 [#38](../investigations/2026-10-08-host-agent-loses-setuid-under-systemd.md) 的能力丢失。
- journald 按发送进程所在的 cgroup 记录 `_SYSTEMD_UNIT`，这个字段不能伪造。`journalctl -u` 按它匹配；`UNIT=` 只在 PID 1 发送时、`OBJECT_SYSTEMD_UNIT=` 只在 root 发送时才计入。因此 Shell 移入自己的 scope 后，用 `logger -t anas-host-agent` 冒用名字写入的内容也不属于 Host Agent。系统测试实测确认了这一点。
- 在系统测试容器里，scope 位于宿主机 cgroup 的子树下，例如 `…/docker-<id>.scope/system.slice/anas-files-alice-4912.scope`，与 `anas-host-agent.service` 并列。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 关闭共享句柄会让并发查询失败 | 旧代码上运行并发测试 | 成立：`sql: database is closed` |
| 只读数据卷上，对账失败但其余功能可用 | 系统测试中把数据卷改为只读后重启 Host Agent | 成立（修复后）：文件列表、终端、日志与健康页可用 |
| `ProtectClock` 会切断磁盘访问 | 容器中对比设备策略 | 不成立（systemd 257，`DevicePolicy=auto`） |

## 根因

- **启动：** 数据卷对账被当作启动前提。它的结果只影响共享与权限，却决定了整个进程能否运行。
- **会话：** 出错后关闭共享句柄，是为了让坏连接重开。但这个句柄是多个调用方共用的。调用方又没有区分“会话不存在”和“暂时读不出”。
- **cgroup 与日志：** Shell 与 Worker 由 Host Agent 直接 fork，所以继承了它的 cgroup。journald 据此把它们记为 Host Agent。

## 修复与回归证据

- **启动**（[`main.go`](../../cmd/anas-host-agent/main.go)）：
  - Host Agent 先监听全部套接字，再在后台对账数据卷（`keepDataVolume`）和撤销到期授权（`keepViewing`）。
  - 对账失败只记录日志，并在 `/v1/state` 中以 `reconcileFailed` 报告。产品服务把它评估为注意项 `volume_reconcile_failed`；数据卷只读时只报 `volume_read_only`（[系统健康](../specs/system-health.md)）。
  - 失败后每 15 分钟重做完整对账，成功后恢复为只修权限。
  - 删除了无效的 `TimeoutStartSec`。
- **会话**（[`session_identity.go`](../../internal/accounts/session_identity.go)、[`server.go`](../../internal/filebroker/rootbroker/server.go)）：
  - 查询出错不再关闭句柄，连接改为每分钟重开。
  - 文件代理把 `ErrSessionNotFound` 判为未登录，把其他错误判为暂不可用。
  - 终端与助手复查会话时，连续 3 次失败仍保持连接，第 4 次才挂断（默认间隔下约 2 分钟）。
  - 助手更改接口按 UID 查账号时，也区分这两种情况。
- **单元**（[`anas-host-agent.service`](../../deploy/systemd/system/anas-host-agent.service)）：
  - 能力上限为 12 项；
  - 启用 `ProtectKernelModules`、`ProtectKernelTunables`、`ProtectKernelLogs`、`ProtectClock`、`LockPersonality`、`RestrictNamespaces`、`SystemCallArchitectures=native`；
  - `MemoryMax=1G`，`TasksMax=512`；
  - `ReadWritePaths` 加入 `/sys/devices`，保留加盘后让 udev 重新探测的写入（[加盘调查](2026-10-20-added-disk-shown-unused-until-reboot.md)）。
- **scope**（[`internal/systemdscope`](../../internal/systemdscope/scope.go)）：
  - Worker 与 Shell 各在 `anas-files-<账号>-<PID>.scope` 或 `anas-terminal-<账号>-<PID>.scope` 中运行，经 systemd 私有套接字创建，并 `BindsTo=` Host Agent 的单元。
  - Shell 先停在 `anas-host-agent terminal-shell` 闸门，进入 scope 后才执行。
  - systemd 建不成 scope 时记录错误，进程照常运行。
- **单元测试：**
  - `TestOneFailedSessionLookupLeavesTheOthersWorking`；
  - `TestFileOperationsAreUnavailableWhileSessionsCannotBeRead`；
  - `TestTerminalOutlivesAPassingSessionDirectoryError`、`TestTerminalEndsWhenTheSessionDirectoryStaysUnreadable`、`TestTerminalIsUnavailableWhileSessionsCannotBeRead`；
  - `TestAssistantConnectionOutlivesAPassingSessionDirectoryError`；
  - `TestTerminalShellStartsOnlyOnceInItsScope`、`TestTerminalStartsWhenItsScopeCannotBeMade`、`TestWorkersArePlacedInScopesOfTheirOwn`；
  - `TestAssessReportsAFailedReconciliation`、`TestClientReadsAFailedReconciliation`；
  - 前端 `health.test.ts`。
- **系统测试**，在真实单元下验证：
  - 能力上限与内存、任务上限；
  - Worker 与 Shell 各在自己的 scope 中；
  - Shell 冒用名字写的日志不进 Host Agent 的日志，也不出现在“设置 → 日志”中；
  - Shell 不能创建命名空间；
  - 数据卷只读时重启 Host Agent，它照常服务、记录并报告对账失败；文件列表、终端、日志可用，健康页报告只读；
  - 重启 Host Agent 会结束它此前建立的 scope；
  - 恢复可写后对账成功。

## 后续工作

- **部署后在实验 NAS 复核：**
  - `/proc/<Host Agent PID>/status` 的 `CapBnd`；
  - `systemd-cgls` 中 Worker 与 Shell 的 scope；
  - 裸机上 `ProtectKernelTunables` 是否让 `/sys` 只读，以及加盘后 `udevadm trigger` 是否不再告警。
- **核对上限：** 运行一周后看 `systemctl show anas-host-agent -p MemoryPeak -p TasksCurrent`。`MemoryMax=1G` 与 `TasksMax=512` 是估计值，Worker 与 Shell 已不计入其中。

## 关联

- 规格：[系统健康](../specs/system-health.md)、[Web 桌面终端](../specs/web-terminal.md)
- 架构：[存储与文件架构](../architecture/storage-and-files.md#失败语义)
- ADR：[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)、[ADR 0020](../adr/0020-read-service-logs-through-the-host-agent.md)
- 调查：[Host Agent 在 systemd 沙箱下丢失 CAP_SETUID](2026-10-08-host-agent-loses-setuid-under-systemd.md)、[加入数据卷的硬盘在重启前仍显示为未使用](2026-10-20-added-disk-shown-unused-until-reboot.md)
- 运行手册：不涉及
