# 启用相册物体检测

状态：verified（2026-10-13 在 Experimental NAS 首次部署 `bb1ec71bff65`）
更新时间：2026-10-13

## 目的

第一次在 Experimental NAS 上安装带物体检测器的 release（切片 18，[实施方案](../architecture/photo-objects.md)）。之后的升级按[实机配置手册](provision-v1.0.1-experimental-storage.md#安装系统服务)进行即可。成功后：

- AI Worker 同时产生向量与检测结果；检测器只在后台有检测任务时加载。
- 搜“手机”这类物品时，结果页说“检测到手机的照片 N 张”或“没有检测到手机”；查看器可以“找相似”。
- 后台整理用满可用逻辑核数减 2（i3-12100 为 6），由安装器写入的 `cpu-reserve.conf` 兜底；前台请求到来时正在进行的检测立即中止。

## 前提与风险

- **权限**：管理员 root 会话。暂存使用日常账号 `anas-dev`。
- **目标**：主机名 `a-nas-dev`，使用前按 [SSH 运行手册](bootstrap-experimental-nas-ssh.md)核对指纹。
- **前提**：
  - 设备已按[启用相册本地 AI](enable-photo-ai.md)运行本地 AI。
  - 用户已同意部署本版本。检测器权重只用于内部测试，对外发行前另行确认许可（[模型清单](../../deploy/models/dfine-m-obj365.json)）。
  - 开发机已用官方导出的检测器重新生成 [`v1.calibration.json`](../../internal/photos/objects/v1.calibration.json)：其中的 `model` 必须是 `dfine-m-obj365@cd832825f883+ort-1.31.0+640+s0.05k100`。否则检测照常进行，但融合与“检测到”分段不生效。
  - 开发机已运行 `make check`，并按[本地环境](../development/LOCAL_ENVIRONMENT.md#系统测试)跑过替身与真实模型（`ANAS_AI_MODEL`、`ANAS_AI_DETECTOR`）两次系统测试。
- **资源**：
  - 系统盘增加约 80 MB 的检测器，另加一份新的运行环境（2026-10-13 构建的 `3eca142df9a765c2` 压缩后 189 MB，解开约 542 MB）：锁文件加入 ONNX Runtime 后运行环境 ID 改变，旧的保留供回滚。
  - 检测器加载时 Worker 多占约 400 MiB；单元 `MemoryHigh=1792M`、`MemoryMax=2G`。
  - 升级后侧栏 AI 卡片的“已整理”从全部回到“0 / N”，因为每张照片还要做一次检测（向量不重做），这是如实的。
- **停止条件**（出现任一项立即停止）：
  - 安装器以退出码 2（检测器清单无效或缺少文件）、3 或 4（暂存的检测器或运行环境与清单不符）结束。
  - `journalctl -u anas-ai -b` 出现 `object detector unavailable` 或 `model unavailable`。
  - Worker 因内存超限被杀（日志出现 `oom-kill` 或 `Failed with result 'oom-kill'`）。

## 步骤

1. 在开发机构建运行环境并暂存。
   - 命令（WSL）：`scripts/build-ai-runtime.sh`。
   - 命令（Windows）：`scripts/deploy-dev.ps1 -NasHost a-nas-dev -StageOnly -WslDistribution Ubuntu -WslUser <WSL 用户>`；检测器默认取自 `D:\A-NAS-models\dfine-obj365\official`，其他位置用 `-DetectorDirectory` 指定。
   - 预期：输出“The NAS already holds embeddinggemma-2-740m.litertlm.”；检测器 `dfine_m_obj365.onnx`（78,517,092 字节）与新的运行环境压缩包各上传一次。
   - 完成标准：暂存 release 中有 `ai/detector.json`，`ai/detector.onnx` 是指向 `apps/a-nas/models/cd832825f883…/dfine_m_obj365.onnx` 的链接；暂存脚本已核对哈希。
2. 以 root 按[实机配置手册的安装步骤](provision-v1.0.1-experimental-storage.md#安装系统服务)核对二进制哈希并运行安装器。
   - 预期：安装器把检测器复制到 `/opt/a-nas/models/cd832825f8831b5a…/`，把新运行环境复制到 `/opt/a-nas/ai-runtimes/<新 ID>/`，复制后核对哈希；写入 `/etc/systemd/system/anas-ai.service.d/cpu-reserve.conf`。
   - 完成标准：安装器正常结束；`systemctl is-enabled anas-ai.socket` 输出 `enabled`。

## 验证

- **链接与资源限制**：
  - `readlink /opt/a-nas/current/ai/detector.onnx` 指向 `/opt/a-nas/models/cd832825f8831b5a…/dfine_m_obj365.onnx`。
  - `cat /etc/systemd/system/anas-ai.service.d/cpu-reserve.conf` 写明本机核数，8 个逻辑核时为 `CPUQuota=600%`。
  - `systemctl show anas-ai.service -p CPUQuotaPerSecUSec -p MemoryHigh -p MemoryMax` 输出 `6s`、`1879048192`、`2147483648`。
- **检测器**：在相册中搜索一次，`journalctl -u anas-ai -b --no-pager` 含 `detecting with dfine-m-obj365@cd832825f883+ort-1.31.0+640+s0.05k100 (6 threads)`，没有 `object detector unavailable`。
- **整理**：相册空闲 5 分钟后，AI 卡片从“0 / N”开始计数并最终显示“已整理全部 N 张”。
- **搜索与找相似**：
  - 搜一个测试照片中有的物品（如“手机”“杯子”），结果页出现“检测到…的照片 N 张”，分隔线之后是没有检测到它的照片；搜一个没有的物品时出现“没有检测到…”。
  - 搜一句话（如“海边的日落”），结果照常分为“最接近”与“相关度较低”两段。
  - 在查看器中点“找相似”，出现相似的照片；返回回到原来的位置。
- **突发时释放**：整理进行中打开相册浏览或搜索，`journalctl -u anas-ai -b` 出现 `detection cancelled`，相册操作不明显变慢。
- **不自己卡住自己**：`systemctl show -p Slice --value anas-ai.service` 输出 `anas-ai.slice`；`journalctl -u anas-photos -b` 含 `AI work yields to the pressure of`，后面列出 `system.slice` 与 `user.slice`。整理期间没人使用设备时，侧栏 AI 卡片不应长时间显示暂停，`journalctl -u anas-ai -b` 中 `detection cancelled` 应只在有人操作时出现。同时记录 `cat /sys/fs/cgroup/system.slice/cpu.pressure /sys/fs/cgroup/anas.slice/anas-ai.slice/cpu.pressure`：若 AI 因 `cpu_pressure` 反复暂停而设备上没有别的负载，停止部署，按[资源策略](../architecture/photo-objects.md#资源策略)调整门控上限。
- **线程与内存不随取消上涨**：整理期间每隔几分钟记录 `grep -E 'Threads|RssAnon' /proc/$(systemctl show -p MainPID --value anas-ai.service)/status`，线程数与内存应保持平稳。
- **资源记录**：整理完成后记录 `systemctl show anas-ai.service -p MemoryPeak -p CPUUsageNSec`、总耗时与张数，以及整理期间与空闲时打开相册、搜索的响应时间，补充到[实施方案](../architecture/photo-objects.md)。`MemoryPeak` 接近 2 GiB 时先调整单元再继续。
- **空闲退出**：最后一次请求约 10 分钟后 `systemctl is-active anas-ai.service` 输出 `inactive`，`anas-ai.socket` 仍为 `active`。

## 回滚或恢复

- **回到不含检测器的 release**：按[实机配置手册的回滚](provision-v1.0.1-experimental-storage.md#回滚)恢复，其中包括恢复上一 release 的 `anas-ai.service` 并删除 `cpu-reserve.conf`。旧 Worker 不认识 `--detector` 参数，只恢复其他单元会让每次连接都启动失败。
- **只停用 AI**：见[启用相册本地 AI](enable-photo-ai.md#回滚或恢复)。
- **检测器与运行环境**：回滚不删除 `/opt/a-nas/models` 与 `/opt/a-nas/ai-runtimes` 中的内容，重新升级时直接复用；删除前的核对见[启用相册本地 AI](enable-photo-ai.md#回滚或恢复)。
- **相册 Catalog**：新版本启动时把 Catalog 从版本 6 迁移到 7（新增 `detections` 表，并为已有照片排入 `objects/v1` 任务）。旧版本遇到更新的 Catalog 会拒绝启动（`photo catalog version 7 is newer than this build supports (6)`），所以回到不含检测器的 release 前，必须先把 Catalog 退回版本 6：
  - 停止 `anas-photos` 与 `anas-ai` 后，以 `a-nas-photos` 身份打开 `/srv/a-nas/data/photos/catalog.db`。
  - 在一个事务中执行 `DELETE FROM jobs WHERE derivation = 'objects/v1'`、`DROP TABLE detections` 和 `PRAGMA user_version = 6`。
  - 必须以 `a-nas-photos` 身份执行，否则 SQLite 可能留下 root 所有的 `-wal`/`-shm` 文件，相册服务之后无法打开。
  - 这一步只删除检测结果，照片、相册、标签、AI 纠错与向量都不受影响。
  - 首次部署时这一步写在 `/root/rollback-bb1ec71bff65.sh` 中，先在模拟的 Catalog 上验证过。
  - 部署前另有一份完整备份 `catalog-before-<release>.db`，只在 Catalog 损坏时使用：恢复它会丢失部署之后的上传与修改。

## 首次实机部署证据

2026-10-13 17:51，主干 `bb1ec71bff65`（#83 物体检测、#85 传输中心测速）由用户以 root 执行部署脚本，上一版本为 `e1c1921d5fdb`：

- **升级前**：暂存的二进制哈希核对通过。Catalog 备份为 `catalog-before-bb1ec71bff65.db`，版本 6，`quick_check` 为 ok，703 张照片。
- **安装器**：
  - 把检测器复制到 `/opt/a-nas/models/cd832825…/`，把运行环境 `3eca142df9a765c2` 复制到 `/opt/a-nas/ai-runtimes/`；
  - 写入 `CPUQuota=600%`（8 个逻辑核）。
  - `systemctl show anas-ai.service` 为 `Slice=anas-ai.slice`、`CPUQuotaPerSecUSec=6s`、`MemoryHigh=1879048192`、`MemoryMax=2147483648`。
- **服务**：相册服务日志写明门控看 `/sys/fs/cgroup/system.slice` 与 `/sys/fs/cgroup/user.slice`。Catalog 迁移到版本 7，排入 697 个检测任务。九个服务均为 active。
- **整理**：
  - 17:57:25 Worker 以 6 个线程开始检测（安装后空闲 5 分钟）。
  - 18:35:56 全部 697 个照片对象检测完毕，约 38.5 分钟，平均约 3.3 秒/张墙钟时间，含压力暂停后的等待。没有失败的任务。
  - 期间检测被取消 17 次，Worker 没有重启，18:45:56 空闲 10 分钟后退出。
  - 这次运行 systemd 记录 CPU 时间 38 分 42 秒（2,322 秒），内存峰值 1 GiB，没有触及 `MemoryHigh`。
  - Worker 平均只用了约 1 个核，远低于 6 核的额度；每次取消后至少空等 1 分钟（[调查](../investigations/2026-10-13-idle-detection-used-one-core.md)）。
- **空闲时（19:06）**：`system.slice` 的 CPU 压力为 0，`user.slice` 为 0.17%。新启动的 Worker（只加载了向量模型）内存峰值 659 MB，32 个线程，没有换出。

## 关联

- 规格：[相册](../specs/photo-library.md)、[相册界面](../specs/photo-gallery-ui.md#物体检测与找相似2026-10-13)
- ADR：[0016 AI 随系统内置安装](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- 架构：[物体检测与混合检索](../architecture/photo-objects.md)、[相册本地 AI（M2）](../architecture/photo-ai.md#模型与运行环境的交付)
- 代码：[`anas-ai.service`](../../deploy/systemd/system/anas-ai.service)、[安装器](../../scripts/install-v1.0.1-system-services.sh)、[暂存](../../scripts/deploy-dev.ps1)、[检测器清单](../../deploy/models/dfine-m-obj365.json)
- 测试：[系统测试](../../scripts/system-test.sh)
- 调查：[闲时检测只用了约 1 个核](../investigations/2026-10-13-idle-detection-used-one-core.md)
