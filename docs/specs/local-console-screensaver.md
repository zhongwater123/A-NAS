# 本地控制台屏幕保护程序

状态：implemented
更新时间：2026-10-09

## 目标

已登录的 A-NAS 本地控制台连续三分钟没有键盘、鼠标或触摸输入后，从视频池随机选择一条静音循环并覆盖桌面；退出后下次闲置再独立随机选择，任意首次输入只退出屏保，不误触发下层应用。

## 非目标

- 不在远程浏览器、登录页或设备启用页启动屏保。
- 不播放声音，不提供视频池设置界面、锁屏认证、显示器休眠或 DPMS 管理。
- 不把外部视频提交到 Git 或嵌入产品二进制。

## 场景

### 每次闲置后随机选择一条循环

- Given：Chromium 通过 `?local-console=1` 打开产品页面，用户已经登录，并且宿主机已安装至少一个屏保视频。
- When：连续 180 秒没有键盘、鼠标、滚轮或触摸输入。
- Then：从当前可用池中等概率随机选择一条，全屏静音循环、隐藏指针并使用居中 `cover` 填满屏幕；保持屏保期间不切换到其他视频。每次选择彼此独立，因此下次可能再次选中同一条。

### 首次输入只返回桌面

- Given：屏保正在播放。
- When：用户移动或点击鼠标、滚动、按键或触摸屏幕。
- Then：屏保立即关闭、重新开始三分钟计时，触发退出的输入不会传递给桌面窗口或图标；下次进入时从全部仍可用视频中重新随机选择。

### 远程浏览不启动屏保

- Given：用户通过不带本地控制台标记的产品 URL 登录。
- When：页面超过三分钟没有输入。
- Then：页面保持原状，不读取视频池、不挂载屏保计时器或视频。

### 单个视频失败时继续

- Given：池内某个视频的 HTTP Range 请求失败，或 Chromium 无法解码播放。
- When：该视频开始播放。
- Then：本次页面会话从池中移除该视频并立即从剩余条目中随机补选一条，不影响其他视频。

### 升级产品不改变视频池

- Given：宿主机已有一个视频池在播放。
- When：安装一个不携带视频的新 release，或回滚到旧 release。
- Then：仍播放原视频池，清单数量不变，不需要重新上传视频；只有安装携带视频的 release 才换成新池。

### 视频池不可用时保留壁纸

- Given：视频目录不存在、清单为空，或池内全部视频均播放失败。
- When：本地控制台加载或播放屏保。
- Then：不显示视频覆盖层且本次页面会话不再重试，静态桌面继续可用。

## 边界与失败

- `local-console=1` 只是显示模式标记，不是权限凭据；产品 API 和 Host Agent 权限边界不变。
- 产品服务只读扫描 `ANAS_SCREENSAVER_DIRECTORY` 指向的绝对目录，只接纳该目录第一层的普通 `.mp4` 文件；默认值与系统安装器写入的值都是 `/var/lib/a-nas/screensavers/current`。该路径可以是指向目录的符号链接，每次请求重新读取，因此切换链接后无需重启产品服务。
- `/local-console/screensavers` 返回不含宿主机文件名的同源视频 URL 清单；各视频 URL 支持标准字节 Range。原 `/local-console/screensaver.mp4` 保留为读取排序后首个视频的兼容路由。
- 部署脚本将每个外部视频分别计算并验证 SHA-256，以匿名编号文件构成完整池；外部媒体缺失不阻止产品服务、登录或桌面启动。
- 视频池的生命周期独立于产品版本。系统安装器把携带视频的 release 逐个复核 SHA-256 后安装为 `/var/lib/a-nas/screensavers/<release-id>/`，再把同级的 `current` 链接原子切到该池；任一视频不符时在改动任何文件前停止。不携带视频的 release 不建池，也不改 `current`。旧池保留，供媒体回滚。
- 首次以本规则安装时还没有 `current`，安装器指向最近安装的池。旧安装器总让最近安装的池生效，或因 [issue #49](https://github.com/zhongwater123/A-NAS/issues/49) 在不带视频的升级后不播放任何池，因此迁移后无需重新上传视频。

## 验收证据

- React 定时、输入消费、每次进入只随机选择一条并循环、失败补选与远程模式测试见 [`LocalConsoleScreenSaver.test.tsx`](../../web/src/LocalConsoleScreenSaver.test.tsx)。
- 清单筛选、匿名 URL、兼容路由与视频 Range 测试见 [`handler_test.go`](../../internal/webui/handler_test.go)。
- 真实安装器的视频池生命周期见[系统测试](../../scripts/system-test.sh)：带视频安装、不带视频升级保留当前池、哈希不符停止升级、新视频替换当前池并保留旧池，以及从旧安装器首次升级。链接切换即时生效见 [`handler_test.go`](../../internal/webui/handler_test.go)。
- 多文件哈希、安装和直连屏幕验收见[本地控制台运行手册](../runbooks/operate-local-kiosk.md)与[部署手册](../runbooks/deploy-web-preview-to-experimental-nas.md)。

## 关联

- 架构：[架构总览](../architecture/OVERVIEW.md)
- ADR：[本地控制台](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- 代码：[`LocalConsoleScreenSaver.tsx`](../../web/src/LocalConsoleScreenSaver.tsx)、[`handler.go`](../../internal/webui/handler.go)、[`install-v1.0.1-system-services.sh`](../../scripts/install-v1.0.1-system-services.sh)
- 测试：[`App.test.tsx`](../../web/src/App.test.tsx)、[`LocalConsoleScreenSaver.test.tsx`](../../web/src/LocalConsoleScreenSaver.test.tsx)、[`handler_test.go`](../../internal/webui/handler_test.go)、[`system-test.sh`](../../scripts/system-test.sh)
- Issue：[#49 不带屏保视频的升级会让本地控制台屏保失效](https://github.com/zhongwater123/A-NAS/issues/49)
