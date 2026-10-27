# 分享服务接入后，相册会话查询与服务恢复失败

状态：fixed，系统回归通过，待实机验收
更新时间：2026-10-27

## 症状与影响

在可丢弃 Debian 13 systemd 容器安装开发版本后，成员分享与相册接口返回 503。普通文件、SMB 与终端仍正常。仅包内单元测试不能覆盖这些跨服务权限和启动关系。

## 最小复现

1. 构建二进制并运行 `scripts/system-test.sh`，使用包含当前安装器依赖的 Debian 13 镜像。
2. 首次安装后停止再启动产品服务，启用账号与测试数据卷。
3. 请求分享成员列表与相册上传；旧配置均返回 503。

## 观察事实与根因

- 相册日志为连接 `/run/a-nas-sessions/photos.sock` 时 `permission denied`。分享会话 socket 和相册 socket 原来共用父目录，而 Host Agent 的监听器会把父目录设置为当前 socket 所属组及 0750；后启动的分享监听器把目录组改为 `a-nas-shares`，使相册身份不能穿越目录。socket 本身的组权限仍然正确。
- 分享服务日志显示正常退出，systemd 状态为 `inactive (dead)`。`PartOf=anas-api.service` 会传播停止操作，但随后单独启动产品服务不会自动拉起已停止的分享服务；`Restart=on-failure` 也不会处理这次正常停止。

## 修复与回归证据

分享会话使用独立的 `/run/a-nas-share-sessions/`，由 Host Agent 的 `RuntimeDirectory` 创建。分享服务不再跟随产品服务停止，而是由现有 supervisor 检测桥与 Host Agent 代次变化，中断旧任务并重新握手。安装器仍明确重启各服务。

系统测试验证真实安装后的分享发送、回复、保存与撤回，同时验证相册仍正常，以及产品服务和 Host Agent 分别重启后分享重新可用。Go 装配测试、`make ops-check` 及完整生产容器系统测试均通过，系统测试以 `failures=0` 结束。

## 后续工作

实机验收见[已部署功能的实机验收手册](../runbooks/accept-features-on-experimental-nas.md#成员分享)。

## 关联

- [系统回归](../../scripts/system-test.sh)、[分享生产链路烟测](../../scripts/smoke-member-sharing.py)
- [Host Agent 装配](../../cmd/anas-host-agent/main.go)、[分享服务单元](../../deploy/systemd/system/anas-shares.service)
- [技术设计](../architecture/member-sharing.md)、[规格](../specs/member-sharing.md)、[ADR 0023](../adr/0023-isolate-member-share-copies-in-a-dedicated-service.md)
