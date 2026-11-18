# 助手确认卡片把非法 UTF-8 后缀与旧二进制内容当成完整预览

状态：open（修复与回归测试在 PR #538 中）
更新时间：2026-11-18

## 复现与影响

审查基线 `53882ca8`，使用临时目录中的真实 `RootFileSystem`，不访问 NAS：

- 提交 5 字节草稿 `safe\xff`，写入 Agent空间。卡片的 `after` 只有 `safe`，却没有 `binary` 或 `partial`，前端因此不要求勾选“有文件没有完整显示”。仅有 1–3 个非法字节的文件甚至显示成空文本。
- 用文本替换 65,540 字节、以非法 UTF-8 开头的旧文件。旧预览为空，卡片却带着 `beforePartial=true`，页面声称显示了旧文件的开头。
- 有效字符“猫”横跨预览边界时，真正未显示的字节数是整个字符的 3 字节；按文件大小减 64 KiB 计算会少计被截掉的字符前缀。

前两个定向测试在基线上失败；已有执行器测试与 CI 通过。

## 根因与修复

原预览函数为了容忍截断的 UTF-8 字符，会尝试删除末尾 1–3 字节，却没有区分真实 EOF 与预览边界。新的预览标记又把非空预览指针当成已成功显示文本。修复让函数接收原文件大小：完整文件有非法字节即不能作为文本预览；只有尚未到 EOF、且标准库 `utf8.FullRune` 确认尾部确实是不完整字符时才裁掉字符前缀。

卡片分别给出新旧内容的二进制标记与实际未显示字节数；旧二进制内容不再标为部分文本。未显示字节数用总大小减实际 UTF-8 预览长度计算，仍能在结束后的副本只保留开头时正确说明。页面显示这些事实，并保留新内容未完整显示时的额外确认。

## 验证

- Go：`go test -count=1 ./internal/assistantchanges`，覆盖短文件非法后缀、旧二进制内容、真实 UTF-8 边界、无效边界字节、空文件以及结束后留存的预览。
- Web：`npx vitest run src/assistant/cards.test.tsx`，覆盖旧二进制提示、服务端未显示字节数和勾选后又取消的确认门槛。
- 浏览器使用临时卡片数据挂载实际 `ApprovalCard`，核验多项提示、长文件名与窄窗口；不要求运行真实助手。

## 关联

- 实现：[执行器](../../internal/assistantchanges/executor.go)、[卡片](../../web/src/assistant/AssistantPanel.tsx)
- 测试：[预览回归](../../internal/assistantchanges/preview_test.go)、[卡片交互](../../web/src/assistant/cards.test.tsx)
- 契约：[AssistantChange](../../api/components/assistant.yaml)
- 规格：[AI 助手](../specs/ai-assistant.md#修改本人的文件)
- ADR：[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
