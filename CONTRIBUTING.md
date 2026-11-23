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
- 同时开几套预览栈、互不登出：Windows 上用 [fake-preview](.agents/skills/fake-preview/SKILL.md)，macOS 见 [Colima 预览](docs/development/environments/macos.md#fake-预览)。

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
- PR 解决某个 issue 时，在描述里写 `Closes #编号`。只解决了一部分时，把剩下的写成新的 issue，PR 仍然关闭原来的 issue，不留下没关联的 PR。
- 动手处理已有的 issue 之前，先看有没有开放的 PR 或新推送的分支在做同一件事（`gh pr list`、`git fetch`）；开 PR 之前再看一次。几个会话常常同时处理同一批 issue。
- PR 描述列出作者自行决定、没问过维护者的默认值与取舍（上限、阈值、措辞、行为），维护者可以只改其中一条。
- 推送前先 `git fetch`：维护者可能已经在 GitHub 上点过“Update branch”，或已经合并了 PR。先把远端合并进来再推，不强制推送（叠放 PR 的变基除外，见下）；PR 合并后才写的提交另开 PR。
- 改动影响到的文档随同一个 PR 更新：状态、规格、ADR、决定记录、调查或运行手册，规则见 [AGENTS.md 的“完工”](AGENTS.md#完工)与[文档系统](docs/README.md)。

### 评审

- 按字节数与近期修改次数（`git log --since`）衡量文件，不只看行数：超长的行让行数失真，改得最频繁的文件也是并行分支最常冲突的地方。
- 提新 issue 前先查开放的评审索引 issue（[#159](https://github.com/zhongwater123/A-NAS/issues/159)、[#267](https://github.com/zhongwater123/A-NAS/issues/267)、[#319](https://github.com/zhongwater123/A-NAS/issues/319)、[#357](https://github.com/zhongwater123/A-NAS/issues/357)）及其子 issue。范围内已有的 issue 用新证据重新定级，不因为已有 issue 就略过；结构上的热点即使已有 issue，也在评审总结里点出。
- 子代理或工具报出的高严重度问题，先自己复现核实再行动。CI 的“偶发失败”可能是两个 PR 合在一起的真冲突，先看随后的提交。
- 安全问题不写成公开 issue：交给维护者以私有的安全公告草稿记录，写缺陷与修复方向，不写利用步骤。

### 叠放 PR

改动大到一个 PR 评审不了时，可以拆成几个相互依赖的 PR，后一个以前一个的分支为基：

- 下面一层的三项 CI 检查都通过后，才能开上面一层。本地跑不了系统测试时，等 CI 的结果。
- 最多两层：下面一层在评审，上面一层是草稿。下面一层合并后，上面一层才转为正式 PR，这时才能开再上面一层。
- 依赖别的 PR 时，以它的分支为基，不把它的提交复制到自己的分支。
- 下面一层合并后，GitHub 删除它的分支，把上面一层改基到 `main`；作者把上面一层与 `main` 同步，CI 重跑。
- 下面一层改动后，由作者给上面各层变基、重跑 CI，并更新 PR 描述里的依赖与提交。本地叠了更多分支时，在最上面的分支运行 `git rebase --update-refs <新的基>`，中间各层的分支一起移动，再用 `git push --force-with-lease` 推送。

### 跨组件的功能

几个进程或服务接力完成的功能（例如剪辑中心：网页、剪辑服务、相册、Worker、Hermes），按下面做。2026-11-19 剪辑中心第一次实机验收时，8 个 PR 各自测过、各自评审过，接起来却在实验 NAS 上停了两处，每处都要用户跑 root 脚本才查清（[调查](docs/investigations/2026-11-19-montage-job-stalls-on-the-experimental-nas.md)）。

- **先通一条最简的链路**：第一个合入 `main` 的里程碑就从入口走到结果，中间各段可以是最简实现或替身；之后的 PR 往上加，每个合入后整条链路仍能走通，并有一个自动测试走它。
- **接缝有负责人**：规格或技术设计为每条跨进程接缝写明哪个 PR 或谁负责、两边的格式在哪里定义；两边的测试读同一份样例（[跨进程契约](docs/development/testing.md#跨进程契约)）。评审改了接缝一侧的 PR，要核对另一侧。
- **先实测再设计**：设计依赖第三方运行环境的行为（进程怎样起停、调度、超时、重试，例如 Hermes 的定时任务与会话），动工前在真实运行环境里实测；结果与几种做法的取舍写进决定记录或研究文档，交用户定。
- **合完再整体跑一次**：一组 PR 全部合入、交给部署之前，在合并后的 `main` 上跑一次带真实运行环境的系统测试（例如 AI 助手与剪辑规划要设 `ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME`），再在实验 NAS 上先用最小样本走一遍（[验收手册](docs/runbooks/accept-features-on-experimental-nas.md#前提与风险)）。
- **卡在哪要看得见**：规格写明每一步在等什么、页面怎样说；管理员不用 root 就能看到任务停在哪一步、为什么、等了多久（[规格模板](docs/specs/_TEMPLATE.md)的“边界与失败”）。
- **合并列车期间冻结**：一组相互依赖的 PR 依次合入期间，只有负责合并的人改这些分支；别的改动等列车合完，或先和负责人商量。

## 版权

仓库保留所有权利，见 [README 的“版权”一节](README.md#版权)。贡献者授权的做法还没定，见[版权人与贡献者授权](docs/decisions/2026-11-17-copyright-holder-and-contributor-grants.md)。
