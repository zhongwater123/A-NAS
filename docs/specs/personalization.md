# 个性化

状态：implemented
更新时间：2026-10-23

## 目标

在“设置”中用一个“个性化”分区集中放置设备外观与本机屏幕行为的选项，以后的屏保视频更换等也加在这里。目前有两项，都对整台设备生效：

- **桌面壁纸**：管理员从自己的电脑上传图片，或选择 NAS 文件、相册中已有的图片作为壁纸，并选择填充、适应、拉伸、居中或平铺；所有账号的桌面都显示这张壁纸。没有设置时使用内置的默认壁纸。
- **本机屏幕息屏**：本地控制台的屏保播放满设定时长后屏幕变黑，停止视频解码，不再长时间显示画面；登录页没有屏保，在同样长的无人操作后也变黑。

## 非目标

- 不切断显示信号、不调节背光、不做 DPMS：Experimental NAS 的 Cage 0.2.0 不提供输出电源管理协议，直连的 LCD 也没有可由系统调节的背光。黑屏期间背光仍按显示器自身亮度点亮；改为真正关闭输出需要另行在实机验证。
- 不提供每个账号各自的壁纸、壁纸轮播、裁剪或调色；登录页的背景不变。
- 本期不提供屏保视频或屏保等待时间的修改，屏保仍在 3 分钟无输入后开始（见[本地控制台屏幕保护程序](local-console-screensaver.md)）。
- 远程浏览器不息屏：只有带 `?local-console=1` 的本地控制台页面计时。
- 修改壁纸或息屏时长不写入活动记录。

## 结构

“个性化”位于设置侧栏“存储”之后，暂时只有管理员能看到和使用；成员的侧栏中没有这一项。分区内有两组：

| 分组 | 内容 |
|---|---|
| 桌面壁纸 | 按当前浏览器屏幕比例画出的桌面预览；“更换图片…”与“恢复默认”；样式：填充（默认）、适应、拉伸、居中、平铺 |
| 本机屏幕 | 息屏：屏保播放多久后黑屏，10 分钟、30 分钟（默认）、1 小时、从不 |

样式和息屏都是点击即保存，失败时恢复原值并显示原因。息屏选项下方用一条时间线说明效果：无人操作 3 分钟开始屏保，再过设定时长黑屏；登录页在两者之和后黑屏。

## 场景

### 上传电脑上的图片作为壁纸

- Given：管理员在电脑浏览器中打开“个性化”。
- When：点击“更换图片…”，在“本机图片”中选择、拖入或粘贴一张图片，确认后点击“设为壁纸”。
- Then：浏览器把图片缩小到长边不超过 3840 像素并重新编码为 JPEG 后上传，拍摄位置等元数据不会上传；服务端再次检查并按同样规则保存。预览立即换成新壁纸，管理员自己的桌面随即更新。

### 选择 NAS 上已有的图片

- Given：管理员的个人空间、共享空间或相册中有照片。
- When：在“更换图片…”中切换到“NAS 文件”或“相册”，浏览文件夹或图库后选择一张并设为壁纸。
- Then：与上传相同；HEIC 照片使用 NAS 生成的预览图。只读查看他人空间或图库时看到的内容不在可选范围内。壁纸保存的是一份副本，原文件之后被修改、移动或删除都不影响壁纸，私有空间里的照片设为壁纸后所有账号都能看到。

### 所有账号看到同一张壁纸

- Given：管理员已设置壁纸，成员和本机屏幕的桌面正开着。
- When：管理员更换壁纸或样式。
- Then：其他已打开的桌面在 1 分钟内（或切回该浏览器标签页时）换成新壁纸，之后登录的桌面直接显示新壁纸。

### 五种样式

- 填充：保持比例铺满桌面，居中裁去多出的部分。
- 适应：完整显示整张图片，空白处为深蓝色。
- 拉伸：把图片拉到桌面的宽和高，比例可能改变。
- 居中：按图片原始大小居中显示，空白处为深蓝色；比桌面大的图片只显示中间部分。
- 平铺：按图片原始大小从左上角起重复铺满。

样式同样作用于默认壁纸。默认壁纸上的光晕效果只在使用默认壁纸时显示。

### 恢复默认壁纸

- Given：已设置自定义壁纸。
- When：管理员点击“恢复默认”并确认。
- Then：删除保存的图片，样式回到“填充”，所有桌面恢复内置默认壁纸。

### 图片无法使用

- Given：管理员选择了浏览器打不开的图片，或直接调用接口上传了非 JPEG、PNG、GIF、WebP 的内容或超过限制的图片。
- When：设为壁纸。
- Then：浏览器打不开时在选择窗口内说明原因，壁纸不变；接口返回 `415 unsupported_image` 或 `413 image_too_large`，壁纸不变。

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

### 成员看不到个性化

- Given：成员登录并打开设置。
- When：查看侧栏，或直接调用修改壁纸、息屏的接口。
- Then：侧栏中没有“个性化”；修改接口返回 `403 forbidden`。成员的桌面照常显示管理员设置的壁纸。

