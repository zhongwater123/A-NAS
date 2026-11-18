# RK3588 候选系统首次引导的扩容与 USB 单元失败

状态：resolved
更新时间：2026-11-18

## 症状与影响

Debian 13 与 `6.12.69-anas1` 已启动，但 `anas-rk3588-grow.service` 和 `anas-rk3588-usb.service` 失败。根文件系统仍约 6 GiB，ADB 无设备，阻塞装包；UART root 控制台正常。

## 最小复现

在本次候选首次启动后，经 UART 执行 `systemctl status anas-rk3588-grow anas-rk3588-usb`，再分别跟踪原脚本。根脚本在设备号比较处退出；USB 日志重复 `timed out while waiting for FUNCTIONFS_BIND`，只有 ep0，UDC 未绑定。

## 观察事实

- CID、p6 PARTUUID、14 GiB 分区长度及 ext4 均正确。`findmnt -n -o MAJ:MIN /` 返回带尾部空格的 `179:6`，sysfs 返回不带空格的 `179:6`；原字符串相等比较失败。
- 先启动 adbd 并重试绑定 `fc000000.usb` 后，出现 `FUNCTIONFS_BIND`，ADB 上线。原脚本却先等 ep1/ep2 再绑定，造成相互等待。
- 本板同时有 `fc000000.usb` 和 `fc400000.usb`。只修端点等待后，要求全机唯一 UDC 的旧门禁又失败；跟踪明确在该循环退出，不能按枚举顺序任选端口。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 根设备身份不同 | 对比 CID、PARTUUID、长度和根挂载设备号 | 排除：仅格式空白不同 |
| USB 描述符未被内核接受 | 查看描述符和绑定事件 | 排除：兼容警告后仍能绑定 |
| 端点与 UDC 的等待顺序错误 | 不先等端点，直接重试绑定 | 确认：ADB 恢复 |
| 全机只有一个 UDC | 列出 sysfs 控制器并跟踪循环 | 排除：有两个，须固定已验证端口 |

## 根因

根脚本把人类可读的列输出当成严格机器值；USB 脚本的握手顺序与该版 adbd 相反，并误设唯一控制器前提。三者都是新引导辅助脚本的问题，不是已证明的板级内核启动失败。

## 修复与回归证据

- 原脚本和单元在板端 `/root/anas-bootstrap-before-v2` 备份；改用 `findmnt --raw`，保留所有身份门禁。
- USB 只复用核对过的本次 gadget，启动 adbd 后重试绑定已实测的 `fc000000.usb`；已有 adbd 或其他已绑定 gadget 时停止。
- 受控重启后 ADB 自动上线；根文件系统保持原分区的 14 GiB，RTC/UTC 正确；无 ADB TCP 监听，真机 Landlock ABI 6。日志在本机 `.cache/rk3588-board-20261117/bootstrap-fixed-reboot-uart.log`、`usb-restart-v3.log`、`installed-board-evidence/preflight.log`。
- 实际修复后的 grow/USB 脚本 SHA-256 分别为 `45aac803e9189890521b144b8585023d8451a53f10d0b648c414bebe1d053fff`、`e1d9757846146cb5cdcb155444cf1d76accb34951ad37aceeefdddb23aaed144`。这是板端增量，封存的原 rootfs 镜像未被覆盖，重新部署该镜像仍须应用修复。

## 后续工作

后续整机交付须把修复纳入新版本镜像并重新封存；不能只凭初版 rootfs 摘要声称包含这些修复。

## 关联

- 运行手册：[首次引导服务修复](../runbooks/build-rk3588-kernel.md#首次引导服务修复)
- 研究：[RK3588 可行性与实测](../research/rk3588-deployment-feasibility-2026-11.md)
- 规格、ADR：不涉及
