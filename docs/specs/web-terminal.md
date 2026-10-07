# Web 桌面终端

状态：implemented（opt-in，尚未在 Experimental NAS 启用）
更新时间：2026-10-07

## 目标

设备所有者在 Web 桌面主界面点击“终端”图标即可打开一个窗口化的设备 Shell，用于开发诊断和恢复；本地控制台的 VT 切换尚未可用，这为直连屏幕提供了不依赖 SSH 的命令行入口。

## 非目标

- 不提供 root、`sudo` 或任何超出产品服务用户的权限；不经由 Host Agent 执行命令。
- 不实现身份认证、TLS 或局域网直接开放；终端与其余 API 一样只面向回环地址。
- 不保存会话、不支持断线重连到同一个 Shell，也不做多标签页。

## 场景

### 从主界面打开终端

- Given：产品服务以 `ANAS_TERMINAL=enabled` 启动。
- When：用户点击桌面“终端”图标。
- Then：出现“终端”窗口，在新 PTY 中以产品服务用户启动登录 Shell（`$SHELL -l`，缺省 `/bin/bash`），窗口尺寸变化会同步到 PTY 行列数。

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

- `GET /api/v1/terminal` 报告是否启用；`GET /api/v1/terminal/session` 升级为 WebSocket，协议见 [OpenAPI](../../api/openapi.yaml)。
- 建立会话前依次拒绝：未启用（403）、对端不是回环地址或 `Host` 不是回环/`localhost`（403，防止误开放到局域网和 DNS rebinding）、非 WebSocket 请求（426）、超过 4 个并发会话（429）、服务正在关闭（503）；浏览器 `Origin` 与 `Host` 不一致时握手被拒绝。
- 同机其他本地进程不受 Origin 约束，能连接回环端口的本地用户即可获得产品服务用户的 Shell；因此功能默认关闭，只应在受信任的开发设备上启用。
- Shell 继承产品服务的 systemd 沙箱：`ProtectSystem=strict`、`ProtectHome=read-only`、`PrivateTmp` 与 `NoNewPrivileges` 意味着 Experimental NAS 上的 Shell 通常无法写入 home 或系统目录。
- 客户端断开或服务关闭时向 Shell 进程组发送 `SIGHUP`，2 秒后升级为 `SIGKILL`；使用作业控制放入其他进程组的后台任务由 systemd 在服务停止时清理。
- 产品服务的 HTTP 读写超时不作用于已升级的终端连接。
- Chromium 保留的快捷键（例如 `Ctrl+W`、`Ctrl+T`）不会传入 Shell。

## 验收证据

- 服务端：[终端测试](../../internal/terminal/terminal_test.go) 覆盖启用开关、回环与 Host 校验、跨源拒绝、Shell 执行与退出码、PTY 尺寸、超时后存活、并发上限和优雅关闭。
- 契约：[OpenAPI 测试](../../internal/httpapi/openapi_test.go)。
- 前端：[桌面测试](../../web/src/App.test.tsx) 与 [协议测试](../../web/src/terminal.test.ts)。
- 2026-10-07 在 WSL2 中以 `ANAS_TERMINAL=enabled` 运行 `build/anas-api`，经浏览器从图标打开终端，验证命令输出、中文、窗口自适应、最小化后变量保留与退出提示。

## 关联

- 代码：[`internal/terminal`](../../internal/terminal/terminal.go)、[`cmd/anas-api`](../../cmd/anas-api/main.go)、[`TerminalPanel`](../../web/src/TerminalPanel.tsx)、[`XtermSession`](../../web/src/XtermSession.tsx)
- 规格：[Web 桌面宿主机状态](web-desktop-host-state.md)
- 调查：[本地 Kiosk 浏览器约束](../investigations/2026-10-06-kiosk-browser-confinement.md)
- 架构：[架构总览](../architecture/OVERVIEW.md)
