# 决定记录

决定记录保存可以改回来的产品与运营决定：默认值、阈值、范围、优先级，以及几种做法里选哪一种。每篇写清要定的问题、当时的选项、谁在哪天怎样定的、理由是什么；规格只写由此得到的行为，并链接到这里。等用户定的事也写成决定记录，定下后在同一篇补上结论，问题与答案始终放在一起。

## 与 ADR 的分界

同一个决定只写在一处：

1. 改回来要迁移数据、改协议、改部署或改安全边界的，写 [ADR](../adr/README.md)；改回来只是一次普通提交的，写决定记录。拿不准时先写决定记录；后来发现难以逆转，再写 ADR，并把决定记录标为 `superseded` 链接过去。
2. ADR 已覆盖的事只改 ADR：修订或推翻 ADR 中的某项决定，在 ADR 中修订，不另写决定记录。ADR 里已经记下的用户选择留在原处。
3. ADR 是否接受以 ADR 的状态行为准，不写决定记录。`proposed` 的 ADR 与 `pending` 的决定记录一起列在[当前状态](../status/CURRENT.md#待用户决定)的“待用户决定”中。

用户对界面细节的反馈（文案、配色、图标、布局）直接写成规格中的行为，不写决定记录；定默认值、上限、范围或在几种做法中选一种的，才写。

## 索引

按主题列出，同一主题内按日期排列。AI 助手的大部分决定在 [ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 中，网页搜索的在 [ADR 0030](../adr/0030-ship-web-search-as-a-built-in-searxng-service.md) 中。

### 文件与存储

- [2026-10-12：备份中心先只做快照保护](2026-10-12-backup-center-snapshots-only.md)
- [2026-10-13：文件管理的图片缩略图是否改用服务端生成](2026-10-13-file-manager-image-thumbnails.md)
- [2026-10-21：FFmpeg Worker 的槽位、分层与内存上限](2026-10-21-ffmpeg-worker-slots-and-memory.md)

### 相册

- [2026-10-10：AI 标签只以 AI 聚合呈现](2026-10-10-ai-labels-as-groups.md)
- [2026-10-10：物体识别用 D-FINE（Objects365）](2026-10-10-object-detector-d-fine.md)
- [2026-10-10：模型准入只看模型自身的许可](2026-10-10-model-license-rule.md)
- [2026-10-12：AI 聚合每组的正确率门槛](2026-10-12-ai-group-precision.md)
- [2026-10-13：AI 后台任务闲时有多少用多少](2026-10-13-ai-uses-idle-capacity.md)
- [2026-10-15：人物与宠物里程碑的范围](2026-10-15-photo-people-scope.md)
- [2026-10-19：相册放视频，视频不参与 AI](2026-10-19-videos-in-the-photo-library.md)
- [2026-10-19：相册做 OCR](2026-10-19-photo-ocr.md)
- [2026-10-20：相册视频实现时选的默认做法](2026-10-20-photo-video-defaults.md)
- [2026-10-20：相册侧栏的“照片”改名“图库”](2026-10-20-library-sidebar-name.md)
- [2026-10-28：AI 模型不设固定内存预算，加载前留出余量](2026-10-28-ai-memory-headroom.md)
- [2026-10-30：OCR 模型用 Qwen3.5-4B 的 Q4_K_M](2026-10-30-ocr-model.md)
- [2026-10-30：OCR 实施方案中采纳的建议](2026-10-30-ocr-plan-defaults.md)
- [2026-10-30：AI 助手能用照片中的敏感字段，但看不到具体的值](2026-10-30-ocr-assistant-handles.md)
- [2026-10-30：点开完整号码是否记入活动记录](2026-10-30-ocr-full-number-reveal-logging.md)
- [2026-10-30：OCR 敏感字段的清单](2026-10-30-ocr-sensitive-fields.md)
- [2026-11-02：OCR 分流的目标召回取 99%](2026-11-02-ocr-routing-recall.md)
- [2026-11-03：OCR 字段核对的单字概率取 0.99](2026-11-03-ocr-character-confidence.md)
- [2026-11-06：照片中的文字在界面上不遮挡](2026-11-06-ocr-no-masking-in-the-ui.md)

### AI 助手

- [2026-10-15：助手的回答提到 Hermes 的 `/help` 与上手引导怎么处理](2026-10-15-assistant-help-and-onboarding.md)
- [2026-10-16：AI 助手在界面上叫 VNAS](2026-10-16-assistant-name-vnas.md)
- [2026-10-20：助手说“下次从笔记接着聊”的措辞是否调整](2026-10-20-assistant-notes-wording.md)

### 桌面与设置

- [2026-10-08：开机动画的风格](2026-10-08-boot-ident-style.md)
- [2026-10-20：程序坞图标悬停时显示窗口缩略图](2026-10-20-dock-hover-preview.md)
- [2026-10-22：活动记录保留多久](2026-10-22-activity-record-retention.md)
- [2026-10-30：Web 桌面首屏 JavaScript 上限放宽到 200 kB](2026-10-30-desktop-js-budget.md)
- [2026-10-30：不调节风扇转速](2026-10-30-no-fan-control.md)

### 虚拟伙伴

- [2026-11-06：虚拟伙伴（大肥鱼）V1 的形象、实现方式与 VNAS 的控制](2026-11-06-companion-v1.md)

### 家庭中心

- [2026-10-29：家庭中心规格中补充的默认规则](2026-10-29-family-center-default-rules.md)

### 网页搜索

- [2026-11-02：网页搜索像人一样询问引擎](2026-11-02-web-search-pacing.md)

## 状态

每篇的状态只写在标题下的“状态：”一行，索引不重复。状态以下列词开头：

- `pending`：等用户定。
- `decided`：已定，括号中写日期与决定人，例如 `decided（2026-11-03，用户）`。
- `superseded`：被后来的决定或 ADR 取代，链接替代项。

`make docs-check` 检查：索引收录每篇决定记录且不带状态；状态行以上述词开头，`decided` 带日期；当前状态的“待用户决定”链接每篇 `pending` 的决定与每篇 `proposed` 的 ADR，不链接已定或被取代的决定。

## 使用方式

从 [`_TEMPLATE.md`](_TEMPLATE.md) 创建 `YYYY-MM-DD-short-title.md`，日期取提出问题的那天，在索引的对应主题下加一行，`pending` 的同时在当前状态的“待用户决定”中加一行链接。用户定下后，把状态改为 `decided`，补上“决定”一节，从“待用户决定”中删去这一行，并把落实它的规格、代码或 PR 链接进来。之后改了主意，新写一篇，把旧的标为 `superseded` 并链接新的，不改写旧结论。
