# 开发环境

更新时间：2026-11-26

日常编辑、Fake 预览与小范围测试优先在本机运行。完整门禁、race 测试、系统测试和长时间的评估默认放到[共享开发服务器](environments/shared-dev-server.md)；模型已在服务器上，或本机的空间、内存不足时，也把计算放到那里。

## 登记开发环境与已知问题

首次在一套环境中开发时，先查下表和对应的平台页，参考同类环境已有的记录。如果是新机器，在表中加一行，并在平台页登记实际验证过的配置。如果已有这台机器，直接更新原条目。同一台机器的后续会话共用这份记录。

- **登记实际状态。** 在机器条目写上核验日期、系统与架构、WSL 或虚拟机版本、工作目录，以及影响构建或测试的工具版本、权限或资源限制。环境变化后，更新受影响的字段和验证结果。
- **把踩过的坑写到下一位会找的地方。** 记录触发条件、可识别的报错或现象、有效做法，以及用什么命令确认已恢复。仅这台机器会遇到的问题写在机器条目；同类环境都可能遇到的写在平台页的公共章节。例如两台 Windows 机器共用一份[检出换行说明](environments/windows.md#windows-检出换行)。尚未确认的原因标为待验证。
- **让记录随修复交接。** 在解决问题的同一 PR 更新对应平台页；单独配置开发机时可提文档 PR。已有调查或操作手册时链接原文。账号接入和凭据由维护者单独交接。

| 已登记环境 | 状态与已知问题 |
|---|---|
| 开发机 A：`E:\A-NAS`、WSL Ubuntu 24.04、用户 `anas-dev` | [Windows 与 WSL](environments/windows.md) |
| 开发机 B：`D:\A-NAS`、WSL Ubuntu 26.04 | [Windows 与 WSL](environments/windows.md) |
| 开发机 C：macOS、Colima `anas` ARM64 虚拟机 | [macOS 与 Colima](environments/macos.md) |
| 共享开发服务器：WSL2 Ubuntu 24.04，多人共用 | [共享开发服务器](environments/shared-dev-server.md) |

产品的 Go 服务面向 Linux。Windows/WSL 与 macOS/Colima 在虚拟机自己的目录中安装依赖和构建；具体同步命令见对应环境文档，宿主与虚拟机不共用 `node_modules`。所需版本以 [Go 版本](../../.go-version)、[Go 模块](../../go.mod)、[Web 锁文件](../../web/package-lock.json)、[Makefile](../../Makefile) 和各工具锁文件为准；机器表记录已安装状态。

## 共享开发服务器

申请账号、机器情况、在上面运行门禁与评估的做法和已知问题，见[共享开发服务器](environments/shared-dev-server.md)。验证设备驱动、实盘或已安装服务时，使用对应的[实验 NAS 运行手册](../runbooks/README.md)。

## 按任务阅读

| 任务 | 权威位置 |
|---|---|
| 检查、Qdrant 依赖、Root 集成测试、系统测试与 CI | [测试与验证](testing.md) |
| 编辑 API、生成类型和响应校验 | [API 契约开发](api-contract.md) |
| 模型测试、AI 运行环境、数据集与评估 | [AI 开发与评估](ai-development.md) |
| Windows Fake 预览与会话隔离 | [fake-preview](../../.agents/skills/fake-preview/SKILL.md) |
| macOS Fake 预览 | [Colima 预览](environments/macos.md#fake-预览) |
| 实验 NAS 身份、SSH 指纹、密钥和连接配置 | [SSH 运行手册](../runbooks/bootstrap-experimental-nas-ssh.md) |
| 暂存、安装和回滚实验 NAS 服务 | [实验存储运行手册](../runbooks/provision-v1.0.1-experimental-storage.md) |

## 关联

- [项目开发规则](../../AGENTS.md)
- [文档路由](../README.md)
