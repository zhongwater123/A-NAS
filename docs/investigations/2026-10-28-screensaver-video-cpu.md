# 本机屏幕播放屏保时占用约 1.4 个核

状态：fixed，待实机验收：驱动 2026-10-29 已装并确认硬件解码，页面改动随下一次部署
更新时间：2026-10-29

## 症状与影响

2026-10-28 整机空闲时用 1.54 个核，其中本机屏幕所在的 `user-1001.slice` 占 1.44 个核，这时屏保正在播放。本机屏幕因此是设备上最大的常驻 CPU 负载。它还会让相册 AI 的门控因 CPU 压力关闭，见[闲时检测只用了约 1 个核](2026-10-13-idle-detection-used-one-core.md)。

屏保播满息屏时长转为黑屏以后，本机屏幕仍占 0.26–0.30 个核。

## 最小复现

1. Experimental NAS 运行 `80b602275b86`，本地控制台已登录，屏保池里有视频。
2. 本机屏幕 3 分钟无输入，屏保开始播放；再等息屏时长，屏幕转黑。
3. 在两个阶段各运行一次 `scripts/measure-local-console-cpu.sh 60`（root 或 kiosk 账号运行时还能读出 GPU 引擎时间与已加载的驱动）。
4. 实际：播放时约 1.4 个核，黑屏时约 0.27 个核。期望：远低于 0.3 个核。

## 观察事实

都来自 `ssh a-nas`（只读账号）与 `ssh a-nas-dev`（与当时的 kiosk 同一账号，能读 Chromium 进程的 `/proc`）。

- **环境**：Debian 13.7，内核 6.12.111；i3-12100，8 个逻辑核；核显 UHD 730（`8086:4692`，Alder Lake-S GT1）。Cage 0.2.0，Chromium `154.0.8037.92-1~deb13u1`，`--ozone-platform=wayland`。kiosk 仍以 `anas-dev` 运行，位于 `user-1001.slice/session-c5.scope`。
- **没有 VA-API 驱动**：装了 `libva2` 与 `libva-drm2` 2.22.0（`ffmpeg` 的依赖），没有 `intel-media-va-driver`、`i965-va-driver` 或 `mesa-va-drivers`。`/usr/lib/x86_64-linux-gnu/dri/` 中只有 Mesa 的 GL 驱动，没有 `*_drv_video.so`。
- **Chromium 参数**：除启动器的参数外，只有 Debian 包装脚本加的 `--enable-gpu-rasterization` 等；GPU 进程只带 `--enable-features=SingleAxisScrollContainers`。
- **屏幕**：DP-2 只有 `1536x2048` 一个模式，`kiosk.env` 把它转 90 度、缩放 1.5，即横向 2048×1536 物理像素、1365×1024 CSS 像素。屏幕是 4:3，视频是 16:9，`object-fit: cover` 只显示视频中间 1440×1080 的部分，再放大约 1.42 倍。
- **视频池** `68299080c557c520`（六条，用 `ffprobe` 读暂存区中同哈希的副本）。`ffmpeg 7.1.5` 在 NAS 上软件解码前 20 秒（不解音频），每秒视频耗的 CPU：

  | 池中名称 | SHA-256 前缀 | 格式 | 码率 | 音轨 | 软件解码 |
  |---|---|---|---|---|---|
  | `screensaver-000.mp4` | `7894c8b5` | H.264 Main，1920×1080，60 fps，12.4 秒 | 10.1 Mbps | AAC | 0.354 核 |
  | `screensaver-001.mp4` | `a0daeb62` | H.264 Main，1920×1080，60 fps，131 秒 | 11.8 Mbps | 无 | 0.350 核 |
  | `screensaver-002.mp4` | `1231b3ed` | H.264 High，1920×1080，30 fps，15.8 秒 | 1.6 Mbps | HE-AAC | 0.159 核 |
  | `screensaver-003.mp4` | `f8d32bf2` | H.264 Main，1920×1080，30 fps，173 秒 | 19.7 Mbps | AAC | 0.253 核 |
  | `screensaver-004.mp4` | `9b5a95d9` | H.264 High，1920×1080，25 fps，123 秒 | 3.8 Mbps | AAC | 0.179 核 |
  | `screensaver-005.mp4` | `0fa34b4e` | H.264 Main，1920×1080，30 fps，246 秒 | 3.1 Mbps | AAC | 0.214 核 |

