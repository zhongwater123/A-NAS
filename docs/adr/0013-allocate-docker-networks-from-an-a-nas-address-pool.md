# 0013：Docker 网络只从 A-NAS 指定的地址池分配

状态：accepted；行为见[应用中心规格](../specs/app-center.md)，配置步骤见[安装 Docker 与容器代理](../runbooks/install-container-agent.md#2-配置-docker-网络地址池)。

Docker 为应用新建网络时，默认从 172.17–172.31/16 与 192.168.0.0/16 中分配，只避开宿主机直连的网段，看不到经网关才能到达的公司网段。2026-10-09 应用中心安装 Immich 后，它的网络按这一规则推断得到了 172.19.0.0/16。NAS 随即把所有 172.19.x 的 WiFi 客户端的回包送进应用网桥，内网 WiFi 用户全部无法访问 NAS，卸载应用后才恢复（[调查](../investigations/2026-10-09-app-network-cut-off-wifi-clients.md)）。我们决定：Docker 的全部网络地址来自现场确认不使用的地址池；地址池未配置时，应用中心拒绝安装会新建网络的应用，并在安装后核对网络地址。

## 决定

1. **地址池**：安装容器代理时在 `/etc/docker/daemon.json` 中配置 `default-address-pools` 与 `bip`，网段由现场（IT）确认在内网中不被使用。这些地址只在 NAS 内部使用，容器出站经 NAT 以 NAS 的地址出现，不需要内网路由。Experimental NAS 使用 `10.96.64.0/18`。
2. **规划时拒绝**：安装计划列出 Compose 将创建的网络（含隐式 `default`）。有网络而 Docker 未配置地址池时返回 `address_pool_missing`；清单自带 `ipam` 子网或驱动的网络在渲染时即被拒绝。
3. **安装后核对**：`compose up` 之后，项目网络的每个子网都必须落在地址池内；有任一不在池内或无法检查时，立即 `compose down` 回滚并让安装任务失败。
4. **单一配置来源**：A-NAS 不维护公司网段列表，地址池是唯一的现场配置。

## 后果

- 未配置地址池的设备上，6 个会新建网络的内置应用（audiobookshelf、excalidraw、filedrop、immich、linkwarden、psitransfer）无法安装；使用 Docker 默认网桥的应用不受影响。
- 修改地址池需要重启 Docker，全部容器会短暂中断；已有的应用网络不会自动迁移，需要卸载重装或重建网络。
- 地址池选错（与内网重叠）时，核对无法发现，仍需 IT 确认网段。
- 安装后核对与回滚之间存在数秒窗口；规划时的检查与地址池配置共同避免冲突网络被创建。

## 未选择

- 渲染时为每个应用网络写入显式子网：需要 A-NAS 自己维护分配记录，重复 Docker 已有的能力。
- 维护"受保护网段"列表并在安装后比对：需要额外配置公司网段，Docker 仍会先分到冲突网段。
- 策略路由（connmark 加 `ip rule`），让从网卡进入的连接总从网卡返回：能容忍冲突，但要自行维护与 Docker iptables 规则共存的网络配置，复杂且风险高。
- 使用 NAS 所在局域网段中的空闲地址：DHCP 随时可能分配这些地址，而且地址不够分。

## 关联

- [应用网络切断 WiFi 客户端调查](../investigations/2026-10-09-app-network-cut-off-wifi-clients.md)
- [ADR 0009：Docker Engine 与容器代理](0009-use-docker-engine-through-a-dedicated-container-agent.md)
- [ADR 0010：应用清单与安装策略](0010-vendor-a-reviewed-app-catalog-with-an-install-policy.md)
- [应用中心规格](../specs/app-center.md)
- [安装 Docker 与容器代理](../runbooks/install-container-agent.md)
