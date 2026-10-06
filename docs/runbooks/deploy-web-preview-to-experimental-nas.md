# 部署 Web 桌面预览到实验 NAS

状态：implemented, remote verification pending
更新时间：2026-10-06

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

脚本固定执行完整检查、Linux/amd64 构建、SHA-256 校验、版本化上传、原子 `current` 切换、用户服务重启和两个 HTTP 冒烟请求；同时把本地控制台启动器、systemd 单元和 PAM 配置放入同一版本目录，供管理员按[本地控制台运行手册](operate-local-kiosk.md)安装。任何激活失败会恢复上一 `current`、环境文件、用户服务单元和 Host Agent 模式；失败制品保留用于诊断。

Host Agent IPC 在实机通过后切换实时状态：

```powershell
.\scripts\deploy-dev.ps1 -NasHost <NAS_HOST> -Mode agent
```

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
- 旧手册：[M1 临时 API 部署](deploy-m1-api-to-experimental-nas.md)
