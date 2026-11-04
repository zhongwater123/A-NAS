# 并发登录绕过退避与密码计算内存边界

状态：resolved；问题仅取决于进程内准入、键表示与计算顺序，回归测试已完整复现并验证，实验 NAS 无额外证据可补充。

PR #370 的 `f71553f` 在密码计算排队前检查退避。多个同客户端请求可以一起通过检查；之后即使已有失败触发退避，队列仍继续校验并写审计。此外，失败记录保留完整用户名，诱饵首次生成发生在计算槽位之外。

## 复现证据

新增测试先在未修改的实现上运行：

```bash
go test ./internal/accounts -run 'TestConcurrentFailuresCannotBypassSignInBackoff|TestFailureKeysBoundUsernameMemory|TestFirstDecoyHashSharesPasswordLimit|TestCancelledDecoyDoesNotInitialize' -count=1
```

- 先累计 4 次同账号失败或 19 次轮换账号失败，再提交 10 个并发请求，用 `testing/synctest` 和通道阻塞密码校验，确保所有请求都已进入排队。实际得到 10 次校验失败、0 次退避拒绝，应为 1 次失败、9 次退避拒绝。
- 接近 1 MiB 的用户名在键中占用 1,048,477 字节，应与用户名长度无关。
- 两个正常密码校验占满计算槽位时，首次未知账号请求仍调用诱饵初始化；已取消的请求也会初始化诱饵。

## 修复边界

按客户端持有一个可取消的 `x/sync/semaphore` 槽位，从排队后的退避检查直到结果计数及审计结束。该客户端在等待全局计算槽位期间没有另一登录能更新失败计数，因此不会使用过期的准入判断。客户端排队发生在全局计算闸门之前，其他客户端仍可利用空闲计算槽位；引用数在请求取消或完成后释放，最后一个请求离开时删除客户端槽位。

失败键只保留账号名的 SHA-256 摘要，仍区分不同超长账号名并累计客户端总失败数。首次诱饵生成移到全局计算槽位内，与随后的校验顺序执行，不递归获取槽位。

回归测试还覆盖等待结束后的第二批并发请求，检查真正执行的哈希次数与失败审计条数；客户端排队测试核对取消释放、其他客户端准入与空闲状态清理。

修复后 `go test -race -count=3 ./internal/accounts` 通过，账户包 golangci-lint 报告 `0 issues`。

## 关联

- [局域网 Web 访问规格](../specs/lan-web-access.md)
- [登录准入与计算限制](../../internal/accounts/signin.go)
- [登录入口](../../internal/accounts/service.go)
- [并发登录回归测试](../../internal/accounts/signin_test.go)
- [内存与排队边界测试](../../internal/accounts/signin_internal_test.go)
- [PR #370](https://github.com/zhongwater123/A-NAS/pull/370)、[issue #175](https://github.com/zhongwater123/A-NAS/issues/175)
