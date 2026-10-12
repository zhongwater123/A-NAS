# 应用中心

状态：implemented
更新时间：2026-10-12

## 目标

设备所有者在桌面“应用中心”按用途找到经过安全审查的应用，在安装前看清它会拉取哪些镜像、开放哪些端口、使用哪些文件夹，确认后一键安装并看到进度，装好后直接打开，并能卸载而保留数据。

## 非目标

- 不支持任意 Compose 文件、自定义镜像或运行时联网获取应用清单；上游清单中的截图链接不加载。
- 不提供应用参数（端口、环境变量、目录）自定义、升级、备份或多实例安装。
- 不在 A-NAS 窗口中嵌入应用网页，不配置反向代理或 HTTPS。
- 不安装需要设备直通、宿主机网络或特权的应用（如 Jellyfin 硬件转码、Home Assistant）。

## 结构

窗口左侧为侧栏：顶部是搜索框，其下是“发现”“已安装”（显示应用数，有任务进行时显示转圈）和“分类”分组。分类由 A-NAS 在审查清单时指定，依次为影音、阅读、照片、下载、文件与同步、笔记与知识、协作与自动化、监控与安全，每个分类有自己的颜色图标和应用数；没有应用的分类不显示。侧栏底部说明应用均经过安全审查、清单来自 CasaOS AppStore，模拟模式下注明“模拟数据”。视觉语言与[设置](settings.md)窗口一致。

应用卡片显示图标、名称、两行简介和一个操作：“安装”、“打开”（已安装且有网页界面）、“已安装”或转圈的“安装中”“卸载中”；上次安装失败或有容器没有运行时在卡片上注明。点击卡片其余位置进入详情。

窗口内容宽度不超过 720 px 时，侧栏变为顶部可横向滚动的一行，搜索框与各入口保留，分组标题、应用数和底部说明隐藏。

## 场景

### 发现与分类

- Given：产品服务以 `ANAS_CONTAINERS_MODE=agent` 连接容器代理（或模拟模式）。
- When：用户从桌面打开“应用中心”，或点击侧栏中的分类。
- Then：“发现”页顶部是说明审查方式的横幅（安装前可审查、独立账号运行、卸载保留数据），下面按分类分组，每组最多显示 4 个应用，更多时提供“全部 N 个”进入该分类。分类页列出该分类的全部应用。容器代理早于 A-NAS 分类时，应用列在“其他”下。

### 搜索

- When：在侧栏搜索框中输入文字。
- Then：结果页显示命中每个词的应用及数量，名称命中排在分类命中之前，分类或简介命中排在只有介绍命中之前，结果卡片注明分类。没有匹配时提示换个关键词并提供“浏览全部应用”。按 Esc 或点击清除按钮清空搜索；点击侧栏入口也会清空搜索。

### 查看应用

- When：点击应用卡片。
- Then：详情页顶部为返回来源（“发现”、分类名、“已安装”或“搜索结果”）的按钮、大图标、名称、简介、开发者和主操作，下面是版本、分类、开发者、网页端口、清单更新日期一栏。清单提供安装前提示（如默认账号）时显示“使用须知”，并注明内容来自 CasaOS 清单、其中的 CasaOS 设置在 A-NAS 中不适用、文件夹与端口以安装计划为准。介绍按 Markdown 显示，超过 6 行时折叠；“A-NAS 如何运行它”说明审查、独立账号和卸载保留数据；“链接”列出官网、源码与文档。返回列表时恢复原来的滚动位置。

### 规划与确认安装

- Given：应用状态为“可安装”。
- When：用户在卡片或详情页点击“安装”。
- Then：详情页上方打开“安装计划”面板，先显示正在生成计划，随后逐项列出运行身份、使用的文件夹（区分应用数据、共享空间、系统只读）、开放端口与用途、将新建的 Docker 网络及其地址池；拉取的镜像与创建的容器默认折叠为数量，最终 Compose 文件可展开。计划挂载共享空间时，面板顶部以警示说明该权限。底栏固定显示计划摘要、“取消”与“确认安装”。另一个应用正在安装或卸载时，“确认安装”不可用并说明原因。未确认前不执行任何操作；Esc、点击面板外或“取消”关闭面板，焦点回到“安装”按钮。

### 执行安装

- Given：用户已查看安装计划。
- When：点击“确认安装”。
- Then：请求携带计划摘要；容器代理重新渲染并比对摘要，再检查端口与容器名冲突，然后在后台运行 `docker compose up`。面板转为进度：已用时间、不确定进度条、最近一行 Compose 输出和可展开的完整输出，每 1.5 秒刷新；“在后台继续”关闭面板而不中断安装，详情页同样显示进度。成功后面板显示“已安装”、运行中的容器数、端口和使用须知，提供“打开”；详情页显示“已安装 · N/N 个容器运行中”和访问地址，容器同时出现在 Docker 窗口。

