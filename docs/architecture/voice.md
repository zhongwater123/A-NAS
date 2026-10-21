# 语音处理技术设计

状态：已实现（2026-10-21，尚未合并、未部署）。行为见[语音处理规格](../specs/voice-processing.md)，云端转写与本机声纹的取舍见 [ADR 0027](../adr/0027-transcribe-in-the-cloud-and-keep-voiceprints-on-the-device.md)，声纹模型与阈值的依据见[声纹评估](../research/voiceprint-evaluation.md)。部署与实机验收进度见[当前状态](../status/CURRENT.md)。

## 组件与数据流

```text
Web 语音处理（web/src/voice）
   │ 会话 Cookie + CSRF，/api/v1/voice；从电脑导入走传输中心的 tus 上传到 个人空间/录音
   ▼
anas-api（a-nas）
   ├── internal/voiceapi ── 只接收已确认的用户（voiceapi.WithUser）
   └── internal/voice
         ├── voice.db（系统盘）：录音、句子、发言人与声纹、人、拒绝、译文、总结、豆包语音的密钥
         ├── 读取：files.Service.OpenContent（成员会话）──► File Broker ──► 用户身份的文件 Worker ──► 只读 fd
         ├── 解码：mediaworker.Client.Speech ──► /run/a-nas-media/media.sock ──► anas-media@（FFmpeg）
         │        16 kHz 单声道 Ogg Opus（识别）或 s16le 样本（声纹），每段至多一小时
         ├── 识别：voice.Doubao ──HTTPS──► openspeech.bytedance.com …/recognize/flash（豆包语音极速版）
         ├── 声纹：voiceworker.Client ──memfd + JSON──► /run/a-nas-voice/voice.sock（root:a-nas 0660）
         │                                                  │ 套接字激活，空闲 5 分钟退出
         │                                                  ▼
         │                                      anas-voice.service（DynamicUser，无网络，
         │                                      看不到 /srv/a-nas 与 /var/lib/a-nas）
         │                                      └── sherpa-onnx 说话人向量（3D-Speaker CAM++）与快速聚类
         └── 翻译与总结：voice.ChatModel ──HTTP over UDS──► /run/anas-assistant-gw/model.sock
                                                              （VNAS 的模型网关，持有 DeepSeek 的密钥）
```

开发模式（`ANAS_HOSTSTATE_MODE=fake`）默认用 `FakeRecognizer`、`FakeTextModel` 与 `FakeVoiceprints`：识别出一段两三人的固定对话，总结与译文由任务本身拼成，同一个识别标签在每段录音里是同一个声音，界面可以离线完整试用。`ANAS_VOICE_RECOGNIZER=doubao`、`ANAS_TEXT_MODEL_SOCKET`（连同 `ANAS_TEXT_MODEL`）与 `ANAS_VOICE_SOCKET` 分别换成真实组件。生产中识别默认是豆包语音，声纹 Worker 的套接字由安装器写入 `anas-api.env`；文本模型只有在 VNAS 的试用把模型网关的路径与模型名写进产品服务的 drop-in 时才可用（[`product-settings.sh`](../../ai/assistant-spike/product-settings.sh)）。

## 身份与可见性

- 每段录音属于导入它的成员（`recordings.owner_id`），以文件的稳定条目 ID 引用文件，同一成员对同一文件只有一段录音。所有接口先按所有者取出录音，别人的录音一律按不存在处理（[`service.go`](../../internal/voice/service.go)）。
- 导入时以请求者的会话 `Lookup` 与 `OpenContent` 文件，并由媒体 Worker 探测：没有音轨、超过 5 小时的文件当场拒绝；容器与编码不在浏览器可播放的组合里（例如 AMR、WMA）时记为 `transcode`，播放时由媒体 Worker 解码为 FLAC。
- 后台处理通过 `accounts.Service.RememberedSessions` 借用录音所有者的有效会话打开文件；没有时录音等待（`sign_in`），每 30 秒（`Options.Poll`）重新检查一次所有等待中的录音。会话只用于打开文件，媒体 Worker、豆包语音与声纹 Worker 都只拿到解码后的内容。
- 人（`people`）与拒绝（`rejections`）同样按成员隔离；声纹比对只在一名成员自己的录音之间进行。

