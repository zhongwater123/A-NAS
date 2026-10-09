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
- When：安装新 release，或回滚到旧 release。
- Then：仍播放原视频池，清单数量不变，不需要重新上传视频。release 不携带视频，只有管理员显式安装另一个池才会更换。

### 只上传 NAS 还没有的视频

- Given：NAS 上已有部分视频，包括早先随 release 暂存过的。
- When：从开发机暂存一个包含新旧视频的池并安装。
- Then：只传输和存储新视频；同一视频出现在多个池中也只保存一份。

### 视频池不可用时保留壁纸

- Given：视频目录不存在、清单为空，或池内全部视频均播放失败。
- When：本地控制台加载或播放屏保。
- Then：不显示视频覆盖层且本次页面会话不再重试，静态桌面继续可用。

## 边界与失败

- `local-console=1` 只是显示模式标记，不是权限凭据；产品 API 和 Host Agent 权限边界不变。
- 产品服务只读扫描 `ANAS_SCREENSAVER_DIRECTORY` 指向的绝对目录，只接纳该目录第一层的普通 `.mp4` 文件；默认值与系统安装器写入的值都是 `/var/lib/a-nas/screensavers/current`。该路径可以是指向目录的符号链接，每次请求重新读取，因此切换链接后无需重启产品服务。
- `/local-console/screensavers` 返回不含宿主机文件名的同源视频 URL 清单；各视频 URL 支持标准字节 Range。原 `/local-console/screensaver.mp4` 保留为读取排序后首个视频的兼容路由。
- 屏保视频是系统盘上的长期媒体，不随 release 发布（[ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)）。每个视频以 SHA-256 命名存放在 `/var/lib/a-nas/screensavers/objects/`；池是 `pools/<池 ID>/` 下匿名编号的硬链接和 `SHA256SUMS`，池 ID 是清单 SHA-256 的前 16 位十六进制；`current` 链接指向当前池。外部媒体缺失不阻止产品服务、登录或桌面启动。
- 只有 root 执行的 `install-screensavers.sh` 会改变当前池。它逐个复核 SHA-256、核对池 ID 与清单一致，任一不符即在改动前停止，然后原子切换 `current`。指定一个已安装的池时直接切换，作为媒体回滚；旧池和对象都保留。
- 升级和产品回滚都不改变 `current`。首次升级时还没有 `current`，系统安装器以硬链接导入旧安装器留下的最近一个池并设为当前池。旧安装器总让这个池生效，或因 [issue #49](https://github.com/zhongwater123/A-NAS/issues/49) 在不带视频的升级后什么都不播，因此迁移后无需重新上传视频。

## 验收证据

- React 定时、输入消费、每次进入只随机选择一条并循环、失败补选与远程模式测试见 [`LocalConsoleScreenSaver.test.tsx`](../../web/src/LocalConsoleScreenSaver.test.tsx)。
- 清单筛选、匿名 URL、兼容路由与视频 Range 测试见 [`handler_test.go`](../../internal/webui/handler_test.go)。
- 视频通道见[系统测试](../../scripts/system-test.sh)，全程使用真实的暂存脚本、媒体安装脚本和系统安装器：只上传缺少的视频、复用旧 release 暂存过的视频、升级保留当前池、共有视频只存一份、重复安装不产生变化、不需要暂存区的媒体回滚、拒绝哈希不符或 ID 不符的池且不留改动，以及从旧安装器首次升级时导入旧池。链接切换即时生效见 [`handler_test.go`](../../internal/webui/handler_test.go)。
- 更换视频、媒体回滚和直连屏幕验收见[本地控制台运行手册](../runbooks/operate-local-kiosk.md)。

## 关联

- 架构：[架构总览](../architecture/OVERVIEW.md)
- ADR：[本地控制台](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)、[屏保视频是系统盘上的长期媒体](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)
- 代码：[`LocalConsoleScreenSaver.tsx`](../../web/src/LocalConsoleScreenSaver.tsx)、[`handler.go`](../../internal/webui/handler.go)、[`install-screensavers.sh`](../../scripts/install-screensavers.sh)、[`remote-stage-screensavers.sh`](../../scripts/remote-stage-screensavers.sh)、[`deploy-screensavers.ps1`](../../scripts/deploy-screensavers.ps1)、[`install-v1.0.1-system-services.sh`](../../scripts/install-v1.0.1-system-services.sh)
- 测试：[`App.test.tsx`](../../web/src/App.test.tsx)、[`LocalConsoleScreenSaver.test.tsx`](../../web/src/LocalConsoleScreenSaver.test.tsx)、[`handler_test.go`](../../internal/webui/handler_test.go)、[`system-test.sh`](../../scripts/system-test.sh)
- Issue：[#49 不带屏保视频的升级会让本地控制台屏保失效](https://github.com/zhongwater123/A-NAS/issues/49)
