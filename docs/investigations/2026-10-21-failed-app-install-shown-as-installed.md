# 安装失败的应用显示为“已安装”

状态：fixed，待实机验收
更新时间：2026-10-27

## 症状与影响

[#169](https://github.com/zhongwater123/A-NAS/issues/169) 来自 2026-10-20 的代码与架构审查（[#159](https://github.com/zhongwater123/A-NAS/issues/159)，基线 `9cda8a3`）。2026-10-21 在 `8f5b6b0` 上按源码复核，基线之后 `internal/appstore`、容器代理单元与应用中心界面都没有改动，问题仍然存在；它没有在实验 NAS 上发生过。

- `docker compose up` 在建出部分容器后出错时，安装直接失败：不核对挂载、运行身份与网络（不变量 15、17），也不回滚。宿主机端口被其他进程占用（规格预期的情形），或 Linkwarden 的 postgres 首次初始化太慢、没有及时通过 `service_healthy`，都会这样：postgres 与 meilisearch 在运行，linkwarden 停在 created。
- 应用状态只看有没有带应用标签的容器，任务结果不参与：上面的应用显示为“Linkwarden 已安装 · 2/3 个容器运行中”，失败原因被盖住；“重新查看安装计划”不可达（前端只在“可安装”时认安装失败），直接重装返回 `already_installed`，只能先卸载。
- 容器代理停止（部署、`apt upgrade` 或 Docker 重启）时，systemd 默认的 `KillMode=control-group` 把 SIGTERM 同时发给 `docker compose` 子进程，与单元注释“让正在进行的安装完成当前步骤”相反；任务只在内存中，重启后不对账，留下的容器同样显示为已安装。

## 最小复现

1. 容器代理运行 `8f5b6b0`。
2. 让 Linkwarden 的 `up` 在 postgres 与 meilisearch 启动后出错（例如事先占用 3428 端口，或 postgres 初始化超过健康检查等待），或在 Linkwarden 拉取镜像时 `systemctl restart anas-container-agent`。
3. 实际：应用中心显示“已安装”与部分容器运行，重装返回 `already_installed`。期望：应用回到“可安装”并给出失败原因，或显示为失败并给出原因，不显示“已安装”。

引擎测试复现了两种情形（见下文回归证据）；修复前，`up` 出错时执行器只调用了一次 `up`，没有 `down`，应用状态为 `installed`。

## 观察事实

- `engine.go` 中 `up` 出错直接 `return err`，核对与回滚都在它之后。
- `Apps` 在 `Total > 0` 时把状态定为 `installed`，`jobs` 只在内存中；重启后任务丢失，规格写明“重启后丢失但已安装状态由容器标签恢复”。
- `conflicts` 见到应用自己的容器即返回 `ErrAlreadyInstalled`。
- 前端 `installFailed` 要求 `state === "available"`；`InstallSheet` 在 `installed` 时显示“安装完成”。
- 单元没有设置 `KillMode`；`exec.CommandContext` 的子进程与代理同在服务的控制组中。
- 产品服务透传状态字符串，不校验取值；旧前端把未知状态显示为“可安装”。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 只在 `up` 出错时补一个 `compose down` 就够 | 对照代理在 `up` 中途被杀：没有任何记录说明安装没完成，重启后仍显示已安装 | 否：还需要持久的任务记录与启动时对账 |
| 重启后可以按记录的计划摘要重新核对，而不是回滚 | 重启常由部署引起，新版本的清单或策略可能渲染出不同的计划，核对的依据已不是用户确认的那份 | 否：一律回滚，由用户重新查看计划后安装 |
| 只设 `KillMode=mixed`、让安装继续就够 | 拉取镜像可能远超 60 秒的停止时限，到时 SIGKILL 一样打断，且代理退出后无人回滚 | 否：停止时由代理取消安装并回滚，`KillMode=mixed` 保证 Compose 不被 systemd 提前终止，超时剩下的由下次启动回滚 |
| 回滚失败时可以显示为“可安装”加失败任务 | 应用的容器仍在运行，“可安装”会让用户以为什么都没留下 | 否：新增状态 `failed`，并允许重装先删除它们 |

## 根因

安装把“结果”只放在内存中的任务里，而应用状态只由 Docker 中的容器推断：凡是让容器留下的失败路径（`up` 出错不回滚、回滚失败、代理被停止或崩溃）都会被当成安装成功。代理停止时不管理正在运行的 Compose，systemd 的默认终止方式又让它和代理同时被杀，没有人回滚。

## 修复与回归证据

- 修复（行为与边界见[应用中心规格](../specs/app-center.md#没有回滚干净的安装)）：
  - `up` 出错与核对失败一样取日志并 `compose down` 回滚；回滚使用不受取消与超时影响、最长 5 分钟的上下文。代码：[`engine.go`](../../internal/appstore/engine/engine.go)。
  - 每个应用最近一次任务在开始与结束时写入状态目录 `jobs/<app>.json`（0600），写不进时拒绝任务；代理启动时读回记录（`Recover`），仍在运行的安装在后台按项目 `compose down` 回滚并记为失败，仍在运行的卸载重新执行完成。停止时（`Stop`）取消正在进行的安装并回滚，卸载照常完成，之后不再接受任务。代码：[`jobs.go`](../../internal/appstore/engine/jobs.go)、[`main.go`](../../cmd/anas-container-agent/main.go)。
  - 有容器而最近一次是失败的安装时，状态为 `failed`（[`appstore.go`](../../internal/appstore/appstore.go)、[OpenAPI](../../api/openapi.yaml)）；重装不返回 `already_installed`，先删除留下的容器。界面把它当作安装失败，说明留下的容器并提供卸载（[`model.ts`](../../web/src/appcenter/model.ts)、[`AppDetail.tsx`](../../web/src/appcenter/AppDetail.tsx)、[`InstallSheet.tsx`](../../web/src/appcenter/InstallSheet.tsx)）。
  - 单元设 `KillMode=mixed`，`make ops-check` 检查这一行（[单元](../../deploy/systemd/system/anas-container-agent.service)）。
- 测试：[引擎测试](../../internal/appstore/engine/engine_test.go) 的 `TestInstallRollsBackWhenComposeFailsPartway`、`TestContainersOfAFailedInstallAreNotAnInstalledApp`、`TestStoppingTheAgentRollsBackARunningInstall`、`TestRecoverRollsBackAnInstallTheAgentDiedIn` 与 `TestRecoverFinishesAnUninstallTheAgentDiedIn`，以及[应用中心测试](../../web/src/appcenter/AppCenterPanel.test.tsx) 的 “shows the containers a failed install left as a failure, never as installed”。新测试在 `-race` 下各运行 3 次通过。

## 后续工作

- 部署后在实验 NAS 上确认：代理能写 `/var/lib/a-nas-container/jobs/`；安装一个应用时 `systemctl restart anas-container-agent`，应用中心显示“容器代理停止，安装被中断，已回滚”，Docker 中没有留下该应用的容器，重新安装成功。部署须同时更新容器代理（含单元）与产品服务的前端。
- 安装中途停止的代理不会自动续装，由用户重新查看计划后安装；`compose down` 不删除已拉取的镜像，重装不必重新下载。

## 关联

- 规格：[应用中心](../specs/app-center.md#安装中停止或重启容器代理)
- ADR：[0009 Docker Engine 与专用容器代理](../adr/0009-use-docker-engine-through-a-dedicated-container-agent.md)、[0013 地址池](../adr/0013-allocate-docker-networks-from-an-a-nas-address-pool.md)、[0015 挂载核对](../adr/0015-take-docker-and-caddy-from-their-upstream-repositories.md)、[0022 运行身份核对](../adr/0022-reach-shared-folders-only-as-the-app-identity.md)
- 运行手册：[安装 Docker 与容器代理](../runbooks/install-container-agent.md#4-安装不可变代理制品)
