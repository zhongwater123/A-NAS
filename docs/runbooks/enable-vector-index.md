# 启用向量索引

状态：draft（系统测试已验证；2026-10-28 在 Experimental NAS 完成第 1–4 步与第 6 步；第 5 步在读路径切换前做过影子比对，切换后的核对待部署后执行）
更新时间：2026-11-11

## 目的

第一次在 Experimental NAS 上安装带向量索引的 release（[ADR 0028](../adr/0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)、[技术设计](../architecture/vector-index.md)）。之后的升级按[部署 A-NAS 软件包](deploy-a-nas-packages.md)进行即可。

成功后：

- 相册服务有自己的 Qdrant 实例。实例按需启动，空闲后退出。
- 相册的图片向量从 Catalog 同步进实例。
- 相册的排序从索引读：索引选出可能排进前面的照片，再按 Catalog 的向量精确打分，结果与精确扫描相同。
- 后台至多每 10 分钟核对一次，比较索引与 Catalog 对同一次搜索的排序。

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
  - apt 报告依赖无法满足、签名或哈希错误，或 `a-nas-check` 有 `FAIL`。
  - 相册空闲以后，`journalctl -u anas-photos -b` 反复出现 `photo vector index sync failed`。
  - 实例因内存超限被杀（日志出现 `oom-kill`）。
  - `photo vector index check` 的 `overlap*` 低于 1，或反复出现 `photo search reads the Catalog's vectors` 且 `reason` 为 `the vector index failed`。

## 步骤

1. 在开发机发布。
   - 命令（WSL）：`scripts/fetch-qdrant.sh`，把 `build/qdrant/<sha256>/` 中的压缩包放进内容库的 `qdrant/<sha256>/`（[`scripts/build-content-packages.sh`](../../scripts/build-content-packages.sh)），再按[部署 A-NAS 软件包](deploy-a-nas-packages.md)第 1 步发布。
   - 预期：`make release` 核对压缩包的大小与 SHA-256，只取出二进制打成 Qdrant 的内容包；此后软件源中已有它，不再构建。
   - 完成标准：`make release` 输出版本、签名密钥的指纹与 `a-nas-archive-keyring` 的 SHA-256。
2. 以 root 按[部署 A-NAS 软件包](deploy-a-nas-packages.md)第 2–6 步安装。
   - 预期：
     - Qdrant 的内容包把二进制放到 `/opt/a-nas/qdrant/<sha256>/qdrant`，`/usr/lib/a-nas/vectors/` 带有清单、配置、`start.sh` 与 `wait-ready.sh`，并链接到这个二进制；
     - `a-nas` 带有三个单元与 `anas-vectors.slice`，启用 `anas-vectors-proxy@a-nas-photos.socket`。
   - 完成标准：`a-nas-check` 全部 `PASS`，其中有 `anas-vectors-proxy@a-nas-photos.socket is active`。
3. 核对套接字与实例。
   - 命令：`stat -c '%a %U %G' /run/a-nas-vectors/a-nas-photos.sock`。
   - 预期：`600 a-nas-photos a-nas-photos`。
   - 命令：`systemctl status anas-vectors@a-nas-photos.service`。
   - 预期：相册服务有向量要同步、有人在用相册，或刚做过搜索时为 `active (running)`；否则为 `inactive`。
4. 等相册空闲（默认 5 分钟无人使用），看首次同步。
   - 命令：`journalctl -u anas-photos -b -o cat | grep 'photo vector index'`。
   - 预期，依次出现：
     - `photo vector index created`，其中 `queued` 等于 Catalog 中的向量数；
     - 若干条 `photo vector index synced`，最后一条 `queued=0`。
   - 完成标准：各条 `synced` 的 `upserted` 与 `deleted` 加起来等于 `created` 的 `queued`，最后一条 `queued=0`。
   - 这时还没有 `photo vector index reconciled`：建出集合的那次对账不再比较。下一次对账在一天后，或相册服务重启后的第一次空闲时，日志应为 `queued=0`。
   - 2026-10-28 实机：`created` 排队 733，三条 `synced` 共写入 730、删除 3，最后 `queued=0`。
