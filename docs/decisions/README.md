# 决定记录

决定记录保存可以改回来的产品与运营决定：默认值、阈值、范围、优先级，以及几种做法里选哪一种。每篇写清要定的问题、当时的选项、谁在哪天怎样定的、理由是什么；规格只写由此得到的行为，并链接到这里。等用户定的事也写成决定记录，定下后在同一篇补上结论，问题与答案始终放在一起。

## 与 ADR 的分界

同一个决定只写在一处：

1. 改回来要迁移数据、改协议、改部署或改安全边界的，写 [ADR](../adr/README.md)；改回来只是一次普通提交的，写决定记录。拿不准时先写决定记录；后来发现难以逆转，再写 ADR，并把决定记录标为 `superseded` 链接过去。
2. ADR 已覆盖的事只改 ADR：修订或推翻 ADR 中的某项决定，在 ADR 中修订，不另写决定记录。ADR 里已经记下的用户选择留在原处。
3. ADR 是否接受以 ADR 的状态行为准，不写决定记录。等用户定的事在两个索引里看：下面的索引中标 `（pending）` 的决定记录，[ADR 索引](../adr/README.md#索引)中标 `（proposed）` 的 ADR；[当前状态](../status/CURRENT.md#待用户决定)只链接这两个索引，不逐条列出。

用户对界面细节的反馈（文案、配色、图标、布局）直接写成规格中的行为，不写决定记录；定默认值、上限、范围或在几种做法中选一种的，才写。

## 索引

按主题列出，同一主题内按日期排列；等用户定的条目后面标 `（pending）`。AI 助手的大部分决定在 [ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 中，网页搜索的在 [ADR 0030](../adr/0030-ship-web-search-as-a-built-in-searxng-service.md) 中。

### 文件与存储

- [2026-10-12：备份中心先只做快照保护](2026-10-12-backup-center-snapshots-only.md)
- [2026-10-13：文件管理的图片缩略图是否改用服务端生成](2026-10-13-file-manager-image-thumbnails.md)（pending）
- [2026-10-21：FFmpeg Worker 的槽位、分层与内存上限](2026-10-21-ffmpeg-worker-slots-and-memory.md)（pending）
- [2026-11-06：镜像缺盘的开机路径是否做成虚拟机回归测试](2026-11-06-mirror-boot-vm-test.md)（pending）

### 相册

- [2026-10-10：AI 标签只以 AI 聚合呈现](2026-10-10-ai-labels-as-groups.md)
- [2026-10-10：物体识别用 D-FINE（Objects365）](2026-10-10-object-detector-d-fine.md)
- [2026-10-10：模型准入只看模型自身的许可](2026-10-10-model-license-rule.md)
- [2026-10-12：AI 聚合每组的正确率门槛](2026-10-12-ai-group-precision.md)
- [2026-10-13：AI 后台任务闲时有多少用多少](2026-10-13-ai-uses-idle-capacity.md)
- [2026-10-15：人物与宠物里程碑的范围](2026-10-15-photo-people-scope.md)
- [2026-10-19：相册放视频，视频不参与 AI](2026-10-19-videos-in-the-photo-library.md)
- [2026-10-19：相册做 OCR](2026-10-19-photo-ocr.md)
- [2026-10-20：相册视频实现时选的默认做法](2026-10-20-photo-video-defaults.md)（pending）
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
- [2026-11-10：AI 助手看不到证件、票据和文字还没读完的照片的画面](2026-11-10-assistant-view-of-documents.md)
- [2026-11-25：成员在等的后台 AI 工作要不要等相册空闲](2026-11-25-photo-ai-gate-member-work.md)（pending）

### AI 助手

- [2026-10-15：助手的回答提到 Hermes 的 `/help` 与上手引导怎么处理](2026-10-15-assistant-help-and-onboarding.md)（pending）
- [2026-10-16：AI 助手在界面上叫 VNAS](2026-10-16-assistant-name-vnas.md)
- [2026-10-20：助手说“下次从笔记接着聊”的措辞是否调整](2026-10-20-assistant-notes-wording.md)（pending）
- [2026-11-10：Hermes 的固定提交怎么越过 gateway 运行时](2026-11-10-hermes-gateway-runtime.md)
- [2026-11-17：AI 助手 V1 的模型、开启范围、密钥与告知](2026-11-17-assistant-v1-cloud-model.md)

### 桌面与设置

- [2026-10-08：开机动画的风格](2026-10-08-boot-ident-style.md)
- [2026-10-20：程序坞图标悬停时显示窗口缩略图](2026-10-20-dock-hover-preview.md)
- [2026-10-22：活动记录保留多久](2026-10-22-activity-record-retention.md)（pending）
- [2026-10-30：Web 桌面首屏 JavaScript 上限放宽到 200 kB](2026-10-30-desktop-js-budget.md)
- [2026-10-30：不调节风扇转速](2026-10-30-no-fan-control.md)
- [2026-11-16：重新启用与删除账号的做法](2026-11-16-account-deletion.md)

### 虚拟伙伴

- [2026-11-06：虚拟伙伴（大肥鱼）V1 的形象、实现方式与 VNAS 的控制](2026-11-06-companion-v1.md)
- [2026-11-06：虚拟伙伴 V1 是否不含待办与专注计时](2026-11-06-companion-v1-no-todo-focus.md)（pending）
- [2026-11-06：虚拟伙伴旧分支中的第三方素材怎么清除](2026-11-06-companion-old-branches.md)（pending）

### 家庭中心

- [2026-10-29：家庭中心规格中补充的默认规则](2026-10-29-family-center-default-rules.md)（pending）

### 剪辑中心

- [2026-11-17：剪辑中心首版的数据归属与几项做法](2026-11-17-montage-center-first-version.md)
- [2026-11-17：剪辑中心方案中补充的默认做法](2026-11-17-montage-center-default-rules.md)
- [2026-11-19：剪辑中心二次编辑起草时选的两项默认做法](2026-11-19-montage-edit-defaults.md)
- [2026-11-20：剪辑中心的规划怎样交给 VNAS](2026-11-20-montage-planning-as-a-vnas-session.md)

### 家庭待办

- [2026-11-16：家庭待办是不是本体能力、按什么分范围、先做哪一步](2026-11-16-family-todos-first-step.md)
- [2026-11-17：家庭待办规格中补充的默认规则](2026-11-17-family-todos-default-rules.md)（pending）

### 网页搜索

- [2026-11-02：网页搜索像人一样询问引擎](2026-11-02-web-search-pacing.md)

### 网络与访问

- [2026-11-09：第一版先做家庭局域网内访问](2026-11-09-home-network-first.md)
- [2026-11-19：宿主机网络与手机直连热点的首版范围](2026-11-19-host-network-and-phone-hotspot-v1.md)

### 设备分档

- [2026-11-18：设备分档与每档的最低规格](2026-11-18-device-tiers.md)
- [2026-11-18：处理器“不低于参考机”用什么衡量](2026-11-18-tier-cpu-comparison.md)（pending）
- [2026-11-18：本机型不支持的功能在界面上隐藏还是标注](2026-11-18-unsupported-feature-display.md)（pending）
- [2026-11-18：除硬件限制外，是否按档位区分功能](2026-11-18-deliberate-tiering.md)（pending）
- [2026-11-18：低端设备上的 AI 功能：慢但完整还是关闭](2026-11-18-ai-on-low-tier.md)（pending）

### 发布与更新

- [2026-11-11：发布用的签名密钥由谁、怎样保管](2026-11-11-release-signing-key.md)
- [2026-11-11：软件源与更新文件放在哪里](2026-11-11-update-hosting.md)
- [2026-11-12：系统更新的默认做法](2026-11-12-system-update-defaults.md)
- [2026-11-13：近期以局域网推送发布，GitHub 作备选](2026-11-13-release-by-lan-push.md)
- [2026-11-13：设备镜像的更新来源、装机方式与 Secure Boot](2026-11-13-device-image-defaults.md)

### 照片打分

- [2026-11-12：照片打分用什么做、放在哪里](2026-11-12-photo-scoring.md)

### 仓库与版权

- [2026-11-17：仓库保持公开，声明保留所有权利](2026-11-17-all-rights-reserved.md)
- [2026-11-17：版权人与贡献者授权](2026-11-17-copyright-holder-and-contributor-grants.md)（pending）

## 状态

每篇的状态写在标题下的“状态：”一行。索引只给 `pending` 的条目加标记，其他状态不在索引中重复。状态以下列词开头：

- `pending`：等用户定。
- `decided`：已定，括号中写日期与决定人，例如 `decided（2026-11-03，用户）`。
- `superseded`：被后来的决定或 ADR 取代，链接替代项。

`make docs-check` 检查：索引收录每篇决定记录；状态行以上述词开头，`decided` 带日期；索引中 `pending` 的条目都标 `（pending）`，已定或被取代的条目不带标记；当前状态的“待用户决定”链接本索引与 ADR 索引，不逐条链接决定记录或 ADR。

## 使用方式

从 [`_TEMPLATE.md`](_TEMPLATE.md) 创建 `YYYY-MM-DD-short-title.md`，日期取提出问题的那天，在索引的对应主题下加一行，`pending` 的在行尾标 `（pending）`。用户定下后，把状态改为 `decided`，补上“决定”一节，去掉索引中的标记，并把落实它的规格、代码或 PR 链接进来。之后改了主意，新写一篇，把旧的标为 `superseded` 并链接新的，不改写旧结论。
