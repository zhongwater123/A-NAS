# 当前状态

更新时间：2026-10-06

## 当前阶段

M1：本地只读开发闭环已完成，准备进入 M2 只读硬件闭环。

## 当前目标

补齐实验 NAS 的只读硬件基线并查明未检测到的数据盘，然后原型验证 Host Agent IPC，在不修改宿主机状态的前提下接入 Debian 13 Linux Adapter。

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
- 实验 NAS 已运行 Debian 13 amd64，主机名为 `a-nas-dev`；SSH 服务和主机指纹已核对。
- 实验 NAS 的 `anas-dev` 为无 `sudo` 权限的独立账号，Windows ED25519 密钥登录已验证，见 [SSH 运行手册](../runbooks/bootstrap-experimental-nas-ssh.md)。

## 下一步

1. 安全关机后拔除 Debian 安装 U 盘，再次确认系统从 NVMe 独立启动。
2. 在 BIOS 和物理连接层排查 512 GB 机械盘未被 Debian 检测到的原因；检测到后仍不格式化。
3. 补充 CPU、内存、PCI、网卡、温度和磁盘健康的只读基线。
4. 比较并原型验证 Host Agent IPC 候选，但不提前加入特权写操作。
5. 定义只读 Linux Adapter 的发现范围、失败语义和契约测试。

## 尚未阻塞本地开发的外部工作

- 实验 NAS 的 SSH 与非 root 账号已就绪；硬件基线尚未完成，512 GB 机械盘当前未出现在 `lsblk`。
- Host Agent IPC、前端、数据库、容器和双盘策略仍待决策。
- 私有 GitHub 仓库的 Actions 运行状态需要有效的 GitHub CLI 登录或网页查看。

## 验证基线

```bash
bash scripts/check-dev-env.sh
make check
```
