# A-NAS 项目上下文与当前决策

> 文档日期：2026-10-06
> 文档性质：项目启动上下文 / 当前单一事实入口
> 目标目录：`E:\A-NAS`
> 注意：本文中的“建议”不是已经冻结的技术决策；“已确认”与“待决策”在文末单独列出。

## 1. 项目目标

A-NAS 的目标不是给传统 NAS 简单增加一个聊天框，而是构建一套真正属于自己的、基于 Linux 的 AI 智能家庭 NAS 产品：

1. 首先是一台可靠、可恢复、权限正确、可长期运行的 NAS。
2. 在可靠 NAS 之上提供本地多模态 AI 能力，理解家庭照片、视频、音频和文档。
3. 最终目标是让敏感数据尽可能在设备本地处理，降低用户对云端隐私的焦虑。
4. MVP 阶段允许接入云端多模态 API，以低成本验证用户需求、产品交互和完整业务闭环。
5. 后续本地模型应通过统一模型接口替换云 API，不能要求上层产品功能整体重写。

一句话产品承诺：

> A-NAS 先做好可信家庭存储，再让 AI 在用户授权范围内理解、检索和整理家庭数据。

核心约束：AI 不能成为存储可靠性的依赖。即使 AI 服务全部停止，文件共享、权限、快照、备份和恢复仍应正常工作。

## 2. 当前硬件与状态

### 2.1 已有硬件

| 项目 | 当前配置 | 判断 |
|---|---|---|
| CPU | Intel Core i3-12100 | x86-64/Intel 64，应安装 Debian `amd64`，不是 `arm64` |
| 内存 | 8 GB | 足够开发 NAS 基础能力和调用云端 AI API；不适合舒适运行较大的本地多模态模型 |
| 系统盘 | Acer N3500CN 256 GB NVMe | Debian 根文件系统使用 ext4，EFI、根分区和 swap 均位于该盘 |
| 数据盘 | 512 GB 机械硬盘 | 计划作为可清空的开发测试盘；当前未被 Debian `lsblk` 检测到，排障前不得格式化任何其他设备 |
| AI 加速 | 暂无独立 GPU/NPU | 不妨碍 API-first MVP；后续根据实测模型和功耗再选择加速硬件 |

当前状态：机器已运行 Debian 13 amd64，Windows 已通过项目专用 ED25519 密钥登录无 `sudo` 权限的 `anas-dev`；系统盘身份已确认，完整硬件基线和 512 GB 机械盘检测仍待完成。

### 2.2 当前机器的定位

这台机器首先作为 **裸机硬件集成测试机**，用于验证：

- UEFI、磁盘、网卡、温度、风扇和电源行为；
- Debian、systemd、udev、SMART、Btrfs、Samba；
- 格式化、挂载、快照、故障和恢复路径；
- 产品服务安装、升级、重启和日志；
- AI 任务对 NAS CPU、内存和 I/O 的干扰。

它不是源码的唯一保存位置，也不承担主要日常编码工作，可以被反复部署甚至重装。

### 2.3 建议升级顺序

1. 内存优先升级至 32 GB；若后续本地模型目标较大，再评估 64 GB。
2. 增加第二块数据盘，用于验证镜像、掉盘、替换和重建。
3. 增加 512 GB～1 TB NVMe，单独保存模型、索引、缩略图和其他可重建派生数据。
4. AI 场景和基准明确后再选择 GPU/NPU，不按宣传 TOPS 盲目采购。

## 3. 基础系统选择

当前主线为：

> Debian Stable amd64 + 自研 A-NAS 产品层 + 成熟的 Linux/NAS 开源组件。

当前安装建议：

- Debian 13 `trixie` amd64 的官方 netinst 镜像；
- UEFI 启动，GPT 分区；
- 系统只安装到 125 GB M.2；
- 安装时暂不格式化 512 GB 数据盘；
- 不安装桌面环境；
- 安装 `SSH server` 和 `standard system utilities`；
- BIOS 使用 AHCI，开启 VT-x/VT-d；避免 Intel RST/主板 RAID 模式干扰 Linux 直接管理磁盘。

