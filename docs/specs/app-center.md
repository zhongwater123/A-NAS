# 应用中心

状态：implemented
更新时间：2026-10-28

## 目标

设备所有者在桌面“应用中心”按用途找到经过安全审查的应用，在安装前看清它会拉取哪些镜像、开放哪些端口、使用哪些文件夹，确认后一键安装并看到进度，装好后直接打开，并能卸载而保留数据。

## 非目标

- 不支持任意 Compose 文件、自定义镜像或运行时联网获取应用清单；上游清单中的截图链接不加载。
- 不提供应用参数（端口、环境变量、目录）自定义、升级、备份或多实例安装。
- 不在 A-NAS 窗口中嵌入应用网页，不配置反向代理或 HTTPS。
- 不安装需要设备直通、宿主机网络或特权的应用（如 Jellyfin 硬件转码、Home Assistant）。

## 结构

窗口左侧为侧栏：顶部是搜索框，其下是“发现”“已安装”（显示应用数，有任务进行时显示转圈）和“分类”分组。分类由 A-NAS 在审查清单时指定，依次为影音、阅读、照片、下载、文件与同步、笔记与知识、协作与自动化、实用工具、网络工具、监控与安全，每个分类有自己的颜色图标和应用数；没有应用的分类不显示。侧栏底部说明应用均经过安全审查、清单来自 CasaOS AppStore，模拟模式下注明“模拟数据”。视觉语言与[设置](settings.md)窗口一致。

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
- Then：详情页顶部为返回来源（“发现”、分类名、“已安装”或“搜索结果”）的按钮、大图标、名称、简介、开发者和主操作，下面是版本、分类、开发者、网页端口、清单更新日期一栏。清单提供安装前提示（如默认账号）时显示“使用须知”。清单的提示讲的是 CasaOS 的设置步骤或 `/DATA` 路径时，须知改为按 A-NAS 写的版本（文件在共享空间的哪个文件夹、去哪里看日志、哪些设置在 A-NAS 中改不了），清单原文收进默认折叠的“上游原始说明”，并注明其中的设置步骤和路径针对 CasaOS、在 A-NAS 中不适用；其余应用的须知注明来自上游清单、文件夹与端口以安装计划为准。介绍按 Markdown 显示，超过 6 行时折叠；介绍中把应用当作 Zima 设备卖点的段落（如“无限存储容量”）不显示。页脚保留清单来源与许可证，并说明介绍和使用须知已按 A-NAS 校订。“A-NAS 如何运行它”说明审查、独立账号和卸载保留数据；“链接”列出官网、源码与文档。返回列表时恢复原来的滚动位置。

### 规划与确认安装

- 生成计划与确认安装时均检查系统状态文件系统（`/var/lib`）和 Docker 实际 `data-root` 的剩余空间。任一处不足总容量的 5% 或 2 GiB（取较大者）时返回 `system_storage_full`，提示查看设置概览并卸载不用的应用；观测失败时拒绝安装。该检查是安装前的最低余量，不是镜像体积估计或磁盘配额，不能保证任意镜像拉取后仍保有该余量。
- Given：应用状态为“可安装”。
- When：用户在卡片或详情页点击“安装”。
- Then：详情页上方打开“安装计划”面板，先显示正在生成计划，随后逐项列出运行身份（应用账号与每个服务的运行方式：应用账号、初始化后降权或镜像自带身份，见下文“运行身份”）、使用的文件夹（区分应用数据、共享空间、系统只读，并标出只读）、开放端口与用途、将新建的 Docker 网络及其地址池，以及 A-NAS 为这次安装生成的登录信息（见下文“生成的登录信息”）；拉取的镜像与创建的容器默认折叠为数量，最终 Compose 文件可展开。计划挂载共享空间时，面板顶部以警示说明该权限，并区分“将能读取共享空间中的 N 个文件夹，不能修改或删除其中的文件”与“将能读写共享空间中的 N 个文件夹，与成员的权限相同”。底栏固定显示计划摘要、“取消”与“确认安装”。另一个应用正在安装或卸载时，“确认安装”不可用并说明原因。未确认前不执行任何操作；Esc、点击面板外或“取消”关闭面板，焦点回到“安装”按钮。

### 执行安装

