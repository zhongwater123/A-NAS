# Web 错误提示与时间格式

状态：implemented
更新时间：2026-10-27

## 错误边界

产品 API 的生成客户端、宿主机状态、应用中心、容器请求及上传通道使用同一个 `APIError`，保留 `status`、`code` 与诊断用 `message`。网络失败用状态 `0`、`network_error`；不可解析的响应使用 `malformed_response`。主动取消保留 `AbortError`，不显示请求失败。tus 上传额外保留队列需要的失败分类，决定等待、重试或停止，不再另建错误类型。

JSON 请求、multipart 上传和媒体流都复用会话失效通知：收到 401 后让桌面回到登录流程。媒体流仍逐段读取响应体，通过公共 `apiFetch` 处理会话，避免把视频缓冲成完整文件。

界面只展示按错误码查到的中文文案。数据卷不可用、网络中断、登录失效等公共文案只有一个定义；照片不存在、分享撤回等领域含义可以覆盖。未知错误统一显示请求失败或服务暂不可用，不直接回退到浏览器或服务端的 `message`。应用安装冲突可以附加经过格式校验的端口或容器名；反馈的字数、图片数上限继续使用该领域的约束。

## 时间与日期

显示文案使用 `format/time.ts`，由 `Intl.RelativeTimeFormat` 提供单位及过去/将来时态，`Intl.DateTimeFormat("zh-CN", …)` 提供日期、星期和时钟。显示本地时区；无效日期显示 `—`，分钟级时钟使用 24 小时制。

- 相对时长依据时间戳之差，不足一分钟显示“刚刚”，未来时间显示“后”。不同列表可选择何时转为日期；跨年日期包含年份。
- “今天”“昨天”“明天”按本地日历日期判断，不用固定 24 小时判断相邻日期，避免午夜和夏令时偏差。
- 日期旁是否带星期、时间，及是否保留“今天”等分组标签由所在界面决定，格式化使用公共函数。
- 数字与中文单位之间保留空格。播放位置、任务用时等持续时长不属于日历相对时间；文件名、导出日志和索引用的固定时间键也不作为面向用户的中文日期拼接。
- 助手附件大小复用既有的二进制[容量单位](size-units.md)格式化。

## 验证

运行 Web format、typecheck、lint、完整 Web 测试、Web build 和 `make docs-check`。回归覆盖 API 与上传的网络错误、取消、状态码保留、未知错误安全回退、播放流 401，以及午夜、跨年、未来时间、无效日期和夏令时的日历标签。

## 关联

- [Issue #276](https://github.com/zhongwater123/A-NAS/issues/276)
- [请求边界](../../web/src/api.ts)、[API 回归](../../web/src/api.test.ts)、[上传分类](../../web/src/transfers/store.ts)
- [公共与领域错误文案](../../web/src/errorMessages.ts)、[文案回归](../../web/src/errorMessages.test.ts)
- [公共时间格式](../../web/src/format/time.ts)、[时间回归](../../web/src/format/time.test.ts)
- [媒体流](../../web/src/media/streamFeed.ts)、[媒体流回归](../../web/src/media/streamFeed.test.ts)
