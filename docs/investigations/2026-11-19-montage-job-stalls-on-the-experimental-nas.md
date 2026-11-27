# 剪辑任务在实验 NAS 上先停在“等待分析”，再停在“等待 VNAS”

状态：resolved
更新时间：2026-11-23

## 症状与影响

剪辑中心（[#514](https://github.com/zhongwater123/A-NAS/issues/514)）的 8 个 PR 合入后，2026-11-19 在实验 NAS 上第一次实机验收，`99446cf5` 的软件包，开关以 drop-in 打开。成员点“开始剪辑”后，任务先在“等待分析”停了一个多小时；分析做完后，又一直停在“VNAS 这一轮没有开始，稍后再试”。两处都没有报错，页面也不说原因，所以看起来像卡死了。

同一段时间，NAS 经网页端口 10 分钟发出约 1.5 GB，几乎全部发给验收用的浏览器，而 NAS 收到的只有约 5.6 MB。

## 观察事实

第一处，等待分析：

- 相册的 `catalog.db` 里有 15 个 `montage-windows/v1` 任务，17:57 起排队，`attempts` 都是 0；AI Worker 从 14:34 起没有再启动，相册服务 17:50 之后没有一行日志。
- 关掉剪辑中心窗口后 5 分钟（19:24 → 19:29），AI Worker 启动，分析开始；窗口、卡片与画质约 20 分钟做完，36 张卡片里 32 张合格，32 张描述全部成功，平均每张 74 秒（关键帧最多 18 张，提示 3,886 token）。
- 相册服务把每个仍在服务中的成员请求都计为前台使用，结束后再静默 5 分钟，期间后台 AI 工作一律暂停（[`photoservice.Authenticate`](../../internal/photoservice/photoservice.go)、[`gate.go`](../../internal/photoservice/gate.go)）。门控按全机计，不分是谁在用。
- 剪辑中心主页的背景每 5 秒换播一段成员视频的原片（`preload="auto"`），全部经相册的 `/video/original`，这正是那 1.5 GB。相册的图片与原片按设计是 `private, no-cache`，视频流是 `no-store`，浏览器缓存挡不住这些请求。

第二处，等待 VNAS：

- 分析完成后，唤醒服务在 20:14、20:21、20:29、20:39、20:56 启动成员的定时任务单元，剪辑中心每次都记下 `planning round claimed`，5 分钟后按 `out of time` 作废这一轮。
- 单元日志每次都是 `montage round: the claim named no round`，之后 Hermes 正常停止；成员的 Hermes 主目录里没有领取回执，唤醒服务因此每次都退回轮数并加长退避。
- 剪辑中心的轮次编号是 `newID("round")`，即 `crypto/rand.Text`，26 个 base32 字母（如 `round:GKFESVH7FI7VTRMHNJ7T7JLHHG`）；回合脚本的 `ROUND_ID`、`JOB_ID` 要求 24 个。回合脚本的测试用 24 个字母的编号，所以两边各自的测试都通过。系统测试只走到“没有任务可规划”（409），没有走过一次真正的领取。

## 根因

1. 剪辑中心的分析是成员在等的工作，却和相册自己的后台 AI 工作共用“有人在用相册就暂停”的门控；剪辑中心窗口本身就在持续读相册，所以成员开着窗口等，任务就永远等不到。任何成员在任何地方用相册也有同样的效果。
2. 回合脚本对编号格式的假设与剪辑中心生成编号的方式不一致，领到的每一轮都被当成格式不对而丢弃，又不报告放弃，只能等超时。

## 修复与回归证据

- [PR #605](https://github.com/zhongwater123/A-NAS/pull/605)：
  - 相册门控对剪辑中心的分析单独回答（`photos.MemberGate`），不看前台活动，内存与压力条件照旧；相册在用时一轮只做剪辑中心的分析，照片自己的工作照旧等待。测试：`TestTheMontageAnalysisGoesOnWhileTheLibraryIsInUse`、`TestAMembersWorkIsNotHeldBackByUseOfTheLibrary`。
  - 剪辑中心主页不再播放成员的视频，只播设备自己的背景片。
  - 回合脚本的编号格式改为 26 个字母；`TestTheRoundScriptAcceptsTheIDsAndTokensOfAClaim` 从 `montage_round.py` 读出它的格式，用剪辑中心真实生成的编号和 token 核对。改回 24 时这个测试失败。

## 后续工作

- 规划的交接整个换掉了（用户 2026-11-20 [决定](../decisions/2026-11-20-montage-planning-as-a-vnas-session.md)，[规划会话](../architecture/videocut-planning-session.md)）：不再借定时任务单元跑一轮，改由 root 的规划发起方把一轮交给成员的规划进程，作为一次对话；上面第 2 条根因所在的回合脚本已删除，编号格式的核对改指向规划发起方（`internal/montage/planner_ids_test.go`）。
- 新版本部署后在实验 NAS 上复验：开着剪辑中心窗口时分析照常进行；规划能领到一轮、交给成员的规划进程并交回两个方案。2026-11-20 在实验 NAS 上已带真实模型（DeepSeek）走完一轮，规划、填槽、渲染到存回相册都通过，用时见[首版闭环的基线](../architecture/videocut-center.md#首版闭环的基线)；开着窗口时的分析仍待按验收手册复验。
- 系统测试已走一次真正的规划：带真实运行环境时，脚本化模型在规划进程里调四个剪辑工具交两个方案，任务越过规划步骤（`ai/assistant-spike/system-test.sh`）。
- 等待 VNAS 时，页面已写明在等什么、几分钟后再试；等待分析时仍只写“相册正在分析视频”，还要写明在等什么。

## 关联

- 规格：[剪辑中心](../specs/videocut-center.md)
- ADR：[0038](../adr/0038-keep-montage-media-in-the-photo-library.md)
- 运行手册：[在实验 NAS 上演示剪辑中心](../runbooks/demo-videocut-center.md)
- 代码：[`internal/photos/airunner.go`](../../internal/photos/airunner.go)、[`internal/photoservice/gate.go`](../../internal/photoservice/gate.go)、[`ai/assistant/montage-planner.py`](../../ai/assistant/videocut-planner.py)（取代回合脚本）、[`web/src/videocut/screens/HeroReel.tsx`](../../web/src/videocut/screens/HeroReel.tsx)
