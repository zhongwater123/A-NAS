# 0008：统一 Linux 身份，以文件系统 ACL 作为唯一授权来源

状态：accepted；已实现（[#11–#16](https://github.com/zhongwater123/A-NAS/issues?q=is%3Aissue+%5BADR+0008%5D)，2026-10-07 合并）。实机部署与验收进度见[当前状态](../status/CURRENT.md)。

决定前（v1.0.1-rc.5 及更早），Web 由产品服务账号 `a-nas` 代为读写、SMB 以每个用户自己的 UID 读写，两个入口没有共同的写入身份，授权只存在于产品服务的应用层（见 [issue #9](https://github.com/zhongwater123/A-NAS/issues/9)）。我们决定采用 DSM 式模型：一套身份、一个授权来源、所有入口以用户本人身份访问磁盘。

## 决定

1. **一套身份**：A-NAS 使用专用区间 20000–29999 的 UID/GID。固定组 `a-nas-users`（全体有效账号，GID 20000）与 `a-nas-admins`（设备管理员，GID 20001）占用区间开头，20002–20099 保留给后续固定组；每个账号对应 UID 与同名私有组 GID 均为 20100–29999 中的同一个值。UID 在 SQLite 中分配和记录，永不复用。Host Agent 按控制面同步账号与组，只创建或修改由 A-NAS 分配的身份，拒绝与区间外现有账号、组或 UID 冲突。
2. **一个授权来源**：文件内容的访问权限只由数据卷上的 POSIX ACL 表达并由内核强制执行；产品 Policy 读写 ACL，不另存文件权限表。授权粒度为个人空间和共享文件夹的根目录，依靠默认 ACL 向下继承，不提供子目录或单个文件的自定义 ACL。
   - 个人空间：只授予所有者。
   - 共享文件夹：所有有效账号可读；写权限按共享文件夹授予用户或组（现有 `Shared` 默认授予 `a-nas-users`）。
3. **所有入口以用户本人身份访问磁盘**：
   - SMB：Samba 以登录用户的 UID 读写，不使用 `force user`/`force group`，关闭客户端编辑 ACL（`nt acl support = no`），开启 `inherit acls` 与 `hide unreadable`。
   - Web 文件操作：由 root **文件代理（File Broker）** 为每个活跃用户派生以该用户 UID/组运行的工作进程（`setresuid`/`setgroups`）。列目录、建目录、改名、删除到回收站等由工作进程执行；打开或创建文件由工作进程完成后经 `SCM_RIGHTS` 把文件描述符交回产品服务流式读写，root 进程不经手文件数据。
   - Web 终端：以登录者本人的 Linux 身份启动 Shell。
   - 应用容器：每个应用使用独立身份 `app-<id>`，对共享文件夹的访问同样以 ACL 授予并可在界面中看到。
4. **文件代理自行校验会话**：产品服务只转发会话令牌，文件代理查询会话存储确认“这是谁”。产品服务账号 `a-nas` 不再持有任何空间的文件权限。
5. **管理员默认不能访问他人个人空间**。需要时使用审计的[管理员查看模式](../../CONTEXT.md)：管理员重新输入自己的密码并填写原因后，Host Agent 为其临时加入只读 ACL 条目，默认 24 小时后自动撤销；审计记录不可在产品界面删除，空间所有者下次登录时看到访问通知。永久转移数据只在删除账号时提供。管理员重置成员密码后，该成员下次登录必须修改密码，且同样收到通知。

## 后果

- issue #9 的两类问题由结构消除：所有写入都以真实用户身份发生并继承同一套 ACL；回收站统一为 `.a-nas-trash/<username>`，Web 与 Samba recycle 使用同一格式。
- 产品服务不能再直接读写文件，需要文件代理与用户工作进程。Go 的 `setuid` 作用于整个进程，无法按 goroutine 切换身份，因此必须使用独立进程。
- 由于只在共享文件夹根目录授权，同一共享文件夹内的改名与移动保持一致权限；跨共享文件夹在 Windows 中表现为复制再删除，新文件继承目标文件夹的 ACL。Web 跨文件夹移动后重新应用目标继承 ACL；后台 ACL 一致性任务修复漂移。
- 未来的搜索、缩略图与 AI 索引需要一个可读全部的索引身份；任何查询结果在返回前都必须以请求者身份做访问检查。
- 文件属主以 UID 记录在数据卷上：重装系统盘后必须按原 UID 恢复账号。Host Agent 把身份表镜像到数据卷的 `.a-nas-identities.json`（仅 root 可读），供恢复使用。
- v1.0.1 实验卷只含可丢弃测试数据，不做迁移；rc.5 让 `a-nas` 与用户共同持有个人空间的过渡 ACL 已由本决定取代。

## 未选择

- 产品服务代为读写、授权留在应用层（rc.5 及更早的做法）：Web 与 SMB 身份分裂，权限不由内核强制，产品服务被攻破即可读写全部数据。
- Samba `vfs_acl_xattr` 保存 NT ACL：只有 Samba 执行，Web 与应用绕过它。
- 产品服务持有 `CAP_DAC_OVERRIDE` 后自行判定：授权重新回到应用层。
- 文件代理信任产品服务声明的用户身份：产品服务被攻破即可冒充任何账号。
- 子目录或单文件 ACL：POSIX ACL 只在创建时继承，SMB 移动会把原权限带到新位置，造成泄露或不可见。
- NFSv4/rich ACL：主线内核的 Btrfs 不支持。

## 关联

- [统一身份与文件授权规格](../specs/unified-identity-and-file-acl.md)
- [ADR 0007：Btrfs、SQLite 与类型化特权边界](0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [存储与文件架构](../architecture/storage-and-files.md)
- [Web 桌面终端规格](../specs/web-terminal.md)
- 实现：[`internal/hostops/linux`](../../internal/hostops/linux/acl.go)（身份、ACL 与查看授权）、[`internal/filebroker`](../../internal/filebroker/server.go)（按用户运行的文件 Worker）、[`internal/files/service.go`](../../internal/files/service.go)
- 部署约束：[Host Agent 在 systemd 沙箱下丢失 CAP_SETUID](../investigations/2026-10-08-host-agent-loses-setuid-under-systemd.md)
