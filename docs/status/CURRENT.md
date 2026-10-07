# 当前状态

更新时间：2026-10-07

## 当前阶段

v1.0.0 发布候选知识收敛已完成：只读硬件闭环、Web 桌面、Host Agent IPC、Live 部署和直连显示已在 Experimental NAS 验收；正式标签与部署尚未执行，本地 Kiosk 浏览器约束作为已知待处理项保留。

## 当前目标

由下一工作会话基于已冻结候选决定创建并部署 `v1.0.0` 标签，或先处理开放的 Kiosk 浏览器约束调查；随后进入数据盘发现和 SMART/NVMe 健康增量。

## 已就绪

- GitHub 远端、`main` 分支和 Go 1.27.1 工具链可用；WSL2 使用隔离的 `anas-dev`。
- 项目文档路由、模板和自动完整性检查可用。
- 产品服务与 Host Agent 使用 Go，客户端产品接口使用 REST/JSON 与 OpenAPI，见 [ADR 0001](../adr/0001-use-go-for-product-services.md)和 [ADR 0002](../adr/0002-use-rest-openapi-for-product-clients.md)。
- `hoststate.Reader`、确定性 Fake Adapter、Debian Linux Adapter 和契约测试可用。
- `anas-api` 提供存活、系统、磁盘和聚合宿主机状态接口；`/api/v1/host-state` 标明 `simulated` 或 `live`，契约见 [OpenAPI](../../api/openapi.yaml)。
- React/TypeScript Web 桌面实现资源管理、系统设置、窗口管理、断线保留和 Fake/Live 标识；Vite 资源嵌入 `anas-api`，见 [Web 桌面规格](../specs/web-desktop-host-state.md)。
- 桌面“终端”应用已实现并在本地 WSL2 验证：PTY Shell 以产品服务用户运行，仅接受回环同源连接，默认关闭，见[终端规格](../specs/web-terminal.md)；Experimental NAS 尚未启用。
- 桌面状态栏已重新设计为 CPU/内存仪表盘、网速与时间，指标经 Host Agent 采样并在本地 WSL2 Live 模式验证，见[状态栏规格](../specs/host-metrics-status-bar.md)；尚未部署到 Experimental NAS。
- 原始桌面原型已保存为独立证据提交 `86b034b`，没有进入 main 生产源码。
- Host Agent 通过权限为 `0600` 的 Unix Socket 暴露原子只读状态，Product Service 通过 Client Adapter 继续使用 `hoststate.Reader`，见 [ADR 0004](../adr/0004-use-http-json-over-unix-socket-for-host-state.md)。
- 一键部署、用户级 systemd、健康检查、自动回滚和限定 SSH 隧道均已通过实机验收，见 [Web 预览运行手册](../runbooks/deploy-web-preview-to-experimental-nas.md)。
- Experimental NAS 明确是带直连屏幕和输入设备的 ITX 设备，不是纯无头服务器；Cage/Chromium 已通过显示和鼠标交互验收，但浏览器约束和 VT 恢复尚未通过，见[开放调查](../investigations/2026-10-06-kiosk-browser-confinement.md)。
- Experimental NAS 已运行 Debian 13 amd64，主机名为 `a-nas-dev`；无 `sudo` 的 `anas-dev`、SSH 指纹与限时密钥登录已验证。
- Experimental NAS 用户级 systemd 正常、`anas-dev` linger 已启用，home 可用空间约 213 GB；Git、Go、Make 和 curl 未安装；Cage、Chromium 154、中文字体和 Wayland 调试工具已安装。
- 仓库发布候选 `a89de7411931` 已推送到 `main` 并以 `VERSION=v1.0.0` 通过完整 `make check`；NAS 当前部署仍为 `f010a90bd357` Live 模式，API、Host Agent 与 Kiosk 均为 `active`。正式标签和候选部署尚未执行。
- 远端制品哈希一致，UDS 目录/Socket 权限分别为 `0700`/`0600`。首次 Live 激活失败后自动回滚并完成修复，见[已关闭调查](../investigations/2026-10-06-host-agent-runtime-directory.md)。
- 直连 Apple Color LCD 以 `DP-2`、原生 `1536x2048@60.6Hz`、逆时针 `90` 度和 `1.5` 缩放持久运行；配置位于 root 管理的 `/etc/a-nas/kiosk.env`。
- Linux Adapter 已在 Experimental NAS 通过只读集成测试；当前识别 NVMe 系统盘和安装 U 盘，健康为 `unknown`，512 GB 机械盘仍未被检测到。
- M1 Fake API 制品曾完成临时冒烟并优雅停止；旧证据见 [M1 部署手册](../runbooks/deploy-m1-api-to-experimental-nas.md)。

## 下一步

1. 明确 `v1.0.0` 是否接受当前 Kiosk 已知限制；接受则创建带注释标签、部署并记录最终清单，不接受则先处理[开放调查](../investigations/2026-10-06-kiosk-browser-confinement.md)。
2. 决定是否在 Experimental NAS 的 `anas-api.env` 中启用 `ANAS_TERMINAL=enabled`（当前由 `remote-activate-release.sh` 生成，尚不写入该项）；启用前评估 Kiosk 浏览器约束和产品服务沙箱下 Shell 的只读 home 是否满足诊断需要。
3. 安全关机后拔除 Debian 安装 U 盘，再次确认系统从 NVMe 独立启动。
4. 在 BIOS 和物理连接层排查 512 GB 机械盘未被 Debian 检测到的原因；检测到后仍不格式化。
5. 由管理员安装 `smartmontools` 和 `nvme-cli` 后，补充只读 SMART、NVMe 健康与温度基线。

## 尚未阻塞本地开发的外部工作

- 当前限时 SSH 授权足以传输制品和建立限定浏览器隧道，常驻服务不依赖登录会话；密钥到期后两类远程访问都会按预期失效。
- 数据库、容器和双盘策略仍待决策；只读 Host Agent IPC 与前端技术栈已经冻结。
- GitHub Actions 运行状态需要有效的 GitHub CLI 登录或网页查看。

## 验证基线

```bash
bash scripts/check-dev-env.sh
make check
```
