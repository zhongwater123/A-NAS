# 空白磁盘计划导致本地桌面蓝屏

状态：resolved；`v1.0.1-rc.4` 在实机执行空白磁盘计划并创建了 `/dev/sda1` Btrfs 数据卷

## 症状与影响

Experimental NAS 在 `v1.0.1-rc.3` 中成功创建管理员后，用户从“存储初始化”为没有现有签名的 SATA 实验盘生成格式化计划，桌面随即只剩蓝色壁纸和鼠标，无法继续操作，也没有进度或错误反馈。

这是前端渲染故障，不是磁盘格式化或 Kiosk 进程崩溃。故障发生后 API、Host Agent、Samba、Cage 和 Chromium 均保持运行；实验盘仍没有分区、文件系统、UUID 或挂载。

## 证据与排除

- 同一启动周期的 API/Host Agent journal 没有创建数据卷请求或执行错误。
- 内核 journal 没有新的分区、Btrfs、I/O 或设备错误；`lsblk` 仍只显示裸 `/dev/sda`。
- Chromium 与 Cage 主进程持续运行，排除显示会话退出。
- 空盘的 `disk.Filesystems` 是 nil slice；Go JSON 将其编码为 `"signatures":null`。
- [`StoragePanel`](../../web/src/App.tsx)直接读取 `plan.signatures.length`，React 因 `null.length` 抛出未捕获异常并卸载桌面内容树。Vitest 完整复现了 `TypeError: Cannot read properties of null (reading 'length')` 和空 DOM。

## 根因

存储计划接口把“没有签名”作为 `null` 返回，但 OpenAPI/TypeScript 契约和 UI 都把它当作数组。前端同时缺少组件级错误边界，所以一个面板的渲染错误扩大成整个桌面不可用。

## 修复与回归

- 存储服务和计划克隆始终输出非 nil 的 `signatures` 与 `actions` 数组。
- 前端仍对旧持久化计划中的 `null` 做兼容，显示“未检测到文件系统签名”。
- 存储计划、确认和执行增加明确的忙碌状态；执行期间显示 `running` 和“请勿关闭设备”。
- 每个桌面窗口增加错误边界，面板异常时保留桌面并提供重新载入入口。
- 后端回归断言空盘计划 JSON 包含 `"signatures":[]`；React 回归把 `signatures/actions` 故意设为 `null` 并验证桌面仍可操作。

`go test ./internal/storage`、`npm test -- --run src/App.test.tsx` 与 `make check VERSION=v1.0.1-rc.4` 已通过。实机只需重新生成计划验证页面，不得把此前蓝屏描述为已经执行过格式化。

## 关联

- [基础存储与共享规格](../specs/basic-storage-and-sharing.md)
- [实机配置与验收手册](../runbooks/provision-v1.0.1-experimental-storage.md)
- [`internal/storage/service_test.go`](../../internal/storage/service_test.go)
- [`web/src/App.test.tsx`](../../web/src/App.test.tsx)
