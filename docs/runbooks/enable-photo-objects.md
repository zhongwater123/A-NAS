# 启用相册物体检测

状态：draft（尚未在 Experimental NAS 执行）
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
- **资源记录**：整理完成后记录 `systemctl show anas-ai.service -p MemoryPeak -p CPUUsageNSec`、总耗时与张数，以及整理期间与空闲时打开相册、搜索的响应时间，补充到[实施方案](../architecture/photo-objects.md)。`MemoryPeak` 接近 2 GiB 时先调整单元再继续。
- **空闲退出**：最后一次请求约 10 分钟后 `systemctl is-active anas-ai.service` 输出 `inactive`，`anas-ai.socket` 仍为 `active`。

## 回滚或恢复

- **回到不含检测器的 release**：按[实机配置手册的回滚](provision-v1.0.1-experimental-storage.md#回滚)恢复，其中包括恢复上一 release 的 `anas-ai.service` 并删除 `cpu-reserve.conf`。旧 Worker 不认识 `--detector` 参数，只恢复其他单元会让每次连接都启动失败。
- **只停用 AI**：见[启用相册本地 AI](enable-photo-ai.md#回滚或恢复)。
- **检测器与运行环境**：回滚不删除 `/opt/a-nas/models` 与 `/opt/a-nas/ai-runtimes` 中的内容，重新升级时直接复用；删除前的核对见[启用相册本地 AI](enable-photo-ai.md#回滚或恢复)。
- **派生数据**：检测结果保存在相册 Catalog 的 `detections` 表中（migration 7）。旧版本不读这张表，回滚后照片、用户元数据与向量不受影响。

## 关联

- 规格：[相册](../specs/photo-library.md)、[相册界面](../specs/photo-gallery-ui.md#物体检测与找相似2026-10-13)
- ADR：[0016 AI 随系统内置安装](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- 架构：[物体检测与混合检索](../architecture/photo-objects.md)、[相册本地 AI（M2）](../architecture/photo-ai.md#模型与运行环境的交付)
- 代码：[`anas-ai.service`](../../deploy/systemd/system/anas-ai.service)、[安装器](../../scripts/install-v1.0.1-system-services.sh)、[暂存](../../scripts/deploy-dev.ps1)、[检测器清单](../../deploy/models/dfine-m-obj365.json)
- 测试：[系统测试](../../scripts/system-test.sh)
- 调查：不涉及
