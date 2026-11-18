# RK3588 GNOME 自动休眠使 USB 网页入口失联

状态：resolved
更新时间：2026-11-18

## 症状与影响

服务和网页正常运行一段时间后，用户访问 localhost 遭拒绝。主机只剩 CH343 串口适配器，ADB 设备和转发消失，串口回车也无响应。用户确认没有断电或拔 USB。

## 最小复现

使用厂商 Debian 13 桌面候选启动，保留 `rockchip` 的 GNOME 电源默认设置：`sleep-inactive-ac-type='suspend'`、`sleep-inactive-ac-timeout=900`。不在本地桌面操作，远程通过 ADB 使用 NAS；观察系统是否进入休眠，再短按电源键唤醒。

## 观察事实

- 2026-11-18 03:14:06 UTC，logind 记录 `The system will suspend now!`，systemd-sleep 执行 suspend，内核记录 `PM: suspend entry (deep)`。
- 03:16:41 UTC，记录 `Power key pressed short`、从 suspend 返回、`PM: suspend exit`。不是通过重启恢复，产品服务仍 active。
- 恢复后原 oneshot USB 单元仍 active，但本次 gadget 的 UDC 为空，adbd 反复等 `FUNCTIONFS_BIND`。重新绑定 `fc000000.usb` 后 ADB 与 HTTP 200 恢复；主机须重建丢失的 ADB 端口转发。
- 厂商真正运行的 USB 管理器是 `usb-gadget.service`；它在本次重新绑定时也尝试使用同一 UDC。此前只检查 `usbdevice.service` 不足以排除冲突。其停止脚本按可执行文件名杀 adbd，有误杀独立调试单元进程的风险。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 用户断电或拔 USB | 用户说明、检查唤醒后是否重启 | 排除本次：用户否认，日志显示 suspend/resume |
| 内核崩溃或服务退出 | UART 唤醒、PM 日志及服务状态 | 排除本次：系统从 suspend 恢复，服务仍在 |
| 桌面自动休眠 | 检查 GNOME 设置、PM 时间和短按唤醒 | 确认休眠且默认策略为 900 秒；关闭策略后补长期观察 |
| 唤醒后 ADB 自动恢复 | 检查 UDC、绑定事件和 HTTP | 原单元失败，监督重绑定后可逆实验恢复 |

## 根因

厂商桌面保留了自动休眠策略，远程使用 NAS 不构成本地桌面输入。系统休眠会断开 USB 调试；恢复时 UDC 未重新绑定，而原 oneshot 只在启动时绑定一次，造成第二层持续失联。

## 修复与回归证据

- 原 GNOME 电源设置与 sleep target 状态备份至板端 `/root/anas-always-on-before-20261118`。关闭交流电/电池的闲置休眠，并 mask 五个 sleep 类 target；logind `CanSuspend` 返回 `no`。关机和重启仍可用。
- 原 USB 脚本和单元备份至 `/root/anas-usb-before-v4`，改为持续监督 adbd、在本次 gadget 的 UDC 为空时重新绑定固定控制器，守护进程退出则重启单元。实际解绑该 gadget 后约 0.3 秒出现 `FUNCTIONFS_BIND/ENABLE`，恢复转发后 HTTP 200。
- 厂商 USB 服务停用方案被自动审批拒绝，理由为可能影响非 ADB USB 功能、超出明确授权；**操作未执行**，当前保留该服务并等待用户选择。未借串口或其他方式绕过拒绝。
- 原始证据在本机 `.cache/rk3588-board-20261117/wake-diagnose.log`、`usb-supervised-restart.log`、`usb-rebind-regression.log`；实际脚本在 `bootstrap-usb-supervised-v4/`。常开配置自 03:20:17 UTC 生效，03:35:48 UTC 已连续观察 15 分 31 秒，无新 suspend 记录，ADB 与 HTTP 200 保持在线；这不等于长时稳定性验收。
- 当前监督脚本与单元 SHA-256 为 `119e5de7ab0277193e81998e293af171293f4959bd813a185e93a47add7fdc4b`、`7318e51f0246d8faca526d8b42e93a5543694902c0f24f8dbc4cbe68c929a7f4`。厂商服务仍保留，未执行被拒绝的停用方案。

## 后续工作

- 长时间连续运行与后续重启时复核常开策略和 USB 恢复。
- 若用户选择保留厂商 USB 管理器，继续验证两者实际冲突边界；若授权停用，严格按运行手册保存和恢复配置。
- 后续镜像纳入常开配置、单一调试入口及回退流程。

## 关联

- 运行手册：[常开与调试连接恢复](../runbooks/build-rk3588-kernel.md#常开与调试连接恢复)
- 调查：[首次引导扩容与 USB 失败](2026-11-18-rk3588-bootstrap-services.md)
- 研究：[RK3588 可行性与实测](../research/rk3588-deployment-feasibility-2026-11.md)
- 规格、ADR：不涉及
