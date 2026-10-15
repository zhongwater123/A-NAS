# 0022：AI 助手以每个账号一个沙箱中的 Hermes Agent 运行

状态：proposed（2026-10-14 与用户讨论后起草；同日在开发机完成 spike，含 DeepSeek 真实模型测试与助手窗口原型；实验 NAS 上尚未验证）。2026-10-15 对照上游后用户决定：助手与 Hermes 之间改用官方 Desktop 所用的 `hermes serve`（REST 加 `/api/ws` 上的 JSON-RPC），写入确认改用 Hermes 的回合内审批；决定 4 已按 [#111](https://github.com/zhongwater123/A-NAS/issues/111) 的验证改写，决定 9 待 [#109](https://github.com/zhongwater123/A-NAS/issues/109)、[#110](https://github.com/zhongwater123/A-NAS/issues/110) 验证后改写。行为见 [AI 助手规格](../specs/ai-assistant.md)，外部事实见 [Hermes Agent 可行性研究](../research/hermes-agent-runtime.md)。

AI 助手需要记忆、技能、多模型与工具调用这些智能体能力，自己实现等于重写一个成熟框架；但 [Hermes Agent](https://github.com/NousResearch/hermes-agent) 是为单一主人设计的，默认带终端与文件的全部权限，而 A-NAS 有多名成员，文件只由内核 ACL 授权。我们决定：以 Hermes Agent 为运行时，每个账号运行一个独立实例，由 systemd 沙箱只给它看本人能读的几处位置，只让它自由写自己的助手目录；对个人空间与 Agent空间的更改先写入暂存区，只有账号本人在自己的会话中确认后才由文件代理执行。权限由内核与 A-NAS 强制，不依赖 Hermes 的配置或模型的自觉。

## 决定

1. **运行时**：固定 Hermes Agent 的一个提交，在开发机构建运行环境，按内容存放在 `/opt/a-nas` 并由 release 引用，设备上不运行 pip、不联网、不自更新（与 [ADR 0016](0016-ship-photo-ai-as-a-built-in-offline-capability.md) 相同）。与相册 AI 不同，运行环境自带 CPython 3.14（Hermes 的依赖只在 3.14 上安装，Debian 13 是 3.13），依赖按上游 `uv.lock` 以哈希固定，Hermes 本身以固定提交的源码可编辑安装（上游拒绝构建 wheel）。A-NAS 只经 API Server 与 MCP 对接 Hermes，不 import 它的内部代码；更换运行时不影响 A-NAS 侧的模型网关、MCP 工具与确认流程。
2. **每个账号一个实例**：模板单元 `anas-assistant@<账号>.service` 以该账号本人的 Linux 身份运行，附加组只有 `a-nas-users`（不带 `a-nas-admins`），所以内核 ACL 决定它能读什么，与文件工作进程和终端一致（[ADR 0008](0008-use-unified-linux-identities-and-filesystem-acls.md)）。同一 UID 带来的风险由沙箱封住：独立 PID 命名空间（`PrivatePIDs`）、禁止 ptrace 一类系统调用、私有 IPC 与 keyring、没有任何能力。PID 命名空间中的 1 号进程是 `tini`，由它回收孤儿进程。实例由每个账号的 Unix 套接字按需启动：套接字触发的 `systemd-socket-proxyd` 以 `JoinsNamespaceOf=` 进入实例的网络命名空间，转发到它的 API Server，闲置后退出并带走实例（`StopWhenUnneeded`）；只有产品服务能连接这些套接字，每个实例另有至少 16 个字符的 API 密钥。
3. **沙箱只挂三处**：本人的个人空间（只读）、共享空间（只读，含 Agent空间）与本人的助手目录（读写，唯一可写处）。助手目录分成两处挂载：Agent 的工作区 `/nas/助手`，以及工作区之外、文件工具写不到的 Hermes 状态目录；Hermes 启动时会加载代码或设置的 `config.yaml`、`.env`、`SOUL.md`、`hooks/`、`plugins/` 是 root 所有的只读文件，因为能写这些位置就等于能在实例中执行代码或改写每次的系统提示；两处空间的回收站在沙箱中各是一个只含本人文件夹的空 tmpfs，他人的回收站文件夹既读不到也看不见（看得见却读不了的文件夹会让 Hermes 的 ripgrep 搜索整体失败）；数据卷其余部分、快照、系统盘上的状态都不在沙箱中。实例没有网络（`PrivateNetwork`），唯一出口是经绑定进来的 Unix 套接字回到产品服务的模型网关与 MCP 端点（实例内由 `socat` 从回环地址转入）；产品服务按请求来自哪个实例的套接字认定账号，Hermes 不持有任何 A-NAS 令牌或模型密钥。
4. **写入**：Hermes 自带的文件工具直接写助手目录。对个人空间或 `Agent空间/<本人>` 的新建、修改、移动、改名与删除，先写到助手目录内的暂存区，再调用 MCP 工具“提交更改”。managed scope 把 A-NAS 的 MCP 服务器设为 `trust: untrusted`，这个工具没有 `readOnlyHint`，所以 Hermes 每次调用它之前都在这一回合内请求审批，与 Hermes Desktop 的审批相同（[研究](../research/hermes-agent-runtime.md#回合内审批1112026-10-15开发机)）。产品服务按回合中这次调用的参数生成确认卡片，卡片只属于发起它的对话，成为一份[待确认更改](../../CONTEXT.md)；成员拒绝、在 managed scope 固定的 `approvals.timeout` 内没有回答，或停止这一轮，都按拒绝处理，什么都不改变，也不跨回合保留；助手在同一回合中得到结果。只放行“确认一次”与“拒绝”，不提供 Hermes 的“本会话”与“始终”。Hermes 转来的同意不足以执行：成员在本人的 Web 会话中点确认时，产品服务先登记一次性授权（账号、更改、参数摘要），再应答 Hermes；“提交更改”到达产品服务时，只执行本人授权过、参数相同、没用过且没过期的更改，写入的是生成卡片时复制的暂存内容，目标在此后变化即拒绝。文件代理以本人身份执行，删除与被覆盖的旧版本进入本人的回收站；执行进程由 Landlock 限制为只能写这两处。共享空间的其他位置无论确认与否都不能写。
5. **Agent空间**：`共享空间/Agent空间` 由 Host Agent 预建并修复，全体成员只读；每个账号在其中有一个只授本人 `rwx` 的子目录，与 `.a-nas-trash/<username>` 的 ACL 模式相同（[存储与文件架构](../architecture/storage-and-files.md)）。
6. **相册**：照片不是普通文件，助手经 MCP 只读工具访问相册服务；`photos.Principal` 增加助手标记，相册服务据此只允许读取，去掉管理员身份与查看授权，范围为本人的私有图库与共享图库（[ADR 0011](0011-run-the-photo-library-as-a-dedicated-service-identity.md)）。
7. **模型**：产品服务提供 OpenAI 兼容的模型网关并持有密钥。开发期转发到云端 API，仅用于开发测试；之后换成设备上的本地多模态模型，Hermes 一侧的配置不变。
8. **固定策略与关闭的能力**：模型地址、启用的工具与各项关闭写在 root 所有、沙箱内只读的 Hermes managed scope（`/etc/hermes`）中，Agent 自己的配置无法覆盖。只启用 `file`、`memory`、`session_search`、`skills`、`todo` 与 A-NAS 的 MCP 工具，并关闭 `tool_search`，让 A-NAS 的工具直接出现在模型的工具列表中；关闭 `terminal`、`code_execution`、`browser`、`web`、`cronjob` 与消息平台，以及 Nous Tool Gateway、Honcho、`smart` 审批的辅助模型、更新检查、模型目录拉取、运行时装包与语言服务器。沙箱使这些能力即使被误开也越不出上述三处，以后重新启用某项另行决定。
9. **界面**：AI 助手窗口是 A-NAS 自己的桌面应用，经产品服务的 `/api/v1/assistant` 使用 Hermes API Server 的会话接口（列出、新建、改名、删除、历史消息，与带工具事件的流式回复），确认卡片也在这里。对话区复用 Hermes Desktop 的聊天层，不另写：同样用 assistant-ui 渲染，并把 Desktop 的会话记录模型（`src/lib/chat-messages`，把 Hermes 存储的消息行折叠成工具调用、推理与文本组成的聊天消息）按固定提交复制进 [`web/src/assistant/hermes`](../../web/src/assistant/hermes/README.md)，依赖版本与 Desktop 的锁文件一致。会话列表、确认卡片、样式与连接层是 A-NAS 的；确认卡片来自决定 4 的回合内审批。不嵌入 Hermes 自带的网页控制台，也不嵌入整个 Hermes Desktop。

## 后果

- **隐私（暂不处理，只在此注明）**：开发期的云端 API 会收到助手读取的文件内容与照片缩略图，这与相册界面“照片只在这台设备上分析，AI 不联网”的说明不一致（[ADR 0016](0016-ship-photo-ai-as-a-built-in-offline-capability.md)）。因此云端 API 只用于开发测试；本地多模态模型接入前，不在保存真实家庭数据的设备上为成员开启助手。
- Hermes 对个人空间里的文件直接 `patch` 会得到只读错误；系统随 Hermes 附带两个技能并在每个对话中自动加载：`anas-files` 说明目录、暂存区与“提交更改”，`anas-member` 由 Host Agent 按账号生成，写明这位成员的名字、发布位置与回收站。没有它们，真实模型要花十几轮探索才找到个人空间；有了它们，修改与删除 3–4 轮完成。Hermes 只从成员自己的 `config.yaml` 读取技能目录（不读 managed scope），所以这个文件由 root 写入、对成员只读；它不是安全边界，丢失只会让技能不加载。读取、搜索与编辑仍全部使用 Hermes 自带的工具。
- 助手目录是数据卷上每个账号一个 Btrfs 子卷（`assistant/<账号>`），以 simple quota（内核 6.7 起）封顶；文件代理因此可以从暂存区读取内容。数据卷离线时助手不可用，不回退到系统盘。在已有数据与快照的数据卷上开启 simple quota 前，须先在实验 NAS 上验证它对快照与删除的影响。
- 开发机上每个实例常驻约 200–270 MB（假模型）；用真实模型（DeepSeek）多轮对话后约 375 MB，峰值 403–442 MB（含 MCP 子进程）；冷启动 5.5–11 秒；闲置退出让不用助手的成员不占内存。运行环境解开约 425 MB（压缩包 132 MB），设备另需 Debian 包 `ripgrep`、`socat` 与 `tini`。本地多模态模型能否在 8 GB 设备上运行另行评估。
- Hermes 每开始一个新对话会多请求一次模型生成标题，正式配置须写明模型的上下文长度，否则它会反复探测；系统提示约 13,000 字符。
- 实例闲置退出后 Hermes 的定时任务不会触发，所以第一版关闭 `cronjob`；以后由 A-NAS 的调度唤醒实例。
- Hermes 的 `memory` 与 `skill_manage` 会把内容留到以后的对话，这是诱导文字持续生效的唯一途径；边界测试中两个模型都没有把诱导写进去，但成员仍应能查看和清除助手记住的内容与它创建的技能。
- Hermes 上游没有版本号，升级等同于一次依赖升级，需要重跑沙箱探针与契约测试。复制进前端的 Desktop 代码随同一提交更新：[`vendor-hermes-ui.py`](../../ai/assistant-spike/vendor-hermes-ui.py) 重新复制并改写导入，会牵连 Desktop 其余部分的四个模块由 A-NAS 的小替身代替；`@assistant-ui/tap` 必须与 Desktop 锁定的版本一致（现为 0.9.8），较新的 0.9.23 会让对话区陷入无限更新。
- 确认只在回合进行中有效：成员不在时，助手提交的更改在 `approvals.timeout` 后作废，要成员回来再说一次。换来的是卡片不会出现在别的对话中，助手当场知道结果，产品服务也不保存跨回合的待确认状态。Hermes 的审批请求不带工具参数，产品服务靠同一回合中“提交更改”的 `tool.started` 配对，这依赖 Hermes 串行执行 MCP 调用，所以 A-NAS 的 MCP 服务器不声明并行调用。
- 一个回合只在它的流打开时运行：关闭流（成员点停止或离开这个对话）后 Hermes 中断该回合，已完成的工具调用留在记录中。要让回合在后台继续、重新打开时接上，须改用 API Server 的 `/v1/runs`（状态、事件、停止）。
- 删除账号时须同时删除其助手目录与 `Agent空间/<账号>`，随账号删除（[#26](https://github.com/zhongwater123/A-NAS/issues/26)）实现。
- Host Agent 新增类型化操作：为账号建立与删除助手目录、Agent空间子目录与实例套接字。

## 未选择

- **装进应用中心现有的 Docker**：安装策略不允许挂载个人空间；即使允许，Docker 默认给容器 `CAP_DAC_OVERRIDE`，Hermes 官方容器加固也保留它，容器内的 root 会无视 ACL。
- **每个账号一个独立 Docker 容器**：隔离强度与 systemd 沙箱相当，但依赖由管理员另装的 Docker、要扩展容器代理、镜像要另走离线交付，现有系统测试也不能直接证明它的边界。
- **直接使用 Hermes 自带的网页控制台（`hermes dashboard`）作为助手页面**：它是整台机器的管理台，配置、密钥、模型、插件等页面正是要固定住的部分，且可以切换机器上所有 profile；对话页是在伪终端里运行的终端界面，需要 Node.js 并为每个对话另起进程；确认卡片只能由成员本人在 A-NAS 会话中发出，控制台里做不到。
- **直接使用 Hermes Desktop**：它是 Electron 应用，经 WebSocket 上的 JSON-RPC（`/api/ws`）连接 Hermes 的网关，而不是沙箱只开放的 API Server；它还带设置、模型、Profiles 与插件等页面。A-NAS 只复用它的聊天层（决定 9）。
- **一个进程服务多个 profile**：官方明说 profile 不是沙箱，成员之间的隔离只能依赖 Hermes 的代码。
- **以独立 UID 运行、所有读写都经 A-NAS 自写的 MCP 工具**：不需要成员 UID，但要重写读取、搜索与编辑工具，重复造轮子；读取范围已由挂载与 ACL 限定，同 UID 的风险已由沙箱封住。
- **overlayfs 让 Hermes 原地修改个人空间**：对工具最透明，但成员同时在使用的个人空间就是下层，内核文档把下层在挂载期间变化定义为未定义行为；挂载与清理暂存层还需要特权与卸载。
- **持久保存、过后在汇总中确认的待确认更改**（2026-10-14 原型的做法）：卡片要靠参数对应到提交它的对话，原型中曾“穿透”到其他对话；助手提交后回合就结束，不知道结果；产品服务要保存跨回合的状态并另定过期规则。
- **由 MCP 服务器在调用中发起 elicitation 来确认**：卡片文字与结果都由 A-NAS 决定，能直接对上更改，但等待发生在 Hermes 对该服务器的 RPC 锁之内，成员考虑期间同一实例所有对话的 A-NAS 工具调用都要排队。
- **写个人空间或 Agent空间不需要确认**：共享空间任何成员都能写，可以放入诱导助手的文字；不确认就可能被诱导把私人文件写到全家可读的位置，或改坏本人的文件。

## 关联

- [AI 助手规格](../specs/ai-assistant.md)、[Hermes Agent 可行性研究](../research/hermes-agent-runtime.md)
- Spike：[`ai/assistant-spike`](../../ai/assistant-spike/run.sh)（沙箱单元、managed scope 与探针）
- 原型：[`internal/assistantapi`](../../internal/assistantapi/handler.go)、[`web/src/assistant`](../../web/src/assistant/AssistantPanel.tsx)、[OpenAPI](../../api/openapi.yaml)
- [ADR 0008](0008-use-unified-linux-identities-and-filesystem-acls.md)、[ADR 0011](0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[ADR 0016](0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- [架构不变量](../architecture/OVERVIEW.md#不变量)、[存储与文件架构](../architecture/storage-and-files.md)
