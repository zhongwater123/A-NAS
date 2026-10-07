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
- Docker 虽已存在于 WSL，但当前不是已冻结的 A-NAS 依赖，也未向 `anas-dev` 授权。
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

完整检查会自动执行 `npm ci`、类型检查、React 测试和 Vite 构建：

```bash
make check
```

只开发界面时：

```bash
cd web
npm ci
npm run dev
```

生成资源位于被 Git 忽略的 `internal/webui/dist/`，随后由 Go `embed` 写入产品二进制；NAS 不需要 Node.js。

## 尚待决定后再安装

以下项目应在对应 ADR 或技术原型完成后再固定版本：

- Docker/Moby 或 Podman；
- PostgreSQL 等常驻数据库；
- OpenAPI、Protobuf 等代码生成器；
- AI Provider SDK 和本地模型 Runtime。
