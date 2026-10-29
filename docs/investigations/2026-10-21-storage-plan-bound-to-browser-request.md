# 存储计划执行绑定浏览器请求

状态：fixed，待实机验收
更新时间：2026-10-27

## 症状与影响

[#165](https://github.com/zhongwater123/A-NAS/issues/165) 来自 2026-10-20 的代码与架构审查（[#159](https://github.com/zhongwater123/A-NAS/issues/159)，基线 `9cda8a3`）。2026-10-21 在 `4f05c2e` 上按源码复核，问题仍然存在；它没有在实验 NAS 上发生过。

- 管理员执行初始化或加盘后关闭页面、网络中断，或产品服务重启，都会让正在运行的 `wipefs`、`parted`、`mkfs.btrfs` 或 `btrfs device add` 被杀。固态硬盘的 `mkfs.btrfs` 会先 TRIM 全盘，可能要几分钟，这个窗口并不小。
- 被打断的初始化只能停在 `needs_attention`，没有恢复路径。界面仍显示“尚未创建数据卷”并允许再建，但原来的盘可能已挂载、不再是“未使用”：
  - 盘上有 `ANAS_DATA` 文件系统，没有卷标记，也没有挂载；
  - 或者卷已完整建好、已挂载，产品却不知道它的 UUID，所有文件请求都返回 `volume_unavailable`。
- 管理员再用另一块空闲盘重试时，`CreateVolume` 不检查已有的卷标记或挂载单元，直接改写 `srv-a\x2dnas-data.mount` 的 UUID。下次开机挂载的是新的空卷，原来的数据盘显示为“未使用”，可以被清除。
- 加盘在 `btrfs device add` 成功之后才把冗余方式写进卷标记。进程恰好在 ioctl 期间或之后被杀时，盘已加入、方式没有记下，Host Agent 的检查不会转换：用户要的是镜像，得到的是没有冗余的扩容。

## 最小复现

1. 产品服务与 Host Agent 运行 `4f05c2e`，有一块未使用的硬盘。
2. 生成初始化计划、确认，点“清除硬盘并创建数据卷”，在 `mkfs.btrfs` 运行时关闭页面（或在浏览器开发者工具中取消 `execute` 请求）。
3. 实际：请求的 context 被取消，`exec.CommandContext` 杀死 `mkfs.btrfs`，计划进入 `needs_attention`，没有重试入口。期望：命令照常完成，计划随后成功，或停在可查询、可安全重试的状态。

单元测试复现了同一路径：Host Agent 的操作在发起请求的 context 被取消后必须照常完成（[`volume_test.go`](../../internal/hostops/linux/volume_test.go) 的 `TestAVolumeOperationOutlivesTheRequestThatStartedIt`）；修复前 `CreateVolume` 直接使用请求的 context。

## 观察事实

- 调用链全程传递浏览器请求的 context：`handleExecuteStoragePlan` 的 `r.Context()` → `storage.Service.ExecutePlan` → Host Agent 客户端 → Host Agent 的 `handleCreateVolume` 的 `r.Context()` → `exec.CommandContext`。对比：后台的冗余方式转换有意使用 `context.Background()`。
- 产品服务启动时把 `running` 的计划一律改为 `needs_attention`，不管 Host Agent 那边是否已经完成。
- 产品只从 `succeeded` 的计划推断数据卷是否存在与它的 UUID（ADR 0025 的后果中写明）；宿主机上挂载着的卷不参与判断。
- 宿主机状态只在硬盘挂载后把它算作数据盘：格式化完成但未挂载的盘仍是“未使用”，带 `btrfs`（`ANAS_DATA`）签名，可以再次规划；已挂载的则不能。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 只把 Host Agent 的命令改用 `context.Background()` 就够 | 对照产品服务重启：结果无人接收，计划仍被改为 `needs_attention`，而卷已建好 | 否：还需要按计划 ID 记录结果并由产品查询 |
| Host Agent 重启后自动接着执行是安全的 | 对照规格“中断后不自动执行”：断电后数小时再擦盘会让管理员意外 | 否：改为可查询、由管理员重试，重试幂等 |
| 卷挂载前被打断时从头再来是安全的 | 挂载前产品没有记录这个卷，文件服务拒绝访问，卷上不可能有文件 | 是 |
| 加盘时先写目标方式会让失败的加盘也触发转换 | 只记方式而不计设备数时，单盘卷会被要求转为镜像，或扩容卷在空间不够时转为镜像 | 是：所以记为 `pending`，带加入后的设备数，内核列出足够的设备才生效 |

## 根因

执行接口把“破坏性操作的生命周期”与“浏览器请求的生命周期”绑在一起，而操作本身既没有按计划 ID 的进度记录，也不幂等；产品端只能从自己保存的成功计划推断宿主机状态，没有与实际观测对账的路径。加盘把记录冗余方式放在加盘之后，进一步留下了一个“盘已加入、意图丢失”的窗口。

## 修复与回归证据

- 修复（行为与边界见[存储与文件架构的失败语义](../architecture/storage-and-files.md#失败语义)和[规格](../specs/disk-roles-and-redundancy.md#边界与失败)）：
  - Host Agent 在自己的上下文中运行建卷与加盘，按计划 ID 把步骤、格式化出的文件系统与结果记在 `/var/lib/a-nas/volume-operations.json`，新增 `GET /v1/storage/operations/{planId}`；重启后把未完成的操作读为 `interrupted`。代码：[`volumeops.go`](../../internal/hostops/linux/volumeops.go)、[`volume.go`](../../internal/hostops/linux/volume.go)、[`agent.go`](../../internal/hoststate/agent/agent.go)。
  - 重试幂等：卷挂载前从头清除并格式化，挂载后只补齐子卷、卷标记与空间；已加入的硬盘跳过。
  - 建卷拒绝不是本计划建出的已挂载卷，以及指向别的文件系统的挂载单元；接管卷标记写着本计划（或更早版本写的、首块硬盘相同）且成员恰为计划硬盘的卷。卷标记新增 `planId`。
  - 加盘先把目标方式作为 `pending` 写进卷标记，再加盘；Host Agent 的检查在设备齐全后把它转为 `layout`。
  - 产品服务执行后返回 `202` 与进行中的计划，在后台每秒查询直到结束，重启后接着查询；`needs_attention` 的计划可重试；没有登记数据卷而宿主机显示一个时拒绝新建（`409 volume_exists`）；新增 `GET /api/v1/storage/plans` 供页面找回。代码：[`operation.go`](../../internal/storage/operation.go)、[`service.go`](../../internal/storage/service.go)、[`product.go`](../../internal/httpapi/product.go)、[`StorageSection.tsx`](../../web/src/settings/StorageSection.tsx)。
- 测试：在每一步注入停止与被杀，见[规格的验收证据](../specs/disk-roles-and-redundancy.md#验收证据)中“执行在设备上进行”一节。修复前，取消请求会杀死被测命令；修复后，取消请求的操作照常完成，Host Agent 在清除、格式化、挂载、准备各步停下后报告 `interrupted` 与步骤，重试只在挂载前重新格式化，并留下与挂载卷一致的卷标记和挂载单元。

- 本地 fake 模式在浏览器中核对：执行时向导逐步显示设备所在的步骤并跟到完成；加盘执行中重启产品服务后，存储页显示“上次的存储操作没有完成”，向导给出停下的原因与“重试”，重试再次停下时显示新的原因。
- 修复在 [PR #202](https://github.com/zhongwater123/A-NAS/pull/202)。

## 后续工作

- 部署后在实验 NAS 上确认：Host Agent 能写 `/var/lib/a-nas/volume-operations.json`；对一块空闲盘执行加盘时关闭页面，回来后向导接着显示并成功。部署须同时更新 Host Agent 与产品服务（IPC 改为启动加查询）。
- Host Agent 自身被停止时操作仍会中断，只能由管理员重试。若需要跨 Host Agent 重启继续运行，可以考虑把每次操作放进 systemd 临时单元；目前没有必要。

## 关联

- 规格：[硬盘用途与数据卷冗余](../specs/disk-roles-and-redundancy.md#边界与失败)、[基础存储与共享](../specs/basic-storage-and-sharing.md#数据卷初始化)
- ADR：[0025](../adr/0025-span-the-data-volume-across-disks-with-btrfs-profiles.md)（卷标记中的目标冗余方式）、[0007](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)（类型化特权边界不变：请求仍只含计划 ID、稳定磁盘 ID 与冗余方式）
- 运行手册：[把硬盘加入数据卷](../runbooks/add-a-disk-to-the-data-volume.md)、[配置并验收 v1.0.1 实验数据卷](../runbooks/provision-v1.0.1-experimental-storage.md)
- 架构：[存储与文件架构](../architecture/storage-and-files.md#失败语义)
