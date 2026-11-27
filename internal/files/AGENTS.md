# 文件服务（internal/files/ 等）

Web 文件的领域逻辑：空间、列目录与对账、回收站、上传、复制移动与快照；也适用于 `internal/filesapi` 与文件代理的客户端 `internal/filebroker`（root 一侧见 [hostops](../hostops/AGENTS.md)）。根目录的 [AGENTS.md](../../AGENTS.md) 仍然适用。权威说明：[存储与文件架构](../../docs/architecture/storage-and-files.md)、[规格](../../docs/specs/basic-storage-and-sharing.md)、[ADR 0008](../../docs/adr/0008-use-unified-linux-identities-and-filesystem-acls.md)。

- **授权只有 ACL：** 文件经文件代理以登录者本人身份读写，由内核按 ACL 判定，Go 里不另存权限（[不变量](../../docs/architecture/OVERVIEW.md#不变量) 6、13）。`files.Service` 的文件系统由调用方给出，本地文件系统只能经 `files.NewLocalService` 显式选择（不变量 19）。
- **条目 ID 跟着 inode 走**（不变量 4）：影视、音乐与传输记录引用它，对账不能删了重建；A-NAS 新建的对象先删掉持有其 inode 的旧行（ext4 立即复用 inode）。
- **改文件的操作要登记“在途”：** 从第一次文件操作到行提交，按空间经 `ordering.go` 登记，对账遇到在途变更就丢弃扫描。漏了会让过期扫描把回收站行写回原路径，文件夹再也恢复不了（[调查](../../docs/investigations/2026-10-21-stale-reconcile-scan-rewrote-trash-rows.md)）。SMB 与助手的“提交更改”不经本服务，由对账吸收。
- **对账只覆盖请求的范围：** 列目录只浅扫该文件夹，`Tree` 只扫子树，只写有变化的行；只有整空间对账能删行，部分对账把没找到的行移到 `.a-nas-stale/<id>`（[调查](../../docs/investigations/2026-10-21-whole-space-reconcile-scale.md)）。
- **写入不留半成品：** 先写成隐藏的 `.a-nas-upload-*`、`.a-nas-copy-*`，再以 `RENAME_NOREPLACE` 就位；替换与跨空间剪切把旧文件或源移进回收站，不直接删（[调查](../../docs/investigations/2026-10-27-cross-space-move-lost-source-changes.md)）。写入口都经 `internal/capacity` 查保留容量，不足回答 507，卷离线或只读时拒绝（不变量 12）。
- **快照：** 拍摄前先登记到 `pending_snapshots`，清扫只删登记过的；保留清理按空间持闸门、每删一份前重读设置（[调查](../../docs/investigations/2026-10-28-snapshot-retention-policy-race.md)）。
- **迁移与验证：** `files.db` 的迁移步骤都带 `Down`（[ADR 0034](../../docs/adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md) 决定 1）。对账与排序是并发代码，跑 `make race-test`；规模用 `ANAS_SCALE_ENTRIES=100000 go test ./internal/files/ -run AtScale`。改了文件代理或文件权限，交部署前跑系统测试。
