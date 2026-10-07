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
  │     ├── 稳定磁盘重新枚举 → GPT → Btrfs → UUID mount unit
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
- 根目录必须同时是 Btrfs 且存在 root 创建的 `.a-nas-volume.json`。缺少任一证据时，文件服务返回 `volume_unavailable`，不会创建空间目录。
- 个人空间位于 `spaces/private/<username>`，共享空间位于 `spaces/shared`；公开接口只使用不透明空间 ID。
- 授权只由 POSIX ACL 表达（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）。空间根目录、回收站目录及其容器均为 `root:root`、无 setgid，权限只在这些目录上设置，内容靠默认 ACL 继承，从不递归改写：

  | 目录 | 访问 ACL | 默认 ACL |
  |---|---|---|
  | `spaces`、`spaces/private` | `a-nas-users` 与 `a-nas` 仅 `--x`（可穿过、不可列出） | 无 |
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

- 磁盘以 Linux 稳定硬件链接形成的 `disk:*` ID 标识，执行前重新验证型号、容量、传输类型和指纹。
- 账号对应 UID 20100–29999 的 Linux 身份，固定组 `a-nas-users`/`a-nas-admins` 使用 GID 20000/20001（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）。Product Service 启动时把全部已启用和已禁用账号同步给 Host Agent；Host Agent 拒绝接管区间外或非 A-NAS 分配的同名账号，并把身份表写入 `/var/lib/a-nas/identity-registry.json` 与数据卷 `.a-nas-identities.json`。
- 普通文件以 `file:*` ID 标识；Catalog 用空间、inode 与相对路径维持重命名后的身份。
- Web 文件操作经文件代理以登录者身份执行（见下节），同步更新文件系统与 Catalog；SMB 变更由列表时增量扫描吸收。隐藏目录、符号链接、设备文件和越界路径不进入目录。同一路径被新 inode 占用时，旧行先让出路径，避免唯一约束使对账永久失败。
- 后台每 5 分钟的目录对账、Samba 回收导入和回收站到期清除只为本进程见过且仍有效的会话执行；长期未登录用户在下次登录时补做。

## 文件代理

- Product Service 的每个文件操作都携带请求的会话令牌。File Broker 以只读方式打开 `control.db` 自行校验令牌、账号状态与过期时间，不信任 Product Service 声明的身份；令牌无效、账号被禁用或数据卷不可用时拒绝，不会派生进程。
- 通过校验后，Broker 为该用户（按 UID 与角色）派生 `anas-host-agent file-worker`：UID/GID 为账号 UID，附加组为 `a-nas-users`（管理员另加 `a-nas-admins`），只接受 20100–29999 内且与 `/etc/passwd` 一致的身份。工作进程拒绝以 root 运行，空闲 5 分钟后退出，同一用户的操作串行执行。
- 工作进程用 `openat2(RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS)` 解析数据卷内路径：`..` 与符号链接无法越界，经过仅可穿过的容器与回收站目录只需执行权限。改名使用 `RENAME_NOREPLACE`，不会覆盖已有条目。需要 Linux 5.6 及以上内核。
- 下载与上传的文件内容不经过 root：工作进程打开或新建文件后经 `SCM_RIGHTS` 把描述符交回 Product Service。容量检查在 Product Service 中对数据卷根目录执行；复制目录按其中文件总大小检查，上传只检查下一块数据。
- 回收站条目只对删除者可见并只能由其恢复或清除，因为每个用户的回收目录只授予本人。
- 例外：只读快照仍由 root Host Agent 创建、列出并读出对象内容；恢复时由工作进程以用户身份写入目标空间。

## 管理员查看模式

- Product Service 校验管理员密码与原因后在 `viewing_grants` 记录授权、写审计并通知所有者，再请求 Host Agent 授予；Policy 在到期或结束前把该个人空间以只读方式列给该管理员，写操作一律拒绝。
- Host Agent 先把授权写入 `/var/lib/a-nas/viewing-grants.json`，再从空间根目录用 `O_NOFOLLOW` 文件描述符逐级为每个目录与普通文件加入 `user:<管理员>` 只读条目（目录另加默认条目），直接读写 `system.posix_acl_*` 扩展属性且不改变已有 mask。空间根、回收站根与个人回收目录的完整 ACL 在授权期间包含该条目，因此漂移修复不会将其移除。
- Host Agent 启动时和此后每分钟撤销到期授权；数据卷离线时保留记录，卷恢复后撤销。撤销同样逐级移除条目，再恢复根目录的精确 ACL。
- SQLite 各模块使用 WAL 和单连接串行化写事务。密码摘要、会话令牌摘要、目录、回收站、快照、计划和审计均在系统盘持久化。

## 失败语义

- 计划十分钟过期；执行期间重启会把 `running` 恢复为 `needs_attention`，不自动重新格式化。
- 可用空间必须保留容量的 5%，且至少 10 GiB；不足、只读或离线卷拒绝写入。文件与相册共用 [`internal/capacity`](../../internal/capacity/capacity.go) 中的同一规则。
- Samba 配置先由 `testparm` 验证，再原子替换；reload 失败恢复旧配置。
- 创建或重置 Samba 凭据失败时账号进入 `error` 或保持不可登录，旧会话失效。
- 数据盘移除后系统盘上的控制面仍可启动；文件与快照接口拒绝访问，绝不回退到系统盘。

## 关联

- [ADR 0007](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- [ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)：本页的身份与写入路径将由文件代理和文件系统 ACL 取代（尚未实现）
- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [实机配置与验收手册](../runbooks/provision-v1.0.1-experimental-storage.md)
- [`api/openapi.yaml`](../../api/openapi.yaml)
- [ACL 对账](../../internal/hostops/linux/acl.go) 与 [权限矩阵 root 集成测试](../../internal/hostops/linux/root_matrix_integration_test.go)
- [相册存储区修复](../../internal/hostops/linux/photos.go)
