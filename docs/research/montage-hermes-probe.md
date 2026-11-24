# 剪辑规划在 Hermes 定时任务里运行的验证（Hermes 探针 P1）

> 状态：已被取代（2026-11-20）。本文验证的是借定时任务跑一轮规划，M9 照它实施；实机验收时卡在“等待 VNAS”，用户 2026-11-20 [决定](../decisions/2026-11-20-montage-planning-as-a-vnas-session.md)改为成员规划进程里的一次对话（[规划会话](../architecture/montage-planning-session.md)）。本文只作记录：开发机完成（2026-11-18），两项必过都过了  
> 更新日期：2026-11-20  
> 适用范围：Hermes 上游 `8fd9f706` 加补丁 0001（当前主线固定的提交，`ai/assistant-spike/run.sh`），2026-11-18 在开发机 C 的 Colima `anas` 虚拟机（aarch64）里用 `run.sh build` 重建，运行环境压缩包 sha256 `eec1ae2753dd62f8732c2dce5fd0db978bc167f601a2687de7dc8a882fb6ea65`（重建不可复现，与 `runtime.json` 记的不同，源码相同）；spike 容器 `anas-systemd:trixie`；替身模型 `fake_model.py`；替身的 montage 服务器 `montage_stub_mcp.py`  
> 关联：[剪辑中心实施方案](../architecture/montage-center.md#规划)的“规划”、[评审意见](https://github.com/zhongwater123/A-NAS/pull/518#pullrequestreview-5950219461)、[Hermes Agent 可行性研究](hermes-agent-runtime.md)、[gateway 运行方式的决定](../decisions/2026-11-10-hermes-gateway-runtime.md)、测试 [`montage-plan.sh`](https://github.com/zhongwater123/A-NAS/blob/3ddad5c50d025af69d8dd1ef9eecce1e4e6a3ea5/ai/assistant-spike/montage-plan.sh)（探针的脚本没有合入主线，在 [#546](https://github.com/zhongwater123/A-NAS/pull/546) 的提交 `3ddad5c5` 里）

剪辑中心方案让规划由 NAS 在后台发起：在成员的定时任务单元里，把这一轮作为一个 Hermes 任务交给 gateway，只开放 montage 工具组，结束后不跑后台复盘。评审人要求动工前先做探针，确认 Hermes 的内部接口可用，并把“只开放 montage”列为必过项；方案另把“不跑后台复盘”列为必过项。本文用替身模型和替身的 montage 服务器，在 spike 容器里逐项验证这一轮要用到的 Hermes 机制。

## 结论

1. **必过项一：这一轮只开放 montage，过了**。`create_job(enabled_toolsets=["montage"])` 建的任务，运行时模型只拿到 montage 的 4 个工具；提示它调用终端，终端没有执行。
   - Hermes 的做法（`cron/scheduler.py` 的 `_resolve_cron_enabled_toolsets`、`_merge_mcp_into_per_job_toolsets`）：任务自己的 `enabled_toolsets` 优先于 `platform_toolsets.cron`；它点名了某个 MCP 服务器时，不再把别的服务器并进来；`agent.disabled_toolsets` 照样叠加在上面。
   - 所以 montage 不用加进任何 `platform_toolsets`。不加时，成员自己的定时任务和对话都看不到剪辑工具（实测都是零个）。方案写的“只加进 `platform_toolsets.cron`”要改。
2. **必过项二：结束后不跑后台复盘，过了**。Hermes 给所有定时任务的运行建 Agent 时都传 `skip_background_review=True`（`cron/scheduler.py`），运行后替身模型没有收到复盘请求。这对成员自己的定时任务也一样，不是只对规划这一轮。
3. **成员的记忆会进这一轮的系统提示**。同一处固定传 `skip_memory=False`，成员 `MEMORY.md` 里的记号出现在这一轮的系统提示里，按任务关不掉。模型上没有 `memory` 工具，写不了记忆；但读到的记忆会随这一轮发给模型。VNAS 用云端模型时，这和成员自己的定时任务、对话一样。
4. **任务的建、触发与删除都能用 Hermes 自己的接口**。
   - `create_job(paused=True)` 建成暂停的任务，定时任务的一轮不会运行它；`trigger_job` 使它到期并恢复，下一轮就运行。
   - 运行留下的会话编号是 `local-<哈希>`，由调度器的准入记录挂到任务上，不是研究文档里写的 `cron_<任务>_<时间>`。要用 `SessionDB.list_cron_job_runs` 找（Hermes 自己的“运行记录”接口也这样找）。
   - 先删会话（`SessionDB.delete_session`）再删任务（`remove_job`），任务和会话都不留。删任务时如果它正在运行，Hermes 会中止这次运行（`remove_job` 里的 `cancel_job_run`，读源码得知，未实测），可以用来取消。
5. **运行中途杀掉单元，不会自动重跑**。Hermes 在运行开始时就把任务的下次运行往后推一个周期，所以杀掉后它不再到期，下一轮也不运行它，只把它记成 `last_error=interrupted`。但它仍是一个按周期运行的任务，不删的话一个周期后会再跑一次，所以回合脚本开头的清理是必需的。
6. **对话实例里的剪辑服务器列零个工具，对话照常**；但每个 MCP 服务器的子进程约占 78–83 MiB，在线成员的每个实例都会多这一份。建议定时任务单元用一份单独的成员配置带上 montage，对话实例的配置不带（未实测）。
7. **内存**：规划这一轮运行时，定时任务单元（gateway 加 `anas`、`companion` 与 `montage` 三个 MCP 服务器）占 642 MiB；gateway 约 4 秒就绪，一轮（替身模型）约 13 秒。
8. **不调用模型就能查模型通不通**：经通往模型的桥 `GET /v1/models`，替身模型回答 200。spike 的 `gateway.py` 把这个请求原样转给上游，上游的模型列表不消耗 token；转正后的模型网关怎样回答还要核对。

## 做法

`run.sh test montage-plan.sh` 在 spike 容器里以 root 运行，做法如下：

- **montage 服务器**：写进托管配置和成员的配置（`mcp_servers`，`trust: full`、`enabled: true`、`lazy: false`），不加进任何 `platform_toolsets`。替身 `montage_stub_mcp.py` 有方案里的 4 个工具名，每次调用在助手目录记一行；参数里有 `sleep` 时先睡这么久。
- **只在定时任务单元里列工具**：方案里剪辑的套接字只绑定进定时任务单元。探针给这个单元加一个 drop-in，在私有的 tmpfs 里绑定一个占位文件 `/run/anas-assistant-montage/montage.sock`；替身看到它才列出工具，对话实例里看不到，就列零个。Hermes 只把 `PATH`、`HOME`、`LANG` 一类环境变量传给 MCP 子进程（`tools/mcp_tool_config.py` 的 `_build_safe_env`），所以不能靠环境变量区分。
- **在成员沙箱里建、触发、删任务**：照探针单元的做法，从 `anas-assistant@.service` 生成 `anas-assistant-montage-jobs@`，以成员身份运行 `montage_jobs.py`。它用 `python -P`：`/spike` 里有 spike 自己的 `gateway.py`，脚本目录排在模块搜索路径最前面时会盖住 Hermes 的 `gateway` 包，删会话时会导入出错。
- **跑一轮**：`systemctl start anas-assistant-cron@alice`，即 `cron_round.py` 的一轮（启动 gateway、`hermes cron tick`、等运行结束、停 gateway）。
- **看模型拿到了什么**：替身模型记下每次请求带的工具名、是不是复盘请求，并存下系统提示。

## 结果

`montage-plan.sh` 的 21 项全部通过（2026-11-18 第三次运行，前两次的失败都是测试本身的错，见“测试中改过的地方”）：

| 项 | 结果 |
|---|---|
| 建成暂停、只开放 montage 的任务 | 通过：`state=paused`，`enabled_toolsets=['montage']` |
| 暂停的任务不被一轮运行 | 通过：没有 `last_run_at`，montage 没有收到调用 |
| `trigger_job` 后下一轮运行 | 通过：`last_status=ok`，`montage_brief` 到达服务器 |
| 模型拿到的工具 | 通过：只有 `mcp__montage__montage_brief`、`…_shots_brief`、`…_submit_outline`、`…_submit_shots` |
| 运行后有没有复盘 | 通过：没有复盘请求 |
| 成员的记忆进系统提示 | 是：`MEMORY.md` 里的记号在系统提示里 |
| 这一轮调用终端 | 通过：终端没有执行，任务照常结束（`last_status=ok`） |
| 成员自己的定时任务拿到的 montage 工具 | 零个 |
| 删任务连同运行会话 | 通过：删掉 1 个 `local-*` 会话，任务与会话都不留 |
| 运行中途杀掉单元 | 通过：不再到期（下次运行在一个周期后），下一轮不重跑，记成 `interrupted`；清理删掉任务与会话 |
| 对话实例里的 montage | 通过：子进程启动 1 次、列 0 个工具，对话正常，模型拿不到 montage 的工具；实例日志里没有关于 montage 的错误 |
| 经模型桥 `GET /v1/models` | 200（替身模型） |

**内存**（虚拟机，aarch64）：

| 对象 | 占用 |
|---|---|
| 定时任务单元，规划这一轮等着 montage 回答时 | 642 MiB |
| 对话实例（开着 4 个 MCP 服务器） | 507 MiB |
| 每个 MCP 服务器子进程（RSS） | montage 替身 78 MiB，`anas_mcp.py` 83 MiB，`anas_desktop_mcp.py` 78 MiB，`anas_companion_mcp.py` 78 MiB |

[gateway 运行方式的决定](../decisions/2026-11-10-hermes-gateway-runtime.md)记的是一轮 410–610 MB（三个 MCP 服务器）。这次的定时任务单元也是三个（`anas`、`companion`、`montage`；`desktop` 不在 cron 里），642 MiB 落在上限附近。

## 对实施方案的影响

下面几处要改 PR #518 的实施方案（“规划”一节），等评审人看过本文再改：

1. montage 服务器不加进任何 `platform_toolsets`。规划这一轮的任务建时带 `enabled_toolsets=["montage"]`；成员自己的定时任务与对话都看不到它。
2. “这一轮结束后不跑后台复盘”由 Hermes 对所有定时任务运行固定做到，不用另外设置；方案里把它写成要由探针定办法的地方，改成引用这里。
3. 成员的记忆会随这一轮发给模型，写进规格的“边界与失败”。
4. 运行会话按 `list_cron_job_runs` 找、按 `local-*` 编号删；回合脚本用 `python -P`。
5. 杀掉单元后任务不会自动重跑，但一个周期后会；回合脚本开头的清理照方案保留。周期取多长，或改用只跑一次的任务（`repeat=1`），在实现时定。
6. 对话实例里多一个约 78 MiB 的空闲子进程。可以给定时任务单元单独一份成员配置带上 montage，对话实例的配置不带（要实测）。
7. 取消可以直接 `remove_job`，Hermes 会中止正在进行的运行（要实测）。

## 测试中改过的地方

- 第一次运行：“暂停的任务不运行”判错了（替身启动时就写日志，不能凭日志非空判断）；“删掉运行会话”误判通过（按 `cron_<任务>_` 前缀找不到 `local-*` 会话，删掉的是空列表）；“中途杀掉”一节没建成任务。
- 第二次：删会话时 `/spike/gateway.py` 盖住了 Hermes 的 `gateway` 包（改用 `python -P`）；杀掉定时任务单元后，同一网络的单元留在失败状态，之后启动辅助单元报“依赖失败”（启动前 `reset-failed`）。
- 第三次全部通过。

## 复现

在开发机 C 的 Colima `anas` 虚拟机里，检出目录下：

```sh
HERMES_SPIKE_DIR=$HOME/hermes-probe ai/assistant-spike/run.sh build
HERMES_SPIKE_DIR=$HOME/hermes-probe HERMES_SPIKE_NAME=montage-probe-sbx ai/assistant-spike/run.sh sandbox
HERMES_SPIKE_DIR=$HOME/hermes-probe HERMES_SPIKE_NAME=montage-probe-sbx ai/assistant-spike/run.sh test montage-plan.sh
```

`build` 从 GitHub 取固定的上游提交，打上 `hermes-patches/` 的补丁，在 `anas-ai-dev:trixie` 里构建；`sandbox` 用 `anas-systemd:trixie` 起一次性的 systemd 容器，探针 39 项全部通过。

## 未做

- 真实模型（DeepSeek）走完两步规划：只用了替身模型，不知道真实模型在只有 4 个工具、没有终端时会不会按提示交答案。
- 实验 NAS（x86）上的内存与用时。
- AI 助手 V1 转正时固定的 Hermes 提交：转正换了提交，按评审要求重跑本文的测试。
- `remove_job` 中止正在进行的运行、`repeat=1` 的任务被杀后的行为、定时任务单元单独一份配置。
- 转正后的模型网关对 `GET /v1/models` 的回答。
