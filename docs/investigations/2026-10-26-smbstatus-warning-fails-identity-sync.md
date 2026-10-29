# smbstatus 的告警混进 JSON，禁用账号与启动时的身份同步失败

状态：fixed（[PR #261](https://github.com/zhongwater123/A-NAS/pull/261)），待实机验收
更新时间：2026-10-27

## 症状与影响

2026-10-23 与 2026-10-26，实验 NAS 上产品服务每次启动都记录：

```
anas-host-agent: "sync identities failed","error":"a12: decode SMB sessions: invalid character 'W' looking for beginning of value\nshokz-zd: decode SMB sessions: invalid character 'W' looking for beginning of value"
anas-api: "identity synchronization failed","error":"host agent state is unavailable: status 503"
```

`a12` 与 `shokz-zd` 是已禁用、且有 Samba 条目的账号。NAS 运行 Debian 13 的 Samba 4.22.11。

影响：

- **已建立的 SMB 会话没有断开。** 出错时 `smbpasswd -d` 已经成功，凭据已停用，新的 SMB 登录会被拒绝。但随后列出会话失败，所以账号已连着的客户端（例如映射的网络驱动器）在自己断开之前仍能访问。
- **身份注册表没有记下这些账号。** `syncIdentity` 在出错后跳过 `recordIdentity`，`/var/lib/a-nas-host/identity-registry.json` 及其在数据卷上的副本里，这些账号的角色与启用状态是旧的。其他账号照常收敛：一个账号出错不会挡住其余账号。
- **启动同步每次都失败，而且只跑一次。** `cmd/anas-api/main.go` 的 `syncIdentities` 不重试，所以要等下一次重启。
- **网页上禁用账号得到误导性的提示。** `DisableCredential` 在断开会话这一步出错，接口返回 `503 smb_disable_pending`，提示“its SMB credential could not be disabled yet”。实际上凭据已经停用，失败的只是断开会话。再次禁用会得到同样的错误。
- **出现的时间。** 调用 `smbstatus` 的代码来自 `0929266`，Host Agent 单元的 `RestrictAddressFamilies=AF_UNIX` 来自 `e4c727c`，两者都是 2026-10-07 的提交。所以从那时起，在 systemd 单元下禁用有 Samba 条目的账号，每次都会这样失败。

## 最小复现

1. 准备环境：系统测试镜像 `anas-systemd:trixie`（Samba `4.22.11-Debian-4.22.11+dfsg-0+deb13u1`，与 NAS 相同），在以 systemd 为 PID 1 的特权容器中操作。写入与 A-NAS 相同的 `[global]`（`interfaces = lo eth0`、`bind interfaces only = yes`），用 `smbpasswd -a` 建一个账号，并用 `smbclient` 保持一个连着的会话。
2. 以 root 运行：
   - `systemd-run --wait --pipe smbstatus --processes --user=bob --json`：标准错误为空。
   - `systemd-run --wait --pipe -p RestrictAddressFamilies=AF_UNIX smbstatus --processes --user=bob --json`：标准错误为 `WARNING: no network interfaces found`，标准输出仍是完整的 JSON，退出码 0。
3. 在代码中复现：单元测试 `TestSMBSessionsAreFoundPastAWarningOnStandardError` 让假的 `smbstatus` 先在标准错误写一行告警，再在标准输出写 JSON。
   - 修复前：把调用改回读取合并输出的 `Run`，报 `bob: decode SMB sessions: invalid character 'W' looking for beginning of value`，与 NAS 上的日志一致。
   - 修复后：找到会话并断开，身份注册表记下账号已禁用。
4. 在部署形态中复现：[系统测试](../../scripts/system-test.sh)让一个成员保持 SMB 连接，管理员用 `DELETE /api/v1/users/{userId}` 禁用它，再重启产品服务。
   - 修复后：返回 `204`，会话被断开，启动同步成功。
   - 修复前：只把 `internal/hostops/linux` 换回修复前的代码，其余不变，再运行系统测试。三项失败：禁用没有返回 `204`、会话没有断开、启动同步一直不成功。`the disabled member cannot open SMB` 仍然通过：凭据已停用，只是会话还连着。

## 观察事实

- Host Agent 的 `execCommandRunner.Run` 返回 `CombinedOutput()`，标准输出与标准错误混在一起。`disconnectSMBSessions` 对这份输出做 JSON 解码，所以第一个字节就是告警的 `W`。
- 在测试镜像中逐项加上 Host Agent 单元的沙箱属性：
  - 只有 `RestrictAddressFamilies=AF_UNIX` 会让 `smbstatus` 与 `smbcontrol` 打印 `WARNING: no network interfaces found`。Samba 探测网卡要用 `AF_NETLINK` 或 `AF_INET` 套接字，这里被拒绝。
  - 两者的退出码仍为 0。`smbcontrol smbd ping` 照常收到 `PONG`，因为它们与 smbd 之间通过 `AF_UNIX` 通信。
  - `ProtectSystem=strict` 加上单元的 `ReadWritePaths`，不会让 `smbstatus` 写标准错误。
- `testparm -s` 无论在不在沙箱中，都在标准错误打印 `Weak crypto is allowed by GnuTLS (e.g. NTLM as a compatibility fallback)`。这是 testparm 自己的提示，不是报错里的 `W`。Host Agent 只在 testparm 失败时展示它的输出。
- 同样解析命令输出的还有：
  - `getent`：解析第一行。混入告警会被当成账号冲突。
  - `getfacl`（[`acl.go`](../../internal/hostops/linux/acl.go)、[`photos.go`](../../internal/hostops/linux/photos.go)）：与期望的 ACL 比较。混入告警时比较总是不等，每次都会重设属主与 ACL。
  - `blkid`（[`volume.go`](../../internal/hostops/linux/volume.go) 两处）：读取文件系统 UUID。混入告警时会得到无效的 UUID。

  这些没有观察到出错，但与 `smbstatus` 的风险相同。
- root 集成测试 `TestRootIdentityLifecycle` 用真实的 `smbstatus` 禁用一个连着 SMB 的账号，但它不在 systemd 单元的沙箱里运行，`smbstatus` 不打印告警，所以测试通过。单元测试里的假命令只返回 JSON。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| `W` 是 testparm 的 `Weak crypto is allowed by GnuTLS` | 在测试镜像中分别运行 `testparm` 与 `smbstatus` | 不成立：`smbstatus` 不打印这行 |
| Host Agent 的沙箱让 `smbstatus` 在标准错误打印告警 | 用 `systemd-run -p` 逐项加上单元的沙箱属性 | 成立：`RestrictAddressFamilies=AF_UNIX` 时打印 `WARNING: no network interfaces found` |
| 告警混入输出让 JSON 解码失败 | 单元测试中的假 `smbstatus` 先写告警，旧代码读合并输出 | 成立：`invalid character 'W'`，与 NAS 相同 |

## 根因

Host Agent 对所有命令都读取合并的输出，`smbstatus` 的 JSON 也从这份输出中解码。Host Agent 单元的 `RestrictAddressFamilies=AF_UNIX` 让 Samba 探测不到网卡，于是每个 Samba 命令都在标准错误打印一行告警，排在 JSON 之前，解码因此失败。

测试没有发现它，原因有两个：假命令从不写标准错误；root 集成测试不在单元的沙箱中运行。

## 修复与回归证据

- **分开两种输出**（[`executor.go`](../../internal/hostops/linux/executor.go)）：`CommandRunner` 增加 `Output`。
  - `Output` 只返回标准输出；命令失败时，错误信息带上标准错误。
  - `smbstatus`、`getent`、`getfacl` 与 `blkid` 改用 `Output`。
  - 输出只用来解释失败的命令仍用 `Run`，读取合并输出。
- **启动同步重试**（[`main.go`](../../cmd/anas-api/main.go)）：失败后从 2 秒起加倍间隔，最长 15 分钟一次，直到成功或服务停止。成功时记录 `identity synchronization succeeded` 与第几次尝试。
  - 同时重启两个服务时，产品服务可能在 Host Agent 设好套接字权限之前连接，得到 `permission denied`。这个竞争由另一项任务处理；重试也能覆盖它。
- **不放宽沙箱。** 这行告警无害：`smbstatus` 与 `smbcontrol` 经 `AF_UNIX` 与 smbd 通信，结果正确。
- **单元测试：**
  - `TestSMBSessionsAreFoundPastAWarningOnStandardError`：在禁用与同步两条路径上，假 `smbstatus` 先写告警再写 JSON。旧代码上以 NAS 的报错失败。
  - `TestOutputKeepsStandardErrorForFailures`：用 `sh` 验证真实的 runner：`Output` 不含标准错误，`Run` 两者都有；失败时错误带退出码与标准错误。
  - `TestIdentitySynchronizationRetriesUntilItSucceeds`：失败后重试直到成功，服务停止时退出。
- **系统测试**（[`system-test.sh`](../../scripts/system-test.sh)）新增检查：
  - `a member stays connected over SMB`；
  - `the administrator disables the member`（`204`）；
  - `disabling ends the member's SMB session`；
  - `the disabled member cannot open SMB`；
  - `the Product Service's start synchronizes identities with a disabled SMB account`。

  2026-10-26 在开发机运行：修复后 295 项全部通过；`internal/hostops/linux` 换回修复前的代码时，上面第 2、3、5 项失败。

## 后续工作

- **部署后在实验 NAS 复核：**
  - 产品服务启动后有 `identity synchronization succeeded`，Host Agent 没有 `sync identities failed`；
  - `identity-registry.json` 记下 `a12` 与 `shokz-zd` 已禁用；
  - root 运行 `smbstatus --processes --user=a12 --json`，应没有会话。
- **可选：** 在 NAS 上以 root 运行 `systemd-run --wait --pipe -p RestrictAddressFamilies=AF_UNIX smbstatus --processes --json`，确认标准错误就是这行告警。
- **未处理：** 凭据已停用、只有断开会话失败时，接口仍返回 `smb_disable_pending` 与“凭据还没停用”的提示。修复后这一步不会再因为告警失败，但 `smbcontrol` 自身出错时，提示仍不准确。

## 关联

- 规格：[统一身份与文件授权](../specs/unified-identity-and-file-acl.md)
- ADR：[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)
- 调查：[Host Agent 在 systemd 沙箱下丢失 CAP_SETUID](2026-10-08-host-agent-loses-setuid-under-systemd.md)（同一单元的 `RestrictAddressFamilies`）
- 代码：[`identity.go`](../../internal/hostops/linux/identity.go)、[`executor.go`](../../internal/hostops/linux/executor.go)、[`identity_test.go`](../../internal/hostops/linux/identity_test.go)、[`runner_internal_test.go`](../../internal/hostops/linux/runner_internal_test.go)、[`main.go`](../../cmd/anas-api/main.go)、[`anas-host-agent.service`](../../deploy/systemd/system/anas-host-agent.service)
- 运行手册：不涉及
