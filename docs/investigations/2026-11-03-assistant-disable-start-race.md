# 停用助手被保活的成员时返回 503

状态：resolved（系统测试在真实 systemd 中确定性复现与回归：旧 Host Agent 失败，修复后通过；竞态只取决于 systemd 的作业语义与命令顺序，实机验收要停用一个不能再启用的账号，没有可补充的证据）
更新时间：2026-11-03

## 症状与影响

2026-11-02 在 v1.0.1-rc.6（`771e5e30386a`）上运行系统测试，带真实 Hermes 运行时、真实 AI 与真实 SearXNG，两次都只有一项失败（446 项 PASS）：“disabling a task owner succeeds through the product API”。`DELETE /api/v1/users/<bob>` 返回 503，而不是 204：

```
{"error":{"code":"smb_disable_pending","message":"the account is disabled, but its host services could not all be disabled yet: host agent state is unavailable: status 503"}}
```

后面的检查全部通过：Host Agent 拒绝 bob 的 UID，正在运行的定时任务已停止，六个单元保持 mask、不能启动。停用实际生效，但 API 报告失败，管理员看到的是停用失败，要再停用一次才得到成功。同一检查 2026-10-28 带 Hermes 通过过一次（`4ec62e17c3b1`），CI 不带 Hermes，一直通过。这段代码来自 [#178](https://github.com/zhongwater123/A-NAS/issues/178) 的修复（[调查](2026-10-27-assistant-cron-waker-member-isolation.md)），实验 NAS 上的版本有同样的竞态：管理员停用一位登录着的成员时，也可能得到这个 503。

## 最小复现

1. 一台 systemd 主机（Debian 13 容器，systemd 257）上有一个实例单元和触发它的 socket（`RemoveOnStop=yes`）；实例要停两秒（`ExecStop=/bin/sleep 2`），代表收到 SIGTERM 后先写回记录的 Hermes。
2. 一个循环每 0.1 秒启动一次实例：连接 socket，或 `systemctl start --no-block`。同时运行 `systemctl disable --now <实例> <API 服务> <socket>`。
3. 实际：输出 `Job for t@a.service canceled.`，退出码 1，实例接着运行。经 socket 启动时，输出与 NAS 日志一样是两行：`Job for t@e.service canceled.`、`Job for t-api@e.service canceled.`。期望：退出码 0，实例停止。

同一顺序的单元测试与系统测试：

```bash
go test ./internal/hostops/linux -run 'TestDisablingAnAssistantThatIsBeingKeptRunning' -count=1
bash scripts/system-test.sh   # disable-system-test.sh 中停用 bob 的几项
```

## 观察事实

- rc.6 运行中 Host Agent 的日志：`disable credential failed … disable assistant units: exit status 1: Removed '…/timers.target.wants/anas-assistant-curator@bob.timer'. Removed '…/sockets.target.wants/anas-assistant-api@bob.socket'. Disabling 'anas-assistant-api@bob.service', but its triggering units are still active … Job for anas-assistant@bob.service canceled. Job for anas-assistant-api@bob.service canceled.`
- 停用前，产品服务的日志有 `assistant could not be kept running … "account":"bob" … "retry":4000000000`。bob 仍然登录着（`/tmp/bob.jar`），产品服务的常驻保活（[`keep.go`](../../internal/assistantapi/keep.go)）不断经文件代理连接 bob 的 socket，socket 再启动 API 服务和它 `Requires=` 的实例。
- [`disableAssistant`](../../internal/hostops/linux/assistant.go) 原来先 `systemctl disable --now`（删除链接，再停止），后 `systemctl mask --now`。[`accounts.DisableUser`](../../internal/accounts/service.go) 把 Host Agent 的任何错误都映射为 `ErrSMBDisablePending`（503）；对已停用的账号再次停用时，只重试 Host Agent 这一步。
- 文件代理的会话闸门（[`rootbroker/assistant.go`](../../internal/filebroker/rootbroker/assistant.go)）在一次连接开始时查一次会话，然后连接成员的 socket 最多两次（登录、网关）；冷启动时登录最多等 90 秒。
- 在 Debian 13 容器（systemd 257.13）中探查作业语义：

  | 情形 | 结果 |
  |---|---|
  | `disable --now`，同时不断 `systemctl start --no-block` 实例 | `Job for t@a.service canceled.`，退出码 1，实例仍在运行 |
  | `disable --now`，同时不断连接 socket | 与 NAS 日志相同的两行 canceled，退出码 1 |
  | `disable` 后 `mask --now`，同样不断连接 socket，三轮 | 两条命令都退出 0；三个单元 inactive、masked |
  | 对已 mask 的单元再 `disable` 和 `mask --now` | 都退出 0；`disable` 打印 `Unit … is masked, ignoring.` |
  | 先 `mask --now` 再 `disable` | 都退出 0，但 `sockets.target.wants/` 中的链接还在 |
  | 实例在 `ExecStartPost` 中启动到一半时 `disable`、`mask --now` | 都退出 0；实例 failed，其余 inactive |

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 停用排队的停止作业被并发的启动请求替换，`systemctl` 因作业被取消而失败 | 上表前两行，停止期间不断启动 | 证实：输出与 NAS 日志相同 |
| 先 mask 再停止，systemd 就拒绝这些启动，停止作业完成 | 上表第三行 | 证实 |
| 先 mask、后 disable 同样可行 | 上表第五行 | 否定：`systemctl disable` 不读已 mask 单元的 `[Install]`，开机与定时器链接留下 |
| 由产品服务在停用前停止该成员的保活就够了 | 读保活与文件代理的代码 | 否定：见下 |
| CI 一直通过，是因为没有 Hermes，实例从未真正运行 | 读系统测试；没有运行时时 `assistant-start.sh` 立即失败 | 与观察一致：只有实例真在运行，停止才持续数秒，留出窗口 |

## 根因

systemd 中，一个单元的停止作业还在排队或执行时，同一单元新排入的启动请求（默认作业模式 `replace`）替换停止作业，停止作业以 `canceled` 结束，等着它的 `systemctl` 退出 1，单元接着运行。`disableAssistant` 先停止、后 mask，停止的整段时间里实例都可以被启动：bob 登录着，经文件代理到 socket 的连接触发 API 服务，API 服务 `Requires=` 实例，于是两个单元的停止作业都被取消。只有带 Hermes 时实例真的在运行，收到 SIGTERM 后要先写回记录，停止持续数秒，窗口才够大；所以 CI 通过，带 Hermes 时有时失败。日志不足以确定撞上停止的是保活的哪一次连接，还是文件代理已在途的连接；两者都经过同一个 socket，结论相同。

紧接着的 `mask --now` 已能拒绝启动，所以最终状态正确，只是 API 报告了失败。

## 修复与回归证据

- 修复：[`disableAssistant`](../../internal/hostops/linux/assistant.go) 先 `systemctl disable`（不带 `--now`，只删除链接，不排作业），再 `systemctl mask --now`（先 mask 并重新加载，再停止）。mask 之后的启动请求被 systemd 拒绝（`Unit … is masked`），无法替换停止作业。两条命令的错误照样返回；Host Agent 仍以 503 报告真正的失败，`ErrSMBDisablePending` 不变。再次停用已停用的账号、身份同步补做停用，都只遇到已 mask 的单元，退出 0。
- 不在产品服务中取消保活：
  - 启动来源不只保活。文件代理已通过会话检查的连接、成员的窗口、定时任务唤醒器、复盘定时器与 root 的操作都能启动这些单元，只有 Host Agent 在 systemd 一侧挡住全部来源。
  - 取消保活也挡不住在途的连接：文件代理在一次连接开始时查会话，之后才连接 socket，冷启动登录最多等 90 秒。产品服务看不到这些连接，等不到它们结束。
  - 保活自己会停：停用删除了会话，文件代理拒绝下一次连接，`ErrSessionRejected` 让保活放下这个会话；socket 停止后被删除，连接得到 `ErrNoAssistant`，保活结束。
- 不用 `--runtime`：账号停用是持久的，目前也没有重新启用账号的产品操作。`/run` 中的 mask 重启后消失，启动到第一次身份同步之间就失去这层屏蔽；而助手的更新脚本靠 mask 判断“账号已停用、不要重新启用”。竞态取决于先后顺序，与 mask 放在哪里无关。
- 单元测试：[`assistant_test.go`](../../internal/hostops/linux/assistant_test.go) 的 `keptSystemd` 按 systemd 的规则模拟保活：每次停止都遇到一次经 socket 的启动，未 mask 的实例与 API 服务的停止作业被取消；已 mask 的单元拒绝启动；`disable` 不删除已 mask 单元的链接。旧实现下 `TestDisablingAnAssistantThatIsBeingKeptRunning` 失败，错误与 NAS 相同（`disable assistant units: exit status 1: Job for anas-assistant@alice.service canceled. Job for anas-assistant-api@alice.service canceled.`），修复后通过；同一测试还覆盖再次停用与身份同步。`TestDisablingReportsAUnitThatDidNotStop` 确认停不下来的单元仍然报告为错误。
- 系统测试：[`disable-system-test.sh`](../../ai/assistant-spike/disable-system-test.sh) 让 bob 的实例停止耗时两秒，在经产品 API 停用期间每 0.1 秒经 socket 连接一次、直接启动一次实例，然后检查返回 204、实例已停止、再次停用也返回 204。
- 2026-11-03，WSL 的 ext4 副本，用当前构建脚本生成的 Debian 13 镜像：
  - 修复后带 Hermes 运行时（`ai/assistant-spike` 构建的 `runtime.tar.gz`，不带真实 AI 与 SearXNG）：461 项 PASS，0 项 FAIL。
  - 同一副本只换回原来的 `assistant.go` 重新构建 Host Agent，不带运行时，即 CI 的配置：425 项 PASS，1 项 FAIL，正是“disabling a task owner succeeds through the product API”。紧接着的“实例已停止”“再次停用返回 204”与六个单元的屏蔽检查都通过，与 rc.6 的症状一致。
  - `make check-steps` 全部通过。

## 后续工作

- 无。

## 关联

- 规格：[AI 助手](../specs/ai-assistant.md)
- ADR：[0024 每位成员一个沙箱中的 Hermes](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
- 运行手册：[在实验 NAS 上试用 AI 助手](../runbooks/try-ai-assistant-on-experimental-nas.md)
- 调查：[助手定时任务的成员文件与停用隔离](2026-10-27-assistant-cron-waker-member-isolation.md)
- 代码：[`assistant.go`](../../internal/hostops/linux/assistant.go)、[`keep.go`](../../internal/assistantapi/keep.go)、[`service.go`](../../internal/accounts/service.go)
