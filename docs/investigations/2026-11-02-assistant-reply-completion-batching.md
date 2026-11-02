# 回答结束时更新到空回复，伙伴没有收到完成状态

状态：fixed，待实机验收

更新时间：2026-11-02

## 现象与原因

VNAS 事件依次到达 `message.start`、工具事件、文字增量和 `message.complete`。文字里即使出现“全部完成”，也只能在协议报告结束后驱动伙伴的开心反馈。

新增事件回归用例发现，完成时伙伴收到 `idle`。`useAssistantThread.updateReply` 把读取 `replyId.current` 放在 React 状态更新函数内；`finishTurn` 排队更新后立即清空这个 ref。批量更新稍后运行时读到空 ID，便追加了一条空回复。实际回复没有在同一对象上结束，最后一条消息也没有正文。

## 修复与验证

在排队前分配并捕获回复 ID，让本次状态更新始终操作同一条回复；本组件内的递增序号避免同毫秒相邻两轮重复命名。不改变网关协议、成员权限、确认规则或历史记录的读取方式。

回归通过真实 React 线程 Hook 和受控网关事件验证：思考 → 工具执行 → 含“完成”字样但未结束的流式回复 → 协议结束 → 开心；下一次错误结束 → 受阻。另有任务摘要用例拒绝 pending、interim 及被新用户消息取代的旧回答。模型及 NAS 执行器仍为测试替身，不代表实机模型验收。

## 关联

- [线程状态更新](../../web/src/assistant/useAssistantThread.ts)
- [事件回归测试](../../web/src/companion/TaskExperience.test.tsx)
- [任务摘要与回归](../../web/src/companion/taskState.test.ts)
- [虚拟伙伴规格](../specs/virtual-companion.md)
