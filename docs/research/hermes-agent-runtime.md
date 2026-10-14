# Hermes Agent 作为 AI 助手运行时的可行性

更新时间：2026-10-14

本页保存选择 [Hermes Agent](https://github.com/NousResearch/hermes-agent) 作为 AI 助手运行时所依据的外部事实、设备事实与待验证项。决定见 [ADR 0022](../adr/0022-run-a-sandboxed-hermes-agent-per-member.md)，行为见 [AI 助手规格](../specs/ai-assistant.md)。

## 上游事实（2026-10-14 查阅）

| 方面 | 事实 | 来源 |
|---|---|---|
| 许可与形态 | MIT；Python 核心，另有 Node/TypeScript 的终端界面与网页，A-NAS 不需要后两者 | [仓库](https://github.com/NousResearch/hermes-agent) |
| Python | `requires-python = ">=3.11,<3.15"`，核心依赖逐个固定版本；官方安装器默认准备 Python 3.14、Node.js、ripgrep 与 FFmpeg | [pyproject.toml](https://raw.githubusercontent.com/NousResearch/hermes-agent/main/pyproject.toml) |
| 版本 | `pyproject.toml` 的版本号为 `0.0.0`，仓库没有发布版本，提交超过五万次；只能按提交固定 | 同上 |
| API Server | 默认 `127.0.0.1:8642`，要求 `API_SERVER_KEY`；提供 OpenAI 兼容的 Chat Completions 与 Responses、带 SSE 事件的 Runs（`approval.request` 事件与 `POST /v1/runs/{id}/approval`）、会话与定时任务接口；页面提醒 API 默认带终端能力 | [API Server](https://hermes-agent.nousresearch.com/docs/user-guide/features/api-server) |
| 工具 | 内置 toolset 包括 `file`（`read_file`、`write_file`、`patch`、搜索）、`terminal`、`code_execution`、`browser`、`web`、`memory`、`session_search`、`skills`、`cronjob`、`delegation` 等，可按平台启用；MCP 服务器的工具以 `mcp-<名称>` 出现 | [Tools](https://hermes-agent.nousresearch.com/docs/user-guide/features/tools) |
| MCP | 作为客户端连接 stdio 或 HTTP 服务器；每个服务器可配 `headers`、`identity_header`，可用 `tools.include`/`exclude` 白名单 | [MCP](https://hermes-agent.nousresearch.com/docs/user-guide/features/mcp) |
| 写保护 | `HERMES_WRITE_SAFE_ROOT` 把 `write_file` 与 `patch` 限制在指定目录；官方说明这些保护不能把恶意或被攻破的 Agent 关进沙箱，`terminal` 仍以同一系统用户运行 | [Security](https://hermes-agent.nousresearch.com/docs/user-guide/security) |
| Profile | 每个 profile 有独立的配置、`.env`、记忆、会话、技能与定时任务；官方明说 profile 不是沙箱，同一系统用户下的 Agent 可以访问该用户能访问的一切 | [Profiles](https://hermes-agent.nousresearch.com/docs/user-guide/profiles) |
| 外部服务 | Nous Tool Gateway（搜索、生图、TTS、浏览器）、Honcho 用户建模、`smart` 审批的辅助模型与自更新都会访问外部网络，A-NAS 全部关闭 | [Security](https://hermes-agent.nousresearch.com/docs/user-guide/security)、[Tools](https://hermes-agent.nousresearch.com/docs/user-guide/features/tools) |
| Docker 后端 | 官方容器加固去掉全部能力后加回 `DAC_OVERRIDE`、`CHOWN`、`FOWNER`；容器内的 root 因此无视挂载目录上的 POSIX ACL | [Security](https://hermes-agent.nousresearch.com/docs/user-guide/security) |

## 设备事实（Experimental NAS，2026-10-14 只读查看）

- 内核 `6.12.111+deb13-amd64`，启用的 LSM 为 `lockdown,capability,landlock,yama,apparmor,tomoyo,bpf,ipe,ima,evm`：Landlock 可用。
- systemd 257（`257.13-1~deb13u1`）：支持 `PrivatePIDs=`。
- `kernel.yama.ptrace_scope = 0`：同一 UID 的进程可以互相 ptrace，以成员身份运行的助手必须自己封住这条路（PID 命名空间与系统调用过滤）。
- `kernel.unprivileged_userns_clone = 1`。
- 系统 Python 为 Debian 13 的 3.13；Hermes 的依赖只在 3.14 上安装（见下文 spike 第 1 项），因此运行环境自带 Python。

## Spike 结果（2026-10-14，开发机）

在开发机上完成（WSL2 内核 6.18、14 核；Docker 中的 Debian 13 容器 `anas-ai-dev:trixie` 构建运行环境，以 systemd 为 PID 1 的 `anas-systemd:trixie` 运行沙箱），固定 Hermes 提交 `0e427d788d25`（2026-10-13）。模型由一个按脚本回答的 OpenAI 兼容假模型代替，它记录 Hermes 每次提供给模型的工具，并按指令发出指定的工具调用。全部脚本在 [`ai/assistant-spike`](../../ai/assistant-spike/run.sh)，按其中的顺序可以复现；没有访问实验 NAS。

| # | 问题 | 结果 |
|---|---|---|
| 1 | 离线运行环境 | 可行，但不是 ADR 0016 的做法：Hermes 的核心依赖全部带 `python_version >= '3.14'` 标记，上游注释写明只支持 3.14，所以运行环境自带 uv 下载的 python-build-standalone CPython 3.14.8；Hermes 拒绝构建 wheel（只支持安装脚本、Docker、Nix 与可编辑安装），所以固定提交的源码放进运行环境并以可编辑方式安装。依赖用上游 `uv.lock` 导出为带哈希的需求文件（`mcp` 与 `sms` 两个可选组件，后者只含 API Server 需要的 aiohttp），共 99 个包。解开约 425 MB（Python 113 MB、依赖 161 MB、源码 151 MB），压缩包 132 MB；不需要 Node.js。设备另需 Debian 包 `ripgrep`（文件搜索）、`socat`（转发）与 `tini`（见第 5 项）。 |
| 2 | 内存与启动 | 经套接字冷启动到 API 可用 5.5–11 秒；刚启动时约 200–230 MB，一次对话后约 255 MB，21 轮工具调用后 270 MB，峰值不超过 300 MB；助手目录中的 Hermes 状态 21 轮后 6.7 MB。两个成员同时冷启动 6 秒，各约 215–226 MB。 |
| 3 | 工具收窄 | `/etc/hermes` 的 managed scope 中写 `platform_toolsets.api_server: [file, memory, session_search, skills, todo]` 后，模型每次只看到 `read_file`、`write_file`、`patch`、`search_files`、`memory`、`skill_*` 与按需加载的 `tool_search`/`tool_describe`/`tool_call`；直接调用 `terminal`、`execute_code` 得到“工具不存在”，经 `tool_call` 调用被拒绝（它只能调用按需加载的待办与会话搜索）。在助手可写的 `~/.hermes/config.yaml` 放入启用终端、改模型地址、关闭审批的配置后重启，启用的工具与模型地址不变：managed scope 优先。Hermes 也拒绝 Agent 用文件工具写自己的配置。API Server 只有在 `API_SERVER_KEY` 至少 16 个字符时才启动，错误的或其他成员的密钥返回 401。 |
| 4 | 私有网络中的转发 | 可行：`anas-assistant-api@<账号>.socket` 监听每个账号的 Unix 套接字，触发的 `systemd-socket-proxyd` 以 `JoinsNamespaceOf=` 进入实例的网络命名空间转发到 `127.0.0.1:8642`；实例内的 `socat` 把 `127.0.0.1:18001` 转到绑定进来的网关套接字。`--exit-idle-time=45s` 加 `StopWhenUnneeded=yes` 使实例在最后一次请求约 45 秒后停止；`kill -9` 主进程后单元进入 `failed`。 |
| 5 | 沙箱 | 探针在实例自己的单元中运行，22 项全部符合预期：以 alice 的 UID 与 `a-nas-users` 组运行、能力为 0；能读本人个人空间、本人回收站、共享空间与他人的 Agent空间，读不到他人的回收站；`/srv` 为空，找不到其他成员的空间与快照；个人空间、共享空间与本人 Agent空间子目录的写、删、改名、`chmod`、`setfacl` 均因只读挂载失败；只有助手目录可写；`/etc/hermes` 与运行环境不可写；外网与 Docker 主机不可达；只看到自己的进程；ptrace、`setuid(0)` 与创建用户命名空间被拒绝。`PrivatePIDs` 使 Hermes 成为命名空间中的 1 号进程，它提示不能回收孤儿进程，所以以 `tini` 作为 1 号进程。 |
| 6 | 只读空间上的写入 | Hermes 的写保护先于内核拒绝：`patch` 个人空间文件返回 “Write denied: ... is outside HERMES_WRITE_SAFE_ROOT (/nas/助手). Unset the variable or add this path's directory prefix.”，并附“文件修改未成功”的提示；这句提示会误导模型去改环境变量，随系统附带的技能需要说明暂存区。在 `/nas/助手/待确认/个人空间/...` 中 `write_file` 与 `patch` 正常。**模型能否按技能稳定改用暂存区尚未验证**：需要真实模型，等待开发用的模型 API。 |
| 7 | 容量上限 | Btrfs simple quota（内核 6.7 起，实验 NAS 为 6.12；btrfs-progs 6.14）：为子卷设 64 MiB 上限后，写到约 64 MiB 时返回 “Disk quota exceeded”。在已有快照与数据的实验 NAS 数据卷上开启 simple quota 的影响尚未验证。 |

其他观察：

- 每开始一个新对话，Hermes 会多发一次请求让模型生成会话标题；模型的上下文长度未配置时，它还会反复请求 Ollama 风格的 `/api/show` 探测。正式配置应写明 `context_length`，并决定是否保留标题生成（它使模型请求数增加约一半）。
- 系统提示约 13,000 字符，选择本地模型时要计入预填充开销。
- Hermes 默认会联网检查更新、拉取模型目录、在运行时安装缺少的包并按需安装语言服务器；managed scope 中分别以 `updates.check`、`model_catalog.enabled`、`security.allow_lazy_installs`、`lsp.enabled` 关闭（[`managed-config.yaml`](../../ai/assistant-spike/managed-config.yaml)）。
- systemd 在 `PrivatePIDs` 下提示 “Supervising process ... which is not our child”，但停止、闲置退出与崩溃都被正确识别。

## 关联

- 决定：[ADR 0022](../adr/0022-run-a-sandboxed-hermes-agent-per-member.md)
- 规格：[AI 助手](../specs/ai-assistant.md)
- Spike 脚本：[`ai/assistant-spike`](../../ai/assistant-spike/run.sh)（沙箱单元 [`anas-assistant@.service`](../../ai/assistant-spike/anas-assistant@.service)、探针 [`probe.sh`](../../ai/assistant-spike/probe.sh)）
- 参照：[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)（离线运行环境）、[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)（身份与 ACL）、[存储与文件架构](../architecture/storage-and-files.md)