- **播放时**（发起本次调查的会话实测）：渲染进程的 3 个 `Media` 线程各约 0.21 个核，GPU 进程主线程 0.27 个核，另有 `VizCompositor` 与 `VideoFrameCompositor`。当时播放的是哪一条没有记录。
- **黑屏时**（18:43–18:59 多次 15–30 秒采样）：会话 scope 0.26–0.30 个核。按线程：GPU 进程主线程 0.09–0.11，渲染进程主线程 0.07，渲染进程 `Compositor` 0.03，`VizCompositor` 0.03，Cage 0.013。10 秒内的唤醒次数：渲染进程 `Compositor` 每秒 245 次，GPU 进程主线程 80 次，`VizCompositor` 73 次，Cage 45 次。GPU 的 3D 引擎 10.2% 忙。也就是说，黑幕之下的页面仍在接近每秒 60 帧地出帧。
- **视频引擎从没用过**：Chromium GPU 进程的 DRM 文件描述符上，`drm-engine-video` 自 kiosk 启动以来累计 0 ns（i915 在 `/proc/<pid>/fdinfo` 中按客户端统计各引擎的忙碌时间）。
- **Chromium 154 的源码**（标签 [`154.0.8037.92`](https://chromium.googlesource.com/chromium/src/+/refs/tags/154.0.8037.92)）：
  - [`media/base/media_switches.cc`](https://chromium.googlesource.com/chromium/src/+/refs/tags/154.0.8037.92/media/base/media_switches.cc)：Linux 上的 `AcceleratedVideoDecoder` 在 `use_vaapi` 构建中默认开启，`AcceleratedVideoDecodeLinuxGL` 默认开启。
  - [`media/mojo/services/gpu_mojo_media_client_linux.cc`](https://chromium.googlesource.com/chromium/src/+/refs/tags/154.0.8037.92/media/mojo/services/gpu_mojo_media_client_linux.cc)：`AcceleratedVideoDecodeLinuxZeroCopyGL` 默认开启。GL 后端（Linux 的默认）下，只要前两个特性开着就选 VA-API 解码器，并在 EGL 能导入 NV12 时零拷贝显示；Vulkan 后端才需要另加特性。
  - [`media/gpu/vaapi/vaapi_wrapper.cc`](https://chromium.googlesource.com/chromium/src/+/refs/tags/154.0.8037.92/media/gpu/vaapi/vaapi_wrapper.cc)：GPU 进程在进入沙箱之前 `dlopen` `libva` 与 `libva-drm`，再由 libva 按渲染节点加载驱动。
  - [`gpu/config/software_rendering_list.json`](https://chromium.googlesource.com/chromium/src/+/refs/tags/154.0.8037.92/gpu/config/software_rendering_list.json)：没有条目在当前的 Intel Mesa 驱动上禁用 `accelerated_video_decode`。
  - [`docs/gpu/vaapi.md`](https://chromium.googlesource.com/chromium/src/+/refs/tags/154.0.8037.92/docs/gpu/vaapi.md) 仍列出 `AcceleratedVideoDecodeLinuxGL` 等开关，但它们已是默认值。
- **Debian 的构建**：trixie 的 [`150.0.7871.181-1~deb13u1`](https://sources.debian.org/src/chromium/150.0.7871.181-1~deb13u1/debian/rules/) 与 sid 的 `156.0.8078.47-1` 都在 amd64 上设 `use_vaapi=true`，补丁中没有关闭视频加速的。trixie 提供 `intel-media-va-driver` 25.2.3（main）。
- **黑幕之下的页面**：屏保与息屏的覆盖层都盖在一个照常运行的桌面上。桌面的 CPU 与内存表盘每 2 秒取一次读数，每次以 0.7 秒的过渡（带 `drop-shadow` 滤镜）移动指针与弧线；打开的窗口里还可能有无限循环的动画。Chromium 不因页面内一个不透明元素而停止渲染它下面的内容。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 没有 VA-API 驱动，Chromium 退回 FFmpeg 软件解码，GPU 进程再把每一帧拷进显存 | 读 Chromium 154 的源码；查已装软件包；读 GPU 进程的 `drm-engine-video` | 确认：特性默认开启、构建带 VA-API、唯一缺的是驱动；视频引擎累计 0 ns |
| 只装驱动、不加 Chromium 参数就能硬件解码 | root 安装 `intel-media-va-driver` 后重启 kiosk，读 GPU 进程加载的驱动、`drm-engine-video` 与渲染进程的 `Media` 线程 | 确认，见下面的实机测量 |
| 硬件解码后仍剩约 0.5 个核，主要是覆盖层下桌面的渲染与每秒 60 帧的合成 | 部署本改动后按同样方法复测屏保与黑屏 | pending：待部署 |
| 黑屏时的约 0.27 个核来自覆盖层下面仍在出帧的桌面 | 无头 Chrome 156 载入 Fake 模式的本机屏幕页面，表盘读数逐次变化，比较覆盖层下的样式计算与布局次数 | 确认，见下面的开发机对照 |
| 重新编码视频池（按屏幕裁成 4:3、降到 30 fps、去掉音轨）还能显著降低 | 页面改动部署后，比较 60 fps 与 30 fps 两类视频的整机占用 | pending：等部署后的数据再决定 |

### 开发机对照（2026-10-28）

无头 Chrome 156，窗口 1365×1024（与 kiosk 的 CSS 视口相同），Fake 模式的产品服务，脚本把每次 `/api/v1/metrics` 的读数改成随机值，模拟实机的变化。每种情况采样 20 秒：

| 情况 | 每秒样式计算 | 每秒布局 | 运行中的动画 |
|---|---|---|---|
| 桌面无覆盖 | 21.8 | 21.8 | 4 |
| 盖上全黑覆盖层（现状） | 22.5 | 22.5 | 4 |
| 覆盖层 + 页面 `visibility: hidden` | 22.5 | 22.3 | 4 |
| 覆盖层 + 页面 `content-visibility: hidden` | 0 | 0 | 0 |
| 覆盖层 + 页面 `display: none` | 0 | 0 | 0 |

`visibility: hidden` 不停止过渡；`content-visibility: hidden` 与 `display: none` 都让页面完全不渲染，前者保留布局状态，唤醒后原样显示。另测：覆盖期间滚过的列表仍停在原位置（`scrollTop` 600），`<audio>` 继续播放。无头模式没有 GPU 进程，这里只比较渲染进程的工作量。

改动后用真实组件复测（Fake 栈的屏保目录放入池中的 `1231b3ed`，等页面闲置 180 秒自己进入屏保，每种情况 15 秒）：

| 情况 | 渲染进程 CPU | 每秒样式计算 | 页面根节点 |
|---|---|---|---|
| 桌面无覆盖 | 0.052 核 | 23.0 | `visible` |
| 屏保（视频在渲染进程软件解码） | 0.090 核 | 0 | `hidden` |
| 按一次键唤醒后 | 0.099 核 | 22.4 | `visible` |
| 黑屏 | 0.008 核 | 0 | `hidden` |

屏保出现后 0、100、200 毫秒时根节点仍是 `visible`，400 毫秒时为 `hidden`：淡入期间仍能看到下面的桌面，之后才停止渲染。

### 实机测量（2026-10-29）

Experimental NAS 运行 `4ec62e17c3b1`，kiosk 以 `a-nas-kiosk` 运行，页面仍是改动前的版本。root 运行一次性脚本安装驱动，并在前后各测两次（日志在 NAS 的 `/var/tmp/a-nas-kiosk-vaapi.log`）。每次测量先重启 kiosk，等 215 秒让屏保开始，再测 60 秒。池中视频随机选择，无法确定每次播放的是哪一条。

ffmpeg 解码每条视频的前 20 秒（不解音频），每秒视频耗的 CPU：

| 池中名称 | 软件解码 | VA-API |
|---|---|---|
| `screensaver-000.mp4`（60 fps） | 0.349 核 | 0.032 核 |
| `screensaver-001.mp4`（60 fps） | 0.354 核 | 0.033 核 |
| `screensaver-002.mp4` | 0.155 核 | 0.015 核 |
| `screensaver-003.mp4` | 0.250 核 | 0.022 核 |
| `screensaver-004.mp4` | 0.169 核 | 0.013 核 |
| `screensaver-005.mp4` | 0.209 核 | 0.018 核 |

kiosk：

| 采样 | kiosk 会话 | 渲染进程 `Media` 线程 | GPU 进程主线程 | GPU 3D 引擎 | GPU 视频引擎 |
|---|---|---|---|---|---|
| 软件解码 1 | 0.867 核 | 0.332 核 | 0.194 核 | 34.2% | 0% |
| 软件解码 2 | 0.700 核 | 无 | 0.165 核 | 28.9% | 0% |
| VA-API 1 | 0.531 核 | 0.030 核 | 0.157 核 | 58.8% | 23.2% |
| VA-API 2 | 0.536 核 | 0.030 核 | 0.160 核 | 59.1% | 23.2% |

- **只装驱动就生效**：没有改任何 Chromium 参数，GPU 进程就加载了 `iHD_drv_video.so`，视频引擎 23% 忙，渲染进程的解码从约 0.33 核降到 0.03 核。`vainfo` 报告 `Intel iHD driver 25.2.3`，支持 H.264 Main/High 与 HEVC Main 解码。
- **软件解码第 2 次没有 `Media` 线程**：采样时屏保没有在播放视频，原因不明（可能是屏保还没开始，或者画面被输入唤醒了）。它反映的是桌面本身的开销，不能算作屏保的数据。
- **仍剩约 0.5 个核**：GPU 进程主线程 0.16 核，渲染进程主线程 0.09 核，`VizCompositor` 0.06 核，Cage 0.04 核，浏览器主线程 0.035 核，其余线程各 0.03 核以下。之后只读采样时，Cage 每秒唤醒约 128 次，即按每秒 60 帧出画。渲染进程主线程的大部分与 GPU 进程的一部分工作来自覆盖层下的桌面，要等本改动部署后复测。
- **GPU 3D 引擎从 34% 升到 59%**：一种可能是解码出的帧在 GPU 上又做了一次格式转换，即 Chromium 在 EGL 不能直接导入 NV12 时走 AR24 的一次拷贝路径。这占的是 GPU 时间，不是 CPU，原因待查。
- 这几次测量把计数差按 60 秒计算，但读取计数本身还要 1–2 秒，所以表中数值偏高约 2–3%。脚本现已改为按实际经过的时间计算，并删去了识别播放视频的部分：产品服务只在回应一次范围请求时打开文件，四次采样都没有抓到。

## 根因

两处相互独立的开销：

1. **软件解码**：NAS 上没有 VA-API 驱动。Chromium 154 默认会用 VA-API，但 libva 没有驱动可加载，于是渲染进程用 FFmpeg 在 CPU 上解码 1080p H.264（每条 0.16–0.35 个核，60 fps 的两条最贵），GPU 进程主线程再把每一帧上传成纹理。
2. **覆盖层下的页面照常渲染**：屏保与息屏只是盖在桌面上的元素。桌面表盘的过渡与窗口里的动画让 Chromium 一直出帧，黑屏阶段这部分就是全部开销，播放阶段则叠加在视频之上。登录画面（[#313](https://github.com/zhongwater123/A-NAS/pull/313)）的流星雨与字标用 `requestAnimationFrame` 逐帧绘制，本机屏幕没有登录时，黑屏同样盖在它们上面。

## 修复与回归证据

- 驱动：Experimental NAS 上由 root 安装 `intel-media-va-driver` 与 `vainfo`，不改 Chromium 参数。安装与核对步骤见[本地控制台手册的硬件视频解码](../runbooks/operate-local-kiosk.md#硬件视频解码)。kiosk 启动器在找不到任何 VA-API 驱动时向 journal 写一行警告（[`run-kiosk.sh`](../../scripts/run-kiosk.sh)）。
- 页面：两种覆盖层改为 `<body>` 的直接子元素，覆盖期间样式表让页面其余部分 `content-visibility: hidden`，等屏保淡入的 0.28 秒结束后才切换（[`LocalConsoleScreenSaver.tsx`](../../web/src/desktop/LocalConsoleScreenSaver.tsx)、[`desktop.module.css`](../../web/src/desktop/desktop.module.css)）。
- 登录画面：`content-visibility` 停掉样式、布局与绘制，却停不掉脚本逐帧运行的循环。[`AuthStage.tsx`](../../web/src/auth/AuthStage.tsx) 用 `IntersectionObserver` 得知画面不再显示，暂停流星雨与字标的循环，重新显示后继续（[`meteorShower.ts`](../../web/src/auth/meteorShower.ts)、[`bootIdent.ts`](../../web/src/boot/bootIdent.ts)）。Chrome 156 中，元素在 `content-visibility: hidden` 之下约 0.5 秒后观察者报告不再相交，覆盖层移除后 17 毫秒恢复。无头 Chrome 打开 Fake 栈的登录页，盖上黑屏覆盖层，每种情况 10 秒：

  | 情况 | 渲染进程 CPU | 主线程 |
  |---|---|---|
  | 登录页，未覆盖 | 0.145 核 | 0.091 核 |
  | 黑屏覆盖，循环照跑（禁用 `IntersectionObserver` 模拟改动前） | 0.056 核 | 0.047 核 |
  | 黑屏覆盖，循环暂停 | ≈ 0 | 0.003 核 |
  | 移除覆盖后 | 0.144 核 | 0.081 核 |
- 测量：[`measure-local-console-cpu.sh`](../../scripts/measure-local-console-cpu.sh) 只读地给出 kiosk 会话与用户 slice 的 CPU、最忙的线程、各 GPU 引擎的忙碌比例与已加载的 VA-API 驱动。
- 测试：[`LocalConsoleScreenSaver.test.tsx`](../../web/src/desktop/LocalConsoleScreenSaver.test.tsx) 确认两种覆盖层都在页面根节点之外、是 `<body>` 的子元素；`content-visibility` 的效果由上面的开发机对照与实机测量证明，jsdom 不计算样式。[`meteorShower.test.ts`](../../web/src/auth/meteorShower.test.ts) 与 [`bootIdent.test.ts`](../../web/src/boot/bootIdent.test.ts) 确认两个循环暂停期间不绘制、恢复后继续，[`AuthStage.test.tsx`](../../web/src/auth/AuthStage.test.tsx) 确认画面不可见时两者都暂停。
- 实机：2026-10-29 装上驱动后，屏保播放时 kiosk 会话从 0.87 核降到 0.53 核，见上面的实机测量。页面改动部署后复测屏保与黑屏。

## 后续工作

- 部署页面改动后，按[本地控制台手册](../runbooks/operate-local-kiosk.md#验收)第 12 项复测屏保与黑屏。
- 若屏保仍明显高于 0.3 个核，就分别测 60 fps 与 30 fps 的片段，例如临时安装只含一条视频的池，再决定是否重新编码视频池：按屏幕裁成 4:3 的 1440×1080，60 fps 降到 30 fps，去掉不播放的音轨。硬件解码下码率几乎不影响 CPU，剩下的开销与帧率成正比；但降帧会让画面不如原来顺滑。
- 查明硬件解码后 GPU 3D 引擎忙碌比例上升的原因（是否走了 AR24 的一次拷贝路径）。
- 其他硬件：AMD 核显用 `mesa-va-drivers`；NVIDIA 的 VA-API 驱动 Chromium 不支持。

## 关联

- 规格：[本地控制台屏保](../specs/local-console-screensaver.md)、[个性化](../specs/personalization.md)
- ADR：[0005 本地控制台](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)、[0014 屏保视频是系统盘上的长期媒体](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)
- 运行手册：[运行实验 NAS 本地控制台](../runbooks/operate-local-kiosk.md#硬件视频解码)
- 调查：[闲时检测只用了约 1 个核](2026-10-13-idle-detection-used-one-core.md)
