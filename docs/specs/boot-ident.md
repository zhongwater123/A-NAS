# 开机动画

状态：implemented
更新时间：2026-10-29

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

### 接上登录页

- Given：开机动画结束时还没有登录。
- When：覆盖层从 DOM 移除。
- Then：登录、首次启用、等待本机启用与重置密码共用同一个画面（[AuthStage](../../web/src/auth/AuthStage.tsx)）：暖黑底上竖直的流星雨，开机动画的定格画面（标志、A-NAS 字标与 HOME STORAGE）不带 CRT 效果地摆在一侧，在出现时重放一次 VHS 切片锁定和标语，之后硬盘指示灯持续闪烁、每 11 秒扫过一次高光；另一侧是标题与表单，叠加扫描线、胶片颗粒、滚动亮带、频道号与摄像机式日期时间。覆盖层仍在时标题、表单与字标都停在入场前，移除后才依次入场；窄屏和竖屏把字标放到表单上方。

### 减少动态效果

- Given：系统设置了 `prefers-reduced-motion: reduce`。
- When：打开 Web 桌面。
- Then：不播放开机动画；登录页的流星、字标与 CRT 纹理都画成静止的一帧，不做入场动画。

## 边界与失败

- 播放记录保存在 `sessionStorage` 的 `a-nas.boot-ident.played.v1`；存储不可用时每次加载都会播放。
- 浏览器不支持 WebGL 或着色器编译失败时，退回为没有 CRT 效果的普通 2D 画面；连 2D Canvas 也不可用时直接显示桌面。
- WebGL 上下文丢失或绘制抛出异常时立即结束动画，不阻塞桌面。
- 动画只依赖前端代码：字标和标语为矢量绘制，不加载网络字体、图片或音频，离线控制台也能完整播放。
- 时间按时钟而不是帧数推进：标签页在后台暂停绘制，恢复后会直接跳到对应时刻，不会补播。
- 登录画面没有显示出来时，流星与字标也停止绘制，例如本机屏幕的黑屏盖住它、页面停止渲染时（[本地控制台屏保](local-console-screensaver.md#覆盖期间下层页面不渲染)）；画面重新显示后从当前时刻继续。
- 覆盖层位于所有桌面窗口之上，结束后从 DOM 移除；它对辅助技术隐藏，不影响下方页面的读屏结构。登录页通过覆盖层的 `data-boot-splash` 属性判断它是否还在。
- 绘制缓冲宽度上限为 1600 像素；Experimental NAS 显卡上的帧率与 WebGL 可用性尚未实测。
- 登录页的流星和字标只是装饰：浏览器不能绘制 2D Canvas 或绘制抛出异常时停在纯黑背景上，标题和表单照常可用。它们同样只用前端代码绘制，不加载字体或图片。

## 验收证据

- [时间线与降级测试](../../web/src/boot/bootIdent.test.ts)：开机光线、稳定画面、关机收线和光点，以及无法绘制时不启动。
- [覆盖层测试](../../web/src/boot/BootSplash.test.tsx)：每个标签页只播放一次、淡出后交给桌面、指针和按键跳过且按键不泄漏、无法启动时直接进入、减少动态效果时不播放。
- 2026-10-08 在本机 Chromium 打开 `make web-build` 产物，确认完整播放后进入登录页、空格跳到关机收线、刷新不重播。
- [字标时间线与降级测试](../../web/src/boot/bootIdent.test.ts)、[流星降级测试](../../web/src/auth/meteorShower.test.ts)：锁定一次后停在定格并周期扫光，无法绘制或绘制失败时不启动，暂停期间不绘制、恢复后继续。[登录画面测试](../../web/src/auth/AuthStage.test.tsx)：画面不再可见时两块画布都暂停。
- 待完成：在 Experimental NAS 的 Kiosk 直连屏幕上确认帧率和 CRT 效果。

## 关联

- 架构：不涉及
- ADR：[本地控制台 Kiosk](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- 代码：[覆盖层](../../web/src/boot/BootSplash.tsx)、[画面与时间线](../../web/src/boot/bootIdent.ts)、[CRT 渲染](../../web/src/boot/crtRenderer.ts)、[挂载入口](../../web/src/main.tsx)、[登录画面](../../web/src/auth/AuthStage.tsx)、[流星雨](../../web/src/auth/meteorShower.ts)
- 测试：[bootIdent.test.ts](../../web/src/boot/bootIdent.test.ts)、[BootSplash.test.tsx](../../web/src/boot/BootSplash.test.tsx)、[meteorShower.test.ts](../../web/src/auth/meteorShower.test.ts)、[AuthStage.test.tsx](../../web/src/auth/AuthStage.test.tsx)
