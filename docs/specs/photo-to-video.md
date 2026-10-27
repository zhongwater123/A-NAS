# 照片剪辑成视频

状态：draft（第一阶段为 MCP 演示，尚未实现；[#283](https://github.com/zhongwater123/A-NAS/issues/283)）
更新时间：2026-10-27

## 目标

成员用一句话说明想要的视频，例如“把海边的照片做成 20 秒的视频”，系统就从本人可见的图库里检索相关照片，按顺序拼接成一段视频。

本规格先定义第一阶段：一个 **MCP 演示**，用最小的代价把“智能体 → 工具 → 相册检索 → 视频拼接”这条链路接通，为后续的视频片段检索与剪辑计划打基础。依据见[CutClaw 调研](../research/video-editing-cutclaw.md)与[FilmAgent 调研](../research/video-editing-filmagent.md)。

第一阶段交付后，开发者可以：

- 在支持 MCP 的客户端（第一阶段用 Claude Code）里用中文提出要求，由模型自己调用检索与拼接工具；
- 得到一段由检索到的照片拼成的 MP4，并能复现、比较不同指令的结果。

## 非目标

第一阶段不做：

- 视频素材：不检索视频片段，也不把视频剪进成片。视频片段索引是后续阶段的工作，向量存储选型见 [#262](https://github.com/zhongwater123/A-NAS/issues/262)。
- 剪辑计划、校验与预览界面，以及成员在相册窗口或 VNAS 中使用的入口。
- 写回图库：成片只保存在开发机的输出目录，不成为照片资产。
- 配乐、节拍对齐、镜头运动、字幕，以及淡入淡出之外的转场与特效。
- 按时间、地点过滤：现有搜索接口不支持，查询中的时间描述只参与向量排序。
- 部署到 Experimental NAS 或随发行制品安装；演示只在开发机运行。

## 第一阶段的组成

```
开发者 ──► Claude Code（MCP 客户端 + 云端模型）
              │ MCP（stdio，本机子进程）
              ▼
           演示 MCP 服务器（开发机，Python，官方 MCP SDK）
              ├─ search_photos ──► GET /api/v1/photos/search
              └─ make_slideshow ──► GET /api/v1/photos/assets/{assetId}（逐张核对）
                                    → GET /api/v1/photos/assets/{assetId}/view → FFmpeg → MP4
```

- **与助手 MCP 工具的关系**：VNAS 已经经 A-NAS 的 MCP 工具只读访问相册，其中有 `search_photos` 与看照片画面的 `look_at_photo`（[`anas_mcp.py`](../../ai/assistant-spike/anas_mcp.py)，[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 决定 6）。那个服务器只在助手沙箱里运行，经相册服务的助手套接字按成员 UID 认人，开发机上的 Claude Code 连不到它。演示服务器因此改经产品服务的 HTTP 接口访问相册，`search_photos` 沿用同名工具的参数与返回形状，新增的只有 `make_slideshow`。
- **MCP 服务器只做转接**：检索、授权与排序都留在相册服务（[`internal/photos/search.go`](../../internal/photos/search.go)），服务器不读 Catalog、向量或受管存储。
- **身份**：服务器以一个测试账号登录产品服务（`/api/v1/session`），之后的每次调用都以这个账号的身份进行，结果按相册 Policy 过滤；会话过期（收到 401）时重新登录一次。连接地址与测试账号由环境变量提供，不写进仓库。这是演示阶段的简化：它持有的是一个完整的成员会话，而产品中的助手只有相册服务按 UID 构造的只读助手身份（[ADR 0011](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md) 决定 4、ADR 0024 决定 6），后续阶段不沿用这种做法。
- **取画面用照片的视图**：`viewPhoto` 返回从像素重新编码的 JPEG，已按方向转正，最长边不超过 1600 像素，不带原图的元数据；视频没有视图。`previewPhoto` 对 JPEG 与 PNG 返回未转正的原图，对视频返回封面帧，不适合拼接。
- **拼接在服务器进程里调用 FFmpeg**：这是演示阶段的简化。产品中渲染交给媒体 Worker，以使用它的排队与资源上限。

### 工具

| 工具 | 参数 | 返回 |
|---|---|---|
| `search_photos` | `query`（1–200 字符，可包含人物全名，如“小明 海边”）、`page`（上一次结果中的 `next`） | 同助手的 `search_photos`：每页至多 30 项，每项有照片资产 ID、名称、所在图库（我的／共享）、拍摄时间、本人加的标签与相册；视频另标 `video`；最接近的一组有几张（`closest`）；只按名称与用户标签匹配时附说明；还有下一页时给出 `next` |
| `make_slideshow` | `asset_ids`（按出现顺序，1–60 个）、`seconds_per_photo`（每张的时长，1–10 秒，可带小数，默认 3）、`crossfade`（是否淡入淡出，默认否） | 输出文件路径、总时长、实际用到的照片数 |

- 成片为 1920×1080、30 fps 的 H.264 MP4，没有音轨；每张照片等比缩放到画面内并补黑边。
- 总时长始终是张数 × `seconds_per_photo`。淡入淡出时相邻两张重叠 0.5 秒，重叠时间由前一张多显示的部分承担，不缩短总时长。

工具说明写明各自的用途与限制，模型据此决定何时调用；返回内容不含照片的画面、存储路径或拍摄地点。

## 场景

### 检索照片

- Given：Claude Code 已登记演示服务器，测试账号的图库里有照片，本地 AI 已为它们生成向量。
- When：开发者说“找几张海边的照片”。
- Then：模型调用 `search_photos`，得到测试账号可见、未进回收站的照片，排序与相册窗口的搜索框一致；回复中列出名称与拍摄时间。

### 用检索结果拼成视频

- Given：上一步已经找到照片。
- When：开发者说“用前 6 张做成视频，每张 3 秒”。
- Then：模型调用 `make_slideshow`；服务器取这些照片的视图，按给出的顺序拼接，写出 18 秒的 MP4，返回文件路径与时长。

### 一句话完成

- When：开发者说“把海边的照片做成 20 秒的视频”。
- Then：模型先检索，再选定张数与每张时长，使两者之积为 20 秒，然后调用 `make_slideshow`；两次调用都能在 Claude Code 的过程记录中看到。

### 搜不到合适的照片

- Given：图库里没有与查询相关的照片，或本地 AI 不可用。
- When：模型检索后得到空结果，或结果只按名称与用户标签匹配（相册返回的 `semantic` 为 false）。
- Then：工具如实返回这一情况，模型告诉开发者或换说法重试，不用无关照片凑数。

### 传入视频资产

- When：`asset_ids` 中有视频资产（按名称搜索时，成员身份的结果里可能有视频）。
- Then：`make_slideshow` 拒绝这次调用，说明哪些 ID 是视频；第一阶段只拼接照片。

## 边界与失败

- **核对资产**：拼接前服务器逐个读取资产（`getPhotoAsset`），任何一项不合格就拒绝整次拼接，并列出不合格的 ID 与原因；不跳过、不部分拼接。
- **权限**：只能看到测试账号自己的私有图库与共享图库；不支持管理员查看模式。看不到的资产 ID 按不存在处理。回收站里的资产按不存在处理：相册服务仍会把它们返回给能修改它们的人（私有图库的主人，共享图库中的上传者与管理员），所以由服务器按资产的 `trash` 字段拒绝。
- **参数越界**：照片数、单张时长超出上表范围时拒绝，并给出允许的范围，不静默截断。
- **相册或产品服务不可用**：返回 `photos_unavailable` 等原始错误代码与一句说明，不重试到超时。
- **FFmpeg 缺失或拼接失败**：返回失败原因；未写完的输出文件删除。
- **输出位置**：只写入配置的输出目录，文件名由服务器生成，不覆盖已有文件。
- **隐私**：Claude Code 使用云端模型，工具返回的名称、拍摄时间、标签与相册名会发给模型服务商；照片画面只交给本机的 FFmpeg，不发给模型。演示只用可丢弃的测试照片，不连接保存真实家庭数据的设备，与 [AI 助手](ai-assistant.md)对云端模型的限制一致。
- **Fake 预览栈**：默认没有本地 AI，搜索只按名称与用户标签匹配。要接通语义检索，另起 `python3 -m anas_ai.worker --fake --socket <路径>`，并在启动产品服务时用 `ANAS_AI_SOCKET` 指向它；Fake Provider 的向量由输入哈希得到，检索结果与查询内容无关，只能用来验证链路是否接通，检索效果要在带真实模型的环境中看。HEIC 照片的视图还需要媒体工具，见 [fake-preview 技能](../../.claude/skills/fake-preview/SKILL.md)。

## 待决定

- 后续阶段的入口：把 `make_slideshow` 加进 A-NAS 的 MCP 工具，让 VNAS 直接使用。要决定拼接在哪里运行（助手沙箱内，或交给媒体 Worker）、成片写到哪里（助手目录可写，图库不可写），以及剪视频片段时怎样授权：助手身份目前看不到视频资产。
- 演示代码放在仓库的什么位置（建议在 `ai/` 下，与 `anas_mcp.py` 共用相册请求与结果整理的代码，沿用 Ruff 与测试），以及它是否随后演变为产品代码。
- 检索效果用哪个环境验收：Experimental NAS 上的测试账号，或开发机上带真实模型的本地环境。

## 验收证据

- 工具层单元测试：参数到接口的转换、范围校验、视频资产、不可见资产与回收站资产的拒绝、错误代码的转换（用假的产品服务响应）。
- 拼接测试：用几张横竖不同的测试图片生成视频，用 `ffprobe` 核对分辨率为 1920×1080、时长与张数 × 每张时长相差不超过 0.1 秒，开启与关闭淡入淡出各一次。
- 端到端：在 Claude Code 中分别跑通“检索”“拼接”“一句话完成”三个场景。Fake 预览栈只证明链路接通；在带真实模型的环境中，用一组固定的中文查询记录检索结果与成片，作为后续优化的基线。

## 关联

- Issue：[#283](https://github.com/zhongwater123/A-NAS/issues/283)
- 架构：[相册本地 AI](../architecture/photo-ai.md)、[相册技术设计](../architecture/photo-library.md)
- ADR：[0011 相册服务的专用身份](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[0024 每个成员一个沙箱化的 Hermes 智能体](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
- 规格：[相册与本地智能检索](photo-library.md)、[相册视频](photo-video.md)、[AI 助手](ai-assistant.md)
- 研究：[CutClaw 调研](../research/video-editing-cutclaw.md)、[FilmAgent 调研](../research/video-editing-filmagent.md)
- 接口：[相册接口契约](../../api/paths/photos.yaml)（`searchPhotos`、`getPhotoAsset`、`viewPhoto`）
- 代码：[相册搜索](../../internal/photos/search.go)、[照片视图](../../internal/photos/view.go)、[资产可见性](../../internal/photos/assets.go)、[助手的相册 MCP 工具](../../ai/assistant-spike/anas_mcp.py)
- 测试：待实现
