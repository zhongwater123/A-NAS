# 存储与文件架构

## 数据流

```text
本地 Web（127.0.0.1）
  │ 会话 Cookie + CSRF
  ▼
非特权 Product Service（无任何空间的文件权限）
  ├── Accounts / Policy ── SQLite/WAL（系统盘）
  ├── File Catalog / Trash / Snapshot Catalog ── SQLite/WAL（系统盘）
  ├── Storage Plans / Operations ── SQLite/WAL（系统盘）
  ├── 类型化 HTTP/JSON over UDS（root:a-nas 0660）
  │       ▼
  │   root Host Agent
  │     ├── 稳定磁盘重新枚举 → GPT → Btrfs → UUID mount unit；加盘与冗余方式转换
  │     ├── Linux 身份、Samba 账号与原子配置、空间 ACL
  │     └── Btrfs 只读子卷快照
  └── 文件操作 + 会话令牌 over UDS（file-broker.sock，root:a-nas 0660）
          ▼
      root File Broker（Host Agent 内）── 只读查询会话表 → 用户身份
          │ socketpair，每个活跃用户一个
          ▼
      文件工作进程（用户 UID 与组）── openat2 操作数据卷，内核按 ACL 判定
          └── 打开/新建的文件经 SCM_RIGHTS 交回 Product Service 流式读写

Windows SMB ── SMB3 ── 个人子卷 / Shared 子卷
                         │ 增量目录扫描与回收站导入
                         └──────────────► File Catalog
```

## 卷与目录

- 数据卷固定挂载到 `/srv/a-nas/data`，mount unit 使用 `UUID=`、`noatime,compress=zstd:3,nodev,nosuid,noexec`。
- 数据卷可以由多块硬盘组成，用 Btrfs 自带的多设备配置（[ADR 0025](../adr/0025-span-the-data-volume-across-disks-with-btrfs-profiles.md)）：mount unit 不变，Btrfs 在全部成员出现后挂载；挂载表只列出其中一块设备，所以宿主机状态从 sysfs 的 `fs/btrfs/<uuid>/devices` 认出每块成员盘，任何已挂载 Btrfs 的设备都算使用中；`lsblk` 的文件系统列来自 udev 数据库，加盘后在重启前是旧的（[调查](../investigations/2026-10-20-added-disk-shown-unused-until-reboot.md)）。卷标记 `.a-nas-volume.json` 的 `layout` 记录目标冗余方式（镜像、扩容或条带；单盘时没有），Host Agent 在启动时和每 15 分钟检查 sysfs 的 `allocation/<类型>/<profile>/used_bytes` 与 `exclusive_operation`，把不在目标 profile 中的数据以 `soft` 过滤的 balance 转换过去。停止 Host Agent 或关机会取消正在进行的 balance，下一次检查接着做。产品服务从同一份观测计算转换进度，目标冗余方式取自最近一次成功的存储计划。
- 根目录必须同时是 Btrfs 且存在 root 创建的 `.a-nas-volume.json`。缺少任一证据时，文件服务返回 `volume_unavailable`，不会创建空间目录。
- 个人空间位于 `spaces/private/<username>`，共享空间位于 `spaces/shared`；公开接口只使用不透明空间 ID。
- 授权只由 POSIX ACL 表达（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）。空间根目录、回收站目录及其容器均为 `root:root`、无 setgid，权限只在这些目录上设置，内容靠默认 ACL 继承，从不递归改写：

  | 目录 | 访问 ACL | 默认 ACL |
  |---|---|---|
  | `spaces`、`spaces/private` | `a-nas-users` 仅 `--x`（可穿过、不可列出） | 无 |
  | `spaces/private/<username>` | 所有者 `rwx` | 同访问 ACL |
  | `spaces/shared` | `a-nas-users` `rwx`（只读共享文件夹为 `r-x`，另授写入者 `rwx`） | 同访问 ACL |
  | `<空间>/.a-nas-trash` | 该空间的用户仅 `--x` | 无 |
  | `<空间>/.a-nas-trash/<username>` | 该用户 `rwx` | 同访问 ACL |
  | `apps`、`apps/<id>` | `apps` 仅 root；`apps/<id>` 属主为应用身份 `app-<id>` | 无 |
  | `photos` | 属主为相册服务身份 `a-nas-photos`（UID/GID 31000），`0700`，无扩展 ACL（[ADR 0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)） | 无 |

  Product Service 账号 `a-nas` 不在任何 ACL 中。数据卷挂载点的父目录 `/srv/a-nas` 与挂载点 `/srv/a-nas/data` 均授予 `a-nas-users` 穿过权限，供 smbd 与文件工作进程以用户身份进入空间；该权限不允许列出卷根。相册服务身份在这两级另有一条穿过权限。`photos` 只在 `a-nas-photos` 账号以固定 UID 存在时由 Host Agent 创建，属主、模式或 ACL 漂移在启动时和每 15 分钟修复，内容从不遍历。它与空间对账分开执行，失败只记录日志，不阻止空间修复。
