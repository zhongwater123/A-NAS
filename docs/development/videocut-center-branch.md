# 剪辑中心分支与公共代码

更新时间：2026-11-26

剪辑小组在长期分支 `videocut-center` 上开发剪辑中心，一个阶段做完后整体合回 `main`。本文写这期间的分支规则、怎么区分剪辑中心代码和公共代码，以及评审时的公共代码检查。根目录 [AGENTS.md](../../AGENTS.md) 在本分支上有一节指向这里；阶段合回 `main` 时，删除那一节和本文，或改写成合回后仍然适用的规则。

分支名按 [#639](https://github.com/zhongwater123/A-NAS/issues/639) 的新名字 VideoCut 起；按那份规格重构之前，代码目录仍叫 `montage`。下面的类别表和检查命令同时认 `montage` 与 `videocut` 两套名字，重构前后都能用；重构完成、`git grep -i montage` 只剩规格允许的地方后，再删去 `montage` 那一套。

剪辑中心以外的代码（下称公共代码）别人也在改：2026-11 以来，相册 `internal/photos` 有 49 次提交、系统测试脚本 `scripts/system-test.sh` 有 69 次，大部分不是剪辑中心的改动。公共代码的改动在分支上放久了，最后合并时会集中冲突，所以剪辑中心代码和公共代码分开提。

## 分支

- **`videocut-center`**：从 `main` 拉出，是剪辑小组的集成分支。剪辑中心的功能分支从它开，PR 的目标分支选它（`gh pr create --base videocut-center`；网页上新建 PR 时默认目标是 `main`，要手动改）。评审照常进行，CI 对任何目标分支的 PR 都会运行；直接推送到本分支不触发 CI，所以改动都走 PR。
- **谁审核合并**：目标是 `videocut-center` 的 PR 由 `liangjianshu` 审核，CI 通过后用合并提交合入，不 squash、不 rebase；`liangjianshu` 自己的 PR 找一位组员审核后再合。阶段合回 `main` 的 PR 和公共代码的主干 PR 照常由仓库维护者审核。本分支没有 GitHub 的分支保护，有写权限的人都能直接推送或合并，以上靠约定执行。
- **每周同步一次 `main`**：在 `videocut-center` 上合并 `main`，用 merge，不用 rebase，免得改写组员已经推送的提交。同步后运行 `make check`。组员轮流负责，同步前在群里说一声。
- **阶段完成后合回 `main`**：开一个 `videocut-center` → `main` 的 PR，按 [CONTRIBUTING.md](../../CONTRIBUTING.md) 的流程评审。按本文的规则做，这个 PR 里的公共代码应当只剩“登记”类改动。
- **研究用的改动不进 `videocut-center`**：比如把描述模型换成公司的模型 API、放开 AI Worker 的联网限制。这类改动放在从 `videocut-center` 拉出的 `research/<主题>` 分支上，只部署到实验 NAS，不提 PR。

## 怎么判断是哪类代码

按目录判断，不按文件名。比如 `internal/photos/montagecards.go` 文件名带 montage，但它在相册里，属于公共代码。

| 类别 | 范围 | 去向 |
|---|---|---|
| 剪辑中心代码 | 下面的 `<名>` 是 `montage` 或 `videocut`：`internal/<名>*`（现在是 `internal/videocut` 与 `internal/videocutcompute`）；`ai/anas_<名>*`；`web/src/<名>`；`deploy/<名>-*`；`api/paths/<名>.yaml`、`api/components/<名>.yaml`；`ai/tests/test_<名>_*.py`、`ai/eval/<名>_*.py`；`scripts/` 和 `docs/` 下文件名带 montage 或 videocut 的 | 合进 `videocut-center` |
| 生成文件 | `api/openapi.bundle.yaml`、`web/src/api.generated.ts`、`web/src/api.validators.generated.ts`、`web/package-lock.json` | 不手工解决冲突：接口文件重新运行 `make api`，锁文件重新运行 `npm install` |
| 公共代码：登记 | 其余文件里，只新增、不改已有行，而且只是把剪辑中心挂上去：比如 `api/openapi.yaml` 加路径引用、加一条路由、`web/package.json` 加依赖、系统测试加剪辑中心的检查 | 可以留在 `videocut-center`，阶段完成时一起合回 |
| 公共代码：改动 | 其余文件里，改了已有的逻辑、数据表、接口或共用组件，或者新加了公共模块的逻辑：比如在相册里加一种分析任务、改 AI Worker、改相册的调度、改共用的播放器 | 尽快合进 `main`，见下一节 |

拿已有的 PR 举例：M7 卡片描述（[#571](https://github.com/zhongwater123/A-NAS/pull/571)）改的全是公共代码，都属于“改动”。二次编辑 R6（[#616](https://github.com/zhongwater123/A-NAS/pull/616)）的公共代码里，`api/openapi.yaml`、`web/package.json` 是登记；`web/src/photos/VideoPlayer.tsx`、`web/src/ui/feedback.tsx` 改了共用组件，属于改动。

## 公共代码的改动怎么提

1. **能拆就先拆。** 公共代码单独开分支，从 `main` 拉，PR 目标选 `main`，按普通流程评审。审核人优先找这个模块最近的维护者（用 `git log` 看谁改得多）。合进 `main` 后同步到 `videocut-center`，再提剪辑中心那部分。
2. **主干 PR 还在等审核、剪辑中心又急着用时**，可以把它的提交先合进 `videocut-center`，主干 PR 保持开着，不用另开 `待合主干` Issue。合进本分支的 PR 要用指向同一提交的新分支来提（`git push origin <主干 PR 的分支>:refs/heads/chore/<主题>-into-videocut`），不能直接用主干 PR 的分支：仓库在合并后自动删除 PR 的分支，用同一个分支，合并时会删掉它，主干 PR 也随之关闭。之后主干 PR 合进 `main`，本分支同步 `main` 时这些提交已经在了，不会重复。例：[#645](https://github.com/zhongwater123/A-NAS/pull/645) 经 [#659](https://github.com/zhongwater123/A-NAS/pull/659) 先合进本分支。
3. **来不及拆，可以先合进 `videocut-center`**，但同时开一个 Issue，打上 `待合主干` 标签，写明文件和原因。下一次同步 `main` 之前（最长一周）提出主干 PR。
4. **主干 PR 要能单独成立。** 有自己的测试；剪辑中心还没合回时，这段代码在 `main` 上没人调用也不会出错。
5. **文档跟着代码走。** 公共代码对应的文档（比如 `docs/architecture/photo-ai.md`）随主干 PR 一起改；剪辑中心的规格和技术设计留在 `videocut-center`。

## 检查命令

提 PR 前，作者在功能分支上运行下面的命令，列出公共代码。目标分支是 `main` 时，把 `base` 改成 `origin/main`。

```bash
base=origin/videocut-center
montage='^(internal/(montage|videocut)[a-z]*/|ai/anas_(montage|videocut)[a-z_]*/|web/src/(montage|videocut)/|deploy/(montage|videocut)-[^/]+/|api/(paths|components)/(montage|videocut)\.yaml$|ai/tests/test_(montage|videocut)_[^/]*\.py$|ai/eval/(montage|videocut)_[^/]*\.py$|scripts/[^/]*(montage|videocut)[^/]*$|docs/.*(montage|videocut)[^/]*$)'
generated='^(api/openapi\.bundle\.yaml|web/src/api(\.validators)?\.generated\.ts|web/package-lock\.json)$'
git fetch -q origin
git diff --numstat "$base"...HEAD | M="$montage" G="$generated" awk -F'\t' '$3 !~ ENVIRON["M"] && $3 !~ ENVIRON["G"] { print ($2 == 0 ? "只新增" : "改了已有行") "\t+" $1 "/-" $2 "\t" $3 }'
```

- 没有输出：只改了剪辑中心代码和生成文件。
- **改了已有行**：一定是公共代码的改动。
- **只新增**：看内容。只是把剪辑中心挂上去的算登记；新加的公共逻辑（比如相册里新加的文件）仍算改动。

把输出贴进 PR 描述的“公共代码”一栏，逐个注明“登记”或“改动”，改动后面写主干 PR 号或 `待合主干` Issue 号。没有公共代码就写“无”。

## 评审时的公共代码检查

适用于目标分支是 `videocut-center` 的 PR，以及阶段完成时 `videocut-center` → `main` 的那个 PR。审核人核对五件事：

1. 自己运行一遍检查命令，“公共代码”一栏和实际改动一致。
2. 标了“改了已有行”的文件，没有被标成“登记”。
3. 每个“改动”都有主干 PR 或 `待合主干` Issue；Issue 开了超过一周、还没有主干 PR 的，在评审里提出来。
4. 研究用的改动（放开 AI Worker 联网、把模型换成公司 API 之类）没有出现在合进 `videocut-center` 的 PR 里。
5. 反过来，目标是 `main` 的公共代码 PR 里，没有夹带剪辑中心代码。

在 PR 上写一条总评论，不用行内评论，列出公共代码的文件和类别，写明要作者做什么，例如：

> 公共代码：`web/src/photos/VideoPlayer.tsx`（改了已有行，改动）、`web/src/ui/feedback.tsx`（改了已有行，改动）。请提主干 PR，或开 `待合主干` Issue 并在本 PR 里链接。

这项检查不是缺陷，不进评审的分级：

| 情况 | 能不能合进 `videocut-center` |
|---|---|
| 公共代码都已分类，改动有主干 PR 或 Issue | 能 |
| 有改动没有主干 PR，也没有 Issue | 补上再合 |
| 夹带研究用的改动 | 拿掉再合 |

这项检查靠上面的命令，不靠 `/code-review`：评审会话只按代码找缺陷，不一定会对照本分支的规则。

## 关联

- [AGENTS.md](../../AGENTS.md)：本分支上“剪辑中心分支”一节。
- [CONTRIBUTING.md](../../CONTRIBUTING.md)：提交、PR 与评审的通用流程。
- [剪辑中心技术设计](../architecture/montage-center.md)：剪辑中心与相册、AI Worker 的分工，说明为什么预处理会改到公共代码。
