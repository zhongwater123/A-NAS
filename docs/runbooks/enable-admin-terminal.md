# 开启管理员终端

状态：draft（系统测试已按本手册验证；Experimental NAS 待执行）
更新时间：2026-10-13

## 目的

让设备管理员能在“设置 → 终端”中以本人的 Linux 账号打开设备 Shell（[终端规格](../specs/web-terminal.md)）。终端默认关闭，网页中没有开关，只能由设备的 root 在产品服务配置中开启。成功标准：管理员打开“设置 → 终端”看到已连接的 Shell，`id` 显示该管理员的 UID（20100 起）与 `a-nas-admins` 组。

## 前提与风险

- 权限：NAS 的 root（`su -`）。
- 目标：主机名 `a-nas-dev`；产品服务 `anas-api.service`，配置文件 `/etc/a-nas/anas-api.env`（`root:a-nas`，`0640`）。
- 风险：开启后，任何管理员会话都能以该管理员的 Linux 账号执行任意命令，权限与该管理员在网页中的文件权限一致，没有 root 或 `sudo`。经局域网 HTTP 使用时，键入的命令和输出与会话 Cookie 一样明文传输（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)）。重启产品服务会让正在进行的网页操作短暂断开，上传会自动续传。
- 版本：包含本手册的版本起，系统服务安装器在升级时保留 `ANAS_TERMINAL=enabled`；更早的安装器会丢弃它，在旧版本上开启后，重新安装旧版本需要重做本手册。
- 停止条件：`/etc/a-nas/anas-api.env` 不存在（系统服务尚未安装）；重启后 `anas-api` 不是 `active`。

## 步骤

1. 确认当前未开启。
   - 命令：`grep -x 'ANAS_TERMINAL=.*' /etc/a-nas/anas-api.env`
   - 预期：没有输出。
   - 完成标准：没有输出则继续；已经是 `ANAS_TERMINAL=enabled` 时跳到“验证”。
2. 写入开启项并重启产品服务。
   - 命令：

     ```bash
     echo ANAS_TERMINAL=enabled >> /etc/a-nas/anas-api.env
     systemctl restart anas-api
     ```

   - 预期：命令无输出。
   - 完成标准：`systemctl is-active anas-api` 为 `active`，且 `journalctl -u anas-api -n 50 -o cat | grep 'A-NAS API listening'` 的最新一行含 `terminal=true`。

## 验证

- 管理员在本机屏幕或 `http://172.18.45.48` 打开“设置 → 终端”，页头显示“已连接”；执行 `id` 显示管理员本人的 UID 与 `a-nas-admins` 组，`pwd` 为 `/srv/a-nas/data/spaces/private/<账号>`，`ls /srv/a-nas/data/spaces/private/<其他成员>` 被拒绝。
- 成员账号的设置侧栏没有“终端”。
- 下一次部署后，`grep -cx 'ANAS_TERMINAL=enabled' /etc/a-nas/anas-api.env` 仍为 `1`。

## 回滚或恢复

- 关闭终端：`sed -i '/^ANAS_TERMINAL=/d' /etc/a-nas/anas-api.env && systemctl restart anas-api`。正在运行的 Shell 随产品服务重启结束，“设置 → 终端”回到“终端尚未开启”。
- 产品服务无法启动：先看 `journalctl -u anas-api -n 50 -o cat`；`ANAS_TERMINAL must be enabled or disabled` 表示该行写错，按上一条删除后重启。

## 关联

- 规格：[Web 桌面终端](../specs/web-terminal.md)、[设置](../specs/settings.md)
- ADR：[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)、[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)
- 调查：不涉及
- 代码与测试：[系统服务安装器](../../scripts/install-v1.0.1-system-services.sh)、[系统测试](../../scripts/system-test.sh)
