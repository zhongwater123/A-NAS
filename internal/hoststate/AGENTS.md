# 宿主机状态（internal/hoststate/）

`linux/` 与 `agent/rootagent/` 在 Host Agent 中以 root 运行，改动前读 [internal/hostops/AGENTS.md](../hostops/AGENTS.md)。`fake/` 是开发与测试用的模拟，与 `linux/` 遵守同一契约，差异由契约测试暴露（[不变量](../../docs/architecture/OVERVIEW.md#不变量) 8）。
