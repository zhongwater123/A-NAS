# Host Agent 无法创建运行时 Socket

状态：resolved
更新时间：2026-10-06

## 症状与影响

首次把 Experimental NAS 从 Fake 切换到 Host Agent 模式时，`anas-host-agent.service` 持续退出，产品接口返回 `503 state_unavailable`。部署健康检查失败并自动恢复到上一份 Fake 配置，没有留下半激活的 Live 环境。

## 最小复现

1. 在 Debian 13 上安装提交 `1b78d2d` 的用户服务单元，其中同时启用 `ProtectSystem=strict`，但没有声明可写运行时目录。
2. 以 `anas-dev` 启动 `anas-host-agent.service`。
3. 服务在创建 `/run/user/1001/a-nas` 时以 `read-only file system` 退出；期望结果是创建权限为 `0700` 的目录和 `0600` 的 UDS。

## 观察事实

- Host Agent 日志为 `mkdir /run/user/1001/a-nas: read-only file system`。
- Product Service 日志为连接 `/run/user/1001/a-nas/host-agent.sock` 时 `no such file or directory`。
- 部署脚本在健康检查失败后恢复 `ANAS_HOSTSTATE_MODE=fake`，API 回到 `active`，Host Agent 回到 `inactive`。
- 问题发生在 systemd 沙箱内；同一用户的普通 SSH 会话可以写入自己的运行时目录。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| Linux Reader 读取命令失败 | 检查 Agent 是否进入 Reader 以及服务日志 | rejected：进程在建立监听器前退出 |
| UDS 路径或用户不一致 | 比较 API、Agent 的 `XDG_RUNTIME_DIR` 和 UID | rejected：两者均为 `/run/user/1001` 与 UID 1001 |
| `ProtectSystem=strict` 未给 UDS 目录可写例外 | 由 systemd 创建 `RuntimeDirectory=a-nas` 后重新部署 | confirmed |

## 根因

Host Agent 单元把文件系统保护设为 `ProtectSystem=strict`，代码却在进程启动后自行创建 UDS 父目录。单元没有使用 `RuntimeDirectory=` 声明这个唯一必要的可写路径，因此 systemd 沙箱先把该位置映射为只读，进程无法到达 Linux Reader。

## 修复与回归证据

- 修复：在 [`anas-host-agent.service`](../../deploy/systemd/user/anas-host-agent.service) 中增加 `RuntimeDirectory=a-nas` 和 `RuntimeDirectoryMode=0700`，保留其余沙箱限制。
- 静态检查：`systemd-analyze verify` 成功解析修复后的单元。
- 实机验证：提交 `657fe3d` 以 `agent` 模式完成哈希核对和健康检查；API 与 Agent 均为 `active`。
- 权限验证：`/run/user/1001/a-nas` 为 `0700`，`host-agent.sock` 为 `0600`，均属于 `anas-dev`。
- 故障恢复：主动停止 Agent 后产品接口返回 `503 state_unavailable`；重新启动后接口恢复 `dataSource: live`。

## 后续工作

- 保留 `RuntimeDirectory=`，不要用放宽整个 `/run/user/1001` 写权限替代。
- 后续新增 systemd 沙箱指令时，在 Experimental NAS 同时验证所需的最小可写目录。

## 关联

- 规格：[Web 桌面宿主机状态](../specs/web-desktop-host-state.md)
- ADR：[Host Agent IPC](../adr/0004-use-http-json-over-unix-socket-for-host-state.md)
- 运行手册：[部署 Web 桌面](../runbooks/deploy-web-preview-to-experimental-nas.md)
