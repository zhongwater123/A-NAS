# 安装 Docker 与容器代理

状态：draft（步骤基于 Debian 13 设计，尚未在 Experimental NAS 执行）
更新时间：2026-10-07

## 目的

在 Experimental NAS 上安装 Docker Engine 与 A-NAS 容器代理，使桌面“Docker”应用以实时模式管理容器。成功后 `anas-api` 环境文件包含 `ANAS_CONTAINERS_MODE=agent`，桌面显示实时容器列表。

## 前提与风险

- 权限：管理员 root 会话（本手册是唯一需要 root 的步骤）；日常账号 `anas-dev` 不获得 `sudo` 或 `docker` 组。
- 目标：主机名 `a-nas-dev` 的 Debian 13 amd64；使用前确认 SSH 指纹与 [SSH 运行手册](bootstrap-experimental-nas-ssh.md) 一致。
- 风险：`docker` 组等同 root。任何把 `anas-dev`、`anas-api` 或 Kiosk 账号加入 `docker` 组的操作都会破坏本设计；Docker 会改写 iptables/nftables 规则，可能影响已有防火墙配置。
- 停止条件：主机名或指纹不符；`anas-dev` 已在 `docker` 组；Docker 安装失败或 `docker info` 报错；容器代理单元无法以 `anas-container` 身份启动。

## 步骤

1. 安装 Debian 打包的 Docker Engine 与 Compose 插件（随 APT 获得安全更新）。
   - 命令：`apt-get update && apt-get install --no-install-recommends docker.io docker-compose`
   - 预期：`systemctl is-active docker` 为 `active`，`docker version` 同时显示 Client 与 Server。
   - 完成标准：`docker info` 无错误，cgroup 版本为 2。
2. 创建容器代理身份，只给它 `docker` 组。
   - 命令：`useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin --user-group anas-container`，然后 `usermod -aG docker anas-container`。
   - 预期：`id anas-container` 包含 `docker`；`id anas-dev` 不包含 `docker`。
   - 完成标准：两条 `id` 输出与预期一致。
3. 允许产品服务连接容器代理（只授予类型化 API）。
   - 命令：`usermod -aG anas-container anas-dev`
   - 预期：`id anas-dev` 包含 `anas-container`。新组成员身份需要重启 `anas-dev` 的用户 systemd 管理器才会生效：`systemctl restart user@$(id -u anas-dev).service`（会短暂重启产品服务与 Host Agent）。
   - 完成标准：以 `anas-dev` 登录后 `id` 包含 `anas-container`，且 `systemctl --user is-active anas-api` 为 `active`。
4. 安装 root 所有的代理程序与单元，防止 `anas-dev` 替换二进制。
   - 在开发机运行 `make build-binaries`，把 `build/anas-container-agent` 与 `deploy/systemd/system/anas-container-agent.service` 传到 NAS 临时目录并核对 SHA-256。
   - 命令：`install -D -o root -g root -m 0755 anas-container-agent /usr/local/lib/a-nas/anas-container-agent`；`install -o root -g root -m 0644 anas-container-agent.service /etc/systemd/system/`；`systemctl daemon-reload && systemctl enable --now anas-container-agent.service`
   - 预期：`systemctl is-active anas-container-agent` 为 `active`；`stat -c '%a %U:%G' /run/a-nas-container /run/a-nas-container/agent.sock` 输出 `750 anas-container:anas-container` 与 `660 anas-container:anas-container`。
   - 完成标准：权限与属主完全一致。
5. 以真实模式重新部署产品服务，让激活脚本检测到 socket 并启用容器管理。
   - 按 [Web 预览运行手册](deploy-web-preview-to-experimental-nas.md) 以 `agent` 模式部署。
   - 预期：`~/.config/a-nas/anas-api.env` 含 `ANAS_CONTAINERS_MODE=agent`。

## 验证

- `curl -s http://127.0.0.1:8080/api/v1/containers` 返回 `"dataSource":"live"` 与 Docker 版本。
- `curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: text/plain' -d '{"action":"stop"}' http://127.0.0.1:8080/api/v1/containers/<64 位 ID>/actions` 返回 `403`。
- 在 Kiosk 或 SSH 隧道中打开“Docker”，对一个专用测试容器（例如 `docker run -d --name anas-smoke nginx:stable`）执行停止、启动和查看日志，结果与 `docker inspect` 一致；完成后 `docker rm -f anas-smoke`。
- `id anas-dev` 仍不包含 `docker`；`sudo -u anas-dev docker ps` 因权限失败。

## 回滚或恢复

- 停用容器管理：`systemctl disable --now anas-container-agent.service`，删除 `/etc/systemd/system/anas-container-agent.service` 与 `/usr/local/lib/a-nas/anas-container-agent`，再重新部署产品服务；激活脚本检测不到 socket 时不会写入 `ANAS_CONTAINERS_MODE`，桌面显示“Docker 未启用”。
- 撤销授权：`gpasswd -d anas-dev anas-container`，然后重启 `anas-dev` 的用户管理器。
- 移除 Docker 本身（会删除全部容器与镜像，需另行确认数据）：`apt-get purge docker.io docker-compose`，并核对 `/var/lib/docker` 是否需要保留。

## 关联

- 规格：[容器管理](../specs/container-management.md)
- ADR：[0009 Docker Engine 与专用容器代理](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)
- 调查：不涉及
