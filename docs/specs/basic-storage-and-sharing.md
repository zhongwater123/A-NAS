# 基础存储与共享

状态：implemented locally；Experimental NAS acceptance pending

## 目标

v1.0.1 让一名管理员和普通成员通过 Web 与 SMB3 对同一份个人或共享文件执行基本管理，并能从回收站或只读手动快照恢复单个文件。所有真实写入只进入用户确认初始化的单盘 Btrfs 数据卷。

## 角色与可见性

- 首次启动只接受一次性初始化码创建首个管理员，不存在默认密码。
- 管理员可创建、重置和禁用成员；普通成员不可调用账号、格式化或共享快照管理操作。
- 每名有效用户拥有一个个人空间，且加入唯一 `Shared` 空间。管理员的普通浏览也不能列出或推断其他成员的个人空间。
- Web 使用 Argon2id 与服务端会话，所有变更请求验证 CSRF；Samba 使用同次输入设置独立凭据。

## 数据卷初始化

1. 只有稳定身份、SATA/NVMe、非系统、非 USB、不可移除、未挂载且不是 swap/md/dm 成员的磁盘可生成计划。
2. 计划公开稳定磁盘 ID、型号、容量、身份指纹、破坏性动作、确认短语、状态与十分钟过期时间；不公开 `/dev/sdX`。
3. 用户必须准确输入带磁盘 ID 后缀的确认短语。执行前 Host Agent 再次枚举并核对身份。
4. 状态依次为 `planned → confirmed → running → succeeded`；失败进入 `failed` 或 `needs_attention`，过期进入 `expired`。成功重复执行不再次格式化；中断后不自动执行。

## 文件、回收站与快照

- Web 支持列出、新建目录、流式上传、Range 下载、重命名、同空间移动、复制和删除。
- 路径穿越、符号链接、设备文件及跨空间隐式移动被拒绝；名称冲突返回 `409`。
- Web 与 Samba 删除均进入隐藏的按空间回收站，默认保留 30 天。个人空间由所有者管理；共享空间仅删除者或管理员恢复和清空。
- 个人空间所有者管理自己的手动快照；共享空间快照仅管理员创建和删除。快照只读且不通过 SMB 暴露。
- 恢复始终复制或移动到指定新位置，不覆盖已有文件，不提供整卷回滚。

## 服务与错误

- `/healthz` 免认证；其他状态和产品接口要求会话。
- Web 只监听 `127.0.0.1`，通过本地 Kiosk 或受控 SSH 隧道访问。
- `401`、`403`、`409`、计划过期、卷离线、空间不足与需人工处理使用统一 JSON 错误结构。
- SMART、温度、容量、占用、文件系统、数据卷角色与不可初始化原因可由磁盘接口读取。

## 验收

- 自动化：Policy 隔离、计划过期/身份变化/拒绝/重启恢复、上传与恢复、Samba 回收站导入、快照、IPC、HTTP/CSRF/OpenAPI 和 React 工作流通过 `make check`。
- 实机：按 [v1.0.1 实机手册](../runbooks/provision-v1.0.1-experimental-storage.md)验证 UUID 重启挂载、Web/Windows SMB 大文件哈希、权限、删除/恢复/快照、SMART 和审计证据。

## 非目标

相册与 AI、NFS、多共享空间、配额、外链、定时快照、外接盘备份、整卷回滚、RAID、Scrub 修复、运行中 SATA 热插拔与自动恢复、局域网 Web、TLS、远程访问、OTA、应用中心及生产数据迁移均不属于 v1.0.1。数据卷离线拒写仍是本版安全要求，但完整热插拔生命周期留待后续实现。

## 关联

- [存储与文件架构](../architecture/storage-and-files.md)
- [ADR 0007](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- [`internal/httpapi/product_test.go`](../../internal/httpapi/product_test.go)
- [`internal/files/service_test.go`](../../internal/files/service_test.go)
