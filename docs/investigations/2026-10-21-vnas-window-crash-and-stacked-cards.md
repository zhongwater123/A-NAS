# VNAS 长对话中窗口崩溃，回答过的确认卡片堆在输入框上方

状态：fixed locally；待部署验收（用户那次崩溃的浏览器报错未取得，根因由症状与复现推断）
更新时间：2026-10-21

## 症状与影响

用户在 Experimental NAS（产品 `9cda8a366ded`，试用包同版本，云端 `deepseek-flash`）上与 VNAS 进行一段高频的长对话，其中有大量操作桌面的步骤：

- 对话进行中 VNAS 窗口变成桌面的窗口兜底页“这个页面暂时无法显示”（[`AppWindow.tsx`](../../web/src/desktop/AppWindow.tsx) `PanelErrorBoundary`），只能“重新载入界面”，即重载整个 WebOS；重新打开 VNAS 进入同一个对话，再次出现同样的页面。
- 助手接连请求确认（命令、提问、桌面上的一步），成员点了允许之后，这些卡片仍留在输入框上方，一张张堆起来，遮住对话。

## 最小复现

崩溃（开发机，jsdom，[`AssistantPanel.test.tsx`](../../web/src/assistant/AssistantPanel.test.tsx) 第一项在修复前的代码上）：

1. 存档中的一轮以工具调用开始，没有回答，Hermes 用一行 `display_kind: failed_turn` 的提示收尾（“This turn did not complete. Some actions may already have run; …”）。
2. 打开这个对话。
3. 实际：assistant-ui 抛出 `status is only supported for assistant messages`，错误冒到桌面的窗口兜底页；每次打开都一样。期望：对话照常显示，这一轮的步骤与一行中文提示。

另一条机制（同一测试文件第二项）：一条实时回复中同一个 `tool_id` 在完成后再次开始，assistant-ui 抛出 `Duplicate key toolCallId-call_0 in useResources`。

卡片堆叠：任一对话中先后有几张命令卡片被允许；窗口把 10 分钟内创建的卡片都显示在输入框上方（修复前 `AssistantPanel.tsx` 的 `here` 过滤），已允许的显示为“已允许，命令在 VNAS 自己的文件夹里运行”。

## 观察事实

- NAS 上 admin 的实例 `anas-assistant@admin` 自 10-20 20:18 起未重启，`memory.events` 中 `oom_kill 0`；匿名内存约 380 MB，另有约 1.3 GB 可回收的页缓存。后端没有崩溃，问题在窗口。
- 一轮没有正常结束（服务商出错、重试用尽、拒答，或在任何回答之前被停止）而最后一行是成员的消息时，Hermes 写入一行 `role: assistant`、`display_kind: failed_turn` 的提示（`agent/conversation_loop.py`、`agent/turn_failure_copy.py`）；`auto_continue`、`process_complete`、`async_delegation_complete`、`model_switch`、`personality_switch` 也是 Hermes 自己写的提示。Hermes Desktop 的还原（复制在 `hermes/lib/chat-messages/hydration.ts`）把它们都变成 `role: system` 的消息，并且把这一轮之前没有归属的工具调用并进这条提示。
- assistant-ui 的 `fromThreadMessageLike` 对非回答消息带 `status` 抛错，对不是恰好一段文字的系统消息也抛错（`@assistant-ui/core` `thread-message-like.ts`）。A-NAS 的 `toThreadMessage` 把系统消息与回答一样转换，带着 `status` 与全部部分；Hermes Desktop 只给它一段文字、不带 `status`（`apps/desktop/src/lib/chat-runtime.ts`）。转换在 `useExternalStoreRuntime` 的 effect 中进行，位于 VNAS 窗口的根组件里，所以错误直接到了桌面的窗口兜底页。
- 这一行在存档里，所以回合结束后的重新加载与以后每次打开都会抛同一个错，与“重新打开同一对话仍崩溃”一致。长而高频的对话更容易出现没有正常结束的一轮。
- 长的存档历史本身不触发崩溃：jsdom 中 5040 行（120 轮、每轮 20 步）的历史可以打开；Hermes 的 REST 默认只给最后 500 行（`hermes_cli/web_routers/sessions.py`）。
- 消息名按它在这一页中的位置生成（`${时间}-${序号}-${角色}`）。超过 500 行后每次重新加载页的起点后移，全部消息改名；assistant-ui 的 `messages` 路径只增不删，同一对话里每轮多存一整页。
- 实时回复按 Hermes Desktop 的 `upsertToolPart` 累积工具步骤；同一 `tool_id` 在已完成之后再出现一次 `tool.start` 时，它追加一个同 id 的步骤。Hermes 只在服务商不给 id 时用 `deterministic_call_id(名称, 参数, 序号)` 生成，同样的调用得到同样的 id；`deepseek-flash` 在开发机上返回的 id 形如 `call_00_<随机串>`，不重复。Hermes Desktop 在交给运行时之前用 `withUniqueToolCallIdsWithinMessage` 改名（上游 #87857），A-NAS 复制了这个函数却没有调用。
- 产品服务把回答过的卡片保留 1 小时（`approvals.go` `keepCards`），让其他窗口与重载后的窗口知道卡片的结局；窗口据此显示 10 分钟内的全部卡片。Hermes Desktop 在回答后立即把卡片移出队列，一次只显示一张，结果留在对话中那一步的工具行里。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 实例内存耗尽或崩溃 | NAS 上只读查看单元状态、`memory.events` | 否：没有重启、没有 OOM |
| 历史太长，打开就崩 | jsdom 打开 50–5040 行的历史 | 否：都能打开 |
| 存档中 Hermes 自己的提示行（系统消息）无法转换 | jsdom 打开带 `failed_turn` 行的历史 | 是：修复前抛出 `status is only supported for assistant messages` |
| 一条实时回复中工具调用 id 重复 | jsdom 推送重复 id 的事件 | 是：修复前抛出 `Duplicate key`；但 `deepseek-flash` 的 id 不重复，不能解释重开即崩 |
| 同一窗口收到同一事件两次 | 阅读 `gateway.go` 的转发 | 否 |

