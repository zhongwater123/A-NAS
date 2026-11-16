# 照片打分技术设计

产品行为以[照片打分规格](../specs/photo-scoring.md)为准；模块怎样接入 A-NAS 见 [ADR 0037](../adr/0037-plug-removable-modules-in-as-separate-services.md)。

## 组件与数据流

```text
浏览器（网页桌面的“照片打分”窗口，scoring/）
    │ 会话 + CSRF
    ▼
产品服务 ── /api/v1/modules/scoring 转发（只带会话令牌）──► /run/a-nas-modules/scoring.sock（套接字单元，属 a-nas-modules）
    │  模块登记表、GET /api/v1/modules                              │
    │                                                              ▼
    │                                    打分服务 anas-scoring（a-nas-scoring，UID 31100，无网络）
    │                                      ├── Host Agent 的模块会话查询（确认身份、列出在用成员）
    │                                      ├── /srv/a-nas/data/module-scoring：scoring.db、previews/、thumbnails/、full/、backups/、staging/
    │                                      └── /srv/a-nas/data/spaces/shared/照片打分：只读，读原图
    ▼
Host Agent ── 按 /etc/a-nas/modules/scoring.json 建 module-scoring 子卷，提供 /run/a-nas-module-sessions/sessions.sock
```

- 打分服务的单元用 `TemporaryFileSystem=/srv:ro` 盖住整个数据卷，再绑定自己的存储（读写）与 `照片打分`（只读）。读共享空间靠单元授予的 `a-nas-users` 组；这个组对共享空间有写权限，所以只读完全来自只读绑定挂载，系统测试核对写入失败。
- 导入时读原图：先从文件头读尺寸，要打分的照片长边不足 2400 px 的直接跳过（`too_small`，锚点不限）；其余哈希、读 EXIF 方向、在解码预算内解码（[`imagerender`](../../internal/imagerender/imagerender.go)，与相册共用），写出长边 2048 px 的 JPEG 预览。标准库编码不写 EXIF、XMP 与 ICC。最小长边是 `scoring.Options.MinLongEdge`，默认 2400；测试用小图时调低。
- 100% 查看再读一次原图：只给领取者本人、只在含清晰度的批次。模块按 `item_paths` 依次打开原图，SHA-256 仍等于导入时的才用，否则 `original_changed`（410）。在同一解码预算内解码，按存储方向原样编码成质量 92 的 JPEG（[`imagerender.FullJPEG`](../../internal/imagerender/imagerender.go)），不转正，因此不透明的照片不再复制像素，6000 万像素的 JPEG 解码后约 90 MB，在单元 768 MB 的内存上限内；EXIF 方向放在响应头 `X-Image-Orientation`，网页用 CSS 矩阵转正。结果按内容缓存在 `full/`，只留最近用过的 16 份，同一张照片的几位打分人共用。

## 模块与身份

| 位置 | 内容 |
|---|---|
| [`internal/modules`](../../internal/modules/modules.go) | 通用部分：清单、产品服务的转发与“已安装”判断、模块端的会话确认与存储检查 |
| [`internal/scoring`](../../internal/scoring/store.go) | 批次、导入、领取、提交、统计、导出、备份；全部状态在一个 SQLite 中 |
| [`internal/scoringapi`](../../internal/scoringapi/handler.go) | HTTP 接口；产品服务只用它的接口清单做路由与契约检查 |
| [`cmd/anas-scoring`](../../cmd/anas-scoring/main.go) | 模块进程：检查存储、接过套接字单元的监听、每 30 秒复查存储 |
| [`cmd/anas-api/modules.go`](../../cmd/anas-api/modules.go) | 模块登记表；开发模式在产品服务进程内运行打分，NAS 上转发 |
| [`deploy/modules/scoring`](../../deploy/modules/scoring)、[`scripts/modules/scoring`](../../scripts/modules/scoring) | 单元、清单、安装与移除脚本 |
| [`web/src/scoring`](../../web/src/scoring) | 窗口；桌面只在模块已安装且本人是管理员或打分人时显示 |

## 数据

`scoring.db` 由 [`sqlitedb`](../../internal/sqlitedb/sqlitedb.go) 打开：WAL、`synchronous=FULL`、外键、一个连接。`PRAGMA user_version` 是已执行的迁移数，[`store.go`](../../internal/scoring/store.go) 的 `migrations` 逐个执行；第 2 版加入打分项，第 1 版的批次升级后只打总分。

