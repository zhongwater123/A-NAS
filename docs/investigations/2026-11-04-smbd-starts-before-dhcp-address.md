# 重启后 smbd 早于 DHCP 地址启动，只监听回环

状态：fixed，待实机验收
更新时间：2026-11-05

## 症状与影响

Experimental NAS 每次重启后，smbd 只监听 `127.0.0.1:445` 与 `[::1]:445`，局域网上的电脑连不上 SMB；root 执行 `systemctl restart smbd` 后恢复。v1.0.1 验收第 6 步（正常重启后空间和 SMB 自动恢复）因此不能通过。

## 最小复现

1. Experimental NAS：Debian 13，`/etc/network/interfaces` 中局域网接口为 `allow-hotplug enp2s0` 与 `iface enp2s0 inet dhcp`，由 ifupdown 0.8.44 调用 dhcpcd 10.1.0 获取地址；`smb.conf` 为 `interfaces = lo enp2s0` 与 `bind interfaces only = yes`。
2. 正常重启。
3. 实际：`ss -ltn` 中 445 只有回环地址。期望：同时监听 `172.18.45.48:445`。

## 观察事实

2026-11-04 装上 `70853075ca40`，其中 `a-nas-host` 的 `postinst` 启用了 `ifupdown-wait-online.service`（[ADR 0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)）。之后正常重启一次，root 执行只读检查得到：

| 时间 | 事件 | 来源 |
|---|---|---|
| 19:50:19 | `networking.service`、`ifup@enp2s0.service` 进入 active；dhcpcd 启动 | `systemctl show`、`ps -o lstart` |
| 19:50:20 | `ifupdown-wait-online.service` 结束（运行约 2 秒），`network-online.target` 达成 | `systemd-analyze critical-chain smbd.service` |
| 19:50:20 | smbd 启动，只绑定回环 | `ExecMainStartTimestamp`、`ss -ltn` |
| 19:50:24 | dhcpcd 开始对 `172.18.45.48` 做 ARP 探测 | dhcpcd 的 `[BPF ARP]` 进程启动时间 |
| 约 19:50:30 | `enp2s0` 得到 `172.18.45.48/24`（租期 86400 秒，19:52:25 时剩 86285 秒） | `ip -4 addr` 的 `valid_lft` |

- smbd 的单元已经 `Wants=` 并排在 `network-online.target` 之后，排序本身生效：smbd 确实等到了网络等待结束。
- `ifupdown-wait-online.service` 已启用并成功结束，`journalctl -b -p err` 中 smbd 与 A-NAS 单元没有错误，smbd `NRestarts=0`。
- `/usr/lib/ifupdown/wait-online.sh` 的默认方法是 `ifup`：没有 `auto` 接口时，等到 `ifquery --state` 对 `allow-hotplug` 接口返回非空就结束。它不看接口有没有地址。
- `ifup@enp2s0.service` 没有设置 `Type=`，按 `simple` 在启动 `ifup` 时就是 active；dhcpcd 的租约在约 10 秒后才到。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 没有服务实现 `network-online.target`，smbd 不等网络 | 启用 `ifupdown-wait-online.service` 后重启 | 否定：等待已启用并在 smbd 之前结束，smbd 仍只监听回环 |
| 网络等待在接口拿到地址之前就结束 | 对比等待结束时间与地址出现时间 | 证实：等待在 19:50:20 结束，地址约 19:50:30 出现 |

## 根因

`ifupdown-wait-online` 的默认方法只等 ifupdown 记下接口状态，不等地址。dhcpcd 拿到租约之前，ifupdown 就记下了 `enp2s0`，所以 `network-online.target` 比地址早约 10 秒达成。smbd 按 `bind interfaces only = yes` 启动时，`enp2s0` 还没有 IPv4 地址，只能绑定回环。之后地址出现，smbd 也不重新绑定。

ADR 0031 写的是“它对 `allow-hotplug` 接口也等到 `ifup` 完成”；在这台机器上，这不足以让 smbd 等到地址。

## 修复与回归证据

- 修复（用户 2026-11-05 选定）：安装器按它写入 `smb.conf` 的接口，为 smbd 写入 `/etc/systemd/system/smbd.service.d/a-nas-lan-address.conf`。smbd 启动前最多等 20 秒，等这个接口有 IPv4 地址；等不到时这次启动失败，systemd 5 秒后再试，直到有地址。`a-nas-host` 不再启用 `ifupdown-wait-online.service`，也不再有 `postinst`。做法与没有选的方案见 [ADR 0031](../adr/0031-declare-host-dependencies-in-debian-packages.md) 的决定 2 与“未选择”。
- 验证前的实验（2026-11-05，系统测试镜像，systemd 257.13、Samba 4.22.11，与 NAS 相同）：
  - 在没有地址的 veth 上，smbd 停在 `activating`；等待超时后 systemd 按 `Restart=on-failure` 再试；加上地址后约 1 秒启动，并监听这个地址的 445；
  - 一个排在 smbd 之后、`Wants=` 它的单元（与 Host Agent 相同）在第一次失败后启动，不等以后的重试；
  - smbd 没有运行时，`smbcontrol all reload-config` 退出码为 0。
- 测试：[`scripts/system-test.sh`](../../scripts/system-test.sh) 检查安装器写入的等待与重试，并在一个暂时没有地址的接口上走一遍上面的过程。部署后要在 Experimental NAS 上正常重启一次，确认 `172.18.45.48:445` 在监听。

## 后续工作

- 部署修复之前，每次重启后由 root 执行 `systemctl restart smbd`。
- Experimental NAS 上由 `0~70853075ca40` 启用的 `ifupdown-wait-online.service`，在部署修复时停用，与新装的宿主机一致。
- DHCP 续租换了地址时 smbd 仍不会跟随；需要时再加 ADR 0031 中列出的钩子。

## 关联

- 规格：不涉及
- ADR：[0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)
- 运行手册：[实验存储配置与验收](../runbooks/provision-v1.0.1-experimental-storage.md#功能验收)（第 6 步）
- 实现：[`scripts/install-v1.0.1-system-services.sh`](../../scripts/install-v1.0.1-system-services.sh)（写入 smbd 的附加配置）；没有修好问题的网络等待在 [PR #385](https://github.com/zhongwater123/A-NAS/pull/385) 中
- 测试：[`scripts/system-test.sh`](../../scripts/system-test.sh)
