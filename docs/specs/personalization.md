# 个性化

状态：implemented（尚未合并）
更新时间：2026-10-20

## 目标

在“设置”中用一个“个性化”分区集中放置设备外观与本机屏幕行为的选项，以后的壁纸、屏保视频更换都加在这里。第一项是本机屏幕息屏：本地控制台的屏保播放满设定时长后屏幕变黑，停止视频解码，不再长时间显示画面；登录页没有屏保，在同样长的无人操作后也变黑。

## 非目标

- 不切断显示信号、不调节背光、不做 DPMS：Experimental NAS 的 Cage 0.2.0 不提供输出电源管理协议，直连的 LCD 也没有可由系统调节的背光。黑屏期间背光仍按显示器自身亮度点亮；改为真正关闭输出需要另行在实机验证。
- 本期不提供壁纸、屏保视频或屏保等待时间的修改，屏保仍在 3 分钟无输入后开始（见[本地控制台屏幕保护程序](local-console-screensaver.md)）。
- 远程浏览器不息屏：只有带 `?local-console=1` 的本地控制台页面计时。
- 修改息屏时长不写入活动记录。

## 结构

“个性化”位于设置侧栏“存储”之后，所有账号可见。本期只有“本机屏幕”一组：

| 项目 | 成员 | 管理员 | 内容 |
|---|---|---|---|
| 息屏 | 只读 | 可改 | 屏保播放多久后黑屏：10 分钟、30 分钟（默认）、1 小时、从不 |

选项下方用一条时间线说明当前设置的效果：无人操作 3 分钟开始屏保，再过设定时长黑屏；登录页在两者之和后黑屏。管理员点击选项即保存，失败时恢复原值并显示原因；成员的选项不可点击，并注明由管理员设置。

## 场景

### 屏保播放满设定时长后黑屏

- Given：本地控制台已登录，息屏为 30 分钟，视频池可用。
- When：连续 3 分钟没有键盘、鼠标、滚轮或触摸输入，屏保开始；之后继续 30 分钟没有输入。
- Then：视频停止并从页面移除，屏幕变为纯黑并隐藏指针，一直保持到下一次输入。

### 第一次输入只唤醒

- Given：屏幕处于黑屏。
- When：用户按键、移动或点击鼠标、滚动或触摸屏幕。
- Then：黑屏消失并回到桌面（不回到屏保），这次输入不传给下层窗口；重新开始 3 分钟计时。

### 没有屏保视频时仍会黑屏

- Given：本地控制台已登录，视频池为空，或池内视频全部播放失败。
- When：连续 3 分钟加息屏时长没有输入。
- Then：桌面直接变黑，唤醒方式相同。

### 登录页息屏

- Given：本地控制台停在登录页、设备启用页或强制改密页，息屏为 30 分钟。
- When：连续 33 分钟没有输入。
- Then：屏幕变黑；第一次输入只唤醒，已经输入的账号和密码保留，按键不会写进输入框。

### 设为“从不”

- Given：管理员把息屏设为“从不”。
- When：本地控制台长时间无人操作。
- Then：已登录时屏保一直播放，登录页一直显示。

### 修改从下一次闲置开始生效

- Given：本地控制台正在显示桌面。
- When：管理员在手机浏览器把息屏改为 10 分钟。
- Then：本机屏幕在下一次无人操作满 3 分钟时读取设置，按 10 分钟计算；已经开始的屏保仍按开始时读到的时长。

### 读取设置失败

- Given：产品服务暂时不可用。
- When：本机屏幕无人操作满 3 分钟。
- Then：按默认 30 分钟息屏，不让屏幕一直亮着。

### 成员不能修改

- Given：成员打开“个性化”。
- When：查看或尝试修改息屏。
- Then：看到当前值但选项不可点击；直接调用修改接口返回 `403 forbidden`。

## 接口与存储

- `GET /api/v1/local-display` 返回 `{"sleepAfterMinutes": 30}`，`0` 表示从不。登录页也要读取，因此不需要登录；它只返回这一个时长。
- `PUT /api/v1/local-display` 只允许管理员，需要 CSRF 令牌，请求体与响应相同；`sleepAfterMinutes` 只接受 `0`、`10`、`30`、`60`，其他值返回 `422 validation_failed`。
- 设置保存在产品服务状态目录（系统盘）的 `personalization.db`（SQLite）。尚未保存过时为默认 30 分钟；升级时自动建表，不需要手工迁移；回滚到旧版本时旧版本不读取这个文件，本机屏幕恢复为不息屏。

## 边界与失败

- `local-console=1` 只是显示标记，息屏不涉及权限；接口的权限由服务端执行。
- 计时与屏保使用同一组输入事件。页面被隐藏时停止计时并回到桌面，重新可见时重新开始计时。
- 黑屏时不播放视频、不请求视频，Chromium 不再解码。
- 黑屏盖住页面上的一切，包括全屏的相册幻灯片和打开的预览；无人操作的幻灯片同样在 3 分钟加息屏时长后变黑。

## 验收证据

- 屏保后黑屏、没有视频时黑屏、登录页黑屏、“从不”、读取失败按默认值、第一次输入只唤醒：[`LocalConsoleScreenSaver.test.tsx`](../../web/src/LocalConsoleScreenSaver.test.tsx)。
- 设置分区的管理员修改、失败恢复与成员只读：[`PersonalizationSection.test.tsx`](../../web/src/settings/PersonalizationSection.test.tsx)。
- 存储默认值、只接受四个时长、重新打开后保留：[`personalization_test.go`](../../internal/personalization/personalization_test.go)。
- 未登录读取、管理员修改、成员 `403`、无效值 `422` 与 OpenAPI 契约：[`local_display_test.go`](../../internal/httpapi/local_display_test.go)。

## 关联

- 规格：[设置](settings.md)、[本地控制台屏幕保护程序](local-console-screensaver.md)
- ADR：[本地控制台](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- 代码：[`personalization.go`](../../internal/personalization/personalization.go)、[`local_display.go`](../../internal/httpapi/local_display.go)、[`LocalConsoleScreenSaver.tsx`](../../web/src/LocalConsoleScreenSaver.tsx)、[`PersonalizationSection.tsx`](../../web/src/settings/PersonalizationSection.tsx)
- 运行手册：[运行实验 NAS 本地控制台](../runbooks/operate-local-kiosk.md)