- `batches`：名称、来源文件夹、每张人数、打分标准、打分项（`dimensions`，按目录顺序以逗号连接的 ID，默认 `overall`）、状态（`importing`、`open`、`closed`）、是否已扫描。打分项的目录在 [`dimensions.go`](../../internal/scoring/dimensions.go)：ID 写进数据库与导出，所以只增不改。批次一有回答，改打分项的 `UPDATE` 在同一条语句里核对“没有回答”，避免第一条回答恰好插在检查与修改之间。
- `import_files`：来源文件夹中的每个文件与处理结果（`pending`、`done`、`skipped` 及原因）。导入一次处理一个文件，重启后从 `pending` 继续。
- `contents`：按原图 SHA-256 的预览尺寸；预览文件在 `previews/<前两位>/<sha256>.jpg`，数轴、批次卡片与管理员批次列表用的缩略图（长边 320 px）在第一次请求时从预览生成，放在 `thumbnails/`。不同批次的同一张图共用一份预览与缩略图。管理员批次列表的缩略图是批次随机顺序中的第一张；导入期间它可能换成后来导入的图，所以这个接口不让浏览器长期缓存。
- `items`：批次中的一张图（同一批次内按内容去重），锚点图带参考分；`covered` 是当前有效、非重复检查的打分数，`shuffle` 是导入时的随机序。`item_paths` 记下它在来源文件夹中的全部相对路径。
- `claims`：发给打分人的图。令牌是 128 位随机数，打分人只知道令牌；10 分钟租期；`repeat_of` 标出重复检查。
- 分数与参考分一律存为整数的百分之一分（0–500），只有接口与导出写成两位小数的 0–5 分，避免浮点误差进入数据。
- `ratings`：只追加。每条回答带提交 ID（唯一）、类型、总分、看图时长、显示尺寸；改分时旧记录写 `superseded_at`，新记录写 `replaces`。部分唯一索引保证同一打分人对同一张图只有一条当前有效的回答（重复检查除外）。总分留在 `ratings.score`，所以凑满人数、重复检查、数轴与只打总分的批次都不受打分项影响。
- `rating_scores`：一条打分在总分以外各项的分数（`rating_id`、`dimension`、`score`）。提交在同一事务中写入，且必须正好是批次的其余打分项。

## 并发与一致性

数据库只有一个连接，领取与提交各是一个事务，所以它们在服务内依次执行，两位打分人不会同时拿到同一张图的最后一个名额。

- **领取**：先把这位打分人手里未过期的图返回，再补到 3 张。补的时候从差一人凑满的那层开始找，每层从随机位置沿 `shuffle` 走，跳过本人回答过或正领着的图，且“已有打分 + 别人正领着”必须小于每张人数。约 5% 的发放（本人在该批次已打满 40 张后）换成本人 20 张以前打过的图。
- **提交**：同一提交 ID 再来返回原结果；令牌只认领取它的人。已经回答过的令牌只有在那条回答是本人在该批次的最新一条时才能改，其余返回 `rating_locked`。租期过后才提交的照样收下，除非本人已经通过另一个令牌回答了同一张图（`already_rated`）。每次提交在同一事务中重算这张图的 `covered`。
- **网页**：回答按顺序进出站队列，网络或服务不可用时以同一提交 ID 退避重试，服务拒绝的丢弃并提示。队列不随窗口关闭而丢失；页面关闭前仍有未送达的回答时提示。
- **导出**：在存储内的 `staging/` 先写完 zip（读事务只占一会儿那唯一的连接），再发给浏览器，慢速下载不挡打分。表的每一行是一条回答，列由 `fields` 选、行由 `include` 选（[`export.go`](../../internal/scoring/export.go)）；单图耗时沿 `replaces` 把被替换的回答的看图时长加到当前回答上。CSV 用标准库 `encoding/csv` 写，开头加 UTF-8 字节序标记；JSON Lines 按列的顺序逐个写键，不经 map，以保持列序。
- **看图时长**：网页的 `Stopwatch` 从照片显示开始计时，页面隐藏、打分标准或休息提示打开时暂停（[`model.ts`](../../web/src/scoring/model.ts)）。

