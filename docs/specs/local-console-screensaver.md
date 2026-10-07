# 本地控制台屏幕保护程序

状态：implemented locally；hardware acceptance pending
更新时间：2026-10-07

## 目标

已登录的 A-NAS 本地控制台连续三分钟没有键盘、鼠标或触摸输入后，以静音循环视频覆盖桌面；任意首次输入只退出屏保，不误触发下层应用。

## 非目标

- 不在远程浏览器、登录页或设备启用页启动屏保。
- 不播放声音，不提供随机视频池、设置界面、锁屏认证、显示器休眠或 DPMS 管理。
- 不把外部视频提交到 Git 或嵌入产品二进制。

## 场景

### 本地控制台闲置后播放

- Given：Chromium 通过 `?local-console=1` 打开产品页面，用户已经登录，并且宿主机已安装屏保视频。
- When：连续 180 秒没有键盘、鼠标、滚轮或触摸输入。
- Then：全屏视频静音、循环、隐藏指针并使用居中 `cover` 填满屏幕。

### 首次输入只返回桌面

- Given：屏保正在播放。
- When：用户移动或点击鼠标、滚动、按键或触摸屏幕。
- Then：屏保立即关闭、重新开始三分钟计时，触发退出的输入不会传递给桌面窗口或图标。

### 远程浏览不启动屏保

- Given：用户通过不带本地控制台标记的产品 URL 登录。
- When：页面超过三分钟没有输入。
- Then：页面保持原状，不挂载屏保计时器或视频。

### 视频不可用时保留壁纸

- Given：视频未安装、HTTP Range 请求失败或浏览器无法解码播放。
- When：屏保尝试启动。
- Then：视频覆盖层消失且本次页面会话不再重试，静态桌面继续可用。

## 边界与失败

- `local-console=1` 只是显示模式标记，不是权限凭据；产品 API 和 Host Agent 权限边界不变。
- 产品服务从 `ANAS_SCREENSAVER_VIDEO` 指向的绝对路径只读提供 `/local-console/screensaver.mp4`，支持标准字节 Range；默认路径为 `/var/lib/a-nas/screensavers/computer-chip.mp4`。
- 视频作为部署时显式提供的外部资产保存，缺失不阻止产品服务、登录或桌面启动。

## 验收证据

- React 定时、输入消费、远程模式与播放失败测试见 [`LocalConsoleScreenSaver.test.tsx`](../../web/src/LocalConsoleScreenSaver.test.tsx)。
- 视频 Range 与缺失回退测试见 [`handler_test.go`](../../internal/webui/handler_test.go)。
- 实机安装与三分钟验收见[本地控制台运行手册](../runbooks/operate-local-kiosk.md)。

## 关联

- 架构：[架构总览](../architecture/OVERVIEW.md)
- ADR：[本地控制台](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- 代码：[`LocalConsoleScreenSaver.tsx`](../../web/src/LocalConsoleScreenSaver.tsx)、[`handler.go`](../../internal/webui/handler.go)
- 测试：[`App.test.tsx`](../../web/src/App.test.tsx)、[`LocalConsoleScreenSaver.test.tsx`](../../web/src/LocalConsoleScreenSaver.test.tsx)、[`handler_test.go`](../../internal/webui/handler_test.go)
