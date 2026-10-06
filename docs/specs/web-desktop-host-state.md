# Web 桌面宿主机状态

状态：implemented；local-console confinement pending
更新时间：2026-10-06

## 目标

设备所有者通过 A-NAS Web 桌面查看一次原子观测的系统与磁盘状态，明确区分模拟数据、实时主机、未知健康和连接中断；该页面只读且不把尚未实现的应用伪装为可用。

## 非目标

- 不实现身份认证、TLS、公网或局域网直接开放。
- 不实现文件、备份、容器或 AI 应用；对应启动器只显示“规划中”。
- 不保存窗口位置，不执行 SMART、挂载、分区、格式化或其他宿主机写操作。

## 场景

### 通过桌面读取模拟状态

- Given：产品服务使用 Fake Adapter。
- When：浏览器打开桌面并启动“资源管理”。
- Then：资源管理窗口显示 Debian 13、确定性磁盘和“模拟数据”，并且数据来自一个 `/api/v1/host-state` 快照。

### 未评估的健康保持未知

- Given：实时系统或磁盘健康为 `unknown`。
- When：界面展示状态。
- Then：使用中性的“未知”，不显示为绿色健康，也不推断 SMART 结果。

### 连接失败时保留最后观测

- Given：至少成功读取一次，后续刷新返回 503 或网络失败。
- When：轮询或手动刷新完成。
- Then：保留最后成功数据并显示连接中断；恢复后自动替换为最新状态。

### 管理桌面窗口

- Given：资源管理或系统设置已打开。
- When：用户拖动、缩放、最小化、恢复、最大化或关闭窗口。
- Then：窗口在当前浏览器会话中按操作变化；刷新页面后回到默认布局。

### 未实现应用保持不可用

- Given：文件、备份、容器或 AI 功能尚未交付。
- When：用户查看启动器。
- Then：入口禁用并标记“规划中”，不会出现虚构的可用状态。

### 桌面时钟持续跟随系统墙钟

- Given：Web 桌面保持打开并跨过一个或多个分钟边界。
- When：浏览器的系统墙钟前进或被操作系统校正。
- Then：顶部时钟在下一个分钟边界重新读取当前时间，不冻结在页面首次渲染值，也不累计 timer 延迟。

### 在 NAS 直连屏幕使用同一桌面

- Given：Experimental NAS 连接屏幕、键盘和鼠标，Kiosk 会话已安装。
- When：设备进入图形启动目标。
- Then：Cage 中的 Chromium 以 `anas-dev` 打开回环地址上的同一 Web 桌面，不出现通用桌面、浏览器导航界面或额外特权 API。

当前实机已验证显示、中文字体和鼠标窗口交互；`F1` 可进入通用 Chromium 界面，且 VT 切换未通过，因此本场景尚未完全验收，见[开放调查](../investigations/2026-10-06-kiosk-browser-confinement.md)。

## 边界与失败

- 首次读取失败显示可重试空态；成功后的失败显示失联状态。
- 前台页面每 10 秒刷新，页面隐藏时暂停，重新可见时立即刷新。
- 实时观测超过 30 秒显示过期提示；确定性的 Fake 时间不按实时过期处理。
- 浏览器只访问同源产品接口，不直接访问 Host Agent。
- SSH 隧道是远程开发入口，不是本地控制台运行的前提。
- 桌面只显示浏览器所在设备的系统墙钟，不自行实现网络校时，也不向浏览器或非特权 Host Agent 暴露改时能力。

## 验收证据

- React 行为测试：[桌面测试](../../web/src/App.test.tsx)。
- 产品契约测试：[HTTP 测试](../../internal/httpapi/server_test.go)、[OpenAPI 测试](../../internal/httpapi/openapi_test.go)。
- 嵌入资源测试：[Web UI Handler 测试](../../internal/webui/handler_test.go)。

## 关联

- ADR：[Web 桌面技术选择](../adr/0003-use-react-typescript-for-web-desktop.md)
- ADR：[客户端 REST/OpenAPI](../adr/0002-use-rest-openapi-for-product-clients.md)
- 运行手册：[部署开发预览](../runbooks/deploy-web-preview-to-experimental-nas.md)
- 运行手册：[本地控制台](../runbooks/operate-local-kiosk.md)