## 处理流程

[`pipeline.go`](../../internal/voice/pipeline.go) 中一个循环按导入顺序一次处理一段录音，另一个循环一次运行一个翻译或总结（[`textjobs.go`](../../internal/voice/textjobs.go)）。产品服务重启时，处理中的录音与运行中的任务回到排队状态，从头再来。

1. **准备**：打开文件、探测时长，按 `Options.Chunk`（默认 1 小时）切成若干段。
2. **识别**：每段请求媒体 Worker 的 `speech`（Ogg Opus，24 kbps，一小时约 11 MB），Base64 后发给豆包语音的录音文件识别极速版：一次请求返回结果，不需要标准版的提交与查询，标准版只接受识别服务能下载的公网 URL，家里的 NAS 提供不了。请求开启说话人分离（普通话或未指定语言时 `ssd_version: 200`）、标点、数字规范化与分句，`corpus.context` 带上成员的热词和已命名的人名（[`doubao.go`](../../internal/voice/doubao.go)）。`45000010` 等鉴权错误与 HTTP 401/403 记为密钥被拒绝；`55000031`、`550xxxx`、429 与 5xx 记为繁忙，先等 15 秒、1 分钟各重试一次；`20000003` 静音与 `45000002` 空音频是没有说话的结果，不是失败。
3. **声纹**：每段内，识别服务给了说话人标签时，每个标签取其句子中 1–15 秒的部分、最长的优先，共至多 120 秒（标签很多时整段至多 25 分钟），一次 `embed_speakers` 得到每个标签的单位向量（各句向量按长度加权平均）；没给标签时，长于 1 秒的句子（各截到 8 秒，至多 25 分钟，超出时均匀抽取）一次 `cluster_speech` 聚类，短句归给时间上最近的一类。样本来自同一段的一次 `speech`（s16le）解码，只保留所选的部分，装进一个 memfd 交给 Worker（[`readStretches`](../../internal/voice/pipeline.go)）。
4. **合并各段**：按段落顺序与第一次开口的时间排列各段的声音，与已有发言人的余弦相似度不低于 `SameVoiceThreshold` 时并入（同一段的两个标签不互相合并），否则成为新发言人；发言人按全录音第一次开口的顺序编号 `s1, s2…`。
5. **保存与比对**：一个事务写入句子与发言人（含声纹向量与其所用的语音时长），录音完成并记下声纹模型；随后对该成员的所有发言人重新比对（`rematch`），再排上导入时选的总结与翻译。

### 声纹比对

一个人的声纹是成员命名为此人（`assigned = user`）且有向量的那些发言人的向量，没有单独的样本表：删除录音或改掉名字，样本随之消失。比对取发言人与此人每个样本的最大余弦相似度（[`speakers.go`](../../internal/voice/speakers.go)）：

- 不低于 `AutoThreshold` 时自动命名（`assigned = auto`）并记下相似度；
- 在 `SuggestThreshold` 与 `AutoThreshold` 之间时只记为建议；
- 成员说过“不是”的人不参与这位发言人的比对。

`rematch` 在每段录音处理完、命名、拒绝、合并发言人、删除人或录音、合并人之后运行一遍，所以命名一次会同时更新成员的其他录音；名字变化的录音增加 `names_revision`。只有同一声纹模型（`recordings.voiceprint_model`）的向量互相比较。

### 翻译与总结

- 两者只读 `voice.db` 中的转写，不再打开录音文件，所以不需要会话。
- 翻译每批至多 40 句、3000 字，以 JSON 任务发给模型并要求 JSON 回答（`response_format: json_object`），按句号对回；一批中漏掉的句子再问一次，仍然漏掉则保留原文，整批两次都不是 JSON 时任务失败。
- 总结把转写写成 `[mm:ss] 名字：句子` 的行，连同场景的说明与栏目（[`templates.go`](../../internal/voice/templates.go)）交给模型；超过 4 万字时分段整理，再让模型合并各段的结果。回答按模板整形：栏目顺序与标题以模板为准，模板不要待办时丢掉待办，关键词至多 8 个（`shaped`）。系统提示说明转写中的指令是录音内容而不是给模型的指令；回答只以文本显示，不执行任何东西。
- 结果记下所依据的 `text_revision` 与 `names_revision`。修改句子增加前者，命名、合并发言人与改句子的发言人增加后者；译文只看前者，总结两者都看，不一致时界面提示重新生成。
- `ChatModel` 用 HTTP over Unix 套接字访问 VNAS 的模型网关（[`textmodel.go`](../../internal/voice/textmodel.go)），路径为 `/v1/chat/completions`，网关加上密钥原样转发到 DeepSeek，并只记录用量与消息条数，不记录内容。