- Given：用户已查看安装计划。
- When：点击“确认安装”。
- Then：请求携带计划摘要；容器代理重新渲染并比对摘要，再检查端口与容器名冲突，然后在后台逐个服务拉取缺少的镜像（见[镜像下载与重试](#镜像下载与重试)），全部就绪后运行 `docker compose up --pull never`，再观察约 20 秒：有容器不断重启、以非零退出码退出、没有启动或健康检查失败时，取 Compose 日志最后 20 行写入输出，执行 `compose down` 回滚，任务以“容器 n8n 启动后不断重启（退出码 1），安装已回滚”这样的原因失败。`up` 本身出错时（例如端口被宿主机进程占用，或 Linkwarden 的 postgres 没有及时通过健康检查，此时已有容器在运行）同样取日志并回滚，任务以“Docker Compose 没有完成启动（…），安装已回滚”失败。面板转为进度：已用时间、不确定进度条、最近一行 Compose 输出和可展开的完整输出，每 1.5 秒刷新；“在后台继续”关闭面板而不中断安装，详情页同样显示进度。成功后面板显示“已安装”、运行中的容器数、端口、登录信息和使用须知，提供“打开”；详情页显示“已安装 · N/N 个容器运行中”、访问地址和登录信息，容器同时出现在 Docker 窗口。安装后才停止运行的容器在详情页逐个说明原因（不断重启、已退出及退出码、没有启动），并提示在 Docker 中查看日志。

安装前置检查先于宿主机准备：不在容器代理目录中的 ID 不保留 UID。确认安装时，容器代理先校验摘要、端口与名称冲突以及已安装/忙碌状态，接受后预留唯一任务位置；产品服务再让 Host Agent 创建账号和目录，随后提交预留，容器代理读取实际 Linux 账号并核对计划身份后才启动 Compose。预留一次性使用，准备失败时取消；请求中断导致无人提交时，一分钟后任务失败并释放位置。接受前被拒绝的请求不创建账号和共享目录。

停止或处于 created 状态的容器仍占用其 `HostConfig.PortBindings` 中的宿主机端口；相同端口是硬冲突。运行中动态分配的端口也从 Docker 列表中保留。停止容器检查遇到 NotFound 表示已删除，可以跳过；其他检查失败拒绝本次安装，不能将它当作没有占用。

### 打开与管理已安装应用

- When：点击卡片、已安装列表或详情页中的“打开”，或打开“已安装”页。
- Then：“打开”在新窗口中打开 `<scheme>://<当前访问 A-NAS 的主机>:<网页端口><路径>`。应用网页与桌面在同一地址的不同端口，浏览器会把桌面会话 Cookie 一并发给应用；该 Cookie 只在登录它的客户端上有效，应用无法用它访问 A-NAS，也不能嵌入桌面（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md) 决定 7）。“已安装”页按行列出已安装、安装中、卸载中和上次安装失败的应用，每行显示运行状态（运行中与端口、部分容器未运行、已用时间或失败），提供“打开”或“安装”。详情页的“更多操作”菜单提供“在 Docker 中查看”（打开 Docker 窗口）和“卸载”。

### 按旧规则安装的应用

- Given：应用的容器在 [ADR 0022](../adr/0022-reach-shared-folders-only-as-the-app-identity.md) 的运行规则之前创建（没有 `io.a-nas.runtime` 标签或版本较旧）。
- When：打开它的详情页。
- Then：运行状态中提示“它按旧的运行规则安装，容器仍以安装时的身份与权限运行。卸载（应用数据会保留）后重新安装，即按当前规则运行”。系统不自动重装；卸载与重装沿用现有流程，登录信息与应用数据保留。

### 从桌面打开已安装的应用

- Given：应用已安装，且清单声明了网页端口。
- When：任何已登录的账号（管理员或成员）查看桌面或启动器。
- Then：应用以自己的图标（应用目录中的图标，白色圆角底）和名称出现在桌面图标之后与启动器中，可以像其他图标一样拖动排序。点击后在新窗口打开 `<scheme>://<当前访问 A-NAS 的主机>:<网页端口><路径>`，与应用中心的“打开”相同，不需要经过应用中心或 Docker。应用的容器全部没有运行时，图标变灰并标注“未运行”：管理员点击后在 Docker 中查看这个应用的容器，成员的图标不可点击。安装中、卸载中、未安装或没有网页端口的应用不出现；卸载后图标消失。桌面在可见时每 15 秒重新读取一次已安装的应用，因此在别处安装、停止或卸载的应用不用刷新页面就会更新。

### 卸载

- Given：应用已安装。
- When：在“更多操作”中选择“卸载”，并在对话框中确认。
- Then：对话框列出将删除容器、Docker 网络、匿名卷与未被其他容器使用的镜像，保留应用数据和命名卷，并撤销共享空间访问。确认后后台先停止容器，再通过 Docker Engine 删除容器与匿名卷，随后由 `docker compose down` 清理网络；不使用会删除命名卷的 `down --volumes`。镜像只在没有任何运行中或已停止容器使用时删除，不强制删除；详情页显示卸载进度，完成后应用回到“可安装”。取消时焦点回到“更多操作”。
- 清理前将镜像 ID 持久化到容器代理的 `apps/<app>/images.json`，因此容器已删除但镜像删除失败、或代理重启后仍能继续。清理失败显示“卸载未完成”和“重试卸载”，不当成安装失败；重新安装也先完成残留清理。安装回滚、中断恢复使用同一清理流程，命名卷和应用数据始终保留。
- Docker 因多标签或其他依赖拒绝按 ID 删除镜像时，保留该镜像，卸载仍可完成；不自动移除可变标签、不强制删除，也不清扫无法证明属于该应用的历史孤立资源。

### 镜像下载与重试

