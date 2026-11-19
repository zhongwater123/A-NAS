# 剪辑中心的规划会话

状态：accepted（用户 2026-11-20 [决定](../decisions/2026-11-20-montage-planning-as-a-vnas-session.md)），实现中
更新时间：2026-11-20

剪辑任务走到规划时，由成员自己的 VNAS 在后台做两步规划（大纲、挑节拍），两个方案。本文是交给 VNAS 的那一段：谁发起、在哪个进程里跑、云端看到什么、怎样等它结束、怎样重试、VNAS 怎样学习。剪辑中心其余部分（任务、步骤、简报与答案的校验、填槽、渲染、存回相册）不变，见剪辑中心的实施方案（随 [#518](https://github.com/zhongwater123/A-NAS/pull/518) 合入）。

它取代原来借成员定时任务单元跑一轮的做法（`montage_round.py`、规划令牌与领取回执、定时任务里的规划任务）。原因见[决定记录](../decisions/2026-11-20-montage-planning-as-a-vnas-session.md)与[调查](../investigations/2026-11-19-montage-job-stalls-on-the-experimental-nas.md)。

## 不变的规则

- **云端看不到画面**：规划时模型只拿到剪辑工具给的文字与结构化数据（本地 Qwen 写的卡片描述，号码已遮掉；节拍；音乐段落；成员的要求），没有图片，也没有能取到原片的文件路径。规划进程里没有看图、看视频、读文件、终端、相册与桌面的工具，提示词改变不了这一点。
- **VNAS 是成员自己的**：规划在成员的沙箱里、以成员的身份、带着成员的记忆与技能运行；规划对话留在成员的 VNAS 历史里。
- **授权**：只有成员在场时登记过规划授权（7 天、5 轮，文件代理写在 Host Agent 自己的状态目录里），后台才为他发起规划。剪辑服务拿不到任何成员实例的凭据。
- **模型可能已被调用就不再自己重来**（#514）：一轮里模型调用过剪辑工具而没交完两个方案，任务停下等成员处理。

## 组成

### 1. 规划进程：`anas-assistant-plan@<账号>`

成员平时的 VNAS 是 `anas-assistant@<账号>`（`hermes serve`）。规划进程是同一个沙箱里的第二个 `hermes serve`：

- 由 `scripts/lib/assistant-units.sh` 从 `anas-assistant@.service` 生成，与定时任务、整理单元同法：同样的挂载、同一个 Hermes 主目录（记忆、技能、对话记录共用）、同一个私有网络与模型桥（`JoinsNamespaceOf=anas-assistant-net@%i.service`），端口换成 9120。
- **工具在进程级固定**：`HERMES_TUI_TOOLSETS=montage,memory,skills`。`hermes serve` 不能按单次对话收窄工具，工具组整个进程一致，所以只能用一个单独的进程。实测模型只拿到 `memory`、`skill_manage`、`skill_view`、`skills_list` 与剪辑的 4 个工具。
- **自己的托管策略**：`managed-plan/` = 普通托管策略加上剪辑的 MCP 服务器（`montage-mcp.yaml`），把 `skills.creation_nudge_interval` 设为 1，每次规划结束都复盘。Host Agent 按它写 `members/<账号>/config-plan.yaml`，单元绑定成 Hermes 的 `config.yaml`。原来的 `managed-cron/` 与 `config-cron.yaml` 只为规划而有，一并去掉，定时任务单元回到普通策略。
- **只有它绑定剪辑套接字**（`/run/a-nas-montage-assistant/assistant.sock` → 沙箱内 `/run/anas-assistant-montage/montage.sock`）；成员平时的 VNAS、定时任务与整理单元都不绑定。
- **自己的入口**：`anas-assistant-planapi@.socket`（`/run/a-nas-assistant-plan/<账号>.sock`，root 0600）与 `anas-assistant-planapi@.service`（`systemd-socket-proxyd --exit-idle-time=45s 127.0.0.1:9120`），与 `anas-assistant-api@` 同构。连接来了才起进程，没有连接 45 秒后退出（`StopWhenUnneeded=yes`）。
- 与成员平时的 VNAS 可以同时运行；两个进程同时写同一主目录的记忆与技能，实测没有丢失。

### 2. 规划发起方：`anas-montage-planner.service`

root 的长驻服务，取代唤醒服务里的规划部分（`cron-waker.py` 只留成员的定时任务）。用 AI 助手运行环境的 Python（有 `websockets`）。

1. **找要规划的**：剪辑服务的记号（`/run/a-nas-montage/plans/<账号>`，只读）、规划授权（只认 root 所有、他人不可写的文件）、账号在用且有 VNAS。全机同一时间只规划一个。
2. **领取一轮**：在剪辑服务的助手套接字上 `POST /v1/rounds/claim`。规划发起方以 root 连接，请求带 `X-A-NAS-Account: <账号>`；剪辑服务只对 UID 0 的对端认这个头（套接字目录只有 root 进得去，沙箱里绑定的对端都是成员 UID），其余对端照旧按 UID 认成员。
3. **开对话**：连 `/run/a-nas-assistant-plan/<账号>.sock`，用 Hermes 自己的登录（`/auth/password-login`，密码是 Host Agent 写的实例密钥，`/var/lib/a-nas-host/assistant/tokens/<账号>.env`），再走 `/api/ws` 的 JSON-RPC：`client.capabilities {server_requests: true}`、`session.create`、`session.title`（“剪辑规划 · <项目名>”）、`prompt.submit`（固定模板 `ai/anas_montage/prompts/<模板>.md`，只填检查过的轮次、任务编号、token 与目标秒数）。然后报 `POST /v1/rounds/<轮次>/started`，`runRef` 是对话的存储编号。
4. **等它结束**：一直连着，收事件直到这次提问的 `message.complete`；Hermes 发来的审批请求一律答 `deny`（实测不应答也是拒绝，这里明确拒绝）。之后再连一段，等后台复盘做完（复盘在断开约 20 秒后会被回收）。连接断了、进程崩溃或超时，都算这一轮结束，按结果报告。
5. **报结果**：`POST /v1/rounds/<轮次>/end`，结果写明完成、出错（附错误原文）、超时或中断；从没开始（登录不了、对话建不起来）报 `abandon`。
6. **轮数**：规划发起方自己数它发起的对话，按授权记在自己的状态文件里，不再要成员目录里的回执。

### 3. 剪辑中心

- 一轮就是一次对话：`rounds.run_ref` 记对话编号，成员的 VNAS 历史里能找到它。
- 剪辑工具与答案校验不变（`/v1/tools/*`，按 `jobId` 与 token）。
- 一轮的时限按[原来的规则](../../internal/montage/planning.go)：领取后 5 分钟没开始就作废；开始后距最后一次调用工具 20 分钟、或开始后 65 分钟仍没结束，就结束。

### 4. 重试

按原因处理，替代唤醒服务按次数翻倍到 1 小时的退避：

| 结果 | 处理 |
|---|---|
| 剪辑中心没有要规划的任务 | 不算失败，不退避，删记号 |
| 暂时连不上（规划进程起不来、登录失败、模型网关不通、剪辑套接字不通） | 不计轮数；2、4、8 分钟后再试，最长 15 分钟 |
| 规划进程或发起方出错（异常、协议不符） | 不计轮数；再试一次，仍出错就让任务停下，页面写明“规划出错”与错误原文 |
| 模型调用过剪辑工具、没交完 | 计轮数；任务停下等成员处理（不变） |
| 两个方案都交了 | 计轮数 |

成员一做会登记授权的操作（开始、重新规划、去掉素材、重试分析），或装了新版本，就清掉这位成员的等待，立即再试。任务页写明在等什么、下次什么时候试。

## 学习

- **读**：规划对话照常带着成员的记忆与技能进系统提示。
- **写**：规划进程里的后台复盘与平时对话一样运行；每次规划结束都复盘（`creation_nudge_interval: 1`），被退回的原因、摸索出的做法写成技能，下次规划直接用。实测复盘在受限进程里写出了技能。
- **成员的反馈**：规划对话留在成员的 VNAS 历史里，成员打开它说“开场太快了”，用的是平时的 VNAS（工具齐全，因为是成员本人在说），复盘照常记下。之后再加“剪辑偏好记录”（挑了哪一版、重新规划时的新要求），作为下一步。
- **风险**：卡片描述里可能有画面里的外来文字，复盘可能把它写进记忆或技能（用户已接受，同 2026-10-15 对普通对话的决定），成员在“记忆”页查看与删除。

## 测试

- **规划链路的系统测试**（带真实运行环境时）：真实的 Hermes、规划进程与入口、剪辑服务、规划发起方；预置一个停在规划步骤的小任务，脚本化模型扮演 VNAS 调四个剪辑工具、交两个方案；检查答案收下、任务往下走、对话留在历史里、复盘写出技能、模型只拿到规定的工具；包含一次长于 2 分钟的模型调用。
- **契约测试**：Go 与 Python 共用的样例（领取、简报、答案、填槽请求、选段）。
- 单元测试：规划发起方（假的 serve 与剪辑套接字）、剪辑服务的 root 对端与账号头。

## 关联

- 决定：[2026-11-20 剪辑规划交给 VNAS 的方式](../decisions/2026-11-20-montage-planning-as-a-vnas-session.md)
- [ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 决定 10（会话闸门）
- 验证脚本：开发机实测用的 spike 沙箱（`ai/assistant-spike/run.sh`），规划进程与四项检查写进系统测试
- 代码：`internal/montage/planning.go`、`internal/montageapi/assistant.go`、`internal/montageservice/assistant.go`、`ai/assistant/anas_montage_mcp.py`、`scripts/lib/assistant-units.sh`
