# 启用互联网访问

状态：draft（系统测试已验证 NAS 一侧；边缘服务器已按第 2 步准备，NAS 一侧与开放待执行）
更新时间：2026-11-11

## 目的

让设备所有者与成员在公司外打开 `https://<域名>` 使用 A-NAS，行为见[互联网访问规格](../specs/internet-access.md)，取舍见 [ADR 0032](../adr/0032-reach-the-web-desktop-from-the-internet-through-an-edge-relay.md)。成功标准：用移动网络上的手机打开该地址，看到受信任的证书，并能登录、浏览与上传文件；“设置 → 日志”把这次登录记为“外网 · <手机的公网地址>”；局域网入口、本地控制台与 SMB 不受影响。

## 前提与风险

- 权限：
  - 边缘服务器：只能经公司堡垒机（JumpServer，密钥加 MFA 动态码）登录，登录账号可以免密 sudo。堡垒机地址与登录串向 IT 索取，不写入仓库；下面的命令记作 `<堡垒机登录>`。
  - NAS：root。暂存发布用 `anas-dev`。
- 目标：
  - 边缘服务器：机房的 Ubuntu 24.04 虚拟机，地址 `172.25.4.8`。
  - 实验 NAS `a-nas-dev`：`enp2s0` 上的 `172.18.45.48`，由 DHCP 分配，变了也不影响隧道。
  - 隧道网段 `fd78:1295:a3fe:1::/64`：边缘服务器是 `::1`，NAS 是 `::2`。
- 外部条件：
  - IT 把一个公网 IP 的 **TCP 443 与 80** 映射到 `172.25.4.8`，不映射其他端口；
  - 域名的 A 记录指向这个公网 IP；
  - 公司线路允许这个域名在 80/443 上对外提供网站（备案）。

  NAS 到边缘服务器的 UDP 51820 不需要 IT 放行：2026-11-05 在边缘服务器上抓包，确认 NAS 发出的包能到达。
- 风险：
  - 登录页暴露在互联网上，登录密码同时是 SMB 密码，登录限速见规格。
  - 只有 HTTPS 的 443 经隧道到达 NAS；SSH、SMB 与应用端口不对外。
  - 边缘服务器上运行着 IT 的监控代理，所以它的防火墙只限制来自公网地址的流量。
- 停止条件：
  - 边缘服务器上已有其他程序占用 80、443 或 UDP 51820；
  - NAS 上的 `a-nas-site` 以非 0 结束；
  - 开放后从外网能连到 443 以外的端口；
  - 证书无法签发。

## 步骤

1. 选择隧道网段。实验 NAS 用上面的网段；其他设备另生成一个，避免两台设备撞在一起。
   - 命令（任意一台有 Python 的电脑）：`python3 -c "import secrets; h=secrets.token_hex(5); print(f'fd{h[:2]}:{h[2:6]}:{h[6:]}:1::/64')"`
   - 完成标准：得到一个 `fdXX:XXXX:XXXX:1::/64`，下面记作 `<网段>`。
2. 准备边缘服务器（隧道与防火墙，HAProxy 关闭）。在开发机上经堡垒机把仓库的脚本交给 root 执行：

   ```bash
   ssh <堡垒机登录> 'sudo -n bash -s -- <网段>' < deploy/edge/setup-edge.sh
   ```

   - 预期：最后两行是 `edge relay: tunnel <网段>1, NAS <网段>2; HAProxy inactive` 和 `relay public key: <边缘公钥>`。
   - 完成标准：`systemctl is-active a-nas-edge-firewall wg-quick@wg-anas` 均为 `active`，`haproxy` 为 `inactive`；记下 `<边缘公钥>`。
   - 实验 NAS 的边缘服务器已于 2026-11-05 手工完成同样的配置。再执行一次脚本会收敛到同样的状态，并保留原有密钥，最后照样打印公钥。
3. 在 NAS 上安装互联网入口需要的软件（root）。
   - 先按[启用局域网 Web 访问](enable-lan-web-access.md)第 2 步从 Caddy 官方源安装 Caddy，替换 Debian 的 2.6.2；再执行 `apt-get install -y --no-install-recommends wireguard-tools nftables`。
   - 预期：`caddy list-modules | grep -x caddy.listeners.proxy_protocol` 有输出，`command -v wg wg-quick nft` 都有路径。
   - 完成标准：上面两项都满足；局域网入口仍可访问。
4. 在 NAS 上写入配置（root）。

   ```bash
   printf '%s\n' 'ANAS_INTERNET_HOST=<域名>' 'ANAS_INTERNET_RELAY_ENDPOINT=172.25.4.8:51820' 'ANAS_INTERNET_RELAY_PUBLIC_KEY=<边缘公钥>' 'ANAS_INTERNET_TUNNEL_PREFIX=<网段>' > /etc/a-nas/internet.env
   ```

   - 预期：`cat /etc/a-nas/internet.env` 正好四行。域名用小写；中文域名写成 `xn--` 形式。
   - 完成标准：文件属主为 root，内容与上面一致。
