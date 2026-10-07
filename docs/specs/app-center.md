# 应用中心

状态：implemented（本地 WSL2 以真实 Docker Compose 验证安装/卸载流程；镜像拉取与 Experimental NAS 未验证）
更新时间：2026-10-07

## 目标

设备所有者在桌面“应用中心”浏览经过安全审查的应用，看清安装会拉取哪些镜像、开放哪些端口、使用哪些文件夹，确认后一键安装，并能卸载而保留数据。

## 非目标

- 不支持任意 Compose 文件、自定义镜像或运行时联网获取应用清单。
- 不提供应用参数（端口、环境变量、目录）自定义、升级、备份或多实例安装。
- 不在 A-NAS 窗口中嵌入应用网页，不配置反向代理或 HTTPS。
- 不安装需要设备直通、宿主机网络或特权的应用（如 Jellyfin 硬件转码、Home Assistant）。

## 场景

### 浏览与搜索

- Given：产品服务以 `ANAS_CONTAINERS_MODE=agent` 连接容器代理（或模拟模式）。
- When：用户从桌面打开“应用中心”。
- Then：显示内置清单中的全部应用卡片（图标、名称、简介、状态徽章），可按名称/简介搜索并按分类筛选；状态为“可安装”“已安装”“安装中”“卸载中”或“安装失败”。

### 规划与确认安装

- Given：应用状态为“可安装”。
- When：用户在详情页点击“安装”。
- Then：容器代理渲染安装计划并显示将拉取的镜像、开放的宿主机端口与用途、使用的文件夹（区分应用数据、共享数据、系统只读）、将创建的容器，以及可展开的最终 Compose 文件和计划摘要；未确认前不执行任何操作。

### 执行安装

- Given：用户已查看安装计划。
- When：点击“确认安装”。
- Then：请求携带计划摘要；容器代理重新渲染并比对摘要，再检查端口与容器名冲突，然后在后台运行 `docker compose up`。界面显示“安装中”与最近的 Compose 输出，每 1.5 秒刷新；成功后显示“已安装 · N/N 个容器运行中”和访问端口，容器同时出现在 Docker 窗口。

### 卸载

- Given：应用已安装。
- When：用户点击“卸载”并确认。
- Then：后台运行 `docker compose down`，删除容器与项目网络，保留应用数据目录；完成后应用回到“可安装”。

### 失败与冲突

- Given：安装请求无法执行或执行失败。
- When：出现计划过期、端口或容器名被占用、另一任务正在运行、容器代理不可达或 Compose 失败。
- Then：分别提示“应用清单已变化，请重新确认”“端口已被占用（端口与占用者）”“容器名已被占用”“另一个应用正在安装或卸载”“无法连接容器代理”，或在任务日志中显示失败原因与 Compose 输出；未启用时显示“应用中心未启用”。

## 边界与失败

- 清单与安装策略见 [ADR 0010](../adr/0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)；`catalog_test.go` 渲染每个内置应用，确保列出的应用都能通过策略。当前内置 26 个应用，来源提交为 CasaOS-AppStore `0909364`。
- 路径：`/DATA/AppData/<app>/…` → `ANAS_APP_DATA_ROOT/<app>/…`（默认数据卷上的 `/srv/a-nas/data/apps`，不经 SMB 发布，成员不可列出）；其余 `/DATA/…`（含整个 `/DATA`）→ `ANAS_SHARED_DATA_ROOT/…`（默认 Shared 共享文件夹 `/srv/a-nas/data/spaces/shared`，绝不映射到数据卷根目录）；`/etc/localtime` 强制只读，`/etc/timezone` 被移除并以 `TZ` 代替。其他应用的数据目录、相对路径、`..` 逃逸和其余宿主机路径一律拒绝。
- **挂载方式**：数据卷上的文件夹不以绑定挂载交给 Docker（Docker 每次启动都会跟随绑定源路径中的符号链接，而能写共享空间或应用自身数据的人可以把其中的文件夹换成指向宿主机的链接）。渲染结果改为两个 A-NAS 卷：`a-nas-appdata` 以 `apps/<app>`、`a-nas-shared` 以共享空间为根（`local` 驱动、`o=bind`，根目录的名称只有 root 能改），具体文件夹作为 `volume.subpath`，并设 `nocopy`。Docker 每次启动在卷内解析子路径，链接指向卷外时拒绝启动；它既不创建卷根也不创建子路径。清单不能声明 `a-nas-` 前缀的卷。需要 Docker Engine 26 及以上（Debian 13 的 `docker.io` 为 26.1）。
- **运行身份**（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）：每个应用对应 Host Agent 分配的 Linux 账号 `app-<id>`（UID/GID 30000–30999，永不复用）。查看计划只在 Host Agent 登记表中预留 UID，账号在安装前预建文件夹时才创建。计划按该身份渲染，`PUID`/`PGID` 即其 UID/GID，并在安装计划中显示。环境变量插值只提供 `AppID`、`TZ`、`PUID`、`PGID`，不读取容器代理自身环境。
- **共享空间授权**：计划挂载共享空间内的文件夹时，计划明示“该应用将获得共享空间的读写权限（与成员相同）”；确认安装后 `app-<id>` 加入 `a-nas-users`，从而经共享空间的 ACL 获得访问，新文件继承该 ACL，成员可读写。卸载时立即撤销该成员资格，应用身份与其数据保留。应用身份永远无法访问任何个人空间。
- **文件夹由 Host Agent 预建**：安装前 Host Agent 校验计划中的每个宿主机文件夹只在 `apps/<app>` 或（已同意时）共享空间内，创建 `apps` 子卷（仅 root）、应用数据文件夹（属主 `app-<id>`）与共享空间文件夹（继承 ACL）；每一级都经不跟随符号链接的描述符创建并改属主，路径中出现链接时拒绝。Docker 不会创建任何宿主机文件夹，数据卷离线时应用无法启动，也绝不会把数据写到系统盘。
- 数据卷离线时安装返回 `423 volume_unavailable`，不预建任何文件夹。
- 局限：rootful Docker 中仍以 root 运行的镜像会以 root 写入其挂载的文件夹；隔离依赖“只挂载被允许的文件夹”。
- 端口冲突检查基于 Docker 已发布端口；未被容器占用但被宿主机进程监听的端口由 Compose 启动失败暴露在任务日志中。
- 一次只运行一个安装或卸载任务；安装超时 30 分钟、卸载 5 分钟，任务保留最近 40 行输出，任务状态保存在容器代理内存中，重启后丢失但已安装状态由容器标签恢复。
- 应用中心仅管理员可用：所有 `/api/v1/apps` 请求需要管理员产品会话，写请求还需要会话的 CSRF 令牌；成员看不到“应用中心”图标。写请求另要求回环 Host、JSON 请求体与同源 Origin；安装请求只接受 `digest` 字段，无法提交 Compose 内容。图标以沙箱化 CSP 与 `nosniff` 返回。

