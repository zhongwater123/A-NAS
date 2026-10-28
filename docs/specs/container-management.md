# 容器管理（Docker MVP）

状态：implemented
更新时间：2026-10-13

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
- Then：窗口与“设置”“应用中心”同一视觉语言，侧栏有搜索框、“全部容器”“运行中”“已停止”“镜像”及各自数量，有容器出问题时出现“需要关注”；下方“分组”按 Compose 项目列出，应用中心安装的项目（`a-nas-<应用 ID>`）显示应用名称与图标，未属于项目的容器归入“独立容器”，含有问题容器的分组带红点；侧栏底部显示 Docker 与 API 版本，模拟数据时注明。“全部容器”顶部汇总运行数与 CPU、内存合计，列表按分组排列。每个容器一行，显示名称、状态标签、已发布的 TCP 端口（运行中时可点击，在新窗口以当前访问地址打开）、中文化的运行时长或停止时间（含非正常的退出码）与镜像，运行中的容器显示约 1 秒采样的 CPU 与内存。“镜像”页按创建时间倒序列出仓库名、标签、镜像仓库地址、短 ID、使用它的容器（可点击打开该容器）或“未使用”、大小和创建距今时间，并汇总未被容器使用的数量。搜索同时匹配容器名称、镜像、项目、应用名称和镜像标签。窗口打开期间每 5 秒刷新，页面隐藏时暂停、重新可见时立即刷新。

### 状态与需要关注

- Given：容器列表已显示。
- When：引擎报告容器的状态与状态文本（如 `Up 3 hours (healthy)`、`Exited (1) 2 days ago`）。
- Then：运行中显示“运行中”（健康检查失败为“不健康”，检查中为“启动中”），暂停为“已暂停”，重启循环为“重启中”，未启动为“未启动”，引擎无法清理为“异常”；退出码为 0、130、137、143 的停止视为主动停止，显示“已停止”，其他退出码显示“异常退出”。不健康、异常退出、重启中与异常的容器进入“需要关注”。无法识别的状态文本原样显示。

### 容器详情

- Given：容器列表已显示。
- When：用户点击容器名称，或在镜像页点击使用它的容器。
- Then：进入该容器的详情页，可返回原列表并恢复滚动位置。头部显示应用图标或状态图标、名称、状态与时长，以及“打开”（应用自己的网页端口优先）、“重启”“停止”或“启动”。“概览”在需要关注时先说明问题（如“容器异常退出（退出码 1）”）并提供“查看日志”；运行中显示 CPU、内存（占内存上限的比例）与健康检查；“详细信息”列出镜像、分组、端口映射与仅容器内端口、引擎状态原文、创建时间和完整容器 ID。容器在详情页期间消失时显示“容器已不存在”。

### 从应用中心查看

- Given：应用已从应用中心安装。
- When：用户在应用详情中点击“在 Docker 中查看”。
- Then：打开或前置 Docker 窗口，直接显示该应用的分组：应用图标与名称、Compose 项目名、该应用的“打开”按钮、运行数与 CPU、内存合计和它的容器。

### 启停与重启

- Given：容器列表已显示。
- When：用户点击已停止容器的“启动”，或点击运行中容器的“停止”/“重启”。
- Then：启动立即执行；停止和重启先在窗口内弹出确认（属于应用的容器写明哪个应用会受影响），Esc、取消或点击空白处放弃，确认后才发出请求。执行期间列表与详情页的按钮都显示进度，完成后列表刷新；失败时在该行和详情页显示原因（容器已不存在、无法连接 Docker 或引擎拒绝）。停止和重启给容器 10 秒优雅退出时间。

### 查看日志

- Given：容器列表已显示。
- When：用户点击行内的日志按钮，或在详情页切换到“日志”。
- Then：显示最近 200 行（可选 500 行）stdout/stderr，按写入顺序交错排列，stderr 以醒目颜色和左侧色条区分，带本地时间。运行中的容器默认每 3 秒自动刷新，停在底部时跟随最新一行，向上滚动阅读时不再跳动；可关闭自动刷新、手动刷新、按关键词筛选并高亮匹配、把当前显示的行下载为 `.log` 文件。底部显示行数（筛选时为匹配数）与更新时间。

### 未启用或不可用

- Given：未安装容器代理，或 Docker Engine 不可达。
- When：用户打开“Docker”。
- Then：分别显示“Docker 未启用”和启用方式，或“无法连接 Docker 引擎”与“重新连接”；已读取过列表后断线时保留最后状态、显示中断提示，侧栏底部的状态点变红。没有任何容器时提示从应用中心安装并提供“打开应用中心”。

## 边界与失败

