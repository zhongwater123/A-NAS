# 应用中心（internal/appstore/）

本目录是应用中心：内置清单（`catalog/`）、安装策略与计划渲染、产品服务一侧的编排（`center.go`），以及在容器代理（`cmd/anas-container-agent`）里运行的 Compose 任务（`engine/`）和两边的协议（`agent/`）。根目录的 [AGENTS.md](../../AGENTS.md) 仍然适用。权威说明：[应用中心规格](../../docs/specs/app-center.md)及其中链接的 ADR 0009、0010、0013、0022。

- **清单保持上游原样：** `catalog/` 是 CasaOS-AppStore 固定提交的原文（`catalog/NOTICE.md`），运行时不下载；分类、文字校订与生成的登录信息写在 `categories.go`、`copy.go`、`credentials.go`。
- **策略只收紧：** 代理只提供类型化操作（[不变量](../../docs/architecture/OVERVIEW.md#不变量) 3）；网络只从地址池分配、装后核对（不变量 15）；共享空间只给以应用身份运行的服务，组写进 Compose、默认只读（不变量 17）。设备、宿主机网络等例外要先有决定。
- **计划摘要就是授权：** 安装只带摘要，代理重新渲染比对。改了运行规则（身份、能力、挂载、日志）要升 `runtime.go` 的 `RuntimePolicy`，旧容器据此提示管理员重装，系统不自动重装（ADR 0022 决定 8）。
- **身份与顺序：** 代理从宿主机账号库读 `app-<id>`，不用调用方给的 UID/GID；先由代理检查冲突并预留，Host Agent 再建账号和目录，最后提交（[调查](../../docs/investigations/2026-10-27-app-install-preparation-order.md)）。
- **代理与产品服务版本可以不同：** `postinst` 不重启正在运行的代理。客户端忽略新代理多给的字段，代理拒绝不认识的请求字段；新增请求字段或操作要保留旧协议，并在[容器代理手册](../../docs/runbooks/install-container-agent.md#安装预留协议的升级顺序)写明升级顺序。
- **卸载保留数据：** 不用 `compose down --volumes`（会删命名卷），镜像只按记录的不可变 ID 非强制删除。
- **验证：** `go test ./internal/appstore/...`，Fake 与真实 Docker 共用 `engine/enginetest` 的契约表。改了挂载、身份核对或清理，另跑 CI 不跑的 `ANAS_DOCKER_INTEGRATION=1 ANAS_TEST_IMAGE=<本地镜像> go test -tags dockerintegration ./internal/appstore/engine`：宿主机的组、开发机较新的 Compose 都曾让测试漏掉 NAS 上的缺陷（[调查](../../docs/investigations/2026-10-14-app-containers-and-the-shared-acl.md)）。
