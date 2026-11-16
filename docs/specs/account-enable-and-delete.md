# 重新启用与删除账号

状态：implemented
更新时间：2026-11-16

## 目标

管理员在[设置](settings.md)的“用户与权限”中，能把禁用的成员账号重新启用，也能把它彻底删除。删除先留 30 天，期间可以撤销；到期后成员的个人空间和私有图库按管理员的选择转给管理员或一并删除，账号名之后可以重新使用。为什么这样定，见[决定记录](../decisions/2026-11-16-account-deletion.md)。

## 非目标

- 删除前导出数据：等 USB 存储（[#25](https://github.com/zhongwater123/A-NAS/issues/25)）或备份（[#27](https://github.com/zhongwater123/A-NAS/issues/27)）后再做，仍在 [#26](https://github.com/zhongwater123/A-NAS/issues/26)。
- 删除管理员账号：设备只有一个管理员，它不能禁用自己，也就不能删除。
- 恢复禁用时撤销的分享。
- 系统更新前备份的旧 `control.db` 中的记录。

## 场景

### 重新启用

- Given：成员 `alice` 已禁用，没有等待删除。
- When：管理员在 `alice` 的菜单中选择“重新启用…”并确认。
- Then：
  - `alice` 能用禁用前的密码登录网页和 SMB；
  - Linux 账号解除锁定，助手单元解除屏蔽，已准备过的助手重新启用入口；
  - 审计记录 `user.enabled`；
  - 家庭频道自动把她算回成员。
- 禁用时撤销的分享不会恢复，确认框写明这一点。
- 需要改密的账号（管理员重置过密码）重新启用后，SMB 与助手仍停用，直到她设置新密码；改密成功时一并恢复。

### 删除

- Given：成员 `alice` 已禁用，没有等待删除。
- When：管理员选择“删除账号…”，为个人空间和私有图库各选“转给我”或“一并删除”，输入自己的密码并确认。
- Then：
  - `alice` 进入“待删除”，列表显示删除日期；30 天内账号、数据都不变，管理员仍可用审计查看模式查看；
  - 审计记录 `user.deletion_scheduled` 和两项选择；
  - 菜单只剩两项查看与“撤销删除…”，重置密码与重新启用都要先撤销删除。
- 只有已禁用的成员能删除，否则返回 `409 account_not_disabled`；已在等待的返回 `409 deletion_pending`。密码错误返回 `403 reauthentication_failed`。

### 撤销删除

- Given：`alice` 在等待删除，还没到期。
- When：管理员选择“撤销删除…”。
- Then：删除取消，账号仍是禁用，数据原样；审计记录 `user.deletion_cancelled`。到期执行开始后不能再撤销，返回 `409 deletion_started`。

### 到期执行

- Given：`alice` 的 30 天已到。
- When：产品服务的删除任务发现它（启动时一次，之后至迟每小时一次，申请或撤销删除时也会唤醒）。
- Then：先记下开始时间，从此不可撤销，然后依次执行：
  1. **相册**：选了转给管理员时，`alice` 私有图库中不在回收站的照片复制到管理员的私有图库，原图字节共用，保留用户标签与纠错，并放进新相册“alice 的照片”（重名时加序号）；人物、文件夹与相册不随迁。随后无论哪种选择，删除她的私有图库及其回收站、相册、人物、文字修订和她的相册审计；不再被任何照片使用的原图一并删除。
  2. **文件**：删除她个人空间的全部快照，再清掉这个空间的文件目录、快照计划，以及她在所有空间的回收站记录。
  3. **宿主机**（Host Agent）：
     - 停止并屏蔽助手，断开 SMB；
     - 结束对她个人空间的查看授权；
     - 把她移出身份登记与空间登记，此后对账不会再建她的目录；
     - 选了转给管理员时，把个人空间（不含回收站）和 `Shared/Agent空间/alice` 以 reflink 复制到管理员个人空间的“alice 的文件”（重名时加序号），属主改为管理员，权限与管理员自己建的文件相同，文件 `0660`、文件夹 `0770`；
     - 删除她的个人空间子卷、`Shared/Agent空间/alice` 与 `Shared/.a-nas-trash/alice`；
     - 删除助手的数据子卷、修改记录、令牌、成员配置和运行目录，并解除单元屏蔽；
     - 删除 Samba 账号、Linux 账号与同名私有组。
  4. **其他服务**：
     - 分享：删除她发出的分享及其内容与对话，把她从别人分享的收件人中移除；
     - 家庭：移除她的成员资格、加入申请与只给她看的审计，她发的内容和留言保留；
     - 音乐、影视中心、传输：删除她的收藏、播放记录、歌单、个人空间中的媒体库与传输记录。
  5. **账号**：在一个事务中删除她的账号、会话、头像、通知、查看授权记录与个人空间记录，审计记录 `user.deleted`。选了转移时，给管理员一条通知，写明文件夹与相册的名字。
- 她的 UID 保留，永不分配给别人；账号名从此可以再用，新账号拿到新 UID，看不到她的任何东西。
- 留下的：她在 Shared 中建的文件（属主仍是她的 UID，其他成员照常读写）、共享图库中她上传的照片（由管理员管理）、家庭频道的内容与留言（显示原名字）、问题反馈、活动日志（显示为“已删除的账号”）。

## 边界与失败

- 每一步遇到已不存在的东西都当作已完成，失败后从失败的那一步重试，15 分钟后自动再试；列表显示“删除未完成，将自动重试”。
- 数据卷离线、相册或分享服务不可用、正在拍快照时，这一轮失败，下次再试。
- 转给管理员的步骤只在申请删除的那位管理员仍是有效管理员时执行；否则失败并重试。
- 相册、分享与家庭服务只按 Host Agent 的回答行事：产品服务的请求只告诉它们去检查哪个账号，Host Agent 从控制面确认删除已开始，再告诉它们怎么处理（[ADR 0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)）。
- 转移在一个只有 root 能进入的临时目录中完成，删除原件后才改名为管理员看到的文件夹；中途失败重试时，要么从原件重新复制，要么直接使用已完成的副本。
- 删除开始后，身份同步不再为她建 Linux 账号，重置密码与审计查看都被拒绝。
- 页面上按账号 ID 查人：内容记下了作者 ID 时，查不到就只显示记下的名字，不会把已删除账号的内容算到之后同名的新账号头上。

## 验收证据

- 单元测试：账号服务的启用、申请、撤销与到期执行（[`deletion_test.go`](../../internal/accounts/deletion_test.go)）；Host Agent 的启用与删除命令（[`account_test.go`](../../internal/hostops/linux/account_test.go)）；相册的转移与清除（[`account_test.go`](../../internal/photos/account_test.go)）；分享、家庭、文件、音乐、影视、传输的清除；设置页的菜单与对话框（[`SettingsPanel.test.tsx`](../../web/src/settings/SettingsPanel.test.tsx)）。
- root 集成测试：真实 Btrfs 与 Samba 下的转移与删除（[`root_account_integration_test.go`](../../internal/hostops/linux/root_account_integration_test.go)）。
- 系统测试：禁用后重新启用、申请并撤销删除、缩短期限后的到期执行与同名重建（[`scripts/system-test.sh`](../../scripts/system-test.sh)）。

## 关联

- 决定：[账号删除的做法](../decisions/2026-11-16-account-deletion.md)
- 规格：[统一身份与文件授权](unified-identity-and-file-acl.md)、[相册](photo-library.md)、[成员文件分享](member-sharing.md)、[家庭中心](family-center.md)
- ADR：[0008 统一 Linux 身份与文件系统 ACL](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)、[0024 每个成员一个沙箱中的 Hermes](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
- 代码：[`lifecycle.go`](../../internal/accounts/lifecycle.go)、[`deletion.go`](../../internal/accounts/deletion.go)、[`account.go`](../../internal/hostops/linux/account.go)、[`accountremoval`](../../internal/accountremoval/accountremoval.go)、[`deletion.go`](../../cmd/anas-api/deletion.go)、[`UsersSection.tsx`](../../web/src/settings/UsersSection.tsx)
- 问题：[#26](https://github.com/zhongwater123/A-NAS/issues/26)