### 打开与管理已安装应用

- When：点击卡片、已安装列表或详情页中的“打开”，或打开“已安装”页。
- Then：“打开”在新窗口中打开 `<scheme>://<当前访问 A-NAS 的主机>:<网页端口><路径>`。“已安装”页按行列出已安装、安装中、卸载中和上次安装失败的应用，每行显示运行状态（运行中与端口、部分容器未运行、已用时间或失败），提供“打开”或“安装”。详情页的“更多操作”菜单提供“在 Docker 中查看”（打开 Docker 窗口）和“卸载”。

### 卸载

- Given：应用已安装。
- When：在“更多操作”中选择“卸载”，并在对话框中确认。
- Then：对话框列出将停止并删除容器和 Docker 网络、保留应用数据、立即撤销它获得过的共享空间访问。确认后后台运行 `docker compose down`，删除容器与项目网络，保留应用数据目录；详情页显示卸载进度，完成后应用回到“可安装”。取消时焦点回到“更多操作”。

### 失败与冲突

- Given：安装请求无法执行或执行失败。
- When：出现计划过期、端口或容器名被占用、另一任务正在运行、数据卷不可用、容器代理不可达或 Compose 失败。
- Then：在安装面板中分别提示“应用清单已变化，请重新确认安装计划”（并自动重新生成计划）“端口已被占用（端口与占用者）”“容器名已被占用”“另一个应用正在安装或卸载”“数据卷当前不可用”“无法连接容器代理”；Compose 失败时面板与详情页显示失败原因和展开的输出，并提供重新查看安装计划。未启用时显示“应用中心未启用”，无法连接时提供“重新连接”；读取过列表后连接中断时保留上次的状态并提示。

## 边界与失败

