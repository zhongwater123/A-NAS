# 伙伴的“这一步完成”测试与阶段合并的 5 秒赛跑

状态：resolved
更新时间：2026-11-19

## 症状与影响

[`Companion.test.tsx`](../../web/src/companion/Companion.test.tsx) 的用例“goes on with a step's clip when the step is done, with no clapping”在 main 上偶发失败：2026-11-18 在 #570 的本地门禁失败一次，2026-11-19 在 #605 的本地门禁（WSL，负载约 6）与 CI 的 Project checks 各失败一次（[CI 44530723069 第 1 次](https://github.com/zhongwater123/A-NAS/actions/runs/44530723069/attempts/1)）。报错都是 `findByText(/搞定一步，继续看看哦~|这步好啦！/)` 在 5 秒内找不到气泡文字；单独重跑这个文件通过。

只影响测试。产品照规格合并 5 秒内的“工作 → 整理”，没有缺陷。

## 复现

- 加负载：在 WSL 的 ext4 副本中，8 个 `while :; do :; done` 占满 CPU（负载约 12），4 个进程各循环用 `vitest run -t` 只跑这个用例：60 次中失败 8 次。
- 确定性复现：在测试的临时副本里，`tool.complete` 之后安排一个定时器，从 4 秒起让事件循环忙等 1.5 秒，模拟负载下的停顿：原测试 5/5 失败，修复后 5/5 通过。

## 观察事实

- 每次失败的日志里，卡片收到“整理”（`working->result`）正好在收到“工作”（`null->working`）5 秒之后，也就是 `findBy` 放弃的时刻。CI 那次是 13:31:47.590 与 13:31:52.596。
- 失败时这个用例里没有睡觉日志。原先怀疑的 `[dsh-pet] 睡觉 -> [睡觉-开始]` 与“入睡被打断”来自同一文件里测试睡觉的其他用例：并行输出时它们夹在附近。
- [`work-status-bridge.ts`](../../web/src/companion/dsh-pet/host/plugin/work-status-bridge.ts) 的 `WORK_STATE_GAP_MS` 为 5000：距上次写入不足 5 秒的“工作 → 整理”先不写，到 5 秒时再补写最新状态。[`workStatus.test.ts`](../../web/src/companion/workStatus.test.ts) 的“merges a working to result flip within 5 s”覆盖这一行为，[规格](../specs/virtual-companion.md)也写明“5 秒内的‘工作 → 整理’合并为一次”。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 测试中途伙伴随机入睡，睡觉收起了工作气泡 | 读失败日志中这个用例的部分；查谁会写睡觉叶子 | 排除：用例里没有睡觉日志，入睡只来自安静模式、成员的睡觉或 VNAS 的命令，用例都没有触发 |
| 负载下 5 秒超时本身不够 | 看卡片何时收到“整理” | 排除：卡片总在 5 秒时才收到，空闲机器上也一样，只是赢得了赛跑 |
| 阶段合并把“整理”推迟到 5 秒，和 `findBy` 的 5 秒赛跑 | 日志时间戳；在 4 秒到 5.5 秒之间停住事件循环 | 成立 |

## 根因

用例让这一步在几毫秒内开始又结束，于是“整理”被合并推迟到“工作”写入 5 秒之后。`findBy` 在等到“工作”的动画之后才开始等，自己的 5 秒期限只比补写晚这一小段时间。补写只是安排 React 渲染，渲染、卡片的 effect 与气泡更新都要在之后的事件循环中进行；负载下事件循环晚到，两个定时器同时到期，`findBy` 的超时在渲染之前执行，用例失败。

## 修复与回归证据

- 修复：用例让 `Date.now` 前移 5 秒，表示这一步已经进行了 5 秒，`tool.complete` 立即发布，`findBy` 不再等合并。合并本身仍由 `workStatus.test.ts` 覆盖。没有放宽等待上限，也没有重试。
- 变异检查：让“这一步完成”换成鼓掌的动画，修复后的用例以 `expected [ '工作状态-雀跃庆祝' ] to deeply equal []` 失败，断言仍然有效。
- 回归：同样负载下循环 100 次，结果与完整门禁写在 PR 中。

## 关联

- 测试：[`Companion.test.tsx`](../../web/src/companion/Companion.test.tsx)、[`workStatus.test.ts`](../../web/src/companion/workStatus.test.ts)
- 代码：[`work-status-bridge.ts`](../../web/src/companion/dsh-pet/host/plugin/work-status-bridge.ts)
- 规格：[虚拟伙伴](../specs/virtual-companion.md)
- ADR：不涉及
- 运行手册：不涉及
