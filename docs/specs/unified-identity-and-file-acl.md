# 统一身份与文件授权

状态：implemented（ADR 0008，[#11–#16](https://github.com/zhongwater123/A-NAS/issues?q=is%3Aissue+%5BADR+0008%5D)）；实机验收进度见[当前状态](../status/CURRENT.md)
更新时间：2026-10-08

## 目标

无论通过 Web、SMB、Web 终端还是应用容器访问数据卷，用户看到的权限一致，且由内核按文件系统 ACL 判定；管理员也无法在不留痕迹的情况下读取成员的个人空间。

## 非目标

- 子目录或单个文件的自定义权限、拒绝（deny）条目、继承中断。
- 在 Windows 资源管理器中查看或编辑 ACL。
- 多管理员、管理员找回、域或 LDAP 账号。
- 迁移 v1.0.1 实验数据卷。

## 场景

### 账号对应 Linux 身份

- Given：管理员创建成员 `alice`。
- When：Host Agent 同步账号。
- Then：`alice` 获得 20100–29999 内新分配且从未使用过的 UID，同名私有组使用相同 GID，属于 `a-nas-users`（管理员另属 `a-nas-admins`）。若系统中已有同名账号或组（如 `root`、`anas-dev`、`a-nas`），或该 UID 已被其他账号占用，创建失败、不修改既有账号，并允许改用其他用户名重试；冲突占用的 UID 不再分配。

### Web 与 SMB 一致读写个人空间

- Given：`alice` 的个人空间只授予 `alice`。
- When：`alice` 先在 Web 上传 `a.txt`、再从 Windows 修改它，并在 Windows 新建 `docs/b.txt` 后从 Web 下载。
- Then：两边都成功；`a.txt` 与 `docs/b.txt` 的属主均为 `alice`，ACL 均继承自个人空间根目录。

### 其他成员与管理员不可见

- Given：`bob` 是成员、`admin` 是设备管理员。
- When：两人通过 Web、SMB 或终端尝试列出或打开 `alice` 的个人空间。
- Then：内核拒绝访问；Web 不显示该空间，SMB 隐藏不可读内容。

### 共享文件夹

- Given：共享文件夹 `Shared` 对 `a-nas-users` 授予读写，共享文件夹 `Media` 对所有人可读、只授予 `alice` 写。
- When：`bob` 在两个文件夹中读取与写入。
- Then：`bob` 可读两者，只能写 `Shared`；`bob` 在 `Shared` 中新建的文件属主为 `bob`，其他成员按继承 ACL 可读写。
- 说明：产品目前只提供一个 `Shared`。只读共享文件夹已由 Host Agent 的 ACL 布局支持并在 root 集成测试中覆盖，产品中尚无创建入口。

### 删除与回收

- Given：任一空间中的文件。
- When：用户先从 Web 删除一个文件、再从 SMB 删除另一个文件（或顺序相反）。
- Then：两者都以用户身份移入 `.a-nas-trash/<username>/…`，都出现在 Web 回收站并可恢复；Samba 日志没有 `purging`。

### 管理员查看模式

- Given：成员 `alice` 离开家庭，管理员需要找回其文件。
- When：管理员在账号管理中对 `alice` 选择“查看个人空间”，重新输入自己的密码并填写原因。
- Then：管理员获得该空间的只读访问，默认 24 小时后自动撤销（Host Agent 重启后仍按时撤销），也可提前结束；审计记录访问者、时间、空间、原因与到期时间，且不能在产品界面删除；`alice` 下次登录时看到访问通知。管理员不能借此写入、改名或删除 `alice` 的文件；查看期间 `alice` 新建的文件同样可读。
- 只读访问以管理员本人身份、由 ACL 授予，因此 Web 文件管理与终端中一致；SMB 的个人空间共享只对所有者开放，不提供查看。撤销后不残留任何 ACL 条目，也不改变文件原有的 ACL mask。
- 查看成员私有图库是另一种范围的授权：流程相同，但由相册服务的 Policy 执行，不改动 ACL，也不顺带开放个人空间，见 [ADR 0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)。
- 数据卷离线时开启查看返回 `423 volume_unavailable`，不记录授权。授予中途失败（例如遇到不可变文件）时，Host Agent 撤销已加入的条目并让该授权立即过期，撤销也失败时由每分钟的到期检查继续重试；产品服务结束该授权并再次请求撤销，不写审计、不通知所有者。

### 重置成员密码

- Given：管理员重置 `alice` 的密码。
- When：任何人使用新密码首次登录。
- Then：必须先设置新密码才能继续，期间除会话、改密与通知外的接口返回 `password_change_required`，SMB 凭据保持禁用；改密后以新密码重新启用 SMB。`alice` 收到“管理员重置过你的密码”的通知，审计记录该重置。管理员重置自己的密码不触发强制改密。

### 终端与应用

- Given：管理员打开 Web 终端；应用 `memos` 已安装并被授予 `Shared` 写权限。
- When：在终端中访问各空间；应用写入 `Shared`。
- Then：终端以管理员本人身份运行，权限与该管理员通过 Web 访问一致；应用以 `app-memos` 写入，文件属主为 `app-memos`，其他成员按 `Shared` 的 ACL 访问。

## 边界与失败

- 文件代理只接受有效会话令牌，自行查询会话存储判定身份；令牌过期、账号被禁用或会话被撤销后，下一次请求在到达工作进程前即被拒绝。空闲的工作进程 5 分钟后退出；运行中的终端每 30 秒复核会话，失效即断开。
- 文件代理依赖 Host Agent 能切换到用户身份。不能切换时 Web 文件与终端不可用，Host Agent 的其余功能照常运行，安装器拒绝这样的部署（见[存储与文件架构](../architecture/storage-and-files.md#文件代理)）。
- 产品服务账号 `a-nas` 不在任何空间 ACL 中；产品服务被攻破时，只能以已登录且未过期的会话身份操作。
- 回收站中的条目只有删除者能看到、恢复或彻底删除；管理员也不能处理其他成员在共享文件夹中删除的文件。
- 后台目录对账与回收站到期清除需要用户的有效会话：长期未登录用户的 SMB 删除与到期清除在其下次登录时处理。
- 永久转移数据只在删除账号时提供；产品尚无删除账号功能，因此当前不提供转移。
- 只在空间与共享文件夹根目录设置 ACL；Web 只在同一空间内移动，跨空间使用复制，新文件继承目标根目录的默认 ACL；后台一致性任务修复漂移并记录修复事件。
- 数据卷离线时文件代理拒绝所有文件操作，不在系统盘创建替代目录。
- UID 永不复用；删除账号后其 UID 仍保留。
- 禁用账号会禁用 Samba 凭据并断开其已建立的 SMB 会话；管理员重置已禁用账号的密码不会重新启用该账号。

## 验收证据

- 自动化：在具备 root 的容器或 CI 环境中，对 {Web, SMB, 终端, 应用} × {个人空间, 可写共享, 只读共享} × {读, 写, 改名, 删除进回收站, 恢复} 运行权限矩阵测试，并覆盖 Web/SMB 两种删除顺序。当前覆盖见 [`root_matrix_integration_test.go`](../../internal/hostops/linux/root_matrix_integration_test.go)：真实 Btrfs 与 Samba 下的 SMB、本地进程（终端入口）与经文件代理的 Web（真实的按用户工作进程），并验证产品服务账号无权访问；应用身份见 [`root_apps_integration_test.go`](../../internal/hostops/linux/root_apps_integration_test.go)。
- 应用的共享空间授权以 `app-<id>` 加入 `a-nas-users` 实现，即与成员相同的共享空间 ACL；应用数据位于数据卷 `apps/<id>`（仅 root 可列出）。rootful Docker 中以 root 运行的镜像不受 ACL 约束，隔离依赖只挂载被允许的文件夹：这些文件夹以 A-NAS 卷的子路径挂载，Docker 每次启动在卷内解析，应用或成员把其中的文件夹换成链接也无法让容器挂载卷外路径。
- 安全：产品服务进程直接访问任何空间均失败；伪造或过期的会话令牌无法驱动文件代理；系统账号同名被拒绝。
- 部署级：[系统测试](../development/LOCAL_ENVIRONMENT.md#系统测试)在以 systemd 为 PID 1 的 Debian 13 容器中用真实安装器、unit 与二进制，按用户检查 Web、SMB、相册与服务身份，CI 每次推送运行。
- 实机：按[实机配置手册](../runbooks/provision-v1.0.1-experimental-storage.md)收集 `getfacl`、`stat`、Samba 日志与审计证据；进度见[当前状态](../status/CURRENT.md)。

## 关联

- ADR：[0008 统一 Linux 身份与文件系统 ACL](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)、[0007 类型化特权边界](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- 规格：[基础存储与共享](basic-storage-and-sharing.md)、[Web 桌面终端](web-terminal.md)
- 问题：[issue #9](https://github.com/zhongwater123/A-NAS/issues/9)
