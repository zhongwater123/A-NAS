# 启用局域网 Web 访问

状态：verified（2026-10-09 已在 Experimental NAS 执行）
更新时间：2026-11-11

## 目的

在实验 NAS 上安装 Caddy，并部署包含局域网 Web 入口的版本，使内网浏览器经 `http://<NAS 地址>` 使用 A-NAS Web 桌面。成功标准：从另一台内网电脑打开 `http://172.18.45.48` 能登录并上传文件；本地控制台、SSH 隧道与 SMB 的行为不变。

## 前提与风险

- 权限：NAS 的 root（安装软件包、运行 `a-nas-site`）；暂存发布使用 `anas-dev`。
- 目标：主机名 `a-nas-dev`，有线网卡 `enp2s0`，当前地址 `172.18.45.48`。地址由 DHCP 分配，可能变化。
- 风险：登录密码（同时是 SMB 密码）、会话 Cookie 与文件内容在局域网明文传输，设备所有者已接受（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)）。Caddy 会占用 TCP 80。
- 停止条件：80 端口已被其他程序占用；`caddy validate` 或 `a-nas-site` 失败；安装后产品服务不再只监听 `127.0.0.1:8080`。

## 步骤

1. 确认 80 端口空闲。
   - 命令：`ss -ltnp 'sport = :80'`
   - 预期：只有表头。
   - 完成标准：没有其他程序监听 80；否则停止。
2. 从 Caddy 官方软件源安装 Caddy（[ADR 0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)）。Debian 13 自带的 `caddy` 停留在 2022 年的 2.6.2，不再使用；官方源与 Debian 的包同名，用 apt 优先级固定来源。已装了 Debian 包的主机执行同一段命令即可原地升级，`--force-confold` 保留 `anas-site.service` 写入的 A-NAS 配置。
   - 命令：

     ```bash
     apt-get update
     apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl gnupg
     curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
     curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' -o /etc/apt/sources.list.d/caddy-stable.list
     chmod o+r /usr/share/keyrings/caddy-stable-archive-keyring.gpg /etc/apt/sources.list.d/caddy-stable.list
     printf 'Package: caddy\nPin: origin dl.cloudsmith.io\nPin-Priority: 700\n' > /etc/apt/preferences.d/caddy
     apt-get update
     apt-get install -y -o Dpkg::Options::=--force-confold caddy
     apt-cache policy caddy
     caddy version
     ```

   - 预期：`caddy.service` 运行；`apt-cache policy caddy` 显示已安装的版本来自 `dl.cloudsmith.io`。全新安装时 80 端口提供 Caddy 的默认欢迎页，已有 A-NAS 配置时保持局域网入口。
   - 完成标准：`systemctl is-active caddy` 为 `active`，`caddy version` 为官方源当前的 2.x 版本。
   - 官方源不可用：2026-10-09 起 Cloudsmith 一度对软件包索引返回 `402 Payment Required`（[caddyserver/caddy#8184](https://github.com/caddyserver/caddy/issues/8184)），`apt-get update` 因此失败；2026-11-05 从实验 NAS 核对，索引已恢复，最新版本为 2.11.9。此时执行 `rm -f /etc/apt/sources.list.d/caddy-stable.list /etc/apt/preferences.d/caddy && apt-get update`，保留已安装的 Caddy（全新安装时先装 Debian 的 `caddy` 过渡），源恢复后重新执行本步骤。A-NAS 的 Caddyfile 只做反向代理，Debian 的 2.6.2 可以提供局域网入口，但不能提供互联网入口：后者需要 2.7 起内置的 `proxy_protocol`（[启用互联网访问](enable-internet-access.md)）。
3. 以 root 运行 `a-nas-site`。还没有装 `a-nas` 时，先按[部署 A-NAS 软件包](deploy-a-nas-packages.md)安装，安装时同样会做这一步。`anas-site.service` 检测到 Caddy 后先校验 `/usr/share/a-nas/Caddyfile`，第一次替换时把原 `/etc/caddy/Caddyfile` 备份为 `/etc/caddy/Caddyfile.before-a-nas`，写入 A-NAS 配置并重新加载 Caddy；之后每次开机都再核对，内容不同时才替换。
   - 预期：`a-nas-site` 正常结束，`head -n 1 /etc/caddy/Caddyfile` 输出 `# Managed by A-NAS.`。
   - 完成标准：`systemctl is-active caddy anas-api` 均为 `active`。

## 验证

- NAS 上：`ss -ltn '( sport = :80 or sport = :8080 )'` 显示 `*:80`，产品服务只有 `127.0.0.1:8080`。NAS 默认没有安装 `curl`，HTTP 检查在另一台电脑上做。
- 另一台内网电脑：PowerShell 中 `curl.exe -sS http://172.18.45.48/healthz` 返回 `{"status":"ok"}`，`Test-NetConnection 172.18.45.48 -Port 80` 的 `TcpTestSucceeded` 为 `True`；浏览器打开 `http://172.18.45.48`，用成员账号登录，上传并下载一个文件。
- 本地控制台仍为已登录桌面；Windows 仍能用同一账号打开 `\\172.18.45.48\Shared`。

2026-10-09 使用本手册在 `64f0e26d8349` 上启用：安装 Caddy 2.6.2，Debian 默认配置备份为 `Caddyfile.before-a-nas`；NAS 上 `*:80` 与 `127.0.0.1:8080` 监听正确；从公司 WiFi 上的开发机访问健康检查返回 `ok`、首页 `200`、未登录接口 `401`。浏览器登录与上传由设备所有者后续确认。

## 回滚或恢复

- 关闭局域网入口：`systemctl disable --now caddy`。产品服务、本地控制台与 SMB 不受影响。
- 恢复 Caddy 原配置：`install -m 0644 /etc/caddy/Caddyfile.before-a-nas /etc/caddy/Caddyfile && systemctl reload caddy`。
- 彻底移除：`apt-get remove caddy`，之后 `anas-site.service` 跳过局域网入口。
- 退回 Debian 的 Caddy：删除 `/etc/apt/preferences.d/caddy` 与 `/etc/apt/sources.list.d/caddy-stable.list`，`apt-get update` 后执行 `apt-get install -y --allow-downgrades -o Dpkg::Options::=--force-confold caddy=2.6.2-12+deb13u1`。仅用于排障。

## 关联

- 规格：[局域网 Web 访问](../specs/lan-web-access.md)
- ADR：[0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)
- 调查：不涉及
- 代码：[`deploy/caddy/Caddyfile`](../../deploy/caddy/Caddyfile)、[`anas-site.service`](../../deploy/site/apply.sh)
