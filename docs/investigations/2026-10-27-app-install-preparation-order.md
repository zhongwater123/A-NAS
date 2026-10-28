# 应用安装拒绝发生在宿主机准备之后

状态：open，本地回归已复现并修复，实机行为待验证
更新时间：2026-10-27

## 症状与复现

[#185](https://github.com/zhongwater123/A-NAS/issues/185) 的三个问题在 `cb62d69` 上均有可重复的测试信号：

```bash
go test ./internal/appstore/engine ./internal/appstoreapi -run 'Test(UnknownAppDoesNotReserveAnIdentity|RejectedReinstallDoesNotPrepareTheHost|DockerInspectorReservesStoppedContainerPorts)$' -count=1
```

- 对目录外 ID 请求计划或安装，记录 Host Agent 调用，得到 `unknown app reserved 1 identities`。
- 安装 Navidrome 后再请求同一安装，得到 409，但 Host Agent 的准备记录仍新增一条。
- HTTP 测试服务器模拟 Docker：列表的 Ports 为空，inspect 的 `HostConfig.PortBindings` 包含 8000；exited/created 容器的端口没有进入冲突检查，pyLoad 可错误地抢占 FileDrop 的端口。

## 根因与修复边界

产品服务在容器代理确认目录成员资格之前保留 UID，且在代理执行冲突与忙碌检查之前调用 `PrepareApp`。简单交换安装与准备的调用顺序也不正确：原安装接口立即启动后台 Compose，账号和目录可能尚未就绪。

使用同一安装引擎接受任务并等待准备提交，复用既有作业记录、串行执行、停止和重启恢复机制。接受时在任务锁内检查容器快照与冲突；预留以随机的一次性令牌绑定应用和计划身份。准备失败时取消，客户端消失时一分钟超时，准备提交读取实际 Linux 账号再核对身份。容器代理无需取得 Host Agent socket 权限。

Docker 列表不足以描述停止容器的保留端口；改为读取每个容器的 HostConfig，并保留列表中的实际动态端口。inspect 失败时整个检查失败，避免漏掉不可读取容器的占用。目录中的 FileDrop 与 pyLoad 仍可以各自安装，但 8000 端口冲突时不能共存。

接受之前拒绝安装不产生账号/目录副作用；接受后准备过程若部分失败，可能已有宿主机数据，这些数据不会自动删除。已接受任务的错误由作业记录保留。

## 验证与关联

- [应用中心规格](../specs/app-center.md#执行安装)
- [产品 API 回归](../../internal/appstoreapi/preflight_test.go)：未知 ID、拒绝、准备失败和取消。
- [Docker 检查回归](../../internal/appstore/engine/docker_test.go)：停止与 created 容器绑定端口仍与目录应用冲突。
- [预留回归](../../internal/appstore/engine/preparation_test.go)：提交前无 Compose、身份核对、一次性令牌、超时与停止。
- [代理协议回归](../../internal/appstore/agent/agent_test.go)：预留时账号可尚未创建，提交必须使用实际账号。
- [容器代理升级顺序](../runbooks/install-container-agent.md#安装预留协议的升级顺序)

## 审查补充

停止容器的端口范围按区间保留；List 后消失的容器跳过。运行中和暂停的容器使用 List 的已分配端口，减少 Inspect。Docker 检查在任务锁外执行，任务代次变化后拒绝旧结果；准备提交与超时在同一锁下确定结果，已过截止时间的提交拒绝。
