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
- `spaces` 与 `spaces/private` 使用 `root:a-nas-members 0710`，允许 Product Service 和已注册 SMB 账号穿过但不能列出个人空间容器。个人空间使用 `username:a-nas 2770` 与访问/默认 POSIX ACL，让该用户和 `a-nas` 对新文件保持共同读写；Shared 使用 `root:a-nas-members 2770`。
- Web 删除移动到空间内 `.a-nas-trash/<trash-id>/content`；Samba `recycle` 写入 `.a-nas-trash/<username>`，目录对账把它导入同一回收目录。
- `.a-nas-trash` 由 Host Agent 在 materialize 时预建并保护；Web 与 Samba 的回收站子目录均使用 `0770`，不得由任一写入方独占为 `0700`。Host Agent 启动时幂等对账已注册空间，修复已初始化卷的容器权限并刷新经过 `testparm` 的 Samba 配置。
- 只读快照位于 `.a-nas-snapshots/<snapshot-hash>`，不由 Samba 发布；恢复复制到普通空间的新位置。

## 身份与一致性

- 磁盘以 Linux 稳定硬件链接形成的 `disk:*` ID 标识，执行前重新验证型号、容量、传输类型和指纹。
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
