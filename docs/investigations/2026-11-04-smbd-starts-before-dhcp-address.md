# 重启后 smbd 早于 DHCP 地址启动，只监听回环

状态：resolved；2026-11-10 部署 `61744597719c` 后正常重启，smbd 未经手动重启即监听 `172.18.45.48:445`
更新时间：2026-11-10

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
- 测试：[`scripts/system-test.sh`](../../scripts/system-test.sh) 检查安装器写入的等待与重试，并在一个暂时没有地址的接口上走一遍上面的过程。
- 实机（2026-11-10，`61744597719c`，NAS 上的 `/var/tmp/a-nas-after-reboot-61744597719c.log`）：
  - 16:02:19 开机，16:02:23 网络服务就绪，16:02:36 smbd 启动，即等了约 13 秒地址，第一次等待内就拿到了；
  - smbd 监听 `127.0.0.1`、`[::1]` 与 `172.18.45.48` 的 445，`NRestarts=0`，开机以来 smbd 与 A-NAS 的单元没有错误日志；
  - 代价：开机的用户空间阶段从 4.3 秒变为 15.1 秒，因为排在 smbd 之后的 Host Agent 也随之晚启动。
  - 2026-11-05 起，CI 中“smbd listens on the SMB interface's address”时好时坏（`fff0c2b0` 推送后，以及 #397、#400、#403、#407）。
    - #397 认为原因是 smbd 先告诉 systemd 已就绪、再打开监听套接字（Samba 4.22 的 `source3/smbd/server.c`：先 `daemon_ready("smbd")`，后 `open_sockets_smbd()`），把检查改为最多等 10 秒。
    - 之后 #400、#403、#407 等满 10 秒仍然失败，说明这个诊断不完整。
    - #403 在最小容器中复现：创建第一个账号之前仍是 Debian 的默认 `smb.conf`，smbd 监听 `0.0.0.0:445` 与 `[::]:445`。它把整段地址等待与监听检查移到账号创建之后，那时 `SetCredential` 一定已写入配置。真正的原因见下一节：Host Agent 安装后的第一次对账本该写入配置，只是有时失败了。

## 新装后 smbd 监听所有地址（2026-11-06）

- **现象**：CI 失败时，等满 10 秒 smbd 也没有监听 SMB 网卡的地址，可后面的 SMB 读写照常通过。
- **本地探针**（2026-11-06，系统测试镜像，容器限 2 核，三轮结果相同）：
  - smbd 检查段开始时，Host Agent 约 0.4 秒前已写好 A-NAS 的 `smb.conf`；
  - 但 smbd 仍监听 `0.0.0.0:445` 与 `[::]:445`。
