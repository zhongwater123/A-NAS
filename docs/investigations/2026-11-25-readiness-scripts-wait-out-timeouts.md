# 就绪脚本让失败的启动与启动中的停止各等满超时

状态：resolved（只取决于 systemd 257 的进程与信号语义，在 Debian 13 测试镜像中用最小单元与系统测试确定性复现和回归；实机没有可补充的证据）
更新时间：2026-11-26

## 症状与影响

为缩短门禁排查系统测试中的长等待时发现（`origin/main` `8696af3e`，共享开发服务器，替身运行环境）。其中三处来自同一类问题：`ExecStartPost` 中等端口的就绪脚本。

- 向量索引：存储损坏让 Qdrant 加载时 panic，主进程约 1 秒后退出，单元却在 60 秒后才判启动失败。系统测试的两次失败启动各等 61 秒；产品中所属服务的调用在这 60 秒里都等着。
- VNAS：成员的实例还在等 9119 端口时被停下（停用成员、删除模型密钥、重启），停止要等满 `TimeoutStopSec`（默认 90 秒）后再 SIGKILL。系统测试中重启 bob 的实例等了 100 秒，删除密钥等了 46 秒。
- 系统测试自己的替身有同样的问题：停用 bob 的检查把实例与定时任务换成裸的 `sleep infinity`，停用等了 91 秒，而测试本意是两秒。

## 最小复现

在 `anas-systemd:trixie`（systemd 257.13）容器中，`TimeoutStopSec=8`：

| 单元 | 操作 | 结果 |
|---|---|---|
| `DynamicUser=yes`、`ProtectProc=invisible`；主进程 1 秒后退出 1；`ExecStartPost` 每 0.1 秒查 `kill -0 "$MAINPID"` | 启动 | 脚本看得到 `$MAINPID`，第 11 次检查发现主进程不在 |
| `PrivatePIDs=yes`；`ExecStartPost` 是不带 trap 的 bash | 启动中停止 | 8.05 秒，`State 'stop-sigterm' timed out. Killing.`；脚本 `kill -0 "$MAINPID"` 失败（看不到主进程） |
| 同上，脚本 `trap 'exit 143' TERM` | 启动中停止 | 0.03 秒 |
| `PrivatePIDs=yes`；`ExecStart=/usr/bin/tini -- /bin/sleep infinity`、`ExecStop=/bin/sleep 2` | 停止 | 2.06 秒 |

## 观察事实

- 向量索引（基线运行的日志）：`Main process exited, code=exited, status=101` 在 15:21:37，`wait-ready.sh` 的 `Qdrant did not accept connections on port 6334 within 60 seconds` 在 15:22:39，第二次启动相同。
- bob 的实例：`State 'stop-sigterm' timed out. Killing process 18439 (bash)`，被杀的是 `wait-port.sh`（替身运行环境没有 `hermes_cli`，`serve` 立即退出，脚本一直等端口）。
- alice 的实例（删除密钥时）：`bash[22286]: Terminated` 只是脚本的 `sleep` 子进程被 SIGTERM 结束，脚本本身继续，直到 `port 9119 never opened`。
- 停用 bob：`anas-assistant@bob` 与 `anas-assistant-cron@bob` 都是 `stop-sigterm timed out`，被杀的是 `sleep`。
- `wait-ready.sh` 第一版在主进程不在时立即失败，系统测试的损坏索引检查随即失败：同一秒内两次失败的启动之后是约二十组 `Start request repeated too quickly` 与代理的 `failed with result 'dependency'`，最后 `anas-vectors-proxy@a-nas-photos.socket: Trigger limit hit, refusing further activation`，套接字进入 failed，套接字文件消失（客户端 `FileNotFoundError`）。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 主进程在 `start-post` 中退出后，systemd 仍等控制进程结束 | 上表第一行与向量索引的日志 | 证实 |
| `PrivatePIDs=` 下 `ExecStartPost` 与主进程在同一个 PID 命名空间 | 上表第二行，脚本看 `$MAINPID` | 否定：每个进程各是自己命名空间的 1 号进程 |
| 命名空间的 1 号进程收不到它没处理的 SIGTERM | 上表第二、三行 | 证实：加 trap 后立即结束 |
| 向量索引的启动失败可以立即报告 | 系统测试的损坏索引检查 | 否定：拉起实例的连接留在套接字队列里，每次失败都立即再次触发，撞上实例的启动频率限制（10 秒 5 次）后代理以依赖失败结束，再撞上套接字的触发上限（2 秒 20 次），套接字永久失败。原来的 60 秒等待碰巧把重试压在这两个上限之下 |

