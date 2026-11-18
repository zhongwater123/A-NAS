# RK3588 未联网重启后 Host Agent 缺运行目录

状态：resolved
更新时间：2026-11-18

## 症状与影响

五寸屏开机控制台已正常出现，API 健康接口返回 200，NVMe 按 UUID 挂载、管理员状态保留，但 Host Agent 在自动重启中，相册以外部分服务能力不可用。不能只凭画面和 HTTP 200 宣告重启验收通过。

## 最小复现

1. 当前 ATK-DLRK3588 Debian 13，安装 `5a3ceb79` 加小屏修复 `.rk5.1`；`end0` 无载波，SMB 等待地址。
2. 正常重启，检查 `journalctl -b -u anas-host-agent` 和 `a-nas-check`。
3. Host Agent 每次在创建 mount namespace 时因 `/run/samba` 不存在失败；预期为 NAS 本地能力在无网线时仍能运行。

## 观察事实

- 第一次新 boot ID 为 `BOARD_UUID_REDACTED_01`；日志为 `Failed to set up mount namespacing: /run/samba: No such file or directory`。
- Samba 的板端地址门禁在 ExecStartPre 等待 `end0` 的 IPv4；SMB 本体未启动，未创建 `/run/samba`。厂商根系统无 Samba tmpfiles 规则。
- Host Agent 的 `ReadWritePaths` 要求 `/run/samba` 存在，其 namespace 在二进制执行前失败；不是管理员数据或 NVMe 数据损坏。
- 首轮健康检查保留四项失败：Host Agent、SMB、分享数据库未打开和文件工作进程身份探针。日志在板端 `/userdata/anas-display-rk5-20261118/health-first-boot.*`。

## 根因

运行目录的创建隐含依赖成功启动的 SMB；网络地址门禁提前失败后，这个前提在冷启动不成立。软件安装时存在的 `/run` 内容无法跨重启保留。

## 修复与回归证据

本板 `/etc/tmpfiles.d/anas-rk3588-runtime.conf` 使用标准规则 `d /run/samba 0755 root root -`，不修改服务沙箱或 SMB 地址门禁。创建后重启 Host Agent 进入 active/running。

第二次正常重启的 boot ID 为 `BOARD_UUID_REDACTED_02`：Host Agent、API、相册、分享、家庭和 kiosk 均 active，Host Agent 的 `NRestarts=0`。健康检查仅保留无网线的 `unit:smbd.service`；管理员初始化状态不变，数据卷按原 UUID 自动挂载，DSI-1 单屏 scale=3、Chromium 自动启动，两个图形设备节点的活动会话 ACL 自动恢复。原 namespace 失败与连带的分享/身份探针失败不再出现。

第二轮采集截图的 ffmpeg 在已输出 PNG 后触及 10 秒采集时限，原退出 124 保留；另用 ffprobe 校验完整 1080×1920 PNG 并目视核验，独立服务/挂载/健康结果断言退出 0，标记 `FIVEINCH_REBOOT_VERIFIED`。不把截图命令超时改写为首次脚本全绿。本机证据在 `.cache/rk3588-fiveinch-20261118/`，包括两轮 UART、`after-reboot-verification.log`、`second-boot-verification.log`、`reboot-final-result.log` 和 `boot-evidence/`。

## 后续工作

- 把创建运行目录的责任纳入正式产品包或根镜像并补无网线启动测试；当前修复仅为本板配置，不声称其他设备已修复。
- 厂商 `bootanim.service` 与 `rkaiq_3A.service` 在启动中另有失败，本调查没有停用它们，也不把它们冒充已修。

## 关联

- [Host Agent 单元](../../deploy/systemd/system/anas-host-agent.service)
- [五寸屏部署、回滚与验收](../runbooks/adapt-rk3588-five-inch-display.md)
- [RK3588 部署状态](../research/rk3588-deployment-feasibility-2026-11.md)
