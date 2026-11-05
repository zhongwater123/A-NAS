# 0033：电脑桌宠使用 Electron 外壳

状态：proposed（2026-11-05）；行为见[虚拟伙伴规格](../specs/virtual-companion.md)。

电脑桌宠要在 Windows 和 macOS 上做到四件事：窗口透明、始终置顶、只有角色身上能点而周围的透明处点穿到下面的程序，并且直接使用网页桌宠的 React 界面和 NAS 登录。截至 2026-11 只有 Electron 现成地同时满足这四点，所以桌宠用 Electron 外壳；外壳尽量薄，以后要换时只换这一层。

## 决定

1. **网页做界面，外壳只做系统能力。** 联网时桌宠窗口加载已部署的 NAS 网页，离线时加载同一份代码构建出的 `dist/companion.html`（[`main.cjs`](../../desktop/companion/main.cjs) 的 `load`）。外壳只负责透明置顶窗口、鼠标穿透、拖动与多屏、托盘、单实例、睡眠恢复、小窝窗口和加载失败时的恢复。登录沿用 NAS 网页自己的会话，不复制系统浏览器的 Cookie。
2. **网页与外壳之间只有一层接口。** 网页只能调用 [`preload.cjs`](../../desktop/companion/preload.cjs) 经 `contextBridge` 暴露的 `window.companionDesktop` 七个方法；外壳逐个核对调用来自桌宠窗口、来自 NAS 或本地页面。换外壳只需重新实现这七个方法，网页代码不动。
3. **安全开关开到最严。** 关闭网页调用 Node，打开上下文隔离和沙箱；安装后的版本不能打开开发者工具；跳转和重定向只允许留在 NAS 自己的地址；权限只把全屏和写剪贴板给 NAS 自己的页面；其他网址交给系统浏览器打开；离线页面带内容安全策略。
4. **鼠标穿透用 `setIgnoreMouseEvents(true, { forward: true })`。** 鼠标在透明处时点击穿到下面的程序，网页仍收到鼠标移动，移到角色身上再改回可点。这个 `forward` 只在 Windows 和 macOS 上有效，正是两个目标平台。
5. **版本始终在官方维护的范围内。** Electron 约每 8 周发一个大版本，只维护最新的 3 个；补丁版本及时跟进，大版本在停止维护前升级（45 系列到 2027-04-27）。桌宠加载 NAS 网页这一远程内容，浏览器内核的安全补丁不能拖。
6. **按实测数字决定是否重新评估。** 方法：Windows 和 macOS 各测一次，离线模式、紧凑桌宠静置 5 分钟，用 `app.getAppMetrics()` 按进程类型记私有内存与 CPU。2026-11-05 在 Windows 11（Intel Core Ultra 5 225U 核显、150% 缩放、Electron 45.2.3）上三次干净测量：4 个进程私有内存合计 243–246 MB，其中 GPU 进程 131–134 MB、页面约 47 MB、主进程约 46 MB、网络约 18 MB，CPU 0.5–0.7%；拖动和专注到点后静置为 298 MB；打开小窝时 334 MB。GPU 进程里约 100 MB 是开着硬件加速就有的固定开销，精灵动画再加约 30 MB，显示过的姿势帧图回到待机后不释放、互动后再加约 50 MB（[调查记录](../investigations/2026-11-05-companion-gpu-memory.md)）。重新评估的门槛见“后果”。

## 后果

