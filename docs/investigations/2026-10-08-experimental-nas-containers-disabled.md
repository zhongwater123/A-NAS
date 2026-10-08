# Experimental NAS 的 Docker 与应用中心保持禁用

状态：resolved；Docker/CLI/容器代理与 `c902acded999` 已在 Experimental NAS 完成实机切换和验证
更新时间：2026-10-08

## 症状与影响

实验 NAS 的桌面可以打开“Docker”和“应用中心”，但两者都提示先安装 Docker 与 A-NAS 容器代理，并以容器代理模式启动产品服务。文件、账号、Kiosk 与屏保功能正常。

这两个窗口共用同一个容器能力边界；能力被禁用时，产品 API 对 `/api/v1/containers` 与 `/api/v1/apps` 返回 503，而前端显示安全启用说明，不会连接虚构的 Docker 数据。

## 最小反馈信号

下列只读检查直接观察运行依赖，不需要产品账号密码：

```bash
command -v docker || echo missing
systemctl is-active docker.service || true
systemctl is-active anas-container-agent.service || true
test -S /run/docker.sock && echo present || echo missing
getent passwd anas-container || echo missing
test -x /usr/local/lib/a-nas/anas-container-agent && echo present || echo missing
```

2026-10-08 首次检查稳定得到：Docker 二进制缺失、两个服务 inactive、Docker socket 缺失、`anas-container` 身份和系统代理二进制均缺失。删除任一检查都会失去对安装链某一层的判定，因此这是当前最小的宿主机反馈信号。

## 假设与证据

| 假设 | 可证伪预测 | 结果 |
|---|---|---|
| Docker 与容器代理从未部署 | 包、身份、单元、二进制与 socket 同时缺失 | confirmed |
| 依赖已安装但服务未启用或启动失败 | 至少存在 Docker 二进制、包或代理单元 | rejected |
| 只有产品服务环境缺少 `ANAS_CONTAINERS_MODE=agent` | Docker 和代理 active，只是 API 返回 disabled | rejected as sole cause；安装后仍须配置 |
| 产品服务没有代理 socket 权限 | socket 存在但 `a-nas` 读取失败 | rejected for current state；安装步骤必须防止该状态 |

## 根因

容器管理和应用中心只完成了代码、规格与本地 WSL2 Docker 验证；项目状态一直标记“Experimental NAS 未部署”。此前的宿主机升级只包含 API、Host Agent、Kiosk、Samba 与屏保，没有安装会改变网络规则且持有等同 root 权限 socket 的 Docker Engine，也没有创建独立容器代理身份。因此实时部署按设计默认为 `disabled`，前端提示准确。

同时发现原容器代理手册仍按 ADR 0008 之前的用户级预览架构编写：它把 `anas-dev` 加入代理组、重启用户级服务并修改 `~/.config/a-nas/anas-api.env`。统一身份升级后，实际产品服务是系统账号 `a-nas`，配置位于 `/etc/a-nas/anas-api.env`。继续照旧手册操作会授权错误的身份，仍无法启用产品能力。

## 修复与防复发

- [容器代理安装手册](../runbooks/install-container-agent.md)改为 ADR 0008 后的系统服务步骤：只有 `anas-container` 加入 `docker` 组，`a-nas` 仅加入 `anas-container` 组，`anas-dev` 不获得两者权限。
- 容器代理二进制和单元必须来自与 API 相同的不可变提交并核对 SHA-256；代理通过 `/run/a-nas-container/agent.sock` 暴露类型化接口。
- 系统服务安装器在代理 socket 已存在时写回 `ANAS_CONTAINERS_MODE=agent`，防止后续升级重建环境文件时意外关闭已经安装的容器能力；socket 缺失时继续 fail closed。
- 完成证据必须同时覆盖 Docker、容器代理、socket 权限、`a-nas` 可达性、API 环境键和登录后的两个桌面窗口。未带管理员会话的 API 请求不能作为正向验证。

## 首次安装续接

2026-10-08 首次按修订手册安装时，`apt-get install --no-install-recommends docker.io docker-compose` 成功安装并启动了 Docker 26.1.5 与 containerd，但随后的 `docker version` 以 `docker: command not found` 停止。Debian 13 的 `docker.io` 只推荐而不依赖 `docker-cli`；禁止推荐包后不会得到 `/usr/bin/docker`。

停止发生在创建 `anas-container` 身份、安装代理和切换 release 之前，因此系统与 Kiosk 仍指向 `ae4b642fe89d`，`c902acded999` 的系统 release 目录尚未创建，原备份保持可用。续接脚本必须显式安装同源同版本的 `docker-cli`，并从当前半安装状态继续；不得重跑会拒绝覆盖备份的原脚本。

