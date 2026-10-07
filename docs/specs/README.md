# 功能规格

规格描述用户或系统可观察的行为，是实现和测试的共同依据。一个功能在存在多个状态、权限边界、失败路径或跨模块契约时创建规格。

## 索引

- [只读宿主机状态](read-only-host-state.md)（implemented）
- [Debian 只读宿主机状态](read-only-linux-host-state.md)（implemented）
- [Web 桌面宿主机状态](web-desktop-host-state.md)（implemented）
- [基础存储与共享](basic-storage-and-sharing.md)（implemented locally；hardware acceptance pending）
- [相册与本地智能检索](photo-library.md)（draft，核心开发基线已冻结）
- [Web 桌面终端](web-terminal.md)（implemented，opt-in）
- [桌面状态栏与实时资源指标](host-metrics-status-bar.md)（implemented）
- [本地控制台屏幕保护程序](local-console-screensaver.md)（implemented locally；hardware acceptance pending）

## 使用方式

从 [`_TEMPLATE.md`](_TEMPLATE.md) 创建 `short-feature-name.md`，明确目标、非目标、场景和验收证据。规格实现后仍保留；行为被替代时标明状态并链接新规格。
