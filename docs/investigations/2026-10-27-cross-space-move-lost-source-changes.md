# 跨空间移动丢失复制期间到达的内容与修改时间

状态：open；修复与自动化回归已实现，待审查、合并及实机验收。
更新时间：2026-10-27

## 症状与影响

Alice 把 Shared/Trip 移到个人空间时，Bob 经 SMB 在源目录新增或改写的文件可能未进入目标副本，随后又被永久删除。复制还会将文件和目录的修改时间重置为当前时间，影响按大小与修改时间识别快照版本。

## 最小复现与观察

`TestCrossSpaceMoveKeepsChangesMadeAfterTheCopy` 在真实文件系统的复制完成、目标发布之前暂停，通过直接写文件模拟不持有 Product Service 锁的 SMB 写入：新增 `late.txt`，改写 `notes.txt`，然后让移动继续。

- 原实现返回成功，目标只含旧内容，源也没有回收项；新增内容与较新版本无法恢复。
- 修改后能从操作者回收站恢复目录，两个文件均保留复制后写入的内容。
- `TestCopyPreservesFileAndDirectoryModificationTimes` 将源文件和目录时间设为 `2020-01-02T03:04:05.123456789Z`。原实现复制后的时间为当前时间；修改后纳秒级时间相等。

复现命令（Linux，普通开发用户）：

```bash
go test ./internal/files -run 'TestCrossSpaceMove|TestCopyPreservesFileAndDirectoryModificationTimes' -count=1
```

## 根因与修复

跨空间移动以复制替代跨 Btrfs 子卷的改名，但之前复制后直接 `RemoveAll` 源路径；服务内锁不能阻止 SMB 写入，也不能证明整个目录已被复制。现在复用现有删除流程，把源条目原子改名进操作者回收站，沿用目录 Catalog 更新、回滚与恢复机制。回收失败时返回错误，保留源及已完成的目标副本。

复制代码之前只复制字节。现在从打开的源描述符取得修改时间，并在完成写入后通过目标描述符设置时间；目录在复制完子项后设置。目标权限仍按新文件继承目标空间 ACL。文件打开后再次检查普通文件类型，避免路径被换成其他类型后误操作。

这不提供目录树的一致性快照：并发写入时，目标可能包含旧版本或复制期间读到的版本，回收站保留的是改名时的源内容。其保留期限与清空行为遵循[文件规格](../specs/basic-storage-and-sharing.md)；源副本暂时继续占用数据卷空间，移动不保证立即释放源空间。

## 回归与后续

- `cross_space_move_test.go` 覆盖并发新增/改写后的恢复、回收失败保留两份、移动后的修改时间。
- `filesystem_test.go` 覆盖文件与目录的纳秒修改时间及目标权限继承；文件代理测试验证复制时间通过 Worker 保留。
- 实机部署后按[验收手册](../runbooks/accept-features-on-experimental-nas.md#文件管理登录与账号)确认 Windows SMB 并发写入后的恢复。

## 关联

- [issue #180](https://github.com/zhongwater123/A-NAS/issues/180)
- [文件服务](../../internal/files/service.go)、[描述符内复制](../../internal/files/rootfs_linux.go)
- [跨空间回归](../../internal/files/cross_space_move_test.go)、[文件系统回归](../../internal/files/filesystem_test.go)、[文件代理回归](../../internal/filebroker/broker_test.go)
- [存储与文件架构](../architecture/storage-and-files.md)、[文件规格](../specs/basic-storage-and-sharing.md)

## 审查补充

跨空间移动回收源内容时只记移动审计，不另记用户删除。界面在跨空间粘贴前提示原空间回收站仍占容量。普通文件复制前后比较大小、mtime 和 ctime；发生变化则拒绝副本并清理目标文件，不给变化中的副本盖旧时间。该检查不提供文件树一致性快照，也不能阻止检查完成后的写入。
