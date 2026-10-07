# 安装 Docker 与容器代理

状态：verified（适用于 ADR 0008 后的系统服务部署；2026-10-08 已在 Experimental NAS 验证）
更新时间：2026-10-08

## 目的

在 Experimental NAS 上安装 Debian 打包的 Docker Engine、Compose 与 A-NAS 容器代理，使桌面“Docker”和“应用中心”以实时模式运行。产品服务只能访问容器代理的类型化 Unix socket，不能直接访问等同 root 权限的 `docker.sock`。

## 前提与停止条件

- 必须在主机名为 `a-nas-dev` 的 Debian 13 amd64 Experimental NAS 上以 root 执行；先按 [SSH 手册](bootstrap-experimental-nas-ssh.md)核对主机指纹。
- `/opt/a-nas/current` 与 `/home/anas-dev/apps/a-nas/current` 必须指向同一个已验证 release；数据卷必须仍以 Btrfs 挂载在 `/srv/a-nas/data`。
- 当前架构的产品服务身份是系统账号 `a-nas`。不得沿用 ADR 0008 以前的用户级预览步骤给 `anas-dev` 授权，也不得把 `a-nas`、`anas-dev` 或 Kiosk 账号加入 `docker` 组。
- Docker 会改写 iptables/nftables 规则并在 `/var/lib/docker` 写入镜像与容器状态。安装失败、Docker Server 低于 26、cgroup 不是 v2、代理单元无法以 `anas-container` 启动，或身份/组不符合预期时立即停止。
- 升级前备份 `/etc/a-nas/anas-api.env`；回滚只停用能力，不自动删除 `/var/lib/docker`、应用数据、镜像或容器。

## 安装

### 1. 预检并安装 Docker

```bash
test "$(id -u)" -eq 0
test "$(hostname)" = a-nas-dev
system_release=$(readlink -f /opt/a-nas/current)
kiosk_release=$(readlink -f /home/anas-dev/apps/a-nas/current)
test "$(dirname "$system_release")" = /opt/a-nas/releases
test "$(dirname "$kiosk_release")" = /home/anas-dev/apps/a-nas/releases
test "$(basename "$system_release")" = "$(basename "$kiosk_release")"
findmnt -T /srv/a-nas/data -o TARGET,SOURCE,FSTYPE,OPTIONS
! id -nG anas-dev | tr ' ' '\n' | grep -Fx docker

test ! -e /root/a-nas-container-agent-backup
install -d -o root -g root -m 0700 /root/a-nas-container-agent-backup
install -o root -g root -m 0600 \
  /etc/a-nas/anas-api.env \
  /root/a-nas-container-agent-backup/anas-api.env

apt-get update
apt-get install --no-install-recommends docker.io docker-cli docker-compose
systemctl enable --now docker.service

systemctl is-active --quiet docker.service
docker version
docker info --format 'server={{.ServerVersion}} cgroup={{.CgroupVersion}}'
docker-compose version
```

完成标准：Docker Client 与 Server 都可用，Server 版本不低于 26.0，cgroup 为 2，Compose 命令可用。Debian 13 把 `/usr/bin/docker` 拆到 `docker-cli` 推荐包；使用 `--no-install-recommends` 时必须显式安装它。应用中心优先使用 `docker compose`，不可用时会使用 Debian 的 `docker-compose`。

### 2. 创建最小权限身份

```bash
getent group anas-container >/dev/null || groupadd --system anas-container
if ! id anas-container >/dev/null 2>&1; then
  useradd --system --gid anas-container \
    --home-dir /nonexistent --shell /usr/sbin/nologin anas-container
fi

usermod -aG docker anas-container
usermod -aG anas-container a-nas

id anas-container
id a-nas
id anas-dev
```

完成标准：只有 `anas-container` 是 `docker` 组成员；`a-nas` 只新增 `anas-container` 组以连接类型化代理；`anas-dev` 不属于 `docker` 或 `anas-container`。

### 3. 安装不可变代理制品

