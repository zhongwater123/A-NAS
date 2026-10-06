# A-NAS

A-NAS 是一套基于 Debian 的 AI 智能家庭 NAS。项目首先保证存储、权限、备份与恢复可靠；AI 是可关闭、可审计、可替换的上层能力。

项目决策与当前背景以 [PROJECT_CONTEXT.md](PROJECT_CONTEXT.md) 为准。

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

产品服务和 Host Agent 使用 Go，当前版本记录在 `.go-version`。数据库和容器运行时仍未冻结。

## Go 开发

运行全部本地检查并构建两个初始命令：

```bash
make check
```

构建产物写入被 Git 忽略的 `build/`：

```text
build/anas-api
build/anas-host-agent
```

Go 模块路径为 `github.com/zhongwater123/A-NAS`。架构语言决策记录在 [ADR 0001](docs/adr/0001-use-go-for-product-services.md)。

启动使用确定性 Fake Adapter 的本地产品 API：

```bash
go run ./cmd/anas-api
```

默认监听 `127.0.0.1:8080`，可通过 `ANAS_HTTP_ADDR` 覆盖。当前接口为 `/healthz`、`/api/v1/system` 和 `/api/v1/disks`；完整契约见 [OpenAPI](api/openapi.yaml)。

## 项目文档

新会话从 [AGENTS.md](AGENTS.md) 开始；它会根据任务把开发者或 Agent 路由到当前状态、领域语言、架构、规格、调查记录或运行手册。文档分类和维护规则见 [文档系统](docs/README.md)。

文档结构和仓库内链接由 Go 工具检查：

```bash
make docs-check
```
