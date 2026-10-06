# A-NAS

A-NAS 是一套基于 Debian 的 AI 智能家庭 NAS。项目首先保证存储、权限、备份与恢复可靠；AI 是可关闭、可审计、可替换的上层能力。

项目决策与当前背景以 [PROJECT_CONTEXT.md](PROJECT_CONTEXT.md) 为准。

当前拟发布基线为 [v1.0.0 首个硬件集成开发版候选](docs/releases/v1.0.0.md)：已打通只读 Live 状态、可回滚部署和设备本地控制台，但正式标签与候选部署尚未执行，也不是具备数据写入与共享能力的消费级 NAS 正式版。版本变化见 [CHANGELOG](CHANGELOG.md)。

## 本地开发入口

当前推荐环境是 Windows 主机加 WSL2 Ubuntu。源码保存在 `E:\A-NAS`，WSL 中通过专用用户访问：

```bash
wsl -d Ubuntu-24.04
cd ~/workspace/A-NAS
bash scripts/check-dev-env.sh
```

WSL 专用用户为 `anas-dev`。该账号不属于 `sudo` 或 `docker` 组，避免 A-NAS 开发环境默认拥有主机管理权限。需要维护 WSL 时，从 Windows 显式进入 root：

```powershell
wsl -d Ubuntu-24.04 -u root
```

仓库已使用公开 GitHub 身份配置本地提交信息；`anas-dev` 的全局 Git 身份保持为空，避免影响其他项目。

产品服务和 Host Agent 使用 Go，当前版本记录在 `.go-version`；Web 桌面使用 React、TypeScript、Vite 和 npm lockfile。数据库和容器运行时仍未冻结。

## 开发与检查

运行前端类型检查与测试、生产资源构建、文档检查、Go 检查，并构建两个 Linux 命令：

```bash
make check
```

构建产物写入被 Git 忽略的 `build/`：

```text
build/anas-api
build/anas-host-agent
```

Go 模块路径为 `github.com/zhongwater123/A-NAS`。架构语言决策记录在 [ADR 0001](docs/adr/0001-use-go-for-product-services.md)。

启动带内嵌 Web 桌面和确定性 Fake Adapter 的本地产品服务：

```bash
make web-build
go run ./cmd/anas-api
```

浏览器访问 `http://127.0.0.1:8080/`。服务默认只监听回环地址，可通过 `ANAS_HTTP_ADDR` 覆盖；聚合状态接口为 `/api/v1/host-state`，完整契约见 [OpenAPI](api/openapi.yaml)。

前端单独开发时可运行 `cd web && npm run dev`；Vite 把 `/api` 和 `/healthz` 代理到本地 Go 服务。前端技术原因见 [ADR 0003](docs/adr/0003-use-react-typescript-for-web-desktop.md)。

部署 Experimental NAS 和打开限定 SSH 隧道见 [Web 预览运行手册](docs/runbooks/deploy-web-preview-to-experimental-nas.md)。NAS 直连屏幕使用 Cage/Chromium 呈现同一个 Web 桌面，安装与恢复步骤见[本地控制台运行手册](docs/runbooks/operate-local-kiosk.md)。

## 项目文档

新会话从 [AGENTS.md](AGENTS.md) 开始；它会根据任务把开发者或 Agent 路由到当前状态、领域语言、架构、规格、调查记录或运行手册。文档分类和维护规则见 [文档系统](docs/README.md)。

文档结构和仓库内链接由 Go 工具检查：

```bash
make docs-check
```
