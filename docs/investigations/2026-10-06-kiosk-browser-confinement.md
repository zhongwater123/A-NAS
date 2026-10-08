# 本地 Kiosk 可进入通用 Chromium 界面

状态：open；禁止保存密码的 Chromium policy 已随 rc.4 及之后的版本部署，设备上的提示检查与完整浏览器约束仍待验收
更新时间：2026-10-07

## 症状与影响

Experimental NAS 的直连屏幕能够显示和操作 A-NAS 桌面，但长按 `F1` 会进入 Chromium 自身的帮助或通用浏览器界面；在该界面反复使用浏览器缩放后，显示会话可能变得不可用。`Ctrl+Alt+F2` 与 `Ctrl+Alt+F1` 也未能完成预期的 VT 切换与返回。

这属于本地控制台产品边界逃逸，不等同于 Chromium 沙箱逃逸、Linux 提权或 root 权限获得。当前运行身份仍为无 `sudo` 权限的 `anas-dev`，但通用浏览器可带来外部导航、局域网访问、文件选择、下载、持久状态污染和本地拒绝服务风险。

本问题由设备所有者标记为开发基线后的待处理项。它不阻塞只读 API、Host Agent、部署回滚或远程隧道继续开发，但在面向非受信任本地用户或消费级发布前必须关闭。

## 最小复现

1. 在 Experimental NAS 上启动 `anas-kiosk@tty1.service`，确认 A-NAS 桌面可见。
2. 长按实体键盘 `F1`。
3. 实际结果：出现 Chromium 自身界面，可使用搜索或其他浏览器功能；反复操作缩放可使会话不可用。
4. 期望结果：输入始终被限制在 A-NAS 产品界面；浏览器导航、帮助、开发工具和不受控缩放不可达，异常操作后仍可恢复。
5. 补充复现：`Ctrl+Alt+F2`/`Ctrl+Alt+F1` 未切换到恢复终端并返回桌面。

当前最终回归仍需要实体键盘和屏幕。浏览器外壳不属于页面 DOM，现有 Playwright 测试不能证明浏览器级快捷键已被阻断。

## 观察事实

- 鼠标可以打开资源管理、拖动窗口并执行窗口操作。
- Chromium 154 以 `anas-dev` 运行，实际参数包含 `--kiosk`、`--app=http://127.0.0.1:8080/` 和持久 `--user-data-dir=/home/anas-dev/.local/state/a-nas/chromium`；不存在 `--no-sandbox`。
- `/etc/chromium/policies/managed` 没有托管策略。
- `v1.0.1-rc.3` 首次创建管理员后出现 Chromium“记住密码”提示；`--password-store=basic` 只选择凭据后端，不会关闭密码管理功能。
- `anas-kiosk@tty1.service` 没有 `IPAddressDeny=`、`IPAddressAllow=` 或地址族白名单，不能在服务边界证明 Chromium 仅访问回环地址。
- Chromium profile 实测约 82 MB。只读检查未捕获已持久化的 per-host zoom 项，仍不足以区分窗口洪泛、瞬时缩放状态和资源耗尽。
- Kiosk journal 未出现 OOM、GPU crash、segfault 或被内核杀死的证据，因此不能把“卡死”归因于内核、Cage 或 GPU 崩溃。
- Cage 的单应用模型允许同一应用产生多个窗口；Chromium 的 Linux `--kiosk` 也不是 ChromeOS 的设备级 Kiosk 模式。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| `F1` 由 Chromium 浏览器级快捷键处理，并在 Cage 内显示新的浏览器窗口 | 检查实际进程参数、Cage 多窗口模型并在实体屏幕复现 | supported |
| 缺少 URL 托管策略和服务级网络限制，使逃逸窗口仍具备通用浏览能力 | 检查 `/etc/chromium/policies/managed` 与 systemd 生效属性 | confirmed |
| 持久 profile 保存异常缩放或窗口状态并导致重启后复发 | 对比清洁临时 profile 与当前 profile，并采集缩放前后差异 | pending |
| Cage、GPU 或内核在缩放时崩溃 | 对齐复现时间戳检查 journal、进程和 OOM 信息 | not observed |
| `cage -s`、PAM/logind 会话或 tty 所有权配置不足导致 VT 切换失败 | 检查 seat/session/VT 属性，并对照最小 Cage 会话测试 | pending |

## 根因

尚未最终闭合。现有证据表明，根本边界错误是把“Cage 单应用全屏 + Chromium `--kiosk`”当作完整浏览器约束；当前实现没有同时建立 URL、网络、浏览器功能、profile 生命周期和输入快捷键等纵深防线。

## 修复与回归证据

- `v1.0.1-rc.4` 增加 root 所有的 Chromium mandatory policy，设置 `PasswordManagerEnabled=false`、`PasswordManagerPasskeysEnabled=false` 和 `SyncDisabled=true`；系统安装脚本把它放到 `/etc/chromium/policies/managed/a-nas.json` 并重启活动 Kiosk。Chromium 官方说明 Linux Chromium 从该目录读取托管 JSON，且托管文件只能由管理员写入；`PasswordManagerEnabled=false` 禁止保存新密码。参见 [Linux Quick Start](https://www.chromium.org/administrators/linux-quick-start/) 与 [Password Manager Enabled](https://chromeenterprise.google/policies/password-manager-enabled/)。
- 登录和创建账号表单也使用正确的 `autocomplete` 语义，但它只是网页提示，不能替代浏览器托管策略。
- 密码提示的实机回归待 rc.4 安装后完成；URL、网络、快捷键和 profile 生命周期的完整收敛仍保持开放，不能把本次修复描述成 Kiosk 已完全加固。
- 自动化门禁待增加：托管策略内容、服务网络限制、临时 profile 和启动参数的静态/实机检查。
- 实体回归待增加：`F1`、`F11`、`F12`、`Ctrl+L`、`Ctrl+O`、`Ctrl++`、`Ctrl+-`、右键菜单、长按以及 VT 恢复。
- 卡死时的当前恢复方式：通过 root SSH 执行 `systemctl restart anas-kiosk@tty1.service`；这不会停止 API 或 Host Agent。

## 后续工作

1. 先构造可重复的实体 HITL 回归，并区分“浏览器窗口洪泛”“缩放状态不可用”和“进程失去响应”。
2. 用 root 管理的 Chromium mandatory policy 默认阻止所有 URL，仅允许 `http://127.0.0.1:8080/*`，并关闭 DevTools 等入口。
3. 在 systemd 层只允许 Kiosk 使用回环网络，并验证 Debian 13 的实际 enforcement。
4. 将 Chromium profile 改为可丢弃运行时状态，使服务重启能够恢复干净界面。
5. 验证浏览器级快捷键能否可靠屏蔽；若不能，评估更窄的 Kiosk WebView 或输入层过滤。
6. 独立修复并验收 VT 切换，不用它替代 SSH 恢复路径。

## 关联

- 规格：[Web 桌面宿主机状态](../specs/web-desktop-host-state.md)
- ADR：[本地控制台使用单应用 Wayland Kiosk](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- 运行手册：[运行实验 NAS 本地控制台](../runbooks/operate-local-kiosk.md)
- 发布记录：[v1.0.0](../releases/v1.0.0.md)
