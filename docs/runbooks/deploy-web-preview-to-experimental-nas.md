# 部署 Web 桌面预览到实验 NAS

状态：superseded（2026-10-08）
更新时间：2026-10-09

> ADR 0008 之后 A-NAS 以 root 管理的系统服务运行（`/etc/systemd/system`、`/opt/a-nas/current`）。本手册的激活、诊断与回滚步骤只适用于旧的 `anas-dev` 用户级部署，**不得再执行**：用户级 Host Agent 会因不是 root 而退出，用户级 `anas-api` 会与系统服务争用 8080 端口。仍然有效的只有“远程浏览与验证”中的 SSH 隧道，以及 `deploy-dev.ps1 -StageOnly` 暂存；部署、诊断与回滚见[实机配置手册](provision-v1.0.1-experimental-storage.md#安装系统服务)。

## 目的

把本机通过检查的 `anas-api` 与 `anas-host-agent` 作为带哈希的版本化制品部署到 Debian 13 Experimental NAS，由 `anas-dev` 用户级 systemd 常驻。设备直连屏幕通过 Cage/Chromium 使用同一 Web 桌面；限定 SSH 本地端口转发仅作为远程开发和诊断入口。本文不安装开发工具、不开放局域网端口、不修改磁盘。

## 前提与停止条件

- 本机工作树必须干净且 HEAD 已提交；NAS 地址只通过运行时参数传入。
- Windows SSH Agent 已加载项目密钥，主机指纹仍为已核验值。
- 远端身份必须是无 `sudo` 的 `anas-dev`，且用户 home 有足够空间。
- 遇到主机指纹变化、哈希不一致、8080 被来源不明的进程占用、`sshd -t` 失败或服务健康检查失败时立即停止。
- 当前密钥的精确到期值保留在操作者环境，不写入仓库。

## 一次性管理员配置

保持现有 root 会话和一个已验证的 `anas-dev` 会话，不要同时关闭。先备份配置：

```bash
cp -a /etc/ssh/sshd_config.d /root/sshd_config.d.before-a-nas
loginctl enable-linger anas-dev
loginctl show-user anas-dev -p Linger
```

创建 `/etc/ssh/sshd_config.d/99-a-nas-dev.conf`：

```text
Match User anas-dev
    AllowTcpForwarding local
    PermitOpen 127.0.0.1:8080
    AllowAgentForwarding no
    X11Forwarding no
Match all
```

验证并平滑加载，不使用 restart：

```bash
sshd -t
systemctl reload ssh
```

以 `anas-dev` 备份 `~/.ssh/authorized_keys`，在当前项目密钥选项中删除 `no-port-forwarding`，增加 `permitopen="127.0.0.1:8080"`；保留 `expiry-time`、`no-agent-forwarding` 和 `no-X11-forwarding`。不要改变公钥正文。使用第二个 PowerShell 窗口同时验证普通命令和限定转发，确认成功后才退出旧会话。

回滚管理员配置：恢复 `/root/sshd_config.d.before-a-nas` 中对应文件，运行 `sshd -t` 后 reload；必要时执行 `loginctl disable-linger anas-dev`。

## 部署

在 PowerShell 中先预览，不执行构建、网络连接或远端修改：

```powershell
.\scripts\deploy-dev.ps1 -NasHost <NAS_HOST> -Mode fake -WhatIf
```

确认 main 工作树干净后部署 Fake 可视增量：

```powershell
.\scripts\deploy-dev.ps1 -NasHost <NAS_HOST> -Mode fake
```

脚本固定执行完整检查、Linux/amd64 构建、SHA-256 校验、版本化上传、原子 `current` 切换、用户服务重启和两个 HTTP 冒烟请求；同时把本地控制台启动器、systemd 单元和 PAM 配置放入同一版本目录，供管理员按[本地控制台运行手册](operate-local-kiosk.md)安装。任何激活失败会恢复上一 `current`、环境文件、用户服务单元和 Host Agent 模式；失败制品保留用于诊断。发布目录始终使用 Git 短提交；当 HEAD 恰好带有合法的 `vMAJOR.MINOR.PATCH` 标签时，二进制和产品接口显示该语义版本，否则显示短提交。

Host Agent IPC 在实机通过后切换实时状态：

```powershell
.\scripts\deploy-dev.ps1 -NasHost <NAS_HOST> -Mode agent
```

只有更换本地控制台的屏保视频池时才显式传入仓库外的 MP4 数组；不传时 release 不携带视频，root 系统安装保留 NAS 上的当前池。传入时，脚本为每个文件计算并验证 SHA-256，再以 `screensaver-000.mp4` 开始的匿名编号放入版本目录。后续 root 系统安装复核哈希后，把视频复制到 `/var/lib/a-nas/screensavers/<release-id>/` 并让它成为当前池（见[本地控制台运行手册](operate-local-kiosk.md)）；不会移动源文件，也不会把视频写入 Git 或二进制。单次最多 32 条，重复路径、非 MP4 或缺失文件会在建立网络连接前拒绝：

```powershell
.\scripts\deploy-dev.ps1 `
  -NasHost <NAS_HOST> `
  -Mode agent `
  -StageOnly `
  -ScreensaverVideo @(
    "E:\SteamLibrary\steamapps\workshop\content\431960\3667411885\BMW M5.mp4",
    "E:\SteamLibrary\steamapps\workshop\content\431960\3556095996\Penguins.mp4",
    "E:\SteamLibrary\steamapps\workshop\content\431960\3666233105\1771036359365.mp4",
    "E:\SteamLibrary\steamapps\workshop\content\431960\3679705103\妄想天使直播.mp4",
    "E:\SteamLibrary\steamapps\workshop\content\431960\3743343692\ЭКСПОНАТ - MIA BOYKA (TikTok Homelander Edit) HARDSTYLE REMIX by MilWo - MilWo (1080p, h264) (1).mp4"
  )
```

`-WhatIf` 会执行 Git 状态、路径、扩展名和哈希前置检查，但不会构建、联网或修改远端。正式上传后核对输出的五个文件名与 SHA-256，再由 root 安装脚本建立新池并切换当前池；不得把 Steam Workshop 目录直接授予产品服务读取权限。

2026-10-06 实机已验证 Fake 部署、失败自动回滚、修复后的 Live 部署、制品哈希、`0600` UDS、`503` 映射和恢复路径。限定 SSH 配置也已验证：普通公钥命令保持可用，`127.0.0.1:18080 → 127.0.0.1:8080` 返回健康响应，而转发到远端 22 端口被 `administratively prohibited` 拒绝。首次 Live 失败的证据链见[调查记录](../investigations/2026-10-06-host-agent-runtime-directory.md)。

## 远程浏览与验证

单独打开一个 PowerShell 窗口：

```powershell
.\scripts\open-dev-tunnel.ps1 -NasHost <NAS_HOST>
```

浏览器访问 `http://127.0.0.1:18080/`。这是可选的远程验收方式；设备直连显示器的验收见[本地控制台运行手册](operate-local-kiosk.md)。验收：

- `fake` 显示“模拟数据”和两块确定性磁盘。
- `agent` 显示“实时主机”，磁盘必须与当时的 `lsblk` 一致；未检测到的 512 GB 盘不得出现。
- 实时健康仍为 `unknown`，没有 SMART 结论时不得显示绿色健康。
- 关闭 SSH 登录会话后服务继续运行；关闭隧道窗口后浏览器无法继续访问。

远端诊断命令：

```bash
systemctl --user status anas-api.service anas-host-agent.service
journalctl --user -u anas-api.service -u anas-host-agent.service --since today
readlink "$HOME/apps/a-nas/current"
```

## 回滚与恢复

- 自动回滚失败时，将 `~/apps/a-nas/current` 原子指向上一版本，恢复环境文件与两个用户服务单元，再恢复上一 Host Agent 模式并重启产品服务。
- 不删除任何 `releases/<git-sha>`；确认原因与可恢复性前不执行清理。
- Host Agent 失败但需要保留 UI 时，重新以 `-Mode fake` 部署同一已提交版本。
- 调查材料记录日志、当前链接、RELEASE 内容和哈希，不记录私钥或公钥完整正文。

## 关联

- 规格：[Web 桌面宿主机状态](../specs/web-desktop-host-state.md)
- ADR：[Web 桌面技术选择](../adr/0003-use-react-typescript-for-web-desktop.md)、[Host Agent IPC](../adr/0004-use-http-json-over-unix-socket-for-host-state.md)、[本地控制台](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- 规格：[本地控制台屏幕保护程序](../specs/local-console-screensaver.md)
- 旧手册：[M1 临时 API 部署](deploy-m1-api-to-experimental-nas.md)
