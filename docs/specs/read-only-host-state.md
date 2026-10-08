# 只读宿主机状态

状态：implemented
更新时间：2026-10-06

这些读取接口现在要求已登录会话，未登录返回 401；下文的无认证描述是 v1.0.0 时的行为。

## 目标

产品 API 在不访问真实磁盘的本地开发环境中，通过 Fake Adapter 返回确定性的宿主机与磁盘状态，并以 OpenAPI 固化客户端可观察契约。

## 非目标

- 不读取实验 NAS、udev、SMART 或真实块设备。
- 不实现 Host Agent IPC、状态写入、身份认证、TLS 或局域网暴露。
- 不提供运行时修改 Fake 状态的接口。

## 场景

### API 进程存活

- Given：产品 API 已启动，宿主机状态 Reader 可用或不可用。
- When：客户端请求 `GET /healthz`。
- Then：返回 `200` 和 `{"status":"ok"}`，不读取宿主机状态。

### 读取系统状态

- Given：健康的 Debian 13 amd64 Fake 状态。
- When：客户端请求 `GET /api/v1/system`。
- Then：返回 `200`，包含稳定主机 ID、系统版本、架构、运行时长、健康状态、产品版本和观测时间。

### 读取磁盘状态

- Given：健康的 Fake 状态包含 125 GB 系统盘和 512 GB 未分配磁盘。
- When：客户端请求 `GET /api/v1/disks`。
- Then：返回 `200`，磁盘按稳定资源 ID 排序，公开身份不包含设备路径或挂载点。

### 状态暂不可用

- Given：宿主机状态 Reader 返回内部错误。
- When：客户端请求系统或磁盘状态。
- Then：返回 `503` 和稳定的 `state_unavailable` 错误，不泄露内部错误文本。

### 路由错误

- Given：产品 API 已启动。
- When：客户端请求未知 API 路径或对已知路径使用不支持的方法。
- Then：分别返回 JSON 格式的 `404` 或 `405`；`405` 同时返回 `Allow: GET`。

## 边界与失败

- 默认只监听 `127.0.0.1:8080`；可通过 `ANAS_HTTP_ADDR` 覆盖。
- `healthz` 是存活检查，不代表宿主机状态 Reader 可用。
- 健康状态只使用 `healthy`、`warning`、`critical` 和 `unknown`。
- 磁盘角色只使用 `system`、`data` 和 `unassigned`。

## 验收证据

- `make check`
- HTTP 行为测试与 OpenAPI 响应校验通过。
- 构建后的 `anas-api` 在收到 `SIGTERM` 后正常退出。

## 关联

- 架构：[架构总览](../architecture/OVERVIEW.md)
- ADR：[客户端产品接口使用 REST 和 OpenAPI](../adr/0002-use-rest-openapi-for-product-clients.md)
- 契约：[OpenAPI](../../api/openapi.yaml)
- 代码：[宿主机状态](../../internal/hoststate/state.go)、[Fake Adapter](../../internal/hoststate/fake/reader.go)、[HTTP 模块](../../internal/httpapi/server.go)
- 测试：[Fake Adapter 测试](../../internal/hoststate/fake/reader_test.go)、[HTTP 行为测试](../../internal/httpapi/server_test.go)、[OpenAPI 契约测试](../../internal/httpapi/openapi_test.go)