- **体积和内存大。** Electron 45 的官方压缩包 Windows x64 约 158 MB、macOS ARM64 约 121 MB。各外壳空应用的内存对比见 [Elanis 对比](https://github.com/Elanis/web-to-desktop-framework-comparison)（Electron 约 279 MB / 370 MB，Windows / macOS；GitHub CI 上测得，只适合相对比较，统计口径与 `privateBytes` 不一定相同，不能直接拿来和上面的数字比）。
- **重新评估看分项，不看一个总量。** 按进程类型分开记 `privateBytes`，并记下 GPU 进程的共享显存与测量环境（系统、显卡、缩放、Electron 版本、显示过哪些姿势、小窝开没开），方法见调查记录。
  - GPU 进程的固定开销（开着硬件加速约 100 MB）随显卡、驱动和缩放变化，只记录、不单独作为触发条件。在 Windows 上，换成同样走系统网页控件的外壳（Tauri、Wails 用的 WebView2 也有自己的 GPU 进程）省不掉它（推断）；真正能动它的是关硬件加速，代价是渲染落到 CPU。
  - 本桌宠自己的开销是动画（约 30 MB 与几乎全部空闲 CPU）和显示过的姿势帧图（互动后约 50 MB）。页面进程超过 60 MB、或互动后静置的合计超过 300 MB 时，先在 Electron 里压这两项；2026-11-05 的 298 MB 已经说明它们有压缩余地。
  - Windows 核显 150% 缩放下的 250 MB 总量只作提示线，超过时先看是哪一项涨了；macOS 另测另定。
  - 只有本桌宠自己的开销已经压到合理、整体仍嫌重（尤其 macOS）时，才评估其他外壳。其他触发条件：掉出官方维护的版本、出现 Electron 做不到的必需能力、转为商用后安装包体积成为分发问题。
- **验证要在真机上做。** 透明、置顶、穿透、托盘都是系统行为，只能在 Windows 和 macOS 本机运行 Electron 验证；开发机要在 Windows 上另装 Node，不能用 WSL 里的结果代替。
- **已知的平台限制。** Windows 上以管理员权限运行的窗口在前台时，`forward` 会暂时失效（UIPI）；macOS 的透明窗口没有阴影，偶尔留下残影；打开开发者工具会让透明失效。系统没有“其他程序进入全屏”的通知，避让要自己检测。
- **发布需要签名。** 自动更新在 macOS 上要求签名，Windows 的系统通知需要安装器创建的快捷方式；这与选哪种外壳无关。

## 未选择

- **Tauri 2**：能直接用 React 界面，安装包只有几 MB；但鼠标穿透只能整窗开关，只穿透透明处要自己在 Rust 里轮询鼠标位置；macOS 上换成 Safari 内核，界面要重新验证；Windows 上的内存并不比 Electron 低（参考值约 312 MB）。团队也不写 Rust。
- **Wails v3（Go）**：与后端同一种语言；但 v3.0.0 在 2026-10-26 才发布，找不到按区域穿透的能力，Windows 透明要求 Windows 11。
- **Neutralinojs**：有透明窗口，没有点击穿透，达不到需求。
- **Qt / PySide6**（QCYTSN/dsh-dafeiyu 的做法）：内存最小（其实测私有内存约 32 MB）；但角色动画、聊天和登录都要重写，首次启动要解压约 100 MB 运行库。
- **Flutter**：窗口插件有带 `forward` 的穿透；但要用 Dart 重写全部界面。
- **完全原生**（macOS Swift、Windows WPF）：资源最省；要维护两份代码，团队没有这类经验。

## 关联

- 规格：[虚拟伙伴](../specs/virtual-companion.md)（电脑桌宠与登录方式）
- ADR：[0003 Web 桌面使用 React 和 TypeScript](0003-use-react-typescript-for-web-desktop.md)
- 代码：[`main.cjs`](../../desktop/companion/main.cjs)、[`preload.cjs`](../../desktop/companion/preload.cjs)、[`policy.cjs`](../../desktop/companion/policy.cjs)、[`web/src/companion/native.ts`](../../web/src/companion/native.ts)
- 测试：[`smoke.cjs`](../../desktop/companion/smoke.cjs)、[`handoff-smoke.cjs`](../../desktop/companion/handoff-smoke.cjs)、[`task-smoke.cjs`](../../desktop/companion/task-smoke.cjs)、[`policy.test.cjs`](../../desktop/companion/policy.test.cjs)
- 说明：[电脑伙伴原型](../../desktop/companion/README.md)
- 调查：[电脑桌宠静置时 GPU 进程占内存的来源](../investigations/2026-11-05-companion-gpu-memory.md)（资源基线与分项）
