# Host Agent 在 systemd 沙箱下丢失 CAP_SETUID

状态：fixed in code；Experimental NAS verification pending
更新时间：2026-10-08

## 症状与影响

Experimental NAS 部署 `946638851c7c` 后，Web 文件管理的个人空间、Shared、回收站与快照全部返回 `request could not be completed`（[issue #38](https://github.com/zhongwater123/A-NAS/issues/38)）。Web 终端同样无法启动。日志：

```text
anas-host-agent: file worker unavailable username=admin error="start file worker: fork/exec /opt/a-nas/releases/946638851c7c/anas-host-agent: operation not permitted"
```

失败发生在 File Broker 派生用户 Worker 时，尚未进入 ACL 判定。SMB 不受影响，因为 smbd 自己切换到登录用户。

## 最小复现

1. 在 Debian 13（systemd `257.13-1~deb13u1`）中，以 systemd 为 PID 1 运行 `scripts/build-system-test-image.sh` 生成的镜像。
2. 用 transient unit 运行同一探针：先打印 `CapEff`，再 `setpriv --reuid=20100 --regid=20100 --groups=20000,20001 id -u`。
3. 结果：

| unit 设置 | `CapEff` | 切换到 UID 20100 |
|---|---|---|
| `User=root` `Group=root` `NoNewPrivileges=yes` `RestrictAddressFamilies=AF_UNIX`（与 NAS 相同） | `000001ffffffff7f` | `setresuid failed: Operation not permitted` |
| 同上但不写 `User=`/`Group=` | `000001ffffffffff` | 成功 |
| 不写 `User=`/`Group=`，加上单元其余文件系统沙箱 | `000001ffffffffff` | 成功 |
| `User=root` `Group=root`，只有 `NoNewPrivileges` 或只有 `RestrictAddressFamilies` | `000001ffffffffff` | 成功 |

## 观察事实

- NAS 上 Host Agent 进程：`CapPrm`/`CapEff` 为 `000001ffffffff7f`，恰好缺少 bit 7（`CAP_SETUID`）；`CapBnd` 完整；`NoNewPrivs: 1`、`Seccomp: 2`。其余服务（`anas-api`、`anas-photos`、`anas-container-agent`）本就以非 root 运行，不受影响。
- 需要切换身份的代码只有两处：File Broker 的文件 Worker 与 Web 终端（`internal/filebroker/server.go`、`terminal.go`），均使用 `syscall.Credential`。
- 既有测试都由测试进程直接以 root 启动 Host Agent 或 File Broker，从未在正式 unit 的沙箱下运行。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 文件系统沙箱（`ProtectSystem` 等）阻止 exec | transient unit 逐项组合 | rejected |
| 单独的 `NoNewPrivileges` 阻止降权 | 只设 `NoNewPrivileges` | rejected |
| systemd 在显式 `User=` 与 seccomp 同时存在时丢弃 `CAP_SETUID` | 读 v257.13 源码并做上表对照 | confirmed |

## 根因

systemd v257 的 `exec-invoke.c`：服务带 seccomp 过滤（`RestrictAddressFamilies` 也会安装）且 `uid_is_valid(uid)`（即写了 `User=`）时设置 `keep_seccomp_privileges`；切换用户后，只要 `CAP_SETUID` 不在 `AmbientCapabilities` 中就主动丢弃它，即使目标用户是 root。`NoNewPrivileges` 又阻止 exec 时重新获得。Host Agent 的 unit 写了冗余的 `User=root`/`Group=root`，因此保留 UID 0 却失去 `CAP_SETUID`。不写 `User=` 时 uid 无效，这条路径整体跳过。

该 unit 早于 File Broker；ADR 0008 引入“Web 以登录用户身份运行”后，部署契约没有被任何在 systemd 下运行的测试覆盖。

## 修复与回归证据

- 修复：[`anas-host-agent.service`](../../deploy/systemd/system/anas-host-agent.service) 删除 `User=root`/`Group=root`，保留 `NoNewPrivileges`、`RestrictAddressFamilies=AF_UNIX` 与全部文件系统沙箱；`make ops-check` 禁止该 unit 再写 `User=`/`Group=`。不采用 `AmbientCapabilities=CAP_SETUID`，也不放宽沙箱或恢复 `a-nas` 的空间 ACL。
- 启动自检：File Broker 以与 Worker 完全相同的启动参数、以 `nobody` 身份运行探针（[`probe.go`](../../internal/filebroker/probe.go)），日志为 `file broker identity switch verified`，失败时记录 `file broker cannot switch to user identities` 而不是在每个请求上失败。
- 安装器：重启服务后核对 Host Agent 的 `CapEff` 含 `CAP_SETUID`/`CAP_SETGID` 且启动自检通过，否则以退出码 5 结束并要求回滚。
- 单元测试：无此能力时探针返回 `EPERM`（`TestIdentitySwitchProbeFailsWithoutTheCapabilities`）；root 集成测试要求探针成功。
- 系统测试：[`scripts/system-test.sh`](../../scripts/system-test.sh) 在 Debian 13 systemd 容器中用真实安装器、unit 与二进制安装发布，按用户检查 Web、SMB、相册与服务身份的权限矩阵，共 40 项。
  - 使用旧 unit 时安装器报告 `CapEff=000001ffffffff7f; file broker cannot switch to user identities` 并以退出码 5 结束；修复后 40 项全部通过。
  - CI 作业 `System test (Debian 13, systemd)` 在每次推送时运行同一流程。

## 系统测试随后发现的问题

系统测试修复 #38 后立刻暴露了被它掩盖的两个部署缺陷：

- **Web 写操作全部返回 423 `volume_unavailable`。** 产品服务以 `ProtectSystem=strict` 运行，它看到的数据卷挂载带 `ST_RDONLY`，而 Host Agent 的视图是可写的（transient unit 对照：根命名空间 `rw`、产品服务沙箱 `ST_RDONLY`、Host Agent 沙箱 `rw`）。产品服务在写入前用自己的视图检查可写性，于是拒绝所有上传、改名和删除；ADR 0008 之后真正写入的是 File Broker Worker。修复：产品服务的卷检查只验证卷身份与存在（与应用文件夹的检查合并为 `sandboxedVolumeGuard`），Worker 遇到 `EROFS` 时报告卷不可用。
- **相册服务启动后最长 30 秒不可用。** 安装或开机时相册服务通常先于 Host Agent 授权存储区，检查失败后每 30 秒才重试。重试间隔改为 2 秒（只是几次 `stat`）。

## 后续工作

- 在 Experimental NAS 部署修复并完成 issue #38 的验收标准。
- 把系统测试接入 CI，见 [本地环境](../development/LOCAL_ENVIRONMENT.md#系统测试)。

## 关联

- 规格：[统一身份与文件授权](../specs/unified-identity-and-file-acl.md)
- ADR：[0008 统一 Linux 身份](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)
- 运行手册：[实机配置与验收](../runbooks/provision-v1.0.1-experimental-storage.md)、[启用相册服务](../runbooks/enable-photo-service.md)
- 上游代码：[systemd v257.13 `exec-invoke.c`](https://github.com/systemd/systemd/blob/v257.13/src/core/exec-invoke.c)