- 清单与安装策略见 [ADR 0010](../adr/0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)；`catalog_test.go` 渲染每个内置应用，确保列出的应用都能通过策略。当前内置 26 个应用，来源提交为 CasaOS-AppStore `0909364`。
- **分类与展示字段**：清单文件保持上游原样；A-NAS 分类写在 [`categories.go`](../../internal/appstore/categories.go)，测试保证每个内置应用都有分类且没有多余条目。使用须知取自清单 `tips.before_install`，介绍取自 `description`（均优先简体中文）；官网、源码与文档链接只保留绝对 http(s) 地址。产品服务把旧版容器代理上报的 CasaOS 分类视为未分类。
- **Markdown 显示**：使用 react-markdown（含 GFM 表格与换行插件）渲染，不渲染原始 HTML，丢弃图片（会从互联网加载），链接在新窗口打开。渲染器单独打包，在应用中心打开时预先加载。
- **打开应用**：本机屏幕同样在新窗口中打开；Kiosk 中新窗口会盖住桌面，浏览器约束见[开放调查](../investigations/2026-10-06-kiosk-browser-confinement.md)。
- 路径：`/DATA/AppData/<app>/…` → `ANAS_APP_DATA_ROOT/<app>/…`（默认数据卷上的 `/srv/a-nas/data/apps`，不经 SMB 发布，成员不可列出）；其余 `/DATA/…`（含整个 `/DATA`）→ `ANAS_SHARED_DATA_ROOT/…`（默认 Shared 共享文件夹 `/srv/a-nas/data/spaces/shared`，绝不映射到数据卷根目录）；`/etc/localtime` 强制只读，`/etc/timezone` 被移除并以 `TZ` 代替。其他应用的数据目录、相对路径、`..` 逃逸和其余宿主机路径一律拒绝。
- **挂载方式**：数据卷上的文件夹不以绑定挂载交给 Docker（Docker 每次启动都会跟随绑定源路径中的符号链接，而能写共享空间或应用自身数据的人可以把其中的文件夹换成指向宿主机的链接）。渲染结果改为两个 A-NAS 卷：`a-nas-appdata` 以 `apps/<app>`、`a-nas-shared` 以共享空间为根（`local` 驱动、`o=bind`，根目录的名称只有 root 能改），具体文件夹作为 `volume.subpath`，并设 `nocopy`。Docker 每次启动在卷内解析子路径，链接指向卷外时拒绝启动；它既不创建卷根也不创建子路径。清单不能声明 `a-nas-` 前缀的卷。子目录挂载需要 Docker Compose 2.30 及以上与 Engine API 1.45 及以上：容器代理在规划时检查，不满足时返回 `409 docker_runtime_outdated`。Compose 2.29 及以前把子目录交给旧的绑定接口，引擎会挂载整个卷（[调查](../investigations/2026-10-09-old-compose-mounted-whole-app-volumes.md)）。安装后，容器代理核对每个容器对 A-NAS 卷的挂载都是计划中的子目录；有任一不符或无法检查时立即 `compose down` 回滚，任务失败并写明容器、卷与挂载点。Docker 来自官方软件源（[ADR 0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)）。
- **运行身份**（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）：每个应用对应 Host Agent 分配的 Linux 账号 `app-<id>`（UID/GID 30000–30999，永不复用）。查看计划只在 Host Agent 登记表中预留 UID，账号在安装前预建文件夹时才创建。计划按该身份渲染：清单显式声明 `user` 时把它改为该 UID/GID，声明 `PUID`/`PGID` 时无论原值是变量还是字面量都改为该 UID/GID，并在安装计划中显示。环境变量插值只提供 `AppID`、`TZ`、`PUID`、`PGID`，不读取容器代理自身环境。既不声明 `user` 也不支持 `PUID`/`PGID` 的镜像保留其镜像默认用户，必须在入库评审中确认它能使用所挂载目录；固定 UID 或 root 初始化等第三种身份策略进入内置目录前需要扩展安装策略并增加兼容性证据。
- **共享空间授权**：计划挂载共享空间内的文件夹时，计划明示“该应用将获得共享空间的读写权限（与成员相同）”；确认安装后 `app-<id>` 加入 `a-nas-users`，从而经共享空间的 ACL 获得访问，新文件继承该 ACL，成员可读写。卸载时立即撤销该成员资格，应用身份与其数据保留。应用身份永远无法访问任何个人空间。
- **文件夹由 Host Agent 预建**：安装前 Host Agent 校验计划中的每个宿主机文件夹只在 `apps/<app>` 或（已同意时）共享空间内，创建 `apps` 子卷（仅 root）、应用数据文件夹（属主 `app-<id>`）与共享空间文件夹（继承 ACL）；每一级都经不跟随符号链接的描述符创建并改属主，路径中出现链接时拒绝。Docker 不会创建任何宿主机文件夹，数据卷离线时应用无法启动，也绝不会把数据写到系统盘。
- 数据卷离线时安装返回 `423 volume_unavailable`，不预建任何文件夹。
- **Docker 网络**（[ADR 0013](../adr/0013-allocate-docker-networks-from-an-a-nas-address-pool.md)）：计划列出 Compose 将新建的网络，包括服务未设 `network_mode: bridge` 时的隐式 `default` 网络。清单自带 `ipam` 子网或驱动的网络被拒绝。应用有网络而 Docker 未配置 `default-address-pools` 时，规划与安装都返回 `409 address_pool_missing`。安装后，项目网络的每个子网都必须落在地址池内；有任一不在池内或无法检查时，立即 `compose down` 回滚，任务失败并写明网络与网段。
- 局限：rootful Docker 中既未声明 `user` 也不支持 `PUID`/`PGID`、因而仍以 root 运行的镜像会以 root 写入其挂载的文件夹；隔离依赖“只挂载被允许的文件夹”。
- 端口冲突检查基于 Docker 已发布端口；未被容器占用但被宿主机进程监听的端口由 Compose 启动失败暴露在任务日志中。
- 一次只运行一个安装或卸载任务；安装超时 30 分钟、卸载 5 分钟，任务保留最近 40 行输出，任务状态保存在容器代理内存中，重启后丢失但已安装状态由容器标签恢复。
- 应用中心仅管理员可用：所有 `/api/v1/apps` 请求需要管理员产品会话，写请求还需要会话的 CSRF 令牌；成员看不到“应用中心”图标。写请求另要求 Host 为 `localhost` 或 IP 地址（[局域网 Web 访问](lan-web-access.md)）、JSON 请求体与同源 Origin；安装请求只接受 `digest` 字段，无法提交 Compose 内容。图标以沙箱化 CSP 与 `nosniff` 返回。

## 验收证据

