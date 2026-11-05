# 建卷测试结束后旧执行器仍写操作日志，竞态检测作业偶发失败

状态：resolved
更新时间：2026-11-04

## 症状与影响

2026-11-04 main 在 `3e3ff277`（CI 运行 41786448540）的“Go tests with the race detector”作业失败，只有 `TestCreatingTheVolumeContinuesAfterTheHostAgentStopsInAnyStep/erase` 一项：

```text
testing.go:1619: TempDir RemoveAll cleanup: unlinkat /tmp/TestCreatingTheVolumeContinuesAfterTheHostAgentStopsInAnySteper1461251632: directory not empty
```

上一次运行（`1a566f82`）代码相同却通过。这个作业是 `main` 的强制检查，偶发失败会拦住无关的 PR。产品不受影响：只有测试会让停止的 Host Agent 继续运行。

## 最小复现

1. WSL，Go 1.27，`go test -race -count=200 -run TestCreatingTheVolumeContinuesAfterTheHostAgentStopsInAnyStep ./internal/hostops/linux/`：空闲机器上 800 个子测试全部通过。
2. 占满全部核，再以 `-test.count=1000 -test.cpu=1,2,4` 运行编译好的测试（12,000 个子测试）。`TMPDIR` 在 tmpfs 上时两轮分别失败 6 次和 5 次，在 ext4 上一轮 0 次：tmpfs 的刷盘不花时间，清理来得更早。
3. 更可靠的指标是测试结束后又出现的临时目录：旧执行器在清理之后才写时，测试不报错，目录留在 `TMPDIR`。上面 tmpfs 的第二轮留下 1,300 个。只在临时副本中让假命令的“被杀”返回晚 0–50 ms，模拟忙碌的 runner 推迟这个 goroutine，800 个子测试留下 767 个。

## 观察事实

- 测试用两个 `linux.Executor` 表示同一个 Host Agent 重启前后。第一个执行器的命令卡住时启动第二个，再用 `host.abandon()` 让卡住的假命令返回 `signal: killed`。没有真实子进程：这句报错是假命令返回的文本。
- 第一个执行器的 [`runOperation`](../../internal/hostops/linux/volumeops.go) 在后台 goroutine 中运行，命令失败后记日志，再把操作日志写进 `systemRoot`（一个 `t.TempDir()`）。测试只等第二个执行器的重试结束，不等第一个。
- 报错路径是子测试的临时目录本身，不是其中的 `001`：`RemoveAll` 已删掉 `001`，之后又有东西在顶层建了目录。[`writeAtomic`](../../internal/hostops/linux/executor.go) 写操作日志前 `os.MkdirAll(filepath.Dir(path))`，把 `001/var/lib/a-nas-host` 整条重新建出来，最后的 `rmdir` 于是报 `directory not empty`。本地复现的报错路径也落在这条目录上，如 `…/001/var/lib/a-nas-host: directory not empty`。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 被杀的假命令仍在写临时目录 | 读 [`diskHost.Run`](../../internal/hostops/linux/volume_test.go)：返回 `signal: killed` 后不写文件 | 否定 |
| 第一个执行器在测试返回后写操作日志 | 数测试结束后又出现的临时目录；延迟假命令的返回，看数目是否随之增加 | 证实 |

## 根因

真实的 Host Agent 停止时进程随之结束，不再写任何东西，所以产品代码不需要等后台操作。测试中“停止的”执行器却还活着：卡住的命令放开后，它照常把失败写进操作日志。测试从不等它，这次写入就可能落在重试期间、清理删除临时目录的时候，或测试结束之后。

## 修复与回归证据

- 修复：`host.abandon()` 之后用 `host.finish(t, first, "plan:cut")` 等第一个执行器结束，它的写入发生在重试之前、清理之前。
- 测试（`-race`，`TMPDIR` 在 tmpfs 上）：

| 条件 | 修复前 | 修复后 |
|---|---|---|
| 空闲，`-count=200`（800 个子测试） | 0 失败 | 0 失败 |
| 占满全部核，`-count=1000 -cpu=1,2,4`（12,000 个子测试） | 5 失败，留下 1,300 个目录 | 0 失败，0 个目录 |
| 假命令返回晚 0–50 ms，`-count=200`（800 个子测试） | 0 失败，留下 767 个目录 | 0 失败，0 个目录 |

## 后续工作

- [测试与验证](../development/testing.md#静态检查)的竞态检测说明加了一条：测试启动的后台操作要在测试返回前结束。

## 关联

- 测试：[`volume_test.go`](../../internal/hostops/linux/volume_test.go)
- [2026-10-21：存储计划执行绑定浏览器请求，被打断的建卷无法恢复](2026-10-21-storage-plan-bound-to-browser-request.md)（操作日志与中断后重试的来由）
- 规格：不涉及
- ADR：不涉及
- 运行手册：不涉及
