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
- 100% 查看再读一次原图：只给领取者本人，每个批次都有。模块按 `item_paths` 依次打开原图，SHA-256 仍等于导入时的才用，否则 `original_changed`（410）。在同一解码预算内解码，按存储方向原样编码成质量 92 的 JPEG（[`imagerender.FullJPEG`](../../internal/imagerender/imagerender.go)），不转正，因此不透明的照片不再复制像素，6000 万像素的 JPEG 解码后约 90 MB，在单元 768 MB 的内存上限内；EXIF 方向放在响应头 `X-Image-Orientation`，网页用 CSS 矩阵转正。结果按内容缓存在 `full/`，只留最近用过的 16 份，同一张照片的几位打分人共用。

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

`scoring.db` 由 [`sqlitedb`](../../internal/sqlitedb/sqlitedb.go) 打开：WAL、`synchronous=FULL`、外键、一个连接。`PRAGMA user_version` 是已执行的迁移数，[`store.go`](../../internal/scoring/store.go) 的 `schema` 逐个执行；第 2 版加入打分项，第 1 版的批次升级后只打总体；第 3 版加入自定义打分项，并让总体可以排在第一项以外（见下面的 `rating_scores`）；第 4 版给批次加上打分方式，之前的批次都是两位小数的数轴；第 5 版重建 `batches` 让它收五档，并给与新内置项同名的自定义项改名（见下面的 `batches`）。