开发机必须先从要部署的提交运行 `make build-binaries VERSION=<GIT_SHA>`，上传 `anas-container-agent` 与匹配的 systemd 单元到同一个不可变 source release，并核对发布清单中的 SHA-256。随后在 NAS 上执行：

```bash
source_release=/home/anas-dev/apps/a-nas/releases/<GIT_SHA>
agent_sha=<64 位 SHA-256>

printf '%s  %s\n' "$agent_sha" "$source_release/anas-container-agent" |
  sha256sum --check

install -D -o root -g root -m 0755 \
  "$source_release/anas-container-agent" \
  /usr/local/lib/a-nas/anas-container-agent
install -o root -g root -m 0644 \
  "$source_release/anas-container-agent.service" \
  /etc/systemd/system/anas-container-agent.service

systemctl daemon-reload
systemctl enable --now anas-container-agent.service
systemctl is-active --quiet anas-container-agent.service
stat -c '%a %U:%G %n' \
  /run/a-nas-container \
  /run/a-nas-container/agent.sock \
  /usr/local/lib/a-nas/anas-container-agent
```

完成标准：运行目录为 `750 anas-container:anas-container`，socket 为 `660 anas-container:anas-container`，二进制为 `755 root:root`。应用数据根为 `/srv/a-nas/data/apps`，共享根为 `/srv/a-nas/data/spaces/shared`；只有挂载点改变时才通过 `/etc/a-nas/container-agent.env` 修改，并且两个根仍须位于同一数据卷。

启用或切换产品版本之前，从开发机上传并核对 [`scripts/verify-container-deployment.py`](../../scripts/verify-container-deployment.py) 的 SHA-256；在 NAS root 会话以产品身份执行它：

```bash
# 此路径是已校验的探针副本，不是新的产品 release。
probe=/home/anas-dev/verify-container-deployment.py
timeout 45s runuser -u a-nas -- python3 - --agent-only < "$probe"
```

它只执行 GET，检查 Docker 版本、容器/镜像数组和非空应用目录；失败时停止在切换之前。`anas-dev` 无权穿过代理运行目录，使用该账号检查 socket 得到 Permission denied 不能当成 socket 缺失。

### 4. 启用产品能力

不要改写环境文件中的其他键。先删除旧能力键，再追加唯一的实时模式配置：

```bash
env_file=/etc/a-nas/anas-api.env
temporary=$(mktemp /etc/a-nas/anas-api.env.XXXXXX)
grep -v '^ANAS_CONTAINERS_MODE=' "$env_file" > "$temporary"
printf 'ANAS_CONTAINERS_MODE=agent\n' >> "$temporary"
install -o root -g a-nas -m 0640 "$temporary" "$env_file"
rm -f -- "$temporary"

systemctl restart anas-api.service
systemctl is-active --quiet anas-api.service
journalctl -u anas-api.service -b --no-pager -n 50
```

`scripts/install-v1.0.1-system-services.sh` 在后续升级时只会在 `/run/a-nas-container/agent.sock` 存在时保留该能力键；代理缺失时继续 fail closed。

## 验证

```bash
systemctl is-active docker.service anas-container-agent.service anas-api.service
systemctl is-enabled docker.service anas-container-agent.service
grep -Fqx 'ANAS_CONTAINERS_MODE=agent' /etc/a-nas/anas-api.env
runuser -u a-nas -- test -r /run/a-nas-container/agent.sock
! runuser -u a-nas -- test -r /var/run/docker.sock
! runuser -u anas-dev -- test -r /var/run/docker.sock
docker version --format 'server={{.Server.Version}} api={{.Server.APIVersion}}'
```

登录本地控制台后：

1. 打开“Docker”，应显示实时 Docker 版本和容器/镜像列表；空列表是有效状态，不应再显示“Docker 未启用”。
2. 打开“应用中心”，应显示内置目录并可生成安装计划；实际安装前仍须确认镜像、端口与文件夹摘要。
3. 首次验收可用专用测试容器核对停止、启动和日志，再删除测试容器。应用中心验收按[应用中心规格](../specs/app-center.md)使用一个可丢弃应用，确认应用身份、数据目录和卸载保留数据。

