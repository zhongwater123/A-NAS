# A-NAS agent guide

## 开工

1. 先检查 `git status` 和任务涉及的路径。
2. 延续既有计划、判断优先级或接手未完成工作时，读 `docs/status/CURRENT.md`。
3. 涉及产品概念、领域命名或资源身份时，读 `CONTEXT.md`。
4. 文档类型或事实归属不明确时，使用 `docs/README.md` 路由；其余情况只加载下面与任务匹配的文档。

## 局部规则

改下面这些目录前，先读其中的 AGENTS.md：它们写着在那里必须遵守的约定，Claude Code 读那里的文件时会自动加载，Codex 只在会话从那个目录启动时才加载。

- [web/AGENTS.md](web/AGENTS.md)：Web 桌面（生成的接口类型、界面基础组件、样式、测试）。
- [api/AGENTS.md](api/AGENTS.md)：产品 API 契约。
- [internal/hostops/AGENTS.md](internal/hostops/AGENTS.md)：Host Agent 的 root 一侧，适用于 `.golangci.yml` 中 `host-agent` 规则列出的全部包。
- [ai/assistant/AGENTS.md](ai/assistant/AGENTS.md)：AI 助手 VNAS。

## 文档路由

- 组件边界、数据流或系统不变量：读 `docs/architecture/OVERVIEW.md` 和相关 ADR。
- 查或记录一个决定：难以逆转的技术决定在 `docs/adr/`；用户作出的其他决定和等用户定的事在 `docs/decisions/`，分界见其 README。
- 新功能或行为变化：读或创建 `docs/specs/` 中的规格。
- 难复现、跨边界或可能复发的缺陷：先查 `docs/investigations/`，再更新或创建调查记录。
- 安装、部署、恢复、磁盘或其他高风险操作：使用 `docs/runbooks/` 中的手册。
- 产品定位：读 `README.md`；早期产品背景、历史研究或 2026-10-06 的决策盘点：按需读 `PROJECT_CONTEXT.md`。
- 本机工具、WSL 或 Colima 问题：从开发环境入口选择对应平台，读 `docs/development/LOCAL_ENVIRONMENT.md`。

## 不重复造轮子

