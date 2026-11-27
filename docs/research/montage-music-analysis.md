# 剪辑配乐在 NAS 上的节拍与副歌分析（配乐探针 P2）

> 状态：开发机完成（2026-11-18）；节拍与重拍过门槛，副歌没过。同日用户决定改用 allin1、商业版权待议，本文的做法留作退路；实验 NAS 的 CPU 未测  
> 更新日期：2026-11-20  
> 适用范围：beat_this 上游提交 `b95c8ab`（2026-11-18 的 main），检查点 `small0`（sha256 `6074be2c4d490c5f6101fcc374a1ec72ae93456e23bb6019783b849f5dc7d47b`）；导出用 PyTorch 2.5.1（CPU）、opset 17，ONNX 文件 sha256 `f30b8009f2a44ca1ab946b1a655c144fc50a7c9bc75dc0c33276c8f358ec8af6`；推理用 onnxruntime 1.31.0（与设备的 `ai/requirements.in` 相同）；开发机 C 的 Colima `anas` 虚拟机（aarch64，6 核，8 GiB）  
> 关联：[剪辑中心实施方案](../architecture/videocut-center.md#配乐)的“配乐”、[评审意见](https://github.com/zhongwater123/A-NAS/pull/518#pullrequestreview-5950219461)、探针脚本 [`ai/eval/montage_music.py`](https://github.com/zhongwater123/A-NAS/blob/3ddad5c50d025af69d8dd1ef9eecce1e4e6a3ea5/ai/eval/montage_music.py)（没有合入主线，在 [#546](https://github.com/zhongwater123/A-NAS/pull/546) 的提交 `3ddad5c5` 里）

剪辑中心的配乐改为从成员自己的音乐库选或上传（PR #518 的评审，2026-11-18）。第八版读 allin1 算好的段落标签，而 allin1 依赖 PyTorch、Demucs 与 madmom，设备的 AI 运行环境里都没有，madmom 的模型文件又是 CC BY-NC-SA 4.0，不能进可再分发的产品。方案改为在 NAS 上自己分析：beat_this 找节拍与重拍，色度自相似找副歌。本文是动工前的配乐探针，用 10 首测试歌回答三件事：beat_this 能不能只靠 NumPy 与 onnxruntime 跑出和上游一样的结果；节拍与副歌准不准；在 CPU 上要多久、占多少内存。

## 结论

1. **beat_this 能在设备现有的运行环境里跑，结果与上游逐个相同**。small0 导出成 ONNX 约 10.6 MB（210 万参数）。梅尔谱、分块推理与后处理用 NumPy 写，3 首歌的节拍与重拍和上游 PyTorch 的 `Audio2Beats(dbn=False)` 逐个相同；梅尔谱最大差 1.8e-4，同一块输入的模型输出最大差 7e-5。不用 madmom。
2. **节拍与重拍过门槛**。对照 allin1，±70 毫秒内的 F 值：节拍 9 首在 0.938–0.999，重拍 9 首在 0.910–0.996。STAY 只有 0.659，是速度差一倍（我们 167 BPM，allin1 85 BPM）：隔一拍取一次，节拍与重拍的 F 值都是 0.99。两种速度对剪辑都能用，只是切点密一倍。
3. **副歌没过门槛**。三种不训练模型的找法、各两种特征，9 首人工确认过的歌里，副歌起点误差不超过 2 小节的最多 4 首，拟的门槛是 7 首。两种做得最好的找法对的歌不同，合起来覆盖 6 首；bad guy、Fortnight、Forever 三首哪种都不对。
4. **速度与内存**。一首 229 秒的歌：梅尔谱 0.1 秒，单线程推理 6.8–7.2 秒（4 线程 2.3–2.8 秒），找副歌 0.1 秒。onnxruntime 的内存池开着时进程峰值 1.8 GiB，关掉后 767 MiB；剪辑计算 Worker 的上限是 1 GiB（方案的“服务形态与部署”），实现时要关内存池、分块算 STFT、找副歌复用同一份 STFT。这些都是虚拟机上的数，实验 NAS 的 CPU 没测。

**等评审人定**：副歌没过门槛，怎么办先问评审人（方案“配乐”一节）。可选的做法见“副歌没过之后”。

## 做法

### 节拍与重拍

- **导出**：`montage_music.py export` 用上游的 `load_model("small0")`，外面包一层只返回两个 logit 的模块，`torch.onnx.export` 导出，时间轴动态。上游推理按 1500 帧一块，导出时的样例输入也是 1500 帧；导出时有关于旋转位置编码缓存的警告，所以只验证了 1500 帧的块，短于 30 秒的歌要补测。
- **梅尔谱**（上游 `LogMelSpect`）：22050 Hz，窗长 1024，步长 441（每秒 50 帧），周期 Hann 窗，两端反射填充，幅度谱除以 √1024（`normalized="frame_length"` 交给 `torch.stft(normalized=True)`，不是除以窗的能量，第一次写错时整张谱差 0.49），Slaney 刻度 30–11000 Hz 的 128 个梅尔带、不归一化，最后 `log1p(1000·x)`。滤波器组用 NumPy 现算，与 `torchaudio.functional.melscale_fbanks` 最大差 1.3e-5。
- **分块**（上游 `split_piece`、`aggregate_prediction`）：1500 帧一块，两边各丢 6 帧，最后一块靠齐结尾，重叠处保留前一块。
- **后处理**（上游 minimal）：7 帧内的极大值且 logit > 0，相邻一帧的峰合并取平均，重拍挪到最近的节拍。
- **采样**：FFmpeg 解码成单声道 22050 Hz 的 float32，与 FFmpeg Worker 的 `audio` 输出相同；上游比对时直接喂这份采样，不经它的 soxr 重采样。

### 副歌

都用 beat_this 的重拍分小节，每小节按拍分 4 份取色度（55–5000 Hz、12 个音级、对数压缩），拼成 48 维；响度取每小节的 RMS 分贝。最后都套第六版的规则：选中的段落后面一段响 2 dB 以上，就把它改判为铺垫，副歌取后面一段。

| 找法 | 做法 |
|---|---|
| 第一版（`--method v1`） | 每个起点往后 8 小节，在歌里还有几处不重叠的相似块（对角线平均余弦 ≥ 0.8），乘上响度的 z 分；取得分接近最高的起点里最早的一个 |
| 第二版（`--method v2`） | 相似度换成这首歌里的百分位（取前 15%），重复次数乘响度挑出最像副歌的块；再取与它相似、且不比它安静 3 dB 以上的块里最早的一处 |
| 第三版（`--method v3`） | Foote 棋盘核在小节自相似矩阵上算新颖度切段，平均特征余弦 ≥ 0.9 的段归成一类；出现两次以上、平均最响的那一类，取第一次出现 |

两种特征：原始的色度（`--raw`），以及减去全曲平均再归一化（默认）。色度全是正数，原始特征下任两小节的余弦都很高，第三版几乎把整首歌归成一类，所以加了第二种。

## 结果

### 节拍与重拍（对照 allin1，±70 毫秒的 F 值）

| 歌 | 时长（秒） | 节拍 F | 重拍 F | 节拍用时（秒，6 核） |
|---|---|---|---|---|
| Billie Eilish - bad guy | 194.1 | 0.987 | 0.910 | 3.2 |
| Billie Eilish - you should see me in a crown | 181.0 | 0.938 | 0.914 | 2.7 |
| Forester - All My Days | 224.6 | 0.999 | 0.996 | 3.4 |
| Forester - Symphony | 202.3 | 0.999 | 0.964 | 2.7 |
| Gavin DeGraw - Fire | 228.5 | 0.997 | 0.988 | 3.1 |
| Ivy Adara 等 - Apartment No. 2 | 183.5 | 0.987 | 0.992 | 2.8 |
| Lynnic 等 - Alone | 170.3 | 0.975 | 0.983 | 2.4 |
| Taylor Swift 等 - Fortnight | 229.0 | 0.989 | 0.989 | 3.6 |
| The Kid LAROI 等 - STAY | 141.8 | 0.659（隔一拍 0.99） | 0.658（隔一个 0.99） | 2.0 |
| keshi - Forever | 171.4 | 0.978 | 0.925 | 3.4 |

allin1 本身不是标准答案，这里看的是两者是否一致。拟的门槛“F ≥ 0.9”按原速 9/10 首过，按速度折半后 10/10 首过。

### 副歌（9 首人工确认过的歌，误差以小节计）

人工确认的副歌起点来自第六版研究的“副歌识别”一节（[`d83340e`](https://github.com/zhongwater123/A-NAS/blob/d83340e/docs/research/video-montage-v6.md)），用户逐首听过；Apartment No. 2 没有确认，不计。

| 歌 | 确认的起点（秒） | 第一版·原始特征 | 第二版·减平均 |
|---|---|---|---|
| bad guy | 88.9 | 0.0（差 49.9） | 58.6（差 17.0） |
| you should see me in a crown | 39.2 | 150.8（差 35.1） | **36.0（差 1.0）** |
| All My Days | 77.6 | **77.7（差 0.0）** | **77.7（差 0.0）** |
| Symphony | 63.7 | **61.7（差 1.0）** | **59.7（差 2.0）** |
| Fire | 53.3 | **53.3（差 0.0）** | 117.0（差 23.9） |
| Alone | 45.8 | **46.7（差 0.5）** | 56.3（差 5.5） |
| Fortnight | 34.1 | 200.4（差 66.5） | 155.4（差 48.5） |
| STAY | 45.4 | 24.2（差 14.9） | **45.4（差 0.0）** |
| Forever | 51.8 | 90.5（差 12.0） | 100.2（差 15.0） |
| 2 小节以内 | | **4/9** | **4/9** |

其余组合：第一版·减平均 2/9，第二版·原始特征 2/9，第三版两种特征都是 0/9。

- **bad guy**：编曲极简，贝斯的同一个动机贯穿全曲，重复最多的块落在前奏或主歌上。
- **Fortnight**：全曲纹理几乎不变，第六版研究里各种分段算法也只能切成碎块或一整段；最响、最常重复的是结尾。
- **Forever**：第一段副歌比后面的副歌安静，找到的是后面那一次。
- 门槛与参数都是在这 9 首上试出来的，没有另外的验证集，所以 4/9 已经偏乐观。

### 用时与内存（Fortnight，229 秒）

| 推理线程 | onnxruntime 内存池 | 梅尔谱（秒） | 推理（秒） | 找副歌（秒） | 峰值内存（MiB） |
|---|---|---|---|---|---|
| 1 | 开 | 0.12 | 6.77 | 0.13 | 1805 |
| 1 | 关 | 0.07 | 7.21 | 0.11 | 766 |
| 4 | 开 | 0.07 | 2.31 | 0.12 | 1805 |
| 4 | 关 | 0.07 | 2.83 | 0.11 | 767 |

- 进程导入 NumPy 与 onnxruntime 后是 81 MiB，算完梅尔谱是 441 MiB：STFT 一次把全部帧展开成矩阵，复数结果是双精度。分块算可以压下去。
- 内存池开着时推理多占约 1 GiB，关掉只多约 325 MiB，单线程慢 7%。
- 找副歌又算了一遍 STFT（`bar_features`），内存池开着时再涨 350 MiB；实现时复用梅尔谱那一份。
- 拟的门槛“4 分钟的歌不超过 30 秒”在虚拟机上单线程约 7 秒，余量大，但这是开发机的 Apple 芯片，实验 NAS 的 x86 CPU 要另测。

## 副歌没过之后

评审人说过不通过时先问评审人，下面只列可选的做法与代价，不替评审人定：

| 做法 | 代价 |
|---|---|
| 首版不追求自动找准：配乐页给出两个候选起点（第一版与第二版各一个，9 首里合起来覆盖 6 首），成员试听后选一个或拖动起点 | 成员每首新歌都要听一下；两个候选都不对时（9 首里 3 首）全靠拖动 |
| 首版仍只用 10 首测试歌，把 allin1 的结果放进设备；M10 的开关默认关闭，只在实验 NAS 上打开（方案写的退路） | 成员不能用自己的歌，首版不能对外开放 |
| 另找能在 CPU 上跑、许可允许分发的段落模型 | 没有现成的：allin1 要 Demucs 分离音轨，评审人记的是 CPU 上一首 2–5 分钟，权重的许可也要另核；SongFormer 约 7 亿参数，作者说 CPU 上太慢（[讨论](https://huggingface.co/spaces/ASLP-lab/SongFormer/discussions/1)） |

**2026-11-18 的决定**：用户决定在 NAS 上接入 allin1，它的商业版权列为待议点（许可的核对见[决定记录](../decisions/2026-11-17-montage-center-first-version.md#allin1-的商业版权)）。本文的 beat_this 与色度自相似留作退路：待议的结论是不能随产品分发时，按上表第一种或第三种做法补副歌。

## 复现

在开发机 C 的 Colima `anas` 虚拟机里：

```sh
python3 -m venv ~/montage-probe/venv && . ~/montage-probe/venv/bin/activate
pip install --index-url https://download.pytorch.org/whl/cpu torch==2.5.1 torchaudio==2.5.1
pip install numpy "onnx<1.20" onnxruntime==1.31.0 einops soxr rotary-embedding-torch tqdm mir_eval \
  "git+https://github.com/CPJKU/beat_this.git@b95c8ab0c58c2d9fcfd40508ae8dffbc05ac4f5c"
E=ai/eval/montage_music.py   # 在检出目录里
python $E export --checkpoint small0 --out ~/montage-probe/beat_this-small0.onnx
python $E check --onnx ~/montage-probe/beat_this-small0.onnx --audio <一首歌>
python $E songs --method v1 --raw --onnx ~/montage-probe/beat_this-small0.onnx --truth <truth.json>
python $E songs --method v2 --onnx ~/montage-probe/beat_this-small0.onnx --truth <truth.json>
```

- 检查点由 beat_this 从作者的服务器（`cloud.cp.jku.at`）下载，代码与权重都是 MIT；上游 README 写明部分训练数据有版权或受限的 CC 许可。
- `truth.json` 每首一项：`name`、`audio`、`allin1`（allin1 的结构文件）、`chorus`（人工确认的副歌秒数，没有就是 `null`）。歌在开发机 C 的 `~/Music/QQ音乐`，allin1 的结果在 `~/A-NAS-datasets/剪辑对比/规划与成片/structure-*.json`，`truth.json` 在 `~/A-NAS-datasets/montage-probe/`。这些都是商业歌曲，不进仓库。

## 未做

- 实验 NAS 的 x86 CPU 上的用时与内存。
- 短于 30 秒（少于 1500 帧）的歌：只导出并验证了 1500 帧的块。
- `music` 的选段与小节槽在 10 首歌上和第八版的差异（规格“剪辑行为迁移”里的一行）：副歌没过，选段的输入还没定，等评审人定了再测。
- final0（约 78 MB）是否更准、多耗多少时间。
