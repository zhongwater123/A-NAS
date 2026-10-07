# 存储与文件架构

## 数据流

```text
本地 Web（127.0.0.1）
  │ 会话 Cookie + CSRF
  ▼
非特权 Product Service
  ├── Accounts / Policy ── SQLite/WAL（系统盘）
  ├── File Catalog / Trash / Snapshot Catalog ── SQLite/WAL（系统盘）
  ├── Storage Plans / Operations ── SQLite/WAL（系统盘）
  └── 类型化 HTTP/JSON over UDS（root:a-nas 0660）
          ▼
      root Host Agent
        ├── 稳定磁盘重新枚举 → GPT → Btrfs → UUID mount unit
        ├── Samba 账号与原子配置
        └── Btrfs 只读子卷快照

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

  过渡期内每个空间与回收站另含 `user:a-nas`，因为 Web 仍由 Product Service 代为读写；文件代理（[#13](https://github.com/zhongwater123/A-NAS/issues/13)）落地后移除。数据卷挂载点的父目录 `/srv/a-nas` 授予 `a-nas-users` 穿过权限，供 smbd 切换到用户身份后进入共享。
- Samba 以登录用户身份读写：`inherit acls = yes`、`nt acl support = no`、`hide unreadable = yes`，不使用 `force group`；`create mask = 0660`、`directory mask = 0770` 决定新条目的 ACL mask，缺省 `0744` 会让继承的写权限失效。
- Web 删除与 Samba `recycle`（`keeptree`）统一写入 `<空间>/.a-nas-trash/<username>`：Web 条目为 `<trash-id>/content`，SMB 删除保留原相对路径并按文件导入回收站，原目录仍存在时恢复到原目录。每个用户的回收目录由 Host Agent 预建，恢复或清除只删除其下的空目录，不删除用户回收目录本身。共享空间中他人删除的文件对其他成员不可见。
- Host Agent 启动时幂等对账已注册空间并刷新经过 `testparm` 的 Samba 配置；之后每 15 分钟以 `getfacl` 比对上述目录，修复属主、setgid 或 ACL 漂移并在 journal 记录 `repaired drifted data-volume permissions`。
- 只读快照位于 `.a-nas-snapshots/<snapshot-hash>`，不由 Samba 发布；恢复复制到普通空间的新位置。

## 身份与一致性

- 磁盘以 Linux 稳定硬件链接形成的 `disk:*` ID 标识，执行前重新验证型号、容量、传输类型和指纹。
- 账号对应 UID 20100–29999 的 Linux 身份，固定组 `a-nas-users`/`a-nas-admins` 使用 GID 20000/20001（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）。Product Service 启动时把全部已启用和已禁用账号同步给 Host Agent；Host Agent 拒绝接管区间外或非 A-NAS 分配的同名账号，并把身份表写入 `/var/lib/a-nas/identity-registry.json` 与数据卷 `.a-nas-identities.json`。
- 普通文件以 `file:*` ID 标识；Catalog 用空间、inode 与相对路径维持重命名后的身份。
- Web 写操作同步更新文件系统与 Catalog；SMB 变更由列表时增量扫描吸收。隐藏目录、符号链接、设备文件和越界路径不进入目录。
- SQLite 各模块使用 WAL 和单连接串行化写事务。密码摘要、会话令牌摘要、目录、回收站、快照、计划和审计均在系统盘持久化。

## 失败语义

- 计划十分钟过期；执行期间重启会把 `running` 恢复为 `needs_attention`，不自动重新格式化。
- 可用空间必须保留容量的 5%，且至少 10 GiB；不足、只读或离线卷拒绝写入。
- Samba 配置先由 `testparm` 验证，再原子替换；reload 失败恢复旧配置。
- 创建或重置 Samba 凭据失败时账号进入 `error` 或保持不可登录，旧会话失效。
- 数据盘移除后系统盘上的控制面仍可启动；文件与快照接口拒绝访问，绝不回退到系统盘。

## 关联

- [ADR 0007](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- [ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)：本页的身份与写入路径将由文件代理和文件系统 ACL 取代（尚未实现）
- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [实机配置与验收手册](../runbooks/provision-v1.0.1-experimental-storage.md)
- [`api/openapi.yaml`](../../api/openapi.yaml)
