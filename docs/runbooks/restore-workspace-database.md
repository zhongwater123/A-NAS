# 从每天的备份恢复共享工作区

状态：draft
更新时间：2026-11-27

## 目的

共享工作区的数据库 `workspace.db` 损坏，或一次误操作在版本与回收站里都挽回不了（例如回收站里的页面已被彻底删除）时，把它换回某一天的备份。成功后共享工作区窗口列出备份那天的页面与版本。

删掉的页面在回收站里 30 天内由上传者或管理员直接恢复，改坏的页面导出旧版本再上传，都不需要本手册（[规格](../specs/shared-workspace.md#页面与版本)）。

## 前提与风险

- 权限：NAS 上的 root。
- 目标：数据卷上的 `/srv/a-nas/data/workspace/`，属主 `a-nas-workspace`。备份在其中的 `backups/workspace-<UTC 日期>.db.gz`，每天一份，留最近 14 份。
- 风险：备份之后保存的版本、上传的页面与改的名字全部丢失；恢复期间共享工作区不可用（`503 workspace_unavailable`），成员的 agent 调用同样失败。
- 停止条件：`a-nas-check` 报数据卷不在、只读或不是预期的那块；`gzip -t` 校验备份失败；要恢复的那天没有备份；停服务后它仍不是 `inactive`（服务还开着数据库时移走它，会丢掉正在写的内容）。

## 步骤

1. 选备份并校验。
   ```sh
   ls -l /srv/a-nas/data/workspace/backups/
   gzip -t /srv/a-nas/data/workspace/backups/workspace-2026-11-26.db.gz && echo ok
   ```
   - 预期：列出 `workspace-<日期>.db.gz`；所选的那份输出 `ok`。
   - 完成标准：确定要回到的日期。日期是 UTC，北京时间 08:00 前保存的内容算在前一天的备份之后。

2. 停服务。
   ```sh
   systemctl stop anas-workspace.service
   systemctl is-active anas-workspace.service
   ```
   - 预期：输出 `inactive`（`failed` 也表示服务已不在运行）。
   - 完成标准：服务已停。输出别的（`active`、`deactivating`、`activating` 等）时它还开着或正要打开数据库：停止，不做下一步。

3. 留下现在的数据库。只在服务的状态是 `inactive` 或 `failed` 时移动：
   ```sh
   cd /srv/a-nas/data/workspace
   state=$(systemctl is-active anas-workspace.service)
   case "$state" in inactive|failed)
     replaced=replaced-$(date -u +%Y%m%dT%H%M%SZ); mkdir -m 0700 "$replaced"
     for f in workspace.db workspace.db-wal workspace.db-shm; do [ -e "$f" ] && mv "$f" "$replaced/"; done
     ls "$replaced";;
   *) echo "服务是 $state，没有移动";;
   esac
   ```
   - 预期：列出 `workspace.db`（`-wal`、`-shm` 可能没有）。输出“服务是 …，没有移动”时回到第 2 步。
   - 完成标准：目录里不再有 `workspace.db*`。现在的 `-wal` 与 `-shm` 不能留在原处：SQLite 会把它们写进恢复的数据库。

4. 解压备份。
   ```sh
   gzip -dc backups/workspace-2026-11-26.db.gz > workspace.db
   chown a-nas-workspace:a-nas-workspace workspace.db && chmod 0600 workspace.db
   ```
   - 预期：`workspace.db` 的属主是 `a-nas-workspace`，权限 `0600`。
   - 完成标准：`sqlite3 workspace.db 'PRAGMA integrity_check'` 输出 `ok`（NAS 上没有 `sqlite3` 时跳过，第 5 步的检查会发现打不开的库）。

5. 启动服务。
   ```sh
   systemctl start anas-workspace.service
   a-nas-check
   ```
   - 预期：服务 `active`；`a-nas-check` 不报共享工作区。
   - 完成标准：日志 `journalctl -u anas-workspace -n 20` 里没有打开数据库失败。

## 验证

- 在桌面打开共享工作区：页面列表与备份那天一致；打开一个页面，最新版本可以浏览、导出。
- 回收站里是备份那天回收站里的页面，删除日期不变，照常在删除 30 天后彻底删除。
- 成员的 agent 调用 `workspace_list_pages` 得到同样的列表；令牌在系统盘的 `control.db` 里，不受恢复影响。

## 回滚或恢复

- 恢复的结果不对：停服务，删掉 `workspace.db*`，把 `replaced-…/` 里的文件移回原处，再启动。
- 确认恢复无误后，`replaced-…/` 由管理员决定删除时间；它占的空间与原来的数据库相同。

## 关联

- 规格：[共享工作区](../specs/shared-workspace.md)
- 技术设计：[共享工作区技术设计的“备份”](../architecture/shared-workspace.md#备份)
- 代码：`internal/workspace/backup.go`、`internal/sqlitedb/backup.go`
- ADR：[0040](../adr/0040-let-members-agents-edit-shared-pages-through-agent-tokens.md)
