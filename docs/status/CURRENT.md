# 当前状态

更新时间：2026-10-06

## 当前阶段

M1：建立可开发骨架。

## 当前目标

完成第一个只读本地开发闭环：定义系统与磁盘状态模型，通过 Fake Adapter 提供数据，并由产品 API 返回。

## 已就绪

- GitHub 远端、`main` 分支和首次提交已建立。
- WSL2 的隔离账号 `anas-dev` 与 Go 1.27.1 工具链可用。
- `anas-api`、`anas-host-agent`、本地检查和 GitHub Actions 骨架可用。
- 项目文档路由、模板和自动完整性检查可用。
- 产品服务与 Host Agent 使用 Go，见 [ADR 0001](../adr/0001-use-go-for-product-services.md)。

## 下一步

1. 定义只读的系统状态和磁盘状态类型。
2. 定义 Storage 能力契约并实现 Fake Adapter。
3. 为契约和 Fake Adapter 建立测试。
4. 在 `anas-api` 暴露只读系统与磁盘 API。
5. 创建首份功能规格并将验收场景链接到测试。

## 尚未阻塞本地开发的外部工作

- 实验 NAS 尚未记录 Debian 安装和 SSH 连通结果。
- Host Agent IPC、前端、数据库、容器和双盘策略仍待决策。
- 私有 GitHub 仓库的 Actions 运行状态需要有效的 GitHub CLI 登录或网页查看。

## 验证基线

```bash
bash scripts/check-dev-env.sh
make check
```
