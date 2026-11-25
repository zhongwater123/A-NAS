# Docker 的地址池保护必须跨产品回滚保留

状态：fixed
日期：2026-11-12

## 现象与原因

PR #472 最初用 `ConditionPathExists=/etc/docker/daemon.json` 判断是否配置地址池。空对象或只有日志设置的合法 Docker 配置也满足条件，Docker 仍会创建默认网段的网桥；安装后的清理同样只判断文件存在，无法拦截。

两个附加配置最初属于 `a-nas` 的普通文件。回滚到不含这些文件的历史产品包时，dpkg 删除它们；Docker 按发布约定不降级且仍启用，下次启动失去保护。原系统测试的“上一版”由当前脚本生成，文件布局也一样，因而没有覆盖这个边界。

## 修复边界

地址池约束仍由 [ADR 0013](../adr/0013-allocate-docker-networks-from-an-a-nas-address-pool.md) 定义。校验用 Python 标准库 `json` 与 `ipaddress`，不维护另一套 Docker 配置解析器；Docker 自己校验其余键。缺失、格式错误、无 IPv4 网桥或无可用 IPv4 地址池时拒绝启动。网段是否空闲仍由现场确认。

校验程序和服务、套接字的附加配置由独立的 `a-nas-docker-policy` 包持有，`a-nas-host` 要求不低于本 release 的版本。产品回滚既不覆盖也不删除该包。安装命令显式选择它，保留手动安装标记，避免回滚到没有该依赖的历史宿主机包后被自动清理。附加配置使用该包自己的文件名，避免与 #472 最初的产品包争用同一路径。

## 复现与验证

- 校验边界：`python3 -m unittest discover -s deploy/docker`，覆盖缺失、空对象、只有日志、缺少任一网络设置、空地址池、非法 JSON、地址与子网大小，以及手册的有效配置。
- `make system-test`：使用真实 Docker，拒绝上述不完整配置，确认未启动服务、套接字或创建 `docker0`；有效配置仍能启动并经容器代理报告版本。
- 回滚夹具使用相同程序但删除旧宿主机包对独立策略包的依赖。回滚后核对文件所有权并重新请求启动，验证保护跨包生命周期保留，空配置也不能绕过。

2026-11-12 本机验证：完整 `check-steps` 的各目标通过；额外取 PR 基线 `eba8e7c0` 的原始包定义与维护脚本，用同源二进制构建历史包（本 PR 不改 Go 程序）。在一次性 Debian 13 systemd 容器中，把当前 `a-nas`、`a-nas-host` 解包降级为历史包并重新加载单元，校验程序及两个附加配置仍归独立包所有。缺失配置、`{}` 均未启动服务、套接字或创建 `docker0`；写入手册配置后，真实 Docker 29.9.3 正常启动，地址池为 `10.96.64.0/19`，网桥为 `10.96.127.1/24`。这项额外检查验证旧包的文件切换与 Docker 启动边界，不代替完整系统测试中的 apt 配置与产品服务验收。

## 关联

- [校验程序](../../deploy/docker/check_address_pool.py)与[单元测试](../../deploy/docker/test_check_address_pool.py)
- [独立包](../../deploy/debian/a-nas-docker-policy/DEBIAN/control)、[构建](../../scripts/build-packages.sh)、[系统测试](../../scripts/system-test.sh)
- [部署手册](../runbooks/deploy-a-nas-packages.md)、[容器代理手册](../runbooks/install-container-agent.md)
