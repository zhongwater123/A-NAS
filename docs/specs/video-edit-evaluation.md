# 视频剪辑方案测评

状态：draft
更新时间：2026-11-02

## 目标

两套视频自动剪辑方案（[#341](https://github.com/zhongwater123/A-NAS/issues/341)）用同一套测试参数和指标口径测评。两位开发在各自的 x86_64 设备上运行同一个测评管线，产出可以直接比较的资源、耗时和链路数据。结果包在 Claude 开发机上统一核对、汇总，并为项目方组织的盲评准备材料。

## 非目标

- **成片质量**：只由项目人员盲评判断。管线不做质量评分，不设质量阈值；盲评的维度和分制由项目方定。
- **宿主机上的真实耗时**：到原型阶段再测，测评不在实验 NAS 上运行。
- **GPU**：测评容器不挂载任何 GPU 设备，硬件编解码不参与测评。依赖核显的方案需要软件回退。

## 口径 v1

### 宿主机约束配置

按实验 NAS 的实测（2026-11-02）定。开发时建议也按这份配置限制环境。

| 项 | 宿主机 | 测评时的限制 |
|---|---|---|
| CPU | i3-12100，4 核 8 线程，x86_64；支持 AVX2 / AVX-VNNI / FMA / F16C，不支持 AVX-512 | 容器只能用 8 个 CPU（`--cpuset-cpus=0-7`）；构建产物不能依赖 AVX-512 |
| GPU | 只有 Intel UHD 730 核显 | 不挂载 GPU |
| 内存 | 共 7.5 GiB，空闲时可用约 4.4–5.7 GiB；后台 AI 只在加载后仍余 1 GiB 时才加载 | 容器内全部进程合计不超过 3.4 GiB（4.4 − 1），不用 swap |
| 系统 | Debian 13，FFmpeg 7.1，Python 3.13 | 测评镜像：Debian 13，由 snapshot.debian.org 固定时间点的软件包组成 |

模拟不了的部分是单核速度和内存带宽：宿主机是单通道 DDR4-2667。所以开发设备上的耗时不等于宿主机上的耗时。

### 测试参数

| 参数 | 规定 |
|---|---|
| 设备 | x86_64 的 Linux，或 Windows 的 WSL2；装有 Docker，cgroup v2，至少 8 个 CPU。管线拒绝在其他设备上运行 |
| 管线 | 双方用同一个提交。`ai/eval/video_edit` 有未提交的改动时，管线拒绝正式运行 |
| 测试集 | Release `video-edit-testset-v1`，每个文件按清单的 sha256 核对 |
| 用例要求 | `case.json` 原样交给方案 |
| 环境 | 测评镜像的环境指纹（全部 Debian 软件包和 Python 包的版本）必须相同 |
| 模型与依赖 | 由方案的 `setup.sh` 事先装进 `/env`，下载和安装时间不计 |
| 计时 | 每个用例用一个新容器。从方案进程启动计到它退出，包括模型加载 |
| 次数 | 每个用例跑一次，不重跑 |
| 时限 | 每个用例 3 小时。超时的用例记为失败 |
| 断网检查 | 声明了 `lan-gpu` 或 `cloud` 步骤的方案，另用 `--network none` 再跑一遍全部用例，只记录能否出片 |
| 对照方案 | 每台设备都跑一遍对照方案（[`baseline`](../../ai/eval/video_edit/baseline/)），作为这台设备的耗时基准 |

### 指标

| 指标 | 定义 | 来源 |
|---|---|---|
| 耗时 | 方案进程从启动到退出的时间 | 容器内计时 |
| 各步耗时 | 名为步骤 id 的 span 的时长之和 | `trace.json` |
| 处理倍率 | 耗时 ÷ 用例素材总时长 | 计算 |
| 相对耗时 | 耗时 ÷ 同一台设备上对照方案在同一用例的耗时 | 计算 |
| CPU 时间 | 容器 cgroup 的 `cpu.stat` 中 `usage_usec` | 容器内读取 |
| 内存峰值 | 容器 cgroup 的 `memory.peak`，含读素材留下的页缓存 | 容器内读取 |
| 匿名内存峰值 | 进程自己分配、不能回收的内存，按 `memory.stat` 每秒采样的最大值；文件映射同样记录 | 容器内每秒读取 |
| OOM | `memory.events` 中的 `oom_kill` | 容器内读取 |
| 体积 | `/env` 和方案目录的磁盘占用；`/tmp` 与输出目录的峰值，每 5 秒统计一次 | 管线统计 |
| 外部计算 | 外部调用的次数、上传字节、token、费用、耗时 | `trace.json` |
| 网络流量 | 容器网卡的收发字节 | 容器内读取 |
| 断网能否出片 | 能 / 不能 | 断网运行 |

- **跟设备无关**：内存、OOM、体积、外部计算和链路核对，可以直接比较。
- **跟设备有关**：耗时和 CPU 时间。报告同时列原始值和相对耗时。对照方案以 FFmpeg 的解码和编码为主，而不同 CPU 做模型推理的快慢比例不同，所以相对耗时只是近似值。
- **改口径**：改了就升版本（v2），已有结果按新口径重跑。结果包里写明口径版本。

### 方案目录

方案交一个目录：

- `pipeline.yaml`：链路说明，结构见 [`pipeline.schema.json`](../../ai/eval/video_edit/schemas/pipeline.schema.json)；
- 可选的安装脚本：`pipeline.yaml` 的 `setup` 指向它；
- 方案代码。

**`pipeline.yaml`** 给人和 agent 读同一份：

- **整体**：方案名、一句话概述、入口命令、依赖和模型清单。模型写明来源、revision、sha256、在 `/env` 里的路径和许可证。
- **每一步**：id、类别、做什么、输入、输出、用什么、在哪里跑，以及哪些数据离开宿主机、发给谁，失败时怎么办。
- **类别**固定为：
  - `decode` 读取与解码
  - `segment` 切分
  - `understand` 理解
  - `arrange` 选择与编排
  - `refine` 精修
  - `render` 渲染
  - `check` 检查
  - `other` 其他
- **在哪里跑**：`nas-cpu`、`nas-igpu`、`lan-gpu`、`cloud` 之一。`lan-gpu` 和 `cloud` 的步骤必须写明离开宿主机的数据。

**安装**：只运行一次，有网络。挂载情况：

| 路径 | 内容 | 权限 |
|---|---|---|
| `/plan` | 方案目录 | 只读 |
| `/env` | 安装目标 | 可写 |

装完后，管线按 `pipeline.yaml` 核对每个模型文件的 sha256。

**运行**：工作目录是 `/plan`，以调用者的 uid 运行，`HOME=/tmp`。挂载情况：

| 路径 | 内容 | 权限 |
|---|---|---|
| `/plan` | 方案目录 | 只读 |
| `/env` | 安装好的环境 | 只读 |
| `/case` | 测试集 | 只读 |
| `/out` | 输出 | 可写 |
| `/tmp` | 临时文件 | 可写 |

管线把 `--case /case/<id>.json --out /out` 接在 `entry` 之后。`case.json` 里的素材文件与它在同一个目录。方案要写出三样东西：

- `/out/timeline.otio`：剪辑决策（[OpenTimelineIO](https://opentimelineio.readthedocs.io/)）；
- `/out/output.mp4`：成片；
- `/out/trace.json`：OTLP JSON 格式的 `ExportTraceServiceRequest`（[OpenTelemetry](https://opentelemetry.io/docs/specs/otlp/#json-protobuf-encoding)）。

`/out/harness/` 留给管线使用。

**`trace.json` 的约定**：

- 每一步至少一个 span，span 名等于步骤 id。
- 嵌套的 span 计入最近的、名字是步骤 id 的上级 span。
- 每次外部调用是一个 span，带下面这些属性：

| 属性 | 类型 | 内容 |
|---|---|---|
| `anas.external.target` | 字符串 | `lan-gpu` 或 `cloud` |
| `anas.external.service` | 字符串 | 服务或模型名 |
| `anas.external.upload_bytes` | 整数 | |
| `anas.external.input_tokens` | 整数 | |
| `anas.external.output_tokens` | 整数 | |
| `anas.external.cost` | 小数 | |
| `anas.external.currency` | 字符串 | |

对照方案（[`baseline.py`](../../ai/eval/video_edit/baseline/baseline.py)）用 OpenTelemetry SDK 写出这个文件，可以照着做。

## 场景

### 搭建测评镜像

- Given：设备上有一个能安装 debootstrap 的 Debian 镜像（默认 `debian:bookworm`），可以访问 snapshot.debian.org 与 PyPI。
- When：运行 `python3 -m eval.video_edit image`。
- Then：
  - 用固定时间点的 Debian 13 软件包组成 `anas-video-edit-eval:v1`，管线自己的 Python 包按哈希安装进 `/opt/harness`；
  - 两台设备构建出的镜像，环境指纹相同。

### 安装方案

- Given：方案目录里有 `pipeline.yaml`。
- When：运行 `setup --plan <目录> --env <目录>`。
- Then：
  - `pipeline.yaml` 不符合结构，或步骤 id 重复，就报错退出；
  - 否则运行安装脚本，核对每个模型的 sha256，记下 `/env` 与方案目录的体积；
  - 模型缺失或哈希不符就报错退出。

### 正式运行

- Given：
  - 方案已安装；
  - 测试集按清单下载齐全（`fetch`）；
  - 管线目录没有未提交的改动。
- When：运行 `run --plan … --env … --testset … --manifest … --out <结果目录>`。
- Then：
  - 管线先核对设备、测试集哈希、每个 `case.json` 的结构和素材哈希，有任何一项不符就拒绝运行；
  - 然后用 ffprobe 读出素材时长，再逐个用例运行；
  - 每个用例记下 `run.json` 和每秒采样，与方案的三个输出放在一起；
  - 声明了外部计算的方案，接着做断网运行。

### 失败的用例

- Given：方案进程非零退出、被 OOM 杀掉、超过时限，或少了三个输出中的任何一个。
- When：这个用例运行结束。
- Then：
  - 这个用例记为失败，并写明原因，已采到的指标照常保留；
  - 管线继续运行下一个用例，不重跑。

### 打包

- Given：结果目录里有一个方案和对照方案的结果，来自同一台设备、同一个管线提交和环境指纹。
- When：运行 `pack --runs <结果目录> --out <包>.tar`。
- Then：
  - 结果包里有每个文件的 sha256、口径版本、管线提交、环境指纹、测试集清单哈希和设备信息；
  - 两份结果的来源不一致时，拒绝打包。

### 核对与报告

- Given：方案 A、B 的结果包。
- When：在 Claude 开发机上运行 `report --a A.tar --b B.tar --out <目录>`。
- Then：
  - 管线核对每个文件的哈希，并要求两个包的口径版本、管线提交、环境指纹、测试集清单和用例都相同，有一项不同就拒绝出报告；
  - 核对通过后生成两张方案卡和一份对比报告；
  - 链路核对的结果写进报告：没有声明的步骤、没有出现的步骤、没有声明的外发、没有声明的网络流量，以及无法读取的剪辑决策。

### 盲评材料

- Given：两个核对通过的结果包。
- When：运行 `blind --a … --b … --out <目录> --key <对照表>`。
- Then：
  - 每个用例的两条成片随机命名为 X、Y；
  - 对照表写到评审目录之外；
  - 生成一份每行一条成片的评审表，评审列由项目方自己加；
  - 评审填完后，`unblind` 把评审表里的 X、Y 换回方案名，并为数字列算出各方案的平均值。

## 边界与失败

- 正式运行要求管线工作区干净。`--dev` 允许在有改动的工作区上试跑，但结果包会标为试跑，`report` 拒绝使用（加 `--allow-dev` 除外）。
- 测试集冻结前，可以用 `manifest` 为本地目录生成清单来试跑。正式清单冻结后放在 `ai/eval/video_edit/testset-v1.json`。
- 解压结果包时只接受普通文件和目录。
- 方案写进 `/out/harness/` 的内容会被管线覆盖。

## 验收证据

- `make ai-test`：`ai/tests/test_video_edit.py` 覆盖以下内容：
  - trace 解析、链路核对和指标计算；
  - 结果包的哈希核对；
  - 盲评的打乱与还原。
- 在 Claude 开发机上，用合成的单视频用例和多段素材用例，按「安装 → 运行 → 打包 → 报告 → 盲评 → 还原」完整跑过一遍对照方案。

## 关联

- 架构：不涉及
- ADR：不涉及
- 代码：[`ai/eval/video_edit/`](../../ai/eval/video_edit/)
- 测试：[`ai/tests/test_video_edit.py`](../../ai/tests/test_video_edit.py)
- Issue：方案对比 [#341](https://github.com/zhongwater123/A-NAS/issues/341)、测试素材 [#342](https://github.com/zhongwater123/A-NAS/issues/342)、测评管线 [#343](https://github.com/zhongwater123/A-NAS/issues/343)、测评结果 [#344](https://github.com/zhongwater123/A-NAS/issues/344)
