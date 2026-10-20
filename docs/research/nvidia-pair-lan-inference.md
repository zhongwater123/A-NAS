# NVIDIA PAIR 作为局域网 AI 算力的可行性

状态：评估完成（2026-10-20，只读官方仓库、文档与社区资料；尚未在容器或实机上运行 PAIR）。据此提出的决定见 [ADR 0026](../adr/0026-use-lan-computers-for-the-assistant-through-a-pair-node.md)（proposed），行为见[局域网算力规格](../specs/lan-ai-compute.md)。

用户希望 A-NAS 接入 NVIDIA PAIR（Personal AI Router）：NAS 部署 PAIR，家里其他电脑也部署，A-NAS 的一部分 AI 处理分发到这些电脑上。本文记录 PAIR 的一手事实、它与 A-NAS 各项 AI 工作的契合程度、接入形态的比较与尚待原型验证的问题。

## 结论

可行，但范围与形态都受限：

- **能分发的是大模型推理请求**：VNAS（[AI 助手](../specs/ai-assistant.md)）经模型网关发出的 OpenAI 兼容对话请求，包括流式、工具调用与看图。这正是 [ADR 0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md) 决定 7 等待的“本地模型”：8 GB、无独显的 NAS 跑不动 Hermes 需要的模型，家里装了 RTX 显卡的电脑可以，且数据不出家庭局域网。
- **相册 AI 不分发**：向量、物体检测、人脸与宠物不是大模型请求，PAIR 无从路由；向量必须出自同一个模型与预处理，而且这些任务在 NAS 的 CPU 上已经跑得动（见[逐项判断](#a-nas-的-ai-工作逐项判断)）。
- **NAS 必须自己是 PAIR 节点**：PAIR 的推理端口只接受本机的明文请求与已配对节点的 mTLS 请求，局域网里没有配对的机器按设计调不进去。NAS 以“不带推理引擎”的节点加入集群，VNAS 的模型网关改连本机的 `http://127.0.0.1:11434`。
- **主要代价**：PAIR 是 beta（v1.0.0 于 2026-10-13 发布），Linux 没有服务模式，需要 A-NAS 自己托管它的 broker；Debian 13 不在官方列出的系统中；NAS 要在局域网开放发现与配对端口；配对用 6 位 PIN，集群信任是传递的；VNAS 读到的文件内容会发到配对的电脑上处理。

## PAIR 是什么

| 方面 | 事实 | 来源 |
|---|---|---|
| 定位 | 局域网推理路由器：每个请求整体交给一个合格节点，不汇聚显存、不切分模型、不拆分请求 | README、`docs/architecture.mdx` |
| 引擎 | Ollama、LM Studio、llama.cpp；PAIR 可代为安装引擎、在节点上拉取模型 | README、`docs/engine-lifecycle.mdx` |
| 系统 | Windows 11、Linux、macOS 的 x64 与 arm64；产品页只列出 Ubuntu 24.04；PAIR 本身不要求 GPU，GPU 要求属于引擎 | README、产品页 |
| 发行 | v1.0.0：Electron 桌面版 `.deb`（约 140 MB，安装时给 `chrome-sandbox` 设 SUID 并装 AppArmor 配置）与不含界面的 `service-binaries-linux-x64.zip`（约 37 MB）；服务用 Go 编写、不启用 cgo | GitHub Releases、`docs/building.mdx`、`desktop/scripts/build/linux/after-install.sh` |
| 许可 | Apache-2.0；引擎与模型另有各自的许可 | README |
| 端口 | 代理接管各引擎的默认端口：Ollama 11434、LM Studio 1234、llama.cpp 8080，引擎本身移到回环上的其他端口 | `docs/architecture.mdx`、`services/nvpair-proxy/spec.md` |
| 接口 | Ollama 原生接口；三种代理都有 `/v1/chat/completions`、`/v1/completions`、`/v1/embeddings`、`/v1/responses`；Ollama 与 LM Studio 代理另有 Anthropic `/v1/messages`；`/v1/models` 合并全集群的模型 | `services/nvpair-proxy/README.md` |
| 谁能调用 | 端口监听所有地址，但明文 HTTP 只接受回环来源，局域网来源得到 403 `loopback-only`；mTLS 只接受已配对且证书已固定的节点，且这些请求只交给该节点自己的引擎、不再转发。没有 API 密钥 | `src/ingress.go`、`SECURITY.md` |
| 不带引擎的节点 | 文档明确：要让一台机器使用整个集群，就让它成为节点，它的本机代理会代它路由，无论它自己是否运行引擎；已有用户以无界面的 Linux 节点作为请求方。路线图中的“只发请求的轻量客户端”尚未提供 | `docs/architecture.mdx`、issue #91、README |
| 调度 | 先按模型过滤（节点的该引擎必须列出完全相同的模型名，没有则本机返回 502），再按“待处理数 + GPU 压力 + 本进程预留”选最空闲的；5xx、408、429、模型 404 与传输错误时换节点，最多 5 次、10 分钟内。调度只看负载，不看显存、GPU 型号与模型是否已加载 | `services/nvpair-proxy/spec.md` §5、`services/nvpair-job-scheduler/README.md`、`docs/known-issues.mdx` |
| 超时 | 120 秒内要拿到响应头，之后 120 秒内要有首个内容；流式请求在第一个字节后才算交付；冷加载超过 120 秒会变成 502（#57，可配置的修复未合并） | `services/nvpair-proxy/spec.md` §5.6、issue #57 |
| 配对 | 发现靠 mDNS（自带应答器，与 avahi 共存）或手动添加地址；配对用一次性的 6 位 PIN（EAP-NOOB，5 分钟过期）；之后节点间是 mTLS。信任是传递的：A 与 B、A 与 C 配对后 B 与 C 也互信 | `services/nvpair-cluster-manager/README.md`、`SECURITY.md` |
| 运行方式 | 没有 systemd 服务，也不需要 root，全部以登录用户运行，数据在 `~/.config/Nvidia Corporation/Personal AI Router`。无界面时用终端界面 `nvpair-tui`，退出或断开 SSH 即停止全部服务（官方建议放进 tmux）；服务模式是未完成的 #49 | `docs/terminal-interface.mdx`、issue #49 |
| 集成接口 | `nvpair-ui-broker` 是全部后端的父进程，监管 10 个工作进程，通过 stdio 或 `--ipc` 的 Unix 套接字与唯一的父进程说 JSON-RPC 2.0，父进程断开即退出；方法包括节点发现与订阅、配对邀请与应答、代理状态、作业订阅。维护者称它是受支持的集成契约，但尚无版本协商（#67） | `services/nvpair-ui-broker/README.md`、`desktop/docs/services-api.md`、issue #67 |
| 版本节奏 | 0.1.0（08-26）、0.1.1（08-28）、NVIDIA 博客发布 beta（09-03）、1.0.0（10-13）；`develop` 已有未发布的 1.0.1–1.0.6 | GitHub Releases、`CHANGELOG.md` |

## 接入形态比较

| 形态 | 判断 |
|---|---|
| **NAS 作为不带引擎的 PAIR 节点**，VNAS 的模型网关连本机 `127.0.0.1:11434` | 采用。这是 PAIR 文档给出的唯一正规用法；NAS 到电脑之间是 mTLS；换节点、负载均衡由 PAIR 负责，A-NAS 不重复实现 |
| NAS 不装 PAIR，直接请求电脑上 PAIR 的代理端口 | 不可行：局域网明文请求被拒（403 `loopback-only`），这是有意的设计 |
| 在电脑上加一个把局域网请求转成回环请求的中继，或用未合并的 API 密钥 PR #38 | 不采用。中继等于 `SECURITY.md` 所说的开放中继，任何局域网设备都能用；#38 仍在安全评审中 |
| 不用 PAIR，模型网关直连电脑上的 Ollama（`OLLAMA_HOST=0.0.0.0`） | 只作为不装 PAIR 的退路。没有加密与负载调度；电脑装了 PAIR 后 11434 归 PAIR、Ollama 移到回环，这条路就走不通 |

## 在 NAS 上运行 PAIR

- **用服务二进制包，不用 `.deb`**：`.deb` 是 Electron 桌面应用，会带入图形栈并设置 SUID 的 `chrome-sandbox`；服务二进制是不依赖 cgo 的 Go 程序，应能在 Debian 13 上直接运行（待验证）。按 [ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md) 的做法，固定版本、核对 SHA-256、随 release 送达，设备上不下载 PAIR、引擎或模型，也不使用 PAIR 的自更新与引擎安装。
- **由 A-NAS 托管 broker**：broker 必须有一个持续连接的父进程，因此 A-NAS 用一个专用服务启动它并保持 JSON-RPC 连接，同时向产品服务提供节点、模型与配对的窄接口（设置页需要）。
- **只启用需要的工作进程**：broker 只要求发现进程，其余缺失时降级运行。NAS 需要发现、Ollama 代理（`--proxy-engines ollama`：llama.cpp 代理默认要 8080，与只监听回环 8080 的产品服务冲突；LM Studio 代理用不到）、配对、作业中继、调度、设置、手动节点与错误同步。不提供引擎管理进程：NAS 不运行引擎，而它的 `ec` 远程控制面会让任何集群成员在 NAS 上安装、启动引擎、改引擎环境变量；也不提供节点信息进程：它在 14318 上以未认证的明文 HTTP 公开主机名与硬件信息，而 NAS 没有 GPU 可报告。缺少引擎管理进程时 Ollama 代理能否照常占用 11434 并路由，需要原型验证。
- **局域网端口**：照上面的取舍，NAS 开放 5353/udp（mDNS）、14319（错误同步，mTLS）、14320（作业中继，mTLS）与 14321（配对，PIN 认证后 mTLS）；11434 只对回环明文。不开放 14318、14322、14323。
- **沙箱**：专用服务账号、只写自己的状态目录、看不到数据卷与产品服务的状态，与相册 AI Worker、媒体 Worker 的隔离方式一致；它需要局域网，所以不能像它们那样 `PrivateNetwork=yes`。

## 与 VNAS 的契合

- **请求路径**：Hermes（沙箱内无网络）→ 模型网关的 Unix 套接字 → `http://127.0.0.1:11434/v1/chat/completions` → PAIR 选一台有该模型、最空闲的电脑 → 该电脑的 Ollama。模型网关只多了一种不带密钥的明文上游（[`gateway.py`](../../ai/assistant-spike/gateway.py)）。
- **流式与工具调用**：代理只读出 `model` 字段后原样转发请求体，流式在首字节后交付，工具调用不受影响。Hermes 用 `/api/show` 探测上下文长度时可能被路由到没有该模型的节点，导致工具调用变成纯文本（#94，修复在未发布的 1.0.6）；A-NAS 的网关不转发 `/api/show`，managed 配置写明 `context_length`，因此不受影响。
- **看图**：图片以 base64 放在请求体中，应能原样通过；代理不限请求体大小（#79 提议 32 MiB）。未见文档说明，需验证。
- **上下文长度**：OpenAI 兼容接口不能逐请求设置 Ollama 的 `num_ctx`，必须在每台持有该模型的电脑上用 PAIR 引擎设置中的环境变量 `OLLAMA_CONTEXT_LENGTH` 统一设置，并与 managed 配置的 `context_length` 一致；否则 Ollama 会静默截断 Hermes 约 13,000 字符的系统提示与工具定义。
- **模型命名**：每台电脑都按完全相同的 Ollama 标签持有模型（`qwen3.5:9b` 与 `qwen3.5:9b-q8_0` 是两个模型）。调度不看显卡强弱，所以只在跑得动的电脑上放 VNAS 的模型。
- **冷启动**：电脑上的模型被卸载后，下一次请求要重新加载，超过 120 秒会得到 502；把模型常驻显存又会影响玩游戏（#96）。默认按 Ollama 的 5 分钟保留，必要时调整 `OLLAMA_KEEP_ALIVE`。

## A-NAS 的 AI 工作逐项判断

| 工作 | 现在的运行方式 | 经 PAIR 分发 | 原因 |
|---|---|---|---|
| VNAS 对话、工具调用 | 开发期经模型网关转发云端 DeepSeek，仅用于测试 | 适合，首要目标 | OpenAI 兼容；NAS 跑不动所需模型；数据留在局域网 |
| VNAS 看图（`vision_analyze`） | 同上，试用期把 1600 像素的照片视图交给模型 | 适合，需选视觉模型并验证 | 同上；视觉模型显存需求更高 |
| 相册向量（EmbeddingGemma 2）、物体检测（D-FINE）、人脸（YuNet + AuraFace）、宠物（DINOv2） | NAS 上无网络的 AI Worker，经 Unix 套接字接收只读描述符 | 不适合 | 不是 Ollama/LM Studio/llama.cpp 的推理请求；向量必须出自同一模型与预处理，模型 ID 含权重哈希；Worker 无网络是 ADR 0016 的隐私承诺；NAS 的 CPU 已能完成 |
| 照片 OCR（候选 Qwen3.5-2B、GLM-OCR，尚未实现） | 计划在 NAS 上用 llama.cpp | 以后可选 | 技术上可以经 llama.cpp 代理分发，但会打破“照片只在这台设备上分析”，且各电脑的量化不同会让结果不可复现；需单独决定 |
| 影视、音乐的转码 | 媒体 Worker 中的 FFmpeg | 不涉及 | 不是推理 |

## 安全与隐私

- **隐私边界移动**：VNAS 读到的文件内容、照片元数据与（试用期的）照片画面会经 mTLS 发到配对的电脑上处理，电脑的使用者与电脑上的恶意软件可能看到。比云端好，但不再是“只在这台设备上”。这需要用户决定，界面也要如实说明。
- **配对的强度**：6 位 PIN 只防被动窃听与误加入，不防局域网上的主动中间人；信任是传递的，一台被攻破的成员可以替别人背书。官方要求只在受控网络中配对。A-NAS 只让管理员在设置中配对。
- **NAS 自身的暴露**：不运行引擎管理与节点信息进程时，配对节点无法在 NAS 上安装或启动任何东西，只能看到发现记录与作业事件；未配对的设备只能看到 mDNS 记录并访问 14321 的配对端点。
- **相册 AI 的承诺不变**：AI Worker 仍在无网络的沙箱中运行，PAIR 不接触照片库与数据卷。

## 成熟度与社区实践

- beta：1.0.0 两周前发布，`develop` 上持续有修复；集成契约未版本化；挂起（而非崩溃）的工作进程不会被发现（#6 中一个卡死的节点让集群慢了 2.1 倍）；Linux 服务模式（#49）、容器镜像（#32）、任意 OpenAI 后端（#24）都未完成。
- 社区：InfoQ 引述一位 Reddit 用户在三台 RTX 5090 之间分发 Qwen 27B，评价分发很省事；XDA 的上手文章经 Tailscale 配对后节点显示离线（mDNS 不穿隧道），需手写手动节点；NAS 厂商 ZimaSpace 的文章把 NAS 定位为 PAIR 电脑旁的常开存储，但没有任何 NAS 集成，并指出 PAIR 默认不是全网可用的 API。没有找到 Unraid、TrueNAS、Synology 或 Docker 的集成，也没有与 LiteLLM、GPUStack、llama.cpp RPC 的直接比较。

## 待原型验证

1. Debian 13 容器中，服务二进制包的 broker 能否以专用账号在 systemd 沙箱中、无图形环境下由 A-NAS 的父进程托管运行；不提供引擎管理与节点信息进程时，Ollama 代理能否占用 `127.0.0.1:11434` 并把请求路由出去。
2. 两个容器（一个当 NAS，一个运行 PAIR 与 CPU 上的小模型）配对后，VNAS 的模型网关经 PAIR 跑通 Hermes 的对话、流式、工具调用与看图。
3. broker 的 JSON-RPC 能否支撑设置页：列出节点与模型、发起与应答配对、显示作业。
4. 真实 RTX 电脑上 VNAS 的首字时间、冷启动与多成员并发；电脑关机、睡眠时 VNAS 的表现。

## 来源

- 官方：[NVIDIA 技术博客（2026-09-03）](https://developer.nvidia.com/blog/nvidia-pair-virtual-inference-router-expands-available-compute-on-your-local-network/)、[产品页](https://www.nvidia.com/en-us/ai-on-rtx/personal-ai-router/)、[FAQ](https://www.nvidia.com/en-us/ai-on-rtx/personal-ai-router/faq/)、[文档](https://docs.nvidia.com/local-ai/nvpair)、[仓库](https://github.com/NVIDIA/Personal-AI-Router)（`README.md`、`SECURITY.md`、`CHANGELOG.md`、`docs/`、`services/*/README.md` 与 `spec.md`，2026-10-20 的 `develop`）、[Releases](https://github.com/NVIDIA/Personal-AI-Router/releases)
- Issues 与 PR：[#6](https://github.com/NVIDIA/Personal-AI-Router/issues/6)、[#28](https://github.com/NVIDIA/Personal-AI-Router/issues/28)、[#49](https://github.com/NVIDIA/Personal-AI-Router/issues/49)、[#57](https://github.com/NVIDIA/Personal-AI-Router/issues/57)、[#67](https://github.com/NVIDIA/Personal-AI-Router/issues/67)、[#91](https://github.com/NVIDIA/Personal-AI-Router/issues/91)、[#94](https://github.com/NVIDIA/Personal-AI-Router/issues/94)、[#96](https://github.com/NVIDIA/Personal-AI-Router/issues/96)、[PR #38](https://github.com/NVIDIA/Personal-AI-Router/pull/38)、[PR #163](https://github.com/NVIDIA/Personal-AI-Router/pull/163)
- 社区：[InfoQ](https://www.infoq.com/news/2026/09/nvidia-pair-ai-task-router/)、[XDA](https://www.xda-developers.com/connected-two-pcs-one-ai-endpoint-nvidia-new-router-serving-engine/)、[ZimaSpace](https://shop.zimaspace.com/blogs/tech-ai-hub/nvidia-pair-home-network-local-ai-cluster-gpu-server)、[MarkTechPost](https://www.marktechpost.com/2026/09/04/nvidia-releases-personal-ai-router-pair-an-open-source-virtual-inference-router-that-distributes-local-ai-requests-across-rtx-dgx-spark-and-mac-nodes/)

## 关联

- ADR：[0026](../adr/0026-use-lan-computers-for-the-assistant-through-a-pair-node.md)、[0024](../adr/0024-run-a-sandboxed-hermes-agent-per-member.md)、[0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)
- 规格：[局域网算力](../specs/lan-ai-compute.md)、[AI 助手](../specs/ai-assistant.md)
- 代码：[`ai/assistant-spike/gateway.py`](../../ai/assistant-spike/gateway.py)、[`ai/assistant-spike/nas-trial.sh`](../../ai/assistant-spike/nas-trial.sh)
- 测试：[`ai/tests/test_assistant_gateway.py`](../../ai/tests/test_assistant_gateway.py)
