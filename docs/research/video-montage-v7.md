# 剪辑第七版：挑一版与精修的网页原型

> 状态：开发机原型完成，剪辑实验到此收尾（成片沿用第六版；挑版、换镜头、重洗、删除是真实交互，处理进度与“重剪”是模拟的；未接进产品）  
> 更新日期：2026-11-13  
> 适用范围：[剪辑第六版](video-montage-v6.md)之后的第七版，代码在 [`ai/eval/montage_v5/prototype/`](../../ai/eval/montage_v5/prototype/)；跟进 issue 见 [#453](https://github.com/zhongwater123/A-NAS/issues/453)、[#341](https://github.com/zhongwater123/A-NAS/issues/341)  
> 关联：[照片与视频成片规格](../specs/photo-to-video.md)、[描述开销对比（#479）](https://github.com/zhongwater123/A-NAS/issues/479)

第六版能剪出成片，但用户怎么用还是空白：一次给几版、怎么挑、哪里不满意怎么改。第七版不再改剪辑算法，在第六版的真实成片和记录上做一个网页，走一遍“上传素材 → 三版里挑一版 → 直接确认，或按音乐槽换镜头后完成”，并参考同类产品补上等待和修改的体验。

## 结论

1. **一次给三版、让用户挑**，对应第六版“每次剪出来都不一样”的问题：不同的剪法当成选项给出去，每版配一句剪辑思路，完整规划收起来。
2. **修改以音乐槽为单位**：槽的长度和切点不动，换的是镜头。点一个镜头，先给同一张卡片的其他节拍，再按文字搜全部节拍；拖动只在同一段落里换顺序，挪到哪个槽就截多长。
3. **“锁定喜欢的、重洗其余”在本机就能做**：每个没锁的槽拿规划时写的画面描述（`slots[].query`）做向量搜索，从最像的 4 个里随机挑一个，不用再调用模型。一次查询约 0.08 秒（虚拟机 6 核）。
4. **删除镜头不留空**：前后两镜里，素材够长、画面又更像被删那一镜的延长过来，切点仍在小节线上；两边都不够长时问用户是否放弃踩点。
5. **确认动作要留后台记录**：直接确认、快捷微调（只记下用户说的那句话）、修改后确认，各记一行，用来看用户实际想怎么改。

## 页面

| 页面 | 做什么 |
|---|---|
| 首页 | 背景是 DJI 新西兰素材剪的 11.8 秒循环（`hero.mp4`），系统设了减少动态效果时只显示封面 |
| 上传 | 素材按 NAS 文件夹分组（新西兰 DJI、新西兰品杰手机、川西 DJI，共 417 段），可整组、按天、单段或 Shift 连选；最下面的 ＋ 添加本机视频。配乐（`#/music`）和剪辑要求（`#/ask`）各是一页，不点就用默认 |
| 处理中 | 依次显示读取的视频、切出的关键帧、质量检查、逐字写出的卡片描述、歌曲段落、三版剪辑思路和渲染进度，共约 22 秒（模拟）。可以先去忙别的，右下角留进度胶囊，做完弹出提醒 |
| 挑一版 | 三版成片各一句剪辑思路，完整规划收在“看完整规划”里；四个快捷按钮（节奏快一点、换个开场、多些人物、多些风景）和一个说一句的输入框，点了模拟重剪 3 秒 |
| 精修 | 按音乐段落排的镜头槽；替换、拖动、删除、锁定与重洗，都能撤销；“预览修改后的成片”按槽依次播原始视频片段 |
| 后台记录 | `#/records` 列出确认记录 |

参考的产品：处理中可离开照 Google 相册、影石 AI 导演、GoPro；等待时显示正在看的东西照苹果回忆；选版页的一句话和快捷按钮照 Sparki；锁定后重洗照影石 FlashCut。

数据是品杰新西兰素材配 Symphony 的三条第六版成片（`place-1/3/4`），不是假数据。三版都按 Symphony 剪，选别的配乐时页面会提示。

## 怎么做的

- **数据**（[`build.py`](../../ai/eval/montage_v5/prototype/build.py)）：从第六版的卡片描述、规划和剪辑记录生成 `data.js`：每个槽记下规划选的卡片、节拍和关键帧，全部 116 张描述过的卡片连同节拍都放进去，供换镜头时挑。每版的一句剪辑思路（`PITCH`）是手写的，代替规划器以后要多给的一个字段。曲库是做过结构分析的歌（`music-*.json`，现在 4 首）。上传页的拍摄时间取 DJI 文件名、iPhone 的 QuickTime 标签，其余取 UTC 创建时间加 13 小时（新西兰夏令时）。
- **搜索**（[`search.py`](../../ai/eval/montage_v5/prototype/search.py)）：用 EmbeddingGemma 2 编码文字，和每个节拍覆盖的 2 秒窗口画面向量比，节拍取最像的窗口的分数。向量来自 A-NAS 视频索引，`build.py` 按节拍分组写进 `beats.npz`；启动时核对模型版本，和建索引时不一致就退出。分数在 0.65–0.77 之间，只用来排序，不设门槛。`/likeness` 用节拍窗口向量的均值算两个节拍的相似度，删除镜头时用。
- **重洗的规则**：同一个节拍只用一次，不和前一镜同卡片，卡片要够长，横拍的槽不换成竖拍。锁住的镜头拖不动，其他镜头拖动时绕开它（用 Sortable 的 `draggable` 排除；它的 `filter` 会让同一列的其他镜头也拖不动）。
- **页面**（[`index.html`](../../ai/eval/montage_v5/prototype/index.html)）：单文件，不用构建。拖动用 [SortableJS](https://github.com/SortableJS/Sortable) 1.15.6（MIT，[`vendor/`](../../ai/eval/montage_v5/prototype/vendor/LICENSE)，与上游逐字节相同）。换页用 View Transitions 淡入，新页面视野内的图片解码好或过 0.25 秒再显示，滚回顶部放在新页面画好之后（原来先滚旧页面再换内容，看起来闪两下）。进度胶囊只建一次、之后只改文字和进度条：每次重建会让转圈动画从头开始而卡顿。上传网格的滚动放在外层 `.vscroll`：滚动条直接加在 grid 上时，浏览器会把行压扁、缩略图叠在一起。只有 HTTPS 或 localhost 下浏览器才给系统通知，局域网 http 地址只有页面内提醒。
- **存在哪**：换镜头和锁定存在浏览器本地（`proto:locked:<版本>` 等），换浏览器就没有；确认时才写进后台记录。

## 运行

代码不带数据。站点文件夹在仓库外（`ANAS_MONTAGE_PROTO`，默认 `~/A-NAS-datasets/剪辑对比/剪辑原型（2026-11-11）/`），`build.py` 往里写 `data.js` 和 `beats.npz`，并建好指向本目录页面、关键帧、成片、歌曲和原始视频的软链接；其余数据位置沿用[第六版的 `paths.py`](../../ai/eval/montage_v5/README.md#数据目录)。数据改了才需要重新生成：

```bash
cd ai/eval/montage_v5/prototype && python3 build.py
```

向量搜索与后台记录（anas 虚拟机，端口 8766 只对本机开放；MediaPipe 只装在 `anas-ai-dev:trixie` 镜像里）：

```bash
R="$HOME/code/A-NAS/.claude/worktrees/video-montage-v5"; P="$HOME/A-NAS-datasets/剪辑对比/剪辑原型（2026-11-11）"
docker --context colima-anas run -d --name proto-search -p 127.0.0.1:8766:8766 \
  -v "$R/ai:/src/ai:ro" -v "$R/deploy/models:/manifest:ro" -v "$P:/proto:ro" -v "$P/log:/log" \
  -v "$HOME/A-NAS-models/embeddinggemma-2-740m:/models:ro" -v "$HOME/A-NAS-datasets/cache/xnnpack:/xnnpack" \
  -w /src/ai -e PYTHONPATH=/src/ai -e PYTHONDONTWRITEBYTECODE=1 anas-ai-dev:trixie \
  /opt/anas-ai/bin/python /src/ai/eval/montage_v5/prototype/search.py --model /models/embeddinggemma-2-740m.litertlm \
  --manifest /manifest/embeddinggemma-2-740m.json --beats /proto/beats.npz --weight-cache /xnnpack --records /log/confirm.jsonl
```

停止：`docker --context colima-anas rm -f proto-search`。

页面（端口 8765，对局域网开放；要支持 Range 请求才能跳到原始视频的指定时间）。页面按同源地址请求 `search`、`likeness`、`record`，`--proxy` 把不是文件的路径转给向量搜索，所以别人只要能连上 8765：

```bash
cd "$HOME/A-NAS-datasets/剪辑对比/剪辑原型（2026-11-11）" && npx --yes http-server@14.1.1 -p 8765 -a 0.0.0.0 -c-1 --proxy http://127.0.0.1:8766
```

本机打开 <http://127.0.0.1:8765>；同一局域网的人打开 `http://<这台 Mac 的局域网 IP>:8765`（`ipconfig getifaddr en0` 查 IP，换网络会变）。这台 Mac 要开着、两个服务要在跑；开放期间同一网络里的任何人都能看到素材。

首页背景不进仓库，是 DJI 新西兰文件夹里三个没有水印的镜头：雪峰破云（`DJI_20261005165519_0986` 的 1.8–6.3 秒）、冰川上的直升机（`DJI_20261005172522_0005` 的 1.0–6.5 秒）、日照金山（`DJI_20261004194614_0926`，1.7 秒放慢一倍）；交叉淡化 0.8 秒，首尾各 0.6 秒黑场给循环接缝，页面从 0.7 秒开始播。720p 在高分屏上发虚，原片是 4K，直接按 1440p 出（约 9 MB）。`hero.jpg` 是它的封面。重做（`P` 是站点文件夹）：

```bash
cd ~/A-NAS-datasets/videos/dji-newzealand-2026-10 && ffmpeg -ss 1.8 -t 4.5 -i DJI_20261005165519_0986_D.MP4 -ss 1.0 -t 5.5 -i DJI_20261005172522_0005_D.MP4 -i DJI_20261004194614_0926_D.MP4 -filter_complex "[0:v]setpts=PTS-STARTPTS,scale=2560:1440:flags=lanczos,fps=30,format=yuv420p,settb=1/30[a];[1:v]setpts=PTS-STARTPTS,scale=2560:1440:flags=lanczos,fps=30,format=yuv420p,settb=1/30[b];[2:v]setpts=2*(PTS-STARTPTS),scale=2560:1440:flags=lanczos,fps=30,format=yuv420p,settb=1/30[c];[a][b]xfade=transition=fade:duration=0.8:offset=3.7[ab];[ab][c]xfade=transition=fade:duration=0.8:offset=8.4,fade=t=in:st=0:d=0.6,fade=t=out:st=11.2:d=0.6,unsharp=5:5:0.35:5:5:0,format=yuv420p[v]" -map "[v]" -an -c:v libx264 -preset slow -crf 24 -movflags +faststart "$P/hero.mp4"
```

## 模拟的部分

- 上传不上传文件，处理进度按开始时间算、存在浏览器里，七步约 22 秒；添加的本机文件不离开浏览器，三版成片仍用品杰的素材。
- 快捷微调和“说一句”只记下用户的要求，画面不变。
- 修改后不重新渲染，预览按槽依次播原始视频片段。
- 推荐配乐写在页面的 `RECOMMENDED` 里；《生命里》本机没有音源，只能选不能试听。

## 装进产品时要补的

1. **修改这一层**：第一层用规划第二步本来就多列的 1~4 个备选节拍（`cut.py` 的 `shots()`），只改 `cut_to_music()` 的输入（锁住哪些槽、其余换下一个备选）再渲染，不调模型；备选用完再像原型这样按画面描述做本地向量搜索；要整版换思路才需要带着锁定的镜头重问一次模型。
2. **规划器多给一个字段**：每版一句剪辑思路，代替手写的 `PITCH`。
3. **快捷微调接上重剪**：把按钮和用户说的话作为第一步规划的追加要求，只重剪选中的那一版。
4. **第六版的已知问题仍在**：画面不够丰富、遮挡规则误伤平静水面、描述的景别约 71% 正确、未在 x86 容器里跑、规划依赖云端模型（见[第六版的已知问题与下一步](video-montage-v6.md#已知问题与下一步)）。

## 关联

- 代码：[`build.py`](../../ai/eval/montage_v5/prototype/build.py)（`PITCH`、`beat_vectors`、`folders`）、[`search.py`](../../ai/eval/montage_v5/prototype/search.py)（`/search`、`/likeness`、`/record`）、[`index.html`](../../ai/eval/montage_v5/prototype/index.html)；数据位置 [`paths.py`](../../ai/eval/montage_v5/paths.py)
- 成片与规划：[剪辑第六版](video-montage-v6.md)；描述的 token 开销与方案 B 的对比见 [#479](https://github.com/zhongwater123/A-NAS/issues/479)
- 数据：站点文件夹 `~/A-NAS-datasets/剪辑对比/剪辑原型（2026-11-11）/`（`data.js`、`beats.npz`、`hero.*`、后台记录 `log/confirm.jsonl`），不进仓库
