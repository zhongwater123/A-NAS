# 当前状态

更新时间：2026-10-06

## 当前阶段

M2：只读硬件闭环进行中；基础 Linux Adapter 已完成，健康工具与 Host Agent IPC 尚待补齐。

## 当前目标

查明未检测到的数据盘，补齐 SMART/NVMe 只读健康能力，并原型验证 Host Agent IPC；产品服务仍不直接加载 Linux Adapter。

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
- Codex 已通过 Windows `ssh-agent` 对实验 NAS 完成无交互 SSH 验证；当前维护窗口使用服务器端公钥到期选项，精确到期时间只保留在操作者会话中，不写入仓库。
- 实验 NAS 的用户级 systemd 正常、home 可用空间约 213 GB；Git、Go、Make 和 curl 尚未安装，`anas-dev` 的 linger 尚未启用。
- M1 `anas-api` 的版本 `03fc068` 已作为校验过哈希的用户态制品上传，并在 Debian 13 上完成三个 Fake API 的临时冒烟验收；进程已优雅停止，见 [部署运行手册](../runbooks/deploy-m1-api-to-experimental-nas.md)。
- Debian Linux Adapter 已通过既有 `hoststate.Reader` seam 实现：从 `os-release`、machine-id、uptime 和固定参数的 `lsblk --json` 生成只读状态，公开 ID 不泄露原始机器或磁盘标识。
- Linux Adapter 已在实验 NAS 上通过显式启用的真实主机集成测试；当前识别唯一 NVMe 系统盘和安装 U 盘，健康状态保持 `unknown`，不伪报 SMART 结果。

## 下一步

1. 安全关机后拔除 Debian 安装 U 盘，再次确认系统从 NVMe 独立启动。
2. 在 BIOS 和物理连接层排查 512 GB 机械盘未被 Debian 检测到的原因；检测到后仍不格式化。
3. 由管理员安装 `smartmontools` 和 `nvme-cli` 后，补充只读 SMART、NVMe 健康与温度基线。
4. 比较并原型验证 Host Agent IPC 候选，但不提前加入特权写操作，也不让产品服务直接加载 Linux Adapter。
5. 决定实机采用“本机构建后上传制品”还是额外安装构建工具；首选上传可追溯制品，避免把实验 NAS 变成源码事实源。
6. 由管理员决定是否为 `anas-dev` 启用 linger，以便无 root 的用户级服务在 SSH 登出后持续运行。

## 尚未阻塞本地开发的外部工作

- 实验 NAS 的 SSH 与非 root 账号已就绪；硬件基线尚未完成，512 GB 机械盘当前未出现在 `lsblk`。
- 当前限时 SSH 授权足以进行用户态部署；持久用户服务仍需要管理员启用 linger，或另行选择 root 管理的 systemd 系统服务。
- Host Agent IPC、前端、数据库、容器和双盘策略仍待决策。
- 私有 GitHub 仓库的 Actions 运行状态需要有效的 GitHub CLI 登录或网页查看。

## 验证基线

```bash
bash scripts/check-dev-env.sh
make check
```
