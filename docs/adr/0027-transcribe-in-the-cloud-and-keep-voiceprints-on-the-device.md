# 0027：语音处理在云端转写与整理，声纹只在本机计算和保存

状态：proposed（2026-10-21，随语音处理 PR 提交评审）；行为见[语音处理规格](../specs/voice-processing.md)，实现见[语音处理技术设计](../architecture/voice.md)，声纹模型与阈值的依据见[声纹评估](../research/voiceprint-evaluation.md)。

用户要求语音处理接入豆包语音的 ASR，翻译与总结沿用 AI 助手的 DeepSeek 模型，发言人用 embedding 模型按声纹识别与匹配（2026-10-21）。这让录音与转写离开设备，与相册 AI 不联网的承诺（[ADR 0016](0016-ship-photo-ai-as-a-built-in-offline-capability.md)）不同；而声纹是可以长期识别一个人的生物特征。我们决定：录音只在成员导入后发往豆包语音，转写交给 VNAS 的模型网关；声纹由一个没有网络、看不到数据卷的本机 Worker 计算，只保存在设备上，只在成员自己的录音之间比对。

## 决定

1. **录音留在原处**：语音处理不持有存储。录音是个人空间或共享空间里的普通文件，以稳定文件 ID 登记，只经文件服务以成员本人身份读取（[ADR 0008](0008-use-unified-linux-identities-and-filesystem-acls.md)），与音乐、影视中心相同（[ADR 0017](0017-run-the-media-center-through-the-file-broker.md)、[ADR 0018](0018-index-music-in-place-through-the-file-broker.md)）。从电脑导入的文件先上传到成员个人空间的“录音”文件夹。转写、发言人、人与声纹、译文和总结属于导入它的成员，保存在产品服务状态目录的 `voice.db`，其他账号看不到。
2. **转写用豆包语音的极速版**：一次请求带上音频、返回结果。标准版只接受识别服务自己下载的公网 URL，家里的 NAS 没有；把录音先传到对象存储再给 URL，会多一个云端副本和一组密钥。极速版单次至多 2 小时、100 MB，A-NAS 每一小时为一段，转成 16 kHz 单声道 Opus 后上传。密钥由管理员在语音处理中填写，保存在 `voice.db`，接口不回显。
3. **翻译与总结经 VNAS 的模型网关**：产品服务以 OpenAI 兼容的接口连接助手的模型网关（[ADR 0024](0024-run-a-sandboxed-hermes-agent-per-member.md)），网关持有 DeepSeek 的密钥并只记录用量。没有网关的设备不提供翻译与总结，转写不受影响。云端模型目前只用于开发与试用，与 ADR 0024 对助手的限定相同。
4. **声纹在本机**：声纹 Worker（`anas-voice.service`）以动态身份运行，`PrivateNetwork`、只允许 Unix 套接字、看不到数据卷和产品服务状态，只接收产品服务放进 memfd 的解码后样本，返回向量。模型（3D-Speaker CAM++，Apache-2.0）随 release 安装，按 ADR 0016 的方式由清单固定、设备不联网获取。向量只存于 `voice.db`，从不随请求发往云端。
5. **比对只在成员自己的录音之间**：一个人的声纹就是成员命名为此人的那些发言人的向量；删除录音或改掉名字即忘记那段发言。达到自动阈值才自动命名，较低时只建议、由成员确认；成员说“不是”的人不再给这位发言人。不跨成员比对、不建全局声纹库，管理员也看不到成员的人与声纹。

## 后果

- 每段录音都会上传到火山引擎；成员在导入对话框和设置页看到这一点。豆包语音按时长计费，A-NAS 不自动处理任何文件，只有成员导入的录音才发送。
- 声纹是派生数据但能识别人：产品服务被攻破时可读到 `voice.db` 中的向量、转写和豆包语音的密钥。向量只对同一模型有意义，不能还原声音。
- 新增一个套接字激活的 Worker 单元、一个模型角色和 Python 运行环境中的 sherpa-onnx（约 15 MB）；运行环境的内容 ID 随之改变，部署时传一次新的运行环境。
- 没有声纹 Worker、没有密钥或没有模型网关时，对应部分降级，其余照常。

## 未选择

- **本机 ASR（Whisper、Paraformer 等）**：录音不出设备，但用户明确选择了豆包语音，而本机识别要在没有 GPU 的 NAS 上长时间占用 CPU。它可以作为以后的另一种 Recognizer，接口不变。
- **用豆包语音的说话人分离代替声纹**：它只在一次请求内区分说话人，不能跨录音认出同一个人，长录音各段之间也对不上。
- **在相册的 AI Worker 里计算声纹**：可以少一个单元，但那个 Worker 一次只服务一个连接，相册的物体检测会挡住语音处理；它的套接字只对相册服务开放，再开给产品服务会混淆两者的边界。
- **在产品服务进程内运行模型（onnxruntime-go 等）**：少一次 IPC，但把 cgo、模型解析和大块内存带进持有会话与密钥的进程。

## 关联

- [语音处理规格](../specs/voice-processing.md)、[语音处理技术设计](../architecture/voice.md)、[声纹评估](../research/voiceprint-evaluation.md)
- [ADR 0008](0008-use-unified-linux-identities-and-filesystem-acls.md)、[ADR 0016](0016-ship-photo-ai-as-a-built-in-offline-capability.md)、[ADR 0017](0017-run-the-media-center-through-the-file-broker.md)、[ADR 0018](0018-index-music-in-place-through-the-file-broker.md)、[ADR 0024](0024-run-a-sandboxed-hermes-agent-per-member.md)
