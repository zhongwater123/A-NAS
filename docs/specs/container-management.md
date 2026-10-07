# 容器管理（Docker MVP）

状态：implemented（本地 WSL2 Docker 验证；未部署到 Experimental NAS）
更新时间：2026-10-07

## 目标

设备所有者像使用其他桌面应用一样，在“Docker”窗口中查看容器与镜像、运行状态和资源占用，启动、停止、重启容器并查看日志，而不必登录命令行或第三方面板。

## 非目标

- 不创建、删除、更新容器，不拉取或删除镜像，不编辑 Compose 文件；应用安装与卸载见[应用中心规格](app-center.md)。
- 不提供 `exec`、终端接入或任意 Docker Engine API 透传。
- 不管理网络、卷、Swarm 或远程 Docker 主机。

## 场景

### 查看容器与镜像

- Given：产品服务以 `ANAS_CONTAINERS_MODE=agent` 连接容器代理，Docker Engine 正常。
- When：用户从桌面打开“Docker”。
- Then：窗口显示 Docker 版本、容器总数与运行数；每个容器显示名称、镜像、中文状态、Compose 项目、Docker 状态文本和宿主机端口映射，运行中的容器显示约 1 秒采样的 CPU 与内存；“镜像”页显示标签、大小、创建日期和短 ID。窗口打开期间每 5 秒刷新，页面隐藏时暂停。

### 启停与重启

- Given：容器列表已显示。
- When：用户点击已停止容器的“启动”，或点击运行中容器的“停止”/“重启”。
- Then：启动立即执行；停止和重启先在卡片内显示“服务会中断”的确认，确认后才发出请求。执行期间按钮显示进度，完成后列表刷新；失败时卡片内显示原因（容器已不存在、无法连接 Docker 或引擎拒绝）。停止和重启给容器 10 秒优雅退出时间。

### 查看日志

- Given：容器列表已显示。
- When：用户点击“查看日志”。
- Then：显示最近 200 行 stdout/stderr，按写入顺序交错排列，stderr 以醒目颜色区分，带本地时间；可手动刷新并返回列表。

### 未启用或不可用

- Given：未安装容器代理，或 Docker Engine 不可达。
- When：用户打开“Docker”。
- Then：分别显示“Docker 未启用”和启用方式，或“无法连接 Docker 引擎”与“重新连接”；已读取过列表后断线时保留最后状态并显示中断提示。

## 边界与失败

- `ANAS_CONTAINERS_MODE` 为 `fake`、`agent` 或 `disabled`；未设置时随宿主机状态模式：模拟模式下为 `fake`，真实模式下为 `disabled`，因此实时部署永远不会显示虚构容器。部署激活脚本仅在真实模式且容器代理 socket 存在时写入 `agent`。
- 容器只能用完整 64 位引擎 ID 指定，名称和短 ID 被拒绝，避免同名或前缀冲突作用到其他容器。
- Docker 仅管理员可用：`/api/v1/containers` 需要管理员产品会话，写请求（`POST /api/v1/containers/{id}/actions`）还需要会话的 CSRF 令牌；成员看不到“Docker”图标（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）。写请求另须指向回环 Host、使用 `application/json` 请求体，且浏览器 Origin 必须同源；否则返回 403。请求体只接受 `action` 字段。
- 日志单次最多 500 行、1 MiB；资源采样在 3 秒预算内最多并发 8 个容器，超时的容器不显示占用但仍列出。
- 错误码：`containers_disabled`、`containers_unavailable`（503）、`container_not_found`（404）、`invalid_request`（400）、`container_engine_error`（502）；浏览器只看到通用中文描述，引擎细节只写入服务日志。
- 产品服务与容器代理对启停请求单独放宽写超时到 30 秒，覆盖 10 秒的停止宽限期。
- 容器启停属于可逆的生命周期变化，以界面内确认代替完整执行计划；安装、删除和修改容器配置时必须走“规划、确认、执行”。

## 验收证据

- 领域、Fake 与 Docker 适配器：[Docker 适配器测试](../../internal/containers/docker/manager_test.go) 覆盖多路复用日志解析与截断、TTY 日志、CPU/内存计算、端口去重与非法 ID/状态过滤。
- 代理 IPC：[容器代理测试](../../internal/containers/agent/agent_test.go) 覆盖往返、错误码映射和只开放类型化操作。
- 产品 API：[容器 API 测试](../../internal/containersapi/handler_test.go)、[同源写检查测试](../../internal/localorigin/localorigin_test.go)、[管理员与 CSRF 测试](../../internal/httpapi/apps_test.go)、[OpenAPI 契约测试](../../internal/httpapi/openapi_test.go)。
- 前端：[桌面测试](../../web/src/App.test.tsx) 覆盖列表、停止需确认、启动、日志、镜像页和未启用状态，并以返回 Promise 的 `scrollIntoView` 复现过日志视图卸载导致整页崩溃的缺陷。
- 2026-10-07 在 WSL2（Docker 29.1.3，API 1.52）以用户态容器代理和 `ANAS_CONTAINERS_MODE=agent` 运行：列出 5 个容器与 2 个镜像及运行中容器的 CPU/内存；对专用测试容器 `anas-docker-smoke` 执行启动、重启、停止均返回 204 且与 `docker inspect` 一致；读取日志 98 行；`text/plain` 跨站请求返回 403；Chromium 中完成确认停止、启动、查看日志与返回列表。

## 关联

- ADR：[0009 Docker Engine 与专用容器代理](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)
- 运行手册：[安装容器代理](../runbooks/install-container-agent.md)
- 契约：[OpenAPI](../../api/openapi.yaml)
- 代码：[`internal/containers`](../../internal/containers/containers.go)、[`internal/containersapi`](../../internal/containersapi/handler.go)、[`DockerPanel`](../../web/src/DockerPanel.tsx)
