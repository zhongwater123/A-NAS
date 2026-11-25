# 启用局域网 Web 访问

状态：verified（2026-10-09 已在 Experimental NAS 执行）；2026-11-12 起 Caddy 随 `a-nas-host` 安装，第 2 步改为核对，新写法尚未在实机执行
更新时间：2026-11-12

## 目的

Caddy 随 A-NAS 的软件包装上，`anas-site.service` 把它配置成局域网 Web 入口，使内网浏览器经 `http://<NAS 地址>` 使用 A-NAS Web 桌面。本手册核对入口，也说明怎样关闭它。成功标准：从另一台内网电脑打开 `http://172.18.45.48` 能登录并上传文件；本地控制台、SSH 隧道与 SMB 的行为不变。

## 前提与风险

- 权限：NAS 的 root（运行 `a-nas-site`）；装包见[部署 A-NAS 软件包](deploy-a-nas-packages.md)。
- 目标：主机名 `a-nas-dev`，有线网卡 `enp2s0`，当前地址 `172.18.45.48`。地址由 DHCP 分配，可能变化。
- 风险：登录密码（同时是 SMB 密码）、会话 Cookie 与文件内容在局域网明文传输，设备所有者已接受（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)）。Caddy 会占用 TCP 80。
- 停止条件：80 端口已被其他程序占用；`caddy validate` 或 `a-nas-site` 失败；安装后产品服务不再只监听 `127.0.0.1:8080`。

## 步骤

1. 第一次装 A-NAS 软件包之前，确认 80 端口空闲。
   - 命令：`ss -ltnp 'sport = :80'`
   - 预期：只有表头。
   - 完成标准：没有其他程序监听 80；否则停止。
2. 核对 Caddy 来自 A-NAS 软件源。Caddy 是 `a-nas-host` 的依赖，用 Caddy 官方的软件包（[ADR 0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)），按[部署 A-NAS 软件包](deploy-a-nas-packages.md)装包时一起装上；Debian 13 自带的 2.6.2 不再使用。以前从 Caddy 官方源（Cloudsmith）装过的主机，按部署手册第 4 步删除那个源，之后由 A-NAS 软件源原地升级，同名的包保留 A-NAS 的配置。
   - 命令：

     ```bash
     apt-cache policy caddy
     caddy version
     caddy list-modules | grep -x caddy.listeners.proxy_protocol
     ```

   - 预期：`Installed` 的版本来自 A-NAS 软件源（`/var/local/a-nas-repository`），不低于 [`packages.json`](../../deploy/upstream/packages.json) 固定的版本；带 `proxy_protocol` 模块，互联网入口需要它（[启用互联网访问](enable-internet-access.md)）。
   - 完成标准：`systemctl is-active caddy` 为 `active`。
3. 以 root 运行 `a-nas-site`；装包时同样会做这一步。`anas-site.service` 先校验 `/usr/share/a-nas/Caddyfile`，第一次替换时把原 `/etc/caddy/Caddyfile` 备份为 `/etc/caddy/Caddyfile.before-a-nas`，写入 A-NAS 配置并重新加载 Caddy；之后每次开机都再核对，内容不同时才替换。
   - 预期：`a-nas-site` 正常结束，`head -n 1 /etc/caddy/Caddyfile` 输出 `# Managed by A-NAS.`。
   - 完成标准：`systemctl is-active caddy anas-api` 均为 `active`。

## 验证

- NAS 上：`ss -ltn '( sport = :80 or sport = :8080 )'` 显示 `*:80`，产品服务只有 `127.0.0.1:8080`。NAS 默认没有安装 `curl`，HTTP 检查在另一台电脑上做。
- 另一台内网电脑：PowerShell 中 `curl.exe -sS http://172.18.45.48/healthz` 返回 `{"status":"ok"}`，`Test-NetConnection 172.18.45.48 -Port 80` 的 `TcpTestSucceeded` 为 `True`；浏览器打开 `http://172.18.45.48`，用成员账号登录，上传并下载一个文件。
- 本地控制台仍为已登录桌面；Windows 仍能用同一账号打开 `\\172.18.45.48\Shared`。

2026-10-09 使用本手册在 `64f0e26d8349` 上启用：安装 Caddy 2.6.2，Debian 默认配置备份为 `Caddyfile.before-a-nas`；NAS 上 `*:80` 与 `127.0.0.1:8080` 监听正确；从公司 WiFi 上的开发机访问健康检查返回 `ok`、首页 `200`、未登录接口 `401`。浏览器登录与上传由设备所有者后续确认。

## 回滚或恢复

- 关闭局域网入口：`systemctl disable --now caddy`。升级与重启不会重新打开它：`anas-site.service` 照常更新 A-NAS 的 Caddyfile，但不启动 Caddy。产品服务、本地控制台与 SMB 不受影响；互联网入口也由 Caddy 提供，随之关闭。重新打开：`systemctl enable --now caddy`。
- Caddy 是 `a-nas-host` 的依赖，不能单独卸载，也不再退回 Debian 的 2.6.2。

## 关联

- 规格：[局域网 Web 访问](../specs/lan-web-access.md)
- ADR：[0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)
- 调查：不涉及
- 代码：[`deploy/caddy/Caddyfile`](../../deploy/caddy/Caddyfile)、[`anas-site.service`](../../deploy/site/apply.sh)
