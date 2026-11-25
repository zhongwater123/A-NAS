# 0040：共享工作区以独立服务运行，页面在无来源的沙箱里展示，成员的 agent 凭 Agent令牌经 MCP 修改

状态：proposed（2026-11-25 起草；行为见[规格](../specs/shared-workspace.md)，实施见[技术设计](../architecture/shared-workspace.md)）

团队要在 NAS 上用 HTML 共写 PRD：成员在桌面里看页面、指出要改的地方，由自己电脑上的 Claude Code 或 Codex 修改。这带来三件难以逆转的事：成员上传的 HTML 带脚本，要在桌面里完整运行却不能拿到成员的登录状态；成员电脑上的 agent 要有一种只能做这件事的凭据；凭据要在局域网的明文 HTTP 上可用。我们决定：共享工作区是内置的独立服务；页面在不透明来源的沙箱框架里运行；agent 凭成员签发的 Agent令牌，经产品服务上一种新的路由姿态调用共享工作区服务的 MCP 接口，共享工作区服务自己向 Host Agent 确认令牌。2026-11-25 的验证在浏览器与 Claude Code 上实测了这几点（[技术设计的“验证”](../architecture/shared-workspace.md#验证)）。

## 决定

1. **内置独立服务。** 照家庭中心与剪辑中心（[ADR 0029](0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)、[ADR 0038](0038-keep-montage-media-in-the-photo-library.md)）接线：单元 `anas-workspace.service`，系统账号 `a-nas-workspace`，固定 UID 31004，数据卷上的子卷 `workspace`，库 `workspace.db`。它持有页面、版本、可使用的成员、一次性传输地址与自己的审计记录。它没有网络，只看得到自己的子卷。
2. **页面在无来源的沙箱里运行。**
   - 页面经 `/api/v1/workspace/pages/{id}/versions/{n}/view` 返回，响应头是 `Content-Security-Policy: sandbox allow-scripts; default-src 'none'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; connect-src data: blob:; form-action 'none'; base-uri 'none'; frame-ancestors 'self'`。桌面用 `<iframe sandbox="allow-scripts">` 打开它，不给 `allow-same-origin`。
   - 页面因此是不透明来源：读不到 Cookie 与 `localStorage`，访问不了桌面，发出的请求被 CSP 拦下；即使拦不住，跨站请求也不带 `SameSite=Strict` 的会话 Cookie。直接打开页面地址时，响应头里的 `sandbox` 同样生效。
   - 服务在返回页面时给源码里写出的每个元素加上 `data-anas-at`（它在源码中的字节位置），并插入一个公开的辅助脚本。辅助脚本只经 `postMessage` 与桌面通信，报告成员选中的元素、截图和被拦下的资源。
   - 导出与一次性下载以附件返回，带 `X-Content-Type-Options: nosniff` 与 `sandbox` 策略，不会在桌面的来源里运行。
3. **Agent令牌归账号库，由 Host Agent 只读解析。**
   - `control.db` 的新表 `agent_tokens` 存令牌的哈希、账号、范围（这一版只有 `workspace`）、名字、种类、创建与上次使用时间。令牌不进 `sessions` 表：那里的每一行，相册、分享与文件代理都会当作完整会话接受。
   - 产品服务创建、列出、撤销令牌；Host Agent 的会话目录（`internal/sessiondir`）只读解析它，只在共享工作区的会话查询套接字上开放，并只认范围为 `workspace` 的令牌。
   - 停用账号、管理员重置密码时，在删除会话的同一事务里删除该账号的全部令牌；账号删除时随账号删除；账号须改密时解析失败。90 天没用过的令牌失效。
   - 令牌不绑定客户端地址，这与会话不同（[不变量 9](../architecture/OVERVIEW.md#不变量)）：agent 所在的电脑换了地址仍要能用。
4. **产品服务上的第五种路由姿态 `agent`。**
   - 它只认 `Authorization: Bearer`，不认 Cookie，也不要 CSRF 令牌（令牌不会被浏览器自动带上）。
   - 带 `Origin` 或 `Sec-Fetch-Site` 头的请求一律拒绝：只有浏览器会带，浏览器不该调用这里。`Host` 要指向本机，只认 `localhost` 与 IP 地址，同终端与 Docker 的写操作（[ADR 0012](0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md) 决定 4）。
   - 产品服务先核验令牌、账号状态与须改密，按客户端限流失败的尝试，再经转发的内部请求头把令牌交给共享工作区服务；共享工作区服务不信产品服务的结论，自己向 Host Agent 再确认一次（[ADR 0011](0011-run-the-photo-library-as-a-dedicated-service-identity.md) 的原则）。
5. **MCP 接口。** 用官方 Go SDK（`github.com/modelcontextprotocol/go-sdk`），无状态的 HTTP 传输、JSON 响应，挂在 `POST|GET|DELETE /api/v1/workspace/mcp`。请求体在服务端限 4 MiB；这是“JSON 只经 `internal/httpjson`”的例外，因为 SDK 自己读写 JSON-RPC。
6. **一次性传输地址。** 整份下载与上传给 agent 一个随机地址 `/api/v1/workspace/transfers/{key}`（128 位随机数，10 分钟内只能用一次，绑定账号、页面、基线版本与签发它的令牌 ID），以便 agent 用 `curl.exe` 传文件，内容不经过对话。这条路由在产品服务上是 public 姿态，由地址本身授权；使用时共享工作区服务按令牌 ID 向 Host Agent 确认签发它的令牌仍然有效。
7. **谁能用由共享工作区服务判断。** 可使用的成员存在它的库里，管理员始终可以；每个请求，包括 MCP 调用与一次性地址，都按 Host Agent 给的账号与角色检查。

## 后果

- 多一个系统账号、单元、子卷与会话查询套接字；Host Agent 多两个只读查询（按令牌解析账号、按令牌 ID 确认有效），只开在这个套接字上。
- 成员的 agent 有了一种比会话活得久的凭据，在局域网上明文传输。截获它的人能读写共享工作区，碰不到 NAS 的其他功能；产品服务被攻破时能为任何账号签发令牌，与它今天能签发会话相同（[#98](https://github.com/zhongwater123/A-NAS/issues/98) 的 E01）。
- 页面不能用 `localStorage`、Cookie 与外网资源；依赖它们的页面会部分失效，窗口如实说明。
- MCP 只能按 IP 或 `localhost` 访问；按主机名打开桌面的成员，连接命令要换成 IP。
- 退回没有 `agent_tokens` 的版本时，表被删除，成员要重新连接 agent。
- 开着的页面每 3 秒查一次新版本，产品里没有新增推送通道。

## 未选择

- **OAuth：** 体验最好（agent 首次连接时在浏览器里点“允许”），但 Claude Code 默认的运行时只把 OAuth 凭据发给 HTTPS 或本机的令牌端点，局域网上的明文 HTTP 会登录失败。局域网有 HTTPS 以后再加，令牌与工具不用改。
- **让 agent 用浏览器里的登录：** 会话 Cookie 是 `HttpOnly` 的完整会话，管理员的会话能管理存储、Docker 与终端；会话还绑定登录时的客户端。
- **SSH 或经 SMB 直接改文件：** 成员没有 SSH；共享空间对所有账号可读，做不到管理员选人；文件没有版本与并发检查；办公网段到 NAS 的 445 不通。
- **令牌存在共享工作区服务自己的库里：** 服务要判断账号是否停用、是否被重置过密码，仍然要向 Host Agent 查；停用与重置时它也收不到通知，令牌会在重新启用后复活；别的模块也用不上。
- **令牌放进 `sessions` 表：** 每个服务都会把它当完整会话。
- **像问题反馈那样放进产品服务、存在系统盘：** 少一个服务，但全部页面与历史版本堆在系统盘，产品服务被攻破时直接暴露，之后再拆出去要迁移数据。
- **做成可移除模块（[ADR 0037](0037-plug-removable-modules-in-as-separate-services.md)）：** 共享工作区是常设功能，并且需要 Host Agent 的令牌解析，模块看不到它。
- **`srcdoc` 框架或同源展示：** `srcdoc` 框架继承桌面的 CSP，页面里的内联脚本跑不起来；同源展示时页面脚本带着成员的会话。
- **服务端推送（SSE）：** 产品里还没有，需要新的长连接约定；3 秒轮询已经够用。
- **整份文件的乐观锁：** 两个 agent 改不同的地方也会冲突；按原文替换只在改到同一处时冲突。
- **在 NAS 上用无头浏览器截图：** 要在 NAS 上运行 Chromium；页面自己画出截图已经足够准确。

## 关联

- [共享工作区规格](../specs/shared-workspace.md)、[技术设计](../architecture/shared-workspace.md)
- [ADR 0011](0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[ADR 0012](0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)、[ADR 0029](0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)、[ADR 0037](0037-plug-removable-modules-in-as-separate-services.md)、[ADR 0038](0038-keep-montage-media-in-the-photo-library.md)
- 代码：`internal/workspace`、`internal/workspaceapi`、`internal/workspacemcp`、`internal/workspaceservice`、`internal/sessiondir`、`internal/apiroute`
