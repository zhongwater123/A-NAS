# Host Agent 的 root 一侧（internal/hostops/ 等）

Host Agent 以 root 运行，是产品里唯一的特权进程。本文件适用于它的全部代码：`.golangci.yml` 的 depguard 规则 `host-agent` 列出的包，主要是本目录、`cmd/anas-host-agent`、`internal/hoststate/agent/rootagent` 与 `internal/filebroker/rootbroker`。根目录的 [AGENTS.md](../../AGENTS.md) 仍然适用。

- **只收高层意图：** 产品服务以非 root 身份运行，可能已被攻破。Host Agent 只接受经过校验的高层意图，不提供任意 Shell（[不变量](../../docs/architecture/OVERVIEW.md#不变量) 2、3）；修改宿主机状态按“规划、确认、执行”三阶段（不变量 5）。
- **不信产品服务给的身份与路径：** 账号、UID、空间与路径由 Host Agent 从自己的记录或会话目录查出（例如删除账号时用 `internal/sessiondir` 的 `AccountRemoval` 从 control.db 读），请求里只带标识。
- **成员控制的路径都是边界：** 在成员能改的目录里，root 不按路径打开文件。以成员身份经文件代理操作，或从数据卷起用 `openat2` 的 `RESOLVE_NO_SYMLINKS`/`RESOLVE_BENEATH` 逐级打开；`O_NOFOLLOW` 只管最后一级（2026-10-15 的审查中，root 跟随中间的链接把 `/etc/shadow` 读进了确认卡片）。文件授权只有 POSIX ACL（不变量 6，[ADR 0008](../../docs/adr/0008-use-unified-linux-identities-and-filesystem-acls.md)）。
- **在进程内解码图片**（例如任务图标）前先读头部、限制尺寸，解码的 panic 视为无法解码（不变量 18）。
- **依赖：** depguard 对这些包是严格白名单，新加一个导入就要改 `.golangci.yml` 的列表，并在 PR 里说明原因：root 二进制链接的代码越少越好。
- **验证：** 改了服务单元、安装脚本、Host Agent、文件代理或文件权限，交部署前按 [system-test](../../.agents/skills/system-test/SKILL.md) 跑 systemd 系统测试；root 集成测试抓不到只在沙箱里出现的行为。
