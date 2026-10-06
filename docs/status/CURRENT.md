# 当前状态

更新时间：2026-10-06

## 当前阶段

M2：只读硬件闭环进行中；Web 桌面、基础 Linux Adapter、只读 Host Agent IPC 和本地 Kiosk 配置已实现，Experimental NAS 常驻部署与直连屏幕尚待验收。

## 当前目标

完成一次性 linger/SSH 限定转发和 Cage 本地控制台配置，把 Fake 与 Live 两个增量部署到 Experimental NAS，并在直连屏幕验收；随后继续排查数据盘并补齐 SMART/NVMe 只读健康。

## 已就绪

- GitHub 远端、`main` 分支和 Go 1.27.1 工具链可用；WSL2 使用隔离的 `anas-dev`。
- 项目文档路由、模板和自动完整性检查可用。
- 产品服务与 Host Agent 使用 Go，客户端产品接口使用 REST/JSON 与 OpenAPI，见 [ADR 0001](../adr/0001-use-go-for-product-services.md)和 [ADR 0002](../adr/0002-use-rest-openapi-for-product-clients.md)。
- `hoststate.Reader`、确定性 Fake Adapter、Debian Linux Adapter 和契约测试可用。
- `anas-api` 提供存活、系统、磁盘和聚合宿主机状态接口；`/api/v1/host-state` 标明 `simulated` 或 `live`，契约见 [OpenAPI](../../api/openapi.yaml)。
- React/TypeScript Web 桌面实现资源管理、系统设置、窗口管理、断线保留和 Fake/Live 标识；Vite 资源嵌入 `anas-api`，见 [Web 桌面规格](../specs/web-desktop-host-state.md)。
- 原始桌面原型已保存为独立证据提交 `86b034b`，没有进入 main 生产源码。
- Host Agent 通过权限为 `0600` 的 Unix Socket 暴露原子只读状态，Product Service 通过 Client Adapter 继续使用 `hoststate.Reader`，见 [ADR 0004](../adr/0004-use-http-json-over-unix-socket-for-host-state.md)。
- 一键部署、用户级 systemd、健康检查、自动回滚和限定 SSH 隧道脚本已实现；实机管理员配置与部署验收尚未执行，见 [Web 预览运行手册](../runbooks/deploy-web-preview-to-experimental-nas.md)。
- Experimental NAS 明确是带直连屏幕和输入设备的 ITX 设备，不是纯无头服务器；Cage/Chromium 单应用本地控制台已形成版本化配置，见[本地控制台运行手册](../runbooks/operate-local-kiosk.md)。
- Experimental NAS 已运行 Debian 13 amd64，主机名为 `a-nas-dev`；无 `sudo` 的 `anas-dev`、SSH 指纹与限时密钥登录已验证。
- Experimental NAS 用户级 systemd 正常、home 可用空间约 213 GB；Git、Go、Make 和 curl 未安装，linger 尚未启用；Cage/Chromium 等本地控制台包正在由管理员安装。
- Linux Adapter 已在 Experimental NAS 通过只读集成测试；当前识别 NVMe 系统盘和安装 U 盘，健康为 `unknown`，512 GB 机械盘仍未被检测到。
- M1 Fake API 制品曾完成临时冒烟并优雅停止；旧证据见 [M1 部署手册](../runbooks/deploy-m1-api-to-experimental-nas.md)。

## 下一步

1. 由管理员完成 Cage/Chromium 安装、启用 `anas-dev` linger，并按运行手册配置本地 Kiosk 与限制为 `127.0.0.1:8080` 的 SSH 转发。
2. 提交并推送当前版本，先以 `fake` 模式部署并在本地屏幕/远程隧道验收，再以 `agent` 模式核对真实 `lsblk` 状态与断线恢复。
3. 安全关机后拔除 Debian 安装 U 盘，再次确认系统从 NVMe 独立启动。
4. 在 BIOS 和物理连接层排查 512 GB 机械盘未被 Debian 检测到的原因；检测到后仍不格式化。
5. 由管理员安装 `smartmontools` 和 `nvme-cli` 后，补充只读 SMART、NVMe 健康与温度基线。

## 尚未阻塞本地开发的外部工作

- 当前限时 SSH 授权足以传输制品；常驻服务和浏览器隧道仍需要管理员完成一次性 linger 与 SSH 限定转发配置。
- 数据库、容器和双盘策略仍待决策；只读 Host Agent IPC 与前端技术栈已经冻结。
- GitHub Actions 运行状态需要有效的 GitHub CLI 登录或网页查看。

## 验证基线

```bash
bash scripts/check-dev-env.sh
make check
```
