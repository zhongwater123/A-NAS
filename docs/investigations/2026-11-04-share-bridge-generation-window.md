# 产品服务重启后两秒内新建的分享显示“已撤回”

状态：fixed
更新时间：2026-11-05

## 症状与影响

2026-11-04 main 合并 #382（`0e52765a`，只改文档）后，CI 系统测试只有一项失败：“member sharing works through production service sandboxes”。新建的分享立即变为 `failed`，错误为 `Unable to prepare share: share withdrawn`，界面显示“分享已撤回，任务停止”，而这个分享没有被撤回。同一检查在此前的 main 运行中都通过（#381、#370、#369 等）。

修复前产品中有同一窗口：产品服务重启（升级或 `Restart=on-failure`）后约两秒内，成员新建的分享或开始的保存会失败，并显示错误的“已撤回”。[#362](https://github.com/zhongwater123/A-NAS/issues/362) 记录了此前三次相同的 CI 失败。

## 根因

- 分享服务经产品服务的分享桥（[`bridge_linux.go`](../../internal/sharefiles/bridge_linux.go)）读写文件。每个桥请求带握手时得到的代次；产品服务每次启动都生成新代次（[`NewLocal`](../../internal/sharefiles/backend.go)），对旧代次的请求一律回答 `inactive`。这是必要的：重启丢失了已撤回分享的隔离记录，分享服务必须重新握手并重新隔离。
- 分享服务的 supervisor（[`service.go`](../../internal/shareservice/service.go)）每 2 秒检查一次桥，发现代次过期后才中断旧任务并重新握手。两次检查之间分享接口照常回答，`/api/v1/shares/members` 返回 200，但准备副本的第一个桥请求得到 `inactive`。准备过程中 `ErrInactive` 只可能来自桥的代次检查，[`Store.prepare`](../../internal/shares/store.go) 却把它记为 `share withdrawn`。
- 系统测试为载入存储记录重启了产品服务，约 1.1 秒后烟测就新建分享。烟测只等 `/shares/members` 回答，这不能说明桥已重新握手。此前通过的运行间隔 1.3–1.7 秒；是否落进窗口取决于 supervisor 计时器的相位，所以时过时不过。

## 修复与回归证据

[系统测试](../../scripts/system-test.sh)载入记录时先停分享服务、再停产品服务，之后按安装器的顺序先启动产品服务、再启动分享服务。分享服务启动时与当前代次握手，此后到烟测之间没有服务重启，烟测等到的 `/shares/members` 回答就说明桥可用。

分享服务现在在每次已认证请求进入领域处理前检查桥及 Host Agent 的代次；失效时返回 `503 shares_unavailable`，创建与保存不会被接受，也不会占用请求幂等键，同时唤醒 supervisor 立即重新握手。旧处理器退役后拒绝迟到请求，已接受的任务与内容流仍按既有中断规则停止，不等待慢客户端下载完才恢复。周期检查仍用于没有请求时的恢复。

预检查不能保证服务在之后不再重启。文件桥把过期代次改为独立的 `generation_changed`，领域任务报告“文件服务已重启，请重新发起操作”；真正的撤回仍为 `inactive`。旧代次不得重新握手后重放写入，已完成文件保留、未知提交结果仍遵循原有确认规则。

确定性复现命令：`go test ./internal/shareservice -run TestRestartRejectsCreateUntilBridgeRecovers -count=1`。测试将周期检查设为一小时，在首次就绪后更换桥代次，立即创建分享。修复前实测返回 202，随后得到 `failed`、`preparedBytes: 0` 与 `Unable to prepare share: share withdrawn`；修复后要求首次返回 503，且不等待周期检查即可恢复，用原请求键重试后发布成功。

真实 Unix socket 测试另覆盖准备期间代次失效的错误分类，以及旧代次不能提交；既有撤回栅栏测试继续要求真正的撤回拒绝后续发布。系统测试在产品服务重启后再次执行创建、读取副本、保存和撤回全流程，采用独立请求键和目标目录，避免误用首次烟测的幂等结果。失败时控制台额外输出分享服务与产品服务的日志尾部，完整 journal 仍作为产物保留。

## 后续

- 发布状态见[当前状态](../status/CURRENT.md)，实机补充验收见[成员分享](../runbooks/accept-features-on-experimental-nas.md#成员分享)。

## 关联

- [分享生产链路烟测](../../scripts/smoke-member-sharing.py)、[分享服务单元](../../deploy/systemd/system/anas-shares.service)
- [2026-10-15：分享服务接入后，相册会话查询与服务恢复失败](2026-10-15-member-sharing-service-startup.md)
- [技术设计](../architecture/member-sharing.md)、[ADR 0023](../adr/0023-isolate-member-share-copies-in-a-dedicated-service.md)
- [服务就绪回归](../../internal/shareservice/recovery_test.go)、[真实文件桥回归](../../internal/sharefiles/integration_linux_test.go)、[撤回栅栏](../../internal/sharefiles/fence_test.go)
