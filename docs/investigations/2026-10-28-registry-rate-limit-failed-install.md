# 镜像仓库一次限流就让应用安装失败

状态：fixed，待实机验收
更新时间：2026-10-28

## 症状与影响

2026-10-28，管理员在实验 NAS（`4ec62e17c3b1`）的应用中心重新安装 Immich（`immich-server` 与 `immich-machine-learning` v2.7.2、`ghcr.io/immich-app/postgres` 与 `docker.io/valkey/valkey` 按摘要固定）。几层镜像并行下载时，任务输出以下面几行结束，安装失败并回滚：

```
3b47e31dda85 Downloading 0B
toomanyrequests: retry-after: 285.983µs, allowed: 44000/minute
已清理应用容器、匿名卷和未使用的镜像；命名卷与应用数据已保留
```

GHCR 对其中一个请求返回 429，要求的等待不到 1 毫秒。实验 NAS 在公司共享的 NAT 之后，镜像仓库的限流会再次出现；任何一次 429、5xx 或网络中断都会让整次安装失败，较大的镜像越难装上。

## 最小复现

1. 一台能运行 Docker 与 Compose 的开发机，Docker 守护进程就在本机（测试从 127.0.0.1 上的镜像仓库拉取）。
2. 在仓库根目录运行：

   ```bash
   ANAS_DOCKER_INTEGRATION=1 go test -tags dockerintegration -count=1 -v \
     -run 'TestRealDocker(PullFailures|RetriesARateLimitedPull|FailedPullKeepsWhatFinished)' ./internal/appstore/engine
   ```

3. [测试](../../internal/appstore/engine/pull_docker_integration_test.go)在 127.0.0.1 上启动一个 TLS 镜像仓库，按仓库名返回 429（清单或镜像层）、503、连接重置、404、401 或摘要不符的镜像层，用安装所用的 Compose 命令拉取，并打印 Docker 的原话。镜像名带时间戳，测试结束按名删除，不清理其他资源。修复前的引擎没有单独的拉取步骤：`up` 遇到同样的错误即失败，不重试。

## 观察事实

- [`engine.go`](../../internal/appstore/engine/engine.go) 以 `docker compose up --detach --remove-orphans` 启动应用，缺少的镜像由 `up` 隐式拉取；[`CLIRunner`](../../internal/appstore/engine/engine.go) 只返回 `exit status 1`，错误原因只在输出里。
- 实验 NAS 的输出在第一个 429 之后即结束，没有重试的迹象。开发机（Docker 29.1.3 的 containerd 镜像存储、Compose 2.40.3）上，测试镜像仓库的访问日志显示 containerd 对同一个 429 在约 1 秒内请求了 6 次后放弃，远短于按分钟计的限流窗口。
- 同一个 Compose 项目里，一个服务拉取失败会中断其他服务的拉取：清单返回 429 的服务失败后，另一个服务的拉取打印 `Interrupted`，事后 Docker 中没有它的镜像。
- 两种镜像存储对同一种失败措辞不同。图驱动存储（实验 NAS 的输出）打印镜像仓库的错误码，例如 `toomanyrequests: retry-after: …`、`manifest unknown`；containerd 存储打印 HTTP 状态或只打印消息：

  | 失败 | containerd 存储打印 |
  |---|---|
  | 清单 429 | `unexpected status from HEAD request to …: 429 Too Many Requests` |
  | 镜像层 429 | `error from registry: retry-after: 285.983µs, allowed: 44000/minute` |
  | 503 | `…: 503 Service Unavailable` |
  | 连接重置 | `read: connection reset by peer` |
  | 清单不存在 | `…: not found` |
  | 需要登录 | `pull access denied, repository does not exist or may require authorization: …` |
  | 镜像层摘要不符 | `commit failed: unexpected commit digest …, expected …` |

- 本机已有的镜像不访问镜像仓库：`compose pull --policy missing` 跳过它们（包括只在本机构建、仓库中不存在的镜像）。`up --pull never` 遇到缺少的镜像会在建好项目网络后以 `No such image` 失败。
- 拉取失败时项目还没有容器。安装开始写入的待清理记录为空，[清理流程](../../internal/appstore/engine/cleanup.go)只能从容器取得镜像 ID，所以回滚没有删除任何镜像；输出中“已清理……未使用的镜像”一句与实际不符。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| Docker 或 Compose 自己会重试 429 | 实验 NAS 的输出；测试镜像仓库的访问日志 | 否。实验 NAS 的输出没有重试的迹象；containerd 存储在约 1 秒内重试数次后放弃 |
| 回滚删除了已下载的镜像 | 阅读清理流程；拉取失败时的待清理记录 | 否。没有容器，清理拿不到任何镜像 ID；丢失的是被中断的拉取 |
| 一个服务失败会中断其他服务的拉取 | 两个服务一起拉取，其中一个返回 429 | 是 |
| 可以从输出区分可重试与不可重试的失败 | 在测试镜像仓库上逐类复现 | 是，但两种存储措辞不同，需按两套措辞匹配 |

