# 助手目录写满配额时 Hermes 被 SIGBUS 结束

状态：open；机制已在开发机复现，修复方案等维护者选择
更新时间：2026-11-24

## 症状与影响

2026-11-24 带真实运行环境的系统测试中（`ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME` 为 [`runtime.json`](../../ai/assistant/runtime.json) 固定的运行环境，镜像 `anas-systemd:trixie`，WSL 内核 6.18），[`system-test.sh`](../../ai/assistant-spike/system-test.sh) 的配额检查给每个助手目录设 64 MiB，alice 往 `files/` 写 100 MB 直到被配额拦下，然后 `sync` 并删除文件。这时 alice 的实例在运行。内核记下 `python3.14: hermes-change-w: potentially unexpected fatal signal 7`（UID 20101），`anas-assistant@alice.service` 以 135 退出，之后依赖实例的 30 项检查失败。CI 不带运行环境，没有实例在运行，所以不出现。

影响不限于测试。[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 给整个 `assistant/<账号>` 子卷设 10 GiB 的 simple quota，Host Agent 还没有设（[`assistant.go`](../../internal/hostops/linux/assistant.go)）。设上以后，助手或成员把工作区 `files/` 写满，这是正常使用中会发生的事，同一子卷里的 Hermes 就会在下一次写 WAL 索引时被 SIGBUS 结束。

## 最小复现

开发服务器上的一次性特权容器（`anas-systemd:trixie`，内核 6.18.58，btrfs-progs 6.14），用固定运行环境的 Python 3.14.8 与 SQLite 3.53.1。每种情形新建 1 GiB 的环回 Btrfs，`btrfs quota enable --simple`，建子卷 `acct` 并在其中建 `hermes/` 与 `files/`（与 A-NAS 相同），给 `acct` 设 64 MiB。UID 20101 的一个进程使用 `hermes/` 中的文件，同一 UID 用 `dd` 往 `files/` 写 100 MB 直到 “Disk quota exceeded”，然后 `sync`。

1. 普通 mmap：按 SQLite 扩展 `-shm` 的方式（每页末尾写一个字节）建文件，`MAP_SHARED` 映射，每 0.2 秒写第 0 页。`sync` 后的第一次写入得到 SIGBUS。
2. 按 Hermes 的方式用 SQLite：一个常驻的写连接（WAL）每 0.3 秒提交一次，名为 `hermes-change-watcher` 的线程每 0.5 秒新开 `mode=ro` 连接读一次。这个线程在 `sync` 后的第一次读取时被 SIGBUS 结束。内核把线程名截成 15 个字符，就是系统测试日志里的 `hermes-change-w`。
3. 固定运行环境的 `hermes serve`：按上游自己的评估脚本启动（`evals/desktop_bug_campaign/history_live.py`：`--host 127.0.0.1`，令牌在 `HERMES_DASHBOARD_SESSION_TOKEN`），接一个 WebSocket 客户端，`tui_gateway/ws.py` 由此启动变化监视线程。另一个进程每秒经 Hermes 的 `SessionDB` 往会话追加一条消息，与定时任务的一轮一样。从一次提交之后到写满并 `sync` 完，用 SIGSTOP 让 serve 停住，再 SIGCONT：3 次都在 0.2 秒内以 SIGBUS 退出。出错的线程是 `hermes-change-w`，位置是 `tui_gateway/change_watcher.py` 第 233 行，即 `_session_db_content_sig` 在新开的只读连接上执行的第一条语句。不定序时 serve 在一次运行中存活：出不出事取决于时机。

| 情形 | 结果 |
|---|---|
| 普通 mmap，写满后 `sync` | SIGBUS |
| SQLite WAL，写满后 `sync` | 监视线程 SIGBUS |
| SQLite WAL，写满后不 `sync`，运行 80 秒 | 每次提交都报 “disk I/O error”，关闭时的检查点 SIGBUS |
| 同上，`hermes/` 设 NOCOW（`chattr +C`） | SIGBUS |
| `journal_mode=DELETE` | 存活；写满期间提交失败（“unable to open database file”，回滚日志建不出来） |
| `files/` 是嵌套子卷并单独设上限 | 存活，39/39 次提交成功 |
| `hermes/` 是嵌套子卷并单独设 16 MiB，`acct` 仍设 64 MiB | 存活，39/39 次提交成功 |
| 两个子卷同属一个 1 级 qgroup（总量 64 MiB），`files/` 另设 56 MiB | `files/` 写满时存活；再把 Hermes 余下的 8 MiB 写满时 SIGBUS |
| 不设配额，整个卷写满（`ENOSPC`） | 写线程与读线程都 SIGBUS |
| 真实 serve，写满后 `sync`，SIGSTOP 定序 | 3/3 次 SIGBUS，线程 `hermes-change-w` |
| 真实 serve，不定序 | 1 次存活 |
| 真实 serve，`files/` 或 `hermes/` 是单独计量的嵌套子卷 | 存活，写满后 `sessions.changed` 照常推送 |
| 真实 serve，`database.journal_mode: delete` | 存活；写满期间另一进程的提交失败 |

## 观察事实

- Hermes 的 `state.db` 用 WAL：`database.journal_mode` 默认 `wal`，运行环境的 SQLite 3.53.1 不受 WAL-reset 缺陷影响，Hermes 因此启用 WAL（`hermes_state_wal.py`）。WAL 的索引是 `state.db-shm`，用这个库的每个进程都把它 `MAP_SHARED` 映射进来：读者把读标记写在里面，写者写头部，检查点写回填进度。
- SQLite 扩展 `-shm` 时先在每个新页的末尾写一个字节，注释说这样能减少以后访问映射时出 SIGBUS 的机会（[`src/os_unix.c`](https://github.com/sqlite/sqlite/blob/version-3.53.1/src/os_unix.c) 的 `unixShmMap`）。这在原地分配的文件系统上有效；Btrfs 写时复制，以后每次改写都要新的空间。
- Linux 6.18 的 `btrfs_page_mkwrite`（[`fs/btrfs/file.c`](https://github.com/torvalds/linux/blob/v6.18/fs/btrfs/file.c)）在页写回后的第一次写入时为新副本预留数据与元数据空间，预留失败就返回 `vmf_error(ret)`，对 `-EDQUOT` 与 `-ENOSPC` 都是 SIGBUS。写回让所有映射里的这一页变成只读，所以每个进程在每次写回后的第一次写入都走这条路。
- 写满时提交本身只是失败：写 WAL 的 `write()` 返回 `EDQUOT`，SQLite 报 “disk I/O error”（只有 `ENOSPC` 对应 `SQLITE_FULL`）。崩溃来自对 `-shm` 的写入：没读到最近一次提交的读者要写读标记，关闭连接时的检查点要写回填进度。
- simple quota 在事务提交时才归还删除文件的空间：`rm` 后 qgroup 仍是 64 MiB，`sync` 后才降下来，不 `sync` 则要等下一次提交（默认最长 30 秒）。危险窗口从写满持续到删除之后的那次提交。
- 变化监视线程只在 `state.db` 或 `-wal` 的修改时间变化后读库，每 0.5 秒检查一次，每次新开一个 `mode=ro` 连接（`change_watcher.py` 的 `_session_db_content_sig`）。它最常是最近一次提交后的第一个读者，所以日志里是它；同一进程或其他进程中用这个库的任何线程都可能这样结束。
- 上游 Hermes 没有处理这种情况。上游 issue 中的 SIGBUS 都与 virtiofs、WSL2 或被删除的 WAL 文件有关；磁盘写满的 [#158790](https://github.com/NousResearch/hermes-agent/issues/158790) 发生在 macOS 上，提交只是失败；可配置 `state.db` 目录的 [#154713](https://github.com/NousResearch/hermes-agent/issues/154713) 尚未实现。Python 进程无法从 SIGBUS 恢复。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 配额满时 Hermes 写 mmap 的 `-shm`，Btrfs 无法为写时复制预留空间而发 SIGBUS | 最小复现 1–3 | 证实 |
| 只是测试中的 `sync` 造成 | 不 `sync` | 否定：关闭时的检查点同样触发，周期写回也会让页重新变成只读 |
| NOCOW 可以避开 | `hermes/` 设 `chattr +C` 后重复 2 | 否定 |
| 与配额无关的整卷写满也会触发 | 不设配额，写满整个卷 | 证实 |

## 根因

Hermes 用 SQLite 的 WAL，WAL 的索引文件 `-shm` 经共享映射写入。Btrfs 上对映射页的写入要为写时复制的新副本预留空间，所在 qgroup 已满时预留失败，内核以 SIGBUS 结束写入的进程。A-NAS 把 Hermes 的状态（`hermes/`）与助手的工作区（`files/`）放在同一个子卷、同一个上限之下，工作区在正常使用中写满，Hermes 就没有写 `-shm` 的空间了。

## 修复选项

1. **把 `hermes/` 做成嵌套子卷，单独设上限（推荐）。** `assistant/<账号>` 保留原定的上限，计的是工作区；`hermes/` 改为其中的子卷，上限另设。实验中工作区写满后，SQLite 情形的提交全部成功，真实 serve 持续运行。代价：
   - Host Agent 建立、对齐与删除第二个子卷。`removeTree` 遇到含子卷的子卷时退回逐个文件删除；先删 `hermes/` 更快。`assistant-layout.sh` 与系统测试随之改。
   - 实验 NAS 上已有的 `hermes/` 是普通目录，要停下实例、建子卷、在同一卷上 `cp --reflink` 复制、替换。`hermes/` 只有 Hermes 的状态，比工作区小，所以嵌套它而不是 `files/`；两者在实验中效果相同。
   - Hermes 自己的部分写满时仍会 SIGBUS：它的缓存、日志与 `state.db`，或者助手的终端工具写进 `HERMES_HOME`（这个目录对成员可写，ADR 0024 决定 3）。上限按实际占用定：实验中一个新的家目录约 6 MiB；实验 NAS 上的占用还没有量，只读账号进不了助手目录，要用 `sudo du -sh /srv/a-nas/data/assistant/*/hermes`。
   - ADR 0024 中“每个账号 10 GiB”要写明计的是哪部分。若总量仍要 10 GiB，可以把两个子卷放进一个 1 级 qgroup 设总量，再给工作区设“总量减 Hermes 余量”。实验中工作区写满时 Hermes 仍有余量，Hermes 自己把余量写满时 SIGBUS。代价是每个账号多一个 1 级 qgroup 的分配、指派与删除，并要在实验 NAS 的 6.12 内核上确认（[#116](https://github.com/zhongwater123/A-NAS/issues/116)）。
2. **托管配置设 `database.journal_mode: delete`。** 这是上游的开关，实验中真实 serve 存活。代价：写满期间提交照样失败，对话记不下来；失去 WAL 的并发读写，而 serve、定时任务、闲时整理与后台复盘共用 `state.db`，Hermes 的只读连接池也只在 WAL 下启用；上游说 DELETE 是运维上的权宜，不是新的默认值；已有的库要在所有持有者停下时离线转换（`hermes sessions set-journal-mode`）；只覆盖经 `apply_wal_with_fallback` 打开的库。
3. **把 Hermes 的状态放到系统盘。** ext4 原地分配，不会这样 SIGBUS。但与 ADR 0024 的“数据卷离线时助手不可用，不回退到系统盘”相反，成员数据离开数据卷的镜像。不推荐。
4. **只调整系统测试**，例如写满前停掉实例，或写别人的目录：掩盖问题，不够。

不可行：NOCOW（实验中仍 SIGBUS）；在 Python 中接住 SIGBUS；把 `-shm` 绑定挂载到 tmpfs（SQLite 只有编译期的 `SQLITE_SHM_DIRECTORY`，上游 #154713 记录了单文件绑定挂载会被 WAL 的生命周期破坏）。

修复后，系统测试改为在 alice 的实例运行时把她的工作区写满，检查实例仍在运行并能回答，再检查 `hermes/` 是单独计量的子卷。

## 修复与回归证据

- 修复：未定，等维护者在上面的选项中选择。
- 测试：见上一节最后一段；复现 3 中“真实 serve，SIGSTOP 定序”是修复前必然失败的情形。

## 后续工作

- 整个数据卷写满时，卷上所有用 WAL 的 SQLite 进程都可能这样结束：各成员的 Hermes，以及经 [`internal/sqlitedb`](../../internal/sqlitedb/sqlitedb.go) 打开数据库的服务，它对每个库都用 WAL。数据卷的保留容量只在文件服务中执行，其他写入绕过它（[#181](https://github.com/zhongwater123/A-NAS/issues/181)）。

## 关联

- 规格：[AI 助手](../specs/ai-assistant.md)（助手目录的容量上限）
- ADR：[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
- 运行手册：不涉及
- [Hermes 运行环境研究的“助手目录与配额”](../research/hermes-agent-runtime.md#助手目录与配额)、[`assistant-layout.sh`](../../ai/assistant-spike/assistant-layout.sh)、[`assistant.go`](../../internal/hostops/linux/assistant.go)
