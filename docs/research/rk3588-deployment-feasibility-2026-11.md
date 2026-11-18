# RK3588 迁移可行性评估（2026-11）

> 状态：Debian 系统与固定主干 `5a3ceb79` 的 24 包已安装；管理员与 NVMe 数据卷已创建，Landlock ABI 6、文件代理 45 项、Btrfs/ACL/快照及正式 Worker 的两次 4B 请求通过运行检查。自动休眠已修，五寸屏 Mali 控制台已启动；触控、输入、联网与完整功能验收未完成。
> 更新日期：2026-11-18
> 已部署候选基准：`5a3ceb79daa9e8c2a1881e890f0ab78f4c21c346`，加本文说明的本地 ARM 适配与测试时钟修复；`b9209e01` 和 `c738affa140b` 候选保留作对应版本证据与回退。
> 目标：正点原子 ATK-DLRK3588，实测约 8GB RAM、64GB eMMC 与 256GB NVMe；已由原 Buildroot/Linux 5.10 启动至 Debian 13 与补齐能力的 `6.12.69-anas1`，保留原分区表和 recovery。

## 结论

**候选 Debian 系统、补齐后的内核与最新主干 24 包已在目标板安装，核心服务与数据卷已启动；完整产品仍需触控、网络及功能验收。** `6.12.69-anas1` 补齐厂商原镜像缺少的 Landlock、Btrfs 等能力，真机 ABI 为 6，普通用户文件代理测试 45 项通过。原厂独立 4B 探针的票据全文约 126 秒，全文加一次字段提取约 254 秒；正式 Worker 的 cgroup 实测单独记录，不沿用此性能承诺。小图漏读与严格字段断言失败均保留，不能称全部质量检查通过。后续按[候选验收手册](../runbooks/prepare-rk3588-candidate.md)执行。