## 接口与存储

- `GET /api/v1/local-display` 返回 `{"sleepAfterMinutes": 30}`，`0` 表示从不。登录页也要读取，因此不需要登录；它只返回这一个时长。
- `PUT /api/v1/local-display` 只允许管理员，需要 CSRF 令牌，请求体与响应相同；`sleepAfterMinutes` 只接受 `0`、`10`、`30`、`60`，其他值返回 `422 validation_failed`。
- `GET /api/v1/wallpaper` 供所有已登录账号读取，返回 `{"style": "fill"}`，设置了图片时另有 `image: {version, width, height}`。`style` 为 `fill`、`fit`、`stretch`、`center` 或 `tile`。
- `PATCH /api/v1/wallpaper` 只允许管理员，请求体 `{"style": "fit"}`，其他样式返回 `422 validation_failed`。
- `PUT /api/v1/wallpaper/image` 只允许管理员，请求体是图片本身（JPEG、PNG、GIF 或 WebP，最大 24 MiB、2500 万像素）；服务端按 EXIF 方向转正，长边缩到不超过 3840 像素，透明处填成深蓝色，保存为 JPEG。`DELETE /api/v1/wallpaper/image` 恢复默认壁纸并把样式改回 `fill`。两者都返回新的壁纸设置。
- `GET /api/v1/wallpaper/image?v=<version>` 供所有已登录账号读取图片；版本号由内容决定，带当前版本号的地址可长期缓存。
- 设置与壁纸图片保存在产品服务状态目录（系统盘）的 `personalization.db`（SQLite），只保留当前一张图片。尚未保存过时为默认壁纸、填充和 30 分钟息屏；升级时自动建表，不需要手工迁移；回滚到旧版本时旧版本不读取这个文件，桌面恢复默认壁纸，本机屏幕恢复为不息屏。

## 边界与失败

- `local-console=1` 只是显示标记，息屏不涉及权限；接口的权限由服务端执行。
- 计时与屏保使用同一组输入事件。页面被隐藏时停止计时并回到桌面，重新可见时重新开始计时。
- 黑屏时不播放视频、不请求视频，Chromium 不再解码。
- 黑屏盖住页面上的一切，包括全屏的相册幻灯片和打开的预览；无人操作的幻灯片同样在 3 分钟加息屏时长后变黑。
- 服务端一次只解码一张上传的图片（与头像共用），避免同时解码多张大图占满内存。
- 读取壁纸失败时桌面显示默认壁纸，不影响其他功能。

## 验收证据

- 屏保后黑屏、没有视频时黑屏、登录页黑屏、“从不”、读取失败按默认值、第一次输入只唤醒：[`LocalConsoleScreenSaver.test.tsx`](../../web/src/LocalConsoleScreenSaver.test.tsx)。
- 设置分区的息屏与壁纸样式修改、失败恢复、上传与恢复默认：[`PersonalizationSection.test.tsx`](../../web/src/settings/PersonalizationSection.test.tsx)；成员侧栏没有“个性化”：[`SettingsPanel.test.tsx`](../../web/src/SettingsPanel.test.tsx)。
- 五种样式对应的显示方式：[`wallpaper.test.ts`](../../web/src/wallpaper.test.ts)。
- 存储默认值、只接受四个时长与五种样式、图片转正缩小与透明填色、恢复默认、重新打开后保留：[`personalization_test.go`](../../internal/personalization/personalization_test.go)、[`wallpaper_test.go`](../../internal/personalization/wallpaper_test.go)。
- 未登录读取息屏、管理员修改、成员 `403`、无效值 `422`、图片的 `413`/`415`、缓存头与 OpenAPI 契约：[`local_display_test.go`](../../internal/httpapi/local_display_test.go)、[`wallpaper_test.go`](../../internal/httpapi/wallpaper_test.go)。

## 关联

- 规格：[设置](settings.md)、[本地控制台屏幕保护程序](local-console-screensaver.md)、[头像与昵称](account-profile.md)（同一套图片来源）
- ADR：[本地控制台](../adr/0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- 代码：[`personalization.go`](../../internal/personalization/personalization.go)、[`wallpaper.go`](../../internal/personalization/wallpaper.go)、[`personalizationapi.go`](../../internal/personalizationapi/personalizationapi.go)、[`LocalConsoleScreenSaver.tsx`](../../web/src/LocalConsoleScreenSaver.tsx)、[`wallpaper.ts`](../../web/src/wallpaper.ts)、[`PersonalizationSection.tsx`](../../web/src/settings/PersonalizationSection.tsx)、[`WallpaperDialog.tsx`](../../web/src/settings/WallpaperDialog.tsx)、[`ImageSources.tsx`](../../web/src/settings/ImageSources.tsx)
- 运行手册：[运行实验 NAS 本地控制台](../runbooks/operate-local-kiosk.md)
