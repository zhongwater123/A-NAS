# 虚拟伙伴旧分支中的第三方素材怎么清除

状态：pending
更新时间：2026-11-06

## 问题

虚拟伙伴早期的三个分支 `codex/companion-review-20261102`、`codex/companion-personal-agent-runtime-20261103` 与 `codex/companion-sync-20261105` 的历史中，提交 `5b44bc8e` 加入了第三方精灵图 `web/src/companion/assets/naiwa.webp`，它不在 MIT 范围内。[#404](https://github.com/zhongwater123/A-NAS/issues/404) 要求从分支上移除；2026-11-06 已在前两个分支追加删除提交（`f0f75f52`、`7e666330`），分支最新的文件中已没有它。但仓库是公开的，这份素材仍可获取：

- `5b44bc8e` 仍是三个分支的祖先，可按提交号直接下载。
- 已关闭的 [#346](https://github.com/zhongwater123/A-NAS/pull/346) 的拉取请求引用 `refs/pull/346/head` 也包含该提交；拉取请求的引用无法由协作者删除。

三个分支的内容已由 [PR #412](https://github.com/zhongwater123/A-NAS/pull/412)（V1）与 V2 原型取代，不再提交。

## 选项

- **保留分支与历史**：不需要操作；素材继续可以公开获取。
- **改写三个分支的历史并强推**：分支上不再有该提交；`refs/pull/346/head` 与按提交号的访问仍在，仍要联系 GitHub Support；已检出这些分支的人需要重新获取。
- **删除三个旧分支，并由仓库所有者联系 GitHub Support 清除 `refs/pull/346/head` 与缓存**：分支删除前在本地保留副本；GitHub Support 清除后，按提交号也无法再获取。代价是要由仓库所有者提交请求。

## 决定

定下后填写。

## 关联

- 提议与审查：[#404](https://github.com/zhongwater123/A-NAS/issues/404)
- 加入素材的 PR：[#346](https://github.com/zhongwater123/A-NAS/pull/346)
- 素材许可：[#414](2026-11-06-companion-v1.md) 记录的 2026-11-06 决定
- GitHub 文档：[Removing sensitive data from a repository](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository)