## 根因

镜像由 `compose up` 隐式并行拉取，引擎不重试；Docker 不等待镜像仓库的限流窗口，Compose 在一个服务失败时中断其余服务的拉取。一次 429 因此让整次安装失败，正在下载的其他镜像也一并中断。

## 修复与回归证据

- 修复：[`pull.go`](../../internal/appstore/engine/pull.go) 在写入待清理记录和 `up` 之前，逐个服务运行 `compose pull --policy missing <服务>`，边转发输出边逐行判断失败类型。限流、5xx、连接重置或超时等待后重试同一服务：从 10 秒起每次加倍，最长 2 分钟，不短于镜像仓库的 `retry-after`；每个服务最多 5 次。重试前在任务输出写一行“<服务>：镜像仓库限流，N 秒后重试（第 k 次）”。镜像或标签不存在、未授权或被拒绝、摘要不符和认不出的错误不重试；同一输出中两类都出现时按不可重试处理。等待超过 2 分钟或会超过安装时限时立即失败，等待中停止代理立即结束。[`engine.go`](../../internal/appstore/engine/engine.go) 中拉取失败不回滚：没有容器，也没有能证明归属的下载，已完成的镜像留给下次安装（与[系统盘调查](2026-10-27-app-system-disk-growth.md)的归属规则一致）。随后以 `up --pull never` 启动，`up` 不再访问镜像仓库。模拟模式的 Compose（[`fake.go`](../../internal/appstore/fake/fake.go)）支持 `pull`。
- 手写原因：重试循环只有一个计时器加 `select`；`cenkalti/backoff/v5` 遇到 `RetryAfterError` 时以它替换并重置退避，GHCR 不到 1 毫秒的提示会让每次等待都退回起点，满足“不短于退避也不短于 `retry-after`”仍需自己包装，因此没有引入依赖。失败分类读的是 Docker CLI 的文字，没有库可用，措辞由上面的集成测试钉住。
- 单元测试：[`pull_test.go`](../../internal/appstore/engine/pull_test.go) 用假 Compose 覆盖两种存储措辞的限流、5xx、重置与超时在一次重试后安装成功；GHCR 的微秒级 `retry-after` 仍等满退避、退避加倍、更长的 `retry-after` 被遵守；持续限流在 5 次拉取后失败、只宣布 4 次重试、不回滚、不删镜像、不留待清理记录；9 类不可重试的输出只拉取一次；等待超过上限或安装时限时立即失败；等待重试时停止代理立即结束；多服务应用在第二个服务失败时不再继续，也不删除已下载的镜像。[`engine_test.go`](../../internal/appstore/engine/engine_test.go) 等既有测试改为断言“先逐个拉取，再 `up --pull never`”。`go test -race ./internal/appstore/...` 通过。
- 集成测试：2026-10-28 在 WSL2（Docker 29.1.3 containerd 存储、Compose 2.40.3）上，复现步骤中的三个测试通过：8 类失败被归为预期的类型；限流直到引擎宣布重试的镜像仓库在一次重试后拉取成功；第二个镜像不存在的应用安装失败后，第一个镜像仍在 Docker 中，项目没有容器和网络，没有待清理记录。同一次运行中，既有的 `TestRealDockerRunsAServiceAsItsPlan` 与清理测试的回滚分支通过，后者的应用使用本机构建的镜像，证明本机已有的镜像跳过拉取；清理测试的成功分支需要读取 Docker 日志文件的权限，以普通用户运行时失败，与本次修改无关。

## 后续工作

- 部署容器代理后，按[实机验收手册](../runbooks/accept-features-on-experimental-nas.md#应用中心与-docker)重新安装 Immich，核对输出中的重试行与最终结果。
- 集成测试只在 containerd 存储上运行过；图驱动存储的措辞取自实验 NAS 的输出和单元测试。Docker 升级或实验 NAS 换镜像存储后，重跑复现步骤中的测试。

## 关联

- 规格：[应用中心](../specs/app-center.md#镜像下载与重试)
- ADR：[0009 Docker Engine 与专用容器代理](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)
- 运行手册：[已部署功能的实机验收](../runbooks/accept-features-on-experimental-nas.md#应用中心与-docker)
- 调查：[应用日志与卸载残留持续占用系统盘](2026-10-27-app-system-disk-growth.md)
