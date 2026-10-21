# 0027：语音处理在云端转写与整理，声纹只在本机计算和保存

状态：proposed（2026-10-21，随语音处理 PR 提交评审）；行为见[语音处理规格](../specs/voice-processing.md)，实现见[语音处理技术设计](../architecture/voice.md)，声纹模型与阈值的依据见[声纹评估](../research/voiceprint-evaluation.md)。

用户要求语音处理接入豆包语音的 ASR，翻译与总结沿用 AI 助手的 DeepSeek 模型，发言人用 embedding 模型按声纹识别与匹配（2026-10-21）。这让录音与转写离开设备，与相册 AI 不联网的承诺（[ADR 0016](0016-ship-photo-ai-as-a-built-in-offline-capability.md)）不同；而声纹是可以长期识别一个人的生物特征。我们决定：录音只在成员导入后，经火山引擎对象存储（TOS）短暂中转给豆包语音，转写交给 VNAS 的模型网关；声纹由一个没有网络、看不到数据卷的本机 Worker 计算，只保存在设备上，只在成员自己的录音之间比对。

## 决定

1. **录音留在原处**：语音处理不持有存储。录音是个人空间或共享空间里的普通文件，以稳定文件 ID 登记，只经文件服务以成员本人身份读取（[ADR 0008](0008-use-unified-linux-identities-and-filesystem-acls.md)），与音乐、影视中心相同（[ADR 0017](0017-run-the-media-center-through-the-file-broker.md)、[ADR 0018](0018-index-music-in-place-through-the-file-broker.md)）。从电脑导入的文件先上传到成员个人空间的“录音”文件夹。转写、发言人、人与声纹、译文和总结属于导入它的成员，保存在产品服务状态目录的 `voice.db`，其他账号看不到。
2. **转写用豆包语音录音文件识别标准版（模型 2.0），经 TOS 中转**：标准版是火山引擎文档中当前的录音文件识别（极速版已移到“历史文档”，只有模型 1.0），单个任务至多 5 小时、512 MB，长录音不必再分段，还有为长会议优化的说话人分离（`ssd_version` 300）。它只接受识别服务自己下载的 URL，而 NAS 在内网（[ADR 0012](0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)）：A-NAS 把转成 16 kHz 单声道 Opus 的录音放进管理员指定的 TOS 存储桶，交出 24 小时有效的签名链接，任务结束（成功、失败或录音被移除）立即删除对象。2026-10-21 用户在得知标准版只收 URL 后选择了这一方式。任务是异步的，提交后保存任务，产品服务重启后接着查询同一任务而不重新提交。豆包语音的密钥和 TOS 的存储桶与 Access Key 由管理员在语音处理中填写，保存在 `voice.db`，接口不回显。
3. **翻译与总结经 VNAS 的模型网关**：产品服务以 OpenAI 兼容的接口连接助手的模型网关（[ADR 0024](0024-run-a-sandboxed-hermes-agent-per-member.md)），网关持有 DeepSeek 的密钥并只记录用量。没有网关的设备不提供翻译与总结，转写不受影响。云端模型目前只用于开发与试用，与 ADR 0024 对助手的限定相同。
4. **声纹在本机**：声纹 Worker（`anas-voice.service`）以动态身份运行，`PrivateNetwork`、只允许 Unix 套接字、看不到数据卷和产品服务状态，只接收产品服务放进 memfd 的解码后样本，返回向量。模型（3D-Speaker CAM++，Apache-2.0）随 release 安装，按 ADR 0016 的方式由清单固定、设备不联网获取。向量只存于 `voice.db`，从不随请求发往云端。
5. **比对只在成员自己的录音之间**：一个人的声纹就是成员命名为此人的那些发言人的向量；删除录音或改掉名字即忘记那段发言。达到自动阈值才自动命名，较低时只建议、由成员确认；成员说“不是”的人不再给这位发言人。不跨成员比对、不建全局声纹库，管理员也看不到成员的人与声纹。

## 后果

- 每段录音都会上传到火山引擎：先在 TOS 存储桶中停留到识别结束，再由豆包语音下载；成员在导入对话框和设置页看到这一点。豆包语音按时长计费，A-NAS 不自动处理任何文件，只有成员导入的录音才发送。
- 管理员要多开通 TOS、建一个存储桶并给一组能在桶里写入和删除的 Access Key（建议子账号）。签名链接在 24 小时内对任何拿到它的人有效；对象在任务结束时删除，设备意外中断时可能留下对象，所以建议在桶上为 `a-nas-voice/` 设置 1 天过期的生命周期规则。
- 识别结果不再随请求立即返回：高峰时任务可能排队数小时，期间这段录音占着识别队列；超过一天没有结果即判为失败，可重新处理。
- 声纹是派生数据但能识别人：产品服务被攻破时可读到 `voice.db` 中的向量、转写和豆包语音的密钥。向量只对同一模型有意义，不能还原声音。
- 新增一个套接字激活的 Worker 单元、一个模型角色和 Python 运行环境中的 sherpa-onnx（约 15 MB）；运行环境的内容 ID 随之改变，部署时传一次新的运行环境。
- 没有声纹 Worker、没有密钥或没有模型网关时，对应部分降级，其余照常。

## 未选择

- **继续用极速版（直接上传音频）**：不需要对象存储和第二组密钥，录音不在云端停留；但它已移到“历史文档”、只有模型 1.0，单次至多 2 小时，长录音要分段后再按声纹拼接说话人。最初的实现用的是它，用户给出最新文档后改为标准版。
- **由 NAS 自己提供下载链接**：录音不经第三方存储，但需要把设备暴露到公网（端口映射或隧道），与 ADR 0012 的内网部署相反。
- **本机 ASR（Whisper、Paraformer 等）**：录音不出设备，但用户明确选择了豆包语音，而本机识别要在没有 GPU 的 NAS 上长时间占用 CPU。它可以作为以后的另一种 Recognizer，接口不变。
- **用豆包语音的说话人分离或它的云端声纹库代替声纹**：分离只在一次任务内区分说话人，不能跨录音认出同一个人；它的声纹匹配要先把成员家人的声纹注册到云端声纹库，与“声纹不离开设备”相反。
- **在相册的 AI Worker 里计算声纹**：可以少一个单元，但那个 Worker 一次只服务一个连接，相册的物体检测会挡住语音处理；它的套接字只对相册服务开放，再开给产品服务会混淆两者的边界。
- **在产品服务进程内运行模型（onnxruntime-go 等）**：少一次 IPC，但把 cgo、模型解析和大块内存带进持有会话与密钥的进程。

## 关联

- [语音处理规格](../specs/voice-processing.md)、[语音处理技术设计](../architecture/voice.md)、[声纹评估](../research/voiceprint-evaluation.md)
- 豆包语音：[任务提交](https://docs.volcengine.com/docs/DoubaoVoice/task-submission-http-1?lang=zh)、[结果查询](https://docs.volcengine.com/docs/DoubaoVoice/result-query-http-1?lang=zh)、[说话人分离最佳实践](https://docs.volcengine.com/docs/DoubaoVoice/speaker-separation?lang=zh)
- [ADR 0008](0008-use-unified-linux-identities-and-filesystem-acls.md)、[ADR 0012](0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)、[ADR 0016](0016-ship-photo-ai-as-a-built-in-offline-capability.md)、[ADR 0017](0017-run-the-media-center-through-the-file-broker.md)、[ADR 0018](0018-index-music-in-place-through-the-file-broker.md)、[ADR 0024](0024-run-a-sandboxed-hermes-agent-per-member.md)