## 验收证据

- 渲染与策略：[渲染测试](../../internal/appstore/render_test.go) 覆盖路径重写、`PUID`/`PGID` 取自应用身份、不让 Docker 创建宿主机文件夹、`/DATA` 只映射到共享空间、缺少身份时拒绝、确定性摘要与 16 类拒绝情形；[清单测试](../../internal/appstore/catalog_test.go)。
- 身份与文件夹：[Host Agent 应用身份测试](../../internal/hostops/linux/apps_test.go)（查看计划不改动宿主机账号）；root 集成测试 [`root_apps_integration_test.go`](../../internal/hostops/linux/root_apps_integration_test.go) 在真实 Btrfs 上以绑定挂载模拟容器，验证账号在预建文件夹时才创建、应用数据属主、共享空间读写与 ACL 继承、无法读取个人空间、经植入的符号链接预建文件夹被拒绝、撤销后不可写共享空间但保留自身数据。
- 2026-10-07 WSL2（Docker 29.1.3、Compose 2.40.3）：以渲染结果运行一次性 Compose 项目，共享空间中被换成指向卷外目录的链接的文件夹使容器拒绝启动（`path concatenation escapes the base directory`），换回真实文件夹后正常挂载，写入文件属主为应用 UID；卷根不存在时挂载失败且 Docker 不创建它。Debian 13 自带的 Compose 2.26.1 尚未实测。
- 产品接口：[应用中心 API 测试](../../internal/appstoreapi/handler_test.go) 覆盖安装前预建文件夹、卸载撤销与数据卷离线拒绝；[管理员与 CSRF 测试](../../internal/httpapi/apps_test.go)。
- 执行层：[引擎测试](../../internal/appstore/engine/engine_test.go) 覆盖按确认计划写入并执行、摘要不符、端口冲突、并发任务、卸载保留数据与失败输出。
- 协议与 API：[代理测试](../../internal/appstore/agent/agent_test.go)、[应用中心 API 测试](../../internal/appstoreapi/handler_test.go)、[OpenAPI 契约测试](../../internal/httpapi/openapi_test.go)。
- 前端：[桌面测试](../../web/src/App.test.tsx) 覆盖搜索、计划确认、按摘要安装、已安装状态、卸载确认、端口冲突提示与未启用状态。
- 2026-10-07 WSL2（Docker 29.1.3、Compose 2.40.3）：通过产品 API 与容器代理安装 Memos——本机 Docker 无法访问 Docker Hub，首次安装如实失败并显示拉取错误；以本地镜像临时标记为 `neosmemo/memos:0.28.0` 后，安装成功（容器带 `a-nas-memos` 项目与 `io.a-nas.app` 标签，绑定目录重写到应用数据根，Docker 窗口可见），重复安装返回 `already_installed`，卸载后容器与网络删除且数据保留；过期摘要返回 `plan_changed`，跨站请求返回 `forbidden`。Chromium 中模拟模式完成浏览、计划、安装、卸载全流程，26 个图标全部加载。临时镜像标签与测试数据已清理。

## 关联

- ADR：[0010 内置清单与安装策略](../adr/0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)、[0009 Docker Engine 与专用容器代理](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)
- 规格：[容器管理](container-management.md)
- 运行手册：[安装 Docker 与容器代理](../runbooks/install-container-agent.md)
- 代码：[`internal/appstore`](../../internal/appstore/appstore.go)、[`internal/appstoreapi`](../../internal/appstoreapi/handler.go)、[`AppCenterPanel`](../../web/src/AppCenterPanel.tsx)
