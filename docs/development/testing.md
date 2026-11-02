# 测试与验证

更新时间：2026-10-27

本文维护检查目标、测试依赖与覆盖范围。机器配置从[开发环境入口](LOCAL_ENVIRONMENT.md)选择；变更需要执行哪些检查，以 [AGENTS.md](../../AGENTS.md#验证) 为准。

## 运行入口

| 任务 | 执行步骤 |
|---|---|
| Windows/WSL 完整门禁 | [check-in-wsl](../../.claude/skills/check-in-wsl/SKILL.md) |
| macOS/Colima 检查与当前限制 | [macOS 环境](environments/macos.md#运行检查) |
| Fake 模式界面预览 | Windows 用 [fake-preview](../../.claude/skills/fake-preview/SKILL.md)，macOS 用[Colima 预览](environments/macos.md#fake-预览) |
| systemd 系统测试 | [system-test](../../.claude/skills/system-test/SKILL.md) |

`make check-steps` 执行 `make check` 的检查目标；后者还依赖 Git 写入部署校验清单。记录实际目标、失败项和日志位置；纯文档检查或缺依赖跳过的测试不能作为完整门禁证据。

## Qdrant 测试依赖

向量索引的测试（`internal/vectorindex` 与相册的同步）运行 [`deploy/qdrant/qdrant.json`](../../deploy/qdrant/qdrant.json) 固定的 Qdrant 二进制。在 Linux x86_64 开发环境中先运行一次 `scripts/fetch-qdrant.sh`：它下载官方压缩包，核对大小与 SHA-256，把二进制留在 `build/qdrant/<sha256>/`。测试默认取这个位置，也可以用 `ANAS_QDRANT_BINARY` 指定。缺少二进制时这些测试跳过；设 `ANAS_QDRANT_REQUIRED=1` 时改为失败，CI 就是这样设的。ext4 副本不带 `build/`，复制后再把 `build/qdrant` 一并复制过去，或在副本中重新获取。

清单目前固定 Linux x86_64 包。其他架构使用 `ANAS_QDRANT_BINARY` 前需自行验证同版本二进制的可运行性；项目尚无已验证的 ARM64 完整门禁流程。完整检查须设置 `ANAS_QDRANT_REQUIRED=1`，缺失依赖就失败；该标志不会让不兼容的二进制变得可运行。读取及跳过逻辑见 [`vectorindextest`](../../internal/vectorindex/vectorindextest/vectorindextest.go)。

## Web 桌面

完整检查在依赖锁变化时执行 `npm ci`，随后运行类型检查、[静态检查](#静态检查)、React 测试和 Vite 构建：

```bash
make check
```

日常迭代先运行受影响的现有目标，例如 `go test ./internal/storage`、`make web-test` 或 `make ops-check`；RC 提交只运行一次完整 `make check VERSION=<RC>`。完整检查会把提交、产品版本和两个二进制哈希写入可丢弃的 `build/.validated-build`，`deploy-dev.ps1` 只有在四者全部一致时才复用结果，否则自动重跑完整门禁。这样不会用“跳过测试”换速度，也不会为同一制品重复执行全量测试。

完整检查中的 `make vuln-check` 用 `go.mod` 的 `tool` 指令固定的 govulncheck 扫描代码可达的已知漏洞，需要能访问 `vuln.go.dev`。它报告可达漏洞时检查失败：升级对应模块，或一起修改 `.go-version` 与 `go.mod` 的 `toolchain` 行来升级 Go。CI 每周一还在 `main` 上单独运行一次（`.github/workflows/vuln.yml`，也可手动触发），这样没有人推送时，新公布的漏洞也会报出；修改这个工作流的 PR 也会运行它，其他 PR 不运行，所以它不能设为必需检查。

只开发界面时，先在 `127.0.0.1:8080` 上启动 `ANAS_HOSTSTATE_MODE=fake` 的产品服务（见 [README](../../README.md)），再：

```bash
cd web
npm ci
npm run dev
```

生成资源位于被 Git 忽略的 `internal/webui/dist/`，随后由 Go `embed` 写入产品二进制；NAS 不需要 Node.js。

## 静态检查

`make check` 包含以下目标，也可单独运行：

| 目标 | 检查 |
|---|---|
| `make api-check` | 检查接口契约（`redocly lint`），再由它重新生成打包文档、Web 类型与响应校验，与已提交的不一致即失败，见[接口契约](api-contract.md#接口契约) |
| `make web-lint` | `web/` 中的 `npm run lint`：Oxlint（含 jsx-a11y）与 eslint-plugin-react-hooks（含 React Compiler 的规则），再由 Biome 检查格式与导入顺序；规则列在 `web/.oxlintrc.json`，所有文件都须通过 |
| `make go-lint` | golangci-lint，版本由 `tools/golangci-lint/go.mod` 固定，首次运行经 `go tool` 从 `proxy.golang.org` 构建；规则与理由见 `.golangci.yml` |
| `make ai-lint` | Ruff 检查 `ai/`，规则与理由见 `ai/ruff.toml`；`ai/requirements-dev.txt` 按哈希固定版本，`PATH` 上的 `ruff` 版本不同即失败。Ruff 在 WSL 中不检查可执行位（EXE001/EXE002），这两条只在 CI 报出：Python 文件在 Git 中是 0644，不写 shebang |
| `make docs-check` | [`tools/doccheck`](../../tools/doccheck/main.go) 按[文档的写入规则](../README.md#写入规则)核对状态与索引；再由 lychee 离线检查每个受 Git 跟踪的 `*.md` 中的链接：目标文件存在，指向 Markdown 的锚点对应真实的标题。lychee 的版本由 Makefile 的 `LYCHEE_VERSION` 固定（不同版本生成标题锚点的规则不同），CI 按哈希安装同一版本 |
| `make ops-check` | 对每个受 Git 跟踪的 `*.sh` 运行 ShellCheck（`.shellcheckrc`）与 `bash -n`；用 actionlint 校验 `.github/workflows`，其中 `run:` 脚本也经 ShellCheck，版本由 `tools/actionlint/go.mod` 固定，首次运行经 `go tool` 构建；由 `tools/depcheck` 确认 Dependabot 不把 0.x 的 Web 包放进分组，再核对单元与安装器的约定；ShellCheck 的版本由 Makefile 的 `SHELLCHECK_VERSION` 固定（不同版本判断不同），CI 按哈希安装同一版本 |

lychee 从发布页下载，核对 SHA-256（与 CI 中的一致）后放进 `PATH`：

```bash
curl -fsSL -o /tmp/lychee.tar.gz https://github.com/lycheeverse/lychee/releases/download/lychee-v0.24.2/lychee-x86_64-unknown-linux-gnu.tar.gz
echo "1f4e0ef7f6554a6ed33dd7ac144fb2e1bbed98598e7af973042fc5cd43951c9a  /tmp/lychee.tar.gz" | sha256sum -c -
tar -xzf /tmp/lychee.tar.gz -C /tmp
install /tmp/lychee-x86_64-unknown-linux-gnu/lychee ~/.local/opt/bin/lychee
```

开发机 C（macOS，Apple 芯片）改用同一发布中的 `lychee-aarch64-apple-darwin.tar.gz`，SHA-256 为 `c9d3740ea2d891854d37116c9fba840f37b6e7c89d330e7db84ac333631c4977`（用 `shasum -a 256` 核对），解出的目录是 `lychee-aarch64-apple-darwin/`。Homebrew 的 lychee 跟随最新版，版本不同时 `docs-check` 会拒绝。

Ruff、pip-audit 与 uv 不随 npm 或 Go 安装，[`ai/requirements-dev.txt`](../../ai/requirements-dev.txt) 按哈希固定它们。有 `ensurepip` 的机器装进独立的 venv，再把其 `bin` 加入 `PATH`：

```bash
python3 -m venv ~/.local/opt/ai-tools
~/.local/opt/ai-tools/bin/pip install --require-hashes --only-binary=:all: -r ai/requirements-dev.txt
```

开发机 B 的 Python 3.14 没有 `ensurepip`：先从 uv 的 GitHub 发布页下载锁文件固定版本的 `uv-x86_64-unknown-linux-gnu.tar.gz`，按同页的 `.sha256` 核对后装进 `~/.local/opt/bin`，再用 `uv venv ~/.local/opt/ai-tools` 建环境，`uv pip install --python ~/.local/opt/ai-tools/bin/python --require-hashes -r ai/requirements-dev.txt` 安装。

`make race-test` 以竞态检测器、每次 4 个包运行 `internal/webui` 以外的全部 Go 测试，需要 gcc，不需要 Node.js；CI 另开作业运行，修改并发代码时本地也运行。测试注入的时钟与 ID 生成器也会被后台 goroutine 调用，要像 `time.Now` 一样可以并发调用：时钟用 `internal/clocktest`，不要在闭包里改写捕获的变量，否则这个作业会偶发失败。

Web 代码由 Biome 格式化并排列导入（`web/biome.json`：先 Node 与 npm 包，空一行，再相对路径）：提交前运行 `cd web && npm run format`。2026-10-22 对全部 Web 代码的一次格式化提交列在 `.git-blame-ignore-revs`，GitHub 的 blame 跳过它，本地运行 `git config blame.ignoreRevsFile .git-blame-ignore-revs`。在它之前分出的分支先合并它的父提交，在分支上运行 `npm run format` 并提交，再合并 `main`，这样只剩真正的冲突。

各作业的 Go 版本取自 `.go-version`，Node.js 版本取自 `.nvmrc`；`package.json` 的 `engines` 只是允许的范围。竞态作业不装 Node.js：`make race-test` 不跑只服务前端构建产物的 `internal/webui`，它由 `make check` 覆盖。系统测试的 Debian 13 镜像按构建脚本的哈希与所在的周缓存，构建脚本改了或过了一周才重新组装。

另有两个每周定时、不在规则集中的工作流：`vuln.yml` 运行 `make vuln-check`，报告此后新发布的公告；`fuzz.yml` 从代码中找出全部 `func Fuzz…`，每个目标跑 5 分钟，失败时上传触发失败的输入，把它放进该包的 `testdata/fuzz/<目标>/` 就成为回归用例。

## Root 集成测试

账号、ACL 与 Samba 行为需要真实 root 工具验证。`internal/hostops/linux/root_integration_test.go` 使用 `rootintegration` 构建标签，只在 root 且 `ANAS_ROOT_INTEGRATION=1` 时运行，并会创建账号、组和 Samba 配置。只在可丢弃的特权 Debian 容器中运行，绝不在开发机或实验 NAS 上直接执行：

```bash
docker run --rm --privileged -e ANAS_ROOT_INTEGRATION=1 -e CGO_ENABLED=1 \
  -v "$PWD:/src" -w /src <装有 Go、gcc、acl、btrfs-progs、samba、smbclient 的 Debian 镜像> \
  make root-integration-test
```

权限矩阵会挂载 loop 设备上的 Btrfs，因此需要 `--privileged`。bookworm 镜像还需 `samba-vfs-modules`（trixie 的 `samba` 已自带 `recycle.so`）。

相册服务的端到端冒烟在同一类容器中进行：

- 运行真实 Host Agent（root）、以 `a-nas-photos` 运行的相册服务和以 `a-nas` 运行的产品服务。
- 经产品服务完成启用管理员、上传、缩略图和原图读取。
- 检查存储区与套接字的属主、权限和隔离。
- 注入数据卷故障：把卷建在 device-mapper 设备上，换成 `error` 目标使 Btrfs 转为只读，再懒卸载与重新挂载，并在上传中途强制终止相册服务，确认相册暂停服务、不写系统盘并在卷恢复后自行重新打开。

真实模型测试、运行环境构建和数据集评估见[AI 开发与评估](ai-development.md)。

相册的规模基线（4 名成员、20,000 张，约 5 分钟）与 12 MP 缩略图计时默认跳过，在同一 cgo 容器中加 `-e ANAS_PHOTO_SCALE=1` 运行 `go test -count=1 -timeout 60m -run 'TestScale' -v ./internal/photos/`；结果的解读见[相册技术设计](../architecture/photo-library.md)切片 7。

device-mapper 与 loop 设备属于宿主内核而不是容器，脚本退出时会停止服务并删除自己的 `anas-smoke-<容器名>` 设备。若脚本被强行中断，用 `ls /dev/mapper` 检查残留，再在特权容器中执行 `dmsetup remove <名称>`。容器缺少 `curl` 或 `dmsetup` 时脚本会临时安装：

```bash
docker run --rm --privileged -e CGO_ENABLED=1 \
  -v "$PWD:/src" <同上镜像> bash /src/scripts/smoke-photo-service.sh
```

## 系统测试

root 集成测试与相册冒烟都由测试进程直接启动服务，覆盖不到正式 systemd unit 的沙箱。[ADR 0008 升级后 Web 文件无法使用](../investigations/2026-10-08-host-agent-loses-setuid-under-systemd.md)正是这类缺口。系统测试在以 systemd 为 PID 1 的 Debian 13 容器中，用真实安装器、unit 与发布二进制安装 A-NAS，再按用户检查：

- 装好的单元连同安装器写的 drop-in，经与 NAS 相同版本的 `systemd-analyze verify` 校验，警告也算失败。`make ops-check` 的 grep 只核对单元中的约定值，不做这项：校验要求单元启动的命令与依赖的单元都存在，开发机与 CI 的检查作业没有它们；
- Host Agent 保留 `CAP_SETUID`/`CAP_SETGID` 并通过启动自检；
- Web 文件的读写、下载、删除与恢复以登录用户 UID 落盘，成员与管理员互相不可见；
- SMB 与 Web 互相可见对方写入的文件，使用同一组 ACL、同一个回收站和同一个密码；
- 禁用账号会断开它连着的 SMB 会话，之后产品服务启动时的身份同步照常成功。Host Agent 的沙箱让 Samba 命令在标准错误打印告警，root 集成测试碰不到这一点（[调查](../investigations/2026-10-26-smbstatus-warning-fails-identity-sync.md)）；
- 产品服务账号与相册服务进不了任何空间，相册上传在专用身份下工作；
- 安装器配置的 Caddy 局域网入口能转发登录与上传，经入口的登录拿不到本机长期会话；
- 活动记录为经入口的登录记下客户端地址、为本机屏幕登录记下来源；Host Agent 在正式单元的沙箱中以 `journalctl` 读到产品服务与 systemd 关于白名单单元的日志，成员读不到系统日志，白名单外的单元被拒绝（[ADR 0020](../adr/0020-read-service-logs-through-the-host-agent.md)）；
- 重启 Host Agent 后以上行为保持；
- 屏保视频的独立通道（[ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)）：暂存只上传缺少的视频并复用旧 release 暂存过的视频；安装后的池成为当前池，release 升级保留它；共有的视频只存一份；重复安装同一个池不产生变化；切回已安装的池不需要暂存区；视频哈希不符或池 ID 与清单不符时拒绝且不留改动；从旧安装器首次升级时以硬链接导入它留下的池；
- 本地 AI 随系统安装（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）：套接字只对相册服务的组开放，搜索经套接字按需启动 Worker，Worker 以动态分配的身份运行；单元的 CPU 上限是本机逻辑核数减 2（安装器写入的 drop-in），内存先回收后上限；上传的照片在 Worker 沙箱中得到向量与检测结果（相册服务的空闲期经 drop-in 缩短为 2 秒）；放在 Worker 自己单元中的探针打不开网络套接字、列不出数据卷，而同一探针在沙箱外可以；暂存的模型或检测器与清单不符时拒绝安装且不留改动；升级不重复复制模型、检测器与运行环境，并启动新 release 的 Worker。CI 使用替身模型与检测器、只含 Debian Python 的运行环境与 Worker 的假 Provider；
- 照片文字识别（[实施方案](../architecture/photo-ocr.md)）：release 链接按内容保存一次的 llama.cpp，只含清单列出的文件；llama-server 在 Worker 自己的单元中能运行；Worker 经套接字读出照片中的文字；压缩包与清单不符时拒绝安装且不留改动。替身下读文字用假 Provider，真实模型时由 llama-server 读出合成图片上的字。运行前需要 `scripts/fetch-llama.sh` 取得的压缩包；
- 向量索引（[ADR 0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)）：
  - 套接字只属于相册服务的账号，产品服务的账号连不上；
  - 连接时拉起实例，实例与代理同在一个只有 `lo` 的网络命名空间，以每个实例自己的动态身份运行；
  - 代理空闲（经 drop-in 缩短为 3 秒）后实例停止；
  - 换了 Qdrant 二进制时存储被清空；
  - 放在实例单元中的探针看不到网络、读不到数据卷与产品状态；
  - 篡改的压缩包被拒绝；
  - 相册服务建好集合、同步照片，并记下搜索的影子比对。

  运行前需要 `scripts/fetch-qdrant.sh` 取得的压缩包。
- 最后卸载数据卷，相册服务在自己的挂载命名空间中察觉并停止服务。容器的挂载默认是 private，脚本先改为与 Debian 主机一致的 shared，否则宿主机的卸载传不进服务的命名空间。

镜像准备、运行命令和结果读取统一见 [system-test](../../.claude/skills/system-test/SKILL.md)。以下流程按 Windows/WSL 的 Linux x86_64 环境验证；macOS/ARM64 的限制见[环境说明](environments/macos.md#运行检查)。

- 镜像用 debootstrap 从 `deb.debian.org` 组装，因此不依赖 Docker Hub；镜像内 systemd 版本与实验 NAS 一致。
- 容器没有真实磁盘，脚本在 `lsblk` 边界把 loop 卷呈现为 SATA 数据盘，并写入与存储初始化相同的卷记录；`lsblk` 之上的 Host Agent、存储状态和卷校验均为正式代码。
- CI 的 `System test (Debian 13, systemd)` 作业运行同一流程。
- 部署前按 [system-test 的真实模型变体](../../.claude/skills/system-test/SKILL.md#变体)复测；模型准备见[AI 开发与评估](ai-development.md)。

## CI

`.github/workflows/ci.yml` 对每个 PR 和每次合入 `main` 运行三个作业：`Project checks`（`make check`）、`Go tests with the race detector`（`make race-test`）与 `System test (Debian 13, systemd)`（`make system-test`）。`main` 的规则集按作业名要求三者通过才能合并，改作业名时同时改规则集。新的强制检查加进这三个作业，通常是 Makefile 的 `check-steps`，规则集不用改。

PR 只改文档时，`Project checks` 只运行 `make docs-check`，另两个作业按条件跳过，GitHub 把条件跳过的作业记为通过。文档指 `docs/` 下的文件、根目录的 `*.md` 和 `.claude/skills/` 中的 `*.md`；其他目录中的 Markdown 随产品发布（助手技能、应用目录的说明），按代码处理。判定见 `scripts/ci-scope.sh`，判定失败时三个作业照常完整运行。合入 `main` 后的运行总是完整的。

## 关联

- [Makefile](../../Makefile)
- [CI 工作流](../../.github/workflows/ci.yml)
- [AI 开发与评估](ai-development.md)
