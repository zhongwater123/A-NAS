# 首次管理员创建在 Samba 系统账号边界失败

状态：fix ready；Experimental NAS verification pending
更新时间：2026-10-07

## 症状与影响

`v1.0.1-rc.2` 系统服务启动后，本地控制台创建首个管理员返回 `request could not be completed`。管理员无法激活，因此存储计划、文件和 SMB 闭环均无法继续。同期暴露的一次性初始化码体验问题已由产品规格改为只输入首个管理员账号和密码，但它不是本次 `500` 的技术根因。

## 最小复现

1. 在 Experimental NAS 安装 rc.2 root Host Agent 与非特权产品服务，保持 Host Agent unit 的 `ProtectSystem=strict` 和仅账号文件级的 `ReadWritePaths`。
2. 在本地控制台提交合法管理员账号和至少 12 字符的密码。
3. API 返回 `internal_error`；Host Agent 日志稳定出现 `create locked Samba account: exit status 1`，管理员保持 `error`，设备仍需启用。

## 观察事实

- root `anas-api.service`、`anas-host-agent.service` 与 `smbd.service` 均为 active；用户级 Fake 服务已停用。
- setup code 校验、账号格式和密码长度均已通过，失败位于 Host Agent 的 `useradd` 调用。
- rc.2 unit 仅放行 `/etc/passwd`、`shadow`、`group`、`gshadow` 等文件；shadow-utils 同时需要在父目录创建瞬时锁和备份文件。
- 账号服务已支持同一用户名从 `pending/error` 状态重试，无需清除数据库。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 用户输入或 setup code 不合法 | 对照 HTTP 阶段与 Host Agent 调用日志 | rejected |
| Samba 服务未安装或未启动 | 检查 `smbd.service` 状态 | rejected |
| Host Agent UDS 不可用 | 确认请求到达 `SetCredential` 并执行 `useradd` | rejected |
| systemd 沙箱允许改账号文件却禁止创建相邻锁文件 | 对照 `useradd` 失败位置与 unit `ReadWritePaths` | confirmed |

## 根因

Host Agent 是负责 Linux/Samba 账号与持久 mount unit 的类型化 root 信任边界，但 rc.2 又试图用单文件 `ReadWritePaths` 限制 `/etc`。shadow-utils 的一致性协议不只修改四个数据库文件，还在 `/etc` 中创建锁与备份文件；父目录仍为只读时 `useradd` 无法完成。产品服务随后把 Host Agent 的 `503` 统一映射成无诊断价值的 `internal_error`。

## 修复与回归证据

- 修复：[`anas-host-agent.service`](../../deploy/systemd/system/anas-host-agent.service)允许该类型化 root 边界写 `/etc`；产品服务仍非特权，UDS 权限和高层请求校验不变。
- 诊断：[`executor.go`](../../internal/hostops/linux/executor.go)把不含密码的 shadow-utils stderr 纳入错误链。
- 回归：`make ops-check` 固定检查真实 unit 权限；[`executor_test.go`](../../internal/hostops/linux/executor_test.go)覆盖 `useradd` 失败详情且断言密码不会进入错误。
- 实机：等待修复 RC 部署后用同一 `admin` 账号重试，并验证用户状态变为 `active`、Samba 凭据可登录。

## 后续工作

- 改进 Host Agent 错误协议，让产品界面区分凭据配置失败与普通内部错误，同时不暴露宿主机细节。
- v1.0.1 之后评估短生命周期账号 helper；当前 Host Agent 已可写 systemd unit，缩小 `/etc` 路径并不构成独立权限边界。

## 关联

- 规格：[基础存储与共享](../specs/basic-storage-and-sharing.md)
- ADR：[基础存储特权边界](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- 运行手册：[配置并验收 v1.0.1 实验数据卷](../runbooks/provision-v1.0.1-experimental-storage.md)
