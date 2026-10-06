# 当前状态

更新时间：2026-10-06

## 当前阶段

M1：本地只读开发闭环已完成，准备进入 M2 只读硬件闭环。

## 当前目标

确认实验 NAS 的 SSH 与非 root 账号基线，原型验证 Host Agent IPC，并在不修改宿主机状态的前提下接入 Debian 13 Linux Adapter。

## 已就绪

- GitHub 远端、`main` 分支和首次提交已建立。
- WSL2 的隔离账号 `anas-dev` 与 Go 1.27.1 工具链可用。
- `anas-api`、`anas-host-agent`、本地检查和 GitHub Actions 骨架可用。
- 项目文档路由、模板和自动完整性检查可用。
- 产品服务与 Host Agent 使用 Go，见 [ADR 0001](../adr/0001-use-go-for-product-services.md)。
- `hoststate.Reader`、确定性 Fake Adapter 和契约测试可用。
- `anas-api` 提供只读系统、磁盘与存活接口，契约见 [OpenAPI](../../api/openapi.yaml)。
- 客户端产品接口使用 REST/JSON 与 OpenAPI，见 [ADR 0002](../adr/0002-use-rest-openapi-for-product-clients.md)。
- 首个可验收行为记录在 [只读宿主机状态规格](../specs/read-only-host-state.md)。

## 下一步

1. 记录实验 NAS 的 SSH、非 root 账号和基础硬件信息。
2. 比较并原型验证 Host Agent IPC 候选，但不提前加入特权写操作。
3. 定义只读 Linux Adapter 的发现范围与安全失败语义。
4. 让 Fake 与 Linux Adapter 复用同一 Reader 契约测试。
5. 将真实只读状态接入产品 API，并保留 Fake 开发模式。

## 尚未阻塞本地开发的外部工作

- 实验 NAS 已刷入 Debian 13 amd64；SSH、非 root 账号和硬件基线尚未记录。
- Host Agent IPC、前端、数据库、容器和双盘策略仍待决策。
- 私有 GitHub 仓库的 Actions 运行状态需要有效的 GitHub CLI 登录或网页查看。

## 验证基线

```bash
bash scripts/check-dev-env.sh
make check
```