- `ANAS_CONTAINERS_MODE` 为 `fake`、`agent` 或 `disabled`；未设置时随宿主机状态模式：模拟模式下为 `fake`，真实模式下为 `disabled`，因此实时部署永远不会显示虚构容器。部署激活脚本仅在真实模式且容器代理 socket 存在时写入 `agent`。
- 容器只能用完整 64 位引擎 ID 指定，名称和短 ID 被拒绝，避免同名或前缀冲突作用到其他容器。
- Docker 仅管理员可用：`/api/v1/containers` 需要管理员产品会话，写请求（`POST /api/v1/containers/{id}/actions`）还需要会话的 CSRF 令牌；成员看不到“Docker”图标（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）。写请求另须指向回环 Host、使用 `application/json` 请求体，且浏览器 Origin 必须同源；否则返回 403。请求体只接受 `action` 字段。
- 日志单次最多 500 行、1 MiB；资源采样在 3 秒预算内最多并发 8 个容器，超时的容器不显示占用但仍列出。
- 每个容器带引擎报告的镜像 ID（`imageId`），镜像页据此判断使用者；旧版容器代理不报告时，按去掉 `docker.io/`、`library/` 与摘要后的镜像引用匹配标签。
- 应用名称、图标与网页端口来自 `/api/v1/apps`；应用中心不可用时 Docker 仍可使用，分组改为显示 Compose 项目名。
- 端口链接假定 HTTP（应用中心应用自己的网页端口按其清单的协议与路径），以浏览器当前访问的主机名打开；非 HTTP 服务的端口打开后会失败。
- 错误码：`containers_disabled`、`containers_unavailable`（503）、`container_not_found`（404）、`invalid_request`（400）、`container_engine_error`（502）；浏览器只看到通用中文描述，引擎细节只写入服务日志。
- 产品服务与容器代理对启停请求单独放宽写超时到 30 秒，覆盖 10 秒的停止宽限期。
- 容器启停属于可逆的生命周期变化，以界面内确认代替完整执行计划；安装、删除和修改容器配置时必须走“规划、确认、执行”。

## 验收证据

- 领域、Fake 与 Docker 适配器：[Docker 适配器测试](../../internal/containers/docker/manager_test.go) 覆盖多路复用日志解析与截断、TTY 日志、CPU/内存计算、端口去重与非法 ID/状态过滤。
- 代理 IPC：[容器代理测试](../../internal/containers/agent/agent_test.go) 覆盖往返、错误码映射和只开放类型化操作。
- 产品 API：[容器 API 测试](../../internal/containersapi/handler_test.go)、[同源写检查测试](../../internal/localorigin/localorigin_test.go)、[管理员与 CSRF 测试](../../internal/httpapi/apps_test.go)、[OpenAPI 契约测试](../../internal/httpapi/openapi_test.go)。
- 前端：[状态模型测试](../../web/src/docker/model.test.ts) 覆盖 Docker 状态文本的中文化、主动停止与异常退出的区分、筛选、分组、搜索、镜像使用者匹配与端口链接；[Docker 窗口测试](../../web/src/docker/DockerPanel.test.tsx) 覆盖按应用分组、需要关注、异常退出的诊断与日志、停止确认与取消、从应用中心定位分组、镜像使用者与搜索、未启用状态，以及独立容器的停止确认、启动、日志筛选与离开日志后的镜像页；[桌面测试](../../web/src/App.test.tsx) 覆盖未运行的已安装应用从桌面图标打开 Docker。日志视图曾因 Chromium 的 `scrollIntoView` 返回 Promise 在卸载时让整页崩溃，现改为直接设置滚动位置。
- 2026-10-13 在 Fake 模式预览（8 个容器、10 个镜像，含应用分组、异常退出与未使用镜像）中用 Chromium 检查全部容器、需要关注、应用分组、详情、日志筛选、镜像、停止确认与搜索各页面。
- 2026-10-07 在 WSL2（Docker 29.1.3，API 1.52）以用户态容器代理和 `ANAS_CONTAINERS_MODE=agent` 运行：列出 5 个容器与 2 个镜像及运行中容器的 CPU/内存；对专用测试容器 `anas-docker-smoke` 执行启动、重启、停止均返回 204 且与 `docker inspect` 一致；读取日志 98 行；`text/plain` 跨站请求返回 403；Chromium 中完成确认停止、启动、查看日志与返回列表。

## 关联

- ADR：[0009 Docker Engine 与专用容器代理](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)
- 运行手册：[安装容器代理](../runbooks/install-container-agent.md)
- 契约：[OpenAPI](../../api/openapi.yaml)
- 规格：[应用中心](app-center.md)
- 代码：[`internal/containers`](../../internal/containers/containers.go)、[`internal/containersapi`](../../internal/containersapi/handler.go)、[`DockerPanel`](../../web/src/docker/DockerPanel.tsx)、[状态模型](../../web/src/docker/model.ts)
