# A-NAS agent guide

给在本仓库工作的所有编码工具与人：每次都要遵守的规则，以及细节在哪里。细节写在被链接的文档中。

## 持续改进

- 规则为目标服务，不是教条。发现规则或文档不合理、过时、与代码或实测冲突，或照做反而更糟，直接指出并提出修改：在回复里说，或在同一个 PR 里改文档。不默默照做，也不默默违反。维护者的原话：“项目文档有不合理的内容，你可以随时挑战”。
- 文档与代码不一致时，以代码与实测为准，在同一任务中改正文档；没改就说明原因。
- 工具在本仓库犯过、可能再犯的错，写成一条规则，放进离它最近的 AGENTS.md 或对应文档；规则过时就删。评审时顺带提出可以改进的规则。
- 本文件只放每次都要遵守的规则。加一条之前，先看能否删掉一条或挪进局部文件；`make docs-check` 限制它的大小。

## 开工

1. 先检查 `git status` 和任务涉及的路径；接手或延续工作时先 `git fetch`，按 `origin/main` 判断现状。
2. 延续既有计划、判断优先级或接手未完成工作时，读 `docs/status/CURRENT.md`。
3. 涉及产品概念、领域命名或资源身份时，读 `CONTEXT.md`。
4. 改下面“局部规则”中的目录前，读其中的 AGENTS.md；其余文档按“文档路由”只读与任务相关的。

## 剪辑中心分支

`videocut-center` 是剪辑小组的集成分支，本节只在这个分支上，合回 `main` 时删除。

- 在本分支上判断现状按 `origin/videocut-center`。剪辑中心的功能分支从它拉，PR 目标选它（`gh pr create --base videocut-center`），不要选 `main`。
- 改到剪辑中心以外的公共代码时，从 `main` 另拉分支，PR 目标选 `main`；研究用的改动放 `research/` 分支，不提 PR。
- 提 PR 前运行检查命令，把公共代码填进 PR 描述。分类、命令与评审要求见[剪辑中心分支与公共代码](docs/development/videocut-center-branch.md)。

## 局部规则

在那个目录必须遵守的约定。Claude Code 读其中的文件时自动加载；Codex 只加载从仓库根到会话启动目录这一路上的文件，所以在根目录启动时要自己去读。

- [web/AGENTS.md](web/AGENTS.md)：Web 桌面。[api/AGENTS.md](api/AGENTS.md)：产品 API 契约。
- [deploy/AGENTS.md](deploy/AGENTS.md)：软件包、systemd 单元与设备镜像。
- [internal/hostops/AGENTS.md](internal/hostops/AGENTS.md)：Host Agent 的 root 一侧，适用于 `.golangci.yml` 中 `host-agent` 规则列出的全部包。
- [internal/files/AGENTS.md](internal/files/AGENTS.md)：文件服务与文件代理的客户端。[internal/appstore/AGENTS.md](internal/appstore/AGENTS.md)：应用中心与容器代理。
- [internal/photos/AGENTS.md](internal/photos/AGENTS.md)：相册。[internal/montage/AGENTS.md](internal/montage/AGENTS.md)：剪辑中心（含 `ai/anas_montage`）。
- [ai/AGENTS.md](ai/AGENTS.md)：Python 一侧的 AI Worker、评估与依赖锁。[ai/assistant/AGENTS.md](ai/assistant/AGENTS.md)：AI 助手 VNAS。

## 文档路由

- 组件边界、数据流、系统不变量：`docs/architecture/OVERVIEW.md` 与相关 ADR。
- 决定：难以逆转的在 `docs/adr/`，用户作出的其他决定与等用户定的事在 `docs/decisions/`，分界见其 README。
- 新功能或行为变化：读或创建 `docs/specs/` 中的规格。
- 难复现、跨边界或可能复发的缺陷：先查 `docs/investigations/`，再更新或创建调查记录。
- 安装、部署、恢复、磁盘等高风险操作：`docs/runbooks/` 中的手册。
- 新用或更换开发环境、排查平台问题、使用共享开发服务器时，查[开发环境入口](docs/development/LOCAL_ENVIRONMENT.md)，补记实测配置与解决方法。产品定位：`README.md`；早期背景：`PROJECT_CONTEXT.md`。
- 文档类型或事实归属不明确时：`docs/README.md`。

## 不重复造轮子

- 写基础设施代码前，先用成熟库、标准库或代码生成；仍要手写时在 PR 中写明原因，评审把手写的轮子记为违反本条。范围包括解析器与编解码、接口类型与客户端、数据获取、界面基础组件、格式化、打包与单元校验；仍手写的二进制解析器要有 fuzz 测试。
- 集成的开源项目出了问题，先看上游的服务端、官方客户端与测试怎么处理，照着做并写明依据。
- 换上一个库之前，测出新旧做法在最坏情况下的开销：库也可能更贵。

## 与维护者协作

