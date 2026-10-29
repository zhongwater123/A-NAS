# Windows 与 WSL 开发环境

更新时间：2026-10-27

记录开发机 A、B 的配置与差异。检查目标见[测试与验证](../testing.md)；完整门禁命令统一维护在 [check-in-wsl](../../../.claude/skills/check-in-wsl/SKILL.md)，Fake 预览按 [fake-preview](../../../.claude/skills/fake-preview/SKILL.md)。

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
- 容器运行时已按 [ADR 0009](../../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md) 选定 Docker Engine；`anas-dev` 仍不加入 `docker` 组。本地联调可在具备 Docker 权限的账号下以 `ANAS_CONTAINER_AGENT_SOCKET=/tmp/...` 运行 `build/anas-container-agent`，再以 `ANAS_CONTAINERS_MODE=agent` 启动产品服务；只对专用测试容器执行启停。
- Git 用户名和邮箱需要由开发者在 `anas-dev` 下自行设置，仓库不记录个人身份。
- Go 安装在 `/opt/go/1.27.1`，`anas-dev` 使用自己的模块、构建和工具缓存。产品要求的版本由 `.go-version` 与 `go.mod` 的 `toolchain` 行固定；本机 Go 较旧时，`go` 命令按默认的 `GOTOOLCHAIN=auto` 从 `proxy.golang.org` 下载该版本。
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
| 产品工具链 | Go 1.27.1、Node.js 26.9.0、npm 11.19.1、ShellCheck 0.11.0、lychee 0.24.2、jq 与 Ruff（见[静态检查](../testing.md#静态检查)），均以用户身份装在 `~/.local/opt`，`. ~/.anas-env` 把它们加入 `PATH`；`go.mod` 要求更新的 Go 时，`go` 命令自动下载；gcc 15.2 来自 Ubuntu 软件包（`/usr/bin/gcc`），cgo 可直接在 WSL 中编译 |
| Docker | WSL 内 Docker Engine 29.1.3，cgroup v2；Docker Hub 不可达，`deb.debian.org` 与 `proxy.golang.org` 可达 |
| 本地镜像 | `anas-acl-matrix:bookworm`：Debian 12，加装 gcc、acl、btrfs-progs、samba、samba-vfs-modules 与 smbclient，用于 root 集成测试，也可在 Debian 12 中复现 cgo 测试；`anas-systemd:trixie`：由 `scripts/build-system-test-image.sh` 以前者为构建器生成，用于系统测试 |

### 运行检查

文档与运维检查可以直接在 `/mnt/d` 上运行：

```bash
. ~/.anas-env
cd /mnt/d/A-NAS
make docs-check ops-check
```

完整门禁按 [check-in-wsl](../../../.claude/skills/check-in-wsl/SKILL.md) 在 WSL 的 ext4 副本中运行，依赖 cgo（SQLite）的 Go 测试与构建直接用 WSL 的 gcc。要在 Debian 12 中复现 cgo 测试时，在 `anas-acl-matrix:bookworm` 中以当前用户运行；以 root 运行会改变依赖权限检查的文件代理与文件测试。模块缓存先在 WSL 中用 `go mod download` 填好：

```bash
docker run --rm -u "$(id -u):$(id -g)" -v "$PWD:/src" -w /src \
  -v "$HOME/.local/opt/go:/usr/local/go:ro" -v "$HOME/go/pkg/mod:/gomod:ro" \
  -e GOMODCACHE=/gomod -e GOCACHE=/tmp/gocache -e HOME=/tmp -e GOPROXY=off -e GOTOOLCHAIN=local \
  -e GOFLAGS="-mod=readonly -buildvcs=false" -e CGO_ENABLED=1 \
  anas-acl-matrix:bookworm /usr/local/go/bin/go test ./...
```

向量索引测试的依赖与强制运行要求见[Qdrant 测试依赖](../testing.md#qdrant-测试依赖)。

把最后的 `go test ./...` 换成 `go vet ./...` 或 `go build -o build/ ./cmd/anas-api ./cmd/anas-host-agent` 即得到对应检查。root 集成测试使用同一镜像，去掉 `-u`，加上 `--privileged -e ANAS_ROOT_INTEGRATION=1`，运行 `go test -tags rootintegration -count=1 ./internal/hostops/linux`。

系统测试按 [system-test](../../../.claude/skills/system-test/SKILL.md) 在同一个 ext4 副本中运行；构建系统测试镜像时以 `anas-acl-matrix:bookworm` 为构建器：

```bash
scripts/build-system-test-image.sh anas-systemd:trixie anas-acl-matrix:bookworm
```

注意事项：

- 从 PowerShell 5.1 向 `wsl ... bash -c '...'` 传带引号的命令会丢失引号；多行命令先写成脚本，再运行 `wsl -d Ubuntu -- bash <脚本>`。
- 不要用 PowerShell 5.1 的 `Set-Content -Encoding utf8` 改仓库文件，它会写入 BOM。
- 在 `/mnt/d` 上运行 Vite 开发服务器时设置 `CHOKIDAR_USEPOLLING=1`，否则看不到 Windows 侧的修改。
- 本机 Docker 上还运行着与 A-NAS 无关的容器；测试脚本只启动并删除自己命名的容器，不要清理其他容器。

### 与实验 NAS 联调

开发机 B 的 SSH 别名、授权撤销见[SSH 手册](../../runbooks/bootstrap-experimental-nas-ssh.md#开发机-b-的连接配置)。暂存与安装步骤见[实验存储手册](../../runbooks/provision-v1.0.1-experimental-storage.md#开发机暂存注意事项)。

## 关联

- [开发环境入口](../LOCAL_ENVIRONMENT.md)
- [环境检查脚本](../../../scripts/check-dev-env.sh)
