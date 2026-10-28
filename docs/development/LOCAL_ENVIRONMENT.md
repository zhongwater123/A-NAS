# 本地开发环境

更新时间：2026-10-27

本页是开发环境入口。项目有两台 Windows 开发机和一台 macOS 开发机；按机器与任务选择下列文档，无需通读其他机器的配置。

## 选择环境

| 环境 | 配置与限制 |
|---|---|
| 开发机 A：`E:\A-NAS`、WSL Ubuntu 24.04、用户 `anas-dev` | [Windows 与 WSL](environments/windows.md) |
| 开发机 B：`D:\A-NAS`、WSL Ubuntu 26.04 | [Windows 与 WSL](environments/windows.md) |
| 开发机 C：macOS、Colima `anas` ARM64 虚拟机 | [macOS 与 Colima](environments/macos.md) |

产品的 Go 服务面向 Linux。Windows/WSL 与 macOS/Colima 在虚拟机自己的目录中安装依赖和构建；具体同步命令见对应环境文档，宿主与虚拟机不共用 `node_modules`。所需版本以 [Go 版本](../../.go-version)、[Go 模块](../../go.mod)、[Web 锁文件](../../web/package-lock.json)、[Makefile](../../Makefile) 和各工具锁文件为准；机器表记录已安装状态。

## 按任务阅读

| 任务 | 权威位置 |
|---|---|
| 检查、Qdrant 依赖、Root 集成测试、系统测试与 CI | [测试与验证](testing.md) |
| 编辑 API、生成类型和响应校验 | [API 契约开发](api-contract.md) |
| 模型测试、AI 运行环境、数据集与评估 | [AI 开发与评估](ai-development.md) |
| Windows Fake 预览与会话隔离 | [fake-preview](../../.claude/skills/fake-preview/SKILL.md) |
| macOS Fake 预览 | [Colima 预览](environments/macos.md#fake-预览) |
| 实验 NAS 身份、SSH 指纹、密钥和连接配置 | [SSH 运行手册](../runbooks/bootstrap-experimental-nas-ssh.md) |
| 暂存、安装和回滚实验 NAS 服务 | [实验存储运行手册](../runbooks/provision-v1.0.1-experimental-storage.md) |

## 关联

- [项目开发规则](../../AGENTS.md)
- [文档路由](../README.md)
