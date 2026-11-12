# Hermes 的固定提交怎么越过 gateway 运行时

状态：decided（2026-11-11，用户）
更新时间：2026-11-11

## 问题

AI 助手固定在 Hermes 2026-10-13 的提交 `0e427d78`。2026-11-10 试着前进到上游当天的 main（`8fd9f706`）：对话、确认卡片、操作桌面、照片、记忆、后台复盘与登录在新提交上都通过，定时任务不再运行（系统测试 567 项通过，3 项失败，都是定时任务；[研究](../research/hermes-agent-runtime.md#2026-11-10-升级到上游-main-的尝试)）。

原因是上游在 `0e427d78` 之后三天内合入了统一的 gateway 运行时，两处与 A-NAS 现在的做法冲突（[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 决定 3、8）：

- 代理任务只在常驻的 gateway 进程里执行。A-NAS 到点运行的 `hermes cron tick` 成了“无头”的一轮：没有就绪的 gateway 就跳过任务，不再自己执行。
- gateway 要求 Hermes 主目录归运行它的用户所有。A-NAS 的主目录归 root，用 ACL 授给成员，被判为不安全。

固定提交不前进，就拿不到上游之后的修复，差距每月增加上万次提交。要前进，得先定下定时任务怎么运行。

## 选项

- **固定提交留在 `0e427d78`**：什么都不用改。上游的修复与新功能都拿不到，以后升级更贵。
- **到点时在定时单元里运行 gateway**：唤醒服务照旧按任务表启动成员的定时单元，单元里启动 gateway，等到期的任务跑完再停掉。成员不在线时任务仍按时运行。要把 Hermes 主目录改为成员所有，或另想办法满足上游的检查，并重验沙箱：主目录里 root 所有的只读文件是不是仍然改不了，多出来的 gateway 进程开了哪些接口。
- **只在成员登录期间调度**：实例运行时由它自己调度，成员不在线时到期的任务等下次登录补跑一次（上游的 `cron.catch_up_missed`）。不需要另起进程；放弃“成员不在线也按时运行”（2026-10-20 的决定），实例还要以 Desktop 模式运行或常驻一个 gateway，前者会带上 A-NAS 关不掉的桌面工具。
- **到点时启动实例，经它的接口运行任务**：唤醒服务启动成员的实例，登录后调用“立即运行”的接口。任务在实例里运行，沿用它的沙箱；要让 root 的唤醒服务持有登录实例的能力，而且这个接口在新提交上是否仍在进程内执行还没有验证。

## 决定

2026-11-11，用户：到点时在定时单元里运行 gateway；Hermes 主目录照上游的要求归成员本人所有，不再归 root。

进程的身份没有变：实例与定时单元一直以成员本人的 UID 在沙箱里运行，没有特权。变的只是磁盘上 `assistant/<账号>/hermes` 这一层目录的属主。

定之前在沙箱里验证过（[研究](../research/hermes-agent-runtime.md#2026-11-11-在沙箱里验证-gateway)），实现时照验证过的做法：

- `assistant/<账号>/hermes` 改为 `<账号>:<账号>`、0700、不带 ACL；上一层与 `files/` 不变。成员因此能对这一层 `chmod`、`setfacl`；上一层归 root、成员只能穿过，其他成员仍然进不来，只读绑定的文件仍然改不了。
- 定时单元里：启动 gateway，等它就绪，运行 `hermes cron tick`，等到没有任务在执行，再 `hermes gateway stop`。
- 定时单元与实例共用一个网络命名空间。分开时，成员在线而任务在运行的那段时间里，对话的改名与删除会失败。

代价：一轮定时任务期间，定时单元约占 410–610 MB，其中三个 MCP 服务器约 240 MB。

2026-11-11 已实现，固定提交前进到 `8fd9f706`（[研究](../research/hermes-agent-runtime.md#2026-11-11-实现)）：

- 布局：[`assistant-layout.sh`](../../ai/assistant-spike/assistant-layout.sh) 的 `member_home`；已装的试用由 `update` 改属主。
- 一轮定时任务：[`cron_round.py`](../../ai/assistant-spike/cron_round.py)。
- 共用的网络与通往模型的桥：[`anas-assistant-net@.service`](../../ai/assistant-spike/anas-assistant-net@.service)，实例、定时任务、闲时整理与接口桥都加入它。
- ADR 0024 的决定 3、8 已修订。

验证时还发现对话的改名与删除在新提交上要带写入凭据，产品服务一起改了（[`sessionwrite.go`](../../internal/assistantapi/sessionwrite.go)）。

## 关联

- [ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)、[AI 助手规格](../specs/ai-assistant.md#定时任务按时运行)
- [Hermes Agent 可行性研究](../research/hermes-agent-runtime.md#2026-11-10-升级到上游-main-的尝试)
- 唤醒服务 [`cron-waker.py`](../../ai/assistant-spike/cron-waker.py)、系统测试中的定时任务一节 [`system-test.sh`](../../ai/assistant-spike/system-test.sh)
