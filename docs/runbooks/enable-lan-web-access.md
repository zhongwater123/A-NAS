# 启用局域网 Web 访问

状态：draft（尚未在实验 NAS 执行）
更新时间：2026-10-09

## 目的

在实验 NAS 上安装 Caddy，并部署包含局域网 Web 入口的版本，使内网浏览器经 `http://<NAS 地址>` 使用 A-NAS Web 桌面。成功标准：从另一台内网电脑打开 `http://172.18.45.48` 能登录并上传文件；本地控制台、SSH 隧道与 SMB 的行为不变。

## 前提与风险

- 权限：NAS 的 root（安装软件包、运行系统服务安装器）；暂存发布使用 `anas-dev`。
- 目标：主机名 `a-nas-dev`，有线网卡 `enp2s0`，当前地址 `172.18.45.48`。地址由 DHCP 分配，可能变化。
- 风险：登录密码（同时是 SMB 密码）、会话 Cookie 与文件内容在局域网明文传输，设备所有者已接受（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)）。Caddy 会占用 TCP 80。
- 停止条件：80 端口已被其他程序占用；`caddy validate` 或系统服务安装器失败；安装后产品服务不再只监听 `127.0.0.1:8080`。

## 步骤

1. 确认 80 端口空闲。
   - 命令：`ss -ltnp 'sport = :80'`
   - 预期：只有表头。
   - 完成标准：没有其他程序监听 80；否则停止。
2. 安装 Caddy。
   - 命令：`apt-get update && apt-get install -y caddy`
   - 预期：Debian 包启动 `caddy.service`，80 端口提供 Caddy 的默认欢迎页。
   - 完成标准：`systemctl is-active caddy` 为 `active`，`caddy version` 为 2.6 或更高。
3. 按[实机配置手册](provision-v1.0.1-experimental-storage.md#安装系统服务)暂存并安装包含本功能的发布，发布目录中应有 `Caddyfile`。安装器检测到 Caddy 后先校验配置，再把原 `/etc/caddy/Caddyfile` 备份为 `/etc/caddy/Caddyfile.before-a-nas`，写入 A-NAS 配置并重新加载 Caddy。
   - 预期：安装器正常结束，`head -n 1 /etc/caddy/Caddyfile` 输出 `# Managed by A-NAS.`。
   - 完成标准：`systemctl is-active caddy anas-api` 均为 `active`。

## 验证

- NAS 上：`curl -fsS http://127.0.0.1/healthz` 返回 `{"status":"ok"}`；`ss -ltn 'sport = :8080'` 只显示 `127.0.0.1:8080`。
- 另一台内网电脑：PowerShell 中 `Test-NetConnection 172.18.45.48 -Port 80` 的 `TcpTestSucceeded` 为 `True`；浏览器打开 `http://172.18.45.48`，用成员账号登录，上传并下载一个文件。
- 本地控制台仍为已登录桌面；Windows 仍能用同一账号打开 `\\172.18.45.48\Shared`。

## 回滚或恢复

- 关闭局域网入口：`systemctl disable --now caddy`。产品服务、本地控制台与 SMB 不受影响。
- 恢复 Caddy 原配置：`install -m 0644 /etc/caddy/Caddyfile.before-a-nas /etc/caddy/Caddyfile && systemctl reload caddy`。
- 彻底移除：`apt-get remove caddy`，之后的安装器会跳过局域网入口。

## 关联

- 规格：[局域网 Web 访问](../specs/lan-web-access.md)
- ADR：[0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)
- 调查：不涉及
- 代码：[`deploy/caddy/Caddyfile`](../../deploy/caddy/Caddyfile)、[系统服务安装器](../../scripts/install-v1.0.1-system-services.sh)
