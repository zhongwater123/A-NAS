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
- 剪辑计划、校验与预览界面；成员在相册窗口或 VNAS 中使用。
- 写回图库：成片只保存在开发机的输出目录，不成为照片资产。
- 配乐、节拍对齐、镜头运动、字幕与转场之外的特效。
- 按时间、地点过滤：现有搜索接口不支持，查询中的时间描述只参与向量排序。
- 部署到 Experimental NAS 或随发行制品安装；演示只在开发机运行。

## 第一阶段的组成

```
开发者 ──► Claude Code（MCP 客户端 + 云端模型）
              │ MCP（stdio，本机子进程）
              ▼
           A-NAS 媒体 MCP 服务器（新写，Python，官方 MCP SDK）
              ├─ search_photos ──► GET /api/v1/photos/search
              └─ make_slideshow ──► GET /api/v1/photos/assets/{assetId}/preview → FFmpeg → MP4
```

- **MCP 服务器只做转接**：检索、授权与排序都留在相册服务（[`internal/photos/search.go`](../../internal/photos/search.go)），服务器不读 Catalog、向量或受管存储。
- **身份**：服务器以一个测试账号登录产品服务（`/api/v1/session`），之后的每次调用都以这个账号的身份进行，结果按相册 Policy 过滤。连接地址与测试账号由环境变量提供，不写进仓库。
- **拼接在服务器进程里调用 FFmpeg**：这是演示阶段的简化。产品中渲染交给媒体 Worker，以使用它的排队与资源上限。

### 工具

| 工具 | 参数 | 返回 |
|---|---|---|
| `search_photos` | `query`（1–200 字符，可包含人物全名，如“小明 海边”）、`limit`（默认 20，最多 50） | 每项的照片资产 ID、名称、拍摄时间与类型；是否按语义排序；最接近的一组有几张 |
| `make_slideshow` | `asset_ids`（按出现顺序，1–60 个）、`seconds_per_photo`（默认 3，1–10）、`crossfade`（是否淡入淡出，默认否） | 输出文件路径、总时长、实际用到的照片数 |

工具说明写明各自的用途与限制，模型据此决定何时调用；返回内容不含原图、文件路径或拍摄地点。

## 场景

### 检索照片

- Given：Claude Code 已登记演示服务器，测试账号的图库里有照片，本地 AI 已为它们生成向量。
- When：开发者说“找几张海边的照片”。
- Then：模型调用 `search_photos`，得到测试账号可见、未进回收站的照片，排序与相册窗口的搜索框一致；回复中列出名称与拍摄时间。

### 用检索结果拼成视频

- Given：上一步已经找到照片。
- When：开发者说“用前 6 张做成视频，每张 3 秒”。
- Then：模型调用 `make_slideshow`；服务器取这些照片的预览图，统一缩放并补边到 1920×1080，按给出的顺序拼接，写出约 18 秒的 H.264 MP4，返回文件路径与时长。

### 一句话完成

- When：开发者说“把海边的照片做成 20 秒的视频”。
- Then：模型先检索、再按 20 秒算出张数与每张时长并调用 `make_slideshow`，两次调用都能在 Claude Code 的过程记录中看到。

### 搜不到合适的照片

- Given：图库里没有与查询相关的照片，或本地 AI 不可用。
- When：模型检索后得到空结果，或结果只按名称与用户标签匹配（`semantic` 为 false）。
- Then：工具如实返回这一情况，模型告诉开发者或换说法重试，不用无关照片凑数。

### 传入视频资产

- When：`asset_ids` 中有视频资产。
- Then：`make_slideshow` 拒绝这次调用，说明哪些 ID 是视频；第一阶段只拼接照片。

## 边界与失败

- **权限**：只能看到测试账号自己的私有图库与共享图库；不支持管理员查看模式。别人的或已删除的资产 ID 按不存在处理，整次拼接被拒绝并列出这些 ID。
- **参数越界**：照片数、单张时长超出上表范围时拒绝，并给出允许的范围，不静默截断。
- **相册或产品服务不可用**：返回 `photos_unavailable` 等原始错误代码与一句说明，不重试到超时。
- **FFmpeg 缺失或拼接失败**：返回失败原因；未写完的输出文件删除。
- **输出位置**：只写入配置的输出目录，文件名由服务器生成，不覆盖已有文件。
- **隐私**：Claude Code 使用云端模型，工具返回的名称与拍摄时间会发给模型服务商。演示只用可丢弃的测试照片，不连接保存真实家庭数据的设备，与 [AI 助手](ai-assistant.md)对云端模型的限制一致。
- **Fake 预览栈**：Fake Provider 的向量由输入哈希得到，检索结果与查询内容无关，只能用来验证链路是否接通；检索效果要在带真实模型的环境中看。

## 待决定

- 后续阶段的客户端：VNAS（Hermes）能否作为 MCP 客户端，还是由产品服务提供同样的工具。
- 演示代码放在仓库的什么位置（建议在 `ai/` 下，沿用 Ruff 与测试），以及它是否随后演变为产品代码。
- 检索效果用哪个环境验收：Experimental NAS 上的测试账号，或开发机上带真实模型的本地环境。

## 验收证据

- 工具层单元测试：参数到接口的转换、范围校验、视频资产与不可见资产的拒绝、错误代码的转换（用假的产品服务响应）。
- 拼接测试：用几张测试图片生成视频，用 `ffprobe` 核对分辨率与时长。
- 端到端：在 Claude Code 中分别跑通“检索”“拼接”“一句话完成”三个场景。Fake 预览栈只证明链路接通；在带真实模型的环境中，用一组固定的中文查询记录检索结果与成片，作为后续优化的基线。

## 关联

- Issue：[#283](https://github.com/zhongwater123/A-NAS/issues/283)
- 架构：[相册本地 AI](../architecture/photo-ai.md)、[相册技术设计](../architecture/photo-library.md)
- ADR：[0011 相册服务的专用身份](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[0024 每个成员一个沙箱化的 Hermes 智能体](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)
- 规格：[相册与本地智能检索](photo-library.md)、[相册视频](photo-video.md)、[AI 助手](ai-assistant.md)
- 研究：[CutClaw 调研](../research/video-editing-cutclaw.md)、[FilmAgent 调研](../research/video-editing-filmagent.md)
- 接口：[相册接口契约](../../api/paths/photos.yaml)（`searchPhotos`、`previewPhoto`）
- 代码：[相册搜索](../../internal/photos/search.go)
- 测试：待实现
