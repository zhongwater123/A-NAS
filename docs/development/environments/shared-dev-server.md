# 共享开发服务器

更新时间：2026-11-26

完整门禁（`make check-steps`）、`make race-test`、系统测试和长时间的评估，默认放到共享开发服务器上运行。只跑一个 Go 包或几个 vitest 文件、Fake 预览这类小范围的事，留在本机。服务器比开发机大得多，几个会话的门禁也能同时跑；本机 WSL 只在服务器连不上时作为备用（[check-in-wsl](../../../.agents/skills/check-in-wsl/SKILL.md)）。

运行步骤在技能 [check-on-shared-server](../../../.agents/skills/check-on-shared-server/SKILL.md) 中。本页写这台机器的情况，以及编码工具（Claude Code、Codex）经 SSH 非交互使用它时踩过的坑。账号、目录、存储分层与清理命令以服务器上的使用手册 `/opt/anas/ONBOARDING.md` 为准，本页不重复。仓库是公开的，所以这里与技能里都不写主机名、地址与账号，只用自己的 SSH 别名。

## 申请账号

向项目维护者提供个人 SSH 公钥，领取账号、主机地址、主机指纹和所需目录的权限；从外网接入时一并索取跳板配置。在自己的 `~/.ssh/config` 里给服务器起一个别名并指定私钥：直接输入完整主机名时，ssh 只试默认私钥，会得到 `Permission denied (publickey)`。连上后先确认能读取要用的模型、工作目录可写，再核对磁盘、内存与工具版本。

## 机器

2026-11-26 核对：

| 项目 | 状态 |
|---|---|
| 系统 | Windows 办公机上的 WSL2 Ubuntu 24.04，带 systemd；根分区 ext4，约 1 TB |
| 资源 | 24 个 CPU、27 GiB 内存，多个开发者共用 |
| 工具链 | 由 `/etc/profile.d/anas-toolchain.sh` 加入 `PATH`：Go 1.27.4、Node 26.9.0，Python 3.13 工具链 `/opt/anas/ai-tools`（带 NumPy 与 Pillow，门禁里的剪辑 Worker 测试会运行），以及 lychee、ruff、pip-audit 等门禁工具 |
| Docker | 所有开发账号都在 `docker` 组，可以跑系统测试；`docker` 组等同 root，只用它跑自己的容器 |
| 门禁包装 | `anas-gate <make 目标…>`：全机共用 3 个槽位；`system-test`、`root-integration-test`、`image-test`、`release-key-test`、`image`、`release` 独占全部槽位。当前占用写在 `/opt/anas/lock/anas-gate.holder` |
| 辅助命令 | `anas-clean`（清理 `~/src/*/build` 与门禁副本目录 `~/anas-check`）、`anas-batch`（在可被抢占的低优先级 slice 中运行重任务）、`anas-images`（系统测试共享镜像的状态）、`anas-usage`（谁在用多少资源） |

## 把要测的提交送过去

