# 墨水屏第一批怎么接入 A-NAS

状态：pending
更新时间：2026-11-27

## 问题

水墨屏原型已经在自己的服务端上跑通了待办屏、纸条等功能，真屏的固件也能定时醒来拉图、整机关电（[#503](https://github.com/zhongwater123/A-NAS/issues/503)、[#590](https://github.com/zhongwater123/A-NAS/issues/590)）。维护者问这些什么时候能接进 NAS 成为正式功能。接进来要先定几件实现前就绕不开的事：屏凭什么认出自己、令牌放在哪、图在哪画、纸条从哪写，以及分几批、先做哪批。行为写在[墨水屏规格](../specs/eink-screens.md)。

## 已定（2026-11-27，用户）

- **在 A-NAS 里正式实现**，不把原型服务部署到 NAS 上凑合用。原型的 Python 代码不带过来，按规则用 Go 重写，测试照搬场景（与 [#503](https://github.com/zhongwater123/A-NAS/issues/503) 第 3 节一致）。
- **第一批只做三样**：配对一块屏（设备令牌）、客厅待办屏、纸条。
- **先提规格的 PR**，应用窗口、设置页等界面细节随实现的 PR 跟上。

## 待定

### 1. 设备令牌放在哪、由谁核验

- **A. 放在家庭服务的 `family.db`，由家庭服务自己核验**（建议）：屏、纸条、待办都在家庭服务里，令牌只认出“是哪块屏”，不属于任何账号，所以不需要向 Host Agent 查账号状态；管理员移除屏就是删掉这一行。产品服务只负责按 `device` 姿态转发并限流。代价：多一种令牌，存储与核验的代码不与 Agent令牌共用。
- **B. 照 Agent令牌放进 `control.db`，由 Host Agent 解析**（[ADR 0040](../adr/0040-let-members-agents-edit-shared-pages-through-agent-tokens.md) 决定 3）：所有不是会话的令牌在一处管理。代价：令牌要挂在某个账号（配对的管理员）下面，这个管理员停用或重置密码时屏会一起失效；Host Agent 要多开一个查询。

选 B，或者维护者认为这属于难以逆转的身份边界，就把这一项改写成 ADR，本篇标为 `superseded`。

### 2. 路由姿态

- **A. 新增第六种姿态 `device`**（建议）：只认 `Authorization: Bearer`，不认 Cookie、不要 CSRF 令牌，带 `Origin` 头一律拒绝；只挂三条 `GET`。
- **B. 复用 `agent` 姿态**：少一种姿态，但 `agent` 的令牌来自 `control.db`，两种令牌混在一个姿态下，核验路径要分叉。

### 3. 图在哪画

- **A. 在家庭服务里用 Go 画**（建议）：用已有依赖 `golang.org/x/image` 排字，800×480 的 1 位图很小，不需要 AI 服务在线（AI 服务停了屏照常更新）。要内嵌一份中文字体：建议 Noto Sans SC（SIL OFL 1.1）按常用字子集化，下载前说明来源与大小。
- **B. 交给 AI Worker（Python）画**：原型的 Pillow 代码可以直接搬，但屏会跟着 AI 服务停，Worker 也不该持有家庭数据。

### 4. 纸条从哪写

- **A. 待办窗口里加“留纸条”**（建议）：纸条和待办都是“家里要知道的事”，范围相同（所有有效账号），不多开一个窗口。
- **B. 家庭中心里**：家庭中心的范围是家人，与待办、纸条的“所有有效账号”不同。
- **C. 新的“墨水屏”窗口**：以后的购物清单、岗位开关可以放在一起，但第一批只有纸条，窗口太空。

### 5. 排期

建议按下面的顺序，每批一个 PR、各步骤单独提交，第一个合入的就从“配对”走到“屏上出现待办”（[跨组件的功能](../../CONTRIBUTING.md#跨组件的功能)）：

| 批次 | 内容 | 预计 |
|---|---|---|
| 规格 | 本篇与[规格](../specs/eink-screens.md) | 2026-11-27 |
| 第一批 | 设备令牌与 `device` 姿态、三条设备接口、刷新规则、待办屏出图、设置页配对；纸条与它的写入入口；固件改成新路径并带令牌头 | 规格定下后约一周提 PR，随后在实验 NAS 上配对真屏验收 |
| 第二批 | 屏幕岗位：玄关（今天明天要办的、垃圾日、车务）、冰箱（购物清单）；早上的“今天”一页；温湿度 | 第一批合入后 |
| 第三批 | 宝宝的今天（等 [#630](https://github.com/zhongwater123/A-NAS/issues/630)）、一句话调屏、VNAS 调屏、局部刷新、低电量 | 依赖定下后 |

### 6. 第一批的默认值

照原型，写在[规格的“第一批的默认规则”](../specs/eink-screens.md#第一批的默认规则)：白天 15 分钟来问一次；夜里 22:00–7:00 不刷；上屏的提前天数（证件 60、合同 45、其他 30）；最多 3 行；医疗类只写类别；纸条最多两行、贴 24 小时、写给所有屏的看过一处就都收起；屏名 1–6 个字。选项是全部保留，或逐项修改。

## 决定

待定。

## 关联

- [墨水屏规格](../specs/eink-screens.md)、[家庭待办规格](../specs/family-todos.md)
- [ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)、[ADR 0029](../adr/0029-keep-the-baby-as-a-profile-and-channel-photos-in-the-shared-library.md)、[ADR 0040](../adr/0040-let-members-agents-edit-shared-pages-through-agent-tokens.md)
- [#503](https://github.com/zhongwater123/A-NAS/issues/503)、[#590](https://github.com/zhongwater123/A-NAS/issues/590)、[#630](https://github.com/zhongwater123/A-NAS/issues/630)
