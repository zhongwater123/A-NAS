# 过期的目录对账扫描改写回收站行，文件夹无法从回收站恢复

状态：fixed locally（未部署）
更新时间：2026-10-21

## 症状与影响

[#164](https://github.com/zhongwater123/A-NAS/issues/164)（架构审查 #159，基线 `9cda8a3`）：一次列目录或后台对账扫描完空间后，有人删除了一个文件夹，对账随后按 inode 把这个文件夹在回收站中的各行改回原路径，`trashed=1` 不变。恢复时，子行按路径前缀算出的新路径与根行相同，违反 `UNIQUE(space_id, relative_path)`，事务回滚，每次重试都失败。回收站经 SMB 隐藏，没有别的入口，30 天后条目被到期清除，数据永久丢失。

同一根因的其他症状：

- 扫描之后新建、上传、复制或恢复的条目被当作“缺失”删除，下一轮对账以新 ID 重建，传输记录、影视与音乐对条目 ID 的引用随之失效。
- 扫描之后移动的条目被写回旧路径，打开时报 `not_found`，直到下一次对账。
- 在删除移动文件之后、提交行之前完成的对账，把整个文件夹的行当作缺失删除；删除随后读不到原路径而失败，回收站条目丢失内容的 ID。
- Samba 回收导入扫描 `.a-nas-trash/<用户名>` 时，刚被 Web 删除移入、尚未记录的文件夹不在已知条目中，其中的每个文件被逐个导入为 SMB 删除的条目，原文件夹条目变空。这一条在审查中未列出，复现时发现。
- 损坏的回收站行占着空间中的旧路径，在那里新建同名文件夹或上传同名文件因唯一约束失败。

## 最小复现

固定交错的回归测试都在 [reconcile_race_test.go](../../internal/files/reconcile_race_test.go)。测试用文件系统包装在扫描读完之后、或删除把文件夹移入回收站之后、提交行之前暂停，在暂停期间执行另一操作，然后放行：

```sh
go test ./internal/files/ -run 'Reconcil|StaleScan|BinStill|Damaged|SameFile|Recycle' -count=1 -v
```

修复前 10 个新用例中 9 个失败：

| 用例 | 修复前结果 |
|---|---|
| 扫描后删除文件夹再恢复 | `Restore` 报 `UNIQUE constraint failed: entries.space_id, entries.relative_path` |
| 删除移动文件后、提交前对账 | `Delete` 报 `sql: no rows in result set` |
| 删除进行中做 Samba 回收导入 | 回收站出现 3 个条目：空文件夹与其中两个文件 |
| 扫描后新建、上传与移动 | 移动后的文件 `OpenContent` 报 `file entry not found` |
| 后台反复对账时删除并恢复 25 轮 | 第 0 轮 `Restore` 即报唯一约束失败 |
| 路径被替换成另一个文件 | 旧 ID 打开的是新文件 |
| 已损坏的回收站行（按旧版本写回原路径） | `Restore` 报唯一约束失败 |
| 损坏行占着旧路径时新建同名文件夹 | `CreateDirectory` 报唯一约束失败 |
| 回收站行声明的 inode 已被空间中的新文件使用 | 新文件不出现在列表中 |
| 回收站仍持有同一 inode（硬链接） | 通过（保护用例，确认修复不会删掉回收站仍持有的行） |

## 观察事实

- `List`、`Tree`、快照比较、复制与上传找回文件夹都对整个空间调用 `reconcile`；后台每 5 分钟为每个记住的会话再做一遍。扫描与逐条登记之间没有任何按空间的锁或代次，`internal/files` 只有 `spaceRootsMu` 与 `uploadsMu`。
- 登记的 upsert 以 `ON CONFLICT(space_id, inode)` 更新 `relative_path`，不看 `trashed`；`UNIQUE(space_id, inode)` 同样约束回收站行。
- 对账只删除 `trashed=0` 的缺失行，且跳过 `.a-nas-trash`，所以被改写的回收站行之后不会被修正。
- 恢复以 `relative_path = 新路径 || substr(relative_path, length(回收站路径) + 1)` 计算子行路径；子行路径若已不在回收站路径下，结果就是新路径本身。
- `entryPath` 只按记录的路径 `Lstat`，不比较 inode。

## 假设

| 假设 | 验证方法 | 结果 |
|---|---|---|
| 对账写入的扫描早于删除，upsert 把回收站行改回原路径 | 扫描后暂停、删除、放行，再恢复 | confirmed：唯一约束失败 |
| 对账在删除提交前把文件夹行当作缺失删除 | 删除移动文件后暂停，期间列目录 | confirmed：删除读不到行而失败 |
| Samba 回收导入与 Web 删除有同样的竞争 | 删除移动文件后暂停，期间列回收站 | confirmed：文件被逐个导入 |
| 单靠拒绝改写回收站行就足够 | 对照“扫描后新建、上传、移动”用例 | ruled out：新条目仍被删除重建、移动仍被写回 |

## 根因

目录对账是“扫描文件系统、再逐条写行”的两段操作，Web 变更是“改文件系统、再提交行”的两段操作，两者在同一空间上没有任何顺序。对账写入的是扫描那一刻的状态，却覆盖在扫描之后已提交的变更之上；upsert 又不区分回收站行，于是把已删除条目改回原路径。Samba 回收导入对回收目录做同样的扫描后写入，与删除、恢复、清除交错时也会写入过期状态。

## 修复与回归证据

- 修复：[ordering.go](../../internal/files/ordering.go) 为每个空间维护代次与在途变更数。删除、恢复、清除（含到期清除）、移动、跨空间移动、复制、上传、文件夹上传与新建文件夹从第一次文件操作到行提交为止在途。[对账](../../internal/files/service.go)与 Samba 回收导入记下扫描开始时的代次：开始时有变更在途、或写入前代次已变，就丢弃扫描；否则在一个事务内写入，期间同一空间的新变更等待。上传完成时找回被移走的文件夹、分享选择的子树扫描必须拿到结果，改为等在途变更结束后独占扫描与写入。复制不再对整个目标空间对账，只登记复制出的子树。
- 回收站行：登记前若 inode 属于回收站行，回收站仍持有该 inode（或当前用户无法确认）就不收录这一项，否则删除这条过期行；同一路径让出时也包括被旧版本写回原路径的回收站行。删除只移动 `trashed=0` 的行。
- 修复已损坏的目录：恢复时，不在回收站路径下的子行改为 `.a-nas-stale/<id>` 占位并离开回收站，下一次对账按 inode 放回正确位置并保留 ID；根行直接写入恢复后的路径。
- `entryPath` 比较记录的 inode 与 `Lstat` 结果，不一致按不存在处理。
- 测试：修复后上述 10 个用例全部通过；`go test -race` 对这组用例共重复 8 次通过；`internal/files` 全部测试与依赖它的 filebroker、media、music、httpapi、assistantchanges 测试通过。`make check-steps` 中除 `web-test` 外的各步骤（web-typecheck、web-build、fmt-check、docs-check、ops-check、vet、test、ai-test、build-binaries）在 ext4 副本中通过；`web-test` 在 WSL 负载约 23 时有 66 个前端用例超时，本次未改动前端代码，交给 CI 验证。

## 后续工作

- 部署后在实验 NAS 上：一人在共享空间反复列目录，另一人删除并恢复带子文件夹的文件夹；检查恢复成功、子条目 ID 不变。已被旧版本损坏的回收站条目在第一次恢复时修复。
- 对账扫描整个空间、写入事务随空间变大而变长的问题已按范围对账处理，见 [#167 的调查](2026-10-21-whole-space-reconcile-scale.md)。
- 助手“提交更改”与 SMB 一样绕过 `files.Service` 直接改文件，由对账吸收，不参与这里的排序。

## 关联

- Issue：[#164](https://github.com/zhongwater123/A-NAS/issues/164)，审查索引 [#159](https://github.com/zhongwater123/A-NAS/issues/159)
- 代码：[ordering.go](../../internal/files/ordering.go)、[service.go](../../internal/files/service.go)、[upload.go](../../internal/files/upload.go)、[sharing.go](../../internal/files/sharing.go)
- 测试：[reconcile_race_test.go](../../internal/files/reconcile_race_test.go)
- 架构：[存储与文件](../architecture/storage-and-files.md#身份与一致性)
- 类似问题：[剧集改名与旧扫描交错导致身份丢失](2026-10-15-media-rename-scan-race.md)
- 规格：不涉及
- ADR：不涉及
- 运行手册：不涉及