- **推送前的门禁**：用 `git archive <提交>` 经 SSH 解到服务器上的 `~/anas-check/<目录>`。整个仓库约 27 MB，压缩后约 8 MB，几秒传完。只含已提交的内容，所以先提交再测。
- **副本没有 `.git`**：运行 `make -k check-steps`，不运行 `make check`（后者要用 Git 写部署校验清单，见[测试与验证](../testing.md)）。要读提交历史的工具（如变异测试的 `gremlins --diff`、`git diff`）在这种副本里用不了：在服务器上的完整克隆里 `git fetch` 已推送的分支，或在副本里自己建一个只有基线与改动两次提交的仓库。
- **副本没有 `build/`**：向量索引的测试要先运行 `scripts/fetch-qdrant.sh`，每个副本下载一次约 34 MB；不想重复下载时，用 `ANAS_QDRANT_BINARY` 指向已经取到的二进制（[Qdrant 测试依赖](../testing.md#qdrant-测试依赖)）。
- 一个提交一个目录，代码解到其中的 `src/`，运行脚本、日志与标记文件放在 `src/` 外面：`ops-check` 对副本里所有 `.sh` 跑 shellcheck，放在里面的运行脚本会让门禁失败。跑完删掉目录，日志拷回本机。
- **复测模型或评估**：在自己的工作目录检出 PR 的确切提交，记录输入清单、运行命令和结果。共享模型可暂存在 `/srv/cold/models/`，由提交者提供本次 PR 的子目录和清单，评审者就地读取；需要由另一个会话构建或复测时，按[模型交付](../ai-development.md#模型交付)给出可重新获取的来源。

## 长时间的任务

门禁、系统测试和评估都比一次 SSH 调用长，编码工具的单条命令也有超时。做法：

- **脱离会话运行**：在服务器上用 `setsid nohup bash <脚本> > /dev/null 2>&1 < /dev/null &` 启动，当场把 `$!` 写进进程号文件，脚本最后把退出码写进一个文件。
- **等待看退出码文件与进程号**：`while [ ! -f status ] && kill -0 "$(cat pid)"; do sleep 20; done`。进程没了也没有退出码，就是被中断了。
- **中途停掉要杀整个进程组**：`kill -- -<进程号>`。`setsid` 启动的脚本自成一个进程组；只杀脚本本身，它启动的 make 与测试进程会成为孤儿留下来。
- **不要用 `pgrep -f <名字>` 等待**：远端执行这条判断的 `bash -c` 自己的命令行里也带着这个名字，pgrep 总能匹配到它，循环永远不会结束（2026-11-25，两个这样的等待循环挂了近一小时）。
- **SSH 断线不影响远端任务**：`client_loop: send disconnect`、`Connection reset by peer`（退出码 255）之后，脱离会话的任务和远端的等待循环都还在跑。重新连上，看进程号与退出码文件，不要重新启动一遍；启动命令本身报 255 时也先看，2026-11-25 一次启动报了 255，任务其实已经开始。
- **自己加超时**：`anas-gate` 不限时，整个门禁给 `timeout 90m`，`pip-audit` 给 `PIP_AUDIT="timeout 600 pip-audit"`。2026-11-26 不带后者的一次门禁卡在 pip-audit 对服务器 DNS 的 TCP 读取上约一小时，手动重跑只要 3 到 5 秒。
- **长时间的评估与变异测试**：用 `anas-batch` 或 `nice` 运行，并限制并行数，给别人的门禁留出 CPU。2026-11-25 一轮 8 路并行的变异测试让负载到 22 左右。

## Python 环境

- 门禁用工具链的 `python3`（`/opt/anas/ai-tools`）。给门禁设 `ANAS_MONTAGE_PYTHON=python3`，和 CI 一样：工具链缺了 NumPy 或 Pillow 时，剪辑 Worker 的 Python 与 Go 测试会失败，而不是悄悄跳过。缺依赖跳过的测试不能算完整门禁（[测试与验证](../testing.md)）。
- 要临时加工具（如 `coverage`）时，不要从工具链的解释器建 `--system-site-packages` 的虚拟环境：工具链本身就是一个虚拟环境，新环境看不到它装的包，NumPy 与 Pillow 的测试会被跳过（2026-11-25，34 个剪辑 Worker 测试跳过了 19 个）。在新环境里按 `ai/requirements.txt` 的版本装上 NumPy 与 Pillow，并同样设 `ANAS_MONTAGE_PYTHON`。

## 用完清理

- 删掉自己的副本、虚拟环境和临时目录。被超时或手动打断的测试会把临时目录留在 `TMPDIR` 下（2026-11-25 一轮变异测试留下约 22 MB）。
- 看有没有自己留下的孤儿进程：`ps -u "$USER" -o pid,ppid,etime,args`。被打断的 Go 测试会留下 PPID 为 1 的 `qdrant`。`anas-gate` 启动的子进程继承了槽位锁的文件描述符，这样的孤儿会一直占着一个门禁槽位，所以杀掉它。别人的进程不要动，告诉管理员。
- `anas-clean`（先空跑看清单，再 `--apply`）清理 `~/src/*/build` 与 `~/anas-check`。

## 已知问题

| 现象 | 原因 | 做法 |
|---|---|---|
| 等远端任务的循环一直不结束 | `pgrep -f` 匹配到了执行判断的 shell 自己 | 用进程号文件与退出码文件等待 |
| `ops-check` 的 shellcheck 报 `run.sh` | 运行脚本放在了副本里，被当成仓库的脚本检查 | 代码解到副本的 `src/`，运行脚本放在外面 |
| 经 SSH 传过去的脚本是空的 | 远端命令写成 `cat > f && setsid nohup bash f &`，`&` 把 `cat` 也放到后台，它的标准输入成了 `/dev/null` | 写脚本与启动分成两次 SSH 调用，或写成 `cat > f; setsid …` |
| 门禁在 pip-audit 停住不动 | pip-audit 卡在 DNS 的 TCP 读取上 | `PIP_AUDIT="timeout 600 pip-audit"` |
| 一个门禁槽位一直被占着，持有者早已结束 | 孤儿子进程继承了槽位锁的文件描述符 | 查 `ps` 中 PPID 为 1 的自己的进程并杀掉；别人的告诉管理员 |
| 自己的门禁排队很久，槽位却没满 | 一个独占任务在等全部槽位空出来，等待期间拿着分配锁，其他门禁都排在它后面 | 跑系统测试前先看 `/opt/anas/lock/anas-gate.holder`；排队时看持有者 |
| 剪辑 Worker 的测试显示跳过 | 解释器看不到 NumPy 或 Pillow | 设 `ANAS_MONTAGE_PYTHON=python3`；临时虚拟环境自己装 NumPy 与 Pillow |
| 槽位锁报 `Permission denied` | 锁目录权限不对（2026-11-25 出现过几小时） | `ls -la /opt/anas/lock`，应为 `anas-devs` 组可写，否则告诉管理员 |

## 关联

- 运行步骤：[check-on-shared-server](../../../.agents/skills/check-on-shared-server/SKILL.md)；备用：[check-in-wsl](../../../.agents/skills/check-in-wsl/SKILL.md)
- [开发环境入口](../LOCAL_ENVIRONMENT.md)、[测试与验证](../testing.md)
- 服务器上的使用手册：`/opt/anas/ONBOARDING.md`
