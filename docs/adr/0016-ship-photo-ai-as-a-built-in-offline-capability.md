# 0016：相册 AI 随系统内置安装，设备不联网获取模型

状态：accepted（2026-10-10）；行为见[相册规格](../specs/photo-library.md)，实现见[相册本地 AI（M2）](../architecture/photo-ai.md)。

M2 原计划把 AI Worker 做成可选组件：由管理员决定是否安装，未安装时相册显示“智能处理不可用”。A-NAS 面向不熟悉技术的家庭用户，他们无法判断该不该安装 AI 组件，也不应该去找模型、下载模型；“本地、离线、不泄露隐私”应当默认成立，而不是一项要用户操作的功能。我们决定：本地 AI 是系统内置能力，随 A-NAS 安装和升级；模型与运行环境随发行制品送达设备，设备从不为 AI 访问网络；Worker 的无网络沙箱由测试证明，界面上的隐私说明以此为依据。

## 决定

1. **随系统安装，默认开启**：每次安装或升级都同时安装与该 release 匹配的 AI Worker、Python 运行环境和模型，并启用 Worker 的套接字。产品不提供单独下载、安装、更新或卸载 AI 的入口。用户只看到结果（语义搜索；AI 标签自 2026-10-10 暂停，见 [M2 实施方案](../architecture/photo-ai.md#ai-标签2026-10-10-起暂停)）、后台整理进度、各图库的 AI 开关（见规格）和隐私说明，看不到模型名称、版本或组件状态。AI 仍不是基础相册的前置依赖：Worker 启动中、崩溃、积压或硬件不足时，相册照常可用，搜索退回按名称与用户标签匹配。
2. **不从网络获取**：设备在安装、升级和运行时都不下载模型或 Python 依赖。模型文件和 Python 运行环境是发行制品的一部分：运行环境在开发机上按锁定哈希的 wheel 构建并打包，设备上不运行 pip。开发期由开发机经 SSH 以 `anas-dev` 暂存到 NAS，与 release 走同一条通道；root 执行的安装器按 release 携带的清单核对大小与 SHA-256 后才安装。Worker 需要的系统包（`python3`、`libegl1`、`libgles2`）属于基础系统，与其他 Debian 包一样随系统安装提供。
3. **按内容共享，由 release 引用**：模型和运行环境按内容各存一份在系统盘的 `/opt/a-nas` 下；release 只携带 Worker 代码和引用它们的清单（模型 SHA-256、运行环境 ID）。暂存时只上传 NAS 还没有的模型与 wheel，升级和回滚只改变引用，不重复传输或复制约 1 GB 的内容。不再被任何 release 引用的模型与运行环境不自动删除，清理需要另行确认，与 [ADR 0014](0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md) 的视频对象相同。
4. **隐私由沙箱强制并经测试证明**：Worker 以 `a-nas-ai` 运行在没有网络的命名空间中（`PrivateNetwork=yes`），只允许 Unix 套接字，不能访问数据卷（[ADR 0011](0011-run-the-photo-library-as-a-dedicated-service-identity.md)），只接收相册服务传来的缩略图描述符。系统测试证明 Worker 无法建立网络连接、无法读取数据卷；只有通过这些测试的版本，界面才说明“照片只在这台设备上分析，AI 不联网”。

## 后果

- 首次部署多传约 650 MB（模型 485 MB 加 163 MB 的运行环境压缩包），之后只有模型或依赖变化时才传输；系统盘常驻约 950 MB（运行环境解开约 460 MB）。
- 每台设备升级后都会运行模型：Worker 工作时占用约 1.2–1.8 GiB 内存，空闲后退出；后台处理只在空闲与资源门控允许时进行。
- 模型只随系统版本更新，换模型后后台自动重建向量。用户不能单独回退模型，只能回退整个 release。
- 把模型装进产品就是再分发：对外发行前必须核对权重随附的许可证（Apache-2.0 还是 Gemma 许可），并随系统提供许可证与 NOTICE。Experimental NAS 上的内部测试不受影响。
- 最低硬件由实机测量确定；不满足时不启动 Worker，相册按无 AI 运行；界面不说 AI 不可用，只是不显示整理进度。
- “AI 未安装”不再是产品状态；代码中无 Worker 的路径保留，服务于启动中、崩溃和硬件不足。

## 未选择

- **可选组件，由管理员安装**（原步骤 7）：家庭用户无法判断是否该安装，也让“本地 AI”变成需要解释和操作的功能。
- **设备首次使用时联网下载模型**：依赖外网（模型托管在 Hugging Face，国内网络经常无法访问），首次体验要等待约 500 MB 的下载并处理中断与校验，也与“离线”的承诺矛盾。
- **模型与运行环境随每个 release 复制**：每次部署多传约 1 GB，系统盘为每个 release 各存一份；ADR 0014 已在屏保视频上遇到同样的问题。
- **像屏保视频一样使用独立的媒体通道**：屏保与代码无关，可以单独更换；模型与代码耦合，标签阈值只对校准时的模型成立，Worker 的查询前缀也是模型的使用约定。因此模型由 release 引用，不单独切换。

## 关联

- [相册规格](../specs/photo-library.md)、[相册本地 AI（M2）](../architecture/photo-ai.md)、[相册技术设计](../architecture/photo-library.md)
- [ADR 0011：相册服务身份与 Catalog 授权](0011-run-the-photo-library-as-a-dedicated-service-identity.md)、[ADR 0014：屏保视频不随 release 发布](0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)
- [模型清单](../../deploy/models/embeddinggemma-2-740m.json)、[标签校准报告](../research/photo-ai-label-calibration.md)
- [Python Worker](../../ai/anas_ai/worker.py)、[系统测试](../../scripts/system-test.sh)
