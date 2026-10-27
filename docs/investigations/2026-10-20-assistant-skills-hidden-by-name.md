# VNAS 自建的技能不出现在“记忆”中

状态：fixed，待实机验收
更新时间：2026-10-27

## 症状与影响

用户在 Experimental NAS（产品 `5d9a31af9783`，试用包同版本）上与 VNAS 对话，回答下方出现后台复盘的说明“自我改进回顾：技能 'anas-photos' 已创建 · 技能 'anas-photos' 已修补”，点“查看记忆”后页面为空。助手确实学会了技能，但成员看不到，也无法删除它。

## 最小复现

1. 在助手沙箱中让助手（或后台复盘）用 `skill_manage` 新建一个名字以 `anas-` 开头的技能，例如 `anas-photos`。
2. 打开 VNAS 的“记忆”。
3. 实际：技能不在列表中。期望：与其他自建技能一样列出，可查看、删除。

## 观察事实

- 产品服务读 Hermes 的学习图 `GET /api/learning/graph` 后，把 `anas-` 开头的技能当成 A-NAS 系统技能跳过（修复前的 `internal/assistantapi/memory.go`）。复盘为照片相关的做法起名 `anas-photos`，与系统技能 `anas-files`、`anas-member` 的命名相似。
- Hermes 本身已区分系统技能：学习图只扫描自带技能与 Hermes 主目录中的技能，并排除 `skills.external_dirs` 中的技能（`agent/learning_graph.py` `_has_learning_signal`）；`skill_manage` 新建与已有技能（含外部目录中的）同名的技能时拒绝；删除外部技能返回 400。
- 在开发机沙箱（固定提交 `0e427d78` 加 A-NAS 补丁，与 NAS 试用包同一运行时）中用脚本模型核对：新建 `anas-photos` 后学习图只有 `anas-photos`，没有 `anas-files`、`anas-member`、`anas-scheduled-tasks`；新建 `anas-files` 被拒（“already exists at /opt/a-nas/assistant-skills/anas-files”）；删除 `anas-files` 得到 400，删除 `anas-photos` 移入 `.archive`。
- 原测试的假 Hermes 把 `anas-files` 放进学习图，与真实 Hermes 不符，所以前缀过滤看似必要，也没有测试覆盖“助手自己起了 `anas-` 名字”。
- 同一轮排查用真实 DeepSeek（`deepseek-flash`）在开发机说了 7 句话：明确的“记住”“新建一个技能”都进了 Hermes 的记忆或技能，并出现在学习图中；但“把要点整理一下存起来，下次接着聊”被写成 `/nas/助手/笔记/` 下的文件，“帮我记一下：某时某事”被建成定时任务，这两者都不在“记忆”中。前者与系统技能 `anas-files` 中“`/nas/助手` 用来记笔记”的说法有关，另作处理（见后续工作）。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 记忆或技能没有写进 Hermes 主目录 | 开发机真实模型对话后检查 `memories/`、`skills/` 与学习图 | 否：都已写入并列出 |
| NAS 的 Hermes 策略关掉了记忆或技能工具 | 只读比对 NAS `/etc/hermes/config.yaml` 与仓库 | 否：仅定时任务不同 |
| 试用包 `update` 清掉了成员数据 | 阅读 `nas-trial.sh` | 否：只替换脚本与运行时 |
| 产品服务过滤掉了技能 | 新测试在修复前的代码上运行 | 是：列表只剩 `family-trip` |

## 根因

产品服务用名字前缀 `anas-` 判断“系统技能”，而技能名由模型自己起，助手自建的技能同样可能以 `anas-` 开头。Hermes 已按来源（`skills.external_dirs`）把系统技能排除在学习图之外，前缀过滤是多余的第二套判断，并误伤了成员自己的技能。

## 修复与回归证据

- 修复：[`memory.go`](../../internal/assistantapi/memory.go) 不再按名字过滤，学习图中的技能原样列出；系统技能的排除与禁止删除由 Hermes 负责，产品服务仍只接受刚取到的列表中的 `id`。
- 测试：[`memory_test.go`](../../internal/assistantapi/memory_test.go) 与[假 Hermes](../../internal/assistantapi/fakeserve_test.go) 改为真实行为（系统技能不在学习图中、不能删除；复盘自建的 `anas-photos` 在其中）。修复前运行失败（技能只剩 `family-trip`），修复后 `go test ./internal/assistantapi/` 通过。

## 后续工作

- 部署：只改产品服务，随下一次产品版本部署生效，试用包不需要 `update`。部署后打开该账号的“记忆”，应看到 `anas-photos`（“VNAS 自己总结”）。
- 名字冲突：Hermes 查找技能时先找主目录、再找 `skills.external_dirs`。成员已有同名自建技能时，A-NAS 以后新增的同名系统技能会被它遮住；新增系统技能前要考虑这一点（例如以后真要做 `anas-photos`）。
- “整理一下存起来”写成 `/nas/助手` 中的文件：不进入记忆，新对话也不会读，但助手会承诺“下次从笔记接着聊”。是否调整 [`anas-files`](../../ai/assistant-spike/skills/anas-files/SKILL.md) 的措辞待用户决定。

## 关联

- 规格：[AI 助手](../specs/ai-assistant.md)（“记忆”）
- 研究：[记忆与自建技能](../research/hermes-agent-runtime.md#记忆与自建技能)
- ADR：[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
- 运行手册：不涉及
