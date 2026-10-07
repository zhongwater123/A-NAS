# 数据卷空间权限阻断文件闭环

状态：fix in progress；自动化最小回归通过，Experimental NAS 待部署验证

## 症状与影响

Experimental NAS 安装 `v1.0.1-rc.4` 并成功初始化 Btrfs 数据盘后，文件管理中的个人空间与 Shared、回收站和文件快照均返回 `request could not be completed`。数据卷本身在线可写，四个入口共同依赖的空间目录却无法被非特权 Product Service 访问。

代码复核同时确认 [Issue #9](https://github.com/zhongwater123/A-NAS/issues/9) 指出的两个后续风险：个人空间缺少 Web/Samba 共同写入权限；Web 首次创建 `0700` 回收站后，Samba `vfs_recycle` 可能无法写入并静默永久删除文件。修复父目录而不修复这两项，仍不能形成存储闭环。

## 最小复现

1. 在 rc.4 初始化已注册管理员的实验数据卷。
2. 以 Product Service 的实际 Linux 用户执行 `stat` 或 `find`：

   ```bash
   runuser -u a-nas -- stat /srv/a-nas/data/spaces/private/admin
   runuser -u a-nas -- stat /srv/a-nas/data/spaces/shared
   ```

3. 两条命令均返回 `Permission denied`；预期 Product Service 能访问授权空间，但普通成员不能列出个人空间容器。

## 观察事实

- `/dev/sda1` 已作为 Btrfs 挂载到 `/srv/a-nas/data`，UUID 与 `.a-nas-volume.json` 一致。
- `anas-api.service` 以 `a-nas:a-nas` 运行，附加组包含 `a-nas-members`；不是进程缺少预期组。
- `spaces` 为 `root:root 0770`，`spaces/private` 为 `root:root 0750`，共同阻断 `a-nas` 和 Samba 账号。
- `spaces/private/admin` 为 `admin:a-nas 0770`，`spaces/shared` 为 `root:a-nas-members 2770`；叶子目录本身不是当前 `EACCES` 的第一阻断点。
- Host Agent 单元使用 `UMask=0007`；Btrfs `subvolume create` 和 `os.MkdirAll` 创建父目录后，执行器只重新赋权叶子空间。
- rc.4 的个人空间没有 setgid/default ACL；Web 与 Samba 分别以 `a-nas` 和登录用户写入，后续新条目会失去另一方的访问权。
- Web 删除以 `0700` 创建 `.a-nas-trash`，而 Samba recycle 默认也使用 `0700`；两种写入身份不能可靠共享该目录。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 空间父目录属组/模式错误 | 检查 `namei -l`，再以 `a-nas` 执行 `stat/find` | confirmed |
| 卷 UUID 或身份标记不一致 | 对照 mount UUID 与 `.a-nas-volume.json` | rejected |
| Product Service 缺少附加组 | 对照 `id a-nas` 与 `/proc/<pid>/status` | rejected |
| SQLite Catalog 损坏 | 先验证不经过 SQLite 的直接文件系统访问 | 不能解释当前 `EACCES`；非首要原因 |

## 根因

空间 materialize 只设置个人与 Shared 子卷的 owner/mode，没有设置 `spaces` 和 `spaces/private` 两个容器。Host Agent 的 umask 因而把它们留成仅 root 可遍历的目录。

同一权限模型还遗漏了跨身份继承：个人空间没有 default POSIX ACL；回收站也没有由 Host Agent 预建为两端可写的受控目录。即使放开父目录，Web 与 SMB 仍会在新文件和删除路径上互相拒绝。

## 修复与回归证据

- Host Agent 对 `spaces` 与 `spaces/private` 固定使用 `root:a-nas-members 0710`：产品服务和成员只能穿过，不能列出容器。
- 个人空间使用 `username:a-nas 2770` 并设置访问/默认 ACL，使用户名和 `a-nas` 共同读写后代；Samba `[homes]` 启用 ACL 继承。
- Host Agent 为个人与 Shared 预建 `.a-nas-trash`；Samba recycle 与 Web 删除均使用 `0770` 子目录模式。
- Host Agent 启动时对已挂载且带身份标记的卷执行幂等空间对账，并原子刷新 Samba 配置，因此 rc.4 数据卷升级后不需重新格式化。
- 回归测试先在原实现上稳定失败，再在修复后通过：`go test ./internal/hostops/linux ./internal/files -run 'Test(...)' -count=1`。

## 后续工作

- 构建并部署下一个不可变 RC；用 `namei`、`getfacl` 和实际 Web/Windows SMB 双向文件操作验证。
- 必须验证“Web 先删除，再由 SMB 删除”时两个文件都可在 Web 回收站恢复，且 smbd journal 不含 recycle `purging`。
- rc.4 当前空间被父目录完全阻断，没有形成有效用户数据；若未来迁移已有文件的旧权限模型，需要单独设计一次性递归 ACL 迁移，不能在每次启动扫描整卷。

## 关联

- 规格：[基础存储与共享](../specs/basic-storage-and-sharing.md)
- 架构：[存储与文件架构](../architecture/storage-and-files.md)
- ADR：[0007](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- 运行手册：[配置并验收 v1.0.1 实验数据卷](../runbooks/provision-v1.0.1-experimental-storage.md)
- 测试：[`space_permissions_test.go`](../../internal/hostops/linux/space_permissions_test.go)、[`service_test.go`](../../internal/files/service_test.go)
