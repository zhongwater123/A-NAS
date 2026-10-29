# 启用向量索引

状态：draft（系统测试已验证；2026-10-28 在 Experimental NAS 完成第 1–4 步与第 6 步，影子比对的数据待收集）
更新时间：2026-10-28

## 目的

第一次在 Experimental NAS 上安装带向量索引的 release（[ADR 0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)、[技术设计](../architecture/vector-index.md)）。之后的升级按[实机配置手册](provision-v1.0.1-experimental-storage.md#安装系统服务)进行即可。

成功后：

- 相册服务有自己的 Qdrant 实例。实例按需启动，空闲后退出。
- 相册的图片向量从 Catalog 同步进实例。
- 文搜图时，相册服务在后台用索引再查一次，并记录与精确扫描的重合率和耗时。用户看到的结果不变。

## 前提与风险

- **权限**：管理员 root 会话。暂存用日常账号 `anas-dev`。
- **目标**：主机名 `a-nas-dev`，使用前按 [SSH 运行手册](bootstrap-experimental-nas-ssh.md)核对指纹。
- **前提**：
  - 用户已同意部署本版本。
  - 开发机已运行 `make check`，并按[本地环境](../development/testing.md#系统测试)跑过系统测试。
  - 开发机已用 `scripts/fetch-qdrant.sh` 取得 [`deploy/qdrant/qdrant.json`](../../deploy/qdrant/qdrant.json) 固定的压缩包。
  - NAS 上有 `/usr/lib/systemd/systemd-socket-proxyd`（Debian 的 `systemd` 包自带）；回退时还要 `sqlite3`。
- **资源**：
  - 系统盘：Qdrant 二进制约 93 MB，按内容只存一份；索引存储很小，2 万张照片约几十 MB。
  - 内存：实例运行时，系统测试中空集合约 75 MB。单元的 `MemoryHigh=512M`、`MemoryMax=768M`。
  - 首次同步会把 Catalog 中的全部向量写入索引，只在相册空闲、门控允许时进行。
- **停止条件**（出现任一项立即停止）：
  - 安装器以退出码 2（缺少向量索引文件或 Qdrant 清单无效）、3（缺少 `systemd-socket-proxyd`）或 4（暂存的 Qdrant 与清单不符）结束。
  - 相册空闲以后，`journalctl -u anas-photos -b` 反复出现 `photo vector index sync failed`。
  - 实例因内存超限被杀（日志出现 `oom-kill`）。

## 步骤

1. 在开发机暂存。
   - 命令（WSL）：`scripts/fetch-qdrant.sh`。
   - 命令（Windows）：`scripts/deploy-dev.ps1 -NasHost a-nas-dev -StageOnly -WslDistribution Ubuntu -WslUser <WSL 用户>`。
   - 预期：Qdrant 压缩包上传一次，此后的暂存输出 “The NAS already holds qdrant-x86_64-unknown-linux-musl.tar.gz.”。
   - 完成标准：暂存 release 中有 `vectors/qdrant.json`、`vectors/config.yaml`、`vectors/start.sh`、`vectors/wait-ready.sh`，`vectors/qdrant.tar.gz` 链接到 `apps/a-nas/qdrant/<sha256>/`；暂存脚本已核对哈希。
2. 以 root 按[实机配置手册的安装步骤](provision-v1.0.1-experimental-storage.md#安装系统服务)核对二进制哈希并运行安装器。
   - 预期：
     - 安装器把压缩包复制到自己的临时目录，核对大小与 SHA-256，只取出二进制放到 `/opt/a-nas/qdrant/<sha256>/qdrant`；
     - 安装三个单元与 `anas-vectors.slice`，启用 `anas-vectors-proxy@a-nas-photos.socket`。
   - 完成标准：安装器正常结束，状态输出中这个套接字是 `active (listening)`。
3. 核对套接字与实例。
   - 命令：`stat -c '%a %U %G' /run/a-nas-vectors/a-nas-photos.sock`。
   - 预期：`600 a-nas-photos a-nas-photos`。
   - 命令：`systemctl status anas-vectors@a-nas-photos.service`。
   - 预期：相册服务有向量要同步，或刚做过搜索时为 `active (running)`；否则为 `inactive`。
4. 等相册空闲（默认 5 分钟无人使用），看首次同步。
   - 命令：`journalctl -u anas-photos -b -o cat | grep 'photo vector index'`。
   - 预期，依次出现：
     - `photo vector index created`，其中 `queued` 等于 Catalog 中的向量数；
     - 若干条 `photo vector index synced`，最后一条 `queued=0`。
   - 完成标准：各条 `synced` 的 `upserted` 与 `deleted` 加起来等于 `created` 的 `queued`，最后一条 `queued=0`。
   - 这时还没有 `photo vector index reconciled`：建出集合的那次对账不再比较。下一次对账在一天后，或相册服务重启后的第一次空闲时，日志应为 `queued=0`。
   - 2026-10-28 实机：`created` 排队 733，三条 `synced` 共写入 730、删除 3，最后 `queued=0`。
5. 收集影子比对。
   - 操作：用桌面相册搜索几条中文查询，例如“海边的猫”“车库里的电动车”。
   - 预期：符合条件的查询之后，日志出现一行 `photo vector index shadow search`，带 `overlap10`、`overlap100`、`overlap500`、`index_ms`、`exact_ms`、`warm`。日志里没有查询文本。
   - 只比对这样的查询：
     - 是第一页；
     - 查询里没有人物名；
     - 查询向量的模型与集合一致；
     - outbox 为空：刚导入、回收或首次同步还没完成时都不为空；
     - 上一次比对已经结束：忙时直接丢弃。

     所以不是每条查询都有日志，没有日志不说明出了问题。
   - `overlap*` 应为 1，低于 1 说明索引与 Catalog 不一致，记下来查。`warm=false` 的那一行包含实例的启动时间。
   - 完成标准：把至少 20 条比对的数字整理进[当前状态](../status/CURRENT.md)或对应 issue，作为是否切换读路径的依据。
6. 看实例空闲退出。
   - 命令：在最后一次搜索或同步约 2 分钟后（相册服务空闲 1 分钟断开连接，代理再等 2 秒），执行 `systemctl is-active anas-vectors@a-nas-photos.service`。
   - 预期：`inactive`。
   - 记录：执行 `systemctl show -p MemoryPeak anas-vectors@a-nas-photos.service`，把内存峰值写进[技术设计](../architecture/vector-index.md#配置)。

## 验证

- 实例只有回环：在实例运行时执行 `nsenter -t "$(systemctl show -p MainPID --value anas-vectors@a-nas-photos.service)" -n ip -o link`，应只列出 `lo`。
- 产品服务连不上：`setpriv --reuid=a-nas --regid=a-nas --init-groups python3 -c 'import socket; socket.socket(socket.AF_UNIX).connect("/run/a-nas-vectors/a-nas-photos.sock")'` 应以 `PermissionError` 失败。
- 遥测关闭：`journalctl -u anas-vectors@a-nas-photos -b -o cat | grep -i telemetry` 应为 `Telemetry reporting disabled`。

## 回滚或恢复

- **只关闭索引**：
  - 写入 drop-in：`/etc/systemd/system/anas-photos.service.d/no-vectors.conf`，内容为 `[Service]` 加一行 `Environment=ANAS_PHOTOS_VECTOR_SOCKET=`。
  - 执行 `systemctl daemon-reload && systemctl restart anas-photos`。
  - 结果：相册服务启动时清空 outbox 与同步状态，其他功能不变。删除这个文件即可重新启用，索引会从 Catalog 重建。
- **删除索引存储**（索引损坏或需要腾出空间）：
  - 执行 `systemctl stop anas-vectors-proxy@a-nas-photos.service anas-vectors@a-nas-photos.service`。
  - 执行 `rm -rf /var/lib/private/a-nas-vectors/a-nas-photos`。
  - 下一次对账发现集合缺失，会新建集合并重新同步全部向量；对账在相册服务启动后的第一次空闲时和此后每天进行。
- **回到 migration 14 之前的版本**：较早的版本拒绝打开更新过的 Catalog，所以先撤销 migration 14，再按实机配置手册切回旧 release。
  1. 执行 `systemctl stop anas-photos`。
  2. 以相册服务的身份执行下面的语句。
     ```sh
     setpriv --reuid=a-nas-photos --regid=a-nas-photos --clear-groups sqlite3 /srv/a-nas/data/photos/catalog.db \
       'DROP TRIGGER embeddings_vector_outbox_insert; DROP TRIGGER embeddings_vector_outbox_update;
        DROP TRIGGER embeddings_vector_outbox_delete; DROP TRIGGER assets_vector_outbox_insert;
        DROP TRIGGER assets_vector_outbox_delete; DROP TRIGGER assets_vector_outbox_update;
        DROP TABLE vector_outbox; DROP TABLE vector_index_state; DROP INDEX embeddings_by_age;
        PRAGMA user_version = 13;'
     ```
     这组语句与 [`TestVectorIndexMigrationCanBeUndone`](../../internal/photos/migrate_test.go) 相同，测试证明撤销后再次升级照常工作。
  3. 切回旧 release，再执行 `systemctl disable --now anas-vectors-proxy@a-nas-photos.socket`。
  4. `/opt/a-nas/qdrant` 与实例的存储不会自动删除，确认不再需要后另行删除。

## 关联

- 规格：[相册规格](../specs/photo-library.md)
- ADR：[0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)、[0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- 技术设计：[向量索引](../architecture/vector-index.md)
- 代码：[单元](../../deploy/systemd/system/anas-vectors@.service)、[安装器](../../scripts/install-v1.0.1-system-services.sh)、[同步](../../internal/photos/vectorsync.go)、[影子比对](../../internal/photos/vectorshadow.go)