- 写基础设施代码前，先用成熟库、标准库功能或代码生成；仍决定手写时，在 PR 中写明原因。
- 范围：解析器与编解码；接口类型与客户端（从 `api/openapi.yaml` 生成，[#220](https://github.com/zhongwater123/A-NAS/issues/220)）；数据获取；对话框、菜单、提示等界面基础组件（已依赖 `radix-ui`，[#195](https://github.com/zhongwater123/A-NAS/issues/195)）；日期与数字格式化（`Intl`）；打包与单元校验（[#189](https://github.com/zhongwater123/A-NAS/issues/189)）。
- 仍手写的二进制解析器要有 fuzz 测试（[#223](https://github.com/zhongwater123/A-NAS/issues/223)）。
- 评审把手写的轮子记为违反本条，而不是一般的偏差。
- 集成的开源项目（如 Hermes Agent）出了问题，先看上游的服务端、官方客户端与测试怎么处理，照着做，并在修复与文档中写明依据；上游没有办法时才自己设计。
- 换上一个库之前，测出新旧两种做法在最坏情况下的开销：库也可能比手写的更贵。

## 与维护者协作

- 讨论方案时，部分同意不等于开工：等指挥你的人明确说“开始做”，再改代码或同步分支。为方案做的只读调查可以先做。
- 动手前估计规模：要超过一小时或要新加基础设施的工作，先用一两句话说明规模和更轻的做法，让对方选。
- 设定的达标线是为了保护目标，不是缩小目标：某一部分没达标时，补数据、换方法或先问，不擅自退回兜底，也不把自己的取舍说成对方的决定。
- 方案按长期结果选，直说代价。迁移成本、避免与开放的 PR 冲突都不是选弱方案的理由；大迁移配上机械化的验证。
- 下载模型、数据集等大文件前，先说明来源与大小。
- 长时间的评估放到后台运行，不耽误开发，被问到时读中间结果；多个模型逐个评完再评下一个，不交错，也不与别的重任务同时跑。

## 验证

- `make docs-check` 和 `make check` 不访问实验 NAS 或生产环境，只写可丢弃的构建产物；可直接运行、修复当前变更导致的失败并重跑相关检查。
- `make check` 要联网：安装 Go 与 npm 依赖，`vuln-check` 还要下载 Go、PyPI 与 npm 的漏洞公告。所以新公告发布后，它可能在代码没变时失败。这时先升级受影响的 Go 版本或依赖，即“有新公告就先修”。
- 纯文档变更运行 `make docs-check`。
- Go 代码、构建、CI 或跨模块契约变更运行 `make check`。
- 改了 `api/` 下的接口契约：运行 `make api` 并提交它写出的三个生成文件，详见 [api/AGENTS.md](api/AGENTS.md)。
- 先跑与改动相关的单个测试：Go 用 `go test ./internal/<包> -run <名称>`，Web 用 `cd web && npx vitest run src/<文件>`，`ai/` 用 `cd ai && python3 -m unittest <模块>`；Windows 开发机上的 Go 在 WSL 中运行（[测试与验证](docs/development/testing.md)）。
- 改了 Go 或 Web 代码，提交前运行 `make fmt`（[`scripts/format-changed.sh`](scripts/format-changed.sh)，只格式化改动的文件；Claude Code 与 Codex 的钩子在每次编辑后自动运行它）；`make check` 还包含 Oxlint、golangci-lint、Ruff 与 ShellCheck，修改并发代码另跑 `make race-test`（见 `docs/development/testing.md#静态检查`）。
- 改了跨进程接缝（服务之间、Go 与 Python、服务与 Hermes）：两边的测试读同一份样例，见 `docs/development/testing.md#跨进程契约`；AI 助手与剪辑规划交部署前另跑一次带真实运行环境的系统测试。
- 其他变更运行能证明结果的最小相关检查。
- 改完全部文件再跑检查：一次格式化，一次门禁。只为尽快暴露失败才先跑针对性的测试，之后的改动由门禁覆盖，不再重跑它。开发机上几个会话共用 WSL，每多跑一轮都要多等几十分钟。

## 安全

- 改特权、权限、身份或沙箱相关的代码前，读[系统不变量](docs/architecture/OVERVIEW.md#不变量)与其中链接的 ADR：产品服务以非 root 运行、Host Agent 只收高层意图、文件授权只有 ACL 等；Host Agent 一侧另见 [internal/hostops/AGENTS.md](internal/hostops/AGENTS.md)。
- 不读、不打印、不提交密钥与凭据；各工具怎样拦截见下文“编码工具”。
- 发现安全问题不写成公开 issue，按 [CONTRIBUTING.md 的“评审”](CONTRIBUTING.md#评审)交给维护者私下记录。

## 编码工具

本仓库由 Claude Code、Codex 等多种工具协同开发。规则、流程与格式化写在仓库里，各工具的配置目录只做适配。

- 本文件是唯一的项目指令文件，Codex 与 Claude Code 都直接读取。不要新建 `CLAUDE.md` 或 `CLAUDE.local.md`：工作目录或它的上级目录中有其中任何一个，Claude Code 就不再读本文件；确需时在其中只写一行 `@AGENTS.md`。
- 可复用流程写成技能，放在 `.agents/skills/`（Codex 等工具读这里），任何工具和人都可以照着做：
  - [check-in-wsl](.agents/skills/check-in-wsl/SKILL.md)：在 Windows 开发机上运行门禁；
  - [system-test](.agents/skills/system-test/SKILL.md)：systemd 系统测试；
  - [fake-preview](.agents/skills/fake-preview/SKILL.md)：在独立的 `*.localhost` 主机名上启动 Fake 预览栈。

  Claude Code 只读 `.claude/skills/`，那里是副本：改 `.agents/skills/` 后运行 `make skills`，两者不一致时 `make docs-check` 失败。
- 编辑后的格式化：`.claude/settings.json` 与 `.codex/hooks.json` 都在编辑后调用 `scripts/format-changed.sh`；改其中一个工具的钩子，另一个同步改。其他工具和人运行 `make fmt`。
- Codex 只在信任本项目后才加载本文件、局部 AGENTS.md 与项目钩子：用 Codex 开发前，先在 Codex 中信任本项目。
- 密钥的拦截：`.claude/settings.json` 禁止 Claude Code 读取密钥文件；Codex 靠它自己的沙箱与审批设置。
- 个人设置不进仓库：Claude Code 写进被 Git 忽略的 `.claude/settings.local.json`，Codex 写进 `~/.codex/config.toml`。

## 多会话协作

- 一个分支同一时间只有一个写入者，子代理与子会话也算；接手别人的分支，先等对方推送并停手。交给子代理或子会话的简报写明分支、基线提交与完成标准。
- 开工和接手时先 `git fetch`，按 `origin/main` 上的文档与代码判断现状，不凭记忆或旧检出。
- 转述子代理、子会话或其他开发者的结论前，自己看 diff、检查输出或运行结果；没核实的写明“未核实”。
- 几个进程接力完成的功能，按 [CONTRIBUTING.md 的“跨组件的功能”](CONTRIBUTING.md#跨组件的功能)：先通最简链路、接缝有负责人、先实测再设计、合完再整体跑一次、合并列车期间冻结。

## 完工

1. 留下已运行检查或其他可复现的验证证据。
2. 只更新发生变化的事实源：术语、当前状态、架构、ADR、决定记录、规格、调查或手册。
3. 在文档的“关联”部分链接相关代码、测试、ADR 或调查记录。
4. 若任务改变了当前阶段、下一步或阻塞，更新 `docs/status/CURRENT.md`。
5. 开 PR 按 [CONTRIBUTING.md 的“提交与 PR”](CONTRIBUTING.md#提交与-pr)：一个功能默认一个 PR，叠放 PR 有条件。
6. 删掉自己创建的副本、容器、镜像、临时工作树与目录，脚本用 `trap` 在退出时清理；失败时只留诊断需要的，并说明位置。开发机上大拷贝或大构建前先看 C 盘余量：WSL 的虚拟盘在 C 盘，删了东西也不会自动变小。不删其他会话或其他项目的数据。

## 记录原则

- 一项事实只有一个权威位置；其他位置使用链接。
- Git 提交记录实现历史，文档记录代码无法表达的原因、边界、复现方法和操作知识。
- `CURRENT.md` 只保留当前事实和下一步，不写逐会话流水账。“下一步”只写还没做的事，做完就删；部署后改写“已部署”一行之前，先把本页存为状态快照（`docs/README.md` 写入规则 8）。
- ADR 只记录难以逆转且存在真实权衡的决定；用户作出的可以改回来的决定写决定记录，同一个决定只写一处。
- 等用户定的事不在 `CURRENT.md` 逐条列出：`pending` 的决定记录在决定索引中标 `（pending）`，`proposed` 的 ADR 在 ADR 索引中标 `（proposed）`，`CURRENT.md` 只链接这两个索引。等实机验收的事写在验收手册，不写进“下一步”。
- 涉及真实磁盘的写操作必须由运行手册给出目标识别、确认、回滚和验证步骤。
