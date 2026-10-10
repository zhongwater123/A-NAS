# 启用相册本地 AI

状态：verified（2026-10-10 在 Experimental NAS 首次部署 `6833c7637f73`）
更新时间：2026-10-10

## 目的

第一次在 Experimental NAS 上安装带本地 AI 的 release（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）。本地 AI 随每个 release 安装，之后的升级按[实机配置手册](provision-v1.0.1-experimental-storage.md#安装系统服务)进行即可，不再需要单独操作。成功后：

- `anas-ai.socket` 已启用；第一次搜索或后台整理时，systemd 按需启动 Worker。
- 相册可以用中文按意思搜索，照片详情显示 AI 标签。
- 侧栏的 AI 卡片显示整理进度。

## 前提与风险

- **权限**：管理员 root 会话。暂存使用日常账号 `anas-dev`。
- **目标**：主机名 `a-nas-dev`，使用前按 [SSH 运行手册](bootstrap-experimental-nas-ssh.md) 核对指纹。
- **前提**：
  - 设备已在运行相册服务（[启用相册服务](enable-photo-service.md)）。
  - 系统有 `python3`（3.13）、`libegl1` 与 `libgles2`。2026-10-10 只读核对 Experimental NAS：Python 3.13.5，两个库均已安装。
  - 开发机已构建运行环境，并用真实模型跑过系统测试（[本地环境](../development/LOCAL_ENVIRONMENT.md#系统测试)）。
- **资源**：
  - 系统盘增加约 950 MB：模型 485 MB，运行环境约 460 MB。
  - Worker 工作时占用约 1.2–1.4 GiB 内存，单元上限 2 GiB；空闲 10 分钟后退出。
  - 已有照片在相册空闲 5 分钟后开始整理，每张约 0.6 秒 CPU 时间（开发机 4 核实测；i3-12100 待测）。
- **隐私**：Worker 在自己的网络命名空间中只有回环接口，也打不开网络套接字，读不到数据卷。系统测试已证明这些，部署后再做一次只读核对。
- **停止条件**（出现任一项立即停止）：
  - 安装器以退出码 2 结束（release 缺少 AI 文件或清单无效）、3（缺少 `python3` 或库，或 Python 版本与运行环境不符）或 4（暂存的模型或运行环境与清单不符）。
  - `anas-ai.socket` 未启用，或套接字不是 `root:a-nas-photos 0660`。
  - Worker 以 root 运行，或它的网络命名空间里除 `lo` 外还有别的网卡。

## 步骤

1. 在开发机构建运行环境并暂存。
   - 命令（WSL）：`scripts/build-ai-runtime.sh`，再按[本地环境](../development/LOCAL_ENVIRONMENT.md#系统测试)运行门禁与两次系统测试。
   - 命令（Windows）：`scripts/deploy-dev.ps1 -NasHost a-nas-dev -StageOnly -WslDistribution Ubuntu -WslUser <WSL 用户>`。
   - 预期：输出“The NAS already holds embeddinggemma-2-740m.litertlm.”（模型已于 2026-10-10 按哈希暂存）；运行环境压缩包只在第一次上传，约 163 MB。脚本最后输出 API 与 Host Agent 的 SHA-256。
   - 完成标准：暂存的 release 中 `ai/model.litertlm` 与 `ai/runtime.tar.gz` 都是指向暂存区的链接，暂存脚本已核对两者的哈希。
2. 以 root 按[实机配置手册的安装步骤](provision-v1.0.1-experimental-storage.md#安装系统服务)核对二进制哈希、运行安装器并切换本地控制台链接。
   - 预期：安装器第一次把模型与运行环境复制到 `/opt/a-nas/models/<SHA-256>/` 与 `/opt/a-nas/ai-runtimes/<ID>/`，复制后核对哈希，约 10 秒；最后的 `systemctl status` 含 `anas-ai.socket`，状态为 `active (listening)`。
   - 完成标准：安装器正常结束；`systemctl is-enabled anas-ai.socket` 输出 `enabled`。

## 验证

- **套接字与链接**：
  - `stat -c '%a %U:%G' /run/a-nas-ai/ai.sock` 输出 `660 root:a-nas-photos`。
  - `readlink /opt/a-nas/current/ai/model.litertlm /opt/a-nas/current/ai/runtime` 分别指向 `/opt/a-nas/models/e7a8a2204b91…/embeddinggemma-2-740m.litertlm` 与 `/opt/a-nas/ai-runtimes/<ID>`，ID 与开发机的 `build/ai-runtime.json` 一致。
- **按需启动**：在相册中搜索“猫”。第一次约需 10 秒（核对模型哈希并加载模型）。界面在 AI 不可用时也不提示，因此以 Worker 的状态为准：
  - `systemctl is-active anas-ai.service` 输出 `active`。
  - `journalctl -u anas-ai -b --no-pager` 含 `serving embeddinggemma-2-740m@e7a8a2204b91+mediapipe-1.1.0+tok70+l2 (768 dimensions)`，没有 `model unavailable`。
- **身份与隔离**（Worker 运行期间）：
  - `pid=$(systemctl show -p MainPID --value anas-ai.service)`
  - `ps -o user=,rss= -p "$pid"`：用户为 `a-nas-ai`（动态分配，UID 在 61184–65519），RSS 约 1.2–1.4 GB。
  - `cat /proc/$pid/net/dev`：只列出 `lo`。
  - `systemctl show anas-ai.service -p PrivateNetwork -p RestrictAddressFamilies -p DynamicUser`：`yes`、`AF_UNIX`、`yes`。
- **整理**：相册空闲 5 分钟后，侧栏的 AI 卡片显示“正在整理 x / y”，结束后显示“已整理全部 y 张”；照片详情出现 AI 标签。整理完成后记录 `systemctl show anas-ai.service -p MemoryPeak` 与总耗时，补充到 [M2 实施方案](../architecture/photo-ai.md)。
- **空闲退出**：最后一次请求约 10 分钟后，`systemctl is-active anas-ai.service` 输出 `inactive`，`anas-ai.socket` 仍为 `active`。

## 首次实机部署证据

2026-10-10 12:27，主干 `6833c7637f73` 在 Experimental NAS 上按本手册部署：

- 安装器把模型与运行环境复制到 `/opt/a-nas/models/e7a8a2204b91…/`（463 MB）与 `/opt/a-nas/ai-runtimes/ccd886271b067afa/`（460 MB），release 的 `ai/` 链接到它们；`anas-ai.socket` 已启用，套接字为 `660 root:a-nas-photos`。
- 以 `a-nas-photos` 身份发送 `info`，返回 `embeddinggemma-2-740m@e7a8a2204b91+mediapipe-1.1.0+tok70+l2`、768 维，与标签校准的模型一致。
- Worker 以动态身份 `a-nas-ai`（UID 64936）运行，`/proc/<pid>/net/dev` 只有 `lo`；`PrivateNetwork=yes`、`RestrictAddressFamilies=AF_UNIX`、`DynamicUser=yes`、`MemoryMax=2G`。
- 首次整理 185 张测试照片，至 14:12 共用 CPU 约 1,000 秒，内存峰值 815 MB，没有重启。
- 两处问题：
  - 部署块在重启容器代理后立即运行探针，代理尚未创建套接字，探针失败，重跑通过；探针现在会等待套接字。
  - 整理完成后 Worker 没有在空闲 10 分钟后退出：相册服务每分钟询问一次 Worker 的模型，每次连接都让它重新计时，于是常驻约 800 MB。修复后相册服务只在有任务时联系 Worker，随下一次部署生效；验证方法见“空闲退出”。

## 回滚或恢复

- **只停用 AI**：`systemctl disable --now anas-ai.socket anas-ai.service`。相册照常可用，搜索只按名称与用户标签匹配，侧栏不再显示整理进度。恢复：`systemctl enable --now anas-ai.socket`。
- **回到不含 AI 的旧 release**：按[实机配置手册](provision-v1.0.1-experimental-storage.md#回滚)恢复，并执行上一条停用 AI；旧 release 没有 `ai/` 目录，否则每次连接都会启动失败。
- **模型与运行环境**：回滚不删除 `/opt/a-nas/models` 与 `/opt/a-nas/ai-runtimes`，重新升级时直接复用。删除前先确认没有 release 引用：`find /opt/a-nas/releases -lname '/opt/a-nas/models/*' -o -lname '/opt/a-nas/ai-runtimes/*'`，再另行确认。
- **派生数据**：向量保存在相册 Catalog 中，停用 AI 不删除它们，也不影响照片与用户元数据。

## 关联

- 规格：[相册](../specs/photo-library.md)
- ADR：[0016 AI 随系统内置安装](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)、[0011 相册服务身份](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)
- 架构：[相册本地 AI（M2）](../architecture/photo-ai.md#模型与运行环境的交付)
- 代码：[`anas-ai.socket`](../../deploy/systemd/system/anas-ai.socket)、[`anas-ai.service`](../../deploy/systemd/system/anas-ai.service)、[安装器](../../scripts/install-v1.0.1-system-services.sh)、[运行环境构建](../../scripts/build-ai-runtime.sh)、[暂存](../../scripts/deploy-dev.ps1)
- 测试：[系统测试](../../scripts/system-test.sh)
- 调查：不涉及