5. 在 NAS 上应用配置（root）：运行 `a-nas-site`。还没有装包含本功能的版本时，先按[部署 A-NAS 软件包](deploy-a-nas-packages.md)安装，安装时同样会应用。`anas-site.service` 发现 `internet.env` 后先校验四项配置，确认 Caddy 2.7 起的版本、WireGuard 与 nftables 都已安装，再写入隧道配置与 Caddy 的互联网站点；`a-nas-generator` 让产品服务监听 `127.0.0.1:8081`，开机时启动防火墙与隧道。
   - 预期：`a-nas-site` 以 0 结束；`journalctl -u anas-site -b -o cat` 中有 `internet entry: https://<域名> through 172.25.4.8:51820; this NAS's tunnel public key: <NAS 公钥>`。
   - 完成标准：
     - `systemctl is-active anas-internet-firewall wg-quick@wg-anas caddy anas-api` 均为 `active`；
     - `ss -ltn 'sport = :8081'` 只有 `127.0.0.1:8081`；
     - 记下 `<NAS 公钥>`。
6. 开放边缘服务器：把 NAS 加为隧道的对端，打开 HAProxy。

   ```bash
   ssh <堡垒机登录> 'sudo -n bash -s -- <网段> <域名> <NAS 公钥>' < deploy/edge/setup-edge.sh
   ```

   - 预期：最后两行中 HAProxy 为 `active`。
   - 完成标准：
     - 边缘服务器上 `wg show wg-anas latest-handshakes` 显示一个不超过 2 分钟前的时间；
     - `timeout 3 bash -c '</dev/tcp/<网段>2/443'` 成功；
     - `ss -ltn '( sport = :443 or sport = :80 )'` 显示 HAProxy 在两个端口监听。
7. 等待证书。IT 的映射与 DNS 生效后，NAS 上的 Caddy 经 443 用 TLS-ALPN 申请证书。
   - 命令（NAS）：`journalctl -u caddy --since -30min | grep -E 'certificate obtained|could not get certificate'`
   - 预期：出现 `certificate obtained successfully` 和域名。
   - 完成标准：已签发证书；签发失败时，先核对 DNS、IT 的 443 映射和备案，再按停止条件处理。

## 验证

- 移动网络上的手机：打开 `https://<域名>`，证书受信任；用成员账号登录，上传并下载一个文件；切换 Wi-Fi 与移动网络后，仍保持登录。
- 同一部手机：`http://<域名>` 跳转到 `https://<域名>`；终端页说明外网不能使用终端，桌面上的应用图标显示“仅局域网”。
- 公司外的电脑：`curl -sI https://<域名>/healthz` 返回 `200` 并带 `strict-transport-security`；`curl -sk --resolve x.example:443:<公网 IP> https://x.example/` 连接被关闭（SNI 不符）。对公网 IP 的 22、445、6806、8080 端口执行 `Test-NetConnection` 都应失败。
- 管理员在“设置 → 日志”中看到这次登录，记为“外网 · <手机的公网地址>”。
- 局域网：`http://172.18.45.48` 照常可用；局域网会话不能在外网入口使用，外网会话也不能在局域网使用。

## 回滚或恢复

- 立即关闭外网访问：在边缘服务器上执行步骤 2 的命令（不带域名与公钥）。脚本关闭 HAProxy 并移除对端，NAS 不需要改动。
- 只在 NAS 一侧关闭：root 执行 `systemctl stop wg-quick@wg-anas`，隧道断开后外网就到不了 NAS。下次运行 `a-nas-site`、安装软件包或重启时会重新启动隧道。
- 彻底停用：删除 `/etc/a-nas/internet.env`，再运行 `a-nas-site`。它换回只有局域网入口的 Caddy 站点，去掉产品服务的 8081 端口，停止隧道与防火墙并删除隧道配置，保留 `/etc/wireguard/wg-anas.key`，以后启用时公钥不变。
- 恢复 HAProxy 原配置：`install -m 0644 /etc/haproxy/haproxy.cfg.before-a-nas /etc/haproxy/haproxy.cfg`。

## 关联

- 规格：[互联网访问](../specs/internet-access.md)、[局域网 Web 访问](../specs/lan-web-access.md)
- ADR：[0032](../adr/0032-reach-the-web-desktop-from-the-internet-through-an-edge-relay.md)、[0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)
- 调查：不涉及
- 代码：[`deploy/edge/setup-edge.sh`](../../deploy/edge/setup-edge.sh)、[`deploy/caddy/Caddyfile.internet`](../../deploy/caddy/Caddyfile.internet)、[`deploy/internet/anas-internet.nft`](../../deploy/internet/anas-internet.nft)、[`anas-site.service`](../../deploy/site/apply.sh)、[`a-nas-generator`](../../deploy/site/a-nas-generator.sh)、[`a-nas-site`](../../deploy/site/a-nas-site.sh)