- Given：确认安装后，应用有本机还没有的镜像。
- When：容器代理在创建容器前按服务名顺序逐个拉取（`docker compose pull --policy missing <服务>`），本机已有的镜像不访问镜像仓库。
- Then：镜像仓库限流（429、`toomanyrequests`）、返回 5xx，或连接被重置、超时时，等待后重试这个服务：等待从 10 秒起每次加倍、最长 2 分钟，镜像仓库给出 `retry-after` 时不短于它；重试前输出一行“immich-server：镜像仓库限流，20 秒后重试（第 2 次）”，进度面板把它显示为最近一行。每个服务最多拉取 5 次。仍失败、需要的等待超过 2 分钟或会超过 30 分钟的安装时限，或遇到重试无用的错误（镜像或标签不存在、未授权或被拒绝、下载内容与摘要不符，以及认不出的错误）时，任务立即失败，例如“镜像下载失败：immich-server 遇到镜像仓库限流，重试 4 次后仍未成功（toomanyrequests: …）；没有创建容器，已下载完成的镜像已保留，重新安装时不会重复下载”。拉取失败时还没有容器：不执行 `compose down`，也不删除任何镜像，因为没有容器能证明下载归这个应用所有；应用回到“可安装”，卡片注明上次安装失败，重新安装直接复用已下载的镜像。等待重试时停止容器代理会立即结束等待，任务以“容器代理停止，安装被中断，还没有创建容器，请重新安装”失败。

### 失败与冲突

- Given：安装请求无法执行或执行失败。
- When：出现计划过期、端口或容器名被占用、另一任务正在运行、数据卷不可用、容器代理不可达或 Compose 失败。
- Then：在安装面板中分别提示“应用清单已变化，请重新确认安装计划”（并自动重新生成计划）“端口已被占用（端口与占用者）”“容器名已被占用”“另一个应用正在安装或卸载”“数据卷当前不可用”“无法连接容器代理”；Compose 失败时面板与详情页显示失败原因和展开的输出，并提供重新查看安装计划。未启用时显示“应用中心未启用”，无法连接时提供“重新连接”；读取过列表后连接中断时保留上次的状态并提示。

### 没有回滚干净的安装

- Given：安装失败，回滚的 `compose down` 也失败（例如 Docker 当时不可达），应用的容器留在 Docker 中。
- When：查看应用中心。
- Then：应用状态为“failed”而不是“已安装”：卡片注明“上次安装失败”并提供“安装”，不提供“打开”，也不出现在桌面；“已安装”页显示“安装失败，打开查看原因”。详情页与安装面板显示失败原因、输出和“回滚没有完成：N 个容器留在 Docker 中（M 个运行中），它们没有按安装计划核对……”，“更多操作”提供“在 Docker 中查看”与“卸载”。重新安装不返回 `already_installed`：任务先按项目 `compose down` 删除这些容器，再照常安装与核对；卸载同样删除它们。

### 安装中停止或重启容器代理

- Given：安装任务正在运行，部署、`apt upgrade` 或 Docker 重启停止了容器代理，或代理崩溃。
- When：代理停止，或再次启动。
- Then：停止时代理取消安装（终止正在运行的 Compose），用不受取消影响、最长 5 分钟的 `compose down` 回滚，任务以“容器代理停止，安装被中断，已回滚，请重新安装”失败；正在进行的卸载不取消，照常完成。代理在 60 秒停止时限内没有完成（或崩溃）时，再次启动后发现记录中仍在运行的安装，后台 `compose down` 删除它的容器，期间应用显示“安装中”、其他任务返回 `app_busy`，任务以“容器代理在安装过程中重启，安装没有完成核对，安装已回滚，请重新安装”失败；中断的卸载则重新执行 `compose down` 完成。重启前已结束的任务（成功或失败的原因与输出）在重启后照常显示。

## 边界与失败

