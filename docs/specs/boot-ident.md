# 开机动画

状态：implemented
更新时间：2026-10-08

## 目标

Web 桌面加载时先播放约 7 秒的 A-NAS 开机动画：80 年代电视台台标风格，在 4:3 CRT 画面上组装“屋顶 + 存储阵列”标志与 A-NAS 字标，然后像老电视关机一样收成一条线和一个光点，再淡出露出桌面。主要面向 4:3 的本地控制台直连屏幕。

## 非目标

- 不覆盖 BIOS、内核与 systemd 启动阶段（例如 Plymouth）；Kiosk 启动 Chromium 之前仍是系统原有画面。
- 不反映真实服务就绪状态；动画时长固定，登录页或桌面在下方并行加载。
- 不播放声音：Chromium 自动播放策略在没有用户手势时会阻止音频。
- 不提供配色或开关设置，只使用已确认的日落配色。

## 场景

### 首次打开

- Given：当前浏览器标签页尚未播放过开机动画，系统未要求减少动态效果。
- When：打开 Web 桌面，包括 Kiosk 启动。
- Then：全屏黑底上以 4:3 依次显示：开机光线与雪花；硬盘从两侧交替插入并自下而上组成存储阵列；三层屋顶描出、阁楼三角弹出；“A-NAS”以 VHS 切片锁定；“HOME STORAGE”逐字出现；高光扫过并闪光；一次跟踪干扰；关机收成线和光点；最后淡出到下方的登录页或桌面。

### 跳过

- Given：动画正在播放。
- When：按任意键，或点击、触摸画面。
- Then：立即进入关机收线；关机过程中再次操作则立刻结束。播放期间的按键不会传给下方的登录表单或桌面。

### 同一标签页刷新

- Given：当前标签页已经播放过开机动画。
- When：刷新页面。
- Then：直接显示登录页或桌面；新标签页或 Kiosk 重启后会再次播放。

### 减少动态效果

- Given：系统设置了 `prefers-reduced-motion: reduce`。
- When：打开 Web 桌面。
- Then：不播放开机动画。

## 边界与失败

- 播放记录保存在 `sessionStorage` 的 `a-nas.boot-ident.played.v1`；存储不可用时每次加载都会播放。
- 浏览器不支持 WebGL 或着色器编译失败时，退回为没有 CRT 效果的普通 2D 画面；连 2D Canvas 也不可用时直接显示桌面。
- WebGL 上下文丢失或绘制抛出异常时立即结束动画，不阻塞桌面。
- 动画只依赖前端代码：字标和标语为矢量绘制，不加载网络字体、图片或音频，离线控制台也能完整播放。
- 时间按时钟而不是帧数推进：标签页在后台暂停绘制，恢复后会直接跳到对应时刻，不会补播。
- 覆盖层位于所有桌面窗口之上，结束后从 DOM 移除；它对辅助技术隐藏，不影响下方页面的读屏结构。
- 绘制缓冲宽度上限为 1600 像素；Experimental NAS 显卡上的帧率与 WebGL 可用性尚未实测。

## 验收证据

- [时间线与降级测试](../../web/src/bootIdent.test.ts)：开机光线、稳定画面、关机收线和光点，以及无法绘制时不启动。
- [覆盖层测试](../../web/src/BootSplash.test.tsx)：每个标签页只播放一次、淡出后交给桌面、指针和按键跳过且按键不泄漏、无法启动时直接进入、减少动态效果时不播放。
- 2026-10-08 在本机 Chromium 打开 `make web-build` 产物，确认完整播放后进入登录页、空格跳到关机收线、刷新不重播。
- 待完成：在 Experimental NAS 的 Kiosk 直连屏幕上确认帧率和 CRT 效果。

## 关联

- 架构：不涉及
- ADR：[本地控制台 Kiosk](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- 代码：[覆盖层](../../web/src/BootSplash.tsx)、[画面与时间线](../../web/src/bootIdent.ts)、[CRT 渲染](../../web/src/crtRenderer.ts)、[挂载入口](../../web/src/main.tsx)
- 测试：[bootIdent.test.ts](../../web/src/bootIdent.test.ts)、[BootSplash.test.tsx](../../web/src/BootSplash.test.tsx)
