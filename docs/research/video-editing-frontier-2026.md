# 自动剪辑前沿调研：给剪辑第四版找优化方向

> 状态：完成（读论文页面、arXiv HTML 与 GitHub README；未在开发机或评测机上复现）  
> 更新日期：2026-11-04  
> 适用范围：2024–2026 年的论文与开源项目，按 [#341](https://github.com/zhongwater123/A-NAS/issues/341) 的评测约束（x86_64、8 CPU、3.4 GiB 内存、无 GPU、无 AVX-512；模型许可要允许再分发；画面与声音不出设备，云端只收文字）筛选  
> 关联：[第四版消融实验与优化方向](video-montage-ablation-v4.md)、[CutClaw](video-editing-cutclaw.md)、[FilmAgent](video-editing-filmagent.md)、[视频剪辑 baseline](video-moment-retrieval.md)

本文由调研代理逐条读页面写成，BEAT、DIRECT、Deep Video Discovery 三篇的标题、日期与摘要另行核对过；文中的细节数字来自正文，以各条的标注为准。

**标注约定**

- 【已核】：数字或事实是我在论文页面、arXiv HTML 或仓库 README 上读到的。
- 【摘要】：只读到摘要或搜索摘要，没有打开正文。
- 【推断】：我按约束推算或判断，没有来源。CPU 耗时没有一项在评测机上实测过。

---

## 一、结论速览

我们五个已知问题，都能在近两年的工作里找到可借的做法：

| 我们的问题 | 最值得借的做法 | 来源 |
|---|---|---|
| 每张卡片都要描述，CPU 上最贵，随素材变长线性增长 | 先按向量聚类，只描述每类的代表卡；其余卡片等规划检索到再描述，选哪些卡由“相关度 + 时间覆盖”一起决定 | VideoTree、AKS、VideoAgent（Stanford）、DIRECT、EditDuet |
| 规划模型读完了全部描述，于是不检索 | 只给规划模型一份压缩过的全局目录（类别摘要 + 对白提纲），卡片只能靠检索工具拿到；Deep Video Discovery 的消融显示，去掉片段检索掉分最多 | Deep Video Discovery |
| 入出点由看不见、听不见的模型定，剪在半句话上；还会跳切、丢掉建立镜头 | ① 用 VAD 和带时间戳的语音识别把入出点吸附到句子边界；② 在镜头内滑动取窗，按运动方向和构图的衔接打分来定入出点；③ 用规则代码禁止同镜头跳切 | Prompt-Driven Agentic Editing、HIVE、DIRECT、BEAT |
| 没有声音理解（对白、节拍、响度） | 用 SenseVoice-Small 一次跑完转写、情绪和 BGM/笑声/掌声事件；节拍用 beat_this（MIT）；响度用 FFmpeg `ebur128` | SenseVoice、beat_this、Silero VAD |
| 预告片的结构和节奏只靠提示词 | 防剧透写成规则（排除片尾 15%）；按音乐能量自适应决定镜头长短（Bar-DP，单核 1 秒以内）；开头和结尾单独选 | BEAT、HIVE、TeaserGen |

**许可证的坑**：

- CutClaw 与 All-In-One 都依赖 madmom，而 madmom 的**模型文件是 CC BY-NC-SA 4.0**【已核】，不能用于再分发。节拍检测要换成 beat_this（代码与权重 MIT）【已核】。
- ImageBind 是 BEAT 的特征提取器。它的非商用许可是【推断】（凭记忆，没有打开页面），用 BEAT 时只借算法。
- QVHighlights 训练出的高光检测权重，许可也要谨慎【推断】。

---

## 二、LLM 与智能体剪辑、预告片生成

### 1. BEAT：节奏弹性对齐的智能体预告片生成

- **链接**：[arXiv 2605.27067](https://arxiv.org/abs/2605.27067)（2026-05，v2 2026-09）；代码 [hhhh1138/BEAT](https://github.com/hhhh1138/BEAT)，**Apache-2.0**【已核】。仓库首页没有看到 MuVA 权重的发布记录。
- **做什么**：从整部电影选镜头、配背景音乐生成预告片。分五步：
  1. 音乐分析；
  2. 候选准备（NarrativeGuard）；
  3. 音画对齐打分加 Bar-DP 选片；
  4. 合成；
  5. VLM 看成片打分，回退重选，最多 3 轮。
- **关键细节**【已核】：
  - **切镜头与音乐分段**：镜头用 TransNetV2 切。音乐不跟踪节拍，而是用 Ruptures 的变点检测分小节，每小节算 CLAP 特征和归一化的 RMS 能量。
  - **防剧透的硬规则**：NarrativeGuard 排除影片最后 15% 和片头标志，再按独特性与位置给镜头打分；用户指令可以由 LLM 解析成关键词，进一步收窄候选。
  - **Bar-DP 的打分与搜索**：用束搜索，W=50，每小节取前 20 个候选，一个镜头最多跨 5 个小节。转移得分由三项组成：
    - 跨度内的平均对齐分；
    - 0.3 × 与上一镜头的视觉相似度；
    - 能量自适应的切点奖励 0.5 ×（能量 − 0.15）：满能量约 +0.43，零能量约 −0.08。
  - **Bar-DP 的效果与约束**：高能段倾向一小节一切，安静段倾向一个镜头跨几小节。用过的镜头，与它余弦 > 0.8 的邻近镜头一并排除。
  - **速度**：约 60 小节、1,500 个镜头时，**单核 CPU 不到 1 秒**。
  - **对白处理**：用 Silero VAD 检测对白，压低配乐（ducking）。
  - **转场**：按能量选溶解、闪白或硬切。
- **数字**【已核】：Test-73 上 F1 0.233（SSMP 0.226），SoftF1@5 0.403（0.325），VLM 总评 5.94（5.81）。排序指标（Kendall τ 等）反而不如 SSMP。实验在一张 H200 上跑。
- **对我们**：
  - Bar-DP 不依赖神经网络，可以直接改写进我们的规则层，“对齐分”换成我们的检索分与音乐能量匹配【推断】。
  - 防剧透的“排除片尾 X%”和“用过就排除相似镜头”，都可以写成校验器规则，正好对应“不剧透结局”这类指令。
  - MuVA 依赖 ImageBind 特征，不借。

### 2. HIVE：仿照剪辑师流程，把长剧剪成短片

- **链接**：[arXiv 2507.02790](https://arxiv.org/abs/2507.02790)（EMNLP 2025 Industry）。页面上没有代码链接。
- **做什么**：把 20–30 分钟的短剧剪成广告短片。
  - **理解阶段**：
    - ASR 加 OCR 字幕，由 LLM 纠错，ASR 时间戳保持不变；
    - 说话人分离加人脸聚类，确定谁在说话；
    - AutoShot 切镜头，VLM 再把同地点、同事件的镜头合并成**场景**；
    - 剪辑单位是场景，目的是“片段边界不打断正在进行的对白或动作”。
  - **剪辑阶段**分三步：
    1. 按规则表给场景打高光分；
    2. 单独选开头和结尾，多组采样；
    3. 删去中间的一般场景。
- **数字**【已核】：
  - **主要结果**：VEI（参与度 × 流畅度）为 HIVE 4.01，人工 6.35，端到端 ASR 基线 0.54。
  - **随机选开头结尾**：钩子率从 0.71 掉到 0.28，悬念率从 0.73 掉到 0.30。这是贡献最大的部分。
  - **去掉删减**：流畅度升到 5.10，参与度降到 0.69。
- **对我们**：
  - “开头与结尾单独选”可以作为规划里一个独立的子步骤，在悬疑预告里尤其重要。
  - “剪辑单位不打断对白”应由代码保证：用句子边界，而不是靠场景这种粗粒度单位。
  - HIVE 用的都是云端大模型，我们只借流程【推断】。

### 3. Prompt-Driven Agentic Video Editing System

- **链接**：[arXiv 2509.16811](https://arxiv.org/abs/2509.16811)（2025-09）。没有代码，作者说会提供提示词与评分标准。
- **做什么**【已核】：
  - **建索引**：把多小时影视切成 15 分钟、彼此重叠的段，先建全局梗概和人物关系图；再按 5 分钟的场景、约每 20 秒或每个镜头切点写一条结构化 JSON 注释。“引导式记忆压缩”把每段的草稿压成摘要往后传。
  - **检索与计划**：检索智能体在带时间戳的注释里找素材，输出剪辑计划（时间段、理由、叙事功能）。每段指定渲染方式：静音加旁白、保留原声对白、或蒙太奇。
  - **入出点微调**：**micro-cut 智能体用转写结果调整入出点**，目标是毫秒级，不剪断语音。**节拍对齐与对白冲突时放弃节拍**（librosa 检测节拍）。
- **数字**【已核】：
  - 索引的人工评分：质量 4.55 对 Gemini 2.5 Pro 的 4.43，差异不显著；可用性 4.45 对 1.10–1.29。
  - 问答时间戳准确度：4.74 对 4.06。去掉精修一步，细节分从 4.46 掉到 2.39。
  - 剪辑的专业度：AI 2.75，人工 4.38，差距仍大。
- **对我们**：
  - **“对白优先于节拍”的冲突规则**，以及“每段标明保留原声还是静音”，可以直接写进计划的结构。
  - micro-cut 一步不需要 LLM：用 ASR 的词或句时间戳做吸附就能实现【推断】。

### 4. EditDuet：编辑与批评两个智能体

- **链接**：[arXiv 2509.10761](https://arxiv.org/abs/2509.10761)（SIGGRAPH 2025，Adobe）；项目页 mudtriangle.com/editduet，**没看到代码**【已核】。
- **做什么**【已核】：
  - **两个智能体**：编辑智能体用工具（`search_collection` 用 CLIP 返回 5 段、`add_to_timeline`、`remove`、`move` 等）在时间线上剪 B-roll；批评智能体只看文字，给意见或放行渲染。两者都是 Llama3.1-8B。
  - **素材表示**：先用 TW-FINCH 把素材**聚类**，每类用 LLaVA-NeXT 看 16 帧写一条描述，再配上景别分类器和运镜分类器的结果。
  - **示范样例**：靠自监督探索生成上下文示范，约 82 分钟。
- **数字**【已核】：失败率 8.2%（VisProg 34.8%）；时间覆盖 89.8%；人类偏好对 T2V 为 86.9%。**评审**：GPT-4o 看每段中点关键帧拼成的网格加每段时长做成对比较，与人类多数票一致 80.6%，人与人之间是 78.7%。
- **对我们**：
  - “按聚类描述，而不是逐卡描述”有直接的先例。
  - 评审的做法（关键帧网格加时长，成对比较）便宜，可以搬到我们的评测里。

### 5. DIRECT：混剪的多智能体系统，带衔接与节拍指标

- **链接**：[arXiv 2604.04875](https://arxiv.org/abs/2604.04875)（2026-04）；代码 [AK-DREAM/DIRECT](https://github.com/AK-DREAM/DIRECT)，README 徽章写 MIT，文件列表里没看到 LICENSE【已核】。
- **做什么**【已核】：
  - **三个智能体**：
    - Screenwriter：按语义向量把素材聚类，**只给类别写描述**，再写分段计划；
    - Director：每段给出查询、剪辑偏好（各项指标的权重）和以拍为单位的节奏；
    - Editor：在素材图上做束搜索（B=3）。
  - **入出点**：在每个原始镜头里**以 4 帧为步长滑动取窗**，取综合分最高的子片段。
  - **六项指标**：
    - m1：指令相关度；
    - m2：相邻段 CLIP 相似度；
    - m3：运动连续，比较切点前后 RAFT 光流的幅度与方向；
    - m4：构图连续，比较切点前后 U2-Net 显著图的 Wasserstein 距离；
    - m5：切点到最近节拍的指数衰减；
    - m6：镜头运动强度与音乐 RMS 的 Spearman 相关。
  - **闭环验证**：Director 用 MLLM 检查候选，全部不合格就放宽查询。
- **数字**【已核】：

  | 项目 | DIRECT | 最好的基线 |
  |---|---|---|
  | 运动连续 m3 | 77.26 | 62.24 |
  | 节拍同步 m5 | 98.69 | 91.46 |
  | 能量对应 m6 | 82.40 | 52.67 |
  | 人评整体（1–7） | 5.9 | 4.6 |

  在 4 张 L20 上，每部两小时影片预处理约 25 分钟，生成一条混剪约 10 分钟。
- **对我们**：
  - **最接近“入出点由代码定”的现成实现**。我们已经用 OpenCV 算了光流，m3 和 m5 几乎零成本就能做成：在 LLM 给的段里滑窗，选衔接分最高的入出点【推断】。
  - U2-Net 显著图在 CPU 上可用，但要实测。
  - 它的音乐分析用 All-In-One，依赖 madmom，许可有问题，要换掉。

### 6. TeaserGen：先写旁白，再配画面

- **链接**：[arXiv 2410.05586](https://arxiv.org/abs/2410.05586)（ICLR 2025）【摘要】。代码与数据“接收后发布”，是否已发布未核实。
- **做什么**：把超过 30 分钟的纪录片压成 3 分钟以内的预告。先把转写分 10 段摘要，改写成以问句结尾的故事旁白，再为每句旁白挑最相关的画面。数据集 DocumentaryNet 有 1,269 对。
- **对我们**：“保留关键对白”的预告，可以**先从转写里挑对白句子作为骨架**，再给句子之间配画面。规划模型只需要读文字转写，不用读所有画面描述【推断】。

### 7. 预告片生成的学习式路线：TGT、IPOT、SSMP

- **TGT**（[CVPR 2024](https://openaccess.thecvf.com/content/CVPR2024/html/Argaw_Towards_Automated_Movie_Trailer_Generation_CVPR_2024_paper.html)，arXiv 2404.03477）：自回归地选下一个镜头，数据集有 23,304 对电影与预告片【摘要】。
- **IPOT**（arXiv 2407.19456）：用逆向部分最优传输，让音乐引导选镜头与排序【摘要】。
- **SSMP**（arXiv 2512.04426）：掩码预测加自我纠错，是 BEAT 之前的最好结果【摘要】；数字见 BEAT 一节。
- **对我们**：都要训练，都在 GPU 上跑，素材也只限电影，不直接可用。它们说明“选片和排序分开做会累积误差”【摘要】。

### 8. 观察名单（只读了摘要）

- **VideoAgent（HKUDS）**：[arXiv 2606.23327](https://arxiv.org/abs/2606.23327)（2026-06），代码 [HKUDS/VideoAgent](https://github.com/HKUDS/VideoAgent)。30 多个剪辑智能体，编排成功率 87–95%，API 费用降约 60%【摘要】。
- **Unified Agentic Video Editing**：[arXiv 2609.12769](https://arxiv.org/abs/2609.12769)（2026-09）。同一套智能体工具做场景预览、摘要和电影预告三种任务，摘要里没有数字【摘要】。
- **From Shots to Stories**：[arXiv 2505.12237](https://arxiv.org/abs/2505.12237)（2025-05）。把镜头转成结构化语言的 L-Storyboard 交给 LLM，与我们的卡片目录同一思路【摘要】。

---

## 三、先检索、再看画面：避免逐段描述

### 9. Deep Video Discovery（DVD）

- **链接**：[arXiv 2505.18079](https://arxiv.org/abs/2505.18079)（NeurIPS 2025，微软亚研）；代码 [microsoft/DeepVideoDiscovery](https://github.com/microsoft/DeepVideoDiscovery)，**MIT**【已核】。
- **做什么**【已核】：
  - **建库**：视频切成 5 秒片段，按 2 fps 解码；每段写描述并编成向量，同时维护一张“主体登记表”。
  - **三个工具**：
    - Global Browse：返回主体级和事件级的全局摘要；
    - Clip Search：按描述向量返回前 16 段；
    - Frame Inspect：给时间段加一个子问题，最多取 50 帧交给 VLM。
  - **推理循环**：ReAct 风格，最多 15 步，实际平均 6.7–7.3 步。
- **数字**【已核】：
  - LVBench 74.2%，加转写 76.0%；o3 单独做是 57.1%。
  - **消融**：去掉 Clip Search 掉 12.3%，去掉 Frame Inspect 掉 8.4%，去掉 Global Browse 只掉 2.9%。
  - 每题约 0.15 M token、0.21 美元。
- **对我们**：
  - **全局浏览的贡献最小，片段检索最大**。这说明“把全部卡片描述塞给规划模型”不是必要的：给一份粗目录，再强制走检索，效果可能不降。我们“规划模型不检索”的问题，正是因为全局浏览给得太多【推断】。
  - DVD 本身仍逐段写描述，所以描述成本这一项要配合下面几项来解决。

### 10. VideoAgent（Stanford）：按需取帧

- **链接**：[arXiv 2403.10517](https://arxiv.org/abs/2403.10517)（2024-03，ECCV 2024【推断，页面没写会议】）。
- **做什么**【已核】：LLM 作为中心智能体，自己判断信息够不够；不够就让视觉语言模型检索并描述新的帧，反复迭代。
- **数字**【已核】：EgoSchema 平均只用 **8.4 帧**、NExT-QA 8.2 帧，零样本准确率 54.1% 和 71.3%。
- **对我们**：直接支持“caption-on-demand”。卡片可以先只有向量和便宜的测量值，规划模型检索到之后，才调用本地 VLM 写描述【推断】。

### 11. VideoTree：查询自适应的层次聚类

- **链接**：[arXiv 2405.19209](https://arxiv.org/abs/2405.19209)（CVPR 2025）；代码 [Ziyang412/VideoTree](https://github.com/Ziyang412/VideoTree)，**MIT**【已核】。
- **做什么**【摘要 + 已核 README】：
  1. 按视觉特征把帧聚类，按与查询的相关度给每类打分；
  2. 只在相关的类里往下细分，形成一棵树；
  3. 只把选中关键帧的描述交给 LLM。
  - README 里，特征用 EVA-CLIP-8B，描述用 LaViLa。
- **数字**【摘要】：EgoSchema 61.1%、NExT-QA 75.6%（测试集），推理时间少于逐帧描述的方法。
- **对我们**：EmbeddingGemma 向量已经有了。k-means 或 FINCH 聚类在 CPU 上是毫秒到秒级【推断】。“每类一个代表卡先描述，相关类再展开”能把描述数从每小时约 400 张降到几十张【推断】。

### 12. AKS：相关度与时间覆盖的平衡

- **链接**：[CVPR 2025](https://openaccess.thecvf.com/content/CVPR2025/html/Tang_Adaptive_Keyframe_Sampling_for_Long_Video_Understanding_CVPR_2025_paper.html)，arXiv 2502.21271；代码 [ncTimTang/AKS](https://github.com/ncTimTang/AKS) **没有声明许可证**【已核】，只能借思路。
- **做什么**【摘要】：在固定的帧预算下选关键帧，兼顾与提示的相关度和对整段视频的覆盖：递归把视频分箱，按每箱的相关度分配名额。只看相关度或只看覆盖都不如两者折中。
- **对我们**：决定“先描述哪些卡”时用同样的目标，既不漏掉低分但必要的段（建立镜头、转场），又把预算集中在相关区域。这能顺带缓解“建立镜头被丢”【推断】。

### 13. Video-RAG：用 ASR、OCR、检测结果当辅助文本

- **链接**：[arXiv 2411.13093](https://arxiv.org/abs/2411.13093)（NeurIPS 2025）；代码 github.com/Leon1207/Video-RAG-master（许可未核）。
- **做什么**【摘要】：不训练，把 ASR、OCR 和物体检测的结果作为与画面对齐的文字，按模型生成的检索请求取回，再拼进提示。六个开源模型平均在 Video-MME 上 +8.0%，每例只多 2.0K token。
- **对我们**：支持“规划模型只读文字”的路线。转写和 OCR 都在本地生成，比逐卡 VLM 描述便宜得多。对白类指令（预告片）的信息主要在转写里【推断】。

### 14. TimeSearch：先粗后细的时间搜索

- **链接**：[arXiv 2504.01407](https://arxiv.org/abs/2504.01407)（2025-04，v2 2025-12）【摘要】。代码说会发布，未核实。
- **做什么**：把时间线分成粗事件再分子事件；VLM 用生成式问答的置信度决定先搜哪个子事件。LVBench 从 41.8% 提到 51.5%，Charades-STA 的 mIoU +11.8%。
- **对我们**：“卡片 → 窗口 → 0.5 秒帧”的核对本来就是三层。可以借它的“用置信度决定展开顺序”，把核对预算花在不确定的段上【推断】。

### 15. Qwen3-VL-Embedding 与 Qwen3-VL-Reranker（2B）

- **链接**：[QwenLM/Qwen3-VL-Embedding](https://github.com/QwenLM/Qwen3-VL-Embedding)，仓库 **Apache-2.0**【已核】；权重另见模型卡【未核】。
- **做什么**【已核】：
  - **Embedding**：输入可以是文字、图片或视频帧序列，支持 MRL 截断维度；2B 输出 2048 维。MMEB-v2 视频检索 53.9（2B）和 58.7（8B）。
  - **Reranker**：单塔结构，对（查询，候选）逐对打分，用 “yes” 和 “no” 的生成概率作为相关度。2B 为 53.2，8B 为 61.0。
- **对我们**：
  - 我们第 3 版的核对问法“先答有/没有”就是 reranker 的形式。用专门训练过的 reranker 替换通用 VLM 做“这一帧是否切题”，可能更准【推断】。
  - 2B 模型在 8 核 CPU 上每帧多半要秒级，量化与 GGUF 支持情况要核实【推断】。
  - 只适合用于核对阶段的少量帧，不适合全量建索引。

### 16. Lighthouse：时刻检索与高光检测工具库

- **链接**：[line/lighthouse](https://github.com/line/lighthouse)（EMNLP 2024 Demo），代码 **Apache-2.0**【已核】。
- **做什么**【已核】：统一了 Moment-DETR、QD-DETR、CG-DETR、UVCOM、TR-DETR、TaskWeave、R2-Tuning 等模型。**提供 CPU 推理接口**，CPU 上建议只用 CLIP 特征（SlowFast 和 PANNs 太慢）。还支持音频时刻检索（CLAP）。
- **限制**：权重的许可没有写明，而主要训练集 QVHighlights 的许可是非商用的【推断】。
- **对我们**：适合作为评测时的检索对照基线，不适合放进产品。

---

## 四、入出点与切点：镜头、句子、节拍

### 17. 句子边界：VAD 与带时间戳的 ASR

| 工具 | 链接 | 许可 | 关键事实 | 对我们 |
|---|---|---|---|---|
| **Silero VAD** | [snakers4/silero-vad](https://github.com/snakers4/silero-vad) | **MIT**（LICENSE 文件）【已核】 | 模型约 2 MB；每 30 ms 以上的块单线程不到 1 ms；支持 8/16 kHz 与 ONNX【已核】 | 一小时音频单线程估计在 1 分钟左右或更少【推断】。给出“有人说话”的区间，入出点不许落在区间内部（J-cut 和 L-cut 显式标注时除外） |
| **SenseVoice-Small** | [FunAudioLLM/SenseVoice](https://github.com/FunAudioLLM/SenseVoice) | 代码 MIT；权重用 FunASR 模型许可，页面说遵守条款即可商用，需署名【已核】 | 中、粤、英、日、韩；**输出词级时间戳**；情绪 7 类；事件含 BGM、掌声、笑声、哭声等【已核】。社区 int8 ONNX 实测 RTF 约 0.04（CPU 型号未注明）【已核，第三方】 | **一次推理拿到对白、情绪和声音事件**，正好补上“没有声音理解”。一小时约 2–4 分钟 CPU【推断】 |
| **Paraformer-zh + fsmn-vad + ct-punc** | [modelscope/FunASR](https://github.com/modelscope/FunASR) | 代码 MIT；Paraformer、CAM++ 的模型卡写 Apache-2.0（FunClip 页面所述）【已核】 | Paraformer 220M，带时间戳；fsmn-vad 0.4M；ct-punc 290M 负责断句加标点；FunASR 的 PyTorch CPU 基准 RTF 约 0.064【已核，第三方汇总】 | 中文为主时的首选组合：标点模型直接给出**句末位置**，吸附入出点最方便 |
| **faster-whisper** | [SYSTRAN/faster-whisper](https://github.com/SYSTRAN/faster-whisper) | MIT【已核】 | small int8、8 线程（i7-12700K）转写 13 分钟音频用 **1 分 42 秒，内存 1.48 GB**；批量 8 时 51 秒、3.6 GB；`word_timestamps=True`；内置 Silero VAD【已核】 | 多语种素材的后备选项。按比例约 8 分钟处理一小时；3.4 GiB 内存下不能开批量【推断】 |
| **whisper.cpp** | 同表数据 | MIT【推断】 | 同一基准：small fp32 2 分 05 秒，1.05 GB【已核，出自 faster-whisper README】 | 内存更省，适合同时跑 VLM 的时候 |
| **WhisperX** | [m-bain/whisperX](https://github.com/m-bain/whisperX) | BSD-2-Clause【已核】 | 用 wav2vec2 强制对齐得到词级时间戳；Whisper 自带的时间戳“可能差几秒”【已核】 | README 没写中文对齐模型，而对齐模型各有许可。中文素材优先用 Paraformer 或 SenseVoice 自带的时间戳 |
| **sherpa-onnx** | [k2-fsa/sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) | Apache-2.0【已核】 | 一个运行时覆盖 SenseVoice、Paraformer、Whisper、Silero VAD、音频标签、说话人分离和标点；支持 x86_64【已核】 | **建议的部署形态**：一个 C++ 和 ONNX 运行时、int8，不需要 PyTorch，内存可控【推断】 |

### 18. 节拍与音乐结构

- **beat_this**（ISMIR 2024，arXiv 2407.21658）：[CPJKU/beat_this](https://github.com/CPJKU/beat_this)，**代码与权重 MIT**【已核】。
  - 输出节拍与**重拍（downbeat）**；small 模型约 8.1 MB，主模型约 78 MB。
  - 不依赖 madmom，madmom 只在可选的 DBN 后处理里用。支持 CPU【已核】。
  - **建议替换 CutClaw 式的 madmom 方案**。一首 1–3 分钟的配乐在 CPU 上估计几秒钟【推断】。
- **madmom**：代码 BSD，但**模型与数据文件是 CC BY-NC-SA 4.0**，商用要找作者【已核】。不能进可再分发的产品。
- **All-In-One**：[mir-aidj/all-in-one](https://github.com/mir-aidj/all-in-one)。仓库 MIT，能输出 BPM、节拍、重拍和 intro/verse/chorus 等段落标签【已核】。
  - 但它依赖 madmom 和 Demucs，在 GPU（4090）上 10 首歌 73 秒【已核】。
  - 在 CPU 和 3.4 GiB 内存下估计不可行【推断】。
  - 需要段落时，可用 RMS 能量曲线加变点检测（BEAT 的做法，Ruptures 是 BSD【推断】）代替。
- **librosa**（ISC【推断】）：`beat_track` 与 onset 包络可作后备，CPU 很便宜。

### 19. 镜头边界

- **TransNetV2**：[soCzech/TransNetV2](https://github.com/soCzech/TransNetV2)，MIT【已核】。
  - F1：ClipShots 77.9、BBC 96.2、RAI 93.9；输入只有 48×27 像素，有 PyTorch 推理版本【已核】。
  - 输入很小，CPU 上应该不贵【推断】。可以减少 FFmpeg `scdet` 漏切（我们曾漏掉 17–59 s 的切点）和渐变漏检。BEAT 和 HIVE（AutoShot）都用学习式检测器。
- **PySceneDetect**：CutClaw 和 DIRECT 都在用，属于已知方案。

### 20. 衔接与跳切规则（汇总，都可以写成代码）

1. **入出点吸附**【推断，综合 Prompt-Driven Editing、HIVE】：LLM 给出大致的段，代码在 ±1 s 内把入点移到句首或 VAD 静音处、出点移到句末。移动后时长不在 1.5–6 s 之间，就退回给规划模型。
2. **同镜头跳切**【推断，参照 BEAT 的“用过就排除相似镜头”（θ=0.8）】：相邻两段来自同一镜头、间隔小于 N 秒且画面余弦大于 0.9 时，判为跳切，退回给规划模型。或者明确要求中间插一个别的镜头。
3. **运动与构图衔接**【已核做法，来自 DIRECT】：切点前 0.5 s 与后 0.5 s 的光流方向和幅度相近时加分；在段内按 0.2–0.5 s 的步长滑窗选最优入出点。
4. **节拍吸附**【已核，来自 CutClaw、DIRECT】：切点到最近节拍的误差 ≤ 0.1 s 算对齐。DIRECT 还搜索 ±0.5 s 的全局音画偏移。
5. **对白优先**【已核，来自 Prompt-Driven Editing】：节拍吸附会切断对白时放弃吸附。
6. **建立镜头**【推断】：每个叙事段的第一段允许选“远景、固定或慢推”的卡片，这些可以从光流统计和 AKS 式覆盖预算里挑出来，不靠 VLM 判断景别（我们已经验证 2B 判断景别不可靠）。

### 21. 学习“在哪里切”（背景）

- [Learning to Cut by Watching Movies](https://arxiv.org/abs/2108.04294)（2021，ICCV）：257,064 个切点，学习音画线索判断切点是否合理【摘要】。
- [When and How to Cut Classical Concerts?](https://arxiv.org/abs/2510.05661)（2025）：用 log-mel 加“距上次切点的时间”预测切点【摘要】。
- 都需要训练，作为将来学习式入出点打分的参考。

---

## 五、CPU 上的声音理解与轻量模型

### 22. 音频标签与文本-音频检索

- **EfficientAT**：[fschmid56/EfficientAT](https://github.com/fschmid56/EfficientAT)，MIT【已核】。

  | 模型 | 参数 | MACs | AudioSet mAP |
  |---|---|---|---|
  | mn04 | 0.98 M | 0.11 G | 43.2 |
  | mn10 | 4.88 M | 0.54 G | 47.1 |
  | dymn10 | 10.6 M | 0.58 G | 47.7 |

  - 用 AudioSet 的 527 类给每 1–2 秒打标签：音乐、爆炸、枪声、人群、尖叫、汽车等。一小时估计在 1 分钟以内【推断】。
  - 补上“悬疑感预告”需要的声音高光（突发巨响、尖叫），也能找出“有音乐的段”避免撞乐。
- **LAION CLAP**：[LAION-AI/CLAP](https://github.com/LAION-AI/CLAP)。仓库 CC0-1.0，**页面没有写权重许可**【已核】。
  - ESC-50 零样本 R@1 0.93【已核】。
  - 能用文字检索声音，例如“紧张的弦乐”“心跳声”。要核实权重许可再用；HTSAT-base 在 CPU 上偏重【推断】。
- **响度**：FFmpeg 自带 `ebur128` 和 `loudnorm` 滤镜，可算短时响度曲线并统一成片响度【推断，属常识，未在本次查证】。

### 23. 轻量视觉模型与向量模型

- **SmolVLM2**（256M、500M、2.2B）：Apache-2.0，支持视频输入；Video-MME 分别为 33.7、42.2、52.1【已核，HF 模型卡】。
  - 500M 可以做低成本的初描述或“是否切题”预筛，不确定的再交给 Qwen3-VL-2B 或 4B【推断】。
  - llama.cpp 和 CPU 的实际速度没有查到。
- **SigLIP2**：Apache-2.0，base 约 0.4B【已核】。可作 EmbeddingGemma 之外的第二路图像向量，用于城堡、建筑这类细粒度区分【推断】。
- **MobileCLIP2**：Apple `apple-amlr` 许可【已核】，**不满足再分发要求**，排除。
- **Qwen3-VL-2B 与 4B**：已经在用。4B 未在 x86 上测。

---

## 六、自动剪辑的评测

### 24. 评审模型与它的局限

- **EditDuet 的成对评审**【已核】：GPT-4o 看两条时间线的关键帧网格和每段时长，做成对比较。与人类多数票一致 80.6%，人与人之间 78.7%；PABAK 0.61 对 0.57。**成对比较的可信度高于我们现在的 0–10 逐项打分**（我们已经观察到同一成片相差 1 分左右）。
- **MEDit-Bench**（[arXiv 2607.25300](https://arxiv.org/abs/2607.25300)，2026-07）【摘要】：面向“按讯息剪辑”的叙事剪辑，同一素材有多条讯息、每条有多个专业剪辑。结论是 **LLM 评审有严重的位置偏差**，不能代替人评；专业剪辑始终更受偏好。
  - **对我们**：成对比较时要交换顺序跑两遍，两次一致才计。指令难度（模糊度）要作为分层因素。
- **VideoJudge**（[arXiv 2509.21451](https://arxiv.org/abs/2509.21451)，2025-09）【摘要】：训练过的 MLLM 评审与人类的相关性更高；**只看文字的 LLM 评审明显差于能看画面的 MLLM 评审**。
- **VEU-Bench**（CVPR 2025）【摘要】：考的是视频大模型对剪辑要素（景别、转场等）的理解。不少模型低于随机水平，说明让 VLM 判断景别、运镜不可靠，与我们的实测一致。
- **DOVER-Mobile**：画面质量评估，9.86M 参数，CPU 上每段 1.4 s【摘要】；许可未核实。可以替代“评审给画面质量打分”。

### 25. 可在 CPU 上算、不需要评审模型的指标（建议加入 `video_judge.py`）

| 指标 | 定义 | 来源 |
|---|---|---|
| 节拍同步率 | 切点距最近节拍 ≤ 0.1 s 的比例，或指数衰减分 | CutClaw、DIRECT m5【已核】 |
| 对白截断数 | 落在 VAD 语音区间内、离句末超过 0.2 s 的切点数 | 【推断】，配合 Silero 或 FunASR |
| 跳切数 | 相邻两段同镜头且画面余弦 > 0.9 的次数 | 【推断】 |
| 运动连续分 | 切点前后光流的方向与幅度相似度 | DIRECT m3【已核】 |
| 能量对应 | 镜头运动强度与音乐 RMS 的 Spearman 相关 | DIRECT m6【已核】 |
| 流畅度 | 时长 /（1 + 打断次数） | HIVE【已核】 |
| 剧透 | 片段落在素材最后 15% 的时长占比 | BEAT NarrativeGuard【已核】 |

---

## 七、建议的第 4 版改动（按收益与成本排序，全部是【推断】）

1. **加一条音频轨道**：一次解码出 16 kHz 单声道，再分三路处理：
   - **对白**：Silero VAD 加 SenseVoice-Small（或 Paraformer 加 ct-punc），用 sherpa-onnx int8 运行，得到句子级时间戳、情绪和 BGM/笑声/掌声事件；
   - **声音事件**：EfficientAT mn10 打 AudioSet 标签；
   - **配乐**：beat_this small 检测节拍与重拍。

   一小时素材估计 5–10 分钟 CPU，内存小于 1 GB。卡片加上“对白文本、是否有人说话、声音事件、情绪”这几个字段，这些都只是文字，可以发给云端规划模型。
2. **入出点交给代码**：
   - LLM 只给“哪张卡、大约哪几秒、要表现什么”。
   - 代码先吸附到句子和静音边界，再按 DIRECT 的方式滑窗，按运动衔接打分；有配乐时再吸附到节拍，对白优先。
   - 跳切和剧透写进校验器。
3. **按需描述**：
   - 用 EmbeddingGemma 向量给卡片聚类，每类只描述代表卡。
   - 给规划模型的目录改成“类别摘要 + 对白提纲”，详细卡片只能经检索取得，检索到的卡再即时描述。
   - 描述预算按 AKS 式的“相关度 + 覆盖”分配。

   预计描述数降一个数量级，并迫使规划模型检索（DVD 的消融支持这样做）。
4. **预告片结构写成规则加模板**：
   - 开头与结尾单独选，多组采样，参照 HIVE。
   - 防剧透排除片尾 X%，参照 BEAT。
   - 用 Bar-DP 按能量决定节奏（有配乐时）。
   - 对白骨架优先，参照 TeaserGen。
5. **核对与纠错**：
   - 用 SmolVLM2-500M 做便宜的预筛，Qwen3-VL-2B 或 4B 只看不确定的帧。
   - Qwen3-VL-Reranker-2B 可以作为“是否切题”的专用打分器，先在易错的 14 张集合上比一比。
   - “带城垛的建筑认作城堡”这类错误，小模型没有好办法，留给成员预览。
6. **评测**：
   - 评审改成成对比较、交换顺序。
   - 加入第六节表中的程序指标。
   - 成片评审要能看画面（VideoJudge 的结论）。

## 八、未核实与待测

- 所有 CPU 耗时与内存都没有在 x86 8 核、3.4 GiB 的评测机上测过，特别是：SenseVoice 和 Paraformer 的 int8 RTF、TransNetV2 的 CPU 速度、SmolVLM2-500M 与 Qwen3-VL-Reranker-2B 的 CPU 速度。
- 待核实的许可：Qwen3-VL-Reranker 与 Qwen3-VL-Embedding 的权重、CLAP 的权重、DOVER、FunASR 模型许可的全文、DIRECT 的 LICENSE 文件、Video-RAG 代码。
- BEAT 的 MuVA 权重是否发布；TeaserGen 与 TimeSearch 的代码是否已发布。
- whisper.cpp 的 DTW 词级时间戳在中文上的表现没有找到数据。

## 关联

- 实验与取舍：[第四版消融实验与优化方向](video-montage-ablation-v4.md)
- 已有调研：[CutClaw](video-editing-cutclaw.md)、[FilmAgent](video-editing-filmagent.md)、[视频剪辑 baseline](video-moment-retrieval.md)
- 本文引用的论文与仓库链接，均在各条目正文中给出。