- 渲染与策略：[渲染测试](../../internal/appstore/render_test.go) 覆盖路径重写、显式 `user` 及字面量 `PUID`/`PGID` 改为应用身份、不让 Docker 创建宿主机文件夹、`/DATA` 只映射到共享空间、缺少身份时拒绝、确定性摘要与 16 类拒绝情形，以及计划列出隐式 `default` 网络、默认网桥不新建网络、清单自带子网被拒绝；[清单测试](../../internal/appstore/catalog_test.go) 另覆盖每个内置应用都有 A-NAS 分类、使用须知与链接的读取以及非 http(s) 链接被丢弃，[分类测试](../../internal/appstore/categories_test.go) 覆盖分类表不含清单外的应用。
- 身份与文件夹：[Host Agent 应用身份测试](../../internal/hostops/linux/apps_test.go)（查看计划不改动宿主机账号）；root 集成测试 [`root_apps_integration_test.go`](../../internal/hostops/linux/root_apps_integration_test.go) 在真实 Btrfs 上以绑定挂载模拟容器，验证账号在预建文件夹时才创建、应用数据属主、共享空间读写与 ACL 继承、无法读取个人空间、经植入的符号链接预建文件夹被拒绝、撤销后不可写共享空间但保留自身数据。
- 2026-10-07 WSL2（Docker 29.1.3、Compose 2.40.3）：以渲染结果运行一次性 Compose 项目，共享空间中被换成指向卷外目录的链接的文件夹使容器拒绝启动（`path concatenation escapes the base directory`），换回真实文件夹后正常挂载，写入文件属主为应用 UID；卷根不存在时挂载失败且 Docker 不创建它。
- 2026-10-08 Experimental NAS（Debian 13 自带 Docker 26.1.5、Compose 2.26.1）：真实拉取并安装 OpenList 4.2.2，以 `app-openlist` 运行，见 [OpenList 运行身份调查](../investigations/2026-10-08-openlist-runtime-identity.md)。
- 产品接口：[应用中心 API 测试](../../internal/appstoreapi/handler_test.go) 覆盖安装前预建文件夹、卸载撤销与数据卷离线拒绝；[管理员与 CSRF 测试](../../internal/httpapi/apps_test.go)。
- 执行层：[引擎测试](../../internal/appstore/engine/engine_test.go) 覆盖按确认计划写入并执行、摘要不符、端口冲突、并发任务、卸载保留数据与失败输出；`TestAppNetworksNeedAnAddressPool`、`TestInstallRollsBackANetworkOutsideThePool` 与 `TestInstallKeepsANetworkInsideThePool` 覆盖没有地址池时拒绝、网络不在池内时回滚与池内正常安装。`TestPlanRefusesADockerThatDropsVolumeSubpaths`、`TestInstallRollsBackAMountBeyondItsFolder` 与 `TestInstallKeepsMountsOnTheirFolders` 覆盖过旧的 Compose 或 Engine API 被拒绝、挂载超出计划文件夹时回滚与正确挂载时正常安装。
- 协议与 API：[代理测试](../../internal/appstore/agent/agent_test.go)（含分类、使用须知与链接往返，以及丢弃旧版代理的 CasaOS 分类）、[应用中心 API 测试](../../internal/appstoreapi/handler_test.go)、[OpenAPI 契约测试](../../internal/httpapi/openapi_test.go)。
- 前端：[应用中心测试](../../web/src/appcenter/AppCenterPanel.test.tsx) 覆盖分类浏览、按命中位置排序的搜索与 Esc 清空、Markdown 与使用须知（表格、无图片、新窗口链接）、计划 → 进度 → 完成并按当前主机打开应用、关闭计划后焦点回到“安装”、失败原因与重新安装、另一任务进行时禁止确认、已安装列表与从菜单卸载（取消时焦点回到菜单按钮）、未分类应用列在“其他”，写请求都带 CSRF 令牌；[桌面测试](../../web/src/App.test.tsx) 覆盖从桌面打开后按摘要安装、端口冲突提示、未启用状态、Docker 网络与地址池显示、地址池缺失与数据卷不可用提示。
- 2026-10-12 Chromium，模拟模式：在 1180×780（默认窗口与最大化）和 800×660（窄窗口横条）下检查发现、分类、搜索、已安装、详情、安装计划、进度、完成、卸载确认与卸载进度；以临时替换的接口数据检查安装失败、部分容器未运行与安装中的列表和详情。
- 2026-10-07 WSL2（Docker 29.1.3、Compose 2.40.3）：通过产品 API 与容器代理安装 Memos——本机 Docker 无法访问 Docker Hub，首次安装如实失败并显示拉取错误；以本地镜像临时标记为 `neosmemo/memos:0.28.0` 后，安装成功（容器带 `a-nas-memos` 项目与 `io.a-nas.app` 标签，绑定目录重写到应用数据根，Docker 窗口可见），重复安装返回 `already_installed`，卸载后容器与网络删除且数据保留；过期摘要返回 `plan_changed`，跨站请求返回 `forbidden`。Chromium 中模拟模式完成浏览、计划、安装、卸载全流程，26 个图标全部加载。临时镜像标签与测试数据已清理。

## 关联

- ADR：[0010 内置清单与安装策略](../adr/0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)、[0009 Docker Engine 与专用容器代理](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)
- 规格：[容器管理](container-management.md)
- 运行手册：[安装 Docker 与容器代理](../runbooks/install-container-agent.md)
- 规格：[设置](settings.md)（视觉语言）
- 调查：[Kiosk 浏览器约束](../investigations/2026-10-06-kiosk-browser-confinement.md)
- 代码：[`internal/appstore`](../../internal/appstore/appstore.go)、[分类](../../internal/appstore/categories.go)、[`internal/appstoreapi`](../../internal/appstoreapi/handler.go)、[应用中心界面](../../web/src/appcenter/AppCenterPanel.tsx)
