# 本地开发环境

更新时间：2026-10-06

## 当前基线

| 项目 | 状态 |
|---|---|
| Windows 工作区 | `E:\A-NAS` |
| WSL | WSL2，Ubuntu 24.04 LTS，systemd 已启用 |
| WSL 开发用户 | `anas-dev`，独立 home：`/home/anas-dev` |
| WSL 工作区入口 | `/home/anas-dev/workspace/A-NAS` → `/mnt/e/A-NAS` |
| Git | 仓库默认分支为 `main`，WSL 使用 LF |
| 通用工具 | Git、C/C++ 构建工具、CMake、Ninja、Python、jq、ripgrep、ShellCheck、SQLite、Ansible |
| 产品工具链 | Go 1.27.1；Node.js 26.9.0；npm 11.19.1 |

## Windows 检出换行

仓库依赖 LF（`gofmt` 检查会拒绝 CRLF）。在 Windows 全局启用 `core.autocrlf=true` 的机器上克隆后，先在仓库内覆盖再重新检出：

```bash
git config core.autocrlf false
git config core.eol lf
```

`.ps1` 等脚本仍由 `.gitattributes` 保持 CRLF。

## 隔离边界

- `anas-dev` 不复用现有的 `docker-dev` home、Git 配置、SSH 密钥或语言缓存。
- `anas-dev` 不属于 `sudo` 或 `docker` 组，日常开发默认非特权运行。
- Windows 已为实验 NAS 创建项目专用 ED25519 密钥，默认路径为 `%USERPROFILE%\.ssh\a-nas-dev_ed25519`；私钥内容和口令不进入仓库。
- 容器运行时已按 [ADR 0009](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md) 选定 Docker Engine；`anas-dev` 仍不加入 `docker` 组。本地联调可在具备 Docker 权限的账号下以 `ANAS_CONTAINER_AGENT_SOCKET=/tmp/...` 运行 `build/anas-container-agent`，再以 `ANAS_CONTAINERS_MODE=agent` 启动产品服务；只对专用测试容器执行启停。
- Git 用户名和邮箱需要由开发者在 `anas-dev` 下自行设置，仓库不记录个人身份。
- Go 安装在 `/opt/go/1.27.1`，`anas-dev` 使用自己的模块、构建和工具缓存。
- 前端依赖由 `web/package-lock.json` 固定；不要同时从 Windows 与 WSL 对同一个 `web/node_modules` 执行安装。

## 日常使用

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

容器缺少 `curl` 时脚本会临时安装：

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
- 重启 Host Agent 后以上行为保持。

```bash
scripts/build-system-test-image.sh anas-systemd:trixie <本地任一 Debian 镜像>
make system-test
```

- 镜像用 debootstrap 从 `deb.debian.org` 组装，因此不依赖 Docker Hub；镜像内 systemd 版本与实验 NAS 一致。
- 容器没有真实磁盘，脚本在 `lsblk` 边界把 loop 卷呈现为 SATA 数据盘，并写入与存储初始化相同的卷记录；`lsblk` 之上的 Host Agent、存储状态和卷校验均为正式代码。
- CI 的 `System test (Debian 13, systemd)` 作业运行同一流程。

## 实验 NAS 接入

| 项目 | 当前状态 |
|---|---|
| 主机名 | `a-nas-dev` |
| 系统 | Debian 13 trixie amd64 |
| SSH | OpenSSH 已启用；Windows 主机密钥登录已验证 |
| 开发账号 | `anas-dev`，无 `sudo` 权限 |
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
