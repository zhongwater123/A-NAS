# 剪辑中心（internal/videocut/ 等）

剪辑中心的服务端：项目、持久任务、规划的轮次、填槽与渲染；适用于 `internal/videocut/` 下的各个包、`internal/videocutcompute` 与 `ai/anas_videocut`；目录与分层见[技术设计的“代码结构”](../../docs/architecture/montage-center.md#代码结构)。根目录的 [AGENTS.md](../../AGENTS.md) 仍然适用。权威说明：[规格](../../docs/specs/montage-center.md)、[技术设计](../../docs/architecture/montage-center.md)、[ADR 0038](../../docs/adr/0038-keep-montage-media-in-the-photo-library.md)、[ADR 0039](../../docs/adr/0039-edit-montage-films-as-server-side-drafts-saved-as-new-versions.md)。

- **素材归相册：** 原片、分析与成片只在相册，剪辑中心凭项目授权经相册读写，不存副本与会话，也不绕过相册直连 AI Worker。改卡片或画质规则就是改相册的分析身份并重算（ADR 0038 决定 2–4）。
- **规划不由剪辑中心发起：** 它只留记号、经工具给数据、校验答案（[规划会话](../../docs/architecture/montage-planning-session.md)）。会触发规划的请求由 anas-api 先登记规划授权再转发，新增这类请求两边一起改。规则只在 `ai/anas_montage/prompts/` 的模板里；不合格回答 `rejected` 而不是 `isError`（会触发熔断）。云端只看文字。只有成员的操作能改变结果时才停下等成员；规划没有完成就自动重来，同一任务对同样的输入最多 3 轮，仍没完成就让任务失败并通知成员（[决定记录](../../docs/decisions/2026-11-25-montage-planning-retries.md)）。
- **每一步可重跑：** 步骤按 `input_key` 复用，键只取任务快照；改了算法就升 `steps.go` 的版本常量。存回有栅栏，已存的方案是定局。`montage.db` 的迁移步骤都带 `Down`（[ADR 0034](../../docs/adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md) 决定 1）。
- **接缝两边读同一份样例：** 各写各的测试会各自通过而接不上（[跨进程契约](../../docs/development/testing.md#跨进程契约)、[调查](../../docs/investigations/2026-11-19-montage-job-stalls-on-the-experimental-nas.md)）。更新途中的旧版 Worker 会回答不认识新操作，按“在等它”处理；每个等待都写明在等什么（[跨组件的功能](../../CONTRIBUTING.md#跨组件的功能)）。
- **默认规则成套改：** 规格“本稿补充的默认规则”里的时限、轮数与上限，连同规格、技术设计一起改，并在[决定记录](../../docs/decisions/2026-11-17-montage-center-default-rules.md)补一行。
- **验证：** `make check`、`make race-test`；改 `ai/anas_montage/domain/` 在开发机设 `ANAS_MONTAGE_SAMPLES` 跑 `python -m eval.montage_golden`；改规划链路，交部署前带 `ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME` 跑系统测试。
