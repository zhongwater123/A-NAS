# 参与 A-NAS 开发

本页写给开发者。AI Agent 从 [AGENTS.md](AGENTS.md) 开始；它是唯一的项目指令文件，本页与它不一致时以它为准。

## 开发环境

产品的 Go 服务面向 Linux。开发机用 Windows 加 WSL2，或 macOS 加 Colima，依赖都装在虚拟机自己的目录里；按机器选择[本地开发环境](docs/development/LOCAL_ENVIRONMENT.md)，Windows 上的专用 WSL 用户与环境检查脚本也在那里。

- Go 版本见 `.go-version`，模块路径是 `github.com/zhongwater123/A-NAS`（[ADR 0001](docs/adr/0001-use-go-for-product-services.md)）。
- Node 版本见 `.nvmrc`，网页桌面用 React、TypeScript、Vite 与 npm lockfile（[ADR 0003](docs/adr/0003-use-react-typescript-for-web-desktop.md)）。
- 相册的本地 AI Worker 用 Python，模型、运行环境与评估见 [AI 开发与评估](docs/development/ai-development.md)。
- 构建需要 gcc（cgo SQLite）。

## 在本地运行

用模拟的宿主机启动内嵌网页桌面的产品服务。`ANAS_HOSTSTATE_MODE` 没有默认值：开发时写 `fake`，NAS 上由 `anas-api.service` 设为 `agent`，不设置时服务拒绝启动。

```bash
make web-build
ANAS_HOSTSTATE_MODE=fake go run ./cmd/anas-api
```

- 浏览器打开 `http://127.0.0.1:8080/`。服务只监听回环地址，可用 `ANAS_HTTP_ADDR` 换成其他回环地址与端口。全部变量及其校验见 [`cmd/anas-api/config.go`](cmd/anas-api/config.go)，接口契约见 [OpenAPI](api/openapi.yaml)。
- 终端默认关闭，要测试时加 `ANAS_TERMINAL=enabled`。本地模拟模式下 Shell 以开发进程用户运行，边界见[终端规格](docs/specs/web-terminal.md)。
- 桌面上的“Docker”应用在模拟模式下使用确定性的 Fake 容器；连接真实 Docker 需要容器代理，见 [ADR 0009](docs/adr/0009-use-docker-engine-through-a-dedicated-container-agent.md) 与[安装手册](docs/runbooks/install-container-agent.md)。
- 只改前端时运行 `cd web && npm run dev`，Vite 把 `/api` 和 `/healthz` 代理到本地的 Go 服务。
- 同时开几套预览栈、互不登出：Windows 上用 [fake-preview](.claude/skills/fake-preview/SKILL.md)，macOS 见 [Colima 预览](docs/development/environments/macos.md#fake-预览)。

## 检查

```bash
make check
```

`make check` 运行全部门禁：接口契约，前端的类型检查、lint、测试与生产构建，格式，文档，运维脚本与工作流，第三方收录核对，Go vet、golangci-lint 与测试，漏洞公告，Python lint 与 AI Worker 测试，最后构建四个 Linux 命令和 Debian 软件包。构建产物写入被 Git 忽略的 `build/`。

改了哪类文件要跑哪些检查（纯文档、接口契约、网页代码、并发代码、服务单元与权限），以 [AGENTS.md 的“验证”](AGENTS.md#验证)为准；各项检查的细节、系统测试与 CI 见[测试与验证](docs/development/testing.md)。提交前可以运行 `make secrets-check`，用 Gitleaks 检查有没有提交密钥。

## 提交与 PR

- `main` 只能经 PR 合并，必须通过 CI 的三项检查：“Project checks”、“Go tests with the race detector”与“System test (Debian 13, systemd)”；分支要先与 `main` 同步。
- 只能用合并提交合并，不能 squash 或 rebase，PR 里的各个提交原样进入 `main`。合并后 GitHub 自动删除 PR 的分支。
- 一个功能或里程碑默认一个 PR，各步骤写成单独的提交。不依赖其他 PR 的工作单开 PR，以 `main` 为基。
- 提交信息用英文，格式为 `type(scope): summary`，常用的 type 有 `feat`、`fix`、`docs`、`test`、`build`、`refactor` 与 `ci`；正文写清为什么这样改。
- PR 解决某个 issue 时，在描述里写 `Closes #编号`。
- 改动影响到的文档随同一个 PR 更新：状态、规格、ADR、决定记录、调查或运行手册，规则见 [AGENTS.md 的“完工”](AGENTS.md#完工)与[文档系统](docs/README.md)。

### 叠放 PR

改动大到一个 PR 评审不了时，可以拆成几个相互依赖的 PR，后一个以前一个的分支为基：

- 下面一层的三项 CI 检查都通过后，才能开上面一层。本地跑不了系统测试时，等 CI 的结果。
- 最多两层：下面一层在评审，上面一层是草稿。下面一层合并后，上面一层才转为正式 PR，这时才能开再上面一层。
- 依赖别的 PR 时，以它的分支为基，不把它的提交复制到自己的分支。
- 下面一层合并后，GitHub 删除它的分支，把上面一层改基到 `main`；作者把上面一层与 `main` 同步，CI 重跑。
- 下面一层改动后，由作者给上面各层变基、重跑 CI，并更新 PR 描述里的依赖与提交。本地叠了更多分支时，在最上面的分支运行 `git rebase --update-refs <新的基>`，中间各层的分支一起移动，再用 `git push --force-with-lease` 推送。

## 版权

仓库保留所有权利，见 [README 的“版权”一节](README.md#版权)。贡献者授权的做法还没定，见[版权人与贡献者授权](docs/decisions/2026-11-17-copyright-holder-and-contributor-grants.md)。