第 5 版起迁移用项目共用的 `sqlitedb.Schema`：每一步带反向步骤，以后的版本留下的数据库，本版本按存下的反向步骤退回。第 4 版及以前的版本是手写的迁移，遇到更新的数据库拒绝启动，不会执行反向步骤；所以模块升级已有的数据库之前，先把它复制成 `backups/scoring-<日期>-v<原版本>.db`（`store.go` 的 `migrate`），复制不成功就不升级。回到这些版本时恢复这份副本（[运行手册](../runbooks/install-and-remove-photo-scoring.md#回滚或恢复)）。副本与每天的备份一起计数，保留最近 14 份。

- `batches`：名称、来源文件夹、每张人数、打分标准、打分项（`dimensions`，按打分顺序以逗号连接的 ID，即管理员点选的先后；第 1 版留下的空值是 `overall`）、状态（`importing`、`open`、`closed`）、是否已扫描。内置打分项在 [`dimensions.go`](../../internal/scoring/dimensions.go)：ID 写进数据库与导出，所以只增不改，名称与说明可以改（`overall` 由总分改名为总体，打分手册的五项有说明）。打分项的名称在内置与自定义之间不重复：第 5 版把名为总体、画质或情绪的自定义项改名为“原名（自定义）”，这个名称也被占用时改为“原名（ID）”，ID 不变；新建自定义项也不能用内置项以前的名称“总分”。目录的顺序就是新建批次时的顺序。三档与五档的档名跟着打分项走：`emotion` 的三档是 1 平静、3 中间、5 激烈，五档是 1 很平静到 5 激烈，其余用 1 差到 5 好与 1 很差到 5 很好，导出的列说明（`scales.go` 的 `describe`）与网页（`model.ts` 的 `levelsFor`）一致。情绪在数轴上也从 1 开始：提交时 `answers` 拒绝情绪低于 1.00 的分，网页的 `lowestScore` 让数轴、按键与输入都从 1 开始，导出的列说明写“1 到 5”。批次一有回答，改打分项的 `UPDATE` 在同一条语句里核对“没有回答”，避免第一条回答恰好插在检查与修改之间。
- `batches.scale` 与 `batches.hide_decimals`：打分方式（`five`、`hundredths`、`tenths`、`whole`、`three`，见 [`scales.go`](../../internal/scoring/scales.go)）与是否不给打分人看分数；表上的约束保证只有两种带小数的方式能不给看。提交时每个分数都要是批次打分方式中的一个（一位小数是 10 的倍数、整数是 100 的倍数、三档是 100、300、500，五档是 100 到 500 中 100 的倍数）。第 5 版加五档：SQLite 改不了列上的约束，迁移在关闭外键的连接上重建 `batches` 表（`sqlitedb.Schema`）。它的反向步骤把五档的批次改为整数（整数包含五档的全部分数），但只有用 `sqlitedb.Schema` 且步数更少的版本才会执行，现在没有这样的版本，见上一段。它们与打分项一起在第一条回答时固定，改动用同一条带“没有回答”核对的 `UPDATE`。
- `custom_dimensions`：管理员自定义的打分项（`custom_<n>`、名称、说明）。编号只增不复用，名称在全部打分项中唯一；没有改名与删除，所以一个 ID 在每个批次、每次导出里都是同一个意思。
- `import_files`：来源文件夹中的每个文件与处理结果（`pending`、`done`、`skipped` 及原因）。导入一次处理一个文件，重启后从 `pending` 继续。
- `contents`：按原图 SHA-256 的预览尺寸；预览文件在 `previews/<前两位>/<sha256>.jpg`，数轴、批次卡片与管理员批次列表用的缩略图（长边 320 px）在第一次请求时从预览生成，放在 `thumbnails/`。不同批次的同一张图共用一份预览与缩略图。管理员批次列表的缩略图是批次随机顺序中的第一张；导入期间它可能换成后来导入的图，所以这个接口不让浏览器长期缓存。
- `items`：批次中的一张图（同一批次内按内容去重），锚点图带参考分；`covered` 是当前有效、非重复检查的打分数，`shuffle` 是导入时的随机序。`item_paths` 记下它在来源文件夹中的全部相对路径。
- `claims`：发给打分人的图。令牌是 128 位随机数，打分人只知道令牌；10 分钟租期；`repeat_of` 标出重复检查。
- 分数与参考分一律存为整数的百分之一分（0–500），只有接口与导出写成 0–5 分（导出按打分方式写两位、一位或不带小数），避免浮点误差进入数据；打分方式只限制哪些百分之一分可以提交。
- `ratings`：只追加。每条回答带提交 ID（唯一）、类型、批次第一项的分数、看图时长、显示尺寸；改分时旧记录写 `superseded_at`，新记录写 `replaces`。部分唯一索引保证同一打分人对同一张图只有一条当前有效的回答（重复检查除外）。第 3 版以前每个批次的第一项都是总体，所以 `ratings.score` 不用迁移就符合“第一项”的含义，凑满人数、重复检查与只打总体的批次都不受打分项影响。
- `rating_scores`：一条打分在第一项以外各项的分数（`rating_id`、`dimension`、`score`），总体不是第一项时也在这里。提交在同一事务中写入，且必须正好是批次的其余打分项。第 3 版重建这张表，去掉了“不能是总体”的约束。接口沿用同一分法：`score` 是第一项，`scores` 是其余各项。

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

多个打分项共用一条数轴：网页为每张照片保存一组百分之一分，新照片从空的一组开始，数轴按正在打的项取分数排列缩略图，打完的照片落到第一项的分数处。只打总体的批次不显示项名，其余都写出正在打的项和自定义项的说明。打分项的目录（内置与自定义）只在管理员接口 `/admin/dimensions` 中；打分人从自己的批次里拿到这一批的打分项与打分方式。数轴按打分方式画成细刻度、整数的六个圆点、三档的三块或五档的五块；同一个分数的缩略图在整数与分档上并排，不叠在一点。不给打分人看分数时窗口不画任何分数，但接口照样返回本人的分数，所以这只是界面上的约定，不是保密手段。点选后进入下一项、最后一项后提交，都由一个 0.35 秒的计时完成，按下数轴或任意键就取消。“上一张”只能回到刚提交的一张，因为模块只允许替换本人最近一次的回答（`rating_locked`）；回去前网页把这一张已打的分数和所在的项暂存，改完提交或按 Esc 后恢复。全屏用浏览器的 Fullscreen API，作用在带 `data-scoring-window` 的打分窗口上，让窗口的提示一起留在屏幕上。100% 查看（[`Loupe.tsx`](../../web/src/scoring/Loupe.tsx)）把原图按设备像素比缩到一个原图像素对一个屏幕像素，用 `orientationMatrix` 转正，平移到鼠标所指的点不动；右下角的小窗用已经加载的预览图，不再多读一次；原图用对象 URL 保存，换照片时释放。

打开批次时窗口依次显示新手教程（[`Tutorial.tsx`](../../web/src/scoring/Tutorial.tsx)，每次都有）、校准页（本浏览器第一次）和打分页。教程的文字与 15 张示例照片随前端打包（[`handbook.ts`](../../web/src/scoring/handbook.ts) 与 `tutorial/`），按打分项 ID 与批次的打分项对上，不经过模块的接口，所以手册改了要改前端。示例照片只在打开窗口后按需加载，不计入桌面首屏的体积；试打的选择只在页面里，不提交也不保存。

## 备份与移除

- 每天一次 `VACUUM INTO` 到 `backups/scoring-<日期>.db`，保留 14 份。复制经一个只读连接（`sqlitedb.Copy`），不占模块唯一的连接，复制期间打分照常。它们和数据库在同一块数据卷上，只防误操作与文件损坏；要留存的数据靠管理员下载的导出。
- 移除按[运行手册](../runbooks/install-and-remove-photo-scoring.md)：导出、运行移除脚本（单元、程序、清单、账号），再删除 `module-scoring` 子卷。Host Agent 不再看到清单后不会重建它。`照片打分` 文件夹里的照片留在共享空间。

## 验证

- [`internal/scoring/store_test.go`](../../internal/scoring/store_test.go)：导入与去重、按原因计数的跳过、2400 像素限制与锚点例外、先凑满再开新图、8 位打分人并发时没有一张图超过人数、租期与迟到提交、重试与改分、重复检查、统计、打分项按选择的顺序与锁定、自定义打分项、打分方式收的分数与锁定、第 1、2、4 版数据库的升级（第 4 版升级前的副本与同名自定义项的改名）、导出的列与记录及改过分照片的耗时、100% 查看、备份保留。
- [`internal/scoring/schema_test.go`](../../internal/scoring/schema_test.go)：带反向步骤的迁移退回后与旧版本新建的数据库结构相同，再升级后与原来相同（`sqlitedbtest.CheckReversible`）。
- [`internal/scoringapi/handler_test.go`](../../internal/scoringapi/handler_test.go)：打分人看不到别人的图与分数，成员进不了管理接口，批次缩略图只给管理员且不长期缓存；打分项（按选择的顺序，第一项在 `score`）、自定义打分项与同名拒绝、打分方式（打分人的批次带上它，拒绝不符合的分数与第一条打分后的改动）、100% 查看与导出选项经过接口。
- [`internal/modules/modules_test.go`](../../internal/modules/modules_test.go)：清单校验、未安装 404、未运行 503、转发只带会话令牌。
- [`internal/httpapi/routes_test.go`](../../internal/httpapi/routes_test.go)：每个接口都在契约中并按姿态守卫。
- [系统测试](../../scripts/system-test.sh)：安装脚本、存储归属、套接字权限、单元内只看到存储与 `照片打分`、写不进共享空间、跳过太小的照片、沙箱内重读原图的 100% 查看、按全部打分项提交、移除脚本。
- [`web/src/scoring`](../../web/src/scoring/ScoringPanel.test.tsx)：键盘打分、改分、悬停缩略图并排对照、新照片没有分数、逐项打分与自动进入下一项和下一张、撤销一步与改上一张、两侧箭头、单击与按住 Z 的 100% 查看、管理员的批次缩略图、按原因的跳过、按项的打分人统计与提醒、导出对话框、打分人开关、每次打开批次的新手教程与试打；[`model.test.ts`](../../web/src/scoring/model.test.ts)：计时、八种 EXIF 方向的转正矩阵、导出的列序。

## 关联

- [照片打分规格](../specs/photo-scoring.md)、[决定记录](../decisions/2026-11-12-photo-scoring.md)
- [ADR 0037](../adr/0037-plug-removable-modules-in-as-separate-services.md)、[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)
