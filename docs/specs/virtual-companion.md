# 虚拟伙伴（大肥鱼）

状态：draft（V1 实现中，[#404](https://github.com/zhongwater123/A-NAS/issues/404)）
更新时间：2026-11-06

## 目标

已登录的 Web 桌面右下角常驻一个虚拟伙伴“大肥鱼”，用 dsh-pet 的渲染、动画链与拖动播放；它随 VNAS 的回合与待确认卡片切换动作，定时任务出了新结果时挥手。伙伴只表现 VNAS 处于什么状态，从不显示内容，所以全家共用的宿主机屏幕（本地控制台）与个人设备上的表现相同。

## 非目标

- V1 不做电脑端桌宠（Electron）、待办、专注计时、聊天、主动邀请与伙伴自己的数据或接口；这些属于 V2 或 VNAS（[#404](https://github.com/zhongwater123/A-NAS/issues/404) 第 3、4 节；待办与专注计时待定，见[决定记录](../decisions/2026-11-06-companion-v1-no-todo-focus.md)）。
- 伙伴不连接成员的 Hermes 实例，也不因自己而启动实例。
- 不提交动画素材，也不把素材嵌入产品二进制。
- 不允许商用：形象为 CC BY-NC-SA 4.0 的社区二创，A-NAS 商用前必须更换形象。

## 场景

### 未安装动画时不显示

- Given：NAS 没有安装伙伴动画（`/companion/animations` 列表为空）。
- When：成员登录 Web 桌面。
- Then：桌面不显示伙伴，也不下载伙伴的代码与素材；其余功能不受影响。

### 首屏之后出现

- Given：动画已按[安装伙伴动画](../runbooks/install-companion-animations.md)装好。
- When：桌面首屏绘制完成、浏览器空闲（最迟 3 秒）。
- Then：页面读取动画列表，再加载伙伴代码；伙伴代码不计入首屏 JS。大肥鱼出现在右下角 Dock 上方，或成员上次在这台设备上放下的位置；位于桌面图标之上、窗口与系统栏之下。

### 空闲时随机动作

- Given：VNAS 没有进行中的回合，也没有待确认卡片。
- Then：按 dsh-pet 的权重在待机、转身、走路与分类动作之间随机切换，只播放已安装的动画。走路按 dsh-pet 的走位配置朝当前朝向横向移动，位置跟着动画的帧走，走完记在本设备；前方空间不够或没装走路动画时改播一个分类动作。

### 跟随 VNAS 的状态

- Given：成员的 VNAS 窗口打开并连接。
- When：回合开始或推理、调用工具、工具返回、回答完成、出错、被停止，或有卡片等成员确认。
- Then：伙伴分别切到思考、工作、整理、完成、出错、回到空闲、等待的动画；多段对话不同时按“等待 > 出错 > 工作 > 思考 > 整理 > 完成”取最要紧的一个。进行中的状态循环播放，完成与出错播放一次后回到空闲动作。某个状态的动画没有安装时，伙伴不为它切换，照常做原来的动作。VNAS 窗口关闭或断线时，伙伴视为空闲。

### 定时任务出了新结果

- When：成员的未读 `assistant_task` 通知增加，且伙伴空闲。
- Then：伙伴挥手一次；结果的内容只在 VNAS 中查看。

### 点击与拖动

- When：成员点击伙伴，或按住拖动后松开，或带着速度甩出。
- Then：点击播放一段点击回应；拖动时播放拖拽动画。慢慢松手时松手处即新位置；带着速度松手（或双击后拉开再放的弹弓）时，伙伴按 dsh-pet 的物理飞出去，碰到页面四边反弹，落地时 Q 弹一下，在空中可以再抓住接着甩。停下的位置按视口比例记在本设备，下次登录沿用；伙伴至少保留一部分在屏幕内，浏览器窗口变小时也回到可及范围（走路停下，飞行在新边界内接着飞）。只有一个伙伴，不做伙伴之间的碰撞。

### 闲置与隐藏时停下

- When：成员 60 秒没有键盘、鼠标、滚轮或触摸输入，或页面被隐藏。
- Then：伙伴停在当前帧，不再解码视频，走路与飞行也停下，不留逐帧循环；走到一半就停在原处并记住位置，飞到一半就悬在原处。本地控制台的屏保（180 秒）出现前伙伴已停下。下一次输入或页面重新可见时恢复：飞行从停下的位置按原速度继续，有进行中的 VNAS 状态则先恢复它。

### 宿主机屏幕只显示状态

- Given：页面是本地控制台（`?local-console=1`），屏幕全家共用。
- When：成员的定时任务（含提醒）有了新结果。
- Then：伙伴照常挥手；桌面提示只写“VNAS 定时任务 · 时间 · 有新结果”，失败时加“没有完成”，不写任务名与回答的开头。成员自己点“查看结果”时，才在 VNAS 中打开这次运行。个人设备上的提示不变。

### Safari 播放 HEVC 版本

- Given：浏览器不支持 VP9 透明通道（iPhone、iPad 上的所有浏览器，以及 Mac 的 Safari）。
- Then：伙伴播放 dsh-pet 另行发布的 `.mov`（HEVC 透明）版本，透明背景与其他浏览器一致。当前动画池没有某段的 `.mov` 时不播放那一段；连待机动画都没有时不显示伙伴，而不是显示黑底视频。

## 边界

- 动画只来自产品服务的 `/companion/animations`，文件名限定在当前池目录内（`os.Root`），池由 root 按固定清单核对哈希后安装；清单固定 dsh-pet 提交中的 webm 与 `assets-mov` 发布中的 mov。
- 页面不向伙伴传递对话内容；伙伴的工作状态只有阶段，任务描述恒为空。
- 署名：伙伴的悬停提示写明社区二创形象、非商业试验与 dsh-pet 原作者地址。

## 关联

- 实现：[`Companion.tsx`](../../web/src/companion/Companion.tsx)、[`CompanionPet.tsx`](../../web/src/companion/CompanionPet.tsx)、[`animations.ts`](../../web/src/companion/animations.ts)、[`workStatus.ts`](../../web/src/companion/workStatus.ts)、[`assistantActivity.ts`](../../web/src/assistant/assistantActivity.ts)、[`TaskToasts.tsx`](../../web/src/assistant/TaskToasts.tsx)、[`handler.go`](../../internal/webui/handler.go)
- 上游代码与许可：[`dsh-pet/README.md`](../../web/src/companion/dsh-pet/README.md)，复制脚本 [`vendor-dsh-pet.py`](../../scripts/vendor-dsh-pet.py)，补丁 [`dsh-pet-patches/`](../../web/src/companion/dsh-pet-patches/0001-serve-animations-from-a-nas.patch)
- 测试：[`Companion.safari.test.tsx`](../../web/src/companion/Companion.safari.test.tsx)、[`tasks.test.tsx`](../../web/src/assistant/tasks.test.tsx)（共用屏幕上的提示）、[`Companion.test.tsx`](../../web/src/companion/Companion.test.tsx)、[`assistantActivity.test.ts`](../../web/src/assistant/assistantActivity.test.ts)、[`AssistantPanel.test.tsx`](../../web/src/assistant/AssistantPanel.test.tsx)、[`handler_test.go`](../../internal/webui/handler_test.go)
- 运行手册：[安装伙伴动画](../runbooks/install-companion-animations.md)
- ADR：[0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)