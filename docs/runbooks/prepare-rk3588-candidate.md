# 准备与验收 RK3588 ARM64 候选包

公开记录中的 `REDACTED` 值是实例标识的占位符。执行前须从本地封存清单恢复真实值并重新完成目标识别与确认。

状态：draft
更新时间：2026-11-18

## 目的

准备 Debian 13 arm64 主程序、内容包及上游软件包；安装前先验证内核与设备身份，再安装到可恢复的测试系统。当前候选目录、固定源码与通过项见[研究记录](../research/rk3588-deployment-feasibility-2026-11.md#最新主干与补齐后的内核)。候选包不是 Rockchip 烧录镜像，也不是已验收的正式发布。

## 前提与风险

- 目标为已由配套烧录工具启动的 ATK-DLRK3588 测试板。先记录板卡修订、系统镜像来源与 SHA-256、串口输出、`/etc/machine-id`、网卡地址及 eMMC/SSD 的序列号。不得沿用实验 NAS 的旧 IP 识别目标。
- 安装会写入目标系统的 `/usr`、`/opt`、`/etc` 和服务状态；使用可重刷的 eMMC 测试系统，并保留原镜像及厂商恢复入口。SSD 不得含唯一数据。
- 原厂 Buildroot 不符合部署前提。内核和原系统备份按[内核手册](build-rk3588-kernel.md#原系统备份与激活门禁)准备；只备份 boot 不足以回退整个根文件系统，不能因软件包测试通过就跳过系统恢复验证。
- 停止条件：目标身份不符、哈希不符、架构错误、Landlock ABI 小于 3、依赖解算失败或安装后的健康检查失败。不能用关闭隔离绕过失败。
- 构建和软件包检查可在开发机执行，不接触任何真实数据盘。当前全量验证结果以[研究记录](../research/rk3588-deployment-feasibility-2026-11.md)为准。

## 开发机准备

1. 固定源码及未提交改动，记录 `git rev-parse HEAD`、版本和源码补丁。使用 ext4 副本、Go 及 Debian 13 的 ARM 交叉编译器：

   ```bash
   export ANAS_TARGET_ARCH=arm64
   make web-build VERSION="$version"
   make build-binaries VERSION="$version" BUILD_DIR="$output/bin"
   bash scripts/build-packages.sh "$output/packages" "$version" "$output/bin"
   ```

   `version` 与 `output` 由本次候选清单给出，不复用其他架构的旧产物。构建器会检查 ELF，缺少 ARM 专用输入时停止。

2. AI 与搜索分别使用 ARM 哈希锁和架构专属 ID：

   ```bash
   bash scripts/build-ai-runtime.sh
   bash scripts/build-search-runtime.sh
   bash scripts/fetch-qdrant.sh
   bash scripts/fetch-llama.sh
   bash scripts/fetch-upstream-packages.sh "$output/content/upstream"
   ```

   将真实运行环境与哈希核验后的文件按[内容构建器](../../scripts/build-content-packages.sh)的目录约定放入 `content`。4B 模型还须按原清单分成两份 GGUF；下载时的原始分片不能代替 GGUF 分片。Hermes 使用独立的真实 ARM 运行环境清单。不得用 stand-in 构建部署包。

3. 构建全部内容包和公开密钥包，将上游 `.deb` 放入安装目录；密钥包只使用仓库公开密钥，不需要私钥。生成 `SHA256SUMS`、逐包架构/版本/依赖与验证清单：

   ```bash
   bash scripts/build-content-packages.sh "$output/packages" "$output/content"
   IMAGE=anas-systemd:pr472-policy bash scripts/apt-repository.sh keyring \
     "$output/packages" "$version" deploy/release/a-nas-archive-key.asc
   ```

   以文件 SHA-256 核对包中 ELF、运行环境与模型，确认没有 amd64 包。当前签名发布、UKI 镜像与自动更新链另有 amd64 假设，不以本候选替代正式发行验证。

## 到货后的验收顺序

1. 串口确认目标身份及 eMMC 启动成功，保存系统镜像、内核配置和启动日志。先执行只读预检：

   ```bash
   bash scripts/arm64-preflight.sh
   ```

   预期为 Debian 13 arm64、CPython 3.13、Landlock ABI 至少 3。QEMU 用户态不支持的 Landlock 调用必须在此与实际服务中验收。

2. 只复制候选的 `packages/` 部署目录，在该目录内运行 `sha256sum -c SHA256SUMS`；任何失败立即停止。先审查依赖与预期变更，再用相同参数实际安装，始终带 `--no-install-recommends`。Debian 基础依赖仍需可信 Debian 源；部署目录并不包含整个 Debian 软件源。先不配置既有 amd64 发布源或整机镜像更新，后续 ARM 升级另行验收。

   本次官方镜像冻结了大部分已装包，不能直接执行 `install ./*.deb`。完成目标、备份和恢复验证后，先保存 dpkg 状态、选择与 `apt-mark showhold`，仅对交付的 `TARGET-INSTALL-PLAN.json` 中七个 Samba 组件解除 hold：`libldb2 libsmbclient0 libtalloc2 libtdb1 libtevent0t64 libwbclient0 samba-libs`。不得解除全部冻结。模拟所选安装方案；不含 `a-nas-console` 的 23 包主服务方案预期仅升级这七项、37 新装、0 移除。实际变化不同则停止并重新审查。

   安装本地控制台的全量方案还须明确选择标准 `chromium`、移除冲突的 `chromium-ozone-wayland`，不使用 purge。已模拟的计划为 7 升级、54 新装、仅移除厂商浏览器；不得顺带移除 GPU 驱动包。保留原系统快照，板端另验显示和视频。其他基础镜像按自身 dpkg 状态重新模拟，不直接沿用此七项名单或数量。

   本板离线安装暂存目录为 `/userdata/anas-product-b9209e01-20261118`，须核对它仍在 p8 且与 p3/p6 不重叠。61 个包与清单逐件校验后，以空 sources/lists、`--no-download --no-install-recommends` 重复同一计划执行；归档在专用 `Dir::Cache::archives` 中按 APT 标准名称硬链接，版本中的冒号编码为 `%3a`。仅传本地 `.deb` 路径而不准备缓存，会在此版本 APT 的实际安装阶段报非绝对路径，模拟无法发现。失败保留状态并重新模拟剩余计划，不进入磁盘初始化。正式 ARM 更新链验收前禁用 `anas-update.path` 与 `.timer`、不配置发布源；不要 mask 产品单元，否则健康检查会识别为覆盖软件包。`site.conf` 仅选择已识别的可信局域网接口（本次先选 `end0`，无载波时 SMB 等待地址）；不借用其他 NAS 配置或凭据。软件包回滚前保存本次产品状态，系统级恢复按原 p6/p3 备份进行。

3. 运行 `a-nas-check`，检查服务日志与失败 unit。逐项验证账号、Web、SQLite、权限切换、Landlock 写入边界、SMB、Qdrant、真实媒体编解码、AI 模型与 Hermes 离线工具协议。云端助手的凭据按现有试用手册另行配置。

4. SSD 初始化只按[数据卷手册](provision-v1.0.1-experimental-storage.md)执行：先匹配序列号和 `/dev/disk/by-id`，核对根文件系统所在 eMMC，确认可丢弃数据与回滚方式；本手册不自动分区或格式化。

5. 使用同一 OCR 数据集测 4B 首 token、整页耗时、字段准确率、并发时可用内存、温度、降频及取消释放。再验证重启、离线依赖、升级回滚、GPU/NPU 与硬解；仿真耗时不作为真机性能承诺。

## 本板的增量升级

仅用于已安装 b9209e01、用户已创建上述 NVMe 数据卷的同一测试板。新固定基准为 `5a3ceb79daa9`，版本以新交付目录 `VERSION` 为准；完整原生门禁及最终 24 包内容审计通过后，只升级六个带版本号的包。18 个内容包须与前一候选逐件 SHA-256 相同，不重新安装模型。

1. 再核对 eMMC CID、根为 p6、NVMe 序列号与数据卷 UUID。新建 p8 专用 `/userdata/anas-upgrade-5a3ceb79-20261118`，拒绝覆盖。复制六包及 `UPGRADE-SHA256SUMS`，验证清单摘要和全部包；使用专用 archives 缓存，空 sources/lists 与 `--no-download`，实际模拟只能是 6 升级、0 新装、0 移除。
2. 保存 dpkg 状态、hold 列表和升级前健康失败名；本板唯一已知失败为未联网的 `unit:smbd.service`，不接受其他失败。维护前通知用户短暂中断。停止本次产品服务后，在 root 专属的新目录保存 `/etc/a-nas`、`/var/lib/a-nas`、`/var/lib/a-nas-host` 与三个数据服务的 SQLite 备份；不把密码、会话或密钥打印或复制到交付报告。备份失败重启旧服务并停止升级。
3. 固定相同参数实际安装，记录 apt/dpkg 日志和退出值；不执行分区或格式化。升级遵循软件包自身的更新单元设置，不附带长期禁用更新机制；该额外步骤曾被自动审批拒绝，已从实际执行脚本移除。检查六包版本、dpkg audit、数据卷及健康失败集合，不能比基线新增失败。异常时按旧六包清单降级；状态回退只使用刚保存的备份并核对期间没有新增用户数据，不盲目覆盖数据库。
4. 一次正常重启验证内核、管理员仍完成设置、NVMe 按 UUID 自动挂载、数据服务和常开策略，恢复主机 ADB 转发并检查 HTTP 200。失败先保留 UART 与 journal；优先恢复包和本次配置，整套系统恢复才使用原 p6/p3 备份，且绝不顺带重建 NVMe。

## 回滚或恢复

### 本板数据卷的可丢弃文件测试

用户通过网页最终确认创建后，先核对存储计划成功、NVMe 序列号 `BOARD_NVME_SERIAL_REDACTED`、数据卷 UUID `BOARD_UUID_REDACTED_03`、挂载为 `/srv/a-nas/data` 的 Btrfs，系统根仍在原 CID 的 eMMC p6。身份不符立即停止；测试不再分区或格式化。仅用 `mktemp -d` 在卷根新建 `.anas-rk3588-qa.*` 目录，保留所有权标记，只在其中创建普通文件与两个测试子卷。验证显式 ACL 授权普通用户写入、只读快照保持原内容并拒绝写入；不触碰已有空间或服务数据。成功后再次检查目录前缀和标记，仅删除本次两个子卷及新目录；失败保留现场和日志，不执行宽泛递归删除。主机日志记录 UUID、输入输出摘要与清理结果；此测试不代替 Web/SMB 端到端验收。

安装或预检失败时保存日志，停止新增服务，不初始化 SSD；首次测试系统可从已保留的厂商镜像重刷 eMMC。已配置数据卷的设备必须先备份应用状态，再按[软件包部署手册](deploy-a-nas-packages.md#回滚或恢复)回退。不要以删除 `/var/lib` 或清空数据卷作为恢复步骤。

## 关联

- [ARM64 交付规格](../specs/linux-arm64-delivery.md)
- [RK3588 评估与验证边界](../research/rk3588-deployment-feasibility-2026-11.md)
- [只读预检](../../scripts/arm64-preflight.sh)、[目标架构选择](../../scripts/lib/target.sh)
- [软件包发布 ADR](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)