5. 核对读路径。
   - 操作：打开桌面相册，搜索几条中文查询，例如“海边的猫”“车库里的电动车”；再对一张照片用“找相似”，打开 AI 聚合。
   - 预期：
     - 打开相册后实例在运行：`systemctl is-active anas-vectors@a-nas-photos.service` 为 `active`（[预热](../architecture/vector-index.md#预热)）。
     - 符合条件的搜索之后出现一行 `photo vector index check`，带 `overlap10`、`overlap100`、`overlap500`、`index_ms`、`exact_ms`、`warm`，三个 `overlap` 都是 1。日志里没有查询文本。
     - 没有 `photo search reads the Catalog's vectors`。首次同步完成前它的 `reason` 为 `the vector index has no collection for the model`，大批导入时为 `too many photos wait for the vector index`，这两种只说明这段时间读的是 Catalog，结果不变。`the vector index failed recently` 只跟在一次 `the vector index failed` 之后，表示出错后的等待（[技术设计](../architecture/vector-index.md#出错之后)）。
   - 只核对这样的搜索：
     - 是第一页；
     - 查询里没有人物名；
     - 这次排序读的是索引；
     - 距上一次核对超过 10 分钟，且上一次已经结束。

     所以不是每条查询都有日志，没有日志不说明出了问题。
   - `overlap*` 低于 1 说明索引选错或漏了对象，记下来查；`warm=false` 的那一行包含实例的启动时间。
   - 完成标准：至少 10 条核对的 `overlap*` 全部为 1，没有 `reason` 为 `the vector index failed` 的回退。
   - 切换前的影子比对（2026-11-06 实机）：
     - 10-28 至 11-06 共 53 条比对，`overlap*` 全部为 1；实例已连接时索引耗时中位 3 ms，冷启动时 298–592 ms（[技术设计](../architecture/vector-index.md#切换前的影子比对)）。
     - 新增照片已同步：11-05 写入 11 张，11-06 写入 43 张。
     - 对账 10 次全部为 `queued=0`，包括部署与重启之后。
     - 新上传的照片在向量算出前就进了 outbox，这一轮同步记为 `deleted`（删除不存在的点，无害），向量算出后再记为 `upserted`。所以一批上传会先后出现等量的 `deleted` 与 `upserted`。
     - 数据来自 root 只读导出的 journal（`journalctl -u anas-photos`），普通账号看不到。
6. 看实例空闲退出。
   - 命令：关掉相册窗口、同步也结束约 2 分钟后（相册服务在最后一个请求后 1 分钟断开连接，代理再等 2 秒；相册窗口只轮询 AI 状态时不算使用），执行 `systemctl is-active anas-vectors@a-nas-photos.service`。
   - 预期：`inactive`。
   - 记录：执行 `systemctl show -p MemoryPeak anas-vectors@a-nas-photos.service`，把内存峰值写进[技术设计](../architecture/vector-index.md#配置)。
   - 2026-11-06 实机：24 次运行的峰值为 123–323 MiB（中位 264 MiB），每次约一分钟后空闲退出。

## 验证

- 实例只有回环：在实例运行时执行 `nsenter -t "$(systemctl show -p MainPID --value anas-vectors@a-nas-photos.service)" -n ip -o link`，应只列出 `lo`。
- 产品服务连不上：`setpriv --reuid=a-nas --regid=a-nas --init-groups python3 -c 'import socket; socket.socket(socket.AF_UNIX).connect("/run/a-nas-vectors/a-nas-photos.sock")'` 应以 `PermissionError` 失败。
- 遥测关闭：`journalctl -u anas-vectors@a-nas-photos -b -o cat | grep -i telemetry` 应为 `Telemetry reporting disabled`。

## 回滚或恢复

- **只关闭索引**：
  - 写入 drop-in：`/etc/systemd/system/anas-photos.service.d/no-vectors.conf`，内容为 `[Service]` 加一行 `Environment=ANAS_PHOTOS_VECTOR_SOCKET=`。
  - 执行 `systemctl daemon-reload && systemctl restart anas-photos`。
  - 结果：相册服务启动时清空 outbox 与同步状态，排序改为读 Catalog（结果相同，更慢），其他功能不变。删除这个文件即可重新启用，索引会从 Catalog 重建。
- **删除索引存储**（索引损坏或需要腾出空间）：
  - 执行 `systemctl stop anas-vectors-proxy@a-nas-photos.service anas-vectors@a-nas-photos.service`。
  - 执行 `rm -rf /var/lib/private/a-nas-vectors/a-nas-photos`。
  - 下一次对账发现集合缺失，会新建集合并重新同步全部向量；对账在相册服务启动后的第一次空闲时和此后每天进行。
- **回到 migration 14 之前的版本**：较早的版本拒绝打开更新过的 Catalog，所以先撤销 migration 14，再按实机配置手册切回旧 release。Catalog 已到 migration 15（照片文字识别）时，先按[照片文字识别的回退](../architecture/photo-ocr.md#交付)撤销 15，并把下面最后一句的版本号照常设为 13。
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
- 代码：[单元](../../deploy/systemd/system/anas-vectors@.service)、[软件包](../../scripts/build-packages.sh)、[内容包](../../scripts/build-content-packages.sh)、[同步](../../internal/photos/vectorsync.go)、[读路径](../../internal/photos/vectorsearch.go)、[核对](../../internal/photos/vectorcheck.go)
