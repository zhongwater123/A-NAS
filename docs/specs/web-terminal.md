# Web 桌面终端

状态：implemented（opt-in，默认关闭）
更新时间：2026-10-21

## 目标

设备管理员在“设置 → 终端”中打开设备 Shell，用于开发诊断和恢复；本地控制台的 VT 切换尚未可用，这为直连屏幕提供了不依赖 SSH 的命令行入口。终端是低频的管理工具，所以收在设置里，不占用桌面。

## 非目标

- 不提供 root、`sudo` 或任何超出登录管理员本人 Linux 账号的权限。
- 不实现独立的终端身份体系或 TLS；终端复用产品会话，只接受本机屏幕和局域网 Web 入口转发的连接（[局域网 Web 访问](lan-web-access.md)）。
- 不在网页中提供开关：开启终端需要设备的 root 修改系统配置，被盗用的管理员会话不能借此为自己打开 Shell。
- 不保存会话、不支持断线重连到同一个 Shell，也不做多标签页。

## 场景

### 从设置打开终端

- Given：设备的 root 已按[运行手册](../runbooks/enable-admin-terminal.md)开启终端，设备管理员已登录。
- When：管理员打开“设置”，在侧栏选择“终端”；在“全部应用”中搜索“终端”或“命令行”会找到“设置”。
- Then：内容区变为占满高度的深色终端。页头写明“以 <账号> 的 Linux 账号在 <主机名> 上运行的 Shell”，并显示连接状态（正在连接、已连接、已结束）和“新建会话”；终端顶栏显示 `<账号>@<主机名>` 与当前行列数。Shell 在新 PTY 中以**该管理员本人的 Linux 账号**（UID 与 `a-nas-users`、`a-nas-admins` 组，[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）启动登录 Shell（`/bin/bash -l`，缺失时 `/bin/sh -l`），起始目录为其个人空间，窗口尺寸变化会同步到 PTY 行列数。终端中的文件权限与该管理员通过 Web 访问一致，不能读取其他成员的个人空间。
- And：桌面、程序坞和“全部应用”中没有单独的“终端”图标；已保存的桌面图标顺序中的旧“终端”记录被忽略。成员的设置侧栏没有“终端”。

### 切换分区与最小化保留会话

- Given：终端中有正在运行的 Shell。
- When：切换到设置的其他分区，或最小化设置窗口后从程序坞恢复，再回到“终端”。
- Then：同一 Shell 继续运行，环境变量和前台进程不丢失，回到“终端”时键盘输入回到 Shell；停留在其他分区时侧栏“终端”旁显示绿色圆点。关闭设置窗口则结束 Shell；再次打开设置会回到“终端”并启动新的 Shell。

### Shell 退出

- Given：终端会话已连接。
- When：Shell 退出或连接断开。
- Then：终端底部浮出“Shell 已退出（代码 N）”或“终端连接已断开”与“新建会话”，页头状态变为“已结束”，已有输出保留在屏幕上。页头的“新建会话”随时可用，会结束当前 Shell 并打开一个新的。

### 未开启

- Given：未设置 `ANAS_TERMINAL` 或其值为 `disabled`。
- When：管理员打开“设置 → 终端”。
- Then：显示“终端尚未开启”，说明终端默认关闭、只能由设备的 root 开启，列出写入 `/etc/a-nas/anas-api.env` 并重启 `anas-api` 的两行命令，并提供“重新检查”；服务端拒绝建立会话。

### 升级保留开启状态

- Given：root 已在 `/etc/a-nas/anas-api.env` 写入 `ANAS_TERMINAL=enabled`。
- When：用系统服务安装器安装新版本，它每次都会重写该文件。
- Then：安装器保留这一行，升级后终端仍然可用。此前的安装器会丢弃它，所以即使手工开启，下一次部署也会让终端回到“未开启”（2026-10-13 发现）。

## 边界与失败

- `GET /api/v1/terminal` 报告是否启用；`GET /api/v1/terminal/session` 升级为 WebSocket，两个端点都要求有效的管理员产品会话，协议见 [OpenAPI](../../api/openapi.yaml)。
- 建立会话前依次拒绝：未登录（401）、不是管理员或未启用（403）、对端不是回环地址（本机屏幕或局域网入口 Caddy）或 `Host` 不是 `localhost`/IP 地址（403，防止绕过入口直连和 DNS rebinding）、非 WebSocket 请求（426）、超过 4 个并发会话（429）、服务正在关闭（503）；浏览器 `Origin` 与 `Host` 不一致时握手被拒绝。
- Shell 由 Host Agent 内的文件代理启动：代理自行校验会话令牌且只为管理员启动，以该账号的 UID/组运行并把 PTY 从设备属主改为该账号（`tty` 组 `0620`），只把 PTY 主端描述符交给产品服务转发数据。产品服务账号不再持有 Shell。
- 同机其他本地进程不受 Origin 约束，但必须持有有效的管理员会话令牌才能获得该管理员的 Shell；功能仍默认关闭，只应在受信任的设备上启用。经局域网 HTTP 使用时，管理员会话与终端输入输出明文传输（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)）。
- Shell 继承 Host Agent 的 systemd 沙箱：`ProtectSystem=strict` 只允许写入数据卷等少数路径，`PrivateTmp`、`ProtectHome` 与 `NoNewPrivileges` 生效；`RestrictAddressFamilies=AF_UNIX` 使 Shell 不能创建 IPv4/IPv6 或 netlink 套接字（“Address family not supported by protocol”），所以 `ping`、`curl`、`ip`、`ss` 等网络诊断不可用，经 D-Bus 的 `systemctl status` 可用（2026-10-13 系统测试中实测）；`RestrictNamespaces` 使 `unshare` 与需要新命名空间的容器工具不可用（[#166](https://github.com/zhongwater123/A-NAS/issues/166)）。
- Shell 不在 Host Agent 的 cgroup 中运行，而在自己的 systemd 临时 scope `anas-terminal-<账号>-<PID>.scope` 中（[#166](https://github.com/zhongwater123/A-NAS/issues/166)）：不占 Host Agent 的内存与任务上限；Shell 里用 `logger`、`systemd-cat` 写入的日志归属这个 scope，即使冒用 `anas-host-agent` 的名字，也不会出现在“日志”的 Host Agent 中。Shell 先由 `anas-host-agent terminal-shell` 等待，进入 scope 后才成为登录 Shell，所以登录脚本也不会在 Host Agent 中执行。systemd 建不成 scope 时记录错误，Shell 照常在 Host Agent 的 cgroup 中启动，因为终端是排障入口。
- 客户端断开、服务关闭、会话失效（注销、过期、账号被禁用或不再是管理员，代理每 30 秒复核）时向 Shell 进程组发送 `SIGHUP`，2 秒后升级为 `SIGKILL`；Shell 已退出并被回收后不再发送信号，因为其 PID（即进程组 ID）可能已被其他进程复用。复核时会话库暂时读不出（如 SQLite 忙）不算失效：连续 4 次（约 2 分钟）都确认不了才挂断；打开终端时读不出则返回暂不可用（503），不当作未登录。
- 使用作业控制放入其他进程组、或脱离终端的后台任务留在 Shell 的 scope 中；scope 绑定 Host Agent 的单元，Host Agent 停止或重启时 systemd 向其中进程发送 `SIGTERM` 与 `SIGHUP`，10 秒后 `SIGKILL`。
- 开发模式（无 Host Agent）仍以产品服务用户在本地 PTY 中启动 Shell。
- 产品服务的 HTTP 读写超时不作用于已升级的终端连接。
- Chromium 保留的快捷键（例如 `Ctrl+W`、`Ctrl+T`）不会传入 Shell。

## 验收证据

- 系统测试：[系统测试](../../scripts/system-test.sh)在 Debian 13 systemd 容器中用真实安装器验证新设备上终端关闭且拒绝会话、按手册开启后管理员经局域网入口获得以本人 UID 运行、起始于个人空间且不能列出成员个人空间的 Shell、退出码经关闭帧到达浏览器、本机屏幕同样可用、成员会话被拒绝、Shell 运行在自己的 scope 中、`logger -t anas-host-agent` 写入的内容不出现在 Host Agent 的日志中、Shell 不能创建命名空间、数据卷只读时终端仍可打开，以及升级保留 `ANAS_TERMINAL=enabled`。
- 服务端：[终端测试](../../internal/terminal/terminal_test.go) 覆盖启用开关、回环对端与 Host 校验（含经局域网入口以 IP 访问）、跨源拒绝、Shell 执行与退出码、PTY 尺寸、超时后存活、并发上限和优雅关闭；[代理终端测试](../../internal/filebroker/terminal_test.go) 覆盖仅管理员、经传递的 PTY 读写与调整尺寸、断开时挂断（含忽略 `SIGHUP` 的 Shell）、会话失效后结束、会话库短暂出错不挂断而持续出错会挂断、读不出会话时暂不可用，以及 Shell 进入 scope 后才执行和 scope 建不成时照常启动；[进程组测试](../../internal/terminal/process_linux_test.go) 覆盖已退出的 Shell 不再收到信号、忽略 `SIGHUP` 时升级。
- root：[终端身份测试](../../internal/hostops/linux/root_terminal_integration_test.go) 在特权容器中验证 Shell 的 UID、组、起始目录与 PTY 属主，且读取其他成员的文件被拒绝。
- 契约：[OpenAPI 测试](../../internal/httpapi/openapi_test.go)。
- 前端：[桌面测试](../../web/src/App.test.tsx) 覆盖桌面上没有终端图标、最小化设置后保留同一 Shell、关闭设置后结束；[设置测试](../../web/src/settings/SettingsPanel.test.tsx) 覆盖从设置打开、切换分区后保留同一 Shell、关闭后重新打开时新建 Shell、退出后新建会话与未开启说明；[协议测试](../../web/src/settings/terminal.test.ts) 覆盖地址、调整尺寸消息与退出码解析。
- 2026-10-07 在 WSL2 中以 `ANAS_TERMINAL=enabled` 运行 `build/anas-api`，经浏览器从图标打开终端，验证命令输出、中文、窗口自适应、最小化后变量保留与退出提示。
- 2026-10-13 在模拟模式下经无头 Chrome 打开“设置 → 终端”，验证已连接状态、命令与中文输出、最大化和窄窗口下的行列自适应、切到“日志”时终端隐藏且侧栏显示运行圆点、切回后仍是同一 Shell、退出后的提示，以及未开启时的说明页。

## 关联

- 代码：[`internal/terminal`](../../internal/terminal/terminal.go)、[`internal/filebroker`](../../internal/filebroker/terminal.go)、[`cmd/anas-api`](../../cmd/anas-api/main.go)、[系统服务安装器](../../scripts/install-v1.0.1-system-services.sh)、[`TerminalSection`](../../web/src/settings/TerminalSection.tsx)、[`XtermSession`](../../web/src/settings/XtermSession.tsx)
- 规格：[设置](settings.md)、[Web 桌面宿主机状态](web-desktop-host-state.md)
- 运行手册：[开启管理员终端](../runbooks/enable-admin-terminal.md)
- 调查：[本地 Kiosk 浏览器约束](../investigations/2026-10-06-kiosk-browser-confinement.md)、[一处故障让 Host Agent 的全部入口失效](../investigations/2026-10-21-host-agent-single-failures-stop-everything.md)
- 架构：[架构总览](../architecture/OVERVIEW.md)