- **CI 日志**（2026-11-06，[#410](https://github.com/zhongwater123/A-NAS/pull/410) 第一次运行，失败时收集的 `journal.txt`）：Host Agent 开始监听后约 35 毫秒，第一次对账就失败了：

  ```text
  data-volume reconciliation failed: create A-NAS group: exit status 9: groupadd: group 'a-nas-admins' already exists
  ```

  直到测试后面重启 Host Agent，对账才成功，`smb.conf` 才写入。
- **根因**，两处：
  1. **第一次对账可能失败**：
     - Host Agent 启动时的数据卷对账，与 Product Service 启动时的身份同步，都会确保 `a-nas-users`、`a-nas-admins` 两个组存在：先用 `getent` 查，没有再 `groupadd`。
     - 两者同时进来，都查到“没有”，后执行 `groupadd` 的以退出码 9 失败。失败的是对账时，它就停在写 Samba 配置之前，15 分钟后才重试。
  2. **写了配置，smbd 也不换监听**：
     - smbd 按 Debian 的默认配置启动，容器里和新装的宿主机都一样。默认配置没有 `bind interfaces only`，smbd 监听所有地址。
     - Host Agent 写入 A-NAS 的配置（`interfaces = lo <网卡>`、`bind interfaces only = yes`）后，只让 smbd 重新加载（`smbcontrol all reload-config`）。smbd 只在启动时打开监听套接字，重新加载不改变它监听的地址，所以它一直监听所有地址，直到下次重启。
- **为什么测试时好时坏**：组的竞争有时出现、有时不出现。出现时，测试自己重启 smbd 那一刻还没有 A-NAS 的配置，smbd 又按默认配置监听所有地址。检查只认 SMB 网卡的地址，所以失败；SMB 读写经通配地址照样成功。等多久都没用，因为配置要到 Host Agent 下次对账才写。
- **影响**：新装的设备在 smbd 下次重启之前（通常是第一次重启机器之前），SMB 在所有网卡上都可达，而不只是安装时选定的网卡；碰上组的竞争时，A-NAS 的 Samba 配置还要晚到 15 分钟。Experimental NAS 之后重启过，smbd 已按 A-NAS 的配置启动，不受影响。
- **修复**：
  - 同一个 Host Agent 中，查组与建组由一把锁串行进行，后来的调用者会看到组已存在。
  - Host Agent 写入 Samba 配置时，用 `testparm` 读出新旧配置的 `interfaces`、`bind interfaces only` 与 `smb ports`：
    - 原先没有配置，或者这三项变了，就让 systemd 重启 smbd（`systemctl try-restart --no-block smbd.service`）；否则照旧只重新加载。
    - 重启会断开已有的 SMB 连接，所以只在监听的地址或端口变化时进行：新装，或者换了 SMB 网卡。
    - smbd 没有运行时不去启动它，由 systemd 按单元启动，并照样等网卡的地址。
- **回归证据**：
  - 单元测试：
    - [`groups_test.go`](../../internal/hostops/linux/groups_test.go)：两个调用同时确保固定组存在，都成功。去掉锁时，它复现 CI 的同一错误。
    - [`samba_test.go`](../../internal/hostops/linux/samba_test.go)：第一次写入配置、替换 Debian 的配置、换 SMB 网卡时都重启，配置不变时只重新加载；smbd 重启或重新加载失败时，恢复原来的配置。
  - 系统测试：
    - 安装后先等 Host Agent 第一次对账成功，再检查 smbd 只监听回环与 SMB 网卡的地址、没有通配地址。修复前，这两项在本地因为没有重启而失败，在 CI 上还会因为组的竞争而失败。后面的检查都在这次重启之后，不再与它竞争。
    - 测试中途重启 smbd 之后的检查改用同一个函数，最多等 30 秒，失败时打印 smbd 实际监听的地址。
  - root 集成测试自己启动的 smbd 不归 systemd 管，它的初始配置改为与 Host Agent 写入的监听设置相同，所以只会重新加载。

## 后续工作

- DHCP 续租换了地址时 smbd 仍不会跟随；需要时再加 ADR 0031 中列出的钩子。
- Experimental NAS 上由 `0~70853075ca40` 启用的 `ifupdown-wait-online.service` 已在 2026-11-10 部署时停用，与新装的宿主机一致。

## 关联

- 规格：不涉及
- ADR：[0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)
- 运行手册：[实验存储配置与验收](../runbooks/provision-v1.0.1-experimental-storage.md#功能验收)（第 6 步）
- 实现：
  - [`scripts/install-v1.0.1-system-services.sh`](../../scripts/install-v1.0.1-system-services.sh)（写入 smbd 的附加配置，[PR #390](https://github.com/zhongwater123/A-NAS/pull/390)）；没有修好问题的网络等待在 [PR #385](https://github.com/zhongwater123/A-NAS/pull/385) 中；
  - [`internal/hostops/linux/executor.go`](../../internal/hostops/linux/executor.go) 的 `applySambaConfiguration`（监听设置变化时重启 smbd）；
  - [`internal/hostops/linux/identity.go`](../../internal/hostops/linux/identity.go) 的 `ensureFixedGroups`（查组与建组串行进行）。
- 测试：[`scripts/system-test.sh`](../../scripts/system-test.sh)、[`internal/hostops/linux/groups_test.go`](../../internal/hostops/linux/groups_test.go)、[`internal/hostops/linux/samba_test.go`](../../internal/hostops/linux/samba_test.go)、[`internal/hostops/linux/root_integration_test.go`](../../internal/hostops/linux/root_integration_test.go)