## 声纹 Worker

[`ai/anas_ai/voice.py`](../../ai/anas_ai/voice.py) 与相册的 AI Worker 共用 Python 运行环境、帧格式与模型清单校验，但是独立的进程和单元：相册的 Worker 一次只服务一个连接，长时间的物体检测不应挡住语音处理；两者的套接字也分属不同的组（相册服务与产品服务）。

- 请求：`info`；`embed_speakers`，`groups` 为至多 64 组 `[offset, count]` 样本区间；`cluster_speech`，`segments` 与余弦距离 `threshold`；`release`。样本以一个 memfd 描述符随请求传来，至多 30 分钟；每个区间 0.5–60 秒，至多 4000 个。
- 推理：[sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx)（Apache-2.0）的 `SpeakerEmbeddingExtractor` 计算 80 维 Fbank 并运行 3D-Speaker 的 CAM++ 中英文模型（192 维，28 MB），向量归一化后按区间长度加权平均；聚类用它的 `FastClustering`。模型由[清单](../../deploy/models/campplus-zh-en-advanced.json)固定大小与 SHA-256，选型与阈值见[声纹评估](../research/voiceprint-evaluation.md)，随 release 以 `ai/speaker-embedder.onnx` 链接（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md) 的方式）。
- 单元：[`anas-voice.socket`](../../deploy/systemd/system/anas-voice.socket) 只让 `a-nas` 组连接；[`anas-voice.service`](../../deploy/systemd/system/anas-voice.service) 以动态身份运行，`PrivateNetwork`、只允许 `AF_UNIX`、看不到数据卷与产品服务状态目录，内存上限 1 GB，和 AI Worker 一样在 `anas-ai.slice` 中让出 CPU，安装器写入与 AI Worker 相同的 CPU 预留。
- 没有声纹 Worker 或它出错时，录音照常完成：发言人只按识别服务的标签分开、没有向量（`voiceprint_model` 为空），不参与比对。

## 豆包语音的密钥

- 管理员在设置中填写新版控制台的 API Key（`X-Api-Key`）或旧版控制台的 APP ID 与 Access Token（`X-Api-App-Key`、`X-Api-Access-Key`），保存在 `voice.db` 的 `settings` 表中；`voice.db` 在产品服务 0700 的状态目录里，其他账号读不到。接口只返回模式、APP ID、密钥的最后 4 位、设置人与时间（[`settings.go`](../../internal/voice/settings.go)）。
- `Doubao.Credentials` 在每次请求时读取，修改立即生效；保存前先用内嵌的一秒静音（[`silence.ogg`](../../internal/voice/silence.ogg)）测试，豆包语音回答“静音音频”即表示密钥可用。

## 关联

- 规格：[语音处理](../specs/voice-processing.md)
- ADR：[ADR 0027](../adr/0027-transcribe-in-the-cloud-and-keep-voiceprints-on-the-device.md)、[ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)、[ADR 0017](../adr/0017-run-the-media-center-through-the-file-broker.md)、[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)、[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)
- 研究：[声纹评估](../research/voiceprint-evaluation.md)
- 代码：[`internal/voice`](../../internal/voice/voice.go)、[`internal/voiceapi`](../../internal/voiceapi/handler.go)、[`internal/voiceworker`](../../internal/voiceworker/client.go)、[媒体 Worker 的 `speech`](../../internal/mediaworker/command.go)、[声纹 Worker](../../ai/anas_ai/voice.py)、[评估脚本](../../ai/eval/voiceprints.py)、[窗口](../../web/src/voice/VoicePanel.tsx)
