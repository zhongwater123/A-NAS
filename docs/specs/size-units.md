# 容量与大小的显示单位

状态：implemented
更新时间：2026-10-13

## 目标

同一个数据卷、同一个文件在任何窗口里读出同一个数字和单位，用户跨页面核对容量时不会误以为容量变了。

## 非目标

- 不改变服务端：API 始终返回字节数，换算只发生在界面。
- 不改变状态栏的网络速率：它按网络链路的惯例使用十进制（[桌面状态栏](host-metrics-status-bar.md)）。
- 不改变状态栏内存仪表的写法（已用/总量共用一个 GiB 单位）。

## 场景

### 跨窗口核对数据卷容量

- Given：数据卷总容量 500,107,862,016 字节，已用 14,929,000,000 字节。
- When：分别查看文件管理目录栏底部和“设置 → 存储”。
- Then：两处都显示已用 13.9 GiB、总容量 466 GiB。

### 文件、照片、快照与传输的大小

- When：文件管理、回收站、相册、影视中心、音乐、Docker、备份中心与传输中心显示大小或传输速度。
- Then：都按 1024 进制换算，单位写作 B、KiB、MiB、GiB、TiB（速度加 `/s`）；数值小于 100 时保留一位小数，否则取整，例如 980 B、3.4 MiB、13.9 GiB、466 GiB。大小未知时显示“—”。

### 硬盘的标称容量

- Given：一块厂商标称 500 GB 的硬盘。
- When：在“设置 → 存储”查看磁盘列表。
- Then：容量显示为 466 GiB，旁边注明“标称 500 GB”；悬停说明厂商按 1000 进制计算，所以 GiB 数字更小。

## 边界与失败

- 界面文案中的固定上限同样使用二进制单位，例如反馈图片“不能超过 10 MiB”、相册上传“照片超过 256 MiB”、文本预览“超过 1 MiB”。
- 数据卷“可用”仍包含写入保留空间（[存储架构](../architecture/storage-and-files.md)），本规格只规定显示单位。

## 验收证据

- `web/src/bytes.test.ts` 覆盖换算、舍入、单位进位与标称容量。
- `web/src/files/FilePanel.test.tsx` 与 `web/src/SettingsPanel.test.tsx` 断言文件管理与设置显示同一单位。

## 关联

- 架构：不涉及
- ADR：不涉及
- 代码：[`web/src/bytes.ts`](../../web/src/bytes.ts)
- 测试：[`web/src/bytes.test.ts`](../../web/src/bytes.test.ts)、[`web/src/files/FilePanel.test.tsx`](../../web/src/files/FilePanel.test.tsx)、[`web/src/SettingsPanel.test.tsx`](../../web/src/SettingsPanel.test.tsx)
- 问题反馈：2026-10-13 Codex 试用“同一数据卷容量单位不一致”
