# 0037：可移除模块以独立服务接入，A-NAS 只提供登录、转发与存储

状态：proposed（2026-11-12 起草；第一个模块是[照片打分](../specs/photo-scoring.md)）

项目组要在 NAS 上临时运行照片打分，用完后完整移除，并且只借用 A-NAS 的存储、成员登录与网页桌面（[决定记录](../decisions/2026-11-12-photo-scoring.md)）。家庭服务那样的内置服务要在产品服务、Host Agent、安装器、部署脚本与系统测试中接线约 30 处，移除时都要改回去。我们决定把这类功能做成可移除模块：模块是独立的程序、账号、单元与存储，由自己的脚本安装和移除；A-NAS 本体只提供与具体模块无关的三样东西，没有模块时它们什么也不做。

## 决定

1. **模块的组成。** 模块在本仓库中开发，但不随 release 安装：
   - 程序在 `/opt/a-nas/modules/<id>/`，以固定 UID 的专用系统账号 `a-nas-<id>` 运行。模块 UID 取 31100–31199，与内置服务的 31000 段分开；照片打分是 31100。
   - root 所有的清单 `/etc/a-nas/modules/<id>.json` 写明模块 ID 与 UID；ID 只能是小写字母与数字，2–31 个字符。
   - systemd 的服务单元与套接字单元 `anas-module-<id>.service`、`anas-module-<id>.socket`。
   - 安装与移除脚本 `scripts/modules/<id>/install.sh`、`remove.sh`，在 NAS 上由 root 运行。

   模块代码放在各自的目录中（`cmd/anas-<id>`、`internal/<id>*`、`web/src/<id>/`、`api/paths/<id>.yaml`、`deploy/modules/<id>/`、`scripts/modules/<id>/`），产品服务只在模块登记表中引用它的接口清单。
2. **转发。** 产品服务把 `/api/v1/modules/<id>/…` 转发到 `/run/a-nas-modules/<id>.sock`：
   - 套接字由模块的套接字单元以 root 创建，`0660`，属主是模块账号、属组是 `a-nas-modules`。产品服务加入 `a-nas-modules` 组；模块进程不在这个组中，所以模块之间连不上。
   - 转发前照常检查会话、改密与写操作的 CSRF，只带会话令牌，不带 Cookie（与相册、分享、家庭服务相同，见 [`serviceproxy`](../../internal/serviceproxy/serviceproxy.go)）。
   - 套接字不存在时返回 404 `module_not_installed`，存在但连不上时返回 503 `module_unavailable`。
   - `GET /api/v1/modules` 列出已安装的模块（套接字存在且在登记表中）。
   - 模块的接口与其他接口一样逐条写在 OpenAPI 契约中，由产品服务的路由表按姿态检查。
3. **登录。** Host Agent 在 `/run/a-nas-module-sessions/sessions.sock` 提供会话查询与在用成员列表，与家庭服务的查询相同。只有带 `a-nas-module-sessions` 附加组的进程能连接，这个组只由模块单元的 `SupplementaryGroups=` 授予。模块用产品服务转来的令牌自己向 Host Agent 确认身份，不信任产品服务转述的用户（[ADR 0011](0011-run-the-photo-library-as-a-dedicated-service-identity.md)）。授权（谁是打分人、谁能导出）完全在模块内。
4. **存储。** Host Agent 读取清单，为每个模块在数据卷上建子卷 `module-<id>`，属主模块账号、`0700`，与相册、分享、家庭服务的存储使用同一段代码，漂移时修复。清单出现后 30 秒内建好，之后每 15 分钟检查一次。Host Agent 从不删除模块存储。数据卷离线时模块不可用，不在系统盘暂存。
5. **模块能碰到什么。** 模块单元：
   - 没有网络（`PrivateNetwork=true`，只允许 `AF_UNIX`）；
   - 用 `TemporaryFileSystem=/srv:ro` 隐藏整个数据卷，再只绑定自己的存储（读写）与清单中声明的共享空间文件夹（只读）。
     - 读共享空间靠单元授予的 `a-nas-users` 附加组。这个组对共享空间有写权限，所以只读完全来自只读绑定挂载。
     - 文件夹的 ACL 不变，符合 [ADR 0008](0008-use-unified-linux-identities-and-filesystem-acls.md) 只在空间与共享文件夹根设 ACL 的规则。
   - 不能使用 Docker，不能连接其他服务的套接字，不能调用 Host Agent 的其他操作；
   - 不写入 A-NAS 的活动记录，不进入系统日志页面的白名单。
6. **界面。** 模块的窗口代码编译进网页桌面，只有 `GET /api/v1/modules` 列出该模块时才出现，是否对当前成员显示由模块自己的接口回答。网页桌面不在运行时从模块加载脚本或页面。
7. **移除。** 移除脚本停用并删除单元、套接字、程序、清单与账号。模块存储由管理员按运行手册先导出、再删除。仓库中的模块代码之后用一个提交删除。转发、会话查询与存储的通用代码留在 A-NAS 本体，没有模块时不提供任何接口，也不建任何目录。

## 后果

- 本体多两个系统组：`a-nas-modules`（产品服务）与 `a-nas-module-sessions`（模块）。产品服务的附加组与 Host Agent 的运行目录各多一项，安装器与 `make ops-check` 同步更新。
- 产品服务能连接所有模块的套接字。产品服务失陷时，模块的数据随之暴露；这与它能连接相册、分享与家庭服务相同。
- 模块能列出在用成员（ID、用户名、昵称），用于选择打分人。
- 模块的程序不在 release 中，部署顺序是：先部署包含登记表与窗口的 A-NAS，再运行模块的安装脚本。模块更新也由它自己的脚本完成；接口变化仍要同时部署 A-NAS。
- 模块进程带 `a-nas-users` 组：如果只读挂载被绕过，它能写共享空间。系统测试证明它在共享空间中写不进、看不到其他文件夹。
- 数据卷在模块启动后才挂载时，绑定挂载被跳过，模块检查存储失败后退出，由 systemd 稍后重启，重启时才能拿到挂载。

## 未选择

- **像家庭服务那样内置接线。** 移除时要改回产品服务、Host Agent、安装器、部署与系统测试中约 30 处；用户要求可插拔、能完整移除。
- **上架到应用中心作为容器应用。** 应用网页开在其他端口，只能在局域网打开，互联网入口不转发（[互联网访问规格](../specs/internet-access.md)）；应用不能用成员登录，界面也不在桌面中。
- **运行时从模块加载网页代码。** 等于让模块在桌面的来源中运行任意脚本，能拿到所有成员的会话能做的事。
- **模块信任产品服务转述的身份。** 少一个套接字，但产品服务的一个缺陷就能让模块把请求当作任何成员处理，与相册、分享、家庭服务的做法不一致。
- **模块把导出写进共享空间。** 要给模块某个文件夹的写权限，账号删除后还会留下属主不存在的文件；导出改由管理员下载。

## 关联

- [照片打分规格](../specs/photo-scoring.md)、[技术设计](../architecture/photo-scoring.md)、[安装与移除手册](../runbooks/install-and-remove-photo-scoring.md)
- [ADR 0011：相册以专用服务身份运行](0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[ADR 0022：应用容器只以应用身份接触共享空间](0022-reach-shared-folders-only-as-the-app-identity.md)、[ADR 0029：家庭服务](0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)
- 代码：[`internal/modules`](../../internal/modules/modules.go)、[`internal/serviceproxy`](../../internal/serviceproxy/serviceproxy.go)、[`internal/sessionlookup`](../../internal/sessionlookup/sessionlookup.go)
