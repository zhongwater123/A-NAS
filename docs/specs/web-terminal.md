# Web 桌面终端

状态：implemented（opt-in，默认关闭）
更新时间：2026-10-09

## 目标

设备管理员在 Web 桌面主界面点击“终端”图标即可打开一个窗口化的设备 Shell，用于开发诊断和恢复；本地控制台的 VT 切换尚未可用，这为直连屏幕提供了不依赖 SSH 的命令行入口。

## 非目标

- 不提供 root、`sudo` 或任何超出登录管理员本人 Linux 账号的权限。
- 不实现独立的终端身份体系或 TLS；终端复用产品会话，只接受本机屏幕和局域网 Web 入口转发的连接（[局域网 Web 访问](lan-web-access.md)）。
- 不保存会话、不支持断线重连到同一个 Shell，也不做多标签页。

## 场景

### 从主界面打开终端

- Given：产品服务以 `ANAS_TERMINAL=enabled` 启动，设备管理员已登录。
- When：管理员点击桌面“终端”图标。
- Then：出现“终端”窗口，在新 PTY 中以**该管理员本人的 Linux 账号**（UID 与 `a-nas-users`、`a-nas-admins` 组，[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）启动登录 Shell（`/bin/bash -l`，缺失时 `/bin/sh -l`），起始目录为其个人空间，窗口尺寸变化会同步到 PTY 行列数。终端中的文件权限与该管理员通过 Web 访问一致，不能读取其他成员的个人空间。

### 最小化保留会话

- Given：终端窗口中有正在运行的 Shell。
- When：用户最小化后从任务栏恢复。
- Then：同一 Shell 继续运行，环境变量和前台进程不丢失；关闭窗口则结束 Shell。

### Shell 退出

- Given：终端会话已连接。
- When：Shell 退出或连接断开。
- Then：窗口顶部显示“Shell 已退出（代码 N）”或“终端连接已断开”，并提供“新建会话”。

### 未启用

- Given：未设置 `ANAS_TERMINAL` 或其值为 `disabled`。
- When：用户打开终端窗口。
- Then：窗口说明终端未启用及启用方式，服务端拒绝建立会话。

## 边界与失败

- `GET /api/v1/terminal` 报告是否启用；`GET /api/v1/terminal/session` 升级为 WebSocket，两个端点都要求有效的管理员产品会话，协议见 [OpenAPI](../../api/openapi.yaml)。
- 建立会话前依次拒绝：未登录（401）、不是管理员或未启用（403）、对端不是回环地址（本机屏幕或局域网入口 Caddy）或 `Host` 不是 `localhost`/IP 地址（403，防止绕过入口直连和 DNS rebinding）、非 WebSocket 请求（426）、超过 4 个并发会话（429）、服务正在关闭（503）；浏览器 `Origin` 与 `Host` 不一致时握手被拒绝。
- Shell 由 Host Agent 内的文件代理启动：代理自行校验会话令牌且只为管理员启动，以该账号的 UID/组运行并把 PTY 从设备属主改为该账号（`tty` 组 `0620`），只把 PTY 主端描述符交给产品服务转发数据。产品服务账号不再持有 Shell。
- 同机其他本地进程不受 Origin 约束，但必须持有有效的管理员会话令牌才能获得该管理员的 Shell；功能仍默认关闭，只应在受信任的设备上启用。经局域网 HTTP 使用时，管理员会话与终端输入输出明文传输（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)）。
- Shell 继承 Host Agent 的 systemd 沙箱：`ProtectSystem=strict` 只允许写入数据卷等少数路径，`PrivateTmp`、`ProtectHome` 与 `NoNewPrivileges` 生效。
- 客户端断开、服务关闭、会话失效（注销、过期、账号被禁用或不再是管理员，代理每 30 秒复核）时向 Shell 进程组发送 `SIGHUP`，2 秒后升级为 `SIGKILL`；Shell 已退出并被回收后不再发送信号，因为其 PID（即进程组 ID）可能已被其他进程复用；使用作业控制放入其他进程组的后台任务由 systemd 在 Host Agent 停止时清理。
- 开发模式（无 Host Agent）仍以产品服务用户在本地 PTY 中启动 Shell。
- 产品服务的 HTTP 读写超时不作用于已升级的终端连接。
- Chromium 保留的快捷键（例如 `Ctrl+W`、`Ctrl+T`）不会传入 Shell。

## 验收证据

- 服务端：[终端测试](../../internal/terminal/terminal_test.go) 覆盖启用开关、回环对端与 Host 校验（含经局域网入口以 IP 访问）、跨源拒绝、Shell 执行与退出码、PTY 尺寸、超时后存活、并发上限和优雅关闭；[代理终端测试](../../internal/filebroker/terminal_test.go) 覆盖仅管理员、经传递的 PTY 读写与调整尺寸、断开时挂断（含忽略 `SIGHUP` 的 Shell）和会话失效后结束；[进程组测试](../../internal/terminal/process_linux_test.go) 覆盖已退出的 Shell 不再收到信号、忽略 `SIGHUP` 时升级。
- root：[终端身份测试](../../internal/hostops/linux/root_terminal_integration_test.go) 在特权容器中验证 Shell 的 UID、组、起始目录与 PTY 属主，且读取其他成员的文件被拒绝。
- 契约：[OpenAPI 测试](../../internal/httpapi/openapi_test.go)。
- 前端：[桌面测试](../../web/src/App.test.tsx) 与 [协议测试](../../web/src/terminal.test.ts)。
- 2026-10-07 在 WSL2 中以 `ANAS_TERMINAL=enabled` 运行 `build/anas-api`，经浏览器从图标打开终端，验证命令输出、中文、窗口自适应、最小化后变量保留与退出提示。

## 关联

- 代码：[`internal/terminal`](../../internal/terminal/terminal.go)、[`internal/filebroker`](../../internal/filebroker/terminal.go)、[`cmd/anas-api`](../../cmd/anas-api/main.go)、[`TerminalPanel`](../../web/src/TerminalPanel.tsx)、[`XtermSession`](../../web/src/XtermSession.tsx)
- 规格：[Web 桌面宿主机状态](web-desktop-host-state.md)
- 调查：[本地 Kiosk 浏览器约束](../investigations/2026-10-06-kiosk-browser-confinement.md)
- 架构：[架构总览](../architecture/OVERVIEW.md)
