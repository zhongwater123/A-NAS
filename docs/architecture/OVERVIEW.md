# 架构总览

本页记录开发时必须保持的系统边界和当前代码入口。完整产品背景与研究依据见 [PROJECT_CONTEXT.md](../../PROJECT_CONTEXT.md)。

## 系统边界

```text
客户端
  │ HTTPS / WebSocket
  ▼
产品服务（非特权）
  ├── Auth / Files / Storage / Share / Jobs / Policy
  ├── AI Orchestrator
  └── hoststate.Reader
          ├── Fake Adapter（当前本地开发与契约测试）
          └── Host Agent client（后续）
                    │ 窄而类型化的 IPC
                    ▼
                Host Agent（最小特权）
                    └── Linux Adapter（实验 NAS）

文件事件 → Ingestion → AI Provider → Search Index → Policy 过滤 → 引用原文件
```

## 不变量

1. AI 服务停止时，文件共享、权限、快照、备份和恢复仍然工作。
2. 产品服务以非 root 身份运行；特权操作只进入 Host Agent。
3. Host Agent 接收经过校验的高层意图，不提供任意 Shell 执行接口。
4. 磁盘和文件使用稳定资源 ID；临时路径和 `/dev/sdX` 不是身份。
5. 修改宿主机状态的操作遵循“规划、确认、执行”三阶段。
6. 文件浏览、共享、传统搜索和 AI 检索共同使用 Policy。
7. AI 派生数据可重建、可清除，原文件只读进入 AI Pipeline。
8. Fake 与 Linux Adapter 遵循同一契约，差异由契约测试暴露。

## 当前代码入口

| 路径 | 责任 | 当前状态 |
|---|---|---|
| `cmd/anas-api` | 产品 API 进程入口 | 可启动，支持优雅关闭 |
| `cmd/anas-host-agent` | Host Agent 进程入口 | 可构建的占位入口 |
| `internal/hoststate` | 只读宿主机状态接口与 Fake Adapter | 已实现并通过测试 |
| `internal/httpapi` | REST/JSON 路由与 DTO 映射 | 已实现并通过 OpenAPI 契约测试 |
| `api/openapi.yaml` | 客户端产品接口契约 | OpenAPI 3.1 |
| `tools/doccheck` | 文档结构和链接检查 | 开发工具 |
| `docs/specs` | 可验收的产品行为 | 已记录首个只读状态规格 |
| `docs/investigations` | 复杂缺陷的证据和根因 | 尚无调查记录 |
| `docs/runbooks` | 可重复且可验证的操作 | 尚无运行手册 |

新增模块时，应在其代码附近放置包级说明和测试；只有跨模块关系或系统不变量才更新本页。

## 关联

- [领域语言](../../CONTEXT.md)
- [当前状态](../status/CURRENT.md)
- [ADR 索引](../adr/README.md)
- [Go 语言决策](../adr/0001-use-go-for-product-services.md)
- [REST/OpenAPI 决策](../adr/0002-use-rest-openapi-for-product-clients.md)
- [只读宿主机状态规格](../specs/read-only-host-state.md)
