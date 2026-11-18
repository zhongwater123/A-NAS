# Linux ARM64 候选交付

状态：implemented
更新时间：2026-11-18

## 目标

在板卡到货前完成可在开发机验证的 ARM64 编译、测试与交付准备。目标用户空间为 Debian 13 arm64，板卡、启动与驱动验收另行记录。

## 行为

- `ANAS_TARGET_ARCH=arm64` 选择 ARM64 制品清单、Python 哈希锁、包架构与内容身份；默认使用本机 Debian 架构，支持 amd64 和 arm64。
- amd64 的现有清单与 AI/搜索运行环境 ID 保持兼容。相同依赖锁在两种架构下不能产生相同运行环境 ID。
- 缺少 ARM 输入、未知架构、错误的 ELF 架构或哈希不匹配时停止。不得回退到 amd64，也不得通过修改包标签混用二进制。
- 核心二进制保持 CGO SQLite；候选包按既有服务与离线内容边界组织，不跳过 AI 交付要求。
- 模型清单的 `memoryBytes` 保留默认架构预算；可选 `memoryBytesByArchitecture` 按运行机器选择覆盖值，`aarch64/arm64` 归一为 `arm64`，`x86_64/amd64` 归一为 `amd64`。覆盖表必须为对象，选中的预算必须是非负整数且不接受布尔值；无对应项时使用原预算，不改变权重身份或提示词。
- 候选产物带源码基准、未提交改动摘要、平台、文件 SHA-256、已执行检查及未验证项。它们不冒用正式发布或真机验收状态。

## 验证范围

完整本机门禁、全包 ARM64 用户态仿真、安装包架构与依赖检查分别记录。真实宿主机、真实模型、媒体工具、systemd 隔离与板级测试不得以跳过项充当通过；QEMU 耗时不得用于承诺 RK3588 性能。

## 关联

- [RK3588 可行性与验证记录](../research/rk3588-deployment-feasibility-2026-11.md)
- [候选准备与板端验收](../runbooks/prepare-rk3588-candidate.md)
- [离线 AI 交付边界](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- [软件包与系统更新](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)
- [目标选择](../../scripts/lib/target.sh)、[二进制构建](../../scripts/build-target-binaries.sh)、[包构建](../../scripts/build-packages.sh)、[架构契约测试](../../scripts/target_test.go)
- [模型预算解析](../../ai/anas_ai/providers.py)、[架构预算回归](../../ai/tests/test_model_memory.py)
