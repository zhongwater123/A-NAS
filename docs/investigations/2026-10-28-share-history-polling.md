# 分享历史导致重复 IPC，最小化窗口仍下载完整对话

状态：open；修复、完整门禁、竞态检测、系统测试和上传前审查通过，实机验收尚未执行。
更新时间：2026-10-28

## 症状与复现

[issue #182](https://github.com/zhongwater123/A-NAS/issues/182) 报告维护成本随历史增长。通过真实 Store 创建、发布并撤回 24 条分享，再用独立上下文计数一次维护与列表请求，排除后台定时器干扰。

```bash
go test ./internal/shares -run 'TestRevokedHistoryDoesNotRepeatMaintenanceIPC|TestMaintenanceUsesOneAccountSnapshotAndRefencesAfterReopen|TestUnavailableAccountSnapshotStopsAcceptedWork' -count=1
cd web
npm test -- src/shares/polling.test.tsx
```

旧实现一次维护触发 24 次活动账号查询、24 次已确认栅栏重发；一次历史列表触发 49 次活动账号查询。前端第二轮对话请求仍使用 `after=0`，窗口最小化后 15 秒内继续发送 18 次列表、详情、任务与消息请求。

## 原因与修复边界

- 领域层只有逐用户授权接口，生产适配器每次都调用 Host Agent 的完整活动账号列表。增加请求/周期局部批量快照，维护和列表复用，后台任务的后续操作仍查询当前权限。
- 成功的发布栅栏没有本代次确认记录。现在只缓存成功确认，失败继续重试；服务/桥接重置后重新确认。批量账号查询失败时仍关闭已接受的预览并取消保存，避免性能优化跳过原授权失效处理。
- 已完成内容清理的历史仍检查发送者。现在直接跳过；保留规则和存储规模限制统一见[技术设计](../architecture/member-sharing.md#恢复与资源上限)。
- 窗口最小化只设置 DOM 的 `hidden`，并不会卸载 React 组件；本地 Kiosk 的页面可见性也不会随窗口改变。现在显式传递桌面窗口可见性，停止窗口内容轮询；桌面通知按规格继续工作。
- 每轮对话重新把游标设为零。现在从已加载消息末尾继续，接收者变化时重置；页面失败不提交半截游标，重试不丢消息。

## 验证

- 服务端历史回归由失败转为通过；批量账号快照每轮一次，成功栅栏不重复，重开 Store 后再次确认。
- 新增账号查询失败回归发现并修复漏取消任务的问题，相关分享领域、分享服务、文件桥测试通过。
- 前端两个最小回归从失败转为通过；增加 100 条分页边界的失败重试检查，全部分享组件 28 项测试、类型检查及 lint 通过。最小化测试延长至 60 秒，确认窗口内容停止轮询、全局未读仍按 30 秒同步，恢复后从旧游标继续。
- `make check-steps` 全部目标及 `make race-test` 通过；可丢弃 Debian 13/systemd 系统测试 332 PASS、0 FAIL。上传前项目规范与需求符合性审查均无剩余发现。

## 关联

- [账号快照](../../internal/shares/authorization.go)、[维护与栅栏](../../internal/shares/store.go)、[生产适配器](../../internal/shareservice/service.go)
- [服务端回归](../../internal/shares/polling_test.go)、[前端回归](../../web/src/shares/polling.test.tsx)
- [分享规格](../specs/member-sharing.md)、[私聊](../../web/src/shares/Conversation.tsx)、[窗口可见性](../../web/src/desktop/Desktop.tsx)

## 审查补充

桌面未读徽标属于全局界面，30 秒同步只随网页隐藏暂停，不随分享窗口关闭或最小化暂停，保证其他设备的已读状态最终同步。窗口内容仍暂停轮询，增量对话游标不变。
