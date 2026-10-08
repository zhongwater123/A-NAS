# 本地控制台屏幕保护程序

状态：implemented locally；video-pool hardware acceptance pending
更新时间：2026-10-08

## 目标

已登录的 A-NAS 本地控制台连续三分钟没有键盘、鼠标或触摸输入后，以静音视频池覆盖桌面；每轮随机播放池内全部视频且不重复，任意首次输入只退出屏保，不误触发下层应用。

## 非目标

- 不在远程浏览器、登录页或设备启用页启动屏保。
- 不播放声音，不提供视频池设置界面、锁屏认证、显示器休眠或 DPMS 管理。
- 不把外部视频提交到 Git 或嵌入产品二进制。

## 场景

### 本地控制台闲置后随机轮播

- Given：Chromium 通过 `?local-console=1` 打开产品页面，用户已经登录，并且宿主机已安装至少一个屏保视频。
- When：连续 180 秒没有键盘、鼠标、滚轮或触摸输入。
- Then：全屏视频静音、隐藏指针并使用居中 `cover` 填满屏幕；多个视频按随机顺序逐个完整播放，一轮内不重复，跨轮不立即重复上一条；池内只有一个视频时循环该视频。

### 首次输入只返回桌面

- Given：屏保正在播放。
- When：用户移动或点击鼠标、滚动、按键或触摸屏幕。
- Then：屏保立即关闭、重新开始三分钟计时，触发退出的输入不会传递给桌面窗口或图标；下次进入时继续池内尚未播放的条目。

### 远程浏览不启动屏保

- Given：用户通过不带本地控制台标记的产品 URL 登录。
- When：页面超过三分钟没有输入。
- Then：页面保持原状，不读取视频池、不挂载屏保计时器或视频。

### 单个视频失败时继续

- Given：池内某个视频的 HTTP Range 请求失败，或 Chromium 无法解码播放。
- When：该视频开始播放。
- Then：本次页面会话从池中移除该视频并立即尝试下一条，不影响其他视频。

### 视频池不可用时保留壁纸

- Given：视频目录不存在、清单为空，或池内全部视频均播放失败。
- When：本地控制台加载或播放屏保。
- Then：不显示视频覆盖层且本次页面会话不再重试，静态桌面继续可用。

## 边界与失败

- `local-console=1` 只是显示模式标记，不是权限凭据；产品 API 和 Host Agent 权限边界不变。
- 产品服务只读扫描 `ANAS_SCREENSAVER_DIRECTORY` 指向的绝对目录，只接纳该目录第一层的普通 `.mp4` 文件；默认目录为 `/var/lib/a-nas/screensavers`。
- `/local-console/screensavers` 返回不含宿主机文件名的同源视频 URL 清单；各视频 URL 支持标准字节 Range。原 `/local-console/screensaver.mp4` 保留为读取排序后首个视频的兼容路由。
- 部署脚本将每个外部视频分别计算并验证 SHA-256，以匿名编号文件构成完整池；外部媒体缺失不阻止产品服务、登录或桌面启动。
- Experimental NAS 使用按 release ID 隔离的持久视频池；切换产品版本不会覆盖上一池，便于回滚。

## 验收证据

- React 定时、输入消费、无重复轮播、失败跳过与远程模式测试见 [`LocalConsoleScreenSaver.test.tsx`](../../web/src/LocalConsoleScreenSaver.test.tsx)。
- 清单筛选、匿名 URL、兼容路由与视频 Range 测试见 [`handler_test.go`](../../internal/webui/handler_test.go)。
- 多文件哈希、安装和直连屏幕验收见[本地控制台运行手册](../runbooks/operate-local-kiosk.md)与[部署手册](../runbooks/deploy-web-preview-to-experimental-nas.md)。

## 关联

- 架构：[架构总览](../architecture/OVERVIEW.md)
- ADR：[本地控制台](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- 代码：[`LocalConsoleScreenSaver.tsx`](../../web/src/LocalConsoleScreenSaver.tsx)、[`handler.go`](../../internal/webui/handler.go)
- 测试：[`App.test.tsx`](../../web/src/App.test.tsx)、[`LocalConsoleScreenSaver.test.tsx`](../../web/src/LocalConsoleScreenSaver.test.tsx)、[`handler_test.go`](../../internal/webui/handler_test.go)
