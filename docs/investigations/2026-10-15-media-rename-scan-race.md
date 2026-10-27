# 剧集改名与旧媒体库扫描交错导致身份丢失

状态：fixed，待实机验收
更新时间：2026-10-27

## 症状与复现

PR #105 的 push CI 在 `TestRenamingAShowRenamesItsFolderAndEpisodesAndKeepsItsID` 报 `RenameShow(繁花) error = media resource not found`；同一提交的 pull_request CI 和两轮系统测试通过。失败来自影视中心既有并发问题，缩略图用例通过。原始证据：[失败运行](https://github.com/zhongwater123/A-NAS/actions/runs/38561369480)。

原用例重复 50 次未复现。新增 `TestRenameShowKeepsIdentityWhenAnOlderScanIsInFlight` 在文件接口上设置同步点：扫描读取旧文件名后暂停，同时改名，再释放旧扫描并检查原 ID 和收藏。修复前连续 3 次稳定返回 `media resource not found`；单次约 0.5–0.9 秒。

```sh
go test ./internal/media -run '^TestRenameShowKeepsIdentityWhenAnOlderScanIsInFlight$' -count=3
```

## 假设与根因

| 假设 | 验证及结论 |
|---|---|
| 旧扫描覆盖新剧名 | confirmed：固定顺序后原 ID 消失，扫描提交按旧剧名创建另一条剧集，再删除原行 |
| 多次查询读到不同批次的数据 | 不是此最小复现的必要条件；所有扫描完成后原 ID 仍不存在 |
| 测试只是没有等扫描完成 | ruled out：等待后台扫描并再同步扫描一次仍丢失 ID 与收藏 |

`scan` 的媒体库锁仅在扫描间使用；`RenameShow` 修改文件和目录行时未取得同一把锁。扫描读旧目录、改名更新剧集 key、旧扫描提交，便会让原剧集行失去分集引用并被清理；删除触发器同时清理收藏。连续重命名不同剧集容易遇到上一轮改名触发的后台扫描。

## 修复边界

片源改名和剧集改名都与扫描共用每个媒体库的互斥锁，覆盖文件改名、目录更新与返回详情；等到锁后重新读取目录，避免沿用等待前的文件名。释放锁后请求一次新扫描，即使改名失败或回滚，也接续期间被合并的扫描请求。不同媒体库仍可并行；同库改名会等待在途扫描完成。没有增加睡眠或重试来掩盖生产问题。

## 验证

本地结果：三个针对性用例连续 30 轮通过，竞态检测连续 3 轮通过；未改动的前端检查复用后，`make check` 的其余阶段通过。

- 原 CI 用例、新固定交错回归与重名冲突回归连续运行：`go test ./internal/media -run 'TestRenameShowKeepsIdentityWhenAnOlderScanIsInFlight|TestRenamingAShow' -count=30`。
- 竞态检测：`go test -race ./internal/media -run 'TestRenameShowKeepsIdentityWhenAnOlderScanIsInFlight|TestRenamingAShow' -count=3`。
- 完整项目门禁：`make check`。前端源码与依赖未改动，本地复用 #105 已通过的 300 项前端测试、类型检查和构建，重新执行其余门禁；GitHub CI 执行全部检查。

## 关联

- [改名实现](../../internal/media/rename.go)、[扫描](../../internal/media/scan.go)
- [固定交错回归](../../internal/media/rename_race_test.go)、[原 CI 用例](../../internal/media/service_test.go)
- [影视中心技术设计](../architecture/media-center.md)、[影视中心规格](../specs/media-center.md)