- 讨论方案时，部分同意不等于开工：等指挥你的人明确说“开始做”。只读的调查可以先做。
- 要超过一小时或要新加基础设施的工作，先用一两句话说明规模和更轻的做法，让对方选。
- 达标线保护目标，不缩小目标：没达标时补数据、换方法或先问，不擅自退回兜底，也不把自己的取舍说成对方的决定。
- 方案按长期结果选，直说代价；迁移成本或避免与开放的 PR 冲突不是选弱方案的理由。
- 下载大文件前说明来源与大小。长时间的评估放到后台，多个模型逐个评完，不交错。

## 验证

- 检查不访问实验 NAS，只写可丢弃的构建产物，可直接运行。纯文档跑 `make docs-check`；Go、构建、CI 或跨模块契约跑 `make check`（要联网，新的漏洞公告会让它在代码没变时失败：先升级受影响的依赖）；其他变更跑能证明结果的最小检查。细节见[测试与验证](docs/development/testing.md)。
- 先跑相关的单个测试：`go test ./internal/<包> -run <名称>`、`cd web && npx vitest run src/<文件>`、`cd ai && python3 -m unittest <模块>`；Windows 上 Go 在 WSL 中运行。改完全部文件再一次格式化、一次门禁：几个会话共用 WSL，每多一轮都要多等几十分钟。
- 提交前运行 `make fmt`（编辑后的钩子也会运行）；改并发代码另跑 `make race-test`。
- 跨进程接缝（服务之间、Go 与 Python、服务与 Hermes）两边的测试读同一份样例（[跨进程契约](docs/development/testing.md#跨进程契约)）；AI 助手与剪辑规划交部署前，跑一次带真实运行环境的系统测试。

## 安全

- 改特权、权限、身份或沙箱相关的代码前，读[系统不变量](docs/architecture/OVERVIEW.md#不变量)与其中链接的 ADR。
- 不读、不打印、不提交密钥与凭据。安全问题不写公开 issue，按 [CONTRIBUTING.md 的“评审”](CONTRIBUTING.md#评审)交给维护者私下记录。

## 编码工具

规则、流程与格式化写在仓库里，`.claude/`、`.codex/` 只做适配；改其中一个工具的配置，另一个同步改。

- 本文件是唯一的项目指令文件。不要新建 `CLAUDE.md` 或 `CLAUDE.local.md`：工作目录或其上级目录中有其中任何一个，Claude Code 就不再读本文件；确需时其中只写一行 `@AGENTS.md`。
- 技能在 `.agents/skills/`：[check-in-wsl](.agents/skills/check-in-wsl/SKILL.md)（Windows 上的门禁）、[system-test](.agents/skills/system-test/SKILL.md)、[fake-preview](.agents/skills/fake-preview/SKILL.md)。Claude Code 读 `.claude/skills/` 中的副本：改后运行 `make skills`。
- 编辑后的格式化：`.claude/settings.json` 与 `.codex/hooks.json` 都调用 `scripts/format-changed.sh`。
- Codex 只在信任本项目后才加载本文件、局部 AGENTS.md 与项目钩子。密钥：Claude Code 由 `.claude/settings.json` 拦截，Codex 靠自己的沙箱与审批。个人设置写进被 Git 忽略的 `.claude/settings.local.json` 或 `~/.codex/config.toml`。

## 多会话协作

- 一个分支同一时间只有一个写入者，子代理与子会话也算；接手别人的分支，先等对方推送并停手。给子代理或子会话的简报写明分支、基线提交与完成标准。
- 转述子代理、子会话或其他开发者的结论前，自己看 diff、输出或运行结果；没核实的写明“未核实”。
- 几个进程接力完成的功能按 [CONTRIBUTING.md 的“跨组件的功能”](CONTRIBUTING.md#跨组件的功能)。

## 完工

1. 留下已运行检查或其他可复现的验证证据。
2. 只更新发生变化的事实源，并在文档的“关联”部分链接相关代码、测试、ADR 或调查。
3. 改变了当前阶段、下一步或阻塞时，更新 `docs/status/CURRENT.md`。
4. 开 PR 按 [CONTRIBUTING.md 的“提交与 PR”](CONTRIBUTING.md#提交与-pr)。
5. 删掉自己创建的副本、容器、镜像、临时工作树与目录（脚本用 `trap`）；大拷贝前先看 C 盘余量，WSL 的虚拟盘不会自动变小；不删其他会话或项目的数据。

## 记录原则

- 一项事实只有一个权威位置，其他地方链接。Git 记实现历史，文档记代码表达不了的原因、边界、复现方法与操作知识。
- 难以逆转、有真实权衡的决定写 ADR，其他用户决定写决定记录，同一个决定只写一处。
- `CURRENT.md` 只放当前事实与下一步，写法见 [docs/README.md 的写入规则](docs/README.md#写入规则)。
- 涉及真实磁盘的写操作，必须由运行手册给出目标识别、确认、回滚与验证步骤。