- 清单与安装策略见 [ADR 0010](../adr/0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)；`catalog_test.go` 渲染每个内置应用，确保列出的应用都能通过策略。当前内置 37 个应用，来源提交为 CasaOS-AppStore `0909364`；入选标准、运行身份证据与未入选原因见[应用目录扩充评审](../research/app-catalog-review-2026-10.md)。
- **分类与展示字段**：清单文件保持上游原样；A-NAS 分类写在 [`categories.go`](../../internal/appstore/categories.go)，测试保证每个内置应用都有分类且没有多余条目。使用须知取自清单 `tips.before_install`，介绍取自 `description`（均优先简体中文）。文字校订在 [`copy.go`](../../internal/appstore/copy.go)：须知讲 CasaOS 设置或清单固定密码的应用（目前为 bentopdf、deluge、etherpad、immich、openlist、photoprism、pyload、qbittorrent、siyuan-note、transmission）改用 A-NAS 版须知，原文作为 `upstreamNotes` 一并返回；介绍中提到 Zima 的段落及以它为标题的整节被删去。测试保证任何应用的介绍不再出现 Zima 或“无限存储”，首先显示的须知不出现 CasaOS、Zima、Volumes、“右上角”或 `/DATA/AppData`；官网、源码与文档链接只保留绝对 http(s) 地址。产品服务把旧版容器代理上报的 CasaOS 分类视为未分类。
- **生成的登录信息**（2026-10-13 Demo 测试：OpenList 默认密码 `casaos` 写在公开清单里）：清单在 Compose 中写死的登录密码、访问授权码和签名密钥，按 [`credentials.go`](../../internal/appstore/credentials.go) 的列表（ConvertX、Etherpad、Linkwarden、OpenList、PhotoPrism、PsiTransfer、思源笔记、Transmission）在渲染时替换为每台 A-NAS 随机生成的值。值在第一次查看该应用的安装计划时生成，保存在容器代理状态目录 `secrets/<app>.json`（权限 0600），卸载后保留，因此计划摘要稳定、重新安装沿用原来的登录。登录密码和授权码在计划、安装完成面板和详情页中以“登录信息”显示：默认隐藏，可显示和复制（局域网 HTTP 没有 Clipboard API，复制使用选中文本的方式）；签名密钥只写入 Compose，不显示。接口仅管理员可用并带 `Cache-Control: no-store`。数据库密码不替换：数据库只在应用自己的网络内可达，且从第一次启动起就固定，换新密码会让保留数据的重新安装无法连接。早于此功能安装的应用（没有保存的值）仍在使用清单公开的值：登录信息显示该值，并警示它人人可查、请在应用里修改。OpenList 与 PhotoPrism 只在第一次启动时设置管理员密码，所以这类应用保留数据重新安装后仍是原来的密码，提示语说明此时以当时的密码为准；在应用里改过密码后同样以应用里的为准。Transmission 的账号保持清单中的名字，旧安装的账号不变。
- **Markdown 显示**：使用 react-markdown（含 GFM 表格与换行插件）渲染，不渲染原始 HTML，丢弃图片（会从互联网加载），链接在新窗口打开；清单文字中的标题降到页面区块标题之下（`h1`–`h3` 为 `h4`，其余为 `h5`）。渲染器单独打包，在应用中心打开时预先加载；加载完成前显示占位行，原始 Markdown 不会出现在屏幕上或辅助功能树中。
- **打开应用**：本机屏幕同样在新窗口中打开；Kiosk 中新窗口会盖住桌面，浏览器约束见[开放调查](../investigations/2026-10-06-kiosk-browser-confinement.md)。
- 路径：`/DATA/AppData/<app>/…` → `ANAS_APP_DATA_ROOT/<app>/…`（默认数据卷上的 `/srv/a-nas/data/apps`，不经 SMB 发布，成员不可列出）；其余 `/DATA/…`（含整个 `/DATA`）→ `ANAS_SHARED_DATA_ROOT/…`（默认 Shared 共享文件夹 `/srv/a-nas/data/spaces/shared`，绝不映射到数据卷根目录）；`/etc/localtime` 强制只读，`/etc/timezone` 被移除并以 `TZ` 代替。其他应用的数据目录、相对路径、`..` 逃逸和其余宿主机路径一律拒绝。
- **挂载方式**：数据卷上的文件夹不以绑定挂载交给 Docker（Docker 每次启动都会跟随绑定源路径中的符号链接，而能写共享空间或应用自身数据的人可以把其中的文件夹换成指向宿主机的链接）。渲染结果改为两个 A-NAS 卷：`a-nas-appdata` 以 `apps/<app>`、`a-nas-shared` 以共享空间为根（`local` 驱动、`o=bind`，根目录的名称只有 root 能改），具体文件夹作为 `volume.subpath`，并设 `nocopy`。Docker 每次启动在卷内解析子路径，链接指向卷外时拒绝启动；它既不创建卷根也不创建子路径。清单不能声明 `a-nas-` 前缀的卷。子目录挂载需要 Docker Compose 2.30 及以上与 Engine API 1.45 及以上：容器代理在规划时检查，不满足时返回 `409 docker_runtime_outdated`。Compose 2.29 及以前把子目录交给旧的绑定接口，引擎会挂载整个卷（[调查](../investigations/2026-10-09-old-compose-mounted-whole-app-volumes.md)）。安装后，容器代理核对每个容器对 A-NAS 卷的挂载都是计划中的子目录；有任一不符或无法检查时立即 `compose down` 回滚，任务失败并写明容器、卷与挂载点。Docker 来自官方软件源（[ADR 0015](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)）。
- **运行身份**（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)、[ADR 0022](../adr/0022-reach-shared-folders-only-as-the-app-identity.md)）：每个应用对应 Host Agent 分配的 Linux 账号 `app-<id>`（UID/GID 30000–30999，永不复用）。查看计划只在 Host Agent 登记表中预留 UID，账号在安装前预建文件夹时才创建。容器代理自己从宿主机的账号库读取 `app-<id>`，不采用调用方给的 UID/GID（[#176](https://github.com/zhongwater123/A-NAS/issues/176)）：只接受 UID 等于 GID、在应用区间内、有同名组，且该 UID 解析到的账号就是它自己（与 `getent` 一样取第一条）的账号；提交安装预留时账号必须已经存在。账号创建之前查看计划和预留安装，才使用产品服务转来的预留 UID，且这个 UID 不得被任何账号占用，也不得被该应用以外的组占用（Host Agent 先建组再建账号，中途停下时只留下应用自己的组）；预留安装时重新渲染并比对摘要，准备后提交时核对实际账号与预留计划的身份，所以只有按账号的 UID 显示的计划能安装。环境变量插值只提供 `AppID`、`TZ`、`PUID`、`PGID`，不读取容器代理自身环境。每个服务按 [`runtime.go`](../../internal/appstore/runtime.go) 恰好以一种方式运行，计划逐个列出：
  - **应用账号**（`app`）：Compose `user` 为 `app-<id>`，`cap_drop: [ALL]`。清单声明 `user`（无论原值），或审查表 `reviewedRunAs` 确认镜像能以任意 UID 运行时采用：n8n、Grafana、PsiTransfer 的镜像固定以非 root 用户运行（2026-10-13 Demo 测试中 n8n 因写不了文件夹而不断重启）；Audiobookshelf、Immich 的服务端、Kavita、Navidrome、PhotoPrism、Swing Music 的镜像默认以 root 运行，但它们挂载共享空间。
  - **初始化后降权**（`app-after-init`）：清单声明 `PUID`/`PGID`（无论原值是变量还是字面量）。容器以 root 启动，入口程序准备后切换到计划设定的 `PUID`/`PGID`；入口程序的监管进程在容器生命周期内保持 root。
  - **镜像自带身份**（`image`）：其余服务保留镜像的用户，可能是 root；只能挂载自己的应用数据，挂载共享空间时规划失败。
  - 所有服务设 `no-new-privileges`；清单不能自带 `group_add` 或 `security_opt`。容器带 `io.a-nas.runtime` 标签，记录渲染时的规则版本。
- **共享空间授权**：共享空间的 ACL 授予 `a-nas-users`（GID 20000）。Docker 只给容器 Compose 服务与镜像 `/etc/group` 中的组，宿主机账号的组成员关系从不进入容器，所以 `app-<id>` 在宿主机上不属于任何附加组（Host Agent 准备与释放应用时清除）。组写在服务自身：应用账号服务为 `group_add: ["20000"]`；初始化后降权的服务在降权时按镜像 `/etc/group` 重算附加组，所以 `PGID` 设为 20000。共享空间挂载默认只读（写入、`chmod`、`setfacl` 都返回 `EROFS`），只有审查表 `sharedWrites` 列为应用用途的挂载可读写：qBittorrent、Deluge、Syncthing 的整个共享空间，Transmission、PyLoad、Resilio Sync 的 `Downloads`，Duplicati 的备份目标与备份源（恢复到原位置会写入），Immich 与 PhotoPrism 的图库，Calibre-Web 的书库，Audiobookshelf 的有声书与播客目录（上传、下载单集与写入元数据）；Navidrome、Swing Music 与 Kavita 只读取媒体库，挂载只读。新文件继承共享空间的 ACL，成员可读写。卸载删除容器即结束访问，应用身份与其数据保留。应用身份永远无法访问任何个人空间。
- **安装后核对身份**：容器代理在核对挂载之后逐个检查容器的 `User`（应用账号服务）、`GroupAdd`、`CapDrop`、`no-new-privileges` 与只读挂载是否与计划一致；有任一不符或无法检查时立即 `compose down` 回滚，任务写明容器与差异。
- **文件夹由 Host Agent 预建**：安装前 Host Agent 校验计划中的每个宿主机文件夹只在 `apps/<app>` 或（已同意时）共享空间内，创建 `apps` 子卷（仅 root）、应用数据文件夹（属主 `app-<id>`）与共享空间文件夹（继承 ACL）；每一级都经不跟随符号链接的描述符创建并改属主，路径中出现链接时拒绝。Docker 不会创建任何宿主机文件夹，数据卷离线时应用无法启动，也绝不会把数据写到系统盘。
- 数据卷离线时安装返回 `423 volume_unavailable`，不预建任何文件夹。
- **Docker 网络**（[ADR 0013](../adr/0013-allocate-docker-networks-from-an-a-nas-address-pool.md)）：计划列出 Compose 将新建的网络，包括服务未设 `network_mode: bridge` 时的隐式 `default` 网络。清单自带 `ipam` 子网或驱动的网络被拒绝。应用有网络而 Docker 未配置 `default-address-pools` 时，规划与安装都返回 `409 address_pool_missing`。安装后，项目网络的每个子网都必须落在地址池内；有任一不在池内或无法检查时，立即 `compose down` 回滚，任务失败并写明网络与网段。
- 局限：镜像自带身份的服务可能以 root 写入它自己的应用数据目录；初始化后降权的镜像，其入口程序与监管进程以 root 运行。rootful Docker 下，容器逃逸漏洞对以 root 运行的容器意味着宿主机 root。
- 端口冲突检查包含 非运行容器的配置绑定端口（含范围），以及运行中或暂停容器的实际发布端口；未被容器占用但被宿主机进程监听的端口由 Compose 启动失败暴露在任务日志中。
- 一次只运行一个安装或卸载任务；安装超时 30 分钟（含镜像下载与重试的等待）、卸载 5 分钟，任务保留最近 40 行输出。每个应用最近一次任务（动作、状态、起止时间、输出与错误）在任务开始和结束时写入容器代理状态目录 `jobs/<app>.json`（目录 0700、文件 0600，输出可能含应用自己的日志），重启后读回；开始时写不进记录则拒绝任务（`503 apps_unavailable`）。应用状态由容器标签与这条记录共同决定：有容器且最近一次是成功的安装（或没有记录，即早于记录的安装）为“已安装”，有容器而最近一次是失败的安装为“failed”。代理单元设 `KillMode=mixed`，停止时 systemd 只向代理本身发 SIGTERM，由代理取消安装并用 Compose 回滚；60 秒后仍剩下的进程被 SIGKILL，留下的安装由下次启动回滚（[#169](https://github.com/zhongwater123/A-NAS/issues/169)）。
- **桌面快捷方式**：`GET /api/v1/app-shortcuts` 对所有已登录账号开放，只返回已安装且有网页端口的应用的 ID、名称、网页端口、协议、路径与运行中/全部容器数，不含安装计划、Compose、登录信息或镜像；`GET /api/v1/app-shortcuts/{id}/icon` 返回应用目录中的图标（与应用中心的图标相同的缓存与沙箱头）。应用中心未启用时列表为空，容器代理不可达时返回 `503`，桌面保留上一次的图标。
- 应用中心仅管理员可用：所有 `/api/v1/apps` 请求需要管理员产品会话，写请求还需要会话的 CSRF 令牌；成员看不到“应用中心”图标。写请求另要求 Host 为 `localhost` 或 IP 地址（[局域网 Web 访问](lan-web-access.md)）、JSON 请求体与同源 Origin；安装请求只接受 `digest` 字段，无法提交 Compose 内容。图标以沙箱化 CSP 与 `nosniff` 返回。

## 验收证据

- 系统盘保护：所有服务固定使用 `json-file`、`max-size=10m`、`max-file=3`，覆盖清单自带日志配置。运行规则版本为 2，旧容器需卸载重装才能应用日志限制。[清理回归](../../internal/appstore/engine/cleanup_test.go) 覆盖容器删除后的重试、重启恢复与损坏记录保护；[真实 Docker 回归](../../internal/appstore/engine/cleanup_docker_integration_test.go) 覆盖日志轮转、安装回滚、命名卷/应用数据保留与镜像清理，运行需要本地 Docker、Compose 和带 Python 3 的 `ANAS_TEST_IMAGE`。空间下限由[存储检查测试](../../internal/appstore/engine/storage_test.go)验证，系统占用由[系统健康规格](system-health.md)定义。

- 镜像下载与重试：[拉取测试](../../internal/appstore/engine/pull_test.go) 用假 Compose 覆盖两种 Docker 镜像存储措辞的限流、5xx、重置与超时在重试后安装成功，不短于退避与 `retry-after` 的等待、退避加倍，持续限流在 5 次后失败且不回滚、不删除镜像，不可重试的错误只拉取一次，超过等待上限或安装时限立即失败，等待中停止代理，以及多服务应用在后一个服务失败时保留前面的镜像。2026-10-28 WSL2（Docker 29.1.3 containerd 存储、Compose 2.40.3）：[真实 Docker 拉取测试](../../internal/appstore/engine/pull_docker_integration_test.go) 从本机 TLS 镜像仓库拉取，钉住 8 类失败的措辞与分类，验证限流后一次重试即成功，以及第二个镜像不存在时安装失败、第一个镜像保留且项目没有容器和网络，见[调查](../investigations/2026-10-28-registry-rate-limit-failed-install.md)。
- 运行身份：[运行规则测试](../../internal/appstore/runtime_test.go) 渲染全部内置应用，确认挂载共享空间的服务都以应用账号（带 20000 组、无能力）或初始化后降权运行、只有审查表列出的共享空间挂载可写、审查表只引用存在的应用与服务、20000 即 `a-nas-users`；[渲染测试](../../internal/appstore/render_test.go) 覆盖保留镜像用户的服务挂载共享空间被拒绝、`PUID` 类镜像在挂载共享空间时以 20000 为 `PGID`、共享空间默认只读而 qBittorrent 可写。
- 渲染与策略：[渲染测试](../../internal/appstore/render_test.go) 覆盖路径重写、显式 `user` 及字面量 `PUID`/`PGID` 改为应用身份、不让 Docker 创建宿主机文件夹、`/DATA` 只映射到共享空间、缺少身份时拒绝、确定性摘要与 17 类拒绝情形，以及计划列出隐式 `default` 网络、默认网桥不新建网络、清单自带子网被拒绝；[清单测试](../../internal/appstore/catalog_test.go) 另覆盖每个内置应用都有 A-NAS 分类、使用须知与链接的读取以及非 http(s) 链接被丢弃，[文字校订测试](../../internal/appstore/copy_test.go) 覆盖全部内置应用的介绍与须知不含 Zima、无限存储与 CasaOS 设置步骤以及卖点段落的删除规则，[登录信息测试](../../internal/appstore/credentials_test.go) 覆盖列表中每个应用缺少生成值时拒绝渲染、生成值写入 Compose 且清单固定值不再出现、须知不再给出清单密码、保留的值渲染出相同摘要、为旧安装记下的清单值与清单一致，[引擎测试](../../internal/appstore/engine/engine_test.go) 的 `TestInstalledAppsShowTheLoginsTheyUse` 覆盖值只生成一次并以 0600 保存、已安装应用显示保存的值、旧安装显示清单值，[分类测试](../../internal/appstore/categories_test.go) 覆盖分类表不含清单外的应用。
- 身份与文件夹：[Host Agent 应用身份测试](../../internal/hostops/linux/apps_test.go)（查看计划不改动宿主机账号）；root 集成测试 [`root_apps_integration_test.go`](../../internal/hostops/linux/root_apps_integration_test.go) 在真实 Btrfs 上以绑定挂载模拟容器（与 Docker 一样只给计划中的组，不用宿主机账号的组），验证账号在预建文件夹时才创建且不属于任何附加组、应用数据属主、没有计划中的组时不能写共享空间而有 `a-nas-users` 时可以、ACL 继承、无法读取个人空间、经植入的符号链接预建文件夹被拒绝、释放时清除旧的组成员关系并保留自身数据。
- 2026-10-14 WSL2（Docker 29.1.3）：按新规则手动运行 Audiobookshelf 2.35.0、Swing Music 2.1.4（应用账号）与 hotio qBittorrent 5.0.4（`PGID=20000`），主进程有效能力为 0、`no-new-privileges` 生效、只读挂载拒绝写入、可写挂载的新文件继承 ACL；同时证明宿主机组与 `group_add` 在 s6 降权后都不进入进程，见[调查](../investigations/2026-10-14-app-containers-and-the-shared-acl.md)。`ANAS_DOCKER_INTEGRATION=1 ANAS_TEST_IMAGE=<带 sh 与 setfacl 的本地镜像> go test -tags dockerintegration ./internal/appstore/engine` 以真实 Compose 运行渲染出的计划，证明安装检查读到的用户、组、能力、`no-new-privileges`、只读挂载与规则标签都与计划一致。
- 2026-10-07 WSL2（Docker 29.1.3、Compose 2.40.3）：以渲染结果运行一次性 Compose 项目，共享空间中被换成指向卷外目录的链接的文件夹使容器拒绝启动（`path concatenation escapes the base directory`），换回真实文件夹后正常挂载，写入文件属主为应用 UID；卷根不存在时挂载失败且 Docker 不创建它。
- 2026-10-08 Experimental NAS（Debian 13 自带 Docker 26.1.5、Compose 2.26.1）：真实拉取并安装 OpenList 4.2.2，以 `app-openlist` 运行，见 [OpenList 运行身份调查](../investigations/2026-10-08-openlist-runtime-identity.md)。
- 产品接口：[应用中心 API 测试](../../internal/appstoreapi/handler_test.go) 覆盖安装前预建文件夹、卸载撤销与数据卷离线拒绝；[桌面快捷方式测试](../../internal/appstoreapi/shortcuts_test.go) 覆盖只列出已安装且有网页端口的应用、未启用时为空列表、图标的沙箱头与 404/405；[管理员与 CSRF 测试](../../internal/httpapi/apps_test.go) 另覆盖成员可以读取快捷方式与图标、未登录返回 401，且响应中没有安装细节。
- 桌面快捷方式：[桌面测试](../../web/src/App.test.tsx) 的 “Installed apps on the desktop” 覆盖点击图标与在启动器中回车都按当前主机和网页端口在新窗口打开、未运行的应用标注“未运行”且管理员点击后打开 Docker、成员也能看到已安装的应用而未运行的不可点击。
- 执行层：[引擎测试](../../internal/appstore/engine/engine_test.go) 覆盖按确认计划写入并执行、摘要不符、端口冲突、并发任务、卸载保留数据与失败输出；`TestAppNetworksNeedAnAddressPool`、`TestInstallRollsBackANetworkOutsideThePool` 与 `TestInstallKeepsANetworkInsideThePool` 覆盖没有地址池时拒绝、网络不在池内时回滚与池内正常安装。`TestPlanRefusesADockerThatDropsVolumeSubpaths`、`TestInstallRollsBackAMountBeyondItsFolder` 与 `TestInstallKeepsMountsOnTheirFolders` 覆盖过旧的 Compose 或 Engine API 被拒绝、挂载超出计划文件夹时回滚与正确挂载时正常安装；`TestInstallRollsBackAContainerThatDoesNotRunAsPlanned`、`TestInstallRollsBackAWritableReadOnlyFolder` 与 `TestAppsReportContainersFromOlderRuntimeRules` 覆盖用户、组、能力、`no-new-privileges` 或服务与计划不符时回滚、只读挂载变成可写时回滚，以及按旧规则安装的应用被标出。[#169](https://github.com/zhongwater123/A-NAS/issues/169) 的两种情形：`TestInstallRollsBackWhenComposeFailsPartway` 让 `up` 在建出 Linkwarden 的 3 个容器中的 2 个运行后出错，确认依次执行 `up`、`logs`、`down` 且应用回到“可安装”并给出原因；`TestContainersOfAFailedInstallAreNotAnInstalledApp` 让回滚也失败，确认状态为 failed（2/3 运行、不显示登录信息），重新安装先按项目 `down` 再 `up`；`TestStoppingTheAgentRollsBackARunningInstall` 在 `up` 进行中停止代理，确认安装被取消并回滚、停止后拒绝新任务、记录为 0600，且重新启动的代理显示同一失败原因；`TestRecoverRollsBackAnInstallTheAgentDiedIn` 与 `TestRecoverFinishesAnUninstallTheAgentDiedIn` 模拟代理在安装或卸载中途被杀，确认再次启动后回滚期间显示“安装中”并拒绝其他任务，随后删除容器并报告失败，或完成卸载。
- 协议与 API：[代理测试](../../internal/appstore/agent/agent_test.go)（含分类、使用须知与链接往返，丢弃旧版代理的 CasaOS 分类，以及 `TestAgentRunsAppsAsTheirAccounts`：计划与安装按宿主机上的应用账号运行而不采用请求中的 UID，账号创建前只接受没人占用（或只有应用自己的组）的预留 UID，账号不存在时拒绝安装）、root 集成测试 `TestRootContainerAgentReadsAppIdentitiesFromTheHost`（从真实账号库读取，拒绝组不同名、UID 在应用区间外或 UID 解析到别的账号的 `app-*` 账号，并接受只建了应用自己的组的预留 UID）、[应用中心 API 测试](../../internal/appstoreapi/handler_test.go)、[OpenAPI 契约测试](../../internal/httpapi/openapi_test.go)。
- 前端：[应用中心测试](../../web/src/appcenter/AppCenterPanel.test.tsx) 覆盖分类浏览、按命中位置排序的搜索与 Esc 清空、回滚没有完成的安装显示为失败（不显示“打开”与运行中的容器数，说明留下的容器，提供卸载与重新安装）、Markdown 与使用须知（表格、无图片、新窗口链接）、A-NAS 版须知与折叠的上游原始说明及其标题层级、计划 → 进度 → 完成并按当前主机打开应用、关闭计划后焦点回到“安装”、失败原因与重新安装、重新安装时不把上一次的失败当作结果、另一任务进行时禁止确认、已安装列表与从菜单卸载（取消时焦点回到菜单按钮）、未分类应用列在“其他”、登录信息默认隐藏并可显示与复制（复制后焦点留在按钮上）、旧安装显示清单默认密码的警示、计划逐个列出服务的运行方式并说明共享空间只读、按旧规则安装的应用提示重装、按计划摘要安装、端口冲突提示、未启用状态、Docker 网络与地址池显示、地址池缺失与数据卷不可用提示，写请求都带 CSRF 令牌。
- 2026-10-12 Chromium，模拟模式：在 1180×780（默认窗口与最大化）和 800×660（窄窗口横条）下检查发现、分类、搜索、已安装、详情、安装计划、进度、完成、卸载确认与卸载进度；以临时替换的接口数据检查安装失败、部分容器未运行与安装中的列表和详情。
- 2026-10-07 WSL2（Docker 29.1.3、Compose 2.40.3）：通过产品 API 与容器代理安装 Memos——本机 Docker 无法访问 Docker Hub，首次安装如实失败并显示拉取错误；以本地镜像临时标记为 `neosmemo/memos:0.28.0` 后，安装成功（容器带 `a-nas-memos` 项目与 `io.a-nas.app` 标签，绑定目录重写到应用数据根，Docker 窗口可见），重复安装返回 `already_installed`，卸载后容器与网络删除且数据保留；过期摘要返回 `plan_changed`，跨站请求返回 `forbidden`。Chromium 中模拟模式完成浏览、计划、安装、卸载全流程，26 个图标全部加载。临时镜像标签与测试数据已清理。

安装前置顺序和端口回归由[API 测试](../../internal/appstoreapi/preflight_test.go)、[Docker 检查测试](../../internal/appstore/engine/docker_test.go)、[预留生命周期测试](../../internal/appstore/engine/preparation_test.go)覆盖；[代理协议测试](../../internal/appstore/agent/agent_test.go)验证跨进程提交时仍按真实账号授权。复现与边界见[调查记录](../investigations/2026-10-27-app-install-preparation-order.md)。

## 关联

- ADR：[0010 内置清单与安装策略](../adr/0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)、[0009 Docker Engine 与专用容器代理](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)、[0022 应用容器只以应用身份接触共享空间](../adr/0022-reach-shared-folders-only-as-the-app-identity.md)
- 调查：[应用容器拿不到共享空间的组权限，root 镜像绕过 ACL](../investigations/2026-10-14-app-containers-and-the-shared-acl.md)
- 调查：[安装失败的应用显示为“已安装”，容器代理重启打断安装](../investigations/2026-10-21-failed-app-install-shown-as-installed.md)
- 调查：[镜像仓库一次限流就让应用安装失败](../investigations/2026-10-28-registry-rate-limit-failed-install.md)
- 规格：[容器管理](container-management.md)
- 运行手册：[安装 Docker 与容器代理](../runbooks/install-container-agent.md)
- 规格：[设置](settings.md)（视觉语言）
- 调查：[Kiosk 浏览器约束](../investigations/2026-10-06-kiosk-browser-confinement.md)
- 代码：[`internal/appstore`](../../internal/appstore/appstore.go)、[任务记录与恢复](../../internal/appstore/engine/jobs.go)、[容器代理单元](../../deploy/systemd/system/anas-container-agent.service)、[分类](../../internal/appstore/categories.go)、[`internal/appstoreapi`](../../internal/appstoreapi/handler.go)、[应用中心界面](../../web/src/appcenter/AppCenterPanel.tsx)