## 盲评

打分人的接口（`/me`、`/batches`、`/claims`、`/recent`、锚点）只返回本人的数据；批次卡片的封面与并排对照的照片都来自本人的 `/recent`。数轴上的最近 12 张包括重复检查的回答：被重复的那张至少在 20 张以前，不会同时出现在数轴上，所以数轴不会暴露哪张是重复检查。别人的分数只出现在管理员的统计与导出中，管理员接口由产品服务与模块各查一次管理员身份。预览只发给持有令牌的本人；令牌不是内容哈希，也不是文件名。打分窗口带 `data-vnas-private`，VNAS 不读取、不代打。

## 窗口

整个窗口是一间中性深灰的“暗房”：灰色不带色相，只有指针、进度和选中用一种暖色；数字用随前端打包的 Fraunces 衬线体（[`@fontsource-variable/fraunces`](https://fontsource.org/fonts/fraunces)，SIL OFL），只在打开窗口时加载拉丁字符子集，NAS 不能上网时也一样。打分页上照片的显示尺寸由窗口按预览的宽高算出（[`Stage.tsx`](../../web/src/scoring/Stage.tsx) 的 `fitIn`），所以并排对照时两张照片的缩放可以平滑过渡；还没量到窗口时只靠样式表保证完整显示。

多个打分项共用一条数轴：网页为每张照片保存一组百分之一分，数轴按正在打的项取分数排列缩略图。点选后自动进入下一项由一个 0.35 秒的计时完成，按下数轴或任意键就取消；最后一项没有这个计时。100% 查看（[`Loupe.tsx`](../../web/src/scoring/Loupe.tsx)）把原图按设备像素比缩到一个原图像素对一个屏幕像素，用 `orientationMatrix` 转正，平移到鼠标所指的点不动；原图用对象 URL 保存，换照片时释放。

## 备份与移除

- 每天一次 `VACUUM INTO` 到 `backups/scoring-<日期>.db`，保留 14 份。它们和数据库在同一块数据卷上，只防误操作与文件损坏；要留存的数据靠管理员下载的导出。
- 移除按[运行手册](../runbooks/install-and-remove-photo-scoring.md)：导出、运行移除脚本（单元、程序、清单、账号），再删除 `module-scoring` 子卷。Host Agent 不再看到清单后不会重建它。`照片打分` 文件夹里的照片留在共享空间。

## 验证

- [`internal/scoring/store_test.go`](../../internal/scoring/store_test.go)：导入与去重、按原因计数的跳过、2400 像素限制与锚点例外、先凑满再开新图、8 位打分人并发时没有一张图超过人数、租期与迟到提交、重试与改分、重复检查、统计、打分项的顺序与锁定、第 1 版数据库的升级、导出的列与记录及改过分照片的耗时、100% 查看、备份保留。
- [`internal/scoringapi/handler_test.go`](../../internal/scoringapi/handler_test.go)：打分人看不到别人的图与分数，成员进不了管理接口，批次缩略图只给管理员且不长期缓存；打分项、100% 查看与导出选项经过接口。
- [`internal/modules/modules_test.go`](../../internal/modules/modules_test.go)：清单校验、未安装 404、未运行 503、转发只带会话令牌。
- [`internal/httpapi/routes_test.go`](../../internal/httpapi/routes_test.go)：每个接口都在契约中并按姿态守卫。
- [系统测试](../../scripts/system-test.sh)：安装脚本、存储归属、套接字权限、单元内只看到存储与 `照片打分`、写不进共享空间、跳过太小的照片、沙箱内重读原图的 100% 查看、按全部打分项提交、移除脚本。
- [`web/src/scoring`](../../web/src/scoring/ScoringPanel.test.tsx)：键盘打分、改分、悬停缩略图并排对照、逐项打分与自动进入下一项、100% 查看、管理员的批次缩略图、按原因的跳过、按项的打分人统计与提醒、导出对话框、打分人开关；[`model.test.ts`](../../web/src/scoring/model.test.ts)：计时、八种 EXIF 方向的转正矩阵、导出的列序。

## 关联

- [照片打分规格](../specs/photo-scoring.md)、[决定记录](../decisions/2026-11-12-photo-scoring.md)
- [ADR 0037](../adr/0037-plug-removable-modules-in-as-separate-services.md)、[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)
