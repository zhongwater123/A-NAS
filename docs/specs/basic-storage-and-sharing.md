# 基础存储与共享

状态：implemented

## 目标

v1.0.1 让一名管理员和普通成员通过 Web 与 SMB3 对同一份个人或共享文件执行基本管理，并能从回收站或只读手动快照恢复单个文件。所有真实写入只进入用户确认初始化的单盘 Btrfs 数据卷。

## 角色与可见性

- 尚无有效管理员时，本地控制台只要求用户设置账号和密码；首个账号成为 A-NAS 产品管理员。不存在初始化码、默认账号或默认密码，产品管理员也不是 Linux root。
- 设备启用完成后，同一入口不可再次创建管理员；日常 Web 与 SMB 访问使用设备启用时设置的账号密码。
- 管理员可创建、重置和禁用成员；普通成员不可调用账号、格式化或共享快照管理操作。
- 每名有效用户拥有一个个人空间，且加入唯一 `Shared` 空间。管理员的普通浏览也不能列出或推断其他成员的个人空间。
- Web 使用 Argon2id 与服务端会话，所有变更请求验证 CSRF；Samba 使用同次输入设置独立凭据。
- 浏览器会话自登录起固定 12 小时有效，使用中不续期。在设备本机屏幕（Kiosk 以 `?local-console=1` 打开）登录或启用设备时，会话不会自行过期，跨夜和重启后仍保持登录，直到主动退出、管理员重置该账号密码或账号被禁用；本人修改密码不影响。能接触本机屏幕的人即可使用已登录的桌面，离开时应主动退出。
- 服务端只对没有 `Forwarded`/`X-Forwarded-For` 头的直接回环连接授予本机会话，否则按 12 小时会话处理。Kiosk 与 SSH 隧道直接连接回环；局域网浏览器经 Caddy 入口访问，Caddy 总会附加转发头（[局域网 Web 访问](lan-web-access.md)）。Chromium 把 Cookie 寿命上限定为 400 天，页面每次加载读取会话时续签本机会话的 Cookie。
- 会话过期、在别处注销或账号被禁用后，已登录页面的任一请求返回 `401` 即回到登录页并提示“登录已过期，请重新登录”，不显示为连接中断；重新登录即可继续，无需重启或刷新。

## 数据卷初始化

1. 只有稳定身份、SATA/NVMe、非系统、非 USB、不可移除、未挂载且不是 swap/md/dm 成员的磁盘可生成计划。
2. 计划公开稳定磁盘 ID、型号、容量、身份指纹、破坏性动作、确认短语、状态与十分钟过期时间；不公开 `/dev/sdX`。
3. 用户必须准确输入带磁盘 ID 后缀的确认短语。执行前 Host Agent 再次枚举并核对身份。
4. 状态依次为 `planned → confirmed → running → succeeded`；失败进入 `failed` 或 `needs_attention`，过期进入 `expired`。成功重复执行不再次格式化；中断后不自动执行。

## 文件、回收站与快照

- Web 支持列出、新建目录、流式上传、Range 下载、重命名、同空间移动、复制和删除。
- Web 文件管理采用可调宽的目录树、当前目录与只读详情三栏；窗口变窄时先隐藏详情栏，再以空间选择器替代目录树。路径、搜索和成熟图标组成的文件命令位于内容上方，上传入口常驻当前目录右下角。
- 当前目录支持列表与图标视图。列表的名称、修改时间、类型和大小均可排序，文件夹在任何升降序下始终位于非文件夹项目之前；文件夹类型使用独立的暖色标签。
- 用户自己的个人空间根目录提供图库、音乐和电影三个应用资源入口：图库是个人图库的投影并交由相册打开，不暴露受管存储路径；音乐与影视中心在功能实现前显示为不可写的“规划中”入口。应用资源不是普通目录，不参与剪切、复制、重命名或删除。
- 详情栏只显示所选资源的元数据与说明，不承载文件操作按钮；剪切、复制、粘贴、重命名和删除统一从上方命令栏发起。
- 路径穿越、符号链接、设备文件及跨空间隐式移动被拒绝；名称冲突返回 `409`。
- Web 与 Samba 删除均进入隐藏的按空间、按删除者回收站，默认保留 30 天。回收站条目只有删除者能看到、恢复和清空（[统一身份与文件授权](unified-identity-and-file-acl.md)）。
- 个人空间所有者管理自己的手动快照；共享空间快照仅管理员创建和删除。快照只读且不通过 SMB 暴露。
- 恢复始终复制或移动到指定新位置，不覆盖已有文件，不提供整卷回滚。

## 服务与错误

- `/healthz` 免认证；其他状态和产品接口要求会话。
- 产品服务只监听 `127.0.0.1`。本地 Kiosk 与受控 SSH 隧道直接访问；安装 Caddy 后，局域网浏览器经 80 端口以 HTTP 访问（[局域网 Web 访问](lan-web-access.md)）。
- `401`、`403`、`409`、计划过期、卷离线、空间不足与需人工处理使用统一 JSON 错误结构。
- SMART、温度、容量、占用、文件系统、数据卷角色与不可初始化原因可由磁盘接口读取。

## 验收

- 自动化：空间隔离由内核 ACL 执行，见 root 权限矩阵与[系统测试](../development/LOCAL_ENVIRONMENT.md#系统测试)；计划过期/身份变化/拒绝/重启恢复、上传与恢复、Samba 回收站导入、快照、IPC、HTTP/CSRF/OpenAPI 和 React 工作流通过 `make check`。
- 实机：按 [v1.0.1 实机手册](../runbooks/provision-v1.0.1-experimental-storage.md)验证 UUID 重启挂载、Web/Windows SMB 大文件哈希、权限、删除/恢复/快照、SMART 和审计证据。

## 非目标

相册与 AI、NFS、多共享空间、配额、外链、定时快照、外接盘备份、整卷回滚、RAID、Scrub 修复、运行中 SATA 热插拔与自动恢复、局域网 Web、TLS、远程访问、OTA、应用中心及生产数据迁移均不属于 v1.0.1 的验收范围；其中相册、Docker 与应用中心已合入主干并部署在实验 NAS，局域网 Web 以明文 HTTP 过渡实现（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)），均不作为 v1.0.1 发布闸门。数据卷离线拒写仍是本版安全要求，但完整热插拔生命周期留待后续实现。

## 关联

- [存储与文件架构](../architecture/storage-and-files.md)
- [ADR 0007](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- [统一身份与文件授权规格](unified-identity-and-file-acl.md)：取代本规格中的空间权限实现方式
- [`internal/httpapi/product_test.go`](../../internal/httpapi/product_test.go)
- [`internal/files/service_test.go`](../../internal/files/service_test.go)
- [`web/src/App.tsx`](../../web/src/App.tsx)
- [`web/src/App.test.tsx`](../../web/src/App.test.tsx)：文件管理交互、会话过期回到登录页、本机屏幕申请持久会话
- [`TestProductAPIKeepsLocalConsoleSessionsUntilSignOut`](../../internal/httpapi/product_test.go)：本机会话只授予直接回环连接、跨越 12 小时、退出后失效
- [本地控制台会话过期调查](../investigations/2026-10-09-local-console-disconnected-after-session-expiry.md)
