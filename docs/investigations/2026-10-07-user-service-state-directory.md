# RC 激活与回滚后用户 API 无法启动

状态：resolved
更新时间：2026-10-07

## 症状与影响

首次上传 `v1.0.1-rc.1` 后，远端激活脚本的 `/healthz` 与初始化状态检查失败并切回 `v1.0.0`。回滚链接正确，但用户级 `anas-api.service` 触发启动频率限制而保持 failed，本地 Kiosk 暂时不可用；Host Agent 和磁盘内容未受影响。

## 最小复现

1. 保持用户 unit 的 `ProtectHome=read-only`，且不声明产品状态目录的写权限。
2. 用发布脚本激活首次需要 SQLite 状态目录的 v1.0.1 二进制。
3. `systemctl --user is-active anas-api.service` 返回 failed，日志稳定出现 `mkdir /home/anas-dev/.config/a-nas/state: read-only file system`，健康检查失败。

## 观察事实

- 远端 `current` 已由回滚恢复到 `4e98e86b77c5`，`ANAS_HOSTSTATE_MODE=agent` 也恢复正确。
- 用户 Host Agent 保持 active；`127.0.0.1:8080` 没有其他监听者，因此不是依赖或端口冲突。
- 清除 `start-limit-hit` 并重启回滚后的旧 API 后，服务恢复 active，`/healthz` 返回 `{"status":"ok"}`。
- v1.0.1 的产品服务首次启动会创建 SQLite/WAL 数据库，而 v1.0.0 不需要该目录，所以原 unit 的权限缺口此前未暴露。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 回滚链接或模式未恢复 | 检查 `current` 与允许公开的模式枚举 | rejected |
| 8080 被残留进程占用 | 检查监听端口 | rejected |
| 用户 Host Agent 失败连带 API | 分别读取两个 unit 状态 | rejected |
| home 只读且未放行新状态目录 | 对照错误日志与用户 unit 沙箱 | confirmed |

## 根因

v1.0.1 把控制面状态持久化到用户配置目录下的新 `state` 子目录，但用户级过渡 unit 延续 v1.0.0 的 `ProtectHome=read-only`，没有声明该目录为可写。激活脚本也没有提前创建和显式配置状态目录。连续重启最终触发 systemd 启动频率限制，使回滚后的旧二进制没有获得新的启动机会。

## 修复与回归证据

- 修复：[`anas-api.service`](../../deploy/systemd/user/anas-api.service)只放行 `%h/.config/a-nas/state`；[激活脚本](../../scripts/remote-activate-release.sh)以 `0700` 创建目录并显式设置 `ANAS_STATE_DIR`。
- 回归：`make ops-check` 固定检查目录创建、unit 白名单、ShellCheck 和 Bash 语法；远端原始复现由后续 RC 激活的相同 `/healthz` 门禁验证。
- 恢复：执行 `systemctl --user reset-failed` 并重启回滚后的 API，`active` 与 `/healthz` 均恢复。

## 后续工作

- `v1.0.1-rc.1` 保持指向含缺陷的不可变提交；修复使用新的 `v1.0.1-rc.2` 标签。
- root 管理的正式系统 unit 继续使用 `/var/lib/a-nas` 的 `StateDirectory=`，不复用用户 home。

## 关联

- 规格：[基础存储与共享](../specs/basic-storage-and-sharing.md)
- ADR：[基础存储特权边界](../adr/0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- 运行手册：[配置并验收 v1.0.1 实验数据卷](../runbooks/provision-v1.0.1-experimental-storage.md)