- Samba 以登录用户身份读写：`inherit acls = yes`、`nt acl support = no`、`hide unreadable = yes`、`veto files = /.a-nas-trash/`（客户端无法按名称打开或重命名回收站目录，`recycle` 不受影响），不使用 `force group`；`create mask = 0660`、`directory mask = 0770` 决定新条目的 ACL mask，缺省 `0744` 会让继承的写权限失效。
- Web 删除与 Samba `recycle`（`keeptree`）统一写入 `<空间>/.a-nas-trash/<username>`：Web 条目为 `<trash-id>/content`，SMB 删除保留原相对路径并按文件导入回收站，原目录仍存在时恢复到原目录。每个用户的回收目录由 Host Agent 预建，恢复或清除只删除其下的空目录，不删除用户回收目录本身。共享空间中他人删除的文件对其他成员不可见。
- Host Agent 启动时先创建固定组，再幂等对账已注册空间并刷新经过 `testparm` 的 Samba 配置；Product Service 同步身份后也立即对账，新账号随即拥有回收目录。之后每 15 分钟以 `getfacl` 比对上述目录，修复属主、setgid 或 ACL 漂移并在 journal 记录 `repaired drifted data-volume permissions`。
- 对账以 `openat2(RESOLVE_NO_SYMLINKS)` 打开每个目录，`chown`、`setfacl` 等只作用于已打开的目录（`/proc/<pid>/fd/<n>`），因此能改写父目录条目的用户无法在检查与修改之间把 root 引到别处。回收站路径上若出现非目录（例如在可写的共享空间中放置的符号链接），将改名为 `<名称>.displaced-<时间>` 后重建，不跟随也不删除。
- 只读快照位于 `.a-nas-snapshots/<snapshot-hash>`，不由 Samba 发布；恢复复制到普通空间的新位置。

## 身份与一致性

