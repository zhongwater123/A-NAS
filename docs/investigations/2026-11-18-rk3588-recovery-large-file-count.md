# RK3588 recovery 的大文件字节计数回绕

状态：resolved
更新时间：2026-11-18

## 症状与影响

原厂 RAM recovery 收到 6,518,996,992 字节 rootfs 镜像后，`wc -c` 只报告 2,224,029,696 字节。若将它作为刷写长度依据，会误判传输截断，或错误缩短校验范围。

## 最小复现

1. 在本次 ATK-DLRK3588 原 recovery 中，通过 ADB 传输候选 rootfs 到专用 p8 暂存目录。
2. 分别使用原厂 `wc -c`、`ls -l` 与独立静态 BusyBox 1.37.0 的 `stat -c %s` 检查同一文件。
3. 原厂 `wc` 返回 2,224,029,696，后两者返回 6,518,996,992；差值恰为 2³²。

## 观察事实

- 原厂 `/usr/bin/wc` 指向 BusyBox 1.36.0，缺少 `stat` applet。拉回的原厂 BusyBox 为 **AArch64 ELF64**，不能由计数回绕推断 recovery 是 32 位 ABI。
- 独立 `busybox-static:arm64` 版本 `1:1.37.0-6+b9` 为静态 AArch64 ELF，文件 1,975,064 字节，SHA-256 为 `7d93682be37cf6ed46699f6fff546b80bf133cf8ec342c2d04bc23387f78bc34`。
- 独立工具的 `stat`、完整 SHA-256 与主机候选一致；写入后的准确长度回读也一致，因此此样本不是传输截断。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| ADB 丢失了超过 4 GiB 的数据 | 对比主机与板端长度、完整 SHA-256 | 排除：独立工具的长度和摘要一致 |
| recovery 使用 32 位 ABI | 检查原厂 BusyBox ELF | 排除：ELF64 AArch64 |
| 原厂 `wc` 的字节计数发生回绕 | 比较原厂 `wc` 与独立 `stat` | 确认：相差 2³²；未进一步定位该厂商二进制的内部实现 |

## 根因

已定位到原厂 BusyBox `wc` 的大文件字节计数边界。具体源码、构建选项或窄类型位置尚未证明；操作层修复不依赖猜测内部原因。

## 修复与回归证据

- 仅把已固定摘要的静态工具放入 `/userdata/anas-rk3588-activation-20261118/busybox64`，不替换原厂系统工具。
- 激活脚本用该工具执行关键 `stat`、`dd`、`sha256sum`、`gzip`，且所有输入在写入前复验摘要。
- 候选根文件系统 6,518,996,992 字节的完整回读 SHA-256 为 `25f98190ea5f77b174dba522259b34f5af9abe43f8d2af1a5891cdee6440f26f`，激活状态为 `COMMITTED apply`。原始脚本和日志保存在本机 `.cache/rk3588-board-20261117/activate-board-system.sh`、`activation-board-evidence/`。

## 后续工作

在其他厂商恢复环境执行大文件操作时，先验证工具的长度边界；不能仅按用户空间位数或小文件测试推断大文件正确性。

## 关联

- 运行手册：[在原分区表上试启动](../runbooks/build-rk3588-kernel.md#在原分区表上试启动)
- 研究：[RK3588 迁移可行性](../research/rk3588-deployment-feasibility-2026-11.md)
- 规格、ADR：不涉及
