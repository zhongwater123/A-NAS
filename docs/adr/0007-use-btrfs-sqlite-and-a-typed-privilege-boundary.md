# 0007：基础存储使用单盘 Btrfs、SQLite 与类型化特权边界

状态：accepted（v1.0.1）

A-NAS v1.0.1 需要在仍属实验设备的单盘上完成可恢复的文件闭环，同时把磁盘破坏性操作限制在可审计的小边界内。我们决定使用单盘 Btrfs 数据卷，使用系统盘上的 SQLite/WAL 保存控制面状态，由 root Host Agent 只执行类型化高层操作；Web 与 SMB 共用账号和权限身份，但分别保存 Argon2id 与 Samba 的不可逆凭据。

## 后果

- 数据卷提供子卷、只读快照与按 UUID 挂载能力，但不提供冗余，也不构成备份。
- SQLite 数据库绑定数据卷身份并位于系统盘；数据盘离线时服务拒绝文件写入，不在系统盘创建替代目录。
- Host Agent 的公共请求只包含磁盘、空间、计划或快照的稳定 ID，不接受设备路径、文件路径或任意命令。
- 密码明文只在创建或重置请求期间通过受限 UDS 传递；不写入日志或数据库。任一 Samba 配置或凭据步骤失败时，账号不可登录。
- SQLite 是控制面事实源；普通文件内容及 Btrfs 快照仍以数据卷为事实源，目录对账负责吸收 SMB 变更。

## 未选择

- ext4：缺少本版按文件恢复所需的原生只读快照边界。
- RAID：实验机只有一块明确用于 v1.0.1 的数据盘，且本版不承诺冗余。
- root 产品服务或任意 Shell IPC：会把浏览器输入扩大为宿主机命令权限。
- 同一密码摘要同时供 Web 与 Samba 使用：两端格式和验证机制不同，无法安全复用。

## 关联

- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [存储与文件架构](../architecture/storage-and-files.md)
- [`internal/storage`](../../internal/storage)
- [`internal/hoststate/agent`](../../internal/hoststate/agent)
- [`internal/accounts`](../../internal/accounts)

