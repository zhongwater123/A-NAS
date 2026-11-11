# 虚拟伙伴（大肥鱼）

状态：draft（V1 实现中，[#404](https://github.com/zhongwater123/A-NAS/issues/404)；VNAS 的控制见 [#412](https://github.com/zhongwater123/A-NAS/pull/412)、[#414](../decisions/2026-11-06-companion-v1.md)）
更新时间：2026-11-10

## 目标

已登录的 Web 桌面右下角常驻一个虚拟伙伴“大肥鱼”，就是 dsh-pet 的宠物卡片：渲染、动画链、走动、拖动、抛出、气泡、睡觉与锁都用 dsh-pet 的代码。它随这台设备上发起的 VNAS 回合与待确认卡片切换动作，定时任务出了新结果时挥手。

VNAS 能控制它（[#414](../decisions/2026-11-06-companion-v1.md) 记录的 2026-11-06 决定）：播放动作、说话气泡、移动与抛出、睡觉与叫醒，只在发起这段对话的设备上。所以在控制它的设备上，伙伴可以说出这段对话的内容；别处来的内容，例如定时任务的提示，在全家共用的宿主机屏幕（本地控制台）上仍只显示状态。

## 非目标

- V1 不做电脑端桌宠（Electron）、聊天、主动邀请，也不带 dsh-pet 的余额、语音、菜单与得分弹窗；这些属于 V2 或 VNAS（[#404](https://github.com/zhongwater123/A-NAS/issues/404) 第 3、4 节）。待办与专注计时待定，见[决定记录](../decisions/2026-11-06-companion-v1-no-todo-focus.md)。
- 伙伴没有自己的数据、数据库或公开接口：VNAS 的命令经产品服务已有的 VNAS 窗口连接送达。伙伴不连接成员的 Hermes 实例，也不因自己而启动实例。
- 定时任务、委派的子任务与 Hermes 自己开启的回合不能让伙伴做动作或说话。
- 不提交动画素材，也不把素材嵌入产品二进制。
- 动画按 1.5 倍速播放（多数片段约 6.7 秒，原长 10.04 秒），让成员一眼看出它在动；走路按片段自己的播放时间移动，速度随之加快。
- 不允许商用：形象为 CC BY-NC-SA 4.0 的社区二创，A-NAS 商用前必须更换形象。

## 场景

### 未安装动画时不显示

- Given：NAS 没有安装伙伴动画（`/companion/animations` 列表为空）。
- When：成员登录 Web 桌面。
- Then：桌面不显示伙伴，也不下载伙伴的代码与素材；其余功能不受影响。VNAS 让伙伴做事时得知这个桌面上还没有伙伴。

### 首屏之后出现

- Given：动画已按[安装伙伴动画](../runbooks/install-companion-animations.md)装好。
- When：桌面首屏绘制完成、浏览器空闲（最迟 3 秒）。
- Then：页面读取动画清单（已安装的动画与池 ID），再加载伙伴代码；伙伴代码不计入首屏 JS。大肥鱼出现在右下角 Dock 上方，或成员上次在这台设备上放下的位置（记住的位置按点击区收回页面的可用区域内，不留在页面外或侧栏下）；位于窗口与状态栏之上、侧栏与通知之下。伙伴的层级（`--dsh-pet-z` 为 76）高于窗口（72）与状态栏（70），打开或拖动窗口都不会盖住它；侧栏与通知（80）、菜单等弹出层和屏保仍在它之上。伙伴在窗口之上，但会让开当前窗口（最上层的那个）：它的点击区和这个窗口重叠时，移到窗口外最近的空位，离窗口 12 像素，在窗口左边、右边（同一高度）和上方里选移动最少的；都放不下（如窗口最大化）时退到可用区域的一角，先选盖住窗口最少的，一样少时选离它近的，且不盖住窗口右下角 220×130 像素（VNAS 的发送与停止按钮在这一片）。可用区域离页面四边 8 像素，且不含侧栏（在伙伴之上）：宽屏时离左边 88 像素以内不去，620 像素以下的窄屏不去底部 75 像素。窗口的位置从页面上量，和样式在各种屏宽下的排布一致。窗口打开、换到最上、移动或缩放结束，伙伴被放下、走完、被 VNAS 移动或抛出后落地，以及页面尺寸变化（停 200 毫秒后）时检查。VNAS 的移动途中不检查，等它到达目标，最多等这次移动的时长再加 1.5 秒；抛出等它停住，最多 10 秒；页面缩放带来的重排不算到达；等不到时到点检查一次。让开本身也这样等，没到位的 1.5 秒后再发，最多再发两次。锁开始（之前没有锁，或上一把已经到期）或成员在伙伴上松开指针后，伙伴静止 150 毫秒就再检查一次，飞行中的不打断。拖着伙伴放到窗口上，松手后它也会让开。让开走 VNAS 移动伙伴的同一条路，不换动画，停下、睡着、被锁时也照样让开，先在原地定位再弹过去，减少动态效果时直接到位，位置照样记在本设备。随机走路可能走到当前窗口上时不走；当前窗口出现时正在走、可能走到窗口上的，停在原地。已知现象：随机走路的落点只按页面边界算，不按可用区域算，朝左走到底时可能走进侧栏下面，下一次检查不会把它挪出来。

### 空闲时随机动作

- Given：这台设备上没有进行中的 VNAS 回合，也没有待确认卡片。
- Then：伙伴基本保持待机，只播放已安装的动画：每次掷骰待机 97%、走路 1%，其余 2% 播一个分类动作，有人使用页面时大约每 8 分钟一个（60 秒无人操作后伙伴停下）。dsh-pet 自带的权重（待机 10、转身 5、走路 5）几秒就是一个分类动作，A-NAS 不用。不随机转身：dsh-pet 唯一的转身动画“东张西望”现在表示 VNAS 在看桌面。表示 VNAS 做事的动画（见[跟随这台设备上的 VNAS 回合](#跟随这台设备上的-vnas-回合)的表）只为那一步播放，不进随机动作，也不作点击回应。走路按 dsh-pet 的走位配置朝当前朝向横向移动，走完记在本设备；前方空间不够或没装走路动画时改播一个分类动作。

### 跟随这台设备上的 VNAS 回合

- Given：成员在这台设备的 VNAS 窗口里发出一句话。
- When：回合开始或推理、调用工具、工具返回、回答完成、出错、被停止，或有卡片等成员确认。
- Then：伙伴按这一步在做什么播放各自的动画（见下表），成员一眼能看出它在读文件、找东西还是运行命令；气泡用一句短话说这一步，例如“我在看 文档/a.md 哦”“跑一下 du -sh /nas/…，稍等哦”，说到的对象取自这一步的工具参数（与回答里“处理过程”一栏的写法相同），截到约 12 个字。思考时说一句思考的台词；VNAS 用待办清单时改说“下一步：清单里正在做的一项”。不显示推理过程的文字：它逐段到达，多为英文。工具返回、回答完成、出错、等成员确认时也各有动画和台词；成员在这台设备上停止回合时，伙伴屈膝行礼一次、说一句“好的，停下啦”之类，然后回到待机。
- 阶段的规则照旧用 dsh-pet 的：几段对话同时进行时按“等待 > 出错 > 工作 > 思考 > 整理 > 完成”取最要紧的一个，5 秒内的“工作 → 整理”合并为一次。进行中的阶段循环播放这一步的动画，动画播完就重播，不交给随机动作；完成与出错播放一次，保留 60 秒后清掉。某一步的动画没有安装时播放这个阶段的 dsh-pet 动画；阶段的动画也没有安装时伙伴不为它切换，照常做原来的动作。伙伴睡着或被锁时不跟随。
- 伙伴头旁有一个小标记：思考、工作、等成员确认时是逐个点亮的省略号，回答完成后是对勾，出错后是叉；对勾和叉与这一句台词一样显示 10 秒后消失。空闲、睡着、被锁或回合被停止时没有标记；伙伴停下时省略号也停住，系统要求减少动态效果时省略号不动。标记在气泡下方，气泡和角色的头发、手臂都盖不住它；它跟着伙伴拖动、走动和飞行，朝向改变时换到头的另一侧，不挡点击。
- VNAS 调用伙伴自己的工具（`mcp__companion__*`）时，伙伴仍播放这个阶段的 dsh-pet 动画、说 dsh-pet 为阶段配的固定台词，不另换动画。
- 工具执行是否失败，网关事件里没有（`tool.complete` 不带错误），所以没有“这一步没成”的动画，失败的一步也按“这一步完成”显示；整个回合出错时才播放出错的动画。

| 这一步 | VNAS 的工具 | 动画 | 气泡（{detail} 为这一步的对象） |
| --- | --- | --- | --- |
| 思考 | 回合开始、推理 | 工作状态-思考冒泡 | 正在认真想下一步呢／嗯……让我想想哦~；有待办清单时“下一步：…” |
| 读 | `read_file`、`skill_view`、`skills_list`、`mcp__anas__recent_changes`、`mcp__anas__photo_details` | 工作状态-清点归档 | 我在看 {detail} 哦／一页一页认真读~ |
| 找 | `search_files`、`mcp__anas__search_photos`、`mcp__anas__similar_photos`、`session_search` | 照镜子 | 帮你找找 {detail}~／找找看藏在哪里~ |
| 写草稿 | `write_file`、`patch` | 轻快记录 | 在写 {detail} 的草稿呢／一笔一笔写好它~ |
| 记住 | `memory` | 原地跳跃抓碎头顶物品 | 这个我记住啦！／收进小脑袋里~ |
| 运行 | `terminal`、`execute_code`、`process_manage` | 写代码 | 跑一下 {detail}，稍等哦／噼里啪啦敲命令中~ |
| 看照片、视频 | `vision_analyze`、`video_analyze`、`mcp__anas__look_at_photo`、`mcp__anas__browse_photos` | 扑克魔术 | 看看这张照片~／{detail} 我瞧瞧哦 |
| 看桌面 | `browser_snapshot`、`browser_vision` | 东张西望 | 看看桌面上有什么~／我先瞅一眼屏幕哦 |
| 操作桌面 | `browser_click`、`browser_type`、`browser_press`、`browser_scroll` | 下五子棋 | 帮你操作一下桌面~／在桌面上动动小手~ |
| 分派子任务 | `delegate_task` | 变鸽子 | 派小鸽子去帮忙啦~／这件交给小帮手咯！ |
| 定时任务 | `cronjob_manage` | 抽陀螺 | 定好时间啦，到点自己转~／设个定时任务~ |
| 提交更改 | `mcp__anas__submit_changes` | 拆礼物 | 改好啦，请你过目~／给你看看我做的~ |
| 等成员 | 待确认卡片、`clarify` | 工作状态-原地踱步张望 | 等你回话哦~／你来定，我乖乖等着 |
| 其他工具 | 以上之外的工具 | 工作状态-忙碌点按 | 用一下小工具 {detail}／叮叮当当忙一下~ |
| 这一步完成 | 工具返回 | 工作状态-雀跃庆祝 | 搞定一步，继续看看哦~／这步好啦！ |
| 完成 | 回答完成 | 放烟花 | 全部搞定啦！／任务完成，撒花~ |
| 出错 | 回答出错 | 工作状态-垂头叹气冒汗 | 呜……这次没做成／出错了，对不起嘛 |
| 停止 | 成员在这台设备上停止回合 | 女仆屈膝礼仪 | 好的，停下啦／听你的，不做了~ |

- 这一步没有对象可说时（例如工具没有参数），只用不带 {detail} 的台词。表中的动画只为这些步骤播放，不进随机动作，也不作点击回应。“被吓一跳”留给“这一步没成”，目前不播放，也不进随机动作。
- 只在另一台设备上发起、这台设备只是跟着显示的回合，不让这台设备的伙伴动。
- 本地控制台（`?local-console=1`）的屏幕全家共用：气泡不说这一步的对象，也不说待办清单的内容，只用不带 {detail} 的台词。

### 窗口断线或关闭时回到空闲

- When：VNAS 窗口断线或关闭。
- Then：伙伴忘掉这个窗口的回合与待确认卡片，回到空闲动作；VNAS 命令在这个窗口拿到的锁随即释放，还在等回答的命令由产品服务结算为已取消（`cancelled`，`window-closed`）。

### 让伙伴做动作、说话

- Given：成员在这台设备的 VNAS 窗口里说“让大肥鱼挥挥手”“让它把刚才的结论说一遍”，这个窗口发出了这一回合的提示。
- When：VNAS 调用伙伴工具：`anim`（播放一段已安装的动画）、`broadcast` 与 `bubble_clear`（说话气泡、收起气泡）、`position` 与 `throw`（移动、抛出）、`sleep` 与 `wake`、`lock` 与 `lock_release`。
- Then：不出确认卡片，命令只送到这个窗口，由这台设备上的伙伴照 dsh-pet 的规则执行；VNAS 得到如实的结果：`anim` 等动画播完才回答，其余写入后立即回答。`position` 的目标会让伙伴盖住当前窗口时，伙伴直接去让开后的位置，回答这个位置并带 `stepped_aside`；`throw` 落地后如果盖住当前窗口，伙伴自己让开。工具说明里写明这两点。`anim` 从收到命令起最多等 11.5 秒：先等伙伴开始播放，最多 4 秒，给动画本身至少留 7.5 秒（素材池里最长的动画 10.04 秒，伙伴以 1.5 倍速播放约 6.7 秒）；开始播放后等到 11.5 秒用完为止，加载快时动画的余量更大；任一段等不到时回答 `failed`（`timeout`）。气泡可以是这段对话的内容，以纯文本显示；在宿主机上进行的对话，内容就显示在宿主机屏幕上。
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
- 一个成员同时在伙伴套接字上等待的调用最多 4 条（Hermes 一次只调一条，其余来自沙箱里的终端之类），多出的立即回答 `busy`（`rate-limited`），不查账号、不等配对；调用读完请求体才计数。在这之前，同一个 UID 在伙伴套接字上同时打开的连接最多 8 条，从接受连接起、读任何内容之前就计数，多出的连接立即得到同样的 `busy`（`rate-limited`）并被关闭，所以只开连接、不发或慢慢发内容的进程最多占 8 条连接。请求头与读取的时限见[边界与失败](#边界与失败)。

### 锁

- When：VNAS 用 `lock` 锁住伙伴（默认 2 分钟，最多 5 分钟，可以续期）。
- Then：随机动作、走动与工作阶段的切换停住，伙伴醒来；此后不带这把锁的 `anim`、`broadcast`、`bubble_clear`、`position`、`throw` 被拒（`lock-required`，带锁的到期时间），锁着时不能睡觉（`locked`）。回合结束时产品服务释放这一回合拿到的锁，别的回合拿到的锁不动；成员抓起伙伴拖动时锁也随即释放。

### 睡觉与叫醒

- When：VNAS 用 `sleep` 让伙伴睡觉。
- Then：伙伴播放 dsh-pet 的入睡、睡眠与醒来动画，睡着时不跟随工作阶段；这时 `anim`、`broadcast`、`bubble_clear` 被拒（`sleeping`），VNAS 要先 `wake`。成员点一下睡着的伙伴、或 VNAS 拿锁，伙伴都会醒来。“起来”那段动画取不到时伙伴照样醒来、回到待机（[动画文件或清单取不到时](#动画文件或清单取不到时)）。

### 页面在后台或屏保盖住时回答暂停

- Given：VNAS 窗口所在的页面被隐藏，或本地控制台的屏保、黑屏正盖着桌面。
- When：VNAS 发来 `anim`、`broadcast`、`bubble_clear`、`position` 或 `throw`。
- Then：命令不执行，也不唤醒伙伴或撤下屏保，VNAS 得到“暂停”（`rejected/paused`）。页面隐藏时，`sleep`、`wake` 与锁照常记下，页面重新可见时伙伴按它们的状态显示。
- 屏保或黑屏盖着而页面没有隐藏时，`sleep`、`wake` 与锁照常执行，不撤下屏保。其中 `sleep`、`wake` 以及叫醒睡着的伙伴的 `lock` 只先记下睡或醒，不唤醒闲置停下的伙伴；屏保撤下时伙伴按最后一次记下的睡或醒动起来：撤下屏保的那一下输入（按键、点击、滚轮或移动鼠标）同时让停下的伙伴继续，不必再操作一次。

### 闲置与隐藏时停下

- When：成员 60 秒没有键盘、鼠标、滚轮或触摸输入，或页面被隐藏。
- Then：伙伴停在当前帧，不再解码视频，随机动作、走路与飞行也停下，不留逐帧循环；走到一半就停在原处并记住位置，飞到一半就悬在原处。本地控制台的屏保（180 秒）出现前伙伴已停下。下一次输入或页面重新可见时恢复，飞行从停下的位置按原速度继续。屏保自己拦下撤下它的那一下输入（`pointerdown`、`pointermove`、`keydown`、`wheel`、`touchstart`），同时通知伙伴，所以伙伴随屏保撤下一起恢复，滚轮和移动鼠标这类没有“松开”的输入也一样。
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
- Then：点击播放一段点击回应或一个分类动作（随机选一个，带文字的“文字”类除外：伙伴朝右时字是反的）。拖动时播放拖拽动画；慢慢松手时松手处即新位置，带着速度松手（或双击后拉开再放的弹弓）时按 dsh-pet 的物理飞出去，碰到页面四边反弹，落地时 Q 弹一下，在空中可以再抓住接着甩。手机倾斜与摇晃不让伙伴动。停下的位置按视口比例记在本设备，下次登录沿用；浏览器窗口变小时伙伴回到可及范围。只有一个伙伴，不做伙伴之间的碰撞。

### Safari 播放 HEVC 版本

- Given：浏览器不支持 VP9 透明通道（iPhone、iPad 上的所有浏览器，以及 Mac 的 Safari）。
- Then：伙伴播放 dsh-pet 另行发布的 `.mov`（HEVC 透明）版本，透明背景与其他浏览器一致。当前动画池没有某段的 `.mov` 时不播放那一段；连待机动画都没有时不显示伙伴，而不是显示黑底视频。

### 换了动画池，已打开的页面不混用

- When：root 安装另一个动画池或切回旧池。
- Then：已打开的页面继续播放它打开时那个池的动画，刷新后换成新池；浏览器不会把两个池的文件混在一起。

### 动画文件或清单取不到时

- Given：动画清单与每段动画都经网络从 NAS 取，网络抖动、NAS 忙或文件损坏时可能取不到。dsh-pet 原本读本机文件，几乎走不到这些情况。
- When：页面读动画清单时请求失败、HTTP 出错，或返回的不是 JSON。
- Then：这与“清单为空”不同。页面 2 秒后再读，每次失败后间隔翻倍，最长 5 分钟，直到读到为止；读到非空的清单后伙伴出现。只有读到的清单为空时才没有伙伴（[未安装动画时不显示](#未安装动画时不显示)）。读到之前，VNAS 让伙伴做事时得到 `rejected`（`not-ready`）。
- When：一段动画取不到（请求失败、HTTP 出错、10 秒内没取完）、解码失败，或浏览器拒绝播放（`play()` 被拒）。
- Then：
  - 不是 VNAS 命令要的动画：等 1 秒再取一次；仍失败，再等一会儿就当它播完，从卡片现在的样子接着往下走（加载期间被挡下的“播完”、正在循环的那段，或已经停下的那段）；前面那段还在正常播放时，等它自己播完。连续失败的每一次等待翻倍（1、2、4……秒），最长 30 秒，有一段播出来后回到 1 秒，NAS 连不上时不会一直请求。等待期间有新的切换或被拖动时，交给它们；伙伴停下或被锁时既不重取也不往下走，恢复后重新等一次，恢复时换了别的动画就不再重取。
  - 睡眠的入睡、循环、起来任何一段失败，都不会让伙伴一直睡着或一直醒着；“起来”那段取不到时伙伴照样醒来、回到待机。
  - VNAS 命令要的动画不重试：命令回答 `failed`（`asset-load`、`decode-error` 或 `play-rejected`），动画链照样往下走。
  - 命令的动画 4 秒内还没开始播放时，VNAS 得到 `failed`（`timeout`），这段动画之后取到也不再播出来，伙伴接着播原来的动画链，屏幕上与回答一致；卡片自己的加载上限是 10 秒，取到或放弃之前伙伴照常等这段加载。命令接手的是动画链本来就在加载的那一段时，这段照样播出，只是不再算这条命令的。

## 边界与失败

- 控制不经确认：伙伴工具是单独的 MCP 服务器 `companion`，托管配置写明 `trust: full`。它只改变成员眼前这块屏幕上伙伴的样子，不碰成员的任何数据，所以按“只拦破坏性操作”不需要逐次确认（[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 决定 8）；`anas` 服务器的“提交更改”照旧要确认。工具没有标成只读，因为它们确实会改变屏幕。
- 送达：伙伴服务器经产品服务的伙伴套接字（`anas-companion-assistant.socket`）调用，产品服务按对端 UID 认出成员。MCP 调用不带对话信息，所以产品服务把它与 Hermes 报告的 `tool.start` 配对：只认进行中回合里、名称以 `mcp__companion__` 开头、不是子代理镜像、工具与规范化参数都相同的那一条，恰好一段对话有它才配对，最多等它 1.5 秒；配对不上回答 `no_desktop`，多于一段回答 `busy`。命令只发给这段对话的浏览器窗口，并且这个窗口发出了本回合的提示；排在后面的提示到下一回合才算数。到点运行的定时任务与闲时整理的单元里没有伙伴套接字，伙伴服务器自己回答 `no_desktop`；立即运行的任务在实例中运行、有套接字，由产品服务因配对不上回答 `no_desktop`，只在与对话的同一条调用同时等待时配上（见[定时任务不能让伙伴做事](#定时任务不能让伙伴做事)）。成员停止回合时，Hermes 丢下排在后面的提示，也放弃了还在路上的调用：这样的调用 5 秒内到达时配给停止的回合，回答 `cancelled`（`turn-ended`），不会配上下一回合里工具与参数都相同的调用。Hermes 报告 `tool.start` 时调用不一定已经发出，停止可能让它永远发不出来，所以这样的配对等满 1.5 秒才做；这时另一段对话进行中的回合也有同样的调用，就分不清是谁的，回答 `busy`。回合从产品服务把窗口的停止请求转给 Hermes 时就算在停止：Hermes 停下回合之后才回答这个请求，回合的最后几个事件可能先到。停止期间（转发停止请求之后、`message.complete` 之前），已登记和新到的 `tool.start` 一律算作停止的回合留下的，照上面处理，不下发：Hermes 可能等正在运行的调用返回后才处理停止，这期间还会开始下一条调用。停止一直算到这一回合的 `message.complete`：这期间另一个窗口打开这段对话（Hermes 报告回合仍在运行），或者有人再发提示（排在停止的回合后面），都不会解除停止。已经配上、还排在前一条后面没发出的命令也不再发出，结算为 `cancelled`（`turn-ended`）。实例在回合中途退出时，这个回合随它的连接结束。
- 结果：每次调用都是普通结果，从不是错误，所以 Hermes 的熔断（[研究](../research/hermes-agent-runtime.md#信任的-mcp-服务器)）不会因为没有桌面而关掉伙伴服务器；`status` 为 `ok`、`ended`、`cancelled`、`failed`、`rejected`（带 dsh-pet 的原因，或 A-NAS 的 `paused`、`not-ready`、`bad-request`）、`no_desktop`、`busy` 或 `timeout`，另附一句中文；产品服务自己给的原因有：`cancelled` 的 `turn-ended`（回合结束或被停止）与 `window-closed`（窗口在回答前断线或关闭），`busy` 的 `rate-limited`，`timeout` 的 `unresponsive`（这个窗口还有命令没回答）。页面给的原因例如 `cancelled` 的 `superseded`（被别的切换盖掉）与 `frozen`（伙伴停下），`failed` 的 `asset-load`、`decode-error`、`play-rejected` 与 `timeout`，拒绝时带上窗口给的说明（例如先叫醒它、要带锁）。
- 时限：窗口 12 秒内回答（`anim` 从收到命令起最多等 11.5 秒，其中等开始播放最多 4 秒），产品服务等 15 秒，没回答的结算为 `timeout`；一个窗口有命令没回答后，30 秒内发给它的命令直接结算为 `timeout`（`unresponsive`），它再回答任何一条即解除；窗口在回答前断线或关闭时，发给它的命令立即结算为 `cancelled`（`window-closed`）。给动画留的 7.5 秒按伙伴的 1.5 倍速定（素材池里最长的 10.04 秒约播 6.7 秒）：等开始播放的上限因此是 4 秒，最长的动画开始播放后还有至少 0.8 秒余量（不留余量时可以等约 4.8 秒）；按原速播放时最长的动画要 10.04 秒，就得连同窗口的 12 秒与产品服务的 15 秒一起改；[`commands.test.ts`](../../web/src/companion/commands.test.ts) 按 `petCard.tsx` 的 `PLAYBACK_RATE` 播放，改慢速度时测试失败。伙伴服务器等产品服务 20 秒，Hermes 对这个服务器的时限是 120 秒（排队时间也算在内）。命令逐条发给窗口，等前一条发出的时间也算在 15 秒内；伙伴服务器先不等了，命令就不再发出，结算为 `cancelled`。回合结束时，还没结算的命令结算为 `cancelled`；释放锁的命令另行发出，一个不收消息的窗口挡不住 Hermes 的事件与其他窗口。
- 参数由产品服务按工具表校验后才下发：未知参数、`url` 与 `image` 一律拒绝为 `bad-request`，不下发；说话的文字为 1–200 个字（按 Unicode 码点计）的纯文本，只允许换行，拒绝其他控制字符、全部 Unicode 双向控制字符与行、段分隔符；气泡最长 60 秒，直线移动最长 5 秒，抛出力度不超过 3；锁的时长超过 5 分钟时按 5 分钟算。伙伴服务器收到编不成 UTF-8 的工具参数（例如孤立的代理项）时回答 `rejected`（`bad-request`），不调用产品服务；这是普通结果，不是 MCP 错误，Hermes 的熔断不计它。
- 伙伴套接字的上限：沙箱里的任何进程都能连到它，所以每个成员的 UID 受两重限制：同时打开的连接最多 8 条，从接受连接起、读任何内容之前计数，多出的回答 `busy`（`rate-limited`）后关闭，伙伴服务器的调用这时即使发不出去，也读出这个回答，而不是当作 `timeout`；正在处理的调用最多 4 条，读完请求体后计数（[太频繁时回答正忙](#太频繁时回答正忙)）。请求头的上限设为 4 KiB（Go 的 net/http 另外多读 4 KiB，实际约 8 KiB 以内都会读完，超出回答 431），须在 5 秒内发完，请求体在其后 5 秒内发完；整个请求（包括产品服务不读的请求体，例如不存在的路径或不是成员的调用）10 秒内读完，否则关闭连接；两次调用之间空闲的连接 5 秒后关闭。没有写超时：读完请求体后，回答最晚要等配对的 1.5 秒加窗口的 15 秒。产品服务的单元另设 `TasksMax=4096` 与 `LimitNOFILE=65536`，平时只用几十个线程与描述符；线程到上限时进程退出，由 `Restart=on-failure` 重启。没有 `MemoryMax`：用量随成员、窗口与传输增长，要在设备上测过才定。开发机的用户单元里，Web 终端的 Shell 在单元内运行，计入 `TasksMax`；设备上它们由 Host Agent 的文件代理启动，不计入。
- 命令与动画链：命令要的动画还在加载时被别的切换盖掉，立即回答 `cancelled`（`superseded`），不等到超时。命令要的正是动画链正在加载的那一段时，命令接手这次加载，按这段动画的播放与结束回答；这次加载原来带着的命令回答 `cancelled`（`superseded`）。命令的动画加载或播放期间，工作阶段变化不打断它：阶段的动画等它播完（或失败）再播，其间阶段回到空闲就不再补播；伙伴停下、被锁或睡着时不跟随阶段，等着的阶段动画随之作废，恢复后按当时的阶段显示，回合已经结束就不再播。前一次切换的动画还在同一个视频元素里解码时被新的切换接手，那段动画的解码失败不算到新的切换上。
- 提示没发出去：本窗口发提示前先登记这一轮由它发起；提交失败（`prompt.submit` 出错）时撤回这一登记，不按停止处理，伙伴不跟随这段对话里别的设备随后发起的一轮。仍未解决：两台设备在同一段对话里同时发提示时，登记按对话而不是按轮次，可能串到另一台设备；要产品服务按轮次登记才能解决。
- 页面一侧的安全限制，与产品服务的检查各自成立：命令另按同样的上限复查一次，再过 dsh-pet 的决策层与锁闸门，每个窗口另有每分钟 20 条（`broadcast`、`throw` 各 6 条）的兜底；气泡只显示纯文本，不解析 HTML 或 Markdown，不显示图片与链接；动画只按名称从这台 NAS 的动画池中选，不播放任何地址指向的文件；页面只有一只伙伴 `main`；不是本窗口发起的回合的命令被拒（`not-controlling`，VNAS 得到 `no_desktop`）；卡片未加载或写入出错时回答 `not-ready`。
- 动画只来自产品服务：`/companion/animations` 列出当前池的动画与池 ID，各动画的地址是 dsh-pet 的 `/companion/thumb/main/<文件名>?v=<池 ID>`，文件名限定在该池目录内（`os.Root`）。带池 ID 的地址按 `Cache-Control: public, max-age=31536000, immutable` 长期缓存；不带池 ID 时读当前池，浏览器每次重新验证。池由 root 用 `install-media-pool.sh companion` 按固定清单核对哈希后安装，脚本只接受本 release 固定的池（`companion_pool`，即 README 记录的池）；清单固定 dsh-pet 提交中的 webm 与 `assets-mov` 发布中的 mov。
- 页面传给伙伴的工作阶段除了阶段，还带这一步的动画名和一句短话（[补丁 0028](../../web/src/companion/dsh-pet-patches/0028-a-stage-plays-the-clip-its-step-asks-for.patch)）：短话只来自本窗口发起的回合，说到的对象（路径、命令、搜索词等）截到约 12 个字，搜索词去掉 `*`、`?`、`[`、`]` 这类通配符，只剩通配符时用不带对象的那句，在本地控制台上从不出现；推理过程与回答的内容不传。除此之外，伙伴说出的内容只来自本窗口发起的回合中 VNAS 的 `broadcast`。
- 署名：设置 → 概览最下方的“桌面伙伴”一行（每个成员都能打开概览；个性化只给管理员）写明“大肥鱼：社区二创形象（原作溟月 @上善无形，女仆设计 @ZipZipPipe，CC BY-NC-SA 4.0），仅作非商业试验。动画来自 github.com/PC2005-cloud/dsh-pet”。伙伴本身不带署名的悬停提示，鼠标移上去只显示 dsh-pet 自己的名字提示“大肥鱼”。动画按 dsh-pet 发布的原样安装；改动过的帧沿用同一协议（CC BY-NC-SA 4.0）。复制进来的代码的 MIT 许可证全文（dsh-pet 提交 `10f6c868b8110c897af33d6137a3d85e84b665ba`、Hermes Desktop 提交 `0e427d788d253630d38a3a51cd76849e73cd6925`）由 `make web-build` 写进 Web 桌面，产品服务在 `/assets/third-party-licenses.txt` 提供，缺一条 MIT 许可证构建即失败；动画是 CC BY-NC-SA 4.0，不在这个文件里。这个文件的名字不带内容哈希，所以不像其他 `/assets/` 那样缓存一年：产品服务以 `no-cache` 提供它，浏览器每次都重新验证，升级后显示的是新的提交。`animations.json`（复制脚本由 dsh-pet 的配置生成的动画分类）算代码还是动画素材、适用哪个许可证，尚待决定。
- 上游代码：dsh-pet 固定在一个提交，仓库里的代码等于这个提交加编号补丁，不跟随上游更新；规则与升级步骤见 [`dsh-pet/README.md`](../../web/src/companion/dsh-pet/README.md#no-updates)。复制脚本只接受固定提交上的干净检出：有已跟踪的改动、任何未跟踪的文件，或 `dsh-pet/src`、`dsh-pet/assets/config.jsonc`、`dsh-pet/assets/webm`、`LICENSE` 下被忽略的文件都拒绝。没有 Safari 素材包时，`--mov-from <animations.sha256>` 从这份清单取 HEVC 动画的哈希，不核对文件。
- 日志：dsh-pet 的 info 日志（动画切换、命令的轨迹）只在开发构建中打印，产品构建的控制台里没有；警告与错误不变。

## 验收证据

- 页面：卡片的渲染、跟随本窗口的回合与头旁的标记、断线回到空闲、闲置停下与命令唤醒、按通知编号挥手、卸载时取消进行中的动画、清单读不到时重试、撤下屏保的那一下让伙伴继续、点击与拖动、让开当前窗口（打开窗口、放下、减少动态效果、被点或上锁打断后再查、等 VNAS 的移动落地、页面缩放、不往窗口上走）（[`Companion.test.tsx`](../../web/src/companion/Companion.test.tsx)）；动画取不到、解码失败或 `play()` 被拒时重试与往下走、冻结期间不重试，命令与动画链的回答（[`animationChain.test.ts`](../../web/src/companion/animationChain.test.ts)、[`cardAnimation.test.tsx`](../../web/src/companion/cardAnimation.test.tsx)）；Safari 的 HEVC（[`Companion.safari.test.tsx`](../../web/src/companion/Companion.safari.test.tsx)）；工作阶段的汇总与 60 秒清理、每一步的动画与短话、共用屏幕不说对象（[`workStatus.test.ts`](../../web/src/companion/workStatus.test.ts)、[`behaviors.test.ts`](../../web/src/companion/behaviors.test.ts)）；让开时去哪里（[`stepAside.test.ts`](../../web/src/companion/stepAside.test.ts)）；桌面在窗口打开、换到最上、移动、缩放、最大化与还原、最小化、关闭和显示桌面时告诉伙伴当前窗口（[`App.test.tsx`](../../web/src/App.test.tsx)）；`position` 让开后的回答（[`commands.test.ts`](../../web/src/companion/commands.test.ts)）；随机动作与点击的动画池（[`animations.test.ts`](../../web/src/companion/animations.test.ts)）；命令的执行与回答、`anim` 的两段等待、锁、睡觉、暂停、频率与参数上限（[`commands.test.ts`](../../web/src/companion/commands.test.ts)）；窗口转交命令、关闭时释放锁、提示没发出去时撤回这一轮（[`AssistantPanel.test.tsx`](../../web/src/assistant/AssistantPanel.test.tsx)）；屏保盖住的标记与撤下屏保的那一下输入的通知（[`LocalConsoleScreenSaver.test.tsx`](../../web/src/desktop/LocalConsoleScreenSaver.test.tsx)）；共用屏幕上的提示（[`tasks.test.tsx`](../../web/src/assistant/tasks.test.tsx)）；设置里的署名（[`SettingsPanel.test.tsx`](../../web/src/settings/SettingsPanel.test.tsx)）。
- 产品服务：配对、只发给发起本回合的窗口、频率、超时、回合结束与窗口离开的结算、锁的释放、参数的规范化与拒绝、停止回合与实例退出后的配对、晚到的子任务事件（[`companion_test.go`](../../internal/assistantapi/companion_test.go)）；每个 UID 的连接数、请求头大小、读取与空闲时限（[`companion_socket_test.go`](../../internal/assistantapi/companion_socket_test.go)）；动画清单、按池取动画与缓存（[`handler_test.go`](../../internal/webui/handler_test.go)）。
- 伙伴服务器：结果从不是错误、参数原样转给产品服务、编不成 UTF-8 的参数回答 `bad-request`、没有套接字时回答 `no_desktop`（[`test_companion_mcp.py`](../../ai/tests/test_companion_mcp.py)）。`make ai-sdk-test` 在 `make check-steps` 中，所以 CI 也运行：它在 `build/companion-mcp-sdk` 里按 [`companion-mcp-sdk.txt`](../../ai/assistant-spike/companion-mcp-sdk.txt) 装 Hermes 锁定的 MCP SDK（带哈希，首次运行从 PyPI 下载）后运行这组测试，设 `ANAS_COMPANION_MCP_SDK=required`，缺 SDK 即失败；Hermes 升级时按这个文件头的命令重新生成。带运行环境的系统测试另用运行环境自带的 SDK 运行同一组测试。
- 沙箱与审批：系统测试（[`system-test.sh`](../../ai/assistant-spike/system-test.sh)，由 [`scripts/system-test.sh`](../../scripts/system-test.sh) 调用）断言伙伴套接字 `/run/a-nas-companion-assistant/assistant.sock` 只绑进 `anas-assistant@<成员>`，不绑进 `anas-assistant-cron@` 与 `anas-assistant-curator@`，成员在沙箱外连不上；带运行环境时还断言托管配置里 `mcp_servers.anas.trust` 为 `untrusted`、`mcp_servers.companion.trust` 为 `full`，伙伴工具不经审批运行，没有 VNAS 窗口时回答没有在用的桌面。[`approvals-managed.sh`](../../ai/assistant-spike/approvals-managed.sh) 是 spike 的手动检查，不在门禁里：装好的审批时限是一天（86400 秒），要用 `SPIKE_APPROVAL_TIMEOUT=20 ai/assistant-spike/run.sh sandbox` 建沙箱后运行。
- 上游代码与许可：`make vendor-check`（在 `make check-steps` 中）离线核对清单的池等于 `install-media-pool.sh` 固定的池与 README 记录的池、README 列出的补丁与补丁目录一致、每个复制文件的文件头写着固定的提交、目录里的文件与复制脚本写下的 `dsh-pet/files.sha256` 逐个一致，所以 CI 也拦得住手工改过的复制文件；`make vendor-check DSH_PET=<检出> DSH_PET_MOV_ZIP=<zip>` 在临时目录重新引入、要求与仓库没有差异，并确认改过的检出被拒绝，不给 zip 时 Safari 动画的哈希取自仓库的清单。CI 没有检出也没有 zip，只运行离线部分并打印 SKIP；完整检查是每次提里程碑或升级前的本地步骤，要用装有 json5 的 Python（`VENDOR_PYTHON=~/.local/opt/ai-tools/bin/python`，见[测试与验证](../development/testing.md#静态检查)）。系统测试从产品服务取 `/assets/third-party-licenses.txt`，要求 dsh-pet 的条目带固定的提交，`web/src` 下每个 MIT 许可证各一条。

## 关联

- 决定：[#414](../decisions/2026-11-06-companion-v1.md)（形象、实现方式与 VNAS 的控制）、[V1 是否不含待办与专注计时](../decisions/2026-11-06-companion-v1-no-todo-focus.md)
- 页面：[`Companion.tsx`](../../web/src/companion/Companion.tsx)、[`petCard.tsx`](../../web/src/companion/petCard.tsx)、[`animations.ts`](../../web/src/companion/animations.ts)、[`behaviors.ts`](../../web/src/companion/behaviors.ts)、[`workStatus.ts`](../../web/src/companion/workStatus.ts)、[`toolLabels.ts`](../../web/src/assistant/toolLabels.ts)、[`commands.ts`](../../web/src/companion/commands.ts)、[`host.ts`](../../web/src/companion/host.ts)、[`AssistantPanel.tsx`](../../web/src/assistant/AssistantPanel.tsx)、[`TaskToasts.tsx`](../../web/src/assistant/TaskToasts.tsx)、[`taskNotices.ts`](../../web/src/assistant/taskNotices.ts)、[`LocalConsoleScreenSaver.tsx`](../../web/src/desktop/LocalConsoleScreenSaver.tsx)
- 产品服务：[`companion.go`](../../internal/assistantapi/companion.go)、[`gateway.go`](../../internal/assistantapi/gateway.go)、[`approvals.go`](../../internal/assistantapi/approvals.go)、[`handler.go`](../../internal/webui/handler.go)；白名单见[研究](../research/hermes-agent-runtime.md#产品服务的转发)
- 伙伴服务器：[`anas_companion_mcp.py`](../../ai/assistant-spike/anas_companion_mcp.py)、[`managed-config.yaml`](../../ai/assistant-spike/managed-config.yaml)、[`anas-companion-assistant.socket`](../../ai/assistant-spike/anas-companion-assistant.socket)、[`assistant-install.sh`](../../ai/assistant-spike/assistant-install.sh)
- 上游代码与许可：[`dsh-pet/README.md`](../../web/src/companion/dsh-pet/README.md)，复制脚本 [`vendor-dsh-pet.py`](../../scripts/vendor-dsh-pet.py)，补丁在 `web/src/companion/dsh-pet-patches/` 中（其中纯文本气泡 [0023](../../web/src/companion/dsh-pet-patches/0023-the-bubble-is-text-only.patch)、不按地址播放 [0022](../../web/src/companion/dsh-pet-patches/0022-the-host-plays-no-animation-from-an-address.patch)、开发构建才打日志 [0024](../../web/src/companion/dsh-pet-patches/0024-trace-lines-only-in-a-development-build.patch)、每一步的动画 [0028](../../web/src/companion/dsh-pet-patches/0028-a-stage-plays-the-clip-its-step-asks-for.patch)、动画没播出来时往下走 [0031](../../web/src/companion/dsh-pet-patches/0031-a-clip-that-does-not-play-moves-the-chain-on.patch)），门禁检查 [`check-dsh-pet-vendor.sh`](../../scripts/check-dsh-pet-vendor.sh)
- 规格：[AI 助手](ai-assistant.md)、[本地控制台屏幕保护程序](local-console-screensaver.md)
- 运行手册：[安装伙伴动画](../runbooks/install-companion-animations.md)
- ADR：[0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)、[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
