# 快照清单超过 1 MiB 即失败，并留下孤儿子卷

状态：fixed（修复未部署）
更新时间：2026-10-21

## 症状与影响

- 来源：2026-10-20 代码与功能架构审查 [#159](https://github.com/zhongwater123/A-NAS/issues/159) 中的 P0 [#162](https://github.com/zhongwater123/A-NAS/issues/162)，审查只读，未在实验 NAS 上复现。
- 文件与文件夹超过约 6 千个的空间，手动快照和定时快照都会失败。
- 每次失败都在 `.a-nas-snapshots` 留下一个没有记录的 Btrfs 只读快照。默认的每日计划失败后每 15 分钟重试，一个大空间每天约留下 95 个。
- 这些孤儿快照钉住已删除的数据：清空回收站也不释放空间，直到保留容量触发并拒绝写入。快照越多，Btrfs 越慢。备份中心没有任何还原点。
- 同类遗留：
  - 复制中途重启会留下 `.a-nas-copy-*` 隐藏副本，经 SMB 可见，也无人回收。
  - 删除快照时子卷已不在，会导致记录永远删不掉，过期清理每次都报错。

## 最小复现

1. 基线 `4f05c2e`。用真实的 Host Agent 客户端与 Handler 经 Unix Socket 往返一次快照创建；测试用的后端返回 N 个对象，路径短、不带修改时间，每个约 105 字节。
2. N 取 1 000、5 000、6 000、10 000。
3. 前三次完整收到；10 000 个对象时产品端收到 0 个，报 `unexpected EOF`。真实对象带修改时间且路径更长，所以门槛更低。

## 观察事实

- Host Agent 拍完只读快照后，遍历全部对象，在 `POST /v1/snapshots` 的一个 JSON 回复中返回。产品端 `doJSON` 用 `io.LimitReader(…, 1<<20)` 解码；截断后 JSON 不完整，得到 `unexpected EOF`。
- 产品端只在 `Create` 成功返回后才注册补偿删除，所以上一条失败时没有人删除已拍的子卷。
- `walkSnapshot` 不检查 context。关闭标签页或服务停止时，请求被取消，补偿删除可能来不及运行。Product Service 停止时也不等调度器，`files.db` 先于调度器关闭。
- 没有代码列举 `.a-nas-snapshots`。
- Host Agent 把子卷缺失的 `ErrNotFound` 也映射为 503，产品端先删后端、再删记录。两步之间中断，记录就永远删不掉。
- 复制的隐藏副本被目录对账整个跳过，没有回收。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 1 MiB 回复上限截断大空间的对象清单 | 基线上按上文用 1 000–10 000 个对象往返 | 证实：10 000 个对象时 `unexpected EOF` |
| 失败的拍摄留下子卷 | 读 `createSnapshot`：补偿删除注册在 `Create` 返回之后 | 证实（代码路径） |
| 只放大上限即可 | 一次 JSON 要在 Host Agent 与产品端各留一份完整清单，任何上限都只是把门槛后移，孤儿与补偿问题也还在 | 否决 |

## 根因

- 快照的对象清单经 IPC 一次性回传，回复大小受 1 MiB 解码上限约束。
- 快照的创建与目录记录之间没有“谁负责删除”的约定，对两侧也没有任何对账。

## 修复与回归证据

- 修复：
  - Host Agent 协议，见 [`agent.go`](../../internal/hoststate/agent/agent.go)：
    - `POST /v1/snapshots` 只创建快照，已存在即成功。
    - `GET /v1/snapshots/{id}/objects` 遍历只读快照，逐行流式返回对象，以带总数的结束行收尾；中途失败写错误行。客户端按行解码，没有总大小上限，缺结束行或总数不符即失败。
    - `DELETE` 对已不存在的快照返回成功。
    - `PUT /v1/snapshots` 接收要保留的快照 ID，只在挂载着数据卷时删除其余快照子卷，并回报哪些仍在。执行器见 [`hostops/linux/snapshot.go`](../../internal/hostops/linux/snapshot.go)。
  - 产品端，见 [`files/snapshot.go`](../../internal/files/snapshot.go)：
    - 请求拍摄之前就登记这份快照。之后任何失败或取消都用不随请求取消、限时 10 秒的 context 删除它。
    - 读完全部清单后，才开短事务写目录；写入时只记文件夹的条目 ID。
    - `SweepSnapshots` 在锁内收集目录记录与正在拍摄的快照交给 `Prune`，再删除快照已不在的记录（审计 `snapshot.missing`）。正在拍摄的快照登记时也要拿这把锁，所以不会被当作孤儿。
    - 调度器启动时清扫，数据卷离线时每分钟重试，之后每小时一次，见 [`schedule.go`](../../internal/files/schedule.go)。[`main.go`](../../cmd/anas-api/main.go) 停止时先等调度器结束，再关闭 `files.db`。
  - 复制：Product Service 在副本创建前就持有它的名字。目录对账以 `ScanSkip.Opaque` 只列出 `.a-nas-copy-*` 而不进入，删除无人持有的副本，见 [`service.go`](../../internal/files/service.go)。
- 测试：
  - [Host Agent 客户端测试](../../internal/hoststate/agent/agent_test.go)：10 万个对象按序完整到达；中途失败、缺结束行与总数不符都被拒绝；删除与清扫的往返。
  - [快照清扫测试](../../internal/files/snapshot_sweep_test.go)：清单失败或请求取消后已拍的快照被删除；删除已不在的快照；清扫两个方向且离线不动；清扫不碰正在拍摄的快照；调度器启动即清扫；2 万个文件的定时快照；开发后端的清扫与修改时间。
  - [复制临时副本测试](../../internal/files/upload_test.go)：进行中的副本保留，无人持有的删除。
  - [系统测试](../../scripts/system-test.sh)在真实安装与 Btrfs 上：
    - 拍摄含 10 万个文件的空间，并在备份中心列出。
    - 删除子卷已不在的快照。
    - 重启 Product Service 后，无记录的子卷与无子卷的记录都被清扫，并写入日志。
    - 列表时删除中断复制留下的副本。

## 取舍

- 产品端仍在内存中保存一次完整清单（约 200 字节/条，10 万条约 20 MiB），换取目录写事务只覆盖插入本身。若要支持百万级条目的空间，应改为分批暂存。按每个文件每份快照一行的目录设计，那时目录本身已先成为瓶颈。
- 清扫删除一切没有目录记录的快照子卷。系统盘重装、`files.db` 丢失时，数据卷上的旧快照也会被删除。产品没有重新收养旧快照的功能，这些快照本就无法浏览或恢复，保留它们只会钉住空间。若以后要从数据卷重建快照目录，需要先改变这一规则。

## 后续工作

- 部署：Host Agent 与 Product Service 必须同版本，因为快照协议变了。部署前记下实验 NAS 上 `.a-nas-snapshots` 的条目数，部署后看 Product Service 日志中清扫删除的数量，并确认大空间出现凌晨 3 点的定时快照。
- 定时快照失败仍只记录在计划上，没有通知（备份中心规格的非目标）。

## 关联

- 规格：[备份中心](../specs/backup-center.md)、[基础存储与共享](../specs/basic-storage-and-sharing.md)
- 架构：[存储与文件架构](../architecture/storage-and-files.md#快照)
- ADR：不涉及（仍是 [ADR 0004](../adr/0004-use-http-json-over-unix-socket-for-host-state.md) 的 Unix Socket 上的 HTTP/JSON）
- 运行手册：[实验存储手册](../runbooks/provision-v1.0.1-experimental-storage.md#功能验收)
- Issue：[#162](https://github.com/zhongwater123/A-NAS/issues/162)、[#159](https://github.com/zhongwater123/A-NAS/issues/159)
