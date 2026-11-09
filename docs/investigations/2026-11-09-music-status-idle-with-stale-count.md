# 音乐扫描刚结束时，状态报告“空闲”却带着扫描前的歌曲数与版本号

状态：resolved
更新时间：2026-11-09

## 症状与影响

2026-11-06 在 WSL 的 ext4 副本上运行 `make check-steps` 时（当时机器负载较高），`internal/music` 的 `TestRescanFollowsRenamesAndRemovalsAndKeepsUserData` 失败一次：

```text
music_test.go:379: after rename: 2 tracks, {ID:file:PTF4RAU5BNYJYPTL6OJBJ5N3II SpaceID:space:shared Folder:Albums Name:renamed.mp3 … Title:Song …}
```

同一份代码单独 `-count=20` 全部通过。门禁偶发失败会拦住无关的 PR。

产品同样受影响。[音乐窗口](../../web/src/music/MusicPanel.tsx)看到扫描状态为空闲就停止轮询，之后只在版本号变化时重新加载歌曲。最后一次轮询如果落在这个竞态里，拿到的是扫描前的版本号：窗口会一直显示扫描前的歌曲，包括已删除的文件和改名前的位置，直到下次扫描或重新打开窗口。

## 最小复现

1. WSL，Go 1.27，ext4 上的副本：`go test -run '^TestRescanFollowsRenamesAndRemovalsAndKeepsUserData$' -count=200 ./internal/music` 全部通过。
2. 不改代码、只占满全部核（`while :; do :; done`，`-test.cpu=1,4`），ext4 与 tmpfs 各跑 15 分钟：都没有失败。原来的失败是一次完整门禁中的一次，单靠负载复现太慢。
3. 只在临时副本中，在 [`Status`](../../internal/music/scan.go) 的两次读取之间睡 0–40 ms，模拟轮询的 goroutine 被调度器挂起，再运行同一测试：旧的读取顺序 100 次失败 4 次，失败信息与门禁中的相同（`after rename: 2 tracks, {… Folder:Albums Name:renamed.mp3 …}`）；新的读取顺序加同样的延迟，100 次全部通过。

## 观察事实

- 门禁日志里，紧挨着失败信息之前的一行是第二次扫描自己的总结：`music library scanned … tracks=1 read=0 removed=1 measured=0`。扫描找到 1 首，删掉 `gone.mp3` 的 1 首，没有重读任何文件，说明它按文件 ID 认出了改名后的歌曲。
- 失败信息中的歌曲已在 `Folder:Albums Name:renamed.mp3`。错的只有 `status.Tracks`，曲目本身正确。
- 测试的 `f.scan` 轮询 `Status`，直到扫描状态为 `idle`，然后返回这一次的 `Status`。
- 修复前的 `Status` 先数歌曲（`countIn`）、读版本号，最后才取扫描任务的状态。[`runScan`](../../internal/music/scan.go) 在扫描写完全部结果之后，才在任务锁内把状态置为空闲。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 文件服务对账没有认出改名（inode 复用、修改时间精度、对账被丢弃） | 读门禁日志中这次扫描的总结行，以及失败信息中歌曲的路径 | 否定：扫描读 0 首、删 1 首，歌曲已在新路径 |
| `Status` 在扫描删除之前数了歌曲，在扫描结束之后才读状态 | 在两次读取之间加 0–40 ms 延迟，比较两种读取顺序 | 证实 |

## 根因

`Status` 的两次读取之间没有任何同步。一次轮询先数到删除前的 2 首，随后它的 goroutine 被挂起；扫描在这期间删掉 `gone.mp3`、更新改名的歌曲并结束；轮询恢复后读到“空闲”。于是返回“空闲”，却带着扫描前的歌曲数和版本号。第二次扫描只需几十毫秒，所以只有负载高、轮询被挂起得足够久时才会出现。

## 修复与回归证据

- 修复：`Status` 先读扫描状态，再数歌曲、读版本号。扫描写完才置为空闲，所以读到空闲之后数到的歌曲与版本号一定包含这次扫描；读到“扫描中”时数字可能已经更新，窗口会继续轮询。
- 测试：新增 `TestStatusReadsTheScanBeforeTheLibrary`。旧代码在两次读取之间没有可以停住的地方，所以测试让扫描在 `Status` 读歌曲库（查成员的空间）时结束：先读状态的 `Status` 仍报告“扫描中”，下一次报告空闲、1 首、版本号增加。

| 条件 | 修复前 | 修复后 |
|---|---|---|
| `TestStatusReadsTheScanBeforeTheLibrary`，`-race -count=3` | 3 次全部失败 | 3 次全部通过 |
| 两次读取之间睡 0–40 ms，`TestRescanFollowsRenamesAndRemovalsAndKeepsUserData -count=100` | 4 次失败 | 0 次失败 |

## 后续工作

- [影视中心的资料库列表](../../internal/media/library.go)也是先读计数、后读扫描标志。它的窗口在空闲时仍按较慢的间隔轮询并比较计数，过时的数字会在下一次轮询时更正，暂不处理。

## 关联

- 代码：[`scan.go`](../../internal/music/scan.go)、[音乐窗口](../../web/src/music/MusicPanel.tsx)
- 测试：[`music_test.go`](../../internal/music/music_test.go)
- 规格：[音乐](../specs/music.md#边界与失败)
- ADR：不涉及
- 运行手册：不涉及
