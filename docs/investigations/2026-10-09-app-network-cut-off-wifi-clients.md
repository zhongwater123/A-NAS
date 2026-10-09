# 安装 Immich 后内网 WiFi 客户端全部无法访问 NAS

状态：fixed locally；Experimental NAS 配置地址池与部署待完成
更新时间：2026-10-09

## 症状与影响

2026-10-09 中午，内网用户经新开放的局域网 Web 入口使用 NAS 约 20 分钟后，设备所有者的多台电脑同时无法访问 `172.18.45.48`：网页、SSH 与 ping 全部超时。本机屏幕的桌面仍然正常。Immich 由他人经应用中心安装。卸载 Immich 之前，所有 172.19.x 的 WiFi 客户端都无法访问 NAS，也无法远程执行修复；重启 NAS 也不能恢复，因为应用会随 Docker 重新创建网络。

## 最小复现

1. 在 [`engine_test.go`](../../internal/appstore/engine/engine_test.go) 中，假 Docker 不报告任何地址池（与默认配置相同），规划 `audiobookshelf`。它没有 `network_mode: bridge`，Compose 会为它创建 `default` 网络。
2. 修复前规划直接成功，安装后 Docker 从内置池分配网络，没有任何检查。修复后规划返回 `address_pool_missing`。
3. 假 Docker 把项目网络报告为 `172.19.0.0/16` 时，修复后的安装执行 `up` 后立即 `down` 回滚，任务失败并写明该网段。

## 观察事实

- 11:30 前后从公司 WiFi 上的开发机（`172.19.172.225`）访问局域网入口正常。11:55 起，该机访问 NAS 的 ping、22 与 80 端口全部超时，而 NAS 所在网段的网关 `172.18.45.254` 与公司 DNS（`172.18.254.101`、`172.16.200.200`）均可 ping 通；`tracert` 在第一跳之后无响应。
- 设备所有者在本机屏幕确认桌面正常，"Docker"应用中多出 Immich。
- 内置 Immich 清单定义了网络 `immich`（`driver: bridge`），没有 `ipam`；目录中另有 5 个应用没有 `network_mode: bridge`，同样会新建网络。
- NAS 上 `/etc/docker/daemon.json` 不存在，Docker 使用内置地址池。本地 Docker 29 在未配置时 `docker info` 的 `DefaultAddressPools` 为 `null`。
- 在 NAS 屏幕上卸载 Immich（`docker compose down` 删除容器与项目网络）后，开发机立即恢复访问。此时 NAS 路由表只剩 `docker0` 的 `172.17.0.0/16` 与局域网 `172.18.45.0/24`。
- 故障期间无法登录 NAS，Immich 网络的实际子网没有被直接观察到。按 Docker 的分配规则推断：`172.17.0.0/16` 已被 `docker0` 占用，`172.18.0.0/16` 与直连的 `172.18.45.0/24` 重叠被跳过，下一个是 `172.19.0.0/16`。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| NAS 卡死或掉网 | 本机屏幕与卸载后的连通性 | rejected：屏幕正常，未重启即恢复 |
| 局域网 Web 入口（Caddy）导致 | 故障时 ping 与 SSH 也失败；卸载 Immich 即恢复 | rejected |
| DHCP 换址 | 恢复后的地址与路由 | rejected：仍为 `172.18.45.48` |
| 应用网络占用 172.19.0.0/16，宿主机把 WiFi 客户端的回包路由进网桥 | 卸载应用（删除网络）后立即恢复；网关可达而 NAS 不可达；Docker 分配规则 | confirmed（子网为推断） |

## 根因

Docker 只把宿主机直连的网段当作已占用，看不到经网关才能到达的公司网段。Immich 的网络从内置池拿到 `172.19.0.0/16`，NAS 因而认为整个 172.19.x 都在本机网桥上，把发往 WiFi 客户端的所有回包送进了 Immich 网桥。应用中心既没有要求 Docker 使用专用地址池，也没有在安装前后检查应用网络。

## 修复与回归证据

- 决定：[ADR 0013](../adr/0013-allocate-docker-networks-from-an-a-nas-address-pool.md)。
- 渲染：[`Render`](../../internal/appstore/render.go) 在计划中列出将新建的网络，拒绝清单自带 `ipam` 的网络。
- 规划：[`Store.Plan`](../../internal/appstore/engine/engine.go) 在应用有网络而 Docker 未配置地址池时返回 `address_pool_missing`。
- 安装：`Store.Install` 在 `compose up` 后核对网络都在地址池内，否则执行 `compose down` 回滚。
- 界面：安装计划显示 Docker 网络及其地址池，地址池缺失时给出明确提示。
- 测试：`TestAppNetworksNeedAnAddressPool`、`TestInstallRollsBackANetworkOutsideThePool`、`TestInstallKeepsANetworkInsideThePool`、`TestRenderListsNetworksAndRefusesOwnAddressRanges`，以及前端"显示 Docker 网络并在没有地址池时拒绝"测试。

## 后续工作

- 按[安装手册](../runbooks/install-container-agent.md#2-配置-docker-网络地址池)在 Experimental NAS 配置 `10.96.64.0/18`（应用网络 `10.96.64.0/19`，`docker0` 为 `10.96.127.0/24`），更新容器代理后再从应用中心重新安装 Immich；它的数据已保留。
- `docker0` 目前仍是 `172.17.0.0/16`。公司若有 172.17.x 的设备，NAS 目前同样访问不到它们，配置 `bip` 后一并解决。

## 关联

- 规格：[应用中心](../specs/app-center.md)
- ADR：[0013](../adr/0013-allocate-docker-networks-from-an-a-nas-address-pool.md)、[0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)
- 运行手册：[安装 Docker 与容器代理](../runbooks/install-container-agent.md)
