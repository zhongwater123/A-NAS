# Agent空间 保留目录未受保护，链接扩大 Landlock 可写范围

状态：open；修复、回归、完整门禁和上传前审查已通过，实机验收尚未执行。
更新时间：2026-10-28

## 症状与复现

[issue #177](https://github.com/zhongwater123/A-NAS/issues/177) 指出 ADR 0024 决定 5 的实现缺口。成员可以通过 Web 改名、移走或删除 `Shared/Agent空间` 及其账号目录；Host Agent 的目录核对只覆盖空间与回收站。另一条路径是把助手可写位置或其祖先替换成符号链接，Landlock 建规则时会跟随到不应授权的位置。

最小回归使用普通 Linux 开发用户，命令如下：

```bash
go test ./internal/files ./internal/filebroker -run 'TestAgentSpacePolicyDirectoriesCannotBeMovedOrDeleted|TestConfinedWorkerDoesNotFollowWritableDirectoryLinks' -count=1
```

修复前，6 个保留目录的 Web 操作均返回成功；真实受 Landlock 约束的 Worker 在两种链接替换后均能创建 `unauthorized` 目录。另在可丢弃特权 Debian 容器运行 `TestRootPermissionMatrix`，确认 Host Agent 完成账号同步后根本没有 `Agent空间`。

## 根因与处理

1. **文件服务只校验普通名称，没有保留目录策略。** 现在只保护 Shared 中的管理位置：根目录及直接账号子目录不能经 Web 改名、移动、删除或以新建、上传、复制、恢复冒名。个人空间的同名目录是普通用户数据，账号目录中的内容仍可按 ACL 编辑。
2. **Landlock 库按路径重新打开授权位置。** Worker 先用 `openat2` 的 `RESOLVE_NO_SYMLINKS`、`RESOLVE_BENEATH` 打开并持有目录，再把 `/proc/self/fd` 路径交给已有 go-landlock 库，直到规则生效才关闭描述符。未创建的授权目录可以略过；链接、越界或其他打开错误使 Worker 拒绝启动。不采用先检查路径、再由库重开的办法，避免检查与使用之间替换链接。
3. **Host Agent 未接入管理目录核对。** 在既有启动、身份同步和 15 分钟核对中创建与维护这些目录；非 root 所有的冒名目录、链接或其他类型移到同级 `.displaced-*`，保留内容并重建正确目录。ACL 操作继续使用已打开的描述符，不跟随链接目标、不递归接管内容。助手试用安装复用这些目录，不再把它们当作残留或在移除试用时删除。

SMB 采用 issue 允许的 veto 方案：Shared 中名为 `Agent空间` 的条目及其内容不通过 SMB 访问，使用 Web 或助手读取发布内容。Samba 的规则匹配名称，因此也适用于 Shared 子目录里的同名条目；个人空间不添加这个 veto。没有给整个 Shared 根目录加粘滞位，普通文件的跨成员协作仍沿用原 ACL。具体产品规则见 [ADR 0024 决定 5](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md#决定)。

本地终端或已授权应用仍可能改名 Shared 顶层目录，Host Agent 在下一次核对修复，修复前的可用性不是即时保证。链接不会扩大新 Worker 的 Landlock 授权；移动到旁边的内容不会自动删除。管理员查看调查内容后再处理，不应把 `.displaced-*` 自动搬回管理目录。

## 验证与后续

- 新增失败复现修复后通过；全部文件服务、文件代理、Host Agent 普通测试通过。
- 真实 Btrfs/Samba 权限矩阵通过：管理目录归 root、本人发布/他人只读、SMB 改名拒绝、冒名内容保留在旁边、链接目标 ACL 不变、修复后账号目录重建；原有普通 Shared 跨成员删除测试仍通过。
- 系统测试改为验证真实 Host Agent 修复，替代 spike 脚本模拟；Debian 13/systemd 可丢弃容器测试通过，334 PASS、0 FAIL。
- `make check-steps` 全部目标通过，`make race-test` 通过。审查发现目录上传会更换管理目录 ID，新增 `TestFolderUploadThroughAgentSpacePreservesDirectoryIDs` 验证原实现失败、修复后通过；最终提交重跑格式、文档、运维、vet、Go lint、全部 Go 测试与二进制构建通过，两项复审无剩余发现。
- 部署后的 Windows SMB 与旧目录恢复核对见[实机验收](../runbooks/accept-features-on-experimental-nas.md#文件管理登录与账号)。

## 关联

- [文件服务保留名回归](../../internal/files/agent_space_test.go)
- [Landlock 实现](../../internal/filebroker/confine_linux.go)与[Worker 回归](../../internal/filebroker/changes_test.go)
- [目录核对](../../internal/hostops/linux/acl.go)与[真实权限矩阵](../../internal/hostops/linux/root_matrix_integration_test.go)
- [助手规格](../specs/ai-assistant.md)、[存储架构](../architecture/storage-and-files.md)、[助手试用手册](../runbooks/try-ai-assistant-on-experimental-nas.md)
- [Samba 官方配置文档](https://www.samba.org/samba/docs/current/man-html/smb.conf.5)：veto files 的访问范围。