| 范围 | 当前判断 | 决定性证据与未完成条件 |
|---|---|---|
| Linux ARM64 产品服务、Host Agent、文件代理等 | 全包遍历与 ARM 内核隔离补测完成，板端服务待验收 | CGO 四命令、非 root 仿真、真实 ARM Linux 6.12.107 文件代理测试，见[本地验证边界](#本地验证边界) |
| eMMC 系统盘 + PCIe NVMe 数据卷 | 优先验证的布局 | 板卡有 eMMC 与 M_KEY PCIe 接口；代码识别未知盘已改善，但数据卷初始化仍拒绝未知传输和 USB，见[存储](#存储) |
| ARM `.deb` 与运行环境 | 候选包已生成；正式软件源与整机更新仍待适配 | 24 包架构/内容/依赖模拟通过；现有 x86 UKI 不适用于板卡，见[发行链](#发行链) |
| 非 OCR 相册本地 AI | ARM 真实模型已运行，真实性能待测 | D-FINE/EmbeddingGemma 回归及 YuNet/AuraFace/DINOv2 合成输入推理通过，见[本地验证边界](#本地验证边界) |
| Qwen3.5-4B OCR，保持现有 GGUF、提示词和核对层 | 目标板 CPU 已实测；精度未全通过 | 票据全文约 126 秒，全文加字段约 254 秒；独立进程匿名内存峰值约 3.89 GiB，完整服务并发尚未验收 |
| GPU Vulkan | 可做第二阶段原型 | 同版 llama.cpp 有 ARM64 Vulkan 制品；仍需候选镜像的 GPU 驱动、算子和沙箱设备访问验证 |
| NPU，RKNN + RKLLM | 需单独适配和质量回归 | 官方已有 Qwen3.5-4B 文本基准与 Qwen3.5 视觉转换路径；不能把 0.8B 多模态结果当成此项目 4B OCR 的证明 |
| 影视转码、Kiosk、应用中心、系统更新等完整体验 | 各自验收，不能由 CPU 架构推断 | 默认 `libx264`、设备沙箱、容器架构和板级启动方式都需要检查 |

这不是更换 OCR 模型的建议。继续以现有 Qwen3.5-4B 为质量基线；是否接受某种加速实现，取决于同一测试集的字段准确率、逐 token 概率、核对结果、取消行为及整机资源表现。[OCR 模型决定](../decisions/2026-10-30-ocr-model.md)、[OCR 实施方案](../architecture/photo-ocr.md)

## 证据等级与范围

本文区分三类证据：仓库固定 SHA 的实现事实、厂商/上游官方能力、目标板尚需实测的条件。其他硬件上的测试只说明原测试环境；官方基准也不等于 A-NAS 工作负载。

本次不访问实验 NAS；已在完整备份和恢复入口验证后覆盖目标板 p3 boot、p6 rootfs，保留 GPT、loader、uboot、recovery，未初始化 NVMe。实机探针目录为 `/userdata/anas-rk3588-probe-20261117`，进程及端口转发已停止；激活和回滚文件保存在独立 p8 目录，具体范围见[试启动手册](../runbooks/build-rk3588-kernel.md#在原分区表上试启动)。构建与磁盘集成测试使用专用文件和可丢弃 loop 设备。现有 NAS 的数值来自仓库中的[模型评估](photo-ocr-model-evaluation.md)，本次没有重新测量。AI 助手使用云端模型；4B 是相册证件与票据 OCR。[当前状态](../status/CURRENT.md)、[系统边界](../architecture/OVERVIEW.md)

### 目标板 4B 实测与资源修正

2026-11-17 在 ATK-DLRK3588 原 Buildroot/Linux 5.10.160 上执行独立 CPU 探针。模型、mmproj、llama.cpp b11655 均来自已校验 ARM 候选，板端再次核对 SHA-256；只在探针目录放置隔离的 Debian 库，不替换系统库。进程为 `nobody`，上下文 8192、视觉 token 上限 1024、输出上限 2048、贪心非思考、mmap，线程数单独对比；使用 ADB 转发回环 HTTP，产品传输使用 UDS，因此此处不是完整服务验收。

| 输入与设置 | 墙钟耗时 / 首文本 | 结果与边界 |
|---|---|---|
| `123` 小图，6 线程 | 5.643 / 5.258 秒 | 返回 `12`，严格转写失败；与此前 ARM 仿真 token 和概率相同，x86 也漏读 |
| 1200×1600 合成英文票据全文，6 线程 | 125.678 / 108.926 秒 | 1002 prompt token，解码 4.835 token/s；去除空白后的六行文本符合预期，原样空白不同 |
| 同票据自定义英文字段请求，6 线程 | 128.418 / 116.509 秒 | 返回 fenced JSON，编号与日期正确，金额为 `CNY42.50`；严格断言要求裸 JSON 和 `42.50`，因此失败 |
| 同票据全文，4 线程 | 125.699 / 111.625 秒 | 解码 5.762 token/s，但总耗时无明显改善；保持产品 6 线程默认策略 |

字段探针使用自定义英文 schema，不等同产品中文票据模板与核对层。产品解析器支持 fenced JSON；本结果不能直接断言产品解析失败，也不能替代产品端到端字段及低置信核对验收。数据只有上述合成样本，未覆盖中文证件、多页或完整精度数据集。

6 线程 51 次采样中，子进程 RSS 峰值 6095.43 MiB，匿名内存峰值 3987.55 MiB，整机 `MemAvailable` 最低 3225.98 MiB，最高温度 58.23°C；4 线程 26 次采样最高温度 57.31°C。RSS 包含映射且可回收的模型页，不能与匿名内存或 cgroup 用量混用。这是原厂空闲系统上的短测，不证明完整产品并发或长期散热安全。

旧 OCR 合计准入预算 3.5 GiB 来自 x86，低于本次 ARM 匿名峰值。新增可选 `memoryBytesByArchitecture`，ARM 语言模型预算 3.5 GiB，加 mmproj 0.75 GiB，合计 **4.25 GiB**；其他架构保持原预算。此值给准入留出余量，仍须在 Debian 产品服务的真实 cgroup 中确认。[预算清单](../../deploy/models/qwen3.5-4b-q4_k_m.json)、[清单解析](../../ai/anas_ai/providers.py)、[回归测试](../../ai/tests/test_model_memory.py)

原始证据在本机 `.cache/rk3588-board-20261117/` 的 `board-4b-results.json`、`board-4b-resource-summary.json`、两份监控 JSONL 与服务器日志；失败结果未删除。最新主干与旧候选的模型权重、运行时和生产 OCR 提示词一致，本轮只修正 ARM 内存准入声明。

### 正式 Worker 的 4B 实测

2026-11-18 在已安装的 Debian 产品上复用项目协议测试客户端，通过真实 `anas-ai.socket` 向 `anas-ai.service` 传递同一合成英文票据的文件描述符。未修改服务沙箱、CPU 或内存上限；它仍以 DynamicUser、PrivateNetwork、AF_UNIX 和独立 cgroup 运行，同时核心产品与 Web 搜索均已启动。没有数据卷，也没有用户文件并发；此处覆盖 Worker，不覆盖照片入库、后台门控、生产中文字段模板及核对层。

| 项目 | 实测 |
|---|---|
| 全文 | 177.640 秒，正常返回；去除空白后六行内容符合预期 |
| 自定义字段 | 136.037 秒，正常返回 fenced JSON，编号、日期及 `CNY 42.50` 与独立探针一致 |
| 两请求合计 | 313.677 秒，约 5.2 分钟；首请求包含 llama 进程启动，未单独测首 token |
| cgroup 峰值 / 上限 | 均为 5,611,085,824 字节，约 5351 MiB |
| cgroup 匿名峰值 | 4,473,122,816 字节，约 4266 MiB |
| 全机 MemAvailable 最低 | 2,527,453,184 字节，约 2410 MiB |
| 最高温度 | 56.384°C，157 次、约 2 秒间隔采样 |
| 内存事件 | `max=5883`，`oom=0`、`oom_kill=0`，没有重启 |

`info.ocrMemoryBytes` 为 6,236,397,568 字节，其中包含本次启动时约 1595 MiB 的 Worker 基线和 4.25 GiB OCR 声明；不能把声明合计、cgroup 实占、匿名内存或 RSS 互换。当前准入算法扣除 Worker 已持有部分，完整后台调度仍须在数据卷就绪后验证。[内存门控](../../internal/photoservice/gate.go)、[Worker 预算](../../ai/anas_ai/worker.py)

两次请求都能完成，证实当前产品限制下 4B 可运行；触及上限并发生回收，仍需检验真实图库并发、长时稳定性与精度。正式测试与原厂独立探针有启动、缓存和整机服务差异，不能把全部耗时差归因于 cgroup。测试后通过现有 release 协议退出 Worker，socket 保持 active，MemAvailable 恢复约 6.7 GiB。完整输入、输出、logprobs、资源 JSONL、汇总及安装证据保存在本机 `.cache/rk3588-board-20261117/installed-board-evidence/`。

### 最新主干与补齐后的内核

固定 `5a3ceb79` 已通过原生完整 `make check-steps`（Go 1.27.4；Web 1020 项、Go 全包、静态与漏洞检查、AI 和 SDK）；修复了伙伴测试中完成气泡仍处于 5 秒合并窗口的问题，只改变测试时钟。首次 ARM 结果保留 13 项失败：三个 Landlock 用例属于 linux-user 限制，九项 Web 用例源于原生首轮提前失败导致未生成静态资源，一项照片搜索未定位。完成原生门禁并重新生成资源后，ARM Web 全包通过；照片搜索在 QEMU 和板端各重复五次通过。不能把原始 ARM 遍历写为全绿。

最终 24 包审计覆盖 202 个 AArch64 ELF、8 个模型和 8 个嵌套归档，无错误；2214 件源码/链接与固定 Git 归档加限定补丁重建一致。六个版本包从 `b9209e01` 升至 `1.0.1~rc.6+dev20261118.100+g5a3ceb79daa9`，18 个内容包完整哈希相同。交付在 `I:\A-NAS-RK3588\product-5a3ceb79`；包清单 SHA-256 为 `3e8979624a26864296b3e4ad6099eff9c57c15f896aaf242e03c1375877e7faa`，六包升级清单为 `4f90a7719012c9af5356373767f7f79bf085ec2f07a4918486d0fb69b61babc5`。板端模拟及实际升级均为 6 升级、0 新装、0 移除，退出 0、dpkg audit 为空，数据卷 UUID 和 hold 不变，健康检查未新增失败。升级前配置与三个 SQLite 的一致性备份只保存在板端 root 专属 `/root/anas-before-5a3ceb79-20261118`，不复制用户状态到报告。后续小屏版已完成正常重启回归，见[启动调查](../investigations/2026-11-18-rk3588-offline-host-agent-start.md)。

五寸屏原生 1080×1920，当前按竖屏 scale=3、360×640 逻辑视口运行；DSI-2 已关闭。专用 kiosk 缺 `/dev/mali0` 及 `/dev/dma_heap/system-uncached` 权限会使 GBM 创建失败，已用针对这两个节点的 seat/uaccess 规则修复，未加入全局 video/input 组。重新启动后 Cage、Chromium 和 Mali 库加载正常，DRM 实机截帧显示完整登录页。触控设备为 `dsi0_ts_gt9xx`；物理点击、最终方向与文字输入仍须验收。[五寸屏适配手册](../runbooks/adapt-rk3588-five-inch-display.md)

以下为此前 `b9209e01` 的构建与系统激活证据，不能误作本次新主干完整重测。旧候选版本为 `1.0.1~rc.6+dev20261117.97+gb9209e014b4f`，源码副本在 I 盘专用 ext4 中，部署目录为 `I:\A-NAS-RK3588\product-b9209e01`。它包含 `b9209e01` 及本地 ARM 构建与上述内存修正，不能仅凭提交号重建。旧候选不被覆盖，哈希校验后的不可变内容包可复用，新核心包与 keyring 必须同版本重建。

已封存部署目录的 24 个 `.deb` 合 4,526,362,922 字节；加入板商安装计划后的 `packages/SHA256SUMS` 为 `5a057ee41120c897a7179862e742d339b7459c0d77424530fea82f0f5a29b02a`。此前清单已全部回验；更新说明时逐包文件指纹与审计一致，保留不可变 `.deb` 哈希，仅重算说明与计划文件，保留旧清单。`metadata/source.tar.gz` 为 `b8e68951ba83c5daa5e8c1715223e32d87579026a8a55d9ac683d9cb2c2517f2`。`VALIDATION.json` 明确保留 `allChecksPassed=false`：软件门禁及仿真补证不等于板端完整产品、精度与性能验收。

原生检查中先修正 Go 工具选择到仓库固定的 1.27.4，再以 UID 1001 运行需要普通用户权限的测试；保留此前错误环境的失败日志。正确工具下静态、安全、前端检查、Go 全包与构建通过；原生 AI 执行 129 项，其中 110 通过、19 条件跳过，独立 MCP SDK 18 项全部通过。ARM 遍历 121 包：87 包通过、33 包无测试，rootbroker 一包内三个 Landlock 调用受 QEMU linux-user 限制失败；2389 个测试/子用例事件通过、6 项条件跳过。ARM Python 执行 129 项，其中 114 通过、15 条件跳过。最新 root integration 退出 0；最新 systemd 安装、升级、失败回滚和中断恢复退出 0，646 行 `PASS`、`failures=0`；它使用测试替身，不作为真实模型证据。

最新候选 24/24 包的架构、版本、依赖和内容审计无错误，空状态 ARM apt 安装模拟退出 0。18 个不可变包逐件重查完整 `.deb` 哈希，与旧候选一致后复用旧载荷审计；6 个同版本核心/keyring 包重新扫描。合计覆盖 202 个 AArch64 ELF、8 个模型文件与8 个嵌套归档，核心 OCR 清单还逐键核对 ARM 预算。未重新验证已经删除的独立上游归档，不将旧归档检查冒充本次重测。另从最新 Git 归档加限定 ARM 补丁重建，2211 件源码文件/链接与测试副本一致，排除了根工作区无关未提交变更。

对官方根文件系统真实 dpkg 状态的补测发现：1739 个已装包中 1735 个为 hold；冻结的 Samba 库阻止安装当前 Samba，厂商 `chromium-ozone-wayland` 132.0.6834.83 又与控制台的标准 Chromium 冲突。确认没有已装 armhf 包，不能把多架构索引当作修复。仅在模拟状态副本解除七项 Samba 家族的 hold 后，23 包主服务计划退出 0（7 升级、37 新装、0 移除）；24 包计划明确以 Debian Chromium 150.0.7871.181 替换厂商浏览器后也退出 0（7 升级、54 新装、1 移除，未移除 GPU 包）。板端未应用这些更改，显示与视频仍须验收。七项名单、原版本、状态摘要及两套计划在交付目录 `TARGET-INSTALL-PLAN.json` 与 `validation/target-*-resolved.log`，操作见[候选验收手册](../runbooks/prepare-rk3588-candidate.md)。

`6.12.69-anas1` 从原镜像内嵌配置构建，50 项最终配置检查全部通过。加入 Landlock、Btrfs、cgroup 内存/PID/BPF、容器网络及 Overlay 等能力；仅修复厂商 Wi-Fi 外部输出构建的重复源码路径，未删除驱动。最终 Image 内嵌配置与构建配置逐字节一致，FIT 含三套屏幕 DTB 与厂商资源，大小约 45 MiB，小于原 64 MiB boot 分区。Image SHA-256 为 `ae3090ac0aab812a6dc8a3db19de6ca336c2eafdfec2733b0de118d5d121dd99`，boot FIT 为 `e4ee86c02e931f4a2a58e61e1c673b4de2751d78dbd5fbd88eee21979ca2c272`。

同一最终内核在整机 ARM 仿真中实际验证 Landlock ABI 6、默认活动 LSM、cgroup memory/pids、产品 nftables 规则和 Btrfs 只读快照，最新源码的非 root 文件代理 45 个顶层测试全通过。QEMU virt 缺 Rockchip EL3 固件，首次启动在 DRM 初始化调用 SIP 时 panic；保留日志后，仅仿真命令行加入 `initcall_blacklist=rockchip_drm_init` 完成上述测试。**板端不使用该参数，显示/GPU/RK 固件尚未验证**，不能把此次仿真表述成板级完整启动成功。

SDK 内核提交为 `c2107791c27aa499f51dd4531b885bdf5cea4f3a`，GCC 14.3.1；配置差异、Wi-Fi 补丁、50 项检查、FIT 清单、`kernel-vm-console.log` 及退出码保存在同一缓存目录。交付在 `I:\A-NAS-RK3588\firmware`，`anas-boot.img` 配套 `modules-6.12.69-anas1.tar.gz`，159 个模块均核对 AArch64 和相同 vermagic；不复用原内核模块。部署前须按[内核构建手册](../runbooks/build-rk3588-kernel.md)保留原系统备份与恢复入口，再作板端验收。

同目录 `rootfs.img` 为官方 Debian 根文件系统的候选副本，已加入匹配模块、临时 USB 调试与受 CID/UUID 保护的根文件系统扩容单元，屏蔽厂商自动调整分区；候选修改后 fsck 退出 0，新增单元通过主机静态验证，原镜像新版本 unit 的解析提示保留。仅加入现有主机 ADB 公钥，未复制主机 ADB 私钥；修正了一项厂商 unit 的可写权限。当前根镜像 SHA-256 `25f98190ea5f77b174dba522259b34f5af9abe43f8d2af1a5891cdee6440f26f`，完整清单为 `CANDIDATE-SHA256SUMS`。它尚未启动，项目软件包另行安装，不称作已装好产品的正式系统镜像。

### 备份、连接与当前激活条件

2026-11-18 将三个原厂 ext4 挂载临时改为只读，完整读取 eMMC，源端同步 SHA-256，主机经 base64 解码 gzip 后核对原始字节数和摘要。有效备份为 `I:\A-NAS-RK3588\board-backup-20261118\original-emmc-v3.img.gz`：解压 62,545,461,248 字节，SHA-256 `b91f69f8f72cea0d536000c7b0b5ee70ffe441cd574425c0acfa20e3a4f5385e`；压缩文件 4,226,018,381 字节，SHA-256 `6237c1faeac15394e65d73310f554b80e1961c7089ffbf896d68aea1ab9a3a00`。原挂载已恢复读写。此前解码错误和主动取消的单核慢速备份均保留，不能用于恢复。

另独立保存并回验 boot0、boot1（各 4 MiB）、原 boot、recovery 与 misc；两硬件 boot 区当前均为全零。原 recovery FIT 的 ramdisk 已按内嵌 SHA-256 核验并审查 init/ADB 脚本，misc 无更新或清空数据请求；这些检查仍不能替代真实恢复启动。备份证据为该目录的 `emmc-backup.json`、`boot-backup.json`、`hardware-areas.json`。NVMe 未写入，也不在本次 eMMC 备份内。

UART 已恢复为 `USB-Enhanced-SERIAL CH343 (COM5)`，USB 身份 `VID_1A86/PID_55D3`、转接器序列号 `UART_ADAPTER_SERIAL_REDACTED`。以 1500000 baud、8N1 收到 root 控制台，并实际执行只读命令，确认原厂内核 5.10.160 与目标 eMMC CID；原始记录为 `.cache/rk3588-board-20261117/uart-probe.log`。控制台带 ANSI 颜色，主机探针须先剥离控制符再匹配提示符，不能将匹配失败误判为串口断开。

UART 与 ADB 已同时恢复。已实际通过 ADB 进入原 recovery，再用 UART 中断 U-Boot、只读加载原 recovery FIT 到 RAM、经 `boot_fit` 的 SHA-256 校验并启动，ADB 重新上线；根为 RAM `rootfs`，p6 未挂载。此时命令行仍带 `androidboot.mode=normal`，不能用该字段代替根挂载与恢复镜像的验证。操作与回滚约束见[试启动手册](../runbooks/build-rk3588-kernel.md#在原分区表上试启动)；串口原始证据为 `recovery-boot-uart.log`、`uboot-recovery-console.log`、`uboot-direct-recovery-boot.log`。

原厂系统的 eth0、eth1、wlan0 均为 DOWN，没有可用网络地址。已下载 37 个 Debian 依赖（130,500,320 字节），与 24 个候选包组合后，在断网、空 apt 索引环境模拟安装通过，计划为 7 升级、54 新装、仅移除厂商浏览器；记录在 `I:\A-NAS-RK3588\debian-dependencies-b9209e01`。用户已授权验证通过后部署最新项目，无须重复索要许可。

2026-11-18 激活脚本完成 `COMMITTED apply`：p6 候选 6,518,996,992 字节回读摘要为 `25f98190ea5f77b174dba522259b34f5af9abe43f8d2af1a5891cdee6440f26f`；p3 填充后的完整 64 MiB 回读摘要为 `4a8e7c598067c87b02fb44ccf40d65c4b4430071c35c7a14f335f3d56e94dd0a`。eMMC 前 16 MiB、尾部 GPT 与原 recovery 的摘要均保持不变。随后经 UART 的 `boot_fit` 启动，日志显示 `Linux version 6.12.69-anas1`、`landlock: Up and running`、Debian 13 及 root 控制台；没有使用仿真专用 DRM blacklist。证据在本机 `.cache/rk3588-board-20261117/activation-board-evidence/` 与 `candidate-first-boot-uart.log`。

首次引导的两个失败已定位并修复：根扩容设备号比较误用了 `findmnt` 带填充空格的列输出，改成 `--raw`；USB 改为启动 adbd 后立即重试绑定已实测的 `fc000000.usb`，不再先等端点，也不要求全机唯一 UDC。备份原脚本后保留全部身份门禁，根文件系统扩展至原 14 GiB 分区；校准 UTC/RTC 后受控重启，ADB 自动恢复、时钟保持正确、根容量不变。真机预检确认 Debian 13、glibc 2.41、Python 3.13、Landlock ABI 6、memory/pids cgroup 与 NVMe 枚举；同版 ARM 文件代理测试以 `rockchip` 普通用户执行，45 个顶层测试通过。本次匹配内核模块还纠正为 root 所有。修复流程见[首次引导服务修复](../runbooks/build-rk3588-kernel.md#首次引导服务修复)。

24 个项目包及 37 个依赖均经板端 SHA-256 检查，实际 dpkg 状态摘要与模拟基线一致。只解除七项 Samba 冻结、仅移除厂商浏览器；首次 APT `--no-download` 安装在移除浏览器后报本地归档路径不是绝对路径，未解包项目。将归档在专用 p8 缓存中以标准 APT 名称硬链接后，重审剩余计划（7 升级、54 新装、0 移除）再继续，安装退出 0、`dpkg --audit` 为空。部署日志保存在 `/userdata/anas-product-b9209e01-20261118`；原失败记录保留。根分区约使用 11 GiB、可用 2.3 GiB，后续仍需规划系统内容与数据卷容量。

Host Agent、API、相册、分享、家庭、Web 搜索、Caddy 与 AI/FFmpeg/向量代理套接字均已启动，`/healthz` 返回 200。首次临时更新 mask 导致健康检查识别到单元覆盖，已撤销 mask，改为禁用尚未验收的自动更新 path/timer，不修改检查器。当前健康检查仅余无网口地址的 SMB 失败：`end0/end1` 均 NO-CARRIER，site 先选 `end0`。USB 直连产品端口的 `http://localhost:18081/` 返回 200，用户已在该入口创建首个管理员，状态转为 `setupRequired=false`；经 Caddy 的 18080 入口会因代理标记拒绝首次设置，不能混用。

NVMe 为 `Acer SSD N3500CN 256GB`，序列号 `BOARD_NVME_SERIAL_REDACTED`，256,060,514,304 字节，by-id 为 `nvme-Acer_SSD_N3500CN_256GB_BOARD_NVME_SERIAL_REDACTED`。创建前只读检查为空 MBR，wipefs 显示 `0x1fe dos`；计划未列出此签名，最终确认前已明确说明会清除 SSD 现有分区表和数据。原计划过期后，用户重新生成并在网页确认创建：`PLAN_ID_REDACTED` 已 succeeded，动作包括清签名、GPT/单分区、Btrfs `ANAS_DATA`、按 UUID 持久挂载。

实际数据卷为 `/dev/nvme0n1p1`，UUID `BOARD_UUID_REDACTED_03`，挂载 `/srv/a-nas/data`，有 nosuid/nodev/noexec、zstd 压缩；系统根仍为原 eMMC p6。私有、共享、相册、分享及家庭子卷均已建立，健康检查确认三个数据服务数据库已打开。可丢弃目录内以实际成员 `admin`（UID 20100）验证 ACL 写入、快照内容不随源变更及只读快照拒写，全部通过并清理；原厂用户 rockchip 被 NAS 父目录拒绝访问，未放宽既有 ACL。SMB 仍因网口无地址待验收，后续重启还须确认 UUID 自动挂载。

用户反馈 localhost 拒绝连接后，日志确认 03:14:06 UTC 进入 deep suspend，03:16:41 电源键唤醒。厂商 GNOME 默认闲置 900 秒休眠，已备份并关闭自动休眠、mask sleep 类 target；调试单元改为持续监督与自动重新绑定。03:35:48 已超过原阈值且无新休眠，ADB/HTTP 持续在线。实际仍运行的厂商 `usb-gadget.service` 另有端口争用；停用方案被自动审批拒绝，未执行，当前保留并等待用户选择。[根因、修复与回归](../investigations/2026-11-18-rk3588-automatic-suspend.md)

### 本地验证边界

#### 已封存的 c738affa 全量候选验证

在主干基准上新增 ARM 构建、独立哈希锁与架构校验，候选开发版本为 `1.0.1~rc.6+dev20261117.89+gc738affa140b`，含未提交构建改动，不能仅凭提交 SHA 重建。工具与全部原始日志位于本机 `.cache/arm64-product-prep/`；候选目录为 `build/arm64-candidate-c738affa/`。以下是本轮进展，后续复测与未执行项分别保留，不能称作 ARM 全量通过。

| 范围 | 已有结果与边界 |
|---|---|
| 原生完整门禁 | 最终 `make check-steps` 退出 0：前端、Go 全包、静态/安全检查、AI 基础与真实 MCP SDK、四命令及五主包构建通过；最后的 ARM 内容包修正另以实际包构建和 ShellCheck 验证 |
| 原生竞态与 root 集成 | `make race-test RACE_PARALLEL=2` 与 root integration 通过；仅使用本地可丢弃测试资源 |
| 原生 systemd 安装链 | 完整签名安装、升级及失败回滚通过，日志 627 行 `PASS`、0 failures；AI/搜索为系统测试 stand-in，不能作为真实模型推理证据 |
| ARM 四命令与全包测试 | CGO AArch64 ELF；最终 `go test ./...` 遍历 120 包，85 包通过、34 包无测试、rootbroker 1 包受模拟系统调用限制；2331 个测试/子用例事件通过、3 失败、7 跳过。三个失败由下面的真实 ARM 内核测试补证；跳过不能算通过 |
| ARM 内核隔离 | QEMU 整机启动 Debian ARM Linux 6.12.107（4 KiB 页），非 root UID 1001，rootbroker 全包 45 个顶层测试通过，含三个 Landlock 用例；没有关闭产品隔离 |
| ARM Python 与 MCP | AI 基础执行 124 项，含 15 个条件跳过；另用真实 ARM Hermes CPython 3.14.8 与固定 MCP SDK 执行 28 项，全部通过、无跳过 |
| ARM 媒体 | 真实 HEIF、动图照片、视频与音乐编解码通过；WMA 夹具改用其支持的 48 kHz 后，音频转换在原生和 ARM 单独复测通过 |
| ARM 真实模型 | D-FINE 与 EmbeddingGemma 10 项全部通过、无跳过；另实际执行 YuNet 检测、AuraFace 合成对齐输入的 512 维向量、DINOv2 合成裁剪的 384 维向量，均有限且归一化；不等于识别人脸/宠物的质量评测 |
| ARM 4B | 真实 Q4_K_M 两分片与 F16 mmproj、原 Provider、8192 上下文/1024 视觉 token 上限/2048 输出上限/4 线程/贪心非思考/mmap；读图、UDS 流、逐 token 概率可执行。精确文本失败，见下面对照；另真实取消测试释放子进程，退出 `-9` |
| ARM 搜索与助手 | 实际 Granian/SearXNG UDS `/config` 返回 200、332 个引擎配置；未测试外部搜索结果。Hermes 真实 ARM 依赖安装、ELF 审计、断网导入、CLI 与线程白名单补丁烟测通过；未调用云端模型 |
| 原生真实模型补测 | Debian 13 产品库环境中执行 124 项，含 5 个 MCP 条件跳过；实际 MCP 已由上述独立检查覆盖。开发机缺 GLES 库的问题通过使用对应测试环境解决 |
| 候选交付审计 | 24/24 包，0 缺项/错误；202 个 ELF（含 8 个嵌套归档）全为 AArch64，13 个 `all` 包无 ELF。8 件模型及五个内容归档完整 SHA/大小匹配，依赖、链接、内嵌 pin 和目标元数据一致；Debian arm64 空状态 apt 安装模拟退出 0 |

rootbroker 子进程会清空环境，丢失 `QEMU_LD_PREFIX`；在可丢弃容器补齐标准 Debian loader/多架构库路径后，大部分首轮失败消失，产品安全逻辑未修改。余下三项受限写入测试的原因已由探针确认：同容器同 UID 的原生 `landlock_create_ruleset` 返回 ABI 7，而 ARM C 探针经 QEMU 10.0.13 返回 `Unknown syscall 444`、`ENOSYS`。[上游实现](https://github.com/qemu/qemu/blob/v10.0.0/linux-user/syscall.c)也没有 Landlock 分派。本轮改用真实 ARM 内核的整机仿真后全包通过；目标板仍须执行[只读预检](../../scripts/arm64-preflight.sh)和实际受限文件操作，不能关闭 Landlock。

全量验证修正了三处测试缺口：OCR 生成取消改为先等待服务器就绪再计时；真实检测取消回归补上已变更的调用参数，并通过 Future 把线程异常传回测试，避免假通过；WMA v2 夹具从不支持的 96 kHz 改为 48 kHz，两个无损夹具仍保留 96 kHz 重采样覆盖。对应原生/ARM 复测通过。真实内容包构建还排除了将 Hermes 源码中以 `.so` 结尾的贡献者文本当作共享库的误判；独立审计仍扫描全部包载荷的真实 ELF。

4B 附加质量样本是 256×128 JPEG，内容为 `123`，SHA-256 `a17a9d43bd96788ad3c6ed99687b515959277f7b849a82918f337f8b53237915`。ARM 返回 `12`、`finish=stop`，两 token logprob 为 `-0.2071/-0.6121`；原生 x86 在相同图片、模型、版本与参数下也返回 `12`，logprob 为 `-0.1895/-0.6548`。两者均漏读，严格的 `123` 断言失败，原始失败记录保留。这个样本未观察到 ARM 额外的字符差异，但概率不完全相同，不能推导整体精度等价；小尺寸输入是否为根因尚未证实。两者概率均低于现有 0.99 核对阈值，应核对，不能当作可信自动通过。未更换模型、提示词或生产超时来掩盖失败。

用户态仿真仍有六个未完成条件用例：真实宿主机观察、百万级文件/相册压力，以及需真实 ACL/Btrfs 宿主条件的两项共享测试；原始列表另含已单独补过的音频用例。它们不是通过项，原生 root/system 测试只能补充公共逻辑证据。ARM 整机 systemd 安装/升级、实际磁盘与 SMB、板级启动、GPU/NPU、散热、并发可用内存和同数据集 OCR 精度/吞吐仍待真机验收。

交付入口为 `build/arm64-candidate-c738affa/packages/`，只复制这个部署目录即可；包含校验文件、验证清单和预检脚本。原始日志包括 `native-check.log`、`final-arm-go-tests.jsonl`、`arm-full-system-rootbroker.log`、`final-arm-real-models.log`、`arm-4b-ocr.json`、`native-4b-reference.json`、`arm-4b-cancel.json` 和 `candidate-audit.json`。这些结果也随候选的 `metadata/validation/` 保留，不能把候选称作正式发布或“所有质量/硬件检查已通过”。

#### 首轮核心验证

2026-11-17 较早对固定主干 `c738affa140b03586e010b7bbadb84fd16988c63` 的独立 ext4 源码副本实际运行一轮，当时根工作区只包含研究文档改动。前端按该副本的锁文件安装，类型检查与 `make web-build VERSION=rk3588-c738affa-probe` 通过，重新生成页面、资源和许可文件，没有复用旧分支的嵌入页面。

| 项目 | 实际结果 | 边界 |
|---|---|---|
| 工具链 | Debian 13.7 可丢弃容器，Go 1.27.4、aarch64 GCC 14.2.0、QEMU linux-user 10.0.13 | 在 x86 WSL 上交叉编译和用户态仿真，不是启动 ARM 整机 |
| 四个命令 | `anas-api`、`anas-host-agent`、`anas-container-agent`、`anas-scoring` 全部生成 AArch64 ELF；`CGO_ENABLED=1`、`GOARM64=v8.0` | 包含 SQLite 的 C 依赖，但没有生成可安装的 ARM Debian 发行包 |
| ABI | 解释器均为 `/lib/ld-linux-aarch64.so.1`；Host Agent 最高引用 `GLIBC_2.38`，其余三个最高为 `GLIBC_2.34` | 只是本次制品的要求，Debian 13 的 glibc 2.41 满足该版本下限；不能推广到任意 ARM 发行版 |
| QEMU 测试 | 九包通过；425 个测试/子用例通过，4 个跳过，没有失败 | 账号、存储、Linux 状态观察、应用目录、OCR 核对、FFmpeg Worker、照片打分、Web 资源及 API 配置/流程 |
| 非 root API 冒烟 | UID 65534；`/healthz`、新网页、许可资源均返回 200；SIGTERM 正常退出（0） | Fake Adapter、临时目录、容器私有网络；没有接触 NAS 或真实数据盘 |
| SQLite | 启动后 14 个数据库的 `PRAGMA quick_check` 全部为 `ok` | 包含账号、相册、分享、家庭和打分等启动数据库；不是实际数据迁移或断电恢复测试 |
| ARM llama.cpp | 固定 b11655 的 ARM64 CPU 包在 Cortex-A76 仿真下成功执行 `--version` 和 `--list-devices` | 没有加载 GGUF 或执行图片编码、OCR；无 GPU/NPU 枚举不代表 CPU 不可用 |

四个跳过项分别是 `TestReaderReadsLocalLinuxHost`（未开启真实宿主机观察）、`TestRealHEIF`（没有 `heif-thumbnailer`）、`TestRealFFmpeg` 和 `TestRealFFmpegAudio`（没有 FFmpeg）。它们不是通过项，也没有完成 ARM 媒体链验证。Go 包测试在容器内以 root 运行，非 root 行为另由上述 API 冒烟覆盖；这不等于所有组件都做过非 root 验证。

交叉构建与测试的核心参数为：

```bash
export GOOS=linux GOARCH=arm64 CGO_ENABLED=1 CC=aarch64-linux-gnu-gcc CXX=aarch64-linux-gnu-g++
export GOPROXY=off GOTOOLCHAIN=local GOFLAGS='-mod=readonly -buildvcs=false'
go build -p 4 -trimpath -ldflags '-X main.version=rk3588-c738affa-probe' -o /out/bin/ \
  ./cmd/anas-api ./cmd/anas-host-agent ./cmd/anas-container-agent ./cmd/anas-scoring
go test -p 2 -count=1 -timeout 10m -json \
  -exec '/usr/bin/qemu-aarch64 -L /usr/aarch64-linux-gnu' \
  ./internal/accounts ./internal/storage ./internal/hoststate/linux ./internal/appstore \
  ./internal/photos/textread ./internal/ffmpegworker ./internal/scoring ./internal/webui ./cmd/anas-api
```

本轮二进制、SHA-256/ABI 信息、JSON 测试事件与汇总、冒烟和工具链日志保留在工作区 `.cache/rk3588-arm64-c738affa/`，关键入口是 `summary.json`、`logs/binary-details.log`、`logs/arm64-tests.jsonl`、`logs/fake-api-smoke.json` 与 `logs/web-release-build.log`。这些可丢弃证据未纳入版本控制；正式验证不能依赖它们永久存在。

llama 启动单独使用仅解包、不安装到系统的 Ubuntu QEMU 8.2.2，以 `-cpu cortex-a76` 限定指令集。ARM64 压缩包按官方发布的 SHA-256 `5b3b62ede4f1ea1e33a1f50018f536d45a1b11ece980b1f9d9adefe43817ffa6` 核验；所需 ARM 库按 Debian 索引中的大小和 SHA-256 核验。本次实际发现并补齐 `zlib1g`、`libzstd1` 等库；ARM 发行包应根据制品依赖声明它们。清单与日志为 `llama-inputs/inputs.json`、`native-qemu-inputs/inputs.json`、`logs/llama-qemu-version.log`、`logs/llama-version.log` 和 `logs/llama-devices.log`。

此前最新主干的本机 amd64 回归也通过了 `internal/hoststate/linux`、`internal/photos/textread`，以及 18 个 Fake llama-server 协议/生命周期用例（`.cache/rk3588-current`）。旧分支 `20674e0115e9` 的 ARM 结果仅为历史证据（`.cache/rk3588-probe`）；本节最新结果已独立重跑。以上均不验证真实 4B 模型、RK3588 性能、ARM systemd 完整隔离、驱动、散热、磁盘或板级启动，也不是 `make check` 完整发行门禁。

## 目标硬件与系统

正点原子公开手册主要使用 **ATK-DLRK3588B 底板 + ATK-CLRK3588B 核心板**名称。用户口述 ATK-DLRK3588，具体修订、SSD 型号和实际容量仍应以实物与启动后的观察为准，不能只从系列名称认定。

| 项目 | 一手资料确认的规格 | 对迁移的含义 |
|---|---|---|
| CPU/GPU | 4×Cortex-A76 + 4×Cortex-A55，Mali-G610 MC4 | 原生 Linux ARM64 CPU 推理有路径；CPU 频率与持续性能还受实际芯片和散热影响 |
| 内存与板载存储 | LPDDR4X 4/8/16GB 可选，eMMC 32/64/128GB 可选 | “8+64”可按 8GB RAM + 64GB eMMC 候选配置准备；不能把 64GB 当 RAM |
| SSD 接口 | M_KEY，PCIe 通道，2280 规格 | 优先验证原生 NVMe；仍需确认用户 SSD 的协议和实际枚举结果 |
| 其他数据盘接口 | 底板另有 SATA 数据接口与电源接口 | 不需要通过 USB 桥才能连接所有数据盘，但 SATA 功能同样需镜像驱动与供电验证 |

规格来自[核心板资源](https://wiki.alientek.com/docs/Boards/Linux/DLRK3588/DLRK3588%20硬件参考手册/resource/1.2/)与[硬件资源详细说明](https://wiki.alientek.com/docs/Boards/Linux/DLRK3588/DLRK3588%20硬件参考手册/notice/notice/)。硬件支持某接口不等于某个系统镜像已经启用它。

### 实机与下载镜像的只读检查

2026-11-17，Windows ADB 通过序列号 `BOARD_ADB_SERIAL_REDACTED` 连接设备（USB VID/PID `2207:0006`）。设备树型号为 `ATK-DLRK3588 Board`，`uname` 为 `aarch64`、Linux `5.10.160`，系统为 Buildroot `2021.11`；root shell 可用，物理内存约 7.7 GiB。ADB 的通用 `Nexus_4/mako` 属性不能作为板型依据。此处未安装软件、切换启动模式或写入磁盘。

用户下载的 `E:\2mipi_2hdmi_update.img` 为 Rockchip RKFW/RKAF 更新容器，标记 RK3588；大小 6,643,755,594 字节，SHA-256 为 `add80369d7c7c437b718a3c40594b9d43623f1d95be8425d2c83c464e7dcffaf`。该哈希用于标识本次检查文件，尚未与厂商独立发布的校验值比对。以强制只读 loop 配合 `debugfs` 检查根文件系统，读取 FIT 内核及内嵌 IKCONFIG，没有烧写板卡。

| 检查项 | 镜像中的实际结果 | 对完整部署的影响 |
|---|---|---|
| 用户空间与内核 | Debian 13.6（trixie），AArch64 Linux 6.12.69；模块目录也是 `6.12.69` | 用户空间版本符合候选包基线，版本号不能替代内核能力检查 |
| 文件代理隔离 | `# CONFIG_SECURITY is not set`，无启用的 `CONFIG_SECURITY_LANDLOCK` | 无法提供要求的 Landlock ABI；不能关闭产品隔离绕过 |
| 数据卷 | `# CONFIG_BTRFS_FS is not set` | 不能使用产品 Btrfs 数据卷与快照；安装 `btrfs-progs` 不能补上内核支持 |
| 资源限制 | `CONFIG_CGROUPS=y`，但 `CONFIG_MEMCG`、`CONFIG_CGROUP_PIDS` 均未启用 | 服务内存及进程数限制能力不完整，须补齐后验证 |
| 容器网络与存储 | `CONFIG_BRIDGE`、`CONFIG_VETH`、`CONFIG_OVERLAY_FS` 均未启用 | 当前配置不足以承载预期 Docker 应用路径，不能仅安装 Docker 即视为通过 |
| 部分基础能力 | NVMe、POSIX ACL、命名空间、seccomp 已启用 | 只证明编译配置存在；实际 SSD 枚举、权限和服务仍须板端测试 |

**这份原始镜像不能作为完整 A-NAS 验收基线直接部署。** 本次已用 SDK 补齐并编译内核，结果见[最新内核验证](#最新主干与补齐后的内核)；启动后仍须运行只读预检与实际受限文件操作。GPU/NPU/MPP 的完整驱动及运行时匹配尚未验收。

用户提供的《【正点原子】ATK-DLRK3588_嵌入式Linux驱动开发指南V1.0.pdf》（111 页，修订表日期 2026-04-15）已检查相关完整页面与截图：第 9 页明确以 Linux 6.1.141 / Buildroot 为例，并将 SDK 编译细节转交《ATK-DLRK3588B_SDK 开发手册V1.0.pdf》；第 13 页给出 `./build.sh lunch` 与 `./build.sh kernel` 的入口，第 15–20 页说明 `alientek_rk3588_defconfig`、`menuconfig` 与配置保存。该指南可用于理解修改流程，但不包含匹配本镜像的 6.12.69 源码、完整环境准备或更新包交付验证；仍须取得对应版本 SDK 与开发手册，不能照搬示例的工具链、配置名和模块路径。

随后提供的《【正点原子】ATK-DLRK3588_SDK开发手册V1.0.pdf》（22 页，修订表日期 2026-07-14，SHA-256 `3d732e43e5fb9978c1ea64071814e2c0c950f8c7a97c996366ac91e39c5df070`）补齐了 SDK 流程说明：第 8 页示例包是 `atk_dlrk3588_linux5.10_sdk_release_v2.0.0_20260714.tar.gz`，第 12、14 页又以 Linux 6.1 为例，因此不能据此认定适用于本次 6.12.69 镜像。第 21 页建议通过 `./build.sh kernel-config` / `kconfig` 保存内核配置；第 13–17 页列出内核、模块、固件检查及 `updateimg` 构建入口，并说明 `boot.img` 包含内核、DTB 与资源。取得匹配源码后，应先核对实际构建脚本、板级配置、模块安装路径和原镜像，再确定增量内核与模块的交付方式，不能只替换内核而遗留不匹配模块。

该 SDK 手册第 8–10 页要求以普通用户在无中文、空格的 Linux 路径解包和编译，推荐 Ubuntu 20.04；完整 SDK 构建最低可用磁盘 100 GB、建议 150 GB，建议内存至少 8 GB。2026-11-17 盘点时，I 盘约 465.76 GiB、空闲约 206.39 GiB，是唯一达到完整构建容量要求的分区；用户确认它是 USB 机械硬盘。本次在 I 盘新建最大逻辑容量 128 GiB 的稀疏 `build.ext4`，标签 `anasrkbuild`，UUID `BOARD_UUID_REDACTED_04`，挂载 `/mnt/anas-rk3588-build`。只格式化新普通文件，未格式化 I 盘；SDK 检出与编译以普通用户执行，未迁移现有 WSL。下载归档、镜像和构建文件共同占用宿主机空间，持续保留至少 20 GiB，不能只看虚拟文件内余量。[操作边界](../runbooks/build-rk3588-kernel.md)

SDK `atk_dlrk3588_linux6.12_sdk_release_v1.2.0_20261104.tar.gz` 已下载完成，大小 13,383,226,582 字节；完整 gzip CRC 与 tar 遍历通过，17,105 个条目，SHA-256 为 `50dcb27e00be30aa83fc53b4ca5e1716f03b413bd64f8ecc672213d1de40a748`，未与独立厂商摘要比对。只提取 repo 数据与必要源码、工具链，未解开 Ubuntu 镜像和 Buildroot 下载包。实际内核是 6.12.69，GCC 14.3.1，与原镜像版本相符；SDK 较镜像新，不能声称源码逐位匹配旧镜像。当前只构建内核、模块和项目候选，复用官方 Debian 根文件系统副本；完整 SDK 最低空间数值不能当作本次实际占用。

首次点亮只需匹配板卡的镜像、烧录说明、相应工具和 USB 驱动；分区式包还需要说明中指定的 loader/配置。本次已发现内核缺项，因此完整部署还需要匹配 SDK/源码；无需为阅读指南先下载厂商整套虚拟机。实际烧录前另按[运行手册路由](../runbooks/README.md)制定目标识别、确认、回退与验证步骤；本文不提供可以直接执行的磁盘写入命令。

## 发行链

本次候选补齐了应用软件包的 ARM 路径；正式软件源、整机镜像与板级更新仍有 x86 约束。

| 位置 | 当前事实 | ARM 迁移要求 |
|---|---|---|
| [主包控制文件](../../deploy/debian/a-nas/DEBIAN/control)、[内容包构建](../../scripts/build-content-packages.sh) | `ANAS_TARGET_ARCH=arm64` 生成真实 ARM 包；默认 amd64 保持兼容 | 真机安装及后续 ARM 发布链验收 |
| [ARM llama 清单](../../deploy/llama/llama-arm64.json) | 独立固定 b11655 ARM CPU 制品及哈希，动态库已声明 | 保留同一模型/协议；真机精度和性能回归 |
| [ARM Qdrant 清单](../../deploy/qdrant/qdrant-arm64.json) | 独立固定 v1.20.0 ARM musl 制品，真实服务已用于仿真测试 | 真机持久化、资源和恢复验收 |
| [ARM 上游清单](../../deploy/upstream/packages-arm64.json) | 五个同版本 ARM 包经签名索引、大小、SHA 和架构核对 | 实机 Docker/Caddy 与网络验收 |
| [Python 运行环境构建](../../scripts/lib/python-runtime.sh)、[内容身份](../../scripts/lib/content.sh) | ARM 独立哈希锁和平台 ID，原 amd64 ID 不变；真实 ARM Hermes pin 已生成 | Debian 13 用户空间、实际 systemd 沙箱与质量回归 |
| [软件源发布](../../scripts/apt-repository.sh) | 正式发布仍固定 `amd64` 索引 | 当前候选手工安装，不配置既有 amd64 源；ARM 签名更新另验 |
| [mkosi 配置](../../deploy/image/mkosi.conf) | `Architecture=x86-64`、`linux-image-amd64`，systemd-boot/UKI 启动 | 板级 bootloader、DTB、分区/启动介质及更新回滚另行适配，不能直接将现有镜像刷到 RK3588 |

先用厂商匹配镜像验证用户空间，再接入 A-NAS 软件包，可以把板级启动问题与产品问题分开。后续若交付整机镜像，还必须完成板级更新和恢复验证；Debian 的用户空间兼容不意味着现有 UKI 更新方案已经可用。[设备镜像手册](../runbooks/build-device-image.md)、[镜像与系统更新验证](sysupdate-image-verification.md)

正式发行必须遵守 [ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)：AI 运行环境和模型随版本离线交付。基础功能先验是隔离性原型，不是认定正式产品可以省略 AI。目标设备不应临时联网 `pip install` 或抓取模型。

## 存储

建议先验证 **64GB eMMC 放系统、M.2 NVMe 承载独立 Btrfs 数据卷**。这与产品不在系统盘目录上替代离线数据卷的边界相容；单块 SSD 不提供硬盘损坏冗余。[数据卷定义](../../CONTEXT.md)、[存储实现](../../internal/storage/service.go)

最新主干的[无法识别磁盘测试](../../internal/hoststate/linux/unidentified_test.go)已覆盖 eMMC/启动设备缺 `MODEL` 或稳定身份的场景：未知设备不再使整机状态读取失败。旧分支中“eMMC 缺字段会阻塞所有观察”的结论已经过时，不能沿用。

但是，[Linux 观察器](../../internal/hoststate/linux/reader.go)的传输映射仍主要识别 `nvme`、`sata`/`ata`、`usb`；其他值归为未知。[数据卷初始化](../../internal/storage/service.go)仍拒绝未知传输、USB、可移除、已使用或非可分配磁盘。因此上板要只读核对真实 `lsblk`/挂载关系、根设备、稳定 ID、eMMC 分区名与 NVMe 传输；出现未知值应按设备实际身份适配，不能直接放宽安全检查。若 SSD 通过 USB 桥连接，当前初始化路径不支持。

基础存储验收必须覆盖：空白数据盘的三阶段确认、Btrfs 与 ACL、账号/SMB、重启后按 UUID 恢复、系统盘保护、快照与恢复。读写测试只使用明确可丢弃的独立测试数据，首次烧录和格式化另由手册定义确认与回退。[存储与共享验收](../runbooks/provision-v1.0.1-experimental-storage.md)

## AI 依赖

[当前 Python 依赖](../../ai/requirements.txt)有 18 个固定版本，不再使用旧分支的 `ai/requirements.lock`。判断 wheel 时必须包含 `py3-none` 通用标签、兼容的 `abi3` 标签与 aarch64 平台；只搜 `cp313-cp313` 会错误排除有效候选，尤其不能据此声称 MediaPipe 缺 ARM wheel。

准备阶段查询 18 个固定版本的官方 PyPI JSON，按 CPython 3.13、Linux aarch64、manylinux 的 glibc 下限不高于 2.41 筛选，18 项均有候选，但原锁仅 9 项包含对应候选哈希。因此已生成独立的 [AI ARM 锁](../../ai/requirements-arm64.txt)和[搜索 ARM 锁](../../deploy/search/requirements-arm64.txt)，保持既有版本，按完整哈希安装；不复用 x86 哈希集合。最初的静态候选审计保留在 `.cache/rk3588-current/`，最终执行证据以[本地验证](#本地验证边界)为准。

其中 MediaPipe 1.1.0 的候选为 `mediapipe-1.1.0-py3-none-manylinux_2_28_aarch64.whl`；ONNX Runtime 1.31.0 为 `onnxruntime-1.31.0-cp313-cp313-manylinux_2_28_aarch64.whl`，两者 ARM 候选哈希均不在当前锁中。[MediaPipe 官方版本元数据](https://pypi.org/pypi/mediapipe/1.1.0/json)、[ONNX Runtime 官方版本元数据](https://pypi.org/pypi/onnxruntime/1.31.0/json)

最终候选已完成真实 ARM 导入、模型执行及包内架构审计；这些结果超过最初的 wheel 静态核查，但仍不代表目标板吞吐、散热、完整数据集质量或系统隔离验收。

[AI Provider](../../ai/anas_ai/providers.py)的三处 ONNX Runtime 会话显式选择 `CPUExecutionProvider`。当前没有 RKNN Provider；6 TOPS NPU、Mali GPU 和 MediaPipe 包存在都不会自动把这些会话迁移到 NPU。原模型文件在 ARM CPU 上能否保持行为，应通过真实模型输出测试确认，不能仅看 Python 单元测试。

Rockchip 官方 RKLLM `release-v1.3.1` 列出的 Python 版本是 3.10/3.11/3.12；RKNN Toolkit2 `v2.5.0` 的官方列表止于 3.12，均未列 3.13。转换环境可以与产品 Python 3.13 分开，板端也可通过 C/C++ API 集成，但这是新的适配工作，不等于直接把 SDK 加入当前依赖。它们都依赖 RKNPU 内核驱动，候选 Linux 6.12 镜像的匹配情况尚未取得证据。[RKLLM 固定版说明](https://github.com/airockchip/rknn-llm/blob/release-v1.3.1/README.md)、[RKNN Toolkit2 官方说明](https://github.com/airockchip/rknn-toolkit2/tree/v2.5.0)

## 4B OCR 与 8GB 内存

### 保留的基线

当前实现是 **Qwen3.5-4B Q4_K_M 语言模型 + F16 视觉投影 + llama-server b11655**，用于照片全文与结构化字段读取。[语言清单](../../deploy/models/qwen3.5-4b-q4_k_m.json)、[视觉清单](../../deploy/models/qwen3.5-4b-mmproj-f16.json)、[llama 清单](../../deploy/llama/llama.json)

官方模型卡说明 4B 版本有视觉编码器，并采用 Gated DeltaNet 与全注意力交替的结构；不能把它当成任意同参数量纯文本 Transformer，或把家族中其他 MoE 型号的支持推到该模型。[Qwen 官方模型卡](https://huggingface.co/Qwen/Qwen3.5-4B/tree/851bf6e806efd8d0a36b00ddf55e13ccb7b8cd0a)

现有 [Provider](../../ai/anas_ai/providers.py)固定每图最多 1024 个视觉 token、8192 上下文、最多 2048 输出 token、非思考/贪心解码、`mmap`、单并发和可用核数减 2。请求需要逐 token `logprobs`，挂断需要及时停止生成。不能为得到好看的板端速度而改小图片或视觉 token、删掉概率、换模型，再称现有质量基线通过。

### 内存应如何读

| 数值 | 含义 | 不应推出的结论 |
|---|---|---|
| 2,783,446,880 + 672,423,392 字节，合约 3.22 GiB | 两个 GGUF 文件的磁盘大小；默认七个模型角色的模型总文件约 4.07 GiB，不含角色未引用的 sface | 不是推理峰值，也不是整机内存需求 |
| x86 为 2816 + 768 MiB，合 3.5 GiB；ARM 为 3584 + 768 MiB，合 4.25 GiB | 清单用于 OCR 阶段的准入预算，ARM 根据本板短测上调，见[实测与资源修正](#目标板-4b-实测与资源修正) | 不是 Worker 的全部 cgroup/RSS，更不是系统剩余内存保证 |
| 现有 NAS 的 cgroup 峰值 5597 MiB、匿名峰值 3494 MiB、全机可用内存最多下降 3319 MiB | 2026-10-30，12 张样本、6 线程、1024 视觉 token、mmap 的观测 | 不是 RK3588 的内存峰值；不同后端的缓冲、重排与驱动占用会改变结果 |

以上数字的来源与测法以[原评估](photo-ocr-model-evaluation.md)为准。相同测试的 p50 105 秒、p90 135 秒、首字 34.5 秒、解码 5.9 token/s 也只属于原 NAS，不能按主频、核心数或 NPU TOPS 比例换算为 RK3588 的速度。

该基准对每个样本调用一次模型。产品对判定为证件或票据的照片还会在全文之后再次抽取字段，所以 105 秒及据此估算的每小时约 34 次读取，不是完整两步流水线的端到端吞吐；必须按真实文档占比测量完整队列。[基准实现](../../ai/eval/ocr.py)、[两步读取](../architecture/photo-ocr.md#两步读取)

[OCR 资源策略](../architecture/photo-ocr.md)已在读文字前释放检测类模型、保留向量模型，并将后台 OCR 排在空闲时串行处理，加载后目标余量为 1GiB。[systemd 生成器](../../deploy/site/a-nas-generator.sh)按总内存减 2.5GiB 设置 AI Worker 上限，这是 cgroup 限额，不是全机空闲保证，也不是 OCR 一定获准加载。8GB 板卡还要容纳系统、产品服务、Qdrant、映射页缓存、GPU/NPU 缓冲、Kiosk、应用与成员助手实例。需要分别测后台门控与前台交互，不能只证明模型能启动一次。

NVMe 能改善读取与装载，但不能把模型 token 生成速度视为 SSD 带宽。默认“核数减 2”在八核 RK3588 上是 6 线程，会跨大小核；可以将 4 个大核与默认 6 线程作受控对照，再根据整机交互和持续散热选择，不能预先认定更多线程更快。

### 三种运行路线

| 路线 | 官方证据 | 保留程度与额外工作 | 本文结论 |
|---|---|---|---|
| ARM64 CPU，固定 llama.cpp b11655 | 该发布有 Ubuntu arm64 CPU 制品；固定源码识别 Qwen3.5-4B 与混合注意力，CPU 构建包含 ARM 后端 | 可优先沿用相同 GGUF/mmproj、提示词、HTTP/UDS 与概率契约；仍需检查 ARM 制品依赖、实际输出和取消 | 最适合作为首个端到端基准；没有板端速度承诺 |
| ARM64 Vulkan，同版 llama.cpp | 同一发布有 Ubuntu arm64 Vulkan 制品；Mesa 当前支持 Mali-G610 的 Vulkan | 可沿用模型，但需要正确 GPU 用户态/内核驱动、可见设备、算子回退与内存验证；浮点差异仍要质量回归 | 独立原型，不预先保证更快或更省内存 |
| RKNN 视觉 + RKLLM 语言 | 固定 RKLLM 版支持 Qwen3.5，提供视觉导出代码与 4B 文本基准 | 转换为 RKNN/RKLLM、新量化、视觉预处理和请求适配；需概率与取消接口、ModelID、离线制品与质量回归 | 有研究基础，但尚无本项目精确 4B OCR 的验收证据 |

CPU/Vulkan 来源：[b11655 官方发布](https://github.com/ggml-org/llama.cpp/releases/tag/b11655)、[固定版 Qwen3.5 实现](https://github.com/ggml-org/llama.cpp/blob/b11655/src/models/qwen35.cpp)、[固定版 ARM 构建](https://github.com/ggml-org/llama.cpp/blob/47f5a64ae12833b9092c6c47b54aa9f310c4f6f1/ggml/src/ggml-cpu/CMakeLists.txt)、[Mesa Panfrost/PanVK](https://docs.mesa3d.org/drivers/panfrost.html)。ARM 构建的特性选择不能给 RK3588 强行启用 SVE/i8mm 等实际不具备的指令；有 ARM64 制品不是这块板已经跑通的证明。

### RKLLM 基准的准确边界

Rockchip 固定 `release-v1.3.1` 的[官方 benchmark](https://github.com/airockchip/rknn-llm/blob/release-v1.3.1/benchmark.md)列出：

| 项目 | RK3588 官方结果 | 条件 |
|---|---|---|
| Qwen3.5-4B，语言模型基准 | TTFT 2068.88ms，decode 6.22 token/s，内存 4575.78MB（沿用官方单位） | W8A8，输入 128 token、输出 64 token |
| Qwen3.5-0.8B，多模态基准 | 图像编码 690ms，prefill 1.56s，decode 27 token/s | 448×448 图片，prefill 长度 216；图像编码为 RKNN FP16，使用全部 NPU 核 |

官方测量将 CPU/NPU 设为最高频，转换使用 `optimization_level=0`。**4B 的第一行不是 4B 视觉 OCR 基准，第二行的 0.8B 也不是本项目模型。** 不将其折算为本项目每张耗时。官方[视觉导出](https://github.com/airockchip/rknn-llm/blob/release-v1.3.1/examples/multimodal_model_demo/export/export_vision.py)和[RKNN 转换](https://github.com/airockchip/rknn-llm/blob/release-v1.3.1/examples/multimodal_model_demo/export/export_vision_rknn.py)有 `qwen3.5` 路径，足以支持开展精确 4B 转换实验；未证明项目 1600px/1024 视觉 token 配置、字段质量与内存已通过。

RKLLM 固定版[公开头文件](https://github.com/airockchip/rknn-llm/blob/release-v1.3.1/rkllm-runtime/Linux/librkllm_api/include/rkllm.h)有 `token_id`、`RKLLM_INFER_GET_LOGITS` 与 `rkllm_abort`，不能说它完全无法实现概率或取消。但取得 logits 的模式和普通生成返回，并不自动满足现有 llama-server 逐 token logprobs、文本片段对齐和挂断语义；[历史更新记录](https://github.com/airockchip/rknn-llm/blob/release-v1.3.1/CHANGELOG.md)曾提及 logprob，也不能替代当前契约测试。

即使取得每 token 概率，W8A8 与现有 Q4_K_M 的数值分布也可能不同。核对层的 `0.99` 阈值来自已有评估，必须重测自动通过的错字段、待核对比例和数字错误召回，不能只检查 JSON 合法或文字看起来正确。模型/后端身份也需要表达变化，防止重用旧结果。[单字概率决定](../decisions/2026-11-03-ocr-character-confidence.md)、[核对层评估](photo-ocr-model-evaluation.md)

## GPU/NPU、媒体与沙箱

[AI Worker](../../deploy/systemd/system/anas-ai.service)与[FFmpeg Worker](../../deploy/systemd/system/anas-ffmpeg@.service)均设置 `PrivateDevices=true`。GPU/NPU 原型必须检查实际设备可见性及权限，保留无网络、无数据卷、只收文件描述符的边界；不能通过取消全部沙箱来证明产品支持硬件加速。[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)

GPU 上，Mesa 的 PanVK 与板商可能随镜像提供的 Mali 用户态栈需要分别核对。官方当前支持表列出 G610 的 Vulkan，不意味着任何 Debian 13 镜像都携带该实现。NPU 上，官方 RKLLM/RKNN 明确依赖 RKNPU 驱动；Linux 版本号本身不能证明模型格式、用户态 runtime、内核驱动、DTB、IOMMU 和设备节点相匹配。[Mesa 支持表](https://docs.mesa3d.org/drivers/panfrost.html)、[RKLLM 固定版架构说明](https://github.com/airockchip/rknn-llm/blob/release-v1.3.1/README.md)

[FFmpeg 命令构造](../../internal/ffmpegworker/command.go)默认编码为 `libx264`。即使候选镜像有 Rockchip MPP/RGA，也不会自动改变此命令；需要另行验证 FFmpeg 编译能力、输入格式、设备权限、解码/缩放/编码链和浏览器播放。Rockchip [MPP 官方仓库](https://github.com/rockchip-linux/mpp)与[RGA 官方仓库](https://github.com/airockchip/librga)描述的是相应硬件接口库，不能把它们与发行版 FFmpeg 已启用的能力混为一谈。

应用目录的静态统计为 37 项，其中 34 项声明 `arm64`，`excalidraw`、`swingmusic`、`vocechat` 未声明 ARM。此统计来自目录元数据，不是 registry manifest 实证；每个目标镜像仍需核对。Kiosk/Chromium 需验证显示、输入、视频播放和内存；云端 AI 助手的运行环境、每成员实例资源与文件边界也各需回归。以上都不由 Go 主程序编译成功覆盖。

## 分阶段验收

| 阶段 | 工作与证据 | 通过后可以说什么 |
|---|---|---|
| 1. 候选镜像点亮，只读盘点 | 确认板修订、镜像哈希、内核/DTB、RAM/eMMC/NVMe、启动设备、网口、驱动与散热；保存完整日志 | 板卡基础系统和目标存储被正确识别；还没有 A-NAS 验收 |
| 2. 最新主干 ARM 制品 | 全部命令和 Debian/内容包 ARM 构建，固定上游包与 Python wheels，检查 ELF/动态库/包架构；可先用 QEMU 做逻辑与 SQLite 冒烟 | 最新主干用户空间有可复现 ARM 制品；QEMU 不证明真实驱动与性能 |
| 3. 基础存储与共享 | systemd 服务、账号/ACL、空白 NVMe 的确认初始化、SMB/Web、重启恢复、快照与系统盘保护，失败回退 | 基础闭环在目标板通过；不能当作完整 AI 发行验收 |
| 4. 同模型 ARM CPU AI/OCR | 真实依赖导入与模型输出；同一 OCR 样本、1024 视觉 token 和提示词；概率、字段核对、挂断、加载失败恢复与离线沙箱；同时观察共享/搜索延迟 | 可以量化该板 4B OCR 的质量、内存和吞吐，决定是否达到产品需求 |
| 5. Vulkan/NPU 对照（若需要） | 分别固定后端/驱动/制品；与 CPU 基线同图同提示词；检测加速是否实际发生、热稳态、概率与核对结果；NPU 另测转换量化 | 有证据选择加速路线，不能只按 TOPS 或文本 decode 排名 |
| 6. 完整产品与交付 | 媒体/应用/Kiosk/助手、更新与恢复；AI/模型随包离线交付；压力与长期运行 | 才能声明该硬件配置的完整迁移范围及明确限制 |

OCR 测量优先复用 [`ai/eval/ocr_nas_bench.sh`](../../ai/eval/ocr_nas_bench.sh)、[`ai/eval/ocr_cancel.py`](../../ai/eval/ocr_cancel.py)、[`ai/eval/worker_memory.py`](../../ai/eval/worker_memory.py)，具体参数按脚本帮助和[评估方法](photo-ocr-model-evaluation.md)核对。这些入口在本次研究中未对目标板执行。

记录至少包括：输入与结果哈希、模型/运行时/驱动版本、视觉与上下文参数、p50/p90 和首字延迟、有效输出/截断/失败率、字段准确率及核对统计、cgroup/匿名/全机 available、同时运行的服务、温度/频率及取消后的回落。分别观察启动、单张、连续处理和后台被交互打断；性能接受门槛由实测和产品需求决定，本文不虚构每秒 token 或每张处理时限。

## 关联

- [ARM64 候选交付规格](../specs/linux-arm64-delivery.md)、[候选准备与板端验收手册](../runbooks/prepare-rk3588-candidate.md)
- [相册 OCR 实施方案](../architecture/photo-ocr.md)、[OCR 模型评估](photo-ocr-model-evaluation.md)、[本地 AI 选型](photo-ai-model-runtime-selection.md)
- [架构总览](../architecture/OVERVIEW.md)、[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)、[领域语言](../../CONTEXT.md)
- [Debian 包部署](../runbooks/deploy-a-nas-packages.md)、[设备镜像](../runbooks/build-device-image.md)、[存储验收](../runbooks/provision-v1.0.1-experimental-storage.md)、[功能验收](../runbooks/accept-features-on-experimental-nas.md)
- [AI Provider](../../ai/anas_ai/providers.py)、[AI 依赖](../../ai/requirements.txt)、[发行包构建](../../scripts/build-content-packages.sh)、[Linux 磁盘观察](../../internal/hoststate/linux/reader.go)、[存储服务](../../internal/storage/service.go)
