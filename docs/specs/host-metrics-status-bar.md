# 桌面状态栏与实时资源指标

状态：implemented
更新时间：2026-10-07

## 目标

设备所有者在桌面右上角一眼看到当前时间、CPU 与内存占用（速度仪表盘）、上下行网速，以及设备连接和数据来源，而不必打开[设置](settings.md)。

## 非目标

- 不提供历史曲线、按进程或按网卡的明细，也不做告警阈值配置。
- 不统计磁盘 I/O、温度或 GPU。
- 不把指标写入任何持久存储。

## 场景

### 查看实时占用

- Given：产品服务以 `agent` 模式连接 Host Agent。
- When：桌面处于前台。
- Then：状态栏每 2 秒通过 `GET /api/v1/metrics` 刷新；CPU 与内存以 210° 仪表盘显示百分比，指针与弧线平滑过渡，60% 起读数转黄、85% 起转红；内存标注已用/总量 GiB，CPU 标注逻辑核数。

### 查看网速

- Given：物理网卡有收发流量。
- When：状态栏刷新。
- Then：下行与上行分别显示十进制单位速率（B/s、KB/s、MB/s、GB/s），回环、Docker 网桥和 veth 等虚拟链路不计入。

### 时间与连接

- Given：桌面已打开。
- When：跨越整分钟或连接状态变化。
- Then：时间在每个整分钟更新并显示“10月7日 周三”格式日期；状态点区分在线、正在连接和中断，徽章显示“模拟数据”或“实时主机”，状态或指标读取失败时改为“连接中断”；会话失效（`401`）不算读取失败，页面回到登录页，见[会话规则](basic-storage-and-sharing.md#角色与可见性)。

### 指标读取失败

- Given：至少成功读取过一次指标。
- When：后续读取失败。
- Then：保留最后读数并降低不透明度，同时显示连接中断；从未成功时仪表显示“—”且无障碍值为“暂无数据”。

## 边界与失败

- 采样位于 Host Agent 的 Linux Adapter：CPU 取 `/proc/stat` 汇总行前八项，`idle + iowait` 视为空闲；内存已用为 `MemTotal - MemAvailable`；网速取 `/proc/net/dev` 中在 sysfs 有 `device` 的接口，全部为虚拟接口时退回到所有非回环接口。
- 速率按相邻两次采样计算。没有 15 秒内的基线时先等待 500 ms 采一个短窗口；距上次采样不足 500 ms 的请求直接返回缓存结果，多个浏览器同时轮询不会产生失真速率。
- 计数器回退（接口重建、重置）按 0 计；只在两次采样中都存在的接口才计入，新出现的接口不会形成尖峰。
- Fake Adapter 返回固定指标（CPU 23.5%、4 核、内存 3.2/8.0 GiB、下行 2.5 MB/s、上行 328 KB/s），便于截图和测试。
- 浏览器只能在有效产品会话中读取同源 `/api/v1/metrics`；产品服务再通过受限 Unix Socket 读取 Host Agent 的 `/v1/metrics`，浏览器不能直接访问 Host Agent。
- 页面隐藏时暂停轮询，重新可见时立即刷新。响应带 `Cache-Control: no-store`。
- 窄屏（≤ 900 px）隐藏仪表盘附注；手机宽度（≤ 620 px）状态栏横跨顶部并隐藏数据来源徽章，桌面图标区下移避让。

## 验收证据

- Linux 采样：[指标测试](../../internal/hoststate/linux/metrics_test.go) 覆盖基线窗口、复用上次采样、快速轮询缓存、计数器重置、虚拟接口排除与回退、格式错误。
- IPC 与 API：[Host Agent 测试](../../internal/hoststate/agent/agent_test.go)、[HTTP 测试](../../internal/httpapi/server_test.go)、[OpenAPI 测试](../../internal/httpapi/openapi_test.go)。
- 前端：[桌面测试](../../web/src/App.test.tsx) 与 [速率格式测试](../../web/src/StatusBar.test.ts)。
- 2026-10-07 在 WSL2 以 `ANAS_HOSTSTATE_MODE=agent` 运行 Host Agent 与产品服务：14 核与 7.5 GiB 内存被正确识别，CPU 压测时仪表升至约 66%，限速下载期间下行速率与 curl 实际速率一致；并在 1024、820 和 375 px 宽度下检查布局。

## 关联

- 代码：[`linux/metrics.go`](../../internal/hoststate/linux/metrics.go)、[`hoststate.Metrics`](../../internal/hoststate/state.go)、[`StatusBar`](../../web/src/StatusBar.tsx)、[`useHostMetrics`](../../web/src/useHostMetrics.ts)
- 契约：[OpenAPI](../../api/openapi.yaml)
- ADR：[Host Agent IPC](../adr/0004-use-http-json-over-unix-socket-for-host-state.md)
- 规格：[Web 桌面宿主机状态](web-desktop-host-state.md)
