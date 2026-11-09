# 虚拟伙伴（大肥鱼）

状态：draft（V1 实现中，[#404](https://github.com/zhongwater123/A-NAS/issues/404)；VNAS 的控制见 [#412](https://github.com/zhongwater123/A-NAS/pull/412)、[#414](../decisions/2026-11-06-companion-v1.md)）
更新时间：2026-11-09

## 目标

已登录的 Web 桌面右下角常驻一个虚拟伙伴“大肥鱼”，就是 dsh-pet 的宠物卡片：渲染、动画链、走动、拖动、抛出、气泡、睡觉与锁都用 dsh-pet 的代码。它随这台设备上发起的 VNAS 回合与待确认卡片切换动作，定时任务出了新结果时挥手。

VNAS 能控制它（[#414](../decisions/2026-11-06-companion-v1.md) 记录的 2026-11-06 决定）：播放动作、说话气泡、移动与抛出、睡觉与叫醒，只在发起这段对话的设备上。所以在控制它的设备上，伙伴可以说出这段对话的内容；别处来的内容，例如定时任务的提示，在全家共用的宿主机屏幕（本地控制台）上仍只显示状态。

## 非目标

- V1 不做电脑端桌宠（Electron）、聊天、主动邀请，也不带 dsh-pet 的余额、语音、菜单与得分弹窗；这些属于 V2 或 VNAS（[#404](https://github.com/zhongwater123/A-NAS/issues/404) 第 3、4 节）。待办与专注计时待定，见[决定记录](../decisions/2026-11-06-companion-v1-no-todo-focus.md)。
- 伙伴没有自己的数据、数据库或公开接口：VNAS 的命令经产品服务已有的 VNAS 窗口连接送达。伙伴不连接成员的 Hermes 实例，也不因自己而启动实例。
- 定时任务、委派的子任务与 Hermes 自己开启的回合不能让伙伴做动作或说话。
- 不提交动画素材，也不把素材嵌入产品二进制。
- 不允许商用：形象为 CC BY-NC-SA 4.0 的社区二创，A-NAS 商用前必须更换形象。

## 场景

### 未安装动画时不显示

- Given：NAS 没有安装伙伴动画（`/companion/animations` 列表为空）。
- When：成员登录 Web 桌面。
- Then：桌面不显示伙伴，也不下载伙伴的代码与素材；其余功能不受影响。VNAS 让伙伴做事时得知这个桌面上还没有伙伴。

### 首屏之后出现

- Given：动画已按[安装伙伴动画](../runbooks/install-companion-animations.md)装好。
- When：桌面首屏绘制完成、浏览器空闲（最迟 3 秒）。
- Then：页面读取动画清单（已安装的动画与池 ID），再加载伙伴代码；伙伴代码不计入首屏 JS。大肥鱼出现在右下角 Dock 上方，或成员上次在这台设备上放下的位置；位于桌面图标之上、窗口与系统栏之下。

### 空闲时随机动作

- Given：这台设备上没有进行中的 VNAS 回合，也没有待确认卡片。
- Then：按 dsh-pet 的权重在待机、转身、走路与分类动作之间随机切换，只播放已安装的动画。走路按 dsh-pet 的走位配置朝当前朝向横向移动，走完记在本设备；前方空间不够或没装走路动画时改播一个分类动作。

### 跟随这台设备上的 VNAS 回合

- Given：成员在这台设备的 VNAS 窗口里发出一句话。
- When：回合开始或推理、调用工具、工具返回、回答完成、出错、被停止，或有卡片等成员确认。
- Then：伙伴分别切到思考、工作、整理、完成、出错、回到空闲、等待的动画，气泡说一句 dsh-pet 为这个阶段配的固定台词（例如“需要你确认一下呢”），不说回答的内容。几段对话同时进行时按 dsh-pet 的“等待 > 出错 > 工作 > 思考 > 整理 > 完成”取最要紧的一个，5 秒内的“工作 → 整理”合并为一次。进行中的阶段循环播放；完成与出错播放一次，保留 60 秒后清掉。某个阶段的动画没有安装时，伙伴不为它切换，照常做原来的动作；伙伴睡着或被锁时也不跟随。
- 只在另一台设备上发起、这台设备只是跟着显示的回合，不让这台设备的伙伴动。

### 窗口断线或关闭时回到空闲

- When：VNAS 窗口断线或关闭。
- Then：伙伴忘掉这个窗口的回合与待确认卡片，回到空闲动作；VNAS 命令在这个窗口拿到的锁随即释放，还在等回答的命令由产品服务结算为已取消。

### 让伙伴做动作、说话

- Given：成员在这台设备的 VNAS 窗口里说“让大肥鱼挥挥手”“让它把刚才的结论说一遍”，这个窗口发出了这一回合的提示。
- When：VNAS 调用伙伴工具：`anim`（播放一段已安装的动画）、`broadcast` 与 `bubble_clear`（说话气泡、收起气泡）、`position` 与 `throw`（移动、抛出）、`sleep` 与 `wake`、`lock` 与 `lock_release`。
- Then：不出确认卡片，命令只送到这个窗口，由这台设备上的伙伴照 dsh-pet 的规则执行；VNAS 得到如实的结果：`anim` 等动画播完（最多 10 秒）才回答，其余写入后立即回答。气泡可以是这段对话的内容，以纯文本显示；在宿主机上进行的对话，内容就显示在宿主机屏幕上。
- 宿主机屏幕上的伙伴只由在宿主机上进行的 VNAS 对话控制；成员在别的设备上打开同一段对话，不能让宿主机屏幕上的伙伴动或说话，反之亦然。

### 定时任务不能让伙伴做事

- Given：成员的定时任务在运行（到点运行，或在“定时任务”里点了立即运行）。
- When：任务调用伙伴工具。
- Then：任务的调用不让伙伴做事，任务得到“没有在用的桌面”（`no_desktop`），回答里如实说明；任务出了结果时伙伴照旧挥手（下一场景）。Hermes 自己开启的回合（后台任务后的唤醒、自动继续）也一样。
- 立即运行的任务在成员的实例中运行，调用能到达产品服务，因为配对不到对话里的 `tool.start` 才得到 `no_desktop`。这时成员某段对话里恰好有一条工具与参数完全相同、还没配上的调用，任务的调用就会配上它：伙伴照这段对话自己要求的内容做，这段对话的那条调用反而得到 `no_desktop`。屏幕上因此只会出现对话自己要求的内容，不会出现定时任务的。
- 委派的子任务调用伙伴工具时，它自己的调用没有 `tool.start`，同样得到 `no_desktop`。子任务的伙伴工具还在运行时（最多算 130 秒），这个成员其他能配上的伙伴调用回答“正忙”（`busy`），以免子任务的调用被误配到某段对话。子任务的调用可能先于说明它的事件到达，所以成员有子任务在运行时，配上的调用等满 1.5 秒才下发。成员停止回合时，它的子任务一并停止，不再算数。

### 太频繁时回答正忙

- When：一个成员的 VNAS 一分钟内发出超过 20 条伙伴命令，其中 `broadcast` 或 `throw` 各超过 6 条；或同样的调用同时出现在这个成员的两段对话中，分不清是谁的。
- Then：多出的命令不执行，VNAS 得到“正忙”（`busy`，频率超限时带 `rate-limited`）。共享文件里的诱导文字因此一分钟内最多让伙伴说 6 次话、抛 6 次。
- 一个成员同时在伙伴套接字上等待的调用最多 4 条（Hermes 一次只调一条，其余来自沙箱里的终端之类），多出的立即回答 `busy`（`rate-limited`），不查账号、不等配对。

### 锁

- When：VNAS 用 `lock` 锁住伙伴（默认 2 分钟，最多 5 分钟，可以续期）。
- Then：随机动作、走动与工作阶段的切换停住，伙伴醒来；此后不带这把锁的 `anim`、`broadcast`、`bubble_clear`、`position`、`throw` 被拒（`lock-required`，带锁的到期时间），锁着时不能睡觉（`locked`）。回合结束时产品服务释放这一回合拿到的锁，别的回合拿到的锁不动；成员抓起伙伴拖动时锁也随即释放。

### 睡觉与叫醒

- When：VNAS 用 `sleep` 让伙伴睡觉。
- Then：伙伴播放 dsh-pet 的入睡、睡眠与醒来动画，睡着时不跟随工作阶段；这时 `anim`、`broadcast`、`bubble_clear` 被拒（`sleeping`），VNAS 要先 `wake`。成员点一下睡着的伙伴、或 VNAS 拿锁，伙伴都会醒来。

### 页面在后台或屏保盖住时回答暂停

- Given：VNAS 窗口所在的页面被隐藏，或本地控制台的屏保、黑屏正盖着桌面。
- When：VNAS 发来 `anim`、`broadcast`、`bubble_clear`、`position` 或 `throw`。
- Then：命令不执行，也不唤醒伙伴或撤下屏保，VNAS 得到“暂停”（`rejected/paused`）。页面隐藏时，`sleep`、`wake` 与锁照常记下，页面重新可见时伙伴按它们的状态显示。
- 屏保或黑屏盖着而页面没有隐藏时，`sleep`、`wake` 与锁照常执行，不撤下屏保。其中 `sleep`、`wake` 以及叫醒睡着的伙伴的 `lock` 只先记下睡或醒，不唤醒闲置停下的伙伴；屏保撤下、有人操作后，伙伴按最后一次记下的睡或醒动起来。

### 闲置与隐藏时停下

- When：成员 60 秒没有键盘、鼠标、滚轮或触摸输入，或页面被隐藏。
- Then：伙伴停在当前帧，不再解码视频，随机动作、走路与飞行也停下，不留逐帧循环；走到一半就停在原处并记住位置，飞到一半就悬在原处。本地控制台的屏保（180 秒）出现前伙伴已停下。下一次输入或页面重新可见时恢复，飞行从停下的位置按原速度继续。
- VNAS 的工作阶段变化不唤醒闲置停下的伙伴：阶段照常记下，成员回来时伙伴直接显示当时的阶段。VNAS 的命令会唤醒它，并算作一次输入，因为成员可能正在读这一回合的回答；页面隐藏时不唤醒，屏保盖住时动作、说话、移动与抛出不唤醒，睡觉、叫醒与锁的例外见上一场景。

### 定时任务出了新结果时挥手

- When：成员的 `assistant_task` 通知列表中出现新的通知编号（同一个任务的下一次结果换一个编号，所以按编号判断，不按条数），且伙伴空闲：没有工作阶段、没睡、没被锁、没有停下。
- Then：伙伴挥手一次（没装挥手动画时播一段点击回应）；结果的内容只在 VNAS 中查看。页面第一次读到的通知列表只作基准，不挥手。

### 宿主机屏幕上别处来的内容只显示状态

- Given：页面是本地控制台（`?local-console=1`），屏幕全家共用。
- When：成员的定时任务（含提醒）有了新结果。
- Then：伙伴照常挥手；桌面提示只写“VNAS 定时任务 · 时间 · 有新结果”，失败时加“没有完成”，不写任务名与回答的开头。内容只在成员自己打开 VNAS 时出现：点“查看结果”在 VNAS 中打开这次运行。个人设备上的提示不变。

### 点击与拖动

- When：成员点击伙伴（或用键盘按它），或按住拖动后松开，或带着速度甩出。
- Then：点击播放一段点击回应。拖动时播放拖拽动画；慢慢松手时松手处即新位置，带着速度松手（或双击后拉开再放的弹弓）时按 dsh-pet 的物理飞出去，碰到页面四边反弹，落地时 Q 弹一下，在空中可以再抓住接着甩。手机倾斜与摇晃不让伙伴动。停下的位置按视口比例记在本设备，下次登录沿用；浏览器窗口变小时伙伴回到可及范围。只有一个伙伴，不做伙伴之间的碰撞。

### Safari 播放 HEVC 版本

- Given：浏览器不支持 VP9 透明通道（iPhone、iPad 上的所有浏览器，以及 Mac 的 Safari）。
- Then：伙伴播放 dsh-pet 另行发布的 `.mov`（HEVC 透明）版本，透明背景与其他浏览器一致。当前动画池没有某段的 `.mov` 时不播放那一段；连待机动画都没有时不显示伙伴，而不是显示黑底视频。

### 换了动画池，已打开的页面不混用

- When：root 安装另一个动画池或切回旧池。
- Then：已打开的页面继续播放它打开时那个池的动画，刷新后换成新池；浏览器不会把两个池的文件混在一起。

## 边界与失败

- 控制不经确认：伙伴工具是单独的 MCP 服务器 `companion`，托管配置写明 `trust: full`。它只改变成员眼前这块屏幕上伙伴的样子，不碰成员的任何数据，所以按“只拦破坏性操作”不需要逐次确认（[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 决定 8）；`anas` 服务器的“提交更改”照旧要确认。工具没有标成只读，因为它们确实会改变屏幕。
- 送达：伙伴服务器经产品服务的伙伴套接字（`anas-companion-assistant.socket`）调用，产品服务按对端 UID 认出成员。MCP 调用不带对话信息，所以产品服务把它与 Hermes 报告的 `tool.start` 配对：只认进行中回合里、名称以 `mcp__companion__` 开头、不是子代理镜像、工具与规范化参数都相同的那一条，恰好一段对话有它才配对，最多等它 1.5 秒；配对不上回答 `no_desktop`，多于一段回答 `busy`。命令只发给这段对话的浏览器窗口，并且这个窗口发出了本回合的提示；排在后面的提示到下一回合才算数。到点运行的定时任务与闲时整理的单元里没有伙伴套接字，伙伴服务器自己回答 `no_desktop`；立即运行的任务在实例中运行、有套接字，由产品服务因配对不上回答 `no_desktop`，只在与对话的同一条调用同时等待时配上（见[定时任务不能让伙伴做事](#定时任务不能让伙伴做事)）。成员停止回合时，Hermes 丢下排在后面的提示，也放弃了还在路上的调用：这样的调用 5 秒内到达时配给停止的回合，回答 `cancelled`（`turn-ended`），不会配上下一回合里工具与参数都相同的调用。实例在回合中途退出时，这个回合随它的连接结束。
- 结果：每次调用都是普通结果，从不是错误，所以 Hermes 的熔断（[研究](../research/hermes-agent-runtime.md#信任的-mcp-服务器)）不会因为没有桌面而关掉伙伴服务器；`status` 为 `ok`、`ended`、`cancelled`、`failed`、`rejected`（带 dsh-pet 的原因，或 A-NAS 的 `paused`、`not-ready`）、`no_desktop`、`busy` 或 `timeout`，另附一句中文，拒绝时带上窗口给的说明（例如先叫醒它、要带锁）。
- 时限：窗口 12 秒内回答（`anim` 最多等 10 秒），产品服务等 15 秒，没回答的结算为 `timeout`；一个窗口有命令没回答后，30 秒内发给它的命令直接结算为 `timeout`，它再回答任何一条即解除。伙伴服务器等产品服务 20 秒，Hermes 对这个服务器的时限是 120 秒（排队时间也算在内）。命令逐条发给窗口，等前一条发出的时间也算在 15 秒内；伙伴服务器先不等了，命令就不再发出，结算为 `cancelled`。回合结束时，还没结算的命令结算为 `cancelled`；释放锁的命令另行发出，一个不收消息的窗口挡不住 Hermes 的事件与其他窗口。
- 参数由产品服务按工具表校验后才下发：未知参数、`url` 与 `image` 一律拒绝为 `bad-request`，不下发；说话的文字为 1–200 个字（按 Unicode 码点计）的纯文本，只允许换行，拒绝其他控制字符、全部 Unicode 双向控制字符与行、段分隔符；气泡最长 60 秒，直线移动最长 5 秒，抛出力度不超过 3；锁的时长超过 5 分钟时按 5 分钟算。
- 页面一侧的安全限制，与产品服务的检查各自成立：命令另按同样的上限复查一次，再过 dsh-pet 的决策层与锁闸门，每个窗口另有每分钟 20 条（`broadcast`、`throw` 各 6 条）的兜底；气泡只显示纯文本，不解析 HTML 或 Markdown，不显示图片与链接；动画只按名称从这台 NAS 的动画池中选，不播放任何地址指向的文件；页面只有一只伙伴 `main`；不是本窗口发起的回合的命令被拒（`not-controlling`，VNAS 得到 `no_desktop`）；卡片未加载或写入出错时回答 `not-ready`。
- 动画只来自产品服务：`/companion/animations` 列出当前池的动画与池 ID，各动画的地址是 dsh-pet 的 `/companion/thumb/main/<文件名>?v=<池 ID>`，文件名限定在该池目录内（`os.Root`）。带池 ID 的地址按 `Cache-Control: public, max-age=31536000, immutable` 长期缓存；不带池 ID 时读当前池，浏览器每次重新验证。池由 root 用 `install-media-pool.sh companion` 按固定清单核对哈希后安装；清单固定 dsh-pet 提交中的 webm 与 `assets-mov` 发布中的 mov。
- 页面不向伙伴传递对话的内容，工作阶段只有阶段、任务描述恒为空；伙伴说出的内容只来自本窗口发起的回合中 VNAS 的 `broadcast`。
- 署名：伙伴外层的悬停提示原文为“大肥鱼：社区二创形象（原作溟月 @上善无形，女仆设计 @ZipZipPipe，CC BY-NC-SA 4.0），仅作非商业试验。动画来自 github.com/PC2005-cloud/dsh-pet”。动画按 dsh-pet 发布的原样安装；改动过的帧沿用同一协议（CC BY-NC-SA 4.0）。
- 日志：dsh-pet 的 info 日志（动画切换、命令的轨迹）只在开发构建中打印，产品构建的控制台里没有；警告与错误不变。

## 验收证据

- 页面：卡片的渲染与署名、跟随本窗口的回合、断线回到空闲、闲置停下与命令唤醒、按通知编号挥手、卸载时取消进行中的动画（[`Companion.test.tsx`](../../web/src/companion/Companion.test.tsx)）；Safari 的 HEVC（[`Companion.safari.test.tsx`](../../web/src/companion/Companion.safari.test.tsx)）；工作阶段的汇总与 60 秒清理（[`workStatus.test.ts`](../../web/src/companion/workStatus.test.ts)）；命令的执行与回答、锁、睡觉、暂停、频率与参数上限（[`commands.test.ts`](../../web/src/companion/commands.test.ts)）；窗口转交命令、关闭时释放锁（[`AssistantPanel.test.tsx`](../../web/src/assistant/AssistantPanel.test.tsx)）；屏保盖住的标记（[`LocalConsoleScreenSaver.test.tsx`](../../web/src/desktop/LocalConsoleScreenSaver.test.tsx)）；共用屏幕上的提示（[`tasks.test.tsx`](../../web/src/assistant/tasks.test.tsx)）。
- 产品服务：配对、只发给发起本回合的窗口、频率、超时、回合结束与窗口离开的结算、锁的释放、参数的规范化与拒绝、停止回合与实例退出后的配对、晚到的子任务事件（[`companion_test.go`](../../internal/assistantapi/companion_test.go)）；动画清单、按池取动画与缓存（[`handler_test.go`](../../internal/webui/handler_test.go)）。
- 伙伴服务器：结果从不是错误、参数原样转给产品服务、没有套接字时回答 `no_desktop`（[`test_companion_mcp.py`](../../ai/tests/test_companion_mcp.py)）；托管配置下 `anas` 仍要审批而 `companion` 不要（[`approvals-managed.sh`](../../ai/assistant-spike/approvals-managed.sh)）。

## 关联

- 决定：[#414](../decisions/2026-11-06-companion-v1.md)（形象、实现方式与 VNAS 的控制）、[V1 是否不含待办与专注计时](../decisions/2026-11-06-companion-v1-no-todo-focus.md)
- 页面：[`Companion.tsx`](../../web/src/companion/Companion.tsx)、[`petCard.tsx`](../../web/src/companion/petCard.tsx)、[`animations.ts`](../../web/src/companion/animations.ts)、[`workStatus.ts`](../../web/src/companion/workStatus.ts)、[`commands.ts`](../../web/src/companion/commands.ts)、[`host.ts`](../../web/src/companion/host.ts)、[`AssistantPanel.tsx`](../../web/src/assistant/AssistantPanel.tsx)、[`TaskToasts.tsx`](../../web/src/assistant/TaskToasts.tsx)、[`taskNotices.ts`](../../web/src/assistant/taskNotices.ts)、[`LocalConsoleScreenSaver.tsx`](../../web/src/desktop/LocalConsoleScreenSaver.tsx)
- 产品服务：[`companion.go`](../../internal/assistantapi/companion.go)、[`gateway.go`](../../internal/assistantapi/gateway.go)、[`approvals.go`](../../internal/assistantapi/approvals.go)、[`handler.go`](../../internal/webui/handler.go)；白名单见[研究](../research/hermes-agent-runtime.md#产品服务的转发)
- 伙伴服务器：[`anas_companion_mcp.py`](../../ai/assistant-spike/anas_companion_mcp.py)、[`managed-config.yaml`](../../ai/assistant-spike/managed-config.yaml)、[`anas-companion-assistant.socket`](../../ai/assistant-spike/anas-companion-assistant.socket)、[`assistant-install.sh`](../../ai/assistant-spike/assistant-install.sh)
- 上游代码与许可：[`dsh-pet/README.md`](../../web/src/companion/dsh-pet/README.md)，复制脚本 [`vendor-dsh-pet.py`](../../scripts/vendor-dsh-pet.py)，补丁在 `web/src/companion/dsh-pet-patches/` 中（其中纯文本气泡 [0023](../../web/src/companion/dsh-pet-patches/0023-the-bubble-is-text-only.patch)、不按地址播放 [0022](../../web/src/companion/dsh-pet-patches/0022-the-host-plays-no-animation-from-an-address.patch)、开发构建才打日志 [0024](../../web/src/companion/dsh-pet-patches/0024-trace-lines-only-in-a-development-build.patch)）
- 规格：[AI 助手](ai-assistant.md)、[本地控制台屏幕保护程序](local-console-screensaver.md)
- 运行手册：[安装伙伴动画](../runbooks/install-companion-animations.md)
- ADR：[0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)、[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