本地屏保的媒体路由是 `/local-console/screensaver.mp4`，不是 `/screensaver.mp4`；如在同一切换脚本中复核 Range 请求，必须使用真实路由并单独打印其 HTTP 状态。

| 检查对象 | 传输和身份 | 路由 |
|---|---|---|
| Docker 列表 | 代理 UDS，`a-nas` | `/v1/snapshot` |
| 应用目录 | 代理 UDS，`a-nas` | `/v1/apps` |
| 桌面 Docker 列表 | 产品 HTTP，管理员会话 | `/api/v1/containers` |
| 桌面应用目录 | 产品 HTTP，管理员会话 | `/api/v1/apps` |
| 屏保媒体 | 产品 HTTP，回环请求 | `/local-console/screensaver.mp4` |

发布探针通过 `--desktop-only` 检查健康 JSON、嵌入式桌面和 1024 字节 Range；通过 `--agent-only` 检查前两行。`bash -n` 和 ShellCheck 不能发现接口路径写错。开发机可先在具备本地 Docker 只读访问权限的环境执行 `python3 scripts/check-container-deployment-probes.py`，它使用已有 `build/anas-api`、`build/anas-container-agent` 和临时状态目录，只读取 Docker；会重现两个错误路径的 404，并用真实二进制验证探针。不要在 NAS 上运行这个开发检查。

`/api/v1/containers` 与 `/api/v1/apps` 需要管理员产品会话；未带会话的 `curl` 返回未授权不能证明能力未启用。以服务、socket、环境键和登录后的 UI 共同作为完成证据。

### Experimental NAS 已验证基线

2026-10-08 使用本手册完成 `c902acded999` 切换：Docker 26.1.5、Compose 2.26.1、containerd、Container Agent、Host Agent、API、Kiosk 与 Samba 均 active；代理的 `/v1/snapshot`、`/v1/apps`，桌面的 `/healthz`、`/` 以及屏保 Range 请求均通过。安装过程确认 Debian 13 配合 `--no-install-recommends` 时必须显式列出 `docker-cli`。

验证脚本曾因手写 `/screensaver.mp4` 和 `/v1/containers` 两个不存在的路径而触发安全回滚。恢复时保留 Docker 数据和已经安装的软件，只重新切换不可变 release；这类 404 应先核对上表的协议层级与真实路由，不要重复安装 Docker，也不要扩大账号权限。

## 回滚或恢复

停用能力但保留 Docker 数据：

```bash
systemctl disable --now anas-container-agent.service
install -o root -g a-nas -m 0640 \
  /root/a-nas-container-agent-backup/anas-api.env \
  /etc/a-nas/anas-api.env
systemctl restart anas-api.service
```

如需撤销授权，执行 `gpasswd -d a-nas anas-container` 后重启 `anas-api.service`。移除 Docker 会影响全部容器与镜像，必须另行确认；不要把 `apt-get purge` 或 `/var/lib/docker` 删除放进常规回滚。

## 关联

- 规格：[容器管理](../specs/container-management.md)、[应用中心](../specs/app-center.md)
- ADR：[0009 Docker Engine 与专用容器代理](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)
- 调查：[Experimental NAS 容器能力保持禁用](../investigations/2026-10-08-experimental-nas-containers-disabled.md)
- 实现：[`deploy/systemd/system/anas-container-agent.service`](../../deploy/systemd/system/anas-container-agent.service)、[`scripts/install-v1.0.1-system-services.sh`](../../scripts/install-v1.0.1-system-services.sh)
- 探针与验证：[`verify-container-deployment.py`](../../scripts/verify-container-deployment.py)、[`check-container-deployment-probes.py`](../../scripts/check-container-deployment-probes.py)
- 协议来源：[`containers/agent`](../../internal/containers/agent/agent.go)、[`appstore/agent`](../../internal/appstore/agent/agent.go)、[`webui`](../../internal/webui/handler.go)