## 根因

- 崩溃：VNAS 窗口按回答的形状转换 Hermes 自己写的提示行（没完成的一轮等），assistant-ui 拒绝并抛错；转换发生在窗口根组件里，没有对话级的错误边界，所以整个窗口退到桌面兜底页。提示行留在存档中，以后每次打开都重复。与 Hermes Desktop 的差异还有两处同类缺口：工具调用 id 的改名没有接上，系统消息以外的成员消息也没有只留文字。
- 卡片堆叠：窗口把“回答过的卡片”当作对话的一部分留在输入框上方 10 分钟，而 Hermes 的审批只在回合内等一个回答。

## 修复与回归证据

- 修复：
  - [`AssistantPanel.tsx`](../../web/src/assistant/AssistantPanel.tsx) `toThreadMessage` 按 Hermes Desktop 的方式转换：成员消息只留文字，提示行是一段不带状态的文字（Hermes 的两种“没完成”提示换成中文），回答前先用 `withUniqueToolCallIdsWithinMessage` 改名重复的工具调用 id。提示显示为对话中居中的一行。
  - [`useAssistantThread.ts`](../../web/src/assistant/useAssistantThread.ts) 把并进提示的工具步骤拆回一条回复，成员仍能看到可能已经执行的操作；消息按 Hermes 的行号（一串工具调用按第一个调用的 id）命名，翻页不再改名。
  - 对话的 assistant-ui 运行时放进 `ConversationBoundary`：一个对话显示不了时只有这一区显示“这个对话暂时无法显示”，侧栏、其他对话、等待回答的卡片与“开始新对话”可用，不需要重载 WebOS；每个对话用新的运行时，记录再变化时重试。
  - 输入框上方只显示 `awaiting`、`deciding` 的卡片；卡片组件去掉回答之后的各种状态。
- 测试：[`AssistantPanel.test.tsx`](../../web/src/assistant/AssistantPanel.test.tsx)（带 `failed_turn` 的对话能打开并显示中文提示与步骤，修复前失败于 `status is only supported for assistant messages`；重复 id 不崩溃，修复前失败于 `Duplicate key toolCallId-call_0 in useResources`；只显示等待中的卡片，另一个窗口回答后收起）、[`threadBoundary.test.tsx`](../../web/src/assistant/threadBoundary.test.tsx)（回复无法渲染时侧栏与“开始新对话”可用，桌面兜底页不出现）、[`useAssistantThread.test.ts`](../../web/src/assistant/useAssistantThread.test.ts)（页的起点后移时消息名不变）。

## 后续工作

- 部署后在 NAS 上打开出过问题的那段对话验收；若仍显示“这个对话暂时无法显示”，从浏览器控制台取 `VNAS conversation render failed` 后面的报错补到本记录。
- 实时回复的每个事件都让 assistant-ui 重新整理整段对话（jsdom 中 150 步约 25 秒）。Hermes Desktop 用 `incremental-external-store-runtime.ts` 只更新变化的一条，首次只取最近 120 行、可加载更早的、渲染上限 600 条；A-NAS 已复制增量运行时但未接入，长对话变卡时再评估。
- A-NAS 取历史时没有 `include_compacted`，上下文压缩之前的轮次在对话中看不到（Hermes Desktop 带 `include_compacted=true`）。

## 关联

- 规格：[AI 助手](../specs/ai-assistant.md)（“回答过的卡片随即收起”、边界与失败）
- ADR：[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
- 运行手册：不涉及
