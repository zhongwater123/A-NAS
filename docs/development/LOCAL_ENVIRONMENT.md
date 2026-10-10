# 本地开发环境

更新时间：2026-10-08

项目有两台 Windows 开发机。[检出换行](#windows-检出换行)、[Root 集成测试](#root-集成测试)、[系统测试](#系统测试)和[实验 NAS 接入](#实验-nas-接入)适用于两台；两台的差异分列在各自的小节。

## Windows 检出换行

仓库依赖 LF（`gofmt` 检查会拒绝 CRLF）。在 Windows 全局启用 `core.autocrlf=true` 的机器上克隆后，先在仓库内覆盖再重新检出：

```bash
git config core.autocrlf false
git config core.eol lf
```

`.ps1` 等脚本仍由 `.gitattributes` 保持 CRLF。

## 开发机 A：`E:\A-NAS`

| 项目 | 状态 |
|---|---|
| Windows 工作区 | `E:\A-NAS` |
| WSL | WSL2，Ubuntu 24.04 LTS，systemd 已启用 |
| WSL 开发用户 | `anas-dev`，独立 home：`/home/anas-dev` |
| WSL 工作区入口 | `/home/anas-dev/workspace/A-NAS` → `/mnt/e/A-NAS` |
| Git | 仓库默认分支为 `main`，WSL 使用 LF |
| 通用工具 | Git、C/C++ 构建工具、CMake、Ninja、Python、jq、ripgrep、ShellCheck、SQLite、Ansible |
| 产品工具链 | Go 1.27.1；Node.js 26.9.0；npm 11.19.1 |

`scripts/deploy-dev.ps1` 的默认参数（WSL `Ubuntu-24.04`、用户 `anas-dev`、`~/workspace/A-NAS`、密钥 `a-nas-dev_ed25519`）按这台机器设置。

### 隔离边界

- `anas-dev` 不复用现有的 `docker-dev` home、Git 配置、SSH 密钥或语言缓存。
- `anas-dev` 不属于 `sudo` 或 `docker` 组，日常开发默认非特权运行。
- Windows 已为实验 NAS 创建项目专用 ED25519 密钥，默认路径为 `%USERPROFILE%\.ssh\a-nas-dev_ed25519`；私钥内容和口令不进入仓库。
- 容器运行时已按 [ADR 0009](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md) 选定 Docker Engine；`anas-dev` 仍不加入 `docker` 组。本地联调可在具备 Docker 权限的账号下以 `ANAS_CONTAINER_AGENT_SOCKET=/tmp/...` 运行 `build/anas-container-agent`，再以 `ANAS_CONTAINERS_MODE=agent` 启动产品服务；只对专用测试容器执行启停。
- Git 用户名和邮箱需要由开发者在 `anas-dev` 下自行设置，仓库不记录个人身份。
- Go 安装在 `/opt/go/1.27.1`，`anas-dev` 使用自己的模块、构建和工具缓存。
- 前端依赖由 `web/package-lock.json` 固定；不要同时从 Windows 与 WSL 对同一个 `web/node_modules` 执行安装。

### 日常使用

从 Windows 进入专用开发环境：

```powershell
wsl -d Ubuntu-24.04
```

进入项目并检查环境：

```bash
cd ~/workspace/A-NAS
bash scripts/check-dev-env.sh
```

需要安装或维护 WSL 系统包时，显式使用 root，而不是提升日常开发账号：

```powershell
wsl -d Ubuntu-24.04 -u root
```

## 开发机 B：`D:\A-NAS`

| 项目 | 状态 |
|---|---|
| Windows | Windows 11 专业版（build 26200）；源码 `D:\A-NAS`，Claude Code 的工作树位于 `D:\A-NAS\.claude\worktrees\` |
| WSL | WSL2，发行版名 `Ubuntu`（Ubuntu 26.04 LTS），systemd 已启用 |
| WSL 用户 | 日常用户，属于 `docker` 组，没有免密 `sudo`；没有 `anas-dev` |
| WSL 工作区入口 | 直接使用 `/mnt/d/A-NAS` 或对应工作树 |
| Git | Git for Windows；仓库内已设置 `core.autocrlf=false`、`core.eol=lf` |
| 产品工具链 | Go 1.27.1、Node.js 26.9.0、npm 11.19.1、ShellCheck 0.11.0 与 jq，均以用户身份装在 `~/.local/opt`，`. ~/.anas-env` 把它们加入 `PATH`；WSL 中没有 gcc |
| Docker | WSL 内 Docker Engine 29.1.3，cgroup v2；Docker Hub 不可达，`deb.debian.org` 与 `proxy.golang.org` 可达 |
| 本地镜像 | `anas-acl-matrix:bookworm`：Debian 12，加装 gcc、acl、btrfs-progs、samba、samba-vfs-modules 与 smbclient，用于 cgo 测试与 root 集成测试；`anas-systemd:trixie`：由 `scripts/build-system-test-image.sh` 以前者为构建器生成，用于系统测试 |

### 运行检查

文档、运维与前端检查直接在 WSL 中运行：

```bash
. ~/.anas-env
cd /mnt/d/A-NAS
make docs-check ops-check web-test web-build
```

WSL 没有 gcc，依赖 cgo（SQLite）的 Go 命令在 `anas-acl-matrix:bookworm` 中以当前用户运行；以 root 运行会改变依赖权限检查的文件代理与文件测试。模块缓存先在 WSL 中用 `go mod download` 填好：

```bash
docker run --rm -u "$(id -u):$(id -g)" -v "$PWD:/src" -w /src \
  -v "$HOME/.local/opt/go:/usr/local/go:ro" -v "$HOME/go/pkg/mod:/gomod:ro" \
  -e GOMODCACHE=/gomod -e GOCACHE=/tmp/gocache -e HOME=/tmp -e GOPROXY=off -e GOTOOLCHAIN=local \
  -e GOFLAGS="-mod=readonly -buildvcs=false" -e CGO_ENABLED=1 \
  anas-acl-matrix:bookworm /usr/local/go/bin/go test ./...
```

把最后的 `go test ./...` 换成 `go vet ./...` 或 `go build -o build/ ./cmd/anas-api ./cmd/anas-host-agent` 即得到对应检查。root 集成测试使用同一镜像，去掉 `-u`，加上 `--privileged -e ANAS_ROOT_INTEGRATION=1`，运行 `go test -tags rootintegration -count=1 ./internal/hostops/linux`。

`make system-test` 在本机会因 `build-binaries` 需要 gcc 而失败，按以下顺序运行系统测试：

```bash
scripts/build-system-test-image.sh anas-systemd:trixie anas-acl-matrix:bookworm   # 只需一次
make web-build                                                                     # WSL 中
# 用上面的容器执行 go build -o build/ ./cmd/anas-api ./cmd/anas-host-agent
bash scripts/system-test.sh
```

注意事项：

- 从 PowerShell 5.1 向 `wsl ... bash -c '...'` 传带引号的命令会丢失引号；多行命令先写成脚本，再运行 `wsl -d Ubuntu -- bash <脚本>`。
- 不要用 PowerShell 5.1 的 `Set-Content -Encoding utf8` 改仓库文件，它会写入 BOM。
- 在 `/mnt/d` 上运行 Vite 开发服务器时设置 `CHOKIDAR_USEPOLLING=1`，否则看不到 Windows 侧的修改。
- 本机 Docker 上还运行着与 A-NAS 无关的容器；测试脚本只启动并删除自己命名的容器，不要清理其他容器。

### 与实验 NAS 联调

- SSH 别名 `a-nas` 写在 `%USERPROFILE%\.ssh\config`，以 Debian 安装时创建的普通账号 `guoyi` 登录，密钥为 `%USERPROFILE%\.ssh\a-nas-guoyi_ed25519`（无口令，只授权给本机）。该账号不属于 `sudo`，只用于只读检查与联调；任何 root 操作仍由管理员按运行手册执行。
- SSH 别名 `a-nas-dev` 以 `anas-dev` 登录，密钥为 `%USERPROFILE%\.ssh\a-nas-dev_ed25519`（无口令，只授权给本机），用于暂存 release。
- `deploy-dev.ps1` 只有在 `build/.validated-build` 与提交、产品版本和两个二进制哈希都一致时才跳过 WSL 中的 `make check`。2026-10-09 暂存 `64f0e26d8349` 时本机还缺 gcc，所以按[运行检查](#运行检查)逐项运行 `check-steps` 的全部目标（cgo 部分在容器中，`go build` 加 `-trimpath -ldflags "-X main.version=<12 位提交>"`），全部通过后按 Makefile `check` 目标的格式写入该文件，再运行 `scripts/deploy-dev.ps1 -NasHost a-nas-dev -StageOnly -WslDistribution Ubuntu -WslUser <WSL 用户>`。同日 WSL 已有 gcc，但在 `/mnt/d` 上 vitest worker 启动超时：把工作树复制到 WSL 自身的 ext4 目录（排除 `.git`、`web/node_modules` 与 `build`），在那里 `npm ci` 后运行 `make check-steps VERSION=<版本>` 与 `bash scripts/system-test.sh`。
- 屏保视频不随 release 暂存。只有更换视频时，才用 `deploy-screensavers.ps1` 上传 NAS 还没有的文件，见[本地控制台运行手册](../runbooks/operate-local-kiosk.md#更换屏保视频)。系统安装器随 release 一起暂存，所以基于 [ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md) 之前主干的分支仍使用旧安装器，会在不带视频的升级后停播屏保；这类分支应先合入主干再部署。
- 在 PowerShell 5.1 中运行 `deploy-dev.ps1` 不要加 `2>&1`：Windows OpenSSH 的 `close - IO is still pending` 会变成错误记录，使脚本在 `ErrorActionPreference = 'Stop'` 下中止。
- 撤销本机访问：删除 NAS 上 `/home/guoyi/.ssh/authorized_keys` 中注释为 `claude-code@SKZ00011303->guoyi@a-nas` 的一行。

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

相册 AI Worker（`ai/`）的协议测试只需 Python 标准库：`make ai-test`。使用真实 EmbeddingGemma 2 的测试需要 MediaPipe，在 Debian 13 开发镜像中运行，模型从开发机预留位置只读挂载（见 [M2 实施方案](../architecture/photo-ai.md#模型与运行环境的交付)）：

```bash
docker build -t anas-ai-dev:trixie -f ai/dev.Dockerfile ai      # 基于 anas-systemd:trixie
docker run --rm --cpuset-cpus=0-3 -v "$PWD:/src:ro" -w /src/ai \
  -v /mnt/d/A-NAS-models/embeddinggemma-2-740m:/models:ro \
  -e ANAS_AI_MODEL=/models/embeddinggemma-2-740m.litertlm -e PYTHONDONTWRITEBYTECODE=1 \
  anas-ai-dev:trixie /opt/anas-ai/bin/python -m unittest discover -s tests -t .
```

发行用的 Worker 运行环境（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）也在开发机上构建：`scripts/build-ai-runtime.sh` 在同一开发镜像中按 [`ai/requirements.lock`](../../ai/requirements.lock) 的哈希安装 wheel，打包为 `build/ai-runtime/<ID>.tar.gz`，并写出 `deploy-dev.ps1` 读取的 `build/ai-runtime.json`。同一 ID 的包已存在时直接复用，因此 NAS 上看到的哈希不变；锁文件改了版本后先运行 `scripts/build-ai-runtime.sh --lock` 更新哈希。两者都从 PyPI 下载，NAS 不需要。2026-10-10 的包 `ccd886271b067afa` 压缩后 163 MB，解开约 460 MB。

AI 标签阈值的校准（`ai/eval/`）使用放在 `D:\A-NAS-datasets`（WSL 中为 `/mnt/d/A-NAS-datasets`）的公开数据集，只在开发机使用，不进入仓库、NAS 或发行包；数据集来源、许可与结论见[标签校准报告](../research/photo-ai-label-calibration.md)。COCO 与 Open Images 元数据按报告中的地址下载并解压后，依次逐张下载其余图片、计算向量、校准阈值：

```bash
AI="docker run --rm -v $PWD:/src -w /src/ai -v /mnt/d/A-NAS-datasets:/datasets \
  -v /mnt/d/A-NAS-models/embeddinggemma-2-740m:/models:ro -v $HOME/.cache/anas-ai-xnnpack:/xnnpack \
  -e PYTHONDONTWRITEBYTECODE=1 anas-ai-dev:trixie /opt/anas-ai/bin/python"
$AI -m eval.fetch --budget-gb 4                       # 先用 HEAD 计算体积，超出预算不下载
$AI -m eval.embed --model /models/embeddinggemma-2-740m.litertlm --weight-cache /xnnpack --tokens 70
$AI -m eval.calibrate --cache "/datasets/cache/<模型 ID>-e512" --report /datasets/report.json --write
```

`eval.embed` 可中断，重跑时只补算缺少的向量；权重缓存放在 WSL 自己的磁盘上，加载比放在 `/mnt/d` 快。`--write` 不带路径时更新 `internal/photos/labels/v1.calibration.json`；`--compare-with` 在两份缓存共有的图片上比较视觉 token 或输入尺寸。

相册的规模基线（4 名成员、20,000 张，约 5 分钟）与 12 MP 缩略图计时默认跳过，在同一 cgo 容器中加 `-e ANAS_PHOTO_SCALE=1` 运行 `go test -count=1 -timeout 60m -run 'TestScale' -v ./internal/photos/`；结果的解读见[相册技术设计](../architecture/photo-library.md)切片 7。

device-mapper 与 loop 设备属于宿主内核而不是容器，脚本退出时会停止服务并删除自己的 `anas-smoke-<容器名>` 设备。若脚本被强行中断，用 `ls /dev/mapper` 检查残留，再在特权容器中执行 `dmsetup remove <名称>`。容器缺少 `curl` 或 `dmsetup` 时脚本会临时安装：

```bash
docker run --rm --privileged -e CGO_ENABLED=1 \
  -v "$PWD:/src" <同上镜像> bash /src/scripts/smoke-photo-service.sh
```

## 系统测试

root 集成测试与相册冒烟都由测试进程直接启动服务，覆盖不到正式 systemd unit 的沙箱。[ADR 0008 升级后 Web 文件无法使用](../investigations/2026-10-08-host-agent-loses-setuid-under-systemd.md)正是这类缺口。系统测试在以 systemd 为 PID 1 的 Debian 13 容器中，用真实安装器、unit 与发布二进制安装 A-NAS，再按用户检查：

- Host Agent 保留 `CAP_SETUID`/`CAP_SETGID` 并通过启动自检；
- Web 文件的读写、下载、删除与恢复以登录用户 UID 落盘，成员与管理员互相不可见；
- SMB 与 Web 互相可见对方写入的文件，使用同一组 ACL、同一个回收站和同一个密码；
- 产品服务账号与相册服务进不了任何空间，相册上传在专用身份下工作；
- 安装器配置的 Caddy 局域网入口能转发登录与上传，经入口的登录拿不到本机长期会话；
- 重启 Host Agent 后以上行为保持；
- 屏保视频的独立通道（[ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)）：暂存只上传缺少的视频并复用旧 release 暂存过的视频；安装后的池成为当前池，release 升级保留它；共有的视频只存一份；重复安装同一个池不产生变化；切回已安装的池不需要暂存区；视频哈希不符或池 ID 与清单不符时拒绝且不留改动；从旧安装器首次升级时以硬链接导入它留下的池；
- 本地 AI 随系统安装（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）：套接字只对相册服务的组开放，搜索经套接字按需启动 Worker，Worker 以动态分配的身份运行；放在 Worker 自己单元中的探针打不开网络套接字、列不出数据卷，而同一探针在沙箱外可以；暂存的模型与清单不符时拒绝安装且不留改动；升级不重复复制模型与运行环境，并启动新 release 的 Worker。CI 使用替身模型、只含 Debian Python 的运行环境与 Worker 的假 Provider；
- 最后卸载数据卷，相册服务在自己的挂载命名空间中察觉并停止服务。容器的挂载默认是 private，脚本先改为与 Debian 主机一致的 shared，否则宿主机的卸载传不进服务的命名空间。

```bash
scripts/build-system-test-image.sh anas-systemd:trixie <本地任一 Debian 镜像>
make system-test
```

- 镜像用 debootstrap 从 `deb.debian.org` 组装，因此不依赖 Docker Hub；镜像内 systemd 版本与实验 NAS 一致。
- 容器没有真实磁盘，脚本在 `lsblk` 边界把 loop 卷呈现为 SATA 数据盘，并写入与存储初始化相同的卷记录；`lsblk` 之上的 Host Agent、存储状态和卷校验均为正式代码。
- CI 的 `System test (Debian 13, systemd)` 作业运行同一流程。
- 部署前在开发机用真实运行环境与模型再跑一遍，同样的检查改由真实模型编码查询：

  ```bash
  scripts/build-ai-runtime.sh
  ANAS_SYSTEM_TEST_AI=real ANAS_AI_MODEL=/mnt/d/A-NAS-models/embeddinggemma-2-740m/embeddinggemma-2-740m.litertlm     bash scripts/system-test.sh anas-systemd:trixie
  ```

## 实验 NAS 接入

| 项目 | 当前状态 |
|---|---|
| 主机名 | `a-nas-dev` |
| 系统 | Debian 13 trixie amd64 |
| SSH | OpenSSH 已启用；开发机 A 以 `anas-dev`、开发机 B 以 `guoyi` 的密钥登录已验证 |
| 开发账号 | `anas-dev`（暂存制品）与 `guoyi`（只读联调），均无 `sudo` 权限 |
| 地址 | DHCP 地址，不写入仓库；每次使用前按需确认 |

首次配置、指纹核对、密钥轮换和恢复步骤见[实验 NAS SSH 运行手册](../runbooks/bootstrap-experimental-nas-ssh.md)。尚未关闭密码登录；完成恢复路径与第二把管理员密钥设计前不做 SSH 全局加固。

## Web 桌面

完整检查在依赖锁变化时执行 `npm ci`，随后运行类型检查、React 测试和 Vite 构建：

```bash
make check
```

日常迭代先运行受影响的现有目标，例如 `go test ./internal/storage`、`make web-test` 或 `make ops-check`；RC 提交只运行一次完整 `make check VERSION=<RC>`。完整检查会把提交、产品版本和两个二进制哈希写入可丢弃的 `build/.validated-build`，`deploy-dev.ps1` 只有在四者全部一致时才复用结果，否则自动重跑完整门禁。这样不会用“跳过测试”换速度，也不会为同一制品重复执行全量测试。

只开发界面时：

```bash
cd web
npm ci
npm run dev
```

生成资源位于被 Git 忽略的 `internal/webui/dist/`，随后由 Go `embed` 写入产品二进制；NAS 不需要 Node.js。

## 尚待决定后再安装

以下项目应在对应 ADR 或技术原型完成后再固定版本：

- PostgreSQL 等常驻数据库；
- OpenAPI、Protobuf 等代码生成器；
- AI Provider SDK 和本地模型 Runtime。
