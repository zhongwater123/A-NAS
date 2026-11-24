# 媒体 Worker 拒绝描述符过多的请求时，客户端偶发报“不可用”

状态：resolved
更新时间：2026-11-20

## 症状与影响

2026-11-20 PR #610 的 CI 运行 44655863597 中，“Go tests with the race detector”作业失败，只有 `TestEditingRequestsRefuseFieldsTheyDoNotKnow` 一项：

```text
assemble with three descriptors = FFmpeg worker is unavailable: write unix @->@: write: broken pipe
```

同一提交在其他 PR 上通过，重跑也通过。这个作业是强制检查，偶发失败会拦住无关的 PR（[#582](https://github.com/zhongwater123/A-NAS/issues/582)）。产品中，正常客户端不会发出超过 Worker 上限的描述符；但凡是 Worker 应回答“请求无效”的这类请求，调用方都可能偶发收到“Worker 不可用”，把调用方的错误说成服务故障。

## 最小复现

1. Colima `anas` 虚拟机（开发机 C），Go 1.27.3，`go test -race -count=200 ./internal/ffmpegworker/ -run TestEditingRequestsRefuseFieldsTheyDoNotKnow`：200 次全部通过，空闲机器上很难碰到。
2. 定向复现：像 [`unixmsg.WriteBodyFiles`](../../internal/unixmsg/unixmsg.go) 那样先发长度与三个描述符，暂不发正文，读 200 ms。
3. 修复前 Worker 立即回答并关闭连接，随后写正文得到 `EPIPE`；期望 Worker 等正文读完再回答。见 `TestAWorkerReadsARefusedRequestBeforeAnswering`，修复前 3/3 失败。

## 观察事实

- 客户端的 [`send`](../../internal/ffmpegworker/client.go) 用 `WriteBodyFiles` 发请求：长度与描述符在一次 `sendmsg` 中，正文另用一次 `write`。写失败一律报 `ErrUnavailable`，不再读回答。
- Worker 的 [`Serve`](../../internal/ffmpegworker/server.go) 只给两个描述符的空间。三个描述符时内核截断（`MSG_CTRUNC`），旧的 `readBody` 读完长度就返回 `ErrTruncated`，不读正文；`Serve` 回答 `invalid_request` 后关闭连接（提交 `fb8c8971`）。
- Linux 的 Unix 流套接字：对端关闭后再写得到 `EPIPE`；已写进本端接收队列的回答仍能读到。所以只有当 Worker 的回答与关闭落在客户端 `sendmsg` 与 `write` 之间时才失败，竞态检测放慢调度后更容易碰上。
- 同一函数原来的单元测试写明“被拒的消息不读到末尾，连接作废”，即这是有意为之，而不是遗漏。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| Worker 在客户端写完正文前回答并关闭 | 只发长度与描述符，看 Worker 是否在正文到达前回答 | 证实：修复前立即收到回答 |
| 原测试的 `invalid_request` 断言本身有误 | 正文写完后再读回答 | 否定：回答是 `invalid_request` |

## 根因

`unixmsg` 的消息分两次写入，接收方却在只读了前一半时就拒绝。拒绝本身没错，错在没读完：对一个双向的请求-回答协议，接收方在回答前必须读完对方正在发送的消息，否则发送方的后半次写入可能撞上已关闭的连接，看不到回答。

## 修复与回归证据

- 修复：[`readBody`](../../internal/unixmsg/unixmsg.go) 遇到被截断的描述符时，仍读完长度与正文（受 `limit` 约束），再报 `ErrTruncated`。连接因此停在消息边界上，`Serve` 的拒绝回答在客户端写完之后才发出。分享桥读到这类消息时不回答、直接关闭，行为不变。
- 测试（Colima `anas`，`-race`）：

| 测试 | 修复前 | 修复后 |
|---|---|---|
| `TestAWorkerReadsARefusedRequestBeforeAnswering` | 3/3 失败：正文到达前收到回答 | `-count=50` 全部通过 |
| `TestAMessageCarriesSeveralDescriptorsInOrder`（被拒后同一连接再读一条） | 3/3 失败：读到 `message is too large` | `-count=50` 全部通过 |
| `TestEditingRequestsRefuseFieldsTheyDoNotKnow` | 本机 200 次 0 失败，CI 偶发 | `-count=50` 全部通过 |

## 后续工作

- 超过 `limit` 的消息仍不读正文、直接关闭：读完可能要几 GiB。媒体 Worker 在这种情况下不回答，客户端发送前也会按同一上限拒绝，所以不受影响。

## 关联

- 代码：[`unixmsg.go`](../../internal/unixmsg/unixmsg.go)、[`server.go`](../../internal/ffmpegworker/server.go)、[`client.go`](../../internal/ffmpegworker/client.go)
- 测试：[`unixmsg_test.go`](../../internal/unixmsg/unixmsg_test.go)、[`edit_test.go`](../../internal/ffmpegworker/edit_test.go)
- 规格：不涉及
- ADR：不涉及
- 运行手册：不涉及
