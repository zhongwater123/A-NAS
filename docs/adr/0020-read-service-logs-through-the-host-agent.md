# 0020：系统日志经 Host Agent 的白名单 journal 查询读取

状态：proposed（2026-10-12，随“设置 → 日志”PR 提交评审）；行为见[日志规格](../specs/logs.md)。

管理员需要在 Web 中查看 A-NAS 服务的运行日志，而这些日志只在 systemd journal 中，读取它需要 root 或 `systemd-journal` 组；非特权的产品服务（`a-nas`）两者都没有。我们决定：由 root Host Agent 提供一个只读、类型化的系统日志查询，按固定参数运行 `journalctl`，只匹配 A-NAS 服务白名单中的单元；产品服务只把管理员的筛选条件转成这个查询，不获得任何读取 journal 的权限。

## 决定

1. **白名单按键名**：产品服务只能传服务键名（如 `api`、`photos`），由 `internal/systemlog` 中的固定表映射到单元名或模板（如 `anas-ffmpeg@*.service`）。白名单包含 A-NAS 自己的服务、本机屏幕会话，以及 A-NAS 托管配置的 `smbd` 与 `caddy`；不含内核、SSH、sudo、Docker 或应用容器。
2. **固定命令**：Host Agent 以 `journalctl --output=json --all --quiet --no-pager` 加按键名生成的 `--unit` 运行，时间范围用 `--since=@<秒>`/`--until=@<秒>`，翻页用经格式校验的 `--after-cursor`；不经 Shell，不接受任意 match、`--grep` 表达式或文件路径。关键词与级别在 Host Agent 中逐行按纯文本过滤。
3. **有界输出**：每次最多返回 500 行、检查 20,000 行、运行 10 秒，单行消息截断到 16 KiB；超出时返回已读部分和继续翻页的游标。
4. **只对管理员开放**：产品 API `/api/v1/system-logs` 只接受管理员会话；开发模式使用 Fake 读取器，与 Linux 实现遵循同一契约。

## 后果

- 不需要新的系统账号、组或 journald 配置；产品服务被攻破时，能读到的只是白名单服务的日志，与管理员在 Web 中看到的相同。
- 服务日志中出现的账号名、文件名与客户端地址会显示给管理员；A-NAS 自己的日志按约定不写文件内容和密码。
- Host Agent 多了一个会长时间运行子进程的读取接口；超时、截断与上下文取消保证它不会占住 Host Agent。
- 新增服务时必须同时更新白名单和规格中的服务表，否则它的日志不会出现在 Web 中。

## 未选择

- **把 `a-nas` 加入 `systemd-journal` 组**：实现最简单，但产品服务会读到整台机器的 journal，包括 SSH、sudo 与其他用户的日志，违背产品服务非特权的边界（[架构不变量](../architecture/OVERVIEW.md#不变量) 2、3）。
- **各服务另写一份日志文件给产品服务读取**：日志重复、需要自己轮转与限额，且仍读不到 systemd 自身关于启动失败、重启和超时的记录，而这些恰恰是排障最需要的。
- **journald 远程上传到产品服务**：需要额外的 `systemd-journal-remote` 服务、存储与配额，对单机家庭 NAS 过重。

## 关联

- [日志规格](../specs/logs.md)
- [ADR 0004](0004-use-http-json-over-unix-socket-for-host-state.md)、[ADR 0007](0007-use-btrfs-sqlite-and-a-typed-privilege-boundary.md)
- [系统总览](../architecture/OVERVIEW.md)
