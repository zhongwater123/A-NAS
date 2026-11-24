# 相册（internal/photos/）

相册的领域库：Catalog、内容对象、派生任务、人物与宠物、照片中的文字与向量同步，由相册服务（`internal/photoservice`、`internal/photosapi`）以 `a-nas-photos` 运行。根目录的 [AGENTS.md](../../AGENTS.md) 仍然适用。权威说明：[相册技术设计](../../docs/architecture/photo-library.md)，以及同目录的 `photo-*.md` 与 `vector-index.md`。

- **授权只走 Policy：** 照片资产由 Catalog 的 Policy 授权，不看 ACL（[不变量](../../docs/architecture/OVERVIEW.md#不变量) 6、14）。资产规则只写在 `policy.go` 的 `assetRules`，列表经 `listedAssets`、单张经 `seesAsset`；索引与搜索的命中返回前按同一条件回查。新的读取路径补进 `leak_test.go`：看不到的照片与不存在无法区分。
- **AI 只交候选：** Worker 不写原图、权限或用户元数据，本模块核对租约、资产状态与派生版本后才提交（不变量 7、11）。人物命名与确认、AI 纠错、文字更正是用户数据，关闭能力或换模型时保留（[人物与用户数据](../../docs/architecture/photo-people.md#人物与用户数据)）。
- **向量以 Catalog 为准：** Qdrant 只是索引，经同一事务写入的 outbox 同步，可随时清空重建；人脸与宠物向量不进索引（不变量 20）。outbox 触发器用 `INSERT … ON CONFLICT DO UPDATE`，`INSERT OR REPLACE` 遇到向量的 upsert 会失效（[相册的同步](../../docs/architecture/vector-index.md#相册的同步)）。
- **迁移只增不改：** 已发布的步骤不改；新步骤带 `Down` 的 SQL，旧版本打开更新的库时按它退回（[ADR 0034](../../docs/adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md) 决定 1），并在 `migrate_test.go` 加撤销测试。
- **解码先查预算：** 完整解码只经 `withDecodedImage`（头部预算、共享槽位、panic 记 `undecodable`；不变量 18，[调查](../../docs/investigations/2026-10-26-photo-decode-memory-budget.md)）。
- **照片中的文字：** 更正按内容对象存、各图库共用、留修改记录（[ADR 0033](../../docs/adr/0033-keep-photo-text-corrections-per-content-object-with-a-change-log.md)），不进活动记录；助手只拿敏感值的句柄（[权限与隐私](../../docs/architecture/photo-ocr.md#权限与隐私)）。
- **验证：** `go test ./internal/photos ./internal/photoservice ./internal/photosapi`；向量与剪辑窗口的测试缺 Qdrant 时跳过，先运行 `scripts/fetch-qdrant.sh` 并设 `ANAS_QDRANT_REQUIRED=1`（[说明](../../docs/development/testing.md#qdrant-测试依赖)）。
