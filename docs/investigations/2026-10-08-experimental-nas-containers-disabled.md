# Experimental NAS 的 Docker 与应用中心保持禁用

状态：root cause confirmed；安装与部署修复待实机验证
更新时间：2026-10-08

## 症状与影响

实验 NAS 的桌面可以打开“Docker”和“应用中心”，但两者都提示先安装 Docker 与 A-NAS 容器代理，并以容器代理模式启动产品服务。文件、账号、Kiosk 与屏保功能正常。

这两个窗口共用同一个容器能力边界；能力被禁用时，产品 API 对 `/api/v1/containers` 与 `/api/v1/apps` 返回 503，而前端显示安全启用说明，不会连接虚构的 Docker 数据。

## 最小反馈信号

下列只读检查直接观察运行依赖，不需要产品账号密码：

```bash
command -v docker || echo missing
systemctl is-active docker.service || true
systemctl is-active anas-container-agent.service || true
test -S /run/docker.sock && echo present || echo missing
getent passwd anas-container || echo missing
test -x /usr/local/lib/a-nas/anas-container-agent && echo present || echo missing
```

2026-10-08 首次检查稳定得到：Docker 二进制缺失、两个服务 inactive、Docker socket 缺失、`anas-container` 身份和系统代理二进制均缺失。删除任一检查都会失去对安装链某一层的判定，因此这是当前最小的宿主机反馈信号。

## 假设与证据

| 假设 | 可证伪预测 | 结果 |
|---|---|---|
| Docker 与容器代理从未部署 | 包、身份、单元、二进制与 socket 同时缺失 | confirmed |
| 依赖已安装但服务未启用或启动失败 | 至少存在 Docker 二进制、包或代理单元 | rejected |
| 只有产品服务环境缺少 `ANAS_CONTAINERS_MODE=agent` | Docker 和代理 active，只是 API 返回 disabled | rejected as sole cause；安装后仍须配置 |
| 产品服务没有代理 socket 权限 | socket 存在但 `a-nas` 读取失败 | rejected for current state；安装步骤必须防止该状态 |

## 根因

容器管理和应用中心只完成了代码、规格与本地 WSL2 Docker 验证；项目状态一直标记“Experimental NAS 未部署”。此前的宿主机升级只包含 API、Host Agent、Kiosk、Samba 与屏保，没有安装会改变网络规则且持有等同 root 权限 socket 的 Docker Engine，也没有创建独立容器代理身份。因此实时部署按设计默认为 `disabled`，前端提示准确。

同时发现原容器代理手册仍按 ADR 0008 之前的用户级预览架构编写：它把 `anas-dev` 加入代理组、重启用户级服务并修改 `~/.config/a-nas/anas-api.env`。统一身份升级后，实际产品服务是系统账号 `a-nas`，配置位于 `/etc/a-nas/anas-api.env`。继续照旧手册操作会授权错误的身份，仍无法启用产品能力。

## 修复与防复发

- [容器代理安装手册](../runbooks/install-container-agent.md)改为 ADR 0008 后的系统服务步骤：只有 `anas-container` 加入 `docker` 组，`a-nas` 仅加入 `anas-container` 组，`anas-dev` 不获得两者权限。
- 容器代理二进制和单元必须来自与 API 相同的不可变提交并核对 SHA-256；代理通过 `/run/a-nas-container/agent.sock` 暴露类型化接口。
- 系统服务安装器在代理 socket 已存在时写回 `ANAS_CONTAINERS_MODE=agent`，防止后续升级重建环境文件时意外关闭已经安装的容器能力；socket 缺失时继续 fail closed。
- 完成证据必须同时覆盖 Docker、容器代理、socket 权限、`a-nas` 可达性、API 环境键和登录后的两个桌面窗口。未带管理员会话的 API 请求不能作为正向验证。

## 关联

- 决策：[ADR 0009：通过专用容器代理使用 Docker Engine](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)
- 规格：[容器管理](../specs/container-management.md)、[应用中心](../specs/app-center.md)
- 手册：[安装 Docker 与容器代理](../runbooks/install-container-agent.md)
- 实现：`cmd/anas-api/main.go`、`deploy/systemd/system/anas-container-agent.service`、`scripts/install-v1.0.1-system-services.sh`
