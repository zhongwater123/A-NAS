# 安装与移除照片打分

状态：draft
更新时间：2026-11-12

## 目的

在实验 NAS 上安装、更新[照片打分](../specs/photo-scoring.md)模块，打分结束后把它完整移除（[ADR 0037](../adr/0037-plug-removable-modules-in-as-separate-services.md)）。安装成功时，管理员与被选中的成员桌面上出现“照片打分”；移除成功时，模块的程序、单元、账号、套接字与存储都不在了，共享空间 `照片打分` 文件夹里的照片仍在。

## 前提与风险

- 权限：NAS 的 root（按项目惯例把命令写成一段脚本，核对哈希后交给 root 运行）；开发机能构建 Go。
- 目标：实验 NAS（`ssh a-nas-dev` 暂存）。数据卷挂在 `/srv/a-nas/data`，模块存储是其顶层的 Btrfs 子卷 `module-scoring`，共享文件夹是 `/srv/a-nas/data/spaces/shared/照片打分`。
- 前提：NAS 上的 A-NAS 已换成软件包并包含模块支持（产品服务的模块转发、Host Agent 的模块会话查询与存储，`a-nas` 包经 `systemd-sysusers` 建的 `a-nas-modules`、`a-nas-module-sessions` 组）。没有时安装脚本以退出码 3 停下。
- 风险：
  - 打分数据只在 `module-scoring` 中。这是[实验 NAS 只放可丢弃数据的例外](../decisions/2026-11-12-photo-scoring.md)，所以打分期间要定期导出。
  - 删除子卷不可撤销，删除前必须已经导出并核对。
- 停止条件：安装脚本或核对命令的结果与下文不符；`module-scoring` 的属主不是 UID 31100；要删除的路径不是 `/srv/a-nas/data/module-scoring`。

## 步骤

### 安装或更新

1. 在开发机构建模块：`make scoring-module VERSION=<提交>`。
   - 预期：`build/modules/scoring/` 中有 `anas-scoring`、两个单元、`scoring.json`、`install.sh` 与 `remove.sh`。
2. 把这个目录复制到 NAS 的暂存位置，例如 `/var/tmp/scoring-module/`，并记下 `anas-scoring` 的 SHA-256。
3. root 运行 `bash /var/tmp/scoring-module/install.sh /var/tmp/scoring-module`。
   - 预期：最后一行是 `photo scoring is installed and running`。脚本会：
     - 建 `a-nas-scoring`（UID/GID 31100）；
     - 建 `照片打分` 文件夹（已有时不动）；
     - 装程序、单元与清单；
     - 等 Host Agent 建好 `module-scoring`；
     - 启用并启动套接字与服务。
   - 完成标准：脚本退出码为 0。重复运行即更新：换程序并重启服务，数据不动。

### 导出（打分期间定期做，移除前必须做）

1. 管理员在“照片打分 → 批次”中逐个打开批次，点“导出”，把 zip 存到 NAS 以外的地方。
2. 核对：zip 中有 `manifest.json`、`images.jsonl`、`ratings.jsonl`、`raters.jsonl`；`manifest.json` 中每个文件的行数与 SHA-256 与文件一致（`sha256sum`、`wc -l`）。

### 移除

1. 确认每个批次都已按上节导出并核对。
2. root 运行 `bash /var/tmp/scoring-module/remove.sh`（或 `/opt/a-nas/modules/scoring` 删除前留下的同一份脚本）。
   - 预期：最后一行说明存储被保留。单元、套接字、`/opt/a-nas/modules/scoring`、`/etc/a-nas/modules/scoring.json` 与 `a-nas-scoring` 账号都已删除。
3. 识别要删除的存储：
   - `btrfs subvolume show /srv/a-nas/data/module-scoring` 能列出这个子卷；
   - `stat -c '%u %a' /srv/a-nas/data/module-scoring` 是 `31100 700`（账号已删，显示数字）；
   - `ls /srv/a-nas/data/module-scoring` 只有 `scoring.db*`、`previews`、`backups`、`staging`。

   三项都相符才继续。
4. 确认：把要删除的完整路径念给负责人确认，确认导出已存到 NAS 以外。
5. 删除：`btrfs subvolume delete /srv/a-nas/data/module-scoring`。
6. 照片：`照片打分` 文件夹是成员的照片，由他们决定是否在文件管理中删除。

## 验证

- 安装后：
  - `systemctl is-active anas-module-scoring.service anas-module-scoring.socket` 都是 `active`；
  - `stat -c '%a %U %G' /run/a-nas-modules/scoring.sock` 是 `660 a-nas-scoring a-nas-modules`；
  - 管理员登录桌面后能打开“照片打分”。
- 移除后：
  - `systemctl cat anas-module-scoring.service` 报告不存在；
  - `getent passwd a-nas-scoring` 无输出；
  - `ls /srv/a-nas/data` 中没有 `module-scoring`；
  - 桌面上没有“照片打分”，`GET /api/v1/modules` 的 `items` 为空。

## 回滚或恢复

- 安装失败：读脚本输出与 `journalctl -u anas-module-scoring -n 50`，修正后重跑安装脚本；或运行移除脚本回到安装前（存储为空时可按移除第 3–5 步删除）。
- 更新后服务起不来：用上一版的 `anas-scoring` 重跑安装脚本。数据库只会被更新的版本迁移，旧版本遇到更新的数据库会拒绝启动；这时从 `module-scoring/backups/` 中更新前那天的备份恢复：停服务，复制备份为 `scoring.db`（删除 `scoring.db-wal` 与 `scoring.db-shm`），属主改为 31100，再启动。
- 移除脚本运行后发现还要打分：在删除子卷之前重跑安装脚本即可，数据都在。
- 子卷删除后无法恢复打分系统的状态；导出的 zip 是唯一留存的数据。

## 关联

- 规格：[照片打分](../specs/photo-scoring.md)
- ADR：[0037](../adr/0037-plug-removable-modules-in-as-separate-services.md)
- 架构：[照片打分技术设计](../architecture/photo-scoring.md)
- 脚本：[`install.sh`](../../scripts/modules/scoring/install.sh)、[`remove.sh`](../../scripts/modules/scoring/remove.sh)
- 调查：不涉及