- 磁盘以 WWN（缺少时为规范化型号与序列号）派生的 `disk:*` ID 标识，执行前重新验证型号、容量、传输类型和指纹。缺少或重复硬件身份时只生成 `observation:*` 展示键，不能用于操作，也不能当作稳定身份保存；状态观测保留设备角色、占用和文件系统事实，并把单个设备的异常传到产品 API。资格与显示规则见[基础存储规格](../specs/basic-storage-and-sharing.md#服务与错误)。
- 账号对应 UID 20100–29999 的 Linux 身份，固定组 `a-nas-users`/`a-nas-admins` 使用 GID 20000/20001（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）。Product Service 启动时把全部已启用和已禁用账号同步给 Host Agent；Host Agent 拒绝接管区间外或非 A-NAS 分配的同名账号，并把身份表写入 `/var/lib/a-nas-host/identity-registry.json` 与数据卷 `.a-nas-identities.json`。
- Host Agent 的记录（空间登记、身份表、查看授权、应用身份与数据卷操作日志）在它自己的 `StateDirectory`：`/var/lib/a-nas-host`，`root:root 0700`，产品服务打不开（[#176](https://github.com/zhongwater123/A-NAS/issues/176)）。只读取 root 所有、只有一个链接的普通文件，并按对应请求的规则逐条校验：空间根只能是 `spaces/shared` 或以账号命名的 `spaces/private/<用户名>`；身份与应用身份不得共用 UID；查看授权超出时限或管理员已不能查看时按已到期读入，下次检查即撤销；操作日志的每条记录须与其类型的盘数、冗余方式、步骤和结果一致。校验不通过的记录整份停用，相应操作报错；身份表停用时不重做任何空间的 ACL（否则每个个人空间都会落到无属主的封闭 ACL），也不覆盖数据卷上的身份表副本。每次写入记录都在改名后刷盘所在目录，操作日志等在断电后仍在。更早的版本把它们放在产品服务的 `/var/lib/a-nas`，Host Agent 启动时移过来一次，回滚步骤见[实机配置手册](../runbooks/provision-v1.0.1-experimental-storage.md#回滚)。
- 普通文件以 `file:*` ID 标识；Catalog 用空间、inode 与相对路径维持重命名后的身份。
- Web 文件操作经文件代理以登录者身份执行（见下节），同步更新文件系统与 Catalog；SMB 变更由目录对账吸收（见下条）。每个空间是独立的 Btrfs 子卷并有自己的根 ACL，跨空间改名会失败（`EXDEV`），即使成功也会带走原空间的 ACL；因此跨空间剪切由 Product Service 拆成复制到目标空间、再把原条目原子改名进操作者的源空间回收站，复制品是新条目。复制期间经 SMB 到达的新内容仍保留在源条目中，恢复与到期规则见[文件规格](../specs/basic-storage-and-sharing.md)。复制通过已打开的描述符读取并保留每个文件和目录的修改时间，不继承源 ACL。复制先写成目标文件夹中的隐藏副本 `.a-nas-copy-*`，完成后改名就位；Product Service 在内存中持有进行中的副本，目录对账只列出副本本身而不进入，并删除无人持有的副本（重启中断的复制留下的）。隐藏目录、符号链接、设备文件和越界路径不进入目录。同一路径被新 inode 占用时，旧行先让出路径，避免唯一约束使对账永久失败。
- 对账与 Web 变更按空间排序（[调查](../investigations/2026-10-21-stale-reconcile-scan-rewrote-trash-rows.md)）。删除、恢复、清除、移动、复制、上传与新建文件夹从第一次文件操作到其行提交为止处于“在途”；对账与 Samba 回收导入记下扫描开始时的代次，开始时有变更在途、或扫描期间有变更开始或结束，就整体丢弃这次扫描：变更自己的行已是最新，SMB 的改动由下一轮补上。扫描结果在一个事务内写入，期间同一空间的新变更等待。必须拿到结果的扫描（上传完成时的文件夹已被 SMB 移走，分享选择的文件夹内容）先等在途变更结束，再独占扫描与写入。扫描到的 inode 属于回收站行时，回收站仍持有该 inode 或无法确认就不收录，否则删除这条过期的回收站行；打开条目时路径上的 inode 必须与记录一致，不一致视为不存在。写入时一行也没改的对账不推进代次，因此浏览文件夹不会让同时进行的整空间对账被丢弃。
- 对账只覆盖请求涉及的范围（[#167](https://github.com/zhongwater123/A-NAS/issues/167)，[调查](../investigations/2026-10-21-whole-space-reconcile-scale.md)）：列目录只扫描该文件夹的直接子项，影视、音乐等读取文件夹之下全部内容的 `Tree` 只扫描该子树；写入时与范围内的现有行逐项比较，只写有变化的行，未变化的范围不写任何行。只有整空间对账能断定没找到的条目已不存在并删除其行：后台每 5 分钟的对账、快照比较与“最近删除”、对整个空间的 `Tree`，以及文件夹不在记录位置（经 SMB 移走）时的查找。部分对账没找到的行改到 `.a-nas-stale/<id>` 待定，列表与重名检查都不包含它们；之后任何一次对账在别处找到同一 inode，就以原 ID 放回。文件夹经 SMB 改名或移动时，其下各行随它改写路径（经它的待定路径中转，避免中途撞上唯一约束）；同名位置被另一个文件夹占用时，旧文件夹下的行随旧行一起待定。同一空间的整空间对账一次只跑一个，等待的请求由开始于其请求之后的那一次满足；后台循环跳过 4 分钟内已对账过的空间，共享空间每轮只对账一次。
- 后台每 5 分钟的目录对账、Samba 回收导入和回收站到期清除只为本进程见过且仍有效的会话执行；长期未登录用户在下次登录时补做。

## 文件代理

- Product Service 的每个文件操作都携带请求的会话令牌。File Broker 以只读方式打开 `control.db` 自行校验令牌、账号状态与过期时间，不信任 Product Service 声明的身份；令牌无效、账号被禁用或数据卷不可用时拒绝，不会派生进程。`control.db` 暂时读不出（如 SQLite 忙）时返回暂不可用而不是拒绝，也不关闭文件代理、终端、助手与相册、分享查询共用的只读句柄（[#166](https://github.com/zhongwater123/A-NAS/issues/166)）。
- 通过校验后，Broker 为该用户（按 UID 与角色）派生 `anas-host-agent file-worker`：UID/GID 为账号 UID，附加组为 `a-nas-users`（管理员另加 `a-nas-admins`），只接受 20100–29999 内且与 `/etc/passwd` 一致的身份。工作进程拒绝以 root 运行，空闲 5 分钟后退出。同一账号最多同时有 4 个工作进程（助手“提交更改”的受限进程另算一组），每个一次执行一个操作：一个成员长时间的复制或删除不会挡住他的列目录、预览与上传，超出时排队等空闲的进程。
- 工作进程与管理员的终端 Shell 各在一个 systemd 临时 scope（`anas-files-<账号>-<PID>.scope`、`anas-terminal-<账号>-<PID>.scope`）中运行，Host Agent 经 systemd 私有套接字创建（[`internal/systemdscope`](../../internal/systemdscope/scope.go)）：它们不计入 Host Agent 的 `MemoryMax`/`TasksMax`，写入日志也不以 Host Agent 的名义出现。scope 绑定（`BindsTo=`）Host Agent 的单元，Host Agent 停止、重启或崩溃时随之结束，与此前同在一个 cgroup 时相同。systemd 建不成 scope 时记录错误，进程留在 Host Agent 的 cgroup 中照常工作。
- Product Service 关闭连接（浏览器离开、请求取消）时，Broker 向工作进程发送取消消息，工作进程停止正在执行的扫描、复制、删除或用量统计并照常应答，之后继续服务；复制大文件按 64 MiB 一段检查取消。助手“提交更改”的操作不随调用方取消，避免改到一半。
- 扫描结果分页传输：每条消息最多 2048 个条目（路径很长时继续减半），Broker 收到一页转发一页，root 进程不持有整棵树。单条消息仍以 64 MiB 为上限，超出时以错误应答，工作进程不退出。分页由请求声明，旧版 Product Service 仍收到单条消息。
- 工作进程用 `openat2(RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS)` 解析数据卷内路径：`..` 与符号链接无法越界，经过仅可穿过的容器与回收站目录只需执行权限。改名使用 `RENAME_NOREPLACE`，不会覆盖已有条目。需要 Linux 5.6 及以上内核。
- 下载与上传的文件内容不经过 root：工作进程打开或新建文件后经 `SCM_RIGHTS` 把描述符交回 Product Service。容量检查在 Product Service 中对数据卷根目录执行；复制目录按其中文件总大小检查，分片上传开始时检查整个文件、之后每片再检查一次，旧的 multipart 上传只检查下一块数据。
- 回收站条目只对删除者可见并只能由其恢复或清除，因为每个用户的回收目录只授予本人。
- 例外：只读快照仍由 root Host Agent 创建、列出并读出对象内容；恢复时由工作进程以用户身份写入目标空间。列出时跳过符号链接；读出时由内核在快照根之下解析路径（`openat2` 的 `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS`），路径中任何一段是符号链接都按不存在处理（[#176](https://github.com/zhongwater123/A-NAS/issues/176)）。
- 派生工作进程要求 Host Agent 保留 `CAP_SETUID`/`CAP_SETGID`。systemd 257 在单元同时写 `User=` 与 seccomp 过滤（`RestrictAddressFamilies`）时会拿走 `CAP_SETUID`，所以 `anas-host-agent.service` 不写 `User=`/`Group=`，由 `make ops-check` 保证。Broker 启动时以 `nobody` 身份按工作进程相同的参数运行探针，记录 `file broker identity switch verified` 或失败原因；安装器在探针未通过或能力缺失时以退出码 5 结束。
- Product Service 以 `ProtectSystem=strict` 运行，它看到的数据卷挂载始终是只读的。它在文件操作前只确认挂载的是预期的数据卷（Btrfs、卷标记与已初始化卷的 UUID 一致），预期 UUID 取自已初始化卷的记录，不在每次请求时读取主机状态，也不判断可写性；真正只读的数据卷由执行写入的工作进程以 `EROFS` 报告为卷不可用。

## 上传与下载

传输中心（[规格](../specs/transfer-center.md)）的数据流：

```text
浏览器 tus-js-client ──POST/HEAD/PATCH /api/v1/uploads──► transfersapi（tusd pkg/handler，会话与 CSRF 由产品 API 先行校验）
                                                            │ 数据存储
                                                            ▼
                                                   transfers.Service ── transfers.db（状态、进度、30 天记录）
                                                            │
                                                            ▼
                                                   files.Service：BeginUpload / WriteUpload / CommitUpload / AbortUpload
                                                            │ 创建临时文件、建子文件夹、重命名、移入回收站
                                                            ▼
                                                   File Broker（登录者 UID）──► 目标文件夹中的 .a-nas-upload-*
```

- 创建上传时以用户身份检查写权限、建好文件夹上传的子文件夹、检查整个文件的保留空间并创建临时文件；产品服务持有其描述符（`files.PendingUpload`）直到完成或放弃，分片直接写描述符，不再经过文件代理（[ADR 0019](../adr/0019-resume-uploads-over-tus-with-broker-held-files.md)）。
- 完成时同步到磁盘，按同名策略以 `RENAME_NOREPLACE` 重命名；`replace` 先把原文件移到上传者的回收站。目录对账跳过 `.a-nas-upload-*`，并以当前用户身份删除内存中无人持有、10 分钟未修改的临时文件。
- 下载先经 `POST /api/v1/transfers/downloads` 校验条目并建立记录，浏览器再以普通下载获取 `/api/v1/transfers/{id}/content`。单个文件经 `http.ServeContent` 支持 Range；文件夹与多选以 `archive/zip` 存储方式流式打包，打包前以用户身份重新列出内容，打不开的文件跳过并在压缩包内列出。进度按发送的字节计数；取消或打包中途失败时中断连接，浏览器不会把截断的文件当作完成。
- 进行中的传输只在产品服务进程内；状态变化与每 5 秒的进度写入 `transfers.db`。重启时未完成的上传记为 `service_restarted`；连接断开或重启时正在发送的下载记为失败（`interrupted`），浏览器续传或重新下载同一地址时这条记录重新开始。

## 管理员查看模式

- Product Service 校验管理员密码与原因后在 `viewing_grants` 记录授权、写审计并通知所有者，再请求 Host Agent 授予；Policy 在到期或结束前把该个人空间以只读方式列给该管理员，写操作一律拒绝。
- Host Agent 先把授权写入 `/var/lib/a-nas-host/viewing-grants.json`，再从空间根目录用 `O_NOFOLLOW` 文件描述符逐级为每个目录与普通文件加入 `user:<管理员>` 只读条目（目录另加默认条目），直接读写 `system.posix_acl_*` 扩展属性且不改变已有 mask。空间根、回收站根与个人回收目录的完整 ACL 在授权期间包含该条目，因此漂移修复不会将其移除。
- Host Agent 开始提供服务后在后台撤销到期授权，此后每分钟一次；数据卷离线时保留记录，卷恢复后撤销。撤销同样逐级移除条目，再恢复根目录的精确 ACL。大空间上撤销需要数分钟，不阻塞文件代理；其间 Product Service 已按到期时间拒绝这份授权，ACL 是第二道防线。
- SQLite 各模块使用 WAL 和单连接串行化写事务。密码摘要、会话令牌摘要、目录、回收站、快照、计划和审计均在系统盘持久化。

## 快照

- 手动快照与定时快照都是空间子卷的 Btrfs 只读快照，由 root Host Agent 创建、列出、读出对象与删除；快照目录在系统盘的 `files.db`，为每个文件记录大小与修改时间，供备份中心把相同的版本合并显示。
- 拍摄分两步：Host Agent 先创建只读快照，再按需逐行流式返回其中的文件与文件夹（`GET /v1/snapshots/{id}/objects`，每行一个 JSON 对象，以带总数的结束行收尾）；Product Service 读完全部清单后在一个短事务中写入目录，所以清单长度不受单次回复大小限制，单连接的目录也不会在遍历期间被占住。创建与删除都按快照 ID 幂等：已存在的快照算拍好，已不存在的算删除。Product Service 从请求 Host Agent 拍摄之前起就持有这份快照，直到目录记下它；其间失败或中断（关闭标签页、服务停止）都会删除它，停止时等这一步完成后才关闭目录。为了让崩溃留下的快照也能删掉，目录在请求拍摄之前把它登记到 `pending_snapshots`，并在记下快照的同一个事务中删除登记；拍摄失败后删不掉的快照，登记会保留。清扫只删除登记过的快照：Product Service 启动后（数据卷离线时等它上线）以及之后每小时，逐个删除不在拍摄中的登记快照（`DELETE /v1/snapshots/{id}`）。随后把目录记录的快照与正在拍摄的快照交给 Host Agent 核对（`POST /v1/snapshot-survey`）。核对只读，只在挂载着数据卷时进行，回报其中哪些仍在、数据卷上另有几份。不在数据卷上的快照，其目录记录随之删除；但如果目录记录的快照一份都不在，更可能是快照文件夹丢失或无法读取，这时保留记录。目录既没有记录、也没有登记的快照一律保留，因为目录可能丢失过，或者从旧副本恢复过；清扫在这类快照的数量变化时写一条活动日志。读目录中途出错时，清扫不删除任何东西。Host Agent 不再提供按名单批量删除的 `PUT /v1/snapshots`（[#269](https://github.com/zhongwater123/A-NAS/issues/269)）。原因与取舍见[调查](../investigations/2026-10-21-snapshot-listing-cut-at-one-mebibyte.md)。
- 每个空间至多一份快照计划。Product Service 每分钟检查一次，到期时直接请求 Host Agent 拍摄，不经过文件代理，因此不需要任何用户会话；拍摄前确认挂载的是预期数据卷，否则记录失败并在 15 分钟后重试。成功后按计划的保留方式（固定份数或智能保留）删除该空间不再保留的定时快照，手动快照不受影响。
- 恢复文件夹时，文件代理以用户身份先建目录、再逐个复制文件，新条目照常继承目标空间的默认 ACL；文件写完后由文件代理以用户身份把修改时间设回快照中的值（`set_mod_time`，只作用于普通文件、不跟随符号链接）。“还原此版本”先把整份文件写成临时文件，再把当前文件移入回收站并改名就位，与替换上传相同。快照对象仍由 Host Agent 读出（见上文的例外）。
- 快照与现在的比较、“最近删除或移走”只读 `files.db`：按路径把快照条目与文件目录逐行比对，打开快照顶层与列出最近删除前先对账空间，以计入 SMB 的改动。
- 行为、权限与失败处理见[备份中心](../specs/backup-center.md)。

## 失败语义

- 计划十分钟过期，执行前可以取消（`cancelled`）。
- 执行与发起它的请求无关（[#165](https://github.com/zhongwater123/A-NAS/issues/165)）：Host Agent 在自己的上下文中运行建卷或加盘，按计划 ID 把所在步骤、格式化出的文件系统与结果记在系统盘的 `/var/lib/a-nas-host/volume-operations.json`，`GET /v1/storage/operations/{planId}` 查询；同一时间只运行一个操作。产品服务每秒查询一次直到结束，重启后对仍为 `running` 的计划接着查询，所以关闭页面、断网或重启产品服务都不会中途杀死 `wipefs`、`mkfs.btrfs` 或 `btrfs device add`。
- Host Agent 自身停止（重启、断电）时，日志中的操作在下次启动读为 `interrupted`，计划进入 `needs_attention`，不自动重做。管理员重试同一计划时，Host Agent 重新核对硬盘并从停下处继续：新卷挂载前从头清除并格式化（上面不可能有文件），挂载后只补齐子卷、卷标记与空间；已经加入数据卷的硬盘不再清除。Host Agent 从没收到的计划什么也没改，同样进入 `needs_attention` 等待重试。失败原因只写步骤与类别，设备路径与命令输出只进 Host Agent 的系统日志。
- 新建数据卷不碰不是本计划建出的数据卷：已挂载的卷只有由本计划格式化，或卷标记写着本计划（更早版本的标记：首块硬盘相同）且成员恰为计划中的硬盘时才接管；mount unit 指向的文件系统只有由一次未成功的建卷格式化、或就在本计划将清除的硬盘上时才可替换，否则离线的数据卷会在下次开机被遗忘、它的硬盘显示为未使用。产品服务没有记录数据卷、宿主机却显示一个时，拒绝新建计划（`409 volume_exists`），由留下它的那次计划重试接管。
- 加盘先把目标冗余方式作为 `pending`（计划 ID、方式、加入后的设备数）写进卷标记，再执行 `btrfs device add`，之后改写为 `layout`。硬盘加入后进程被杀时，Host Agent 的检查在内核列出足够的设备后把 `pending` 转为 `layout` 并开始转换。加盘成功后的冗余方式转换不属于计划，被中断时由 Host Agent 的检查接着做。
- 可用空间必须保留容量的 5%，且至少 10 GiB；不足、只读或离线卷拒绝写入。文件与相册共用 [`internal/capacity`](../../internal/capacity/capacity.go) 中的同一规则。
- Samba 配置先由 `testparm` 验证，再原子替换；reload 失败恢复旧配置。
- 创建或重置 Samba 凭据失败时账号进入 `error` 或保持不可登录，旧会话失效。
- 数据盘移除后系统盘上的控制面仍可启动；文件与快照接口拒绝访问，绝不回退到系统盘。
- Host Agent 先提供服务，再在后台对账数据卷：修复空间权限、把身份清单写到数据卷、刷新 Samba 配置（[#166](https://github.com/zhongwater123/A-NAS/issues/166)）。任何一步失败（如数据卷被 Btrfs 转为只读、身份注册表无法解析、`testparm` 或 `smbcontrol` 失败）只记录并经宿主机状态报告（`reconcileFailed`），系统健康显示[“数据卷的权限与共享设置没能更新”](../specs/system-health.md)；此后每 15 分钟重做完整对账，成功后恢复为只修权限。数据卷只读时 Web 文件可以读，写入返回卷不可用，终端与日志照常可用。原因与证据见[调查](../investigations/2026-10-21-host-agent-single-failures-stop-everything.md)。

## 关联

- [ADR 0007](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- [ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)：本页描述的身份与授权模型
- [Host Agent 在 systemd 沙箱下丢失 CAP_SETUID](../investigations/2026-10-08-host-agent-loses-setuid-under-systemd.md)、[一处故障让 Host Agent 的全部入口失效](../investigations/2026-10-21-host-agent-single-failures-stop-everything.md) 与[系统测试](../../scripts/system-test.sh)
- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)、[传输中心规格](../specs/transfer-center.md)
- [ADR 0019](../adr/0019-resume-uploads-over-tus-with-broker-held-files.md)：分片上传与持有的临时文件
- [备份中心规格](../specs/backup-center.md)与[快照计划](../../internal/files/schedule.go)
- [实机配置与验收手册](../runbooks/provision-v1.0.1-experimental-storage.md)
- [`api/openapi.yaml`](../../api/openapi.yaml)
- [ADR 0025](../adr/0025-span-the-data-volume-across-disks-with-btrfs-profiles.md)、[硬盘用途与数据卷冗余规格](../specs/disk-roles-and-redundancy.md) 与[数据卷操作](../../internal/hostops/linux/volume.go)
- [ACL 对账](../../internal/hostops/linux/acl.go) 与 [权限矩阵 root 集成测试](../../internal/hostops/linux/root_matrix_integration_test.go)
- [Host Agent 记录的目录与迁移](../../internal/hostops/linux/state.go)、[读入校验与迁移测试](../../internal/hostops/linux/state_internal_test.go) 与 [root 集成测试 `TestRootStateLeavesTheProductServiceDirectory`](../../internal/hostops/linux/root_integration_test.go)
- [目录对账与变更的排序](../../internal/files/ordering.go) 与[交错回归测试](../../internal/files/reconcile_race_test.go)
- [按范围对账](../../internal/files/reconcile.go)、[范围回归测试](../../internal/files/reconcile_scope_test.go) 与[规模测量](../../internal/files/reconcile_scale_test.go)
- [文件代理的分页、取消与工作进程池](../../internal/filebroker/rootbroker/server.go) 与[测试](../../internal/filebroker/rootbroker/worker_test.go)
- [相册存储区修复](../../internal/hostops/linux/photos.go)
