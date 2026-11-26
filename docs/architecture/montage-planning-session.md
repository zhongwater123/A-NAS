# 剪辑中心的规划会话

状态：accepted（用户 2026-11-20 [决定](../decisions/2026-11-20-montage-planning-as-a-vnas-session.md)），已实现（[#608](https://github.com/zhongwater123/A-NAS/pull/608)），2026-11-20 部署到实验 NAS 并完整出片一次
更新时间：2026-11-25

剪辑任务走到规划时，由成员自己的 VNAS 在后台做两步规划（大纲、挑节拍），两个方案。本文是交给 VNAS 的那一段：谁发起、在哪个进程里跑、云端看到什么、怎样等它结束、怎样重试、VNAS 怎样学习。剪辑中心其余部分（任务、步骤、简报与答案的校验、填槽、渲染、存回相册）不变，见剪辑中心的[实施方案](montage-center.md)。

它取代原来借成员定时任务单元跑一轮的做法（`montage_round.py`、规划令牌与领取回执、定时任务里的规划任务）。原因见[决定记录](../decisions/2026-11-20-montage-planning-as-a-vnas-session.md)与[调查](../investigations/2026-11-19-montage-job-stalls-on-the-experimental-nas.md)。

## 不变的规则

- **云端看不到画面**：规划时模型只拿到剪辑工具给的文字与结构化数据（本地 Qwen 写的卡片描述，号码已遮掉；节拍；音乐段落；成员的要求），没有图片，也没有能取到原片的文件路径。规划进程里没有看图、看视频、读文件、终端、相册与桌面的工具，提示词改变不了这一点。
- **VNAS 是成员自己的**：规划在成员的沙箱里、以成员的身份、带着成员的记忆与技能运行；规划对话留在成员的 VNAS 历史里。
- **授权**：只有成员在场时登记过规划授权（7 天、5 轮，文件代理写在 Host Agent 自己的状态目录里），后台才为他发起规划。剪辑服务拿不到任何成员实例的凭据。
- **只有成员能改变结果时才停下等成员**（2026-11-25 用户[决定](../decisions/2026-11-25-montage-planning-retries.md)，取代 #514 的“模型可能已被调用就不再自己重来”）：规划没有完成是临时故障或模型出错，系统 1 分钟后自动重来，同一个任务对同样的输入最多 3 轮；仍没有完成，任务以“失败”结束（`plan_failed`），桌面提示写明原因，成员一键重新规划。

## 组成

### 1. 规划进程：`anas-assistant-plan@<账号>`

成员平时的 VNAS 是 `anas-assistant@<账号>`（`hermes serve`）。规划进程是同一个沙箱里的第二个 `hermes serve`：

- 由 `scripts/lib/assistant-units.sh` 从 `anas-assistant@.service` 生成，与定时任务、整理单元同法：同样的挂载（同它们一样不绑定伙伴的套接字，没有窗口）、同一个 Hermes 主目录（记忆、技能、对话记录共用）、同一个私有网络与模型桥（`JoinsNamespaceOf=anas-assistant-net@%i.service`），端口换成 9120（`ANAS_ASSISTANT_CHANNEL=plan`，`assistant-start.sh`）。生成时核对每处替换都生效，模板变了在打包时失败。
- **工具在进程级固定**：`HERMES_TUI_TOOLSETS=videocut,memory,skills`。`hermes serve` 不能按单次对话收窄工具，工具组整个进程一致，所以只能用一个单独的进程。实测模型只拿到 `memory`、`skill_manage`、`skill_view`、`skills_list` 与剪辑的 4 个工具。
- **自己的托管策略**：`managed-plan/` = 普通托管策略加上剪辑的 MCP 服务器（`videocut-mcp.yaml`），把 `skills.creation_nudge_interval` 设为 1，每次规划结束都复盘。Host Agent 按它写 `members/<账号>/config-plan.yaml`，单元绑定成 Hermes 的 `config.yaml`。原来的 `managed-cron/` 与 `config-cron.yaml` 只为规划而有，一并去掉，定时任务单元回到普通策略。
- **只有它绑定剪辑套接字**（`/run/a-nas-videocut-assistant/assistant.sock` → 沙箱内 `/run/anas-assistant-videocut/videocut.sock`）；成员平时的 VNAS、定时任务与整理单元都不绑定。
- **自己的入口**：`anas-assistant-planapi@.socket`（`/run/a-nas-assistant-plan/<账号>.sock`，root 0600）与 `anas-assistant-planapi@.service`（`systemd-socket-proxyd --exit-idle-time=45s 127.0.0.1:9120`），由 `anas-assistant-api@` 的两个单元生成。连接来了才起进程，没有连接 45 秒后退出（`StopWhenUnneeded=yes`）。Host Agent 与 `anas-assistant-api@.socket` 一样为每个有 VNAS 的成员启用这个套接字；账号停用时连同 `anas-assistant-plan@` 一起屏蔽，删掉密钥时停下。
- 与成员平时的 VNAS 可以同时运行；两个进程同时写同一主目录的记忆与技能，实测没有丢失。

### 2. 规划发起方：`anas-videocut-planner.service`

root 的长驻服务（`ai/assistant/videocut-planner.py`），取代唤醒服务里的规划部分（`cron-waker.py` 只留成员的定时任务）。用 AI 助手运行环境的 Python（有 `websockets`）；与模型网关、唤醒服务一样只在保存了密钥时运行，Host Agent 随密钥启停。每 15 秒看一次。

1. **找要规划的**：剪辑服务的记号（`/run/a-nas-videocut/plans/<账号>`，只读）、规划授权（只认 root 所有、他人不可写的文件）、账号在用且有 VNAS。全机同一时间只规划一个。
2. **领取一轮**：在剪辑服务的助手套接字上 `POST /v1/rounds/claim`。规划发起方以 root 连接，请求带 `X-A-NAS-Account: <账号>`；剪辑服务只对 UID 0 的对端认这个头（套接字目录只有 root 进得去，沙箱里绑定的对端都是成员 UID），其余对端照旧按 UID 认成员。
3. **开对话**：先确认模型网关应答（`GET /v1/models`，不花 token），连 `/run/a-nas-assistant-plan/<账号>.sock`，用 Hermes 自己的登录（`/auth/password-login`，密码是 Host Agent 写的实例密钥，`/var/lib/a-nas-host/assistant/tokens/<账号>.env`，只认 root 所有的文件），再走 `/api/ws` 的 JSON-RPC：`client.capabilities {server_requests: true}`、`session.create`、`session.title`（“剪辑规划 · <项目名>”，项目名随领取的回答带来）。然后报 `POST /v1/rounds/<轮次>/started`，`runRef` 是对话的存储编号（`stored_session_id`）；记下开始后才 `prompt.submit`（固定模板 `ai/anas_videocut/prompts/<模板>.md`，只填检查过的轮次、任务编号、token 与目标秒数），没记下开始（这一轮已作废或任务取消了）就不提交，模型不会被调用。
4. **等它结束**：一直连着，收事件直到这次提问的 `message.complete`，至多 60 分钟（超时先 `session.interrupt`）；Hermes 发来的审批请求一律答 `{"choice": "deny"}`（实测不应答也是拒绝，这里明确拒绝），别的服务器请求答“不处理”。报完结果后再连着，等后台复盘的 `review.summary`，至多 150 秒（复盘在断开约 20 秒后会被回收）。连接断了、进程崩溃或超时，都算这一轮结束，按结果报告。
5. **报结果**：`POST /v1/rounds/<轮次>/end`，`result` 是 `complete`、`error`（附错误原文）、`timeout` 或 `interrupted`；提示词没交进去（模型网关不应答、登录不了、对话建不起来）报 `abandon`，`failure` 写明是连不上（`unreachable`）还是出错（`error`）。
6. **轮数**：规划发起方自己数它发起的对话，按授权（账号与签发时间）记在自己的状态目录（`/var/lib/anas-videocut-planner/state.json`），不再要成员目录里的回执。提示词交进去之前先记一轮，进程中途死掉也不会少记；模型没调用过剪辑工具、对话也没有完成的，退回这一轮。

### 3. 剪辑中心

- 一轮就是一次对话：`rounds.run_ref` 记对话编号，成员的 VNAS 历史里能找到它。
- 剪辑工具与答案校验不变（`/v1/tools/*`，按 `jobId` 与 token）。
- 一轮的时限按[原来的规则](../../internal/videocut/app/planning.go)：领取后 5 分钟没开始就作废；开始后距最后一次调用工具 20 分钟、或开始后 65 分钟仍没结束，就结束。

### 4. 重试

按原因处理，替代唤醒服务按次数翻倍到 1 小时的退避：

| 结果 | 处理 |
|---|---|
| 剪辑中心没有要规划的任务 | 不算失败，不退避，删记号 |
| 暂时连不上（规划进程起不来、登录时连不上、模型网关不通、领取后 5 分钟没开始） | 不计轮数；2、4、8 分钟后再试，之后每 15 分钟 |
| 剪辑套接字不通（领取不了） | 发起方自己等 2、4、8、15 分钟再领；有成员新登记了授权就立即再试 |
| 规划进程或发起方出错（异常、协议不符、登录被拒；或对话开始后、模型调用剪辑工具前就出错或超时） | 不计授权的轮数；任务算试过一轮，1 分钟后自动重来 |
| 模型调用过剪辑工具、没交完（含对话中断、超时、只交回一部分） | 计授权的轮数；任务算试过一轮，1 分钟后自动重来，已收下的大纲与方案保留 |
| 答案连续 3 次不合格 | 剪辑中心在第 3 次结束这一轮，告诉模型“这一轮到此为止”；计授权的轮数，任务算试过一轮，1 分钟后自动重来，新的一轮重新计数 |
| 同一个任务对同样的输入试满 3 轮仍没有完成 | 任务以“失败”结束（`failed`，原因 `plan_failed`），错误写“规划试了 3 轮都没有完成：<最后一轮怎样结束>”；桌面提示“没有剪成”；成员点“重新规划”从停下的地方接着规划，又有 3 轮 |
| 两个方案都交了 | 计授权的轮数 |

这里有两种轮数：**授权的轮数**由规划发起方按成员的授权数（7 天 5 轮，几个任务共用），保护成员不在场时调用云端模型的次数；**任务试过的轮数**由剪辑中心按任务、规划轮次与输入数（`maxPlanRounds`，3 轮），决定什么时候不再自动重来。2026-11-25 之前停在 `vnas_unknown`、`plan_invalid`、`plan_error` 的任务，成员点“重新规划”照旧接着规划；之后不再有任务停在这三个原因。

试过的轮数由剪辑中心按任务的轮次算（`rounds` 的状态与 `failure`，`internal/videocut/app/planning.go` 的 `awaitPlan`）：结束了的一轮、或开始前规划出错的一轮算试过，连不上的不算。重试的等待（`retryWait`）只看成员最近一次登记授权与剪辑服务这次启动之后结束的轮次：成员一做会登记授权的操作（开始、重新规划、去掉素材、重试分析、重剪），或装了新版本（服务重启），等待就清掉，任务一分钟内再要一轮；试过的轮数不因此清零。任务页写明在等什么、几分钟后再试：“VNAS 正在规划”“VNAS 暂时连不上，N 分钟后再试”“规划没有完成，1 分钟后自动重来”。

## 学习

- **读**：规划对话照常带着成员的记忆与技能进系统提示。
- **写**：规划进程里的后台复盘与平时对话一样运行；每次规划结束都复盘（`creation_nudge_interval: 1`），被退回的原因、摸索出的做法写成技能，下次规划直接用。实测复盘在受限进程里写出了技能。
- **成员的反馈**：规划对话留在成员的 VNAS 历史里，成员打开它说“开场太快了”，用的是平时的 VNAS（工具齐全，因为是成员本人在说），复盘照常记下。之后再加“剪辑偏好记录”（挑了哪一版、重新规划时的新要求），作为下一步。
- **风险**：卡片描述里可能有画面里的外来文字，复盘可能把它写进记忆或技能（用户已接受，同 2026-10-15 对普通对话的决定），成员在“记忆”页查看与删除。

## 测试

- **规划链路的系统测试**（带真实运行环境时，`ai/assistant-spike/system-test.sh`）：真实的 Hermes、规划进程与入口、剪辑服务、规划发起方；脚本化模型（`fake_model.py` 的规划模式：读 brief，交 Schema 允许的最小答案）扮演 VNAS 调四个剪辑工具、交两个方案；检查答案收下、任务越过规划步骤、对话带标题留在历史里、复盘写出技能、模型只拿到规定的工具；第一次模型调用长于 2 分钟。规划之前的步骤要系统测试没装的模型（相册的视频分析、allin1 的配乐分析），所以测试在剪辑服务停着时给任务写进一份 4 张卡片的目录、给歌写进分析（`montage-plan-seed.py`），之后的键与 brief 都由任务自己算，brief 的数据与 Schema 由计算 Worker 的替身写；产品没有为测试开任何入口。用真实内容（`ANAS_SYSTEM_TEST_AI=real`）时这一节跳过：真实的计算 Worker 不认这些卡片。
- **契约测试**：Go 与 Python 共用的样例（领取、简报、答案、填槽请求、选段）。还没有做。
- 单元测试：规划发起方（`ai/tests/test_videocut_planner.py`，假的剪辑套接字、登录与 JSON-RPC）、剪辑服务的 root 对端与账号头（`internal/videocut/protocol/socket_test.go`）、重试与等待说明（`internal/videocut/app/planning_test.go`），规划发起方认得剪辑中心生成的编号（`internal/videocut/app/planner_ids_test.go`）。

## 关联

- 决定：[2026-11-20 剪辑规划交给 VNAS 的方式](../decisions/2026-11-20-montage-planning-as-a-vnas-session.md)
- [ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 决定 10（会话闸门）
- 验证脚本：开发机实测用的 spike 沙箱（`ai/assistant-spike/run.sh`），规划进程与四项检查写进系统测试
- 代码：`internal/videocut/app/planning.go`、`internal/videocut/protocol/assistant.go`、`internal/videocut/protocol/socket.go`、`ai/assistant/videocut-planner.py`、`ai/assistant/anas_videocut_mcp.py`、`scripts/lib/assistant-units.sh`、`internal/hostops/linux/assistant.go`、`deploy/systemd/system/anas-videocut-planner.service`
