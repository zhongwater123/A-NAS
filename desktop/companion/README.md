# A-NAS 电脑伙伴原型

与 NAS 网页共用 `web/src/companion/` 的角色、动作和玩法。先验证点击、拖动、放下、抛接球、安静陪伴、隐藏与叫回。当前固定使用社区奶蛙精灵图，功能与形象层独立；[素材来源与使用边界](../../web/src/companion/assets/NOTICE.md)已记录。

## 网页先试玩

使用 `web/package.json` 要求的 Node 与 npm，在仓库根目录执行：

```sh
cd web
npm ci
npm run companion:dev
```

打开 Vite 输出地址下的 `/companion.html`。该页无 NAS 登录，数据只保存在本机浏览器；不是在线的 NAS 设备预览。NAS 正常桌面则从“伙伴小窝”打开，沿用已登录账号。

## 电脑桌宠

```sh
cd web
npm run companion:build
cd ../desktop/companion
npm ci
npm start
```

默认离线运行，不依赖模型或 NAS。轻点打招呼，按住角色拖动窗口，点球抛接；菜单提供安静、隐藏和小窝。托盘可叫回、归位和退出。没有自动注册开机启动。

构建桌面安装目录：`npm run package:mac` 或 `npm run package:win`。产物放在本目录 `build/`，未签名；跨平台构建成功不代表对应系统实测通过。

## 连接已部署本功能的 NAS

```sh
ANAS_NAS_URL=http://192.168.1.20 npm start
```

把示例地址替换为自己的 NAS。登录页在桌宠所在的客户端自行登录；账号通过后才进入透明角色模式。小窝窗口与桌宠使用同一个 Electron 会话。产品现有的会话失效策略继续有效。形象、安静偏好与玩球次数由 NAS 按成员保存，屏幕位置留在各客户端。小窝打开后点“叫到这里”；桌宠登录后申请接续；“回小窝”让小窝窗口申请接续。其他端在线时先交接，离线时最多等待旧端 15 秒租约到期。

联网小窝中可选择 NAS 文本会议记录、整理摘要与待办，并请求保存到个人空间。桌宠观察同一对话的处理/确认/完成状态，可停止回答、打开完整 VNAS。保存仍须由用户在原 VNAS 确认卡决定；文件预览只使用执行器已确认写入的记录。离线模式不提供真实任务。

生产连接前必须部署包含本功能的 NAS Web 构建并验证登录与退出；本轮不自动连接、登录或部署实验 NAS。不关闭 Electron 的网页安全、上下文隔离或沙箱来规避认证问题。

## 开发检查

```sh
cd web
npm run format
npm run typecheck
npm run lint
npm test
npm run build
npm run companion:build
cd ../desktop/companion
npm test
npm run smoke
npm run smoke:handoff
npm run smoke:tasks
```

领域状态与 HTTP 契约的最小并发检查（仓库根目录）：`go test -race ./internal/companion ./internal/companionapi`。产品鉴权检查须在 Linux 运行：`go test -race ./internal/httpapi -run TestCompanionMemberScopeAndHandoff`。

完整仓库门禁仍按 `AGENTS.md` 运行；macOS 上 Linux 产品服务和完整门禁须使用文档中的 Linux 开发环境。桌宠本身使用 macOS/Windows 的 Electron 环境，依赖不与 Linux 构建共用。

`smoke:handoff` 需要仓库指定的 Go 工具链与本机 Chrome（可用 `ANAS_TEST_BROWSER` 指定已安装的 Playwright channel）。它启动只监听回环的测试 API、临时 SQLite 和 Vite，以两个浏览器上下文验证小窝与桌宠组件的双向交接、进度继承与断网本地玩不回写；结束后删除临时状态。固定合成成员绕过产品鉴权，只供测试，不验证 NAS 登录或 CSRF，也不能部署这个测试服务。结果与截图在 `build/handoff/`。

`smoke` 会打开并关闭离线桌宠与小窝，使用临时独立配置目录，不连接 NAS，也不复用真实登录。截图和结果放在 `build/smoke/`。系统级鼠标穿透与其他应用的全屏行为仍需人工验收。设置 `ANAS_SMOKE_EXECUTABLE` 为已打包的可执行文件绝对路径，可用同一脚本检查打包产物，结果放在 `build/smoke-packaged/`。

`smoke:tasks` 额外验证文本纪要闭环、双端同一对话、原 VNAS 确认卡手动确认、已保存文件预览及刷新不重复发送。伙伴存储/API 和客户端为真实代码；Hermes 运行时、模型、文件服务、确认执行器与账号为模拟，未接真实 NAS。截图和机器可读结果在 `build/tasks/`。运行浏览器烟测时不要同时运行 Web Biome 检查，避免其临时入口写入期间被扫描。

音乐菜单测试现等待真实表格行，原型快照完整 Web 602 项通过。完整产品服务的检查交由仓库 Ubuntu x86_64 / Debian systemd CI；macOS 的桌宠本机验证不替代 Linux 检查或真实 NAS 验收。

公司内网测试人员从 PR 的 Actions 下载 Mac ARM64 / Windows x64 试玩包，并按[内网联调手册](../../docs/runbooks/accept-virtual-companion.md)核对同一候选提交、安装和回滚。CI 的协议演示不访问真实 NAS，也不调用云端模型。

## 待办

- [ ] 触屏手势、公共与个人模式：等待触屏硬件支持。
- [x] NAS 保存形象、安静偏好和玩球次数；单一互动端、交接、租约到期与断线本地模式。
- [ ] Linux 完整门禁、产品路由鉴权测试与真实 NAS/Electron 登录接续验收。
- [ ] 更丰富的物件与活动状态。
- [x] VNAS 文本纪要入口、同一对话跨端状态、原确认卡与已确认文件预览（本地协议验收）。
- [ ] 真实模型/NAS 文件落盘/Electron 已登录接续验收；按住说话、统一 TTS、专注陪伴、关系记忆和小作品。
- [ ] Windows 实机、多屏混合 DPI、系统其他应用全屏自动隐藏。
- [ ] 正式动画素材包、资源与功耗基线、签名和发布流程。

## 社区参考

参考 [dsh-pet](https://github.com/PC2005-cloud/dsh-pet) 的共享物理与动作分层、[pet registry](https://github.com/zhu1090093659/dsh-pet) 的素材包隔离、[Live2D 桌面版](https://github.com/A8Chann/dsh-pet-live2d) 的宿主与页面分离。交互与接续控制器没有复制上述 dsh-pet 分支代码。任务链复用 A-NAS 已有的 Hermes JS 网关、会话和确认卡，避免重造 Agent。形象使用 [timerring/codex-pet-naiwa](https://github.com/timerring/codex-pet-naiwa) 的社区精灵图，原文件摘要与署名见素材说明。

行为规格见[虚拟伙伴](../../docs/specs/virtual-companion.md)。
