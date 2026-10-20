# 局域网算力

状态：draft（M1 模型网关已支持本机的 PAIR 代理；NAS 上运行 PAIR 与“设置”页尚未实现，待原型验证与用户决定隐私边界）
更新时间：2026-10-20

## 目标

让 VNAS 使用家里装了 NVIDIA 显卡的电脑运行模型：NAS 与这些电脑经 NVIDIA PAIR 配对，VNAS 的每次模型请求交给其中一台有该模型、最空闲的电脑，数据不离开家庭局域网，也不需要云端 API（[ADR 0026](../adr/0026-use-lan-computers-for-the-assistant-through-a-pair-node.md)，外部事实见[可行性研究](../research/nvidia-pair-lan-inference.md)）。

## 非目标

- 不分发相册 AI（向量、物体检测、人脸、宠物），它们仍只在 NAS 上运行；照片 OCR 是否分发另行决定。
- 不在 NAS 上运行推理引擎，也不让配对的电脑在 NAS 上安装或启动任何东西。
- 不在 A-NAS 中管理电脑上的引擎、模型与显卡设置；这些在电脑上的 PAIR 中完成，A-NAS 只检查并提示。
- 不经互联网、Tailscale 或其他 VPN 使用别处的电脑；不在电脑都关机时改走云端。
- 不让成员配对电脑或选择模型。

## 场景

### 默认关闭

- Given：新安装或升级后的设备。
- When：管理员打开“设置”。
- Then：“AI 算力”显示为关闭；NAS 不运行 PAIR、不在局域网开放任何新端口；VNAS 保持现有状态（开发期为云端试用或“规划中”）。

### 开启并发现电脑

- Given：管理员在“AI 算力”中开启；家里有电脑运行着 PAIR。
- When：页面载入。
- Then：NAS 开始以“这台 NAS”出现在电脑的 PAIR 中；页面列出发现的电脑（名称、地址、是否已配对、在线状态）。发现不到时可以按 IP 地址添加。

### 配对一台电脑

- Given：一台电脑已被发现、尚未配对。
- When：管理员点“配对”，页面显示一个 6 位 PIN，管理员在电脑的 PAIR 中接受邀请并输入它；或电脑先邀请 NAS，管理员在页面上输入电脑显示的 PIN。
- Then：5 分钟内完成时电脑显示为已配对，之后与它的通信经 mTLS；PIN 只能用一次，过期或输错时页面提示重新开始。页面说明：与一台电脑配对就会信任它已配对的其他电脑。

### 为 VNAS 选择模型

- Given：至少一台已配对的电脑上有模型。
- When：管理员打开模型选择。
- Then：只列出至少一台在线电脑上有的模型，每个模型标出在哪些电脑上；选定后 VNAS 的新对话使用它。所选模型的上下文长度在各电脑上不一致或小于 VNAS 的需要时提示管理员在电脑的 PAIR 中设置 `OLLAMA_CONTEXT_LENGTH`。

### 成员使用 VNAS

- Given：已选定模型，至少一台有该模型的电脑开着。
- When：成员在 VNAS 中对话。
- Then：请求经 NAS 上的 PAIR 交给一台有该模型、最空闲的电脑；回复照常逐字出现。VNAS 的隐私说明写明对话在家里的电脑上处理。

### 电脑都关机

- Given：有该模型的电脑都关机、睡眠或断网。
- When：成员在 VNAS 中对话。
- Then：VNAS 报告“模型暂不可用”，不改走云端；文件、相册与其他功能不受影响。电脑开机后下一次对话照常使用。

### 解除配对与关闭

- Given：已配对若干电脑。
- When：管理员解除某台电脑的配对，或关闭“AI 算力”。
- Then：解除后 NAS 不再把请求交给它；关闭后 NAS 停止 PAIR、关闭端口，VNAS 报告“模型暂不可用”。配对信息保留到管理员选择“忘记所有电脑”。

## 边界与失败

- 只有管理员能看到和操作“AI 算力”；成员只在 VNAS 中看到模型是否可用。
- PAIR 在 NAS 上以专用服务账号运行，看不到数据卷；它停止或崩溃时，VNAS 报告“模型暂不可用”（模型网关返回 502），其他功能不受影响（[架构不变量](../architecture/OVERVIEW.md#不变量) 1）。
- 模型冷加载超过 PAIR 的 120 秒时这次请求失败，成员重试即可。
- PAIR 的版本随系统固定，设备不联网更新它。

## 验收证据

- 模型网关以明文 HTTP 上游转发对话与流式回复、不带密钥，上游不可达时返回 502 JSON 错误，不转发 `/api/show`（[`test_assistant_gateway.py`](../../ai/tests/test_assistant_gateway.py)）。
- 试用脚本以 `ANAS_TRIAL_UPSTREAM=http://127.0.0.1:11434` 安装时不要求云端密钥，`check` 显示 PAIR 是否有所选模型（[`nas-trial.sh`](../../ai/assistant-spike/nas-trial.sh)）。
- 待实现：Debian 13 容器中两个 PAIR 节点配对后，VNAS 经 NAS 节点跑通对话、工具调用与看图；系统测试证明 NAS 上的 PAIR 读不到数据卷、不开放 14318、14322、14323。

## 关联

- 架构：[架构总览](../architecture/OVERVIEW.md)
- ADR：[0026](../adr/0026-use-lan-computers-for-the-assistant-through-a-pair-node.md)、[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)、[0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- 研究：[NVIDIA PAIR 的可行性](../research/nvidia-pair-lan-inference.md)
- 代码：[`ai/assistant-spike/gateway.py`](../../ai/assistant-spike/gateway.py)、[`ai/assistant-spike/nas-trial.sh`](../../ai/assistant-spike/nas-trial.sh)
- 测试：[`ai/tests/test_assistant_gateway.py`](../../ai/tests/test_assistant_gateway.py)