运行手册现把 `docker-cli` 列为显式依赖。使用 `--no-install-recommends` 的所有主机安装步骤都应在目标发行版上核对实际二进制归属，不能只依据上游软件包名称推断客户端随守护进程安装。

补装 CLI 后的第一次续接成功启动 Container Agent、Host Agent、API 与 Kiosk；API 明确记录 `version=c902acded999` 和 `containers=true`，并稳定运行约 32 秒。续接脚本随后把不存在的 `/screensaver.mp4` 当作屏保路由，连续收到 404 后误判整体 HTTP 健康失败并自动回滚。事故当时产品和测试共同定义的真实路由是 `/local-console/screensaver.mp4`（`internal/webui/handler.go` 与 `web/src/LocalConsoleScreenSaver.tsx`）。最终切换改为逐项打印 `/healthz`、`/` 和真实屏保路由的状态，避免复合断言隐藏具体失败项；后续视频池探针改为先读清单再请求清单内 URL。

验证脚本属于产品契约的调用者；路由、环境键或 socket 路径不能凭记忆重复书写。优先复用规格/测试中的同一常量；无法直接复用时，运行手册必须链接定义和回归测试，并让每个探针独立输出结果。

修正屏保路径后，最终切换的所有 HTTP 检查均已通过，但代理检查再次误把 `/v1/containers` 当成列表路由。真实代理的 Client 和 Handler 都使用 `/v1/snapshot`，公开产品接口才使用 `/api/v1/containers`。这是部署探针的第二处错误；404 表明请求已到达代理，不能据此认定 Docker 或授权链损坏。失败后两个 current 再次回到 `ae4b642fe89d`。

现将只读检查集中在 [`verify-container-deployment.py`](../../scripts/verify-container-deployment.py)，先以 `a-nas` 身份读取代理的 snapshot 和 app catalog，全部通过才允许切换；切换后复用同一探针，避免复制路径时再次漂移。失败必须报告具体路由和状态；日志验收限定当前服务 InvocationID，避免旧启动记录造成假成功。

2026-10-08 在本地 WSL Docker 29.8.1 上，使用与 NAS 上传制品相同的 `c902acded999` 二进制运行 [`check-container-deployment-probes.py`](../../scripts/check-container-deployment-probes.py)：两个错误路径均复现 404；`/v1/snapshot`、`/v1/apps` 返回 200（目录 26 项），健康/桌面返回 200，当时的真实屏保路由返回 206 和 1024 字节。API 使用临时模拟状态，代理只读真实 Docker；此结果验证了当时的探针协议，不代替 NAS 上 Debian Docker 26.1.5 与 `a-nas` 组权限的切换前检查。

## 实机完成证据

2026-10-08，集中式探针先以 `a-nas` 身份完成切换前只读检查，再在切换后复用同一检查；最终现场满足：

- `/opt/a-nas/current` 与 `/home/anas-dev/apps/a-nas/current` 都指向不可变 release `c902acded999`。
- Docker、containerd、Container Agent、Host Agent、API、Kiosk 与 Samba 全部为 active；Docker Server 为 26.1.5，Compose 为 2.26.1。
- Container Agent 的 `/v1/snapshot` 与 `/v1/apps` 通过，产品服务以 `containers=true` 启动；桌面 Docker 与应用中心不再显示能力未启用提示。
- `/healthz` 和嵌入式桌面返回 200，`/local-console/screensaver.mp4` 的 1024 字节 Range 请求返回 206。
- `a-nas` 只能访问类型化代理 socket，不能读取 Docker socket；`anas-dev` 无权读取两者，符合 ADR 0009 的权限隔离。

两次自动回滚因此不是产品能力连续损坏，而是部署验证代码与真实协议漂移。后续切换必须复用仓库探针，禁止在一次性 shell 中重新手写产品路由；HTTP 404 也必须先按“请求已到达服务但路径不存在”分类，不能直接归因于依赖或权限失败。

## 关联

- 决策：[ADR 0009：通过专用容器代理使用 Docker Engine](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)
- 规格：[容器管理](../specs/container-management.md)、[应用中心](../specs/app-center.md)
- 手册：[安装 Docker 与容器代理](../runbooks/install-container-agent.md)
- 实现：`cmd/anas-api/main.go`、`deploy/systemd/system/anas-container-agent.service`、`scripts/install-v1.0.1-system-services.sh`
