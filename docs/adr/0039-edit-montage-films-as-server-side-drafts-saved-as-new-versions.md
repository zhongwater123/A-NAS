# 0039：剪辑中心出片后的二次编辑存为服务端的修改稿，改完另存新版本

状态：proposed（2026-11-19 起草；第 5、7、8、10 项由用户 2026-11-19 定下，其余是起草时的默认做法，等用户确认；修订[剪辑中心规格](../specs/montage-center.md#非目标)的非目标“挑一版、快捷微调、精修”）

剪辑中心出了方案一、方案二两条成片之后，成员要能在成片上直接改：确认一版、按音乐槽换镜头，或者说一句话让 VNAS 重剪（[#596](https://github.com/zhongwater123/A-NAS/issues/596)，父 issue [#514](https://github.com/zhongwater123/A-NAS/issues/514)）。第八版网页原型已经做出了这套交互，但修改只存在浏览器里，“重剪”“修改后的渲染”“预览”都是模拟的。

我们决定：
- 修改以“修改稿”的形式存在剪辑中心的服务端，按音乐槽记录每个槽用哪个节拍；
- 预览就是真实渲染，满意后把这次渲染另存为相册里的新版本；
- 只有“说一句话重剪”调用 VNAS、占规划授权的轮数；
- 文字搜镜头用相册的剪辑窗口向量。

这些选择牵涉剪辑中心的数据表、任务种类、相册授权套接字的新操作和窗口向量集合的第一个读者，改回来都要迁移数据或改协议，所以写成 ADR。

## 背景

- **原型**：第八版的[网页原型](https://github.com/zhongwater123/A-NAS/blob/feat/video-montage-v5/ai/montage_v8/web/index.html)（#454 的分支，只作参考）。选版页 `#/choose` 每版下面有“修改镜头”“确认这一版”和一句话的“重剪”；修改页 `#/edit/<方案>` 按音乐段落排出镜头，可以替换、拖动、删除、锁定、重洗、撤销、预览、导出镜头清单。说明见[第七版研究](https://github.com/zhongwater123/A-NAS/blob/feat/video-montage-v5/docs/research/video-montage-v7.md)：换镜头与锁定存在浏览器的 `localStorage`，预览按槽依次播原片片段，重剪只放 3 秒动画。
- **首版**：[实施方案](../architecture/montage-center.md#首版范围)把精修、挑一版和说一句重剪都列为延后，理由是要在服务端保存编辑记录并让修改进入导出，文字搜镜头还要窗口向量的检索。
- **main 上的代码**（`99446cf5`，2026-11-19 核对）：
  - 时间线是 `fill`/`fill-N` 步骤的结果 `{clips, known}`（`internal/montage/render.go`）。音乐槽按“段落 → 画面 → 小节槽”嵌套，长度与切点来自配乐分析；一个槽由选中的节拍填，不够长时由 `fill` 按规则补上别的片段，所以一个槽可能有几段。
  - 每个画面有第一步规划写的画面描述 `query`，第二步答案还多列了一两个备用节拍；`fill` 把 `query` 抄进每个镜头，备用节拍只留在 `shots` 步骤的结果里。
  - 渲染按镜头逐段 `segment`，再 `assemble` 配乐（`renderFilm`）；存回的版本键是任务 ID 加渲染键，版本号在项目内递增（`save`）。
  - `jobs` 没有种类，每个任务都跑完整的生成步骤。
  - 相册的剪辑分析不保留关键帧图片；授权套接字上没有搜索。文字编码是 AI Worker 的 `embed_query`，与窗口向量同一个模型（EmbeddingGemma 2）。

## 决定

1. **选版页的三个操作**。两个方案并排的每一条成片下面有“确认这一版”“修改镜头”和一个说一句话的输入框加“重剪”。
   - **确认这一版**：在项目上记下选定的版本（`projects.chosen_version_id`），项目列表与主页以它为这个项目的成片，另一条收进“其他版本”。不删除任何成片，另一条照规格由成员在相册里删；可以改选。（起草时的默认做法）
   - 修改镜头、重剪见下面几项。任何版本（包括更早的、改过的）都可以修改或重剪。

2. **修改的单位是音乐槽，槽的长度与切点不动**。修改稿记录每个槽钉住哪张卡片的哪个节拍、是否锁定；不直接改时间线里的帧。
   - **重新填槽**：渲染前由计算 Worker 的 `fill` 按修改稿重算时间线。`fill` 增加输入 `pins`（槽 → 卡片与节拍）：钉住的槽首段就用这个节拍，不够长时仍按原规则放慢或补段，无遮挡区间照旧经重入取帧。不带 `pins` 时结果与现在逐个相同，第八版的黄金比对不受影响。
   - **换镜头**：候选依次是同一张卡片里还没用到的其他节拍、这个画面在第二步答案里的备用节拍，再往后是文字搜索全部素材（第 8 项）。搜索框默认填这个画面的 `query`，成员可以改。只列卡片够长、按 `fill` 的放慢上限填得满这个槽的节拍。
   - **拖动**：只在同一个段落里换顺序；锁定的镜头不动，其他镜头按新顺序依次占没锁的槽。卡片不够新槽长度时这次拖动不成立，页面说明原因。拖动用成熟的库（例如 dnd-kit），不手写。
   - **删除**：不留空，由前一镜或后一镜延长补上，延长的那一镜的卡片要盖得住两段之和。两边都够时选画面更像的一边：两个节拍的窗口向量均值的余弦，由计算 Worker 的新操作 `likeness` 算。两边都不够时拒绝删除，提示换一个镜头代替。原型还有“放弃踩点、后面整体提前”，不采用：成片时长要等于音乐选段，这是时间线校验的规则。至少留一个镜头。
   - **锁定后重洗**：每个没锁的槽用所在画面的 `query` 搜索，从最像的 4 个里随机挑一个，规则同原型：一个节拍只用一次，不和前一镜同卡片，卡片够长。随机种子记进这一步的修改记录，不调用模型。
   - **还原**：把修改稿恢复成这个版本原来的样子，也是一步可撤销的修改。

3. **修改稿存在剪辑中心的服务端**。
   - 新表 `drafts`（id、owner_user_id、project_id、base_version_id、revision、slots_json、preview_ref、updated_at）与 `draft_revisions`（draft_id、revision、op、slots_json、created_at）。每位成员每个版本一份修改稿，刷新页面、换一台电脑都接着改。
   - 每个操作是一次请求，带 `baseRevision`；服务端按第 2 项的规则校验并应用，写一个新的修订，回答新的修改稿。修订号对不上（例如两个窗口同时改）回答 409，页面重新读取。
   - **撤销**：回到上一个修订，本身也写成新修订；保留最近 100 个修订。
   - 不选原型的浏览器本地存储：#514 要求编辑记录在服务端，修改要能进入导出。

4. **预览就是真实渲染，满意后把它另存**。
   - **预览**：按修改稿走“查素材 → 重新填槽 → 渲染”，用和正式成片相同的 1920×1080 预设，渲染到 `tmp/draft-<id>/`，由剪辑中心按 Range 送给页面播放。
   - **存为新版本**：把这份渲染原样交给 `saveFilm`，不再渲染一遍；修改稿在预览之后又改过，就先重新渲染。也可以不预览直接“渲染并存为新版本”。
   - 预览文件在修改稿再改、存成版本或 7 天没有动静时删除；写之前照常用 `capacity.Admits` 检查空间。
   - **即时的辅助**：镜头与候选的缩略图由剪辑中心经 `open` 加 FFmpeg `frames`（JPEG，长边 320，`Interactive` 优先级）现取，不存盘，靠浏览器缓存（内容对象不可变，可以 `private, immutable`）。单个节拍的试看用相册现有的视频播放，从节拍起点播到终点；`VideoPlayer` 还不支持起止时间，实施时补上。
   - 不选原型的“按槽依次播原片”：它没有放慢、竖拍的模糊垫底、淡出和无遮挡的截取，不是成片的样子。也不选“先出低清预览、确认后再出正式片”：耗时主要在解码原片，低清省不了多少，却要渲染两遍。

5. **改完另存新版本，原版本不动**（用户 2026-11-19 定）。
   - `versions` 增加 `kind`（`plan` 规划出的、`edit` 修改后渲染的、`recut` 重剪出的）、`source_version_id` 与 `draft_revision`。`plan` 列只对 `plan` 版本有意义。
   - 版本号照旧在项目内递增，文件名照旧是“<项目名> v<版本号>.mp4”。修改版本的版本键是修改稿 ID、修订号与渲染键，重试只存一次。
   - 每个版本都留下自己的时间线与音乐槽，所以改过的版本还能再改、能导出镜头清单。
   - 这与规格“已存进相册的方案是定局”一致：要换掉旧的，成员在相册里删。

6. **任务分种类，一个项目同一时刻只跑一个任务**。
   - `jobs` 增加 `kind`：`generate`（现在的生成）、`edit`（修改稿的渲染，结束于预览，或接着存回）、`recut`（第 7 项）。步骤表仍由 `steps()` 一处按种类给出。
   - 项目已有进行中的任务时，“生成”“重剪”“预览”“存为新版本”都回答 409，页面写“等这个任务做完”；修改稿随时可以改。
   - `edit` 任务里素材缺失或进了回收站：照规格停下或暂停，页面标出用到这些素材的槽，由成员换掉后再渲染；`edit` 任务没有“去掉这些继续”。

7. **说一句话重剪：只重剪这一版，只有它占规划授权的轮数**（用户 2026-11-19 定）。
   - `POST /versions/{id}/recut` 建一个 `recut` 任务：素材、歌、起点与是否按拍摄时间都取这个版本的任务快照，只出一个方案，得到一个 `recut` 版本。一句话的上限与规格的“一句话要求”相同（200 字），也过“不支持的要求”的词表。
   - 这一轮的 brief 多带两项：`base`（这个版本收下的大纲与镜头）和 `change`（这句话）。提示词模板升到 `plan-v3.md`，规则是从 `base` 出发、只改这句话要求的地方。修改版本的 `base` 取它所来自的规划；手动改的镜头不带进重剪，页面写明。
   - 这条请求成为 anas-api 在转发前登记规划授权的第五条（另外四条是 `generate`、`replan`、`drop-assets`、`retry-analysis`）：刷新 7 天与 5 轮，这一轮照常计数。
   - 换镜头、搜索、重洗、删除、预览、存为新版本都不调用模型，不需要规划授权，不占轮数。

8. **文字搜镜头用相册的剪辑窗口向量**（用户 2026-11-19 定）。
   - 相册授权套接字新增 `searchWindows(grantId, texts, k)`：相册用自己的 `embedQuery` 编码文字，在窗口集合里按这条授权的内容对象过滤后检索，回答每段文字最像的窗口（资产、窗口序号、分数）。一次可以带多段文字，重洗时没锁的画面一起查。
   - 成员在等结果，所以它走相册的交互查询路径，和照片搜索一样算“有人在用相册”。文字与窗口向量的模型不一致时（换了模型、窗口还没重算）回答明确的错误，页面写“分析模型已更新，重新分析后才能搜索”。
   - 这是窗口集合的第一个读者，所以要满足[向量索引](../architecture/vector-index.md#换模型)的“先改为集合别名”，Qdrant 不可用时照照片搜索退回 Catalog 精确计算。
   - 剪辑中心把窗口换算成节拍：节拍的分数取它覆盖的窗口里最高的一个（同原型），再按第 2 项的规则过滤。分数只用来排序，不设门槛。
   - 不选剪辑中心自己调 AI Worker 编码文字：它在 `a-nas-photos` 组里连得上，但那会绕过相册的准入（[ADR 0038](0038-keep-montage-media-in-the-photo-library.md) 决定 4）。不选等 [#207](https://github.com/zhongwater123/A-NAS/issues/207) 的相册视频分段：范围是全相册，#596 要排在它之后。

9. **导出镜头清单**：`GET /versions/{id}/shots.csv` 与 `GET /drafts/{id}/shots.csv`，用 Go 的 `encoding/csv` 写，UTF-8 带 BOM 以便表格软件直接打开。每个镜头一行：镜号、音乐槽、成片里的起止、段落、资产 ID、拍摄时间、原片里的起止、放慢倍数、是否改过；删掉的槽另列由哪一镜补上。原型导出 JSON，是给开发看的；产品给人看。（起草时的默认做法）

10. **文档**：按用户 2026-11-19 的要求，这次只写本 ADR，不改规格与实施方案，不开 PR。实施第一张票时，把规格的非目标、场景与验收，实施方案的首版范围、数据、接口与交付，以及 [CONTEXT](../../CONTEXT.md) 的术语（修改稿、音乐槽、锁定、重洗、重剪）按本 ADR 改写，并在那里链接回来。三版并排与快捷微调按钮仍不做。

## 接口

挂在现有的 `/api/v1/montage`，约定同[实施方案](../architecture/montage-center.md#接口)：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/versions/{id}/confirm` | 确认这一版 |
| POST | `/versions/{id}/recut` | `{requestKey, change}`：202 与任务；anas-api 先登记规划授权 |
| GET | `/versions/{id}/draft` | 这一版的修改稿，没有就按版本建一份：音乐段落与槽、每槽的节拍与锁定、修订号、能否撤销、预览状态 |
| POST | `/drafts/{id}/ops` | `{baseRevision, op}`：`replace`、`move`、`delete`、`lock`、`unlock`、`shuffle`、`reset`、`undo` |
| GET | `/drafts/{id}/candidates` | `?slot=&q=`：同卡片、备用节拍与搜索结果，标明够不够长 |
| POST | `/drafts/{id}/render` | `{requestKey, save}`：202 与 `edit` 任务；`save` 为真时渲染完直接存回 |
| GET | `/drafts/{id}/preview` | 预览片，支持 Range |
| POST | `/drafts/{id}/save` | `{requestKey}`：把当前修订的预览存为新版本，预览过期时先渲染 |
| GET | `/versions/{id}/shots.csv`、`/drafts/{id}/shots.csv` | 镜头清单 |
| GET | `/projects/{id}/frames/{assetId}` | `?t=` 毫秒：缩略图 |

## 后果

- **数据迁移**：`projects.chosen_version_id`；`jobs.kind`；`versions` 的三列与每个版本的时间线；`drafts` 与 `draft_revisions`。删除项目、删除账号时一并清掉修改稿与预览文件。
- **相册**：授权套接字多一个操作；窗口集合改为经别名读取，并补 Catalog 的退回路径；`leak_test` 覆盖新操作。
- **计算 Worker**：`fill` 的 `pins` 与 `likeness`；黄金比对照旧。
- **规划**：新模板 `plan-v3.md` 与 brief 的 `base`、`change`；文件代理和唤醒服务不变，授权的计数照旧。
- **渲染负载**：预览占用全机 2 个后台 FFmpeg 槽位中剪辑中心的那一个，和生成一样。缩略图用 `Interactive`，会和播放一起排队。
- **网页**：`ProjectView.tsx` 的 `Films` 加三个操作；新增修改页。

## 交付

都在首版闭环（实施方案的 M10）之后，按 #596 拆票：

| 票 | 内容 | 依赖 |
|---|---|---|
| R1 | 规格、实施方案与 CONTEXT 按本 ADR 改写 | 本 ADR 接受 |
| R2 | 相册 `searchWindows`、窗口集合的别名与 Catalog 退回 | R1、M5 |
| R3 | 计算 Worker 的 `pins` 与 `likeness`；修改稿、操作、候选、确认、镜头清单、缩略图 | R1、M8a |
| R4 | `jobs.kind`、`edit` 任务的预览与存回、`versions` 的新列 | R3 |
| R5 | `recut`：模板 `plan-v3.md`、brief 的 `base` 与 `change`、anas-api 的第五条登记 | R4、M9 |
| R6 | 选版页的三个操作与修改页 | R2–R5 |

## 未选择

- **覆盖原版本**：相册里只留一条，但要推翻“已存进相册是定局”，重渲染失败时还得能退回原片。
- **重剪也出两个方案，或重剪不占轮数**：前者耗时与费用翻倍；后者让一句话就能绕过 5 轮的上限。
- **修改存在浏览器**：换浏览器就没了，也进不了导出。
- **按槽依次播原片当预览、低清预览加正式渲染**：见第 4 项。
- **剪辑中心自己编码文字、等 #207**：见第 8 项。

## 关联

- 需求：[#596](https://github.com/zhongwater123/A-NAS/issues/596)、[#514](https://github.com/zhongwater123/A-NAS/issues/514)；原型 PR [#454](https://github.com/zhongwater123/A-NAS/pull/454)
- 规格与方案：[剪辑中心规格](../specs/montage-center.md)、[剪辑中心技术设计](../architecture/montage-center.md)、[向量索引](../architecture/vector-index.md)
- ADR：[0038](0038-keep-montage-media-in-the-photo-library.md)、[0028](0028-index-vectors-in-a-rebuildable-qdrant-instance-per-owner.md)
- 代码：[`internal/montage/render.go`](../../internal/montage/render.go)、[`internal/montage/steps.go`](../../internal/montage/steps.go)、[`ai/anas_montage/domain/fill.py`](../../ai/anas_montage/domain/fill.py)、[`web/src/montage/ProjectView.tsx`](../../web/src/montage/ProjectView.tsx)、[`internal/photos/search.go`](../../internal/photos/search.go)