## 根因

1. 主进程在 `start-post` 状态退出时，systemd 继续等 `ExecStartPost` 的控制进程。只等端口的脚本因此等满自己的上限，单元才判失败。
2. `PrivatePIDs=` 让单元执行的每个进程成为新 PID 命名空间的 1 号进程（systemd.exec(5)）。内核只把命名空间 1 号进程设置了处理函数的信号从上层命名空间投递给它（pid_namespaces(7)），bash 不设 trap、`sleep` 都不处理 SIGTERM，所以停止只能等到 `TimeoutStopSec` 后的 SIGKILL。VNAS 的主进程用 `tini` 正是为此，`ExecStartPost` 和测试替身没有。

## 修复与回归证据

- 修复：
  - [`deploy/qdrant/wait-ready.sh`](../../deploy/qdrant/wait-ready.sh) 在 `$MAINPID` 不在后 5 秒失败：排队的连接因此每隔约 6 秒才再拉起一次实例，第三次启动清空存储后回答它；
  - [`anas-vectors@.service`](../../deploy/systemd/system/anas-vectors@.service) 与代理一样设 `StartLimitIntervalSec=0`：实例不再因启动频率限制拒绝启动，代理也就不会立即以依赖失败结束；
  - [`anas-assistant@.service`](../../deploy/systemd/system/anas-assistant@.service) 与由它生成的单元（[`assistant-units.sh`](../../scripts/lib/assistant-units.sh)）的每条命令都用 `tini` 运行，生成时检查；
  - [`ai/assistant-spike/disable-system-test.sh`](../../ai/assistant-spike/disable-system-test.sh) 的替身用 `tini` 运行。
- 测试：上面的最小单元；[系统测试](../development/testing.md#系统测试)前后在共享开发服务器上的同一段日志，见下表（修复后 799 项全部通过）。

| 等待 | 修复前 | 修复后 |
|---|---|---|
| 损坏的向量索引，两次失败启动 | 61 + 61 秒 | 两次失败加上清空后的启动共 11 秒，套接字一直在监听 |
| bob 的实例带慢停止重启 | 100 秒 | 不到 1 秒 |
| 停用 bob | 91 秒 | 1 秒 |
| 删除模型密钥 | 46 秒 | 不到 1 秒 |

## 后续工作

- `PrivatePIDs=` 下 `wait-port.sh` 看不到主进程，`hermes serve` 一启动就退出时，单元仍显示“启动中”，到 `TimeoutStartSec`（120 秒）才失败。要早于此判失败，需要主进程自己报告就绪（`Type=notify`），没有做；做时 `anas-assistant-api@.service` 也要设 `StartLimitIntervalSec=0`，理由同向量索引。
- [`deploy/AGENTS.md`](../../deploy/AGENTS.md) 记下这两条，供以后的单元与测试替身遵守。

## 关联

- 规格：不涉及
- ADR：[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)、[0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)
- 架构：[向量索引](../architecture/vector-index.md)
- 调查：[停用助手被保活的成员时返回 503](2026-11-03-assistant-disable-start-race.md)（停用检查的慢停止替身来自这里）
- 运行手册：不涉及
