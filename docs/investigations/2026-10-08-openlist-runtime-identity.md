# OpenList 安装后因数据目录权限反复重启

状态：root cause confirmed；fix in progress
更新时间：2026-10-08

## 症状与影响

应用中心可以完成 OpenList 安装，但容器持续重启。容器日志报告当前用户对 `/opt/openlist/data`（`./data`）没有写和/或执行权限，Web UI 无法使用。

## 最小复现

1. Experimental NAS 运行 release `c9370518bade`，Docker、Container Agent、Host Agent、Product Service 与 Kiosk 均为 active。
2. 从内置应用中心安装 OpenList 4.2.2。
3. 只读比较容器的 `Config.User`、`app-openlist` UID/GID、数据目录属主、重启状态和权限日志。
4. 实际结果：容器以 `999:1000` 运行并反复报告数据目录不可写；期望结果：容器以 A-NAS 分配的应用身份运行并能写入自己的数据目录。

## 观察事实

- 容器镜像为 `openlistteam/openlist:v4.2.2`，`configured_user=999:1000`，状态为 `restarting`、退出码 1。
- Host Agent 已创建 `app-openlist` 为 UID/GID `30002:30002`；`/srv/a-nas/data/apps/openlist/data` 为 `0750 app-openlist:app-openlist`。
- 容器把 A-NAS appdata 卷的 `data` 子路径挂载到 `/opt/openlist/data`，日志稳定报告当前用户没有该目录的写或执行权限。
- `/srv/a-nas/data/apps` 为 `root:root 0700` 是既定隔离：Docker 以 root 解析卷子路径后直接挂载应用目录，容器进程不需要穿过宿主机父目录。因此从宿主机完整路径以 `app-openlist` 执行 `access(2)` 得到 deny，不表示挂载后的应用目录属主错误。
- 内置 OpenList 清单显式写了 `user: "999:1000"`；计划渲染器保留该值，只给声明 `$PUID`/`$PGID` 插值的清单提供应用身份。
- OpenList 官方文档说明 4.1.0 之后移除 `PUID`/`PGID`，默认使用 1001:1001，并要求通过 `--user UID:GID` 或目录属主对齐运行身份。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| Host Agent 没有创建目录或目录属主漂移 | 检查账号、目录、mode 与属主 | rejected；目录存在且属于 30002:30002 |
| 数据卷只读或挂载失败 | 检查容器 Mounts 与数据卷既有安装链 | rejected；卷以 `rw=True` 挂到正确目标 |
| OpenList 镜像忽略 A-NAS 应用身份 | 比较 `Config.User`、应用身份与官方运行约定 | confirmed；999:1000 与 30002:30002 冲突 |
| 放宽 `apps` 父目录即可修复 | 区分宿主机路径遍历与 Docker 子路径挂载 | rejected；会破坏应用间隔离且不能纠正容器内挂载根的属主冲突 |

## 根因

ADR 0008 要求每个应用以独立 `app-<id>` 身份访问文件，但应用计划渲染模块只把 `PUID`/`PGID` 作为可选插值变量，没有把清单显式声明的 Compose `user` 纳入身份适配。CasaOS 来源的 OpenList 清单硬编码 `999:1000`，Host Agent 则正确地把数据目录交给分配的 `30002:30002`，于是同一执行计划的运行身份与文件授权在 Container Agent/Host Agent 边界两侧不一致。

同一缺口还会影响把 `PUID`/`PGID` 写成 `1000` 等字面量的清单：它们能够通过当前“可渲染”测试，却没有遵守 A-NAS 的应用身份不变量。

## 修复与回归证据

- 修复：[计划渲染](../../internal/appstore/render.go)在服务显式声明 `user` 时改为已分配的应用 UID/GID，并强制改写已声明的 `PUID`/`PGID`，无论原值是插值还是字面量。
- 测试：[渲染回归](../../internal/appstore/render_test.go)以 `user: 999:1000` 和字面量 `PUID`/`PGID=1000` 验证输出统一为测试应用身份。
- 实机：pending；须用新 release 重新创建 OpenList 容器，确认其 `Config.User=30002:30002`、停止重启且数据目录可写。

## 后续工作

- 为目录增加逐服务的运行身份策略词汇；固定镜像 UID、root 初始化等模式必须显式评审，不能用 `chmod 777` 或通用递归 `chown` 兜底。
- 目录升级时增加运行身份与挂载目录兼容性检查，避免只验证 Compose 可以解析。
- 安装任务后续应观察容器短期退出/重启并把错误反馈到应用中心，而不是把 `compose up -d` 成功当作应用已就绪。

## 关联

- 规格：[应用中心](../specs/app-center.md)
- ADR：[0008 统一身份与 ACL](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)、[0010 内置清单与安装策略](../adr/0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)
- 运行手册：[安装 Docker 与容器代理](../runbooks/install-container-agent.md)