官方下载入口：

- [Debian 官方下载页](https://www.debian.org/download.en.html)
- [Debian amd64 netinst 当前镜像目录](https://cdimage.debian.org/debian-cd/current/amd64/iso-cd/)

目前不建议把 fnOS、OpenMediaVault、CasaOS 或 TrueNAS 直接作为 A-NAS 的产品底座。它们继续作为竞品、架构和实现参考。

## 4. 推荐开发方式

新 NAS 实机不适合承担日常源码开发。推荐采用三层开发闭环：

```text
Windows 主电脑
E:\A-NAS：Git 源码与文档的唯一事实源
        │
        ▼
WSL2：Linux 本地开发、构建、单元测试
        │
        ├── Fake Adapter：模拟磁盘、共享、用户和系统状态
        │
        └── SSH / 自动化部署
                    │
                    ▼
i3-12100 Debian 裸机：真实磁盘、网络、systemd 和破坏性集成测试
```

具体原则：

1. 日常编辑、代码审查和 Git 操作发生在 `E:\A-NAS`。
2. WSL2 提供接近 Debian 的 Linux 命令、编译和测试环境。
3. 大多数业务开发使用 Fake Adapter，不要求本机拥有真实块设备或 root 权限。
4. 只有 SMART、分区、格式化、挂载、Btrfs、Samba、systemd 等集成测试部署到裸机。
5. 裸机创建专用的非 root 开发账号，通过 SSH 密钥登录；密码和私钥不进入仓库或聊天记录。
6. 实机初始化建议用 Ansible 等幂等方式管理；高频代码部署可以使用构建产物加 SSH/rsync，避免每次完整重装。
7. 实机不是源码唯一副本，不直接在生产路径上临时修改代码。

当前本机开发环境状态（2026-10-06）：

- `E:\A-NAS` 已初始化为 `main` 分支的 Git 仓库；
- WSL2 使用 Ubuntu 24.04 LTS，systemd 已启用；
- 已建立隔离的非特权开发用户 `anas-dev`，其独立 home 为 `/home/anas-dev`；
- WSL 工作区入口为 `~/workspace/A-NAS`，指向 `/mnt/e/A-NAS`；
- Git、通用编译工具、Python、SQLite、Ansible 和常用检查工具已经就绪；
- Go 1.27.1 已作为产品服务和 Host Agent 的初始工具链；数据库服务和容器运行时仍等待原型与 ADR。

详细使用方式见 `docs/development/LOCAL_ENVIRONMENT.md`。

未来可提供统一命令，例如：

```text
dev       启动前端、产品服务和 Fake Adapter
test      运行单元测试与契约测试
build     在 Debian 兼容环境生成可部署制品
deploy    部署到实验 NAS
itest     运行只读或显式授权的实机集成测试
logs      收集服务日志和诊断包
```

## 5. 建议的软件边界

产品服务和 Host Agent 已确定使用 Go 起步，当前工具链版本为 Go 1.27.1。语言选择不改变既定架构边界：产品层仍保持非特权，Host Agent 仍通过窄而类型化的 IPC 隔离特权操作；硬件集成或本地 AI Runtime 将来可以在显式 Adapter 边界后采用其他语言。

```text
浏览器 / 手机端 / 桌面客户端
              │ HTTPS / WebSocket
              ▼
        Gateway / Product API
              │
              ▼
┌─────────────────────────────────────┐
│ A-NAS 产品层（非特权、模块化单体起步）│
│ Auth / Files / Storage / Share      │
│ Jobs / Policy / Events / Update     │
│ Apps / AI Orchestrator / Audit      │
└─────────────────┬───────────────────┘
                  │ 窄而类型化的 IPC
                  ▼
┌─────────────────────────────────────┐
│ Host Agent（最小常驻特权面）         │
│ disk / mount / btrfs / samba        │
│ smart / systemd / update / network  │
└─────────────────────────────────────┘

文件事件 → Ingestion → Metadata/Chunk → AI Job Queue
                                          │
                    ┌─────────────────────┴─────────────────┐
                    ▼                                       ▼
              Cloud AI Adapter                       Local Runtime Adapter
              MVP 首先使用                           后续本地模型
                    └──────────→ Search Index ←─────────────┘
                                      │
                              按文件 ACL 过滤检索
```

### 5.1 产品层

MVP 先采用模块化单体，不复制 fnOS 的数十进程部署规模。模块可以在代码层隔离，在出现明确的权限、故障隔离或独立扩缩容需求后再拆进程。

建议领域模块：

- 身份、用户、用户组与会话；
- 文件和共享目录；
- 存储计划、执行、健康和恢复；
- SMB/NFS 等共享服务；
- 后台任务和事件；
- 统一权限策略；
- 更新、恢复和诊断；
- 应用管理；
- AI 编排、模型 Provider 和审计。

### 5.2 Host Agent

只有少量操作需要 root。Host Agent 应保持很小，不提供“执行任意 Shell”接口，只接受经过校验的高层意图，例如：

- `ListDisks()`
- `GetDiskHealth(id)`
- `PlanCreateVolume(request)`
- `ExecuteApprovedPlan(planId)`
- `MountVolume(id)`
- `ApplyShareConfig(version)`
- `RestartManagedService(name)`

产品层不能拼接磁盘 Shell 命令，也不能把 `/dev/sdX` 当作稳定磁盘身份；应使用 `/dev/disk/by-id` 等稳定标识。

### 5.3 Fake 与 Linux Adapter

开发敏捷性的关键不是远程编辑，而是让系统能力可替换：

```text
Storage Interface
├── FakeStorage：在主电脑模拟磁盘、容量、故障和任务状态
└── LinuxStorage：在实验机调用 udev、smartctl、Btrfs 等真实能力
```

共享、用户、服务管理和 AI Runtime 同样应提供 Fake/真实 Adapter。契约测试确保两个实现遵循相同行为。

### 5.4 权限策略

文件浏览、外链、传统搜索和 AI 检索必须使用同一套 Policy 模块：

- 索引条目关联稳定资源 ID，不直接把文件路径当权限依据；
- 检索前过滤候选结果，返回内容前再次检查实时权限；
- ACL 变化会更新索引授权元数据；
- 文件删除会清理文本块、向量、缩略图和缓存；
- 管理员跨用户查看也应显式授权并审计。

## 6. AI 路线

### 6.1 MVP：先用 API 验证闭环

8 GB 内存和无独立 GPU 不妨碍初步验证。当前设备负责：

- 文件扫描和元数据提取；
- 图片、音频、视频和文档预处理；
- 调用云端多模态模型；
- 保存结构化结果和建立检索索引；
- 权限过滤、引用原文件和审计；
- AI Web 交互和任务进度。

业务不能直接耦合某一家模型 SDK，应定义统一 AI Provider：

```text
AI Gateway
├── Cloud Provider A
├── Cloud Provider B
├── Mock Provider
└── Local Provider（后续 llama.cpp / ONNX Runtime / 其他运行时）
```

建议能力接口：

- 文本/图片/音频/视频请求描述；
- 流式和非流式响应；
- Embedding、OCR、ASR、视觉理解和生成能力声明；
- 成本、超时、速率限制和重试；
- 数据是否离开设备的明确标志；
- 每次外发内容的授权和审计记录。

### 6.2 隐私边界

云 API 只是 MVP 验证手段，不应偷偷违背最终产品承诺：

- 初期只使用测试数据或用户明确授权的数据；
- UI 明确显示哪些内容会离开设备、发送给谁、目的是什么；
- 记录外发任务、字段、时间和结果，不记录不必要的敏感正文；
- 用户可以关闭云处理并删除 AI 派生数据；
- 后续本地 Runtime 复用同一 Provider Interface；
- 原文件只读进入 AI Pipeline，AI 派生数据可重建、可单独删除。

### 6.3 建议优先验证的 AI 产品闭环

“家庭记忆搜索”是当前最清晰的候选：

1. 导入照片、视频、音频、PDF、Office 和文本；
2. 提取 EXIF、OCR、ASR、缩略图、文本块和 Embedding；
3. 支持关键词与语义混合搜索；
4. 自然语言提问时返回文件级引用和原始位置；
5. 支持按时间、人物、场景聚合；人脸能力可关闭、可清除；
6. 展示索引进度、资源占用以及任务使用云端还是本地。

## 7. NAS MVP 范围

### 7.1 必须完成

1. 安装、首次初始化和配置导出/恢复。
2. 安全识别系统盘与数据盘，磁盘操作提供计划、确认、执行三阶段。
3. 单盘数据卷；后续增加双盘镜像验证。
4. SMART、自检、容量、温度、Scrub 和故障告警。
5. 用户、用户组、个人空间、共享空间和 ACL。
6. SMB 文件共享；NFS 作为高级能力评估。
7. Web 文件管理、回收站、快照和外接盘备份。
8. 审计日志、诊断包和安全关机。
9. AI 全部关闭后，NAS 基本功能仍完整可用。

### 7.2 当前明确不做

- RAID5/6、分布式存储和多节点集群；
- 同时支持多种复杂存储拓扑和任意在线迁移；
- 完整虚拟机平台和大型应用商店；
- 本地训练或全量微调；
- 未经确认的 Agent 删除、移动、改名或修改权限；
- 一开始同时支持多家 GPU/NPU；
- 在 512 GB 单盘上承诺生产级冗余。

## 8. 可复用与应自研的边界

### 8.1 优先复用的成熟组件

| 领域 | 可复用组件/能力 |
|---|---|
| 系统 | Debian、Linux、systemd、udev、APT |
| SMB/NFS | Samba、Linux NFS server |
| 存储 | Btrfs、mdadm/LVM（按选定存储策略使用） |
| 磁盘健康 | smartmontools、NVMe 工具、udev |
| 容器 | Docker/Moby 或 Podman，二选一后冻结 |
| 媒体 | FFmpeg、ExifTool、ImageMagick/OpenCV（按需） |
| AI Runtime | 云 API；后续评估 llama.cpp、ONNX Runtime、OpenVINO 等 Adapter |
| 网络与安全 | Nginx/Caddy、OpenSSH、WireGuard 等成熟实现 |

### 8.2 形成自有产品资产的部分

- A-NAS 品牌、交互、Web UI 和客户端；
- 设备、存储、用户、共享和任务领域模型；
- 安全的磁盘编排、回滚和恢复流程；
- 产品 API、事件模型和权限 Policy；
- Host Agent 的最小特权接口；
- AI Provider、任务调度、索引授权和隐私审计；
- 安装器、恢复模式、OTA、签名和兼容矩阵；
- 应用 manifest、权限声明、升级/回滚和应用目录；
- 日志、告警、诊断和家庭用户可理解的故障体验。

目标不是重写 Samba、Btrfs 或模型 Runtime，而是在稳定进程/API 边界上将这些能力编排成可靠产品。

## 9. fnOS / ophub/fnnas 调研结论

### 9.1 `ophub/fnnas` 的真实角色

`ophub/fnnas` 不是完整 NAS 产品源码，而是官方 fnOS/FnNAS ARM64 镜像的板卡适配与镜像重制工具链：

- 输入官方 ARM64 完整镜像；
- 保留官方 rootfs 和产品功能；
- 替换或增加 U-Boot、Kernel、DTB 和内核模块；
- 叠加通用、SoC 平台和具体板卡配置；
- 输出适配 Orange Pi 3B 等设备的镜像。

它适合参考/复用的内容是板卡数据库、启动链、内核/DTB 打包、镜像分区、TF/eMMC 安装和更新脚本；它不提供 fnOS Web UI、账号、存储管理、应用中心等产品源码。

### 9.2 fnOS 可观察的三层结构

| 层 | 内容 |
|---|---|
| 开源系统底座 | Debian、Linux、systemd、Samba、NFS、Btrfs、mdadm、LVM、ZFS、Docker、QEMU/libvirt、PostgreSQL 等 |
| 飞牛产品层 | `/usr/trim` 下的 Web、账号、存储、共享、应用中心、媒体、备份、更新、许可证和云连接 |
| OPHUB 适配层 | U-Boot、Kernel、DTB、分区重制、板卡配置、安装和更新脚本 |

对本地 Orange Pi 3B 镜像的静态审计显示：

- 产品层约 850 MB；
- `trim` C++ Core 会动态加载多个 `.hdl` Handler；
- 存在 Go 与 C++ 混合领域服务以及 Shell 硬件脚本；
- 启发式分类结果为 Go 20、C++ 52、Shell 15、其他 ELF 11；
- Web 为 React 构建产物，没有原始 TypeScript/React 工程、构建清单或 source map；
- 外部入口为 Nginx，内部使用 HTTP/WebSocket、Unix socket、自有 RPC 和 gRPC；
- 57 个产品 systemd 单元中绝大部分按当前配置会以 root 运行。

值得借鉴的是“成熟底层组件 + 产品 Adapter + 统一内部平台 + 领域边界”；不应复制其大量 root 进程或私有二进制、前端构建产物、品牌素材和许可证/云服务实现。

### 9.3 fnOS 是否基于 OpenMediaVault

当前没有证据支持“fnOS 是 OMV 换皮”。

OMV 的高特异性运行链通常包含：

```text
Nginx → PHP-FPM → rpc.php → omv-engined
      → /etc/openmediavault/config.xml
      → omv-salt / Salt 状态树
```

本地 fnOS 镜像中没有发现：

- `openmediavault` 软件包；
- `/etc/openmediavault`；
- `omv-*` 命令；
- `openmediavault-engined`；
- OMV `config.xml`；
- PHP/PHP-FPM；
- Salt。

相反，fnOS 使用 `/usr/trim`、Go/C++、自有 RPC、PostgreSQL 和 React 构建产物。更准确的结论是：

> fnOS 与 OMV 共享 Debian、Samba、NFS、Btrfs 等上游生态，但分别实现了自己的 NAS 管理平面。

这不能排除设计参考或局部代码借鉴；没有源码级证据时也不能做绝对的代码血缘结论。

## 10. 四类竞品的用户侧结论

| 产品 | 强项 | 主要不足/不适合作为当前底座的原因 | A-NAS 借鉴重点 |
|---|---|---|---|
| fnOS | 消费级文件、手机/桌面端、相册、影视、远程访问和应用体验完整 | 产品层不提供完整可二次开发源码；商业授权与品牌边界独立于 OPHUB 仓库 | 消费级交互、应用包、统一产品体验 |
| OpenMediaVault | Debian NAS 管理成熟，SMB/NFS、用户、共享、插件边界清楚 | GPL-3.0；PHP/Angular/XML/Salt 管理面较深，fork 后长期合并和品牌化成本高 | NAS 功能模型、配置部署和插件机制 |
| CasaOS | UI 轻、Docker/Compose 应用体验好，Apache-2.0 较利于改造 | 更像已有 Linux 上的个人云/应用层，不提供完整 RAID、快照和故障恢复闭环 | 消费级桌面、Compose 应用模型 |
| TrueNAS CE | ZFS、快照、复制、数据完整性和虚拟化强 | appliance 属性强、复杂度和硬件要求更高，不适合作为普通 Debian 产品层直接改造 | 数据保护、故障处理和存储运维标准 |

结论：四者都可以作为参考或组件来源，但当前最符合“长期形成自有产品资产”的路线仍是 Debian + 自研产品管理层 + 成熟组件 Adapter。

## 11. 商业化与许可证边界

开源不等于不可商用：

- GPL 软件可以商用，但分发修改或派生代码时必须履行对应源码、许可证和版权义务；
- OMV 核心 GPL-3.0，不适合把修改后的 OMV 代码闭源占有；
- `ophub/fnnas` 仓库主要是 GPL-2.0 的适配层，不能把该许可证外推到飞牛私有产品层；
- CasaOS 主仓库 Apache-2.0，相对宽松，但商标和第三方应用仍需分别处理；
- Debian 不是单一许可证，镜像中的每个包都有自己的许可条件；
- Samba、Linux、Btrfs、FFmpeg、容器镜像、模型权重和数据集都要逐项建立 SBOM/许可证清单；
- 通过独立进程、CLI、配置文件或公开 API 复用第三方组件，通常比复制其内部实现更容易保持清晰边界，但仍需正式合规审查。

不得直接复用或重新品牌化：

- `/usr/trim` 私有二进制；
- fnOS Web 构建产物和品牌素材；
- 飞牛私有 RPC、云服务、许可证实现；
- 未经授权的官方应用、图标、描述和商标。

本文不是法律意见；商业发布前需要按实际分发制品做许可证和商标审查。

## 12. 建议仓库结构

```text
E:\A-NAS
├── AGENTS.md                # Agent 开工、路由与完工规则
├── CONTEXT.md               # 项目领域语言
├── PROJECT_CONTEXT.md       # 本文，项目上下文入口
├── README.md                # 面向开发者的快速开始
├── docs/
│   ├── status/              # 当前阶段、下一步和阻塞
│   ├── architecture/        # 系统边界、数据流和不变量
│   ├── adr/                 # 难以逆转的架构决策
│   ├── specs/               # 可验收的功能行为
│   ├── investigations/      # 复杂缺陷的证据与根因
│   └── runbooks/            # 安装、恢复和高风险操作
├── api/                     # OpenAPI、JSON Schema、事件契约
├── web/                     # 自研管理前端
├── services/                # Go 产品层
├── host-agent/              # Go 最小特权服务
├── adapters/
│   ├── fake/                # 本地模拟实现
│   └── linux/               # Debian 实现
├── ai-providers/
│   ├── mock/
│   ├── cloud/
│   └── local/
├── deploy/
│   └── ansible/
├── image/                   # 后续镜像、恢复和 OTA 构建
├── tests/
│   ├── contract/
│   ├── integration/
│   └── destructive/         # 只允许在明确指定的实验磁盘运行
└── third_party/             # notices、SBOM 和许可证资料，不放来历不明二进制
```

## 13. 当前决策状态

### 13.1 已确认

- 产品方向：AI 智能家庭 NAS，同时必须首先做好 NAS 本职工作。
- 最终隐私方向：优先端侧、本地处理，数据默认不上传。
- MVP AI 路线：允许先接多模态 API 验证产品闭环。
- 基础系统：Linux，当前选择 Debian Stable。
- 当前硬件：i3-12100、8 GB、256 GB NVMe 系统盘；512 GB 机械盘当前未被 Debian 检测到。
- CPU 架构和安装镜像：x86-64，对应 Debian `amd64`。
- 开发目录：`E:\A-NAS`。
- 开发方式：主电脑开发 + Linux 本地环境 + Debian 裸机远程集成测试。
- 本机 Linux 开发环境：WSL2 Ubuntu 24.04，使用隔离的非特权用户 `anas-dev`。
- 实验 NAS：Debian 13 amd64，主机名 `a-nas-dev`，使用无 `sudo` 权限的 `anas-dev` 和项目专用 SSH 密钥。
- Git 仓库：已初始化，默认分支为 `main`。
- 后端语言：产品服务和 Host Agent 使用 Go，见 ADR 0001。
- 客户端产品接口：使用版本化 REST/JSON 与 OpenAPI，见 ADR 0002；Host Agent IPC 仍待决策。
- 文档与溯源：使用 `AGENTS.md` 路由到术语、状态、架构、ADR、规格、调查和运行手册，Git 保存实现历史。
- 不直接使用 fnOS 私有产品层作为商业代码底座。
- 不假定 fnOS 基于 OMV。

### 13.2 强建议但尚未正式冻结

- 裸机使用 SSH 密钥与自动化部署；
- 产品层从模块化单体开始；
- 将极少数特权操作收敛到 Host Agent；
- 使用 Fake/Linux Adapter 支撑本地开发和实机测试；
- MVP 数据盘先使用单盘 Btrfs，后续增加镜像验证；
- SMB 为第一共享协议；
- AI 使用统一 Provider Interface；
- 前端自研，不依赖 fnOS 构建产物。

### 13.3 待决策

- Host Agent 与产品层之间的 IPC 协议；
- 前端框架；
- SQLite 或 PostgreSQL；
- Docker/Moby 或 Podman；
- Btrfs RAID1，还是 md RAID1 + Btrfs；
- 身份模型、ACL 映射和远程访问方案；
- OTA/A-B 更新和恢复分区设计；
- 云端多模态 Provider 与数据合规策略；
- 本地模型 Runtime、GPU/NPU 和最终硬件 SKU；
- 应用包格式和第三方应用安全模型。

Go 的选择记录在 `docs/adr/0001-use-go-for-product-services.md`，客户端产品接口的 REST/OpenAPI 选择记录在 `docs/adr/0002-use-rest-openapi-for-product-clients.md`。进程权限边界、Host Agent 类型化 IPC 和 Adapter 设计仍需通过原型验证。

## 14. 建议的近期里程碑

### M0：打通实验机

- Debian amd64 最小安装完成；
- M.2 为系统盘，机械盘保持未误格式化；
- 创建非 root 开发账号并完成 SSH 密钥登录；
- 记录 `lsblk`、`lspci`、网卡、SMART、温度和 systemd 基线；
- 主电脑可自动收集诊断信息。

### M1：建立可开发骨架

- 初始化 Git 仓库、README、ADR 和基础 CI；
- 建立 API 契约、产品服务空壳、Host Agent 空壳；
- 完成 Fake Adapter 和契约测试；
- 本机可以启动 Web/API 并显示模拟 NAS 状态。

### M2：只读硬件闭环

- 实机发现磁盘、容量、序列号、总线和系统盘身份；
- 获取 SMART 和温度；
- Web 显示健康状态；
- 全流程不要求产品 Web 进程以 root 运行。

### M3：安全存储与共享闭环

- 对非系统实验盘生成格式化计划；
- 显式确认后创建 Btrfs 数据卷并持久挂载；
- 创建用户、目录与 SMB 共享；
- 验证权限、重启、快照、恢复和诊断。

### M4：AI API 闭环

- 导入一组明确授权的测试文件；
- 完成元数据、缩略图、OCR/视觉理解和索引；
- 通过自然语言检索并返回原文件引用；
- UI 显示云端外发范围、任务状态和审计记录；
- Mock/云端 Provider 契约一致，为本地模型预留替换点。

## 15. 历史研究的定位

早期研究曾围绕 Orange Pi 3B / RK3566 / ARM64 展开，相关结论对板卡镜像、U-Boot、DTB、Armbian 和 OPHUB 适配层仍有参考价值。但当前 MVP 主机已转向 Intel i3-12100 x86-64，因此：

- 当前系统镜像选择 Debian amd64；
- 不需要 Orange Pi 的 U-Boot/DTB/TF 卡适配链；
- `ophub/fnnas` 不进入当前产品运行时；
- ARM 研究保留为未来低功耗 SKU 或兼容性参考，不影响当前主线。

## 16. 原始调研资料

迁移前的详细研究位于：

- `F:\NAS\research\fnnas-architecture-analysis.md`
- `F:\NAS\research\omv-fnnos-comparison.md`
- `F:\NAS\research\nas-os-four-way-user-feature-matrix.md`
- `F:\NAS\research\nas-os-open-source-options.md`
- `F:\NAS\research\ai-first-home-nas-architecture.md`

其中早期 AI 架构文档曾把 Go 写为既定选择。当前已更正：**架构边界暂定，产品层实现语言未定。**

主要公开参考：

- [Debian](https://www.debian.org/)
- [OpenMediaVault](https://github.com/openmediavault/openmediavault)
- [CasaOS](https://github.com/IceWhaleTech/CasaOS)
- [TrueNAS Community Edition](https://www.truenas.com/truenas-community-edition/)
- [ophub/fnnas](https://github.com/ophub/fnnas)
- [Samba](https://www.samba.org/)
- [Btrfs documentation](https://btrfs.readthedocs.io/)
- [llama.cpp](https://github.com/ggml-org/llama.cpp)
- [ONNX Runtime](https://onnxruntime.ai/)
