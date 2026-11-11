# 设备镜像与 systemd-sysupdate 的验证（ADR 0034 第 3 步）

> 状态：完成（2026-11-10，开发机 B 的 QEMU 虚拟机）；结论写入 [ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md#第-3-步的验证结论2026-11-10)  
> 更新日期：2026-11-10  
> 适用范围：Debian 13 的 systemd 257.13-1~deb13u1（systemd-sysupdate、systemd-boot、systemd-repart、systemd-sysext）、mkosi 25.3、QEMU 10.0.13、OVMF 2025.02；客机内核 6.12.116+deb13-amd64  
> 关联：[ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)、[镜像配置与驱动脚本](../../deploy/sysupdate-spike/verify.sh)、[ADR 0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)

ADR 0034 决定 2 提出：家里的设备以 mkosi 构建的整机镜像运行，systemd-boot 加载统一内核镜像（UKI）并按启动计数判断新版本，systemd-sysupdate 从 HTTPS 下载带版本的分区镜像与 UKI。第 3 步要证明 Debian 13 自带的 systemd 257 能满足六项要求，否则改用 RAUC。本文记录做法、结果与复现步骤。

## 结论

六项在 Debian 13 的 systemd 257 上都能满足，不需要更新的 systemd，也不需要改用 RAUC。有两处 257 的缺陷要在镜像里绕开，另有几条配置上的注意事项，见下表与“要带进设计的事”。

| 项 | 结果 | 要点 |
|---|---|---|
| 1. v1 → v2 后从 v2 启动并被标为成功 | 通过 | v2 的 UKI 以 `a-nas_2+3-0.efi` 装入；从它启动、健康检查通过后，`systemd-bless-boot` 把它改名为 `a-nas_2.efi` |
| 2. 坏的新版本自动退回 | 通过 | 健康检查失败的 v3、内核崩溃的 v4 各试启动 3 次，第 4 次 systemd-boot 选回 v2；坏版本的 UKI 留在 ESP 中，名为 `+0-3` |
| 3. 写入中途断电后仍能启动 | 通过 | 在写 `/usr` 槽位时、下载 UKI 时、服务器发完 UKI 时各杀一次 QEMU，三次都从 v2 正常启动；写了一半的槽位仍标为 `_empty`，之后 v5 正常装上 |
| 4. 数据分区与数据卷不变 | 通过 | `/var/lib/a-nas`、`/etc/a-nas` 与数据卷上文件的 SHA-256 在每次切换与断电后都与写入时相同 |
| 5. 下载量；不变的组件不再下载 | 通过 | 一次系统更新下载 748.0 MiB（`/usr` 镜像 611.7 MiB + UKI 136.3 MiB），没有增量；没有新版本时只取 282 字节的清单与签名；模型作为单独组件，不变时 204 字节，变了才下载 |
| 6. 产品层做成 sysext、由 sysupdate 单独更新 | 可行，有约束 | 不重启即可从产品 1 升到 2（下载 4.3 KB）；257 上要绕开两处缺陷，软件包布局有硬约束 |

### 257 的两处缺陷与绕法

1. **sysext 合并后，`/usr` 的 transfer 用 `Path=auto` 会失败。** sysupdate 257 从 `/usr` 所在的块设备找整块磁盘；sysext 把 `/usr` 合成 overlayfs 后没有块设备，报 `Failed to find whole disk device for partition backing file system: No such device`，系统更新完全做不了。上游有同样的报告（[systemd#36311](https://github.com/systemd/systemd/pull/36311)），258 起 sysupdate 会穿过 sysext 的 overlayfs 找回块设备（`get_sysext_overlay_block`，258–261；262 改名为 `extension_overlay_block`）。
   - 绕法：`Path=/run/a-nas/system-disk`；镜像中的 `a-nas-system-disk.service` 在开机时用 `bootctl -RR`（根文件系统所在的整块磁盘）创建这个链接。验证中链接指向 `/dev/vda`，之后的系统更新全部正常。
2. **sysext 中的服务开机不启动，sysusers 账号不存在。** systemd-sysext 在 systemd-sysusers 之后才合并，而 sysusers 以 `ConditionNeedsUpdate=/etc` 判断“无需运行”而跳过；开机事务在合并前就已排好，sysext 带进来的 `.wants` 链接不生效。257 的手册也写明 sysext 不适合放系统服务与 sysusers 定义。
   - 实测：不加辅助单元时，演示服务未启动，账号不存在（第一轮与第三轮相同）。
   - 绕法：`a-nas-sysext-ready.service`，排在 systemd-sysext 之后、tmpfiles 之前，依次执行 `systemd-sysusers`、`daemon-reload` 与 `systemctl restart --no-block sockets.target timers.target multi-user.target`，与 Flatcar 的 `ensure-sysext.service` 相同。启用后服务开机即运行，账号存在。

### 要带进设计的事

- **关掉 sysupdate 自带的定时器。** 回退到 v2 后，`systemd-sysupdate list` 把坏掉的 v3 标为 `current`，`systemd-sysupdate reboot` 认为“已安装的 v3 比正在运行的 v2 新”而重启，再由 systemd-boot 选回 v2。257 的 sysupdate 不看启动评估的结果，所以 `systemd-sysupdate-reboot.timer` 会让设备按定时反复重启。mkosi 构建时执行 `systemctl preset-all`，Debian 默认启用所有带 `[Install]` 的单元，这两个定时器在镜像中都已启用。设备镜像要用 preset 文件禁用它们，由产品的更新服务决定何时检查、安装与重启。
- **回退后同一个坏版本不会重装。** v3 退回后再运行 `update` 只下载清单（445 字节）：sysupdate 认为 v3 已安装。设备停在 v2，直到发布更新的 v4，v4 写进 v3 的槽位。
- **健康检查必须能让设备重启。** 健康检查单元带 `FailureAction=reboot`，内核命令行带 `panic=`；否则坏版本会一直运行，等有人重启。内核卡死（不崩溃）时要硬件看门狗才能重启，这次没有测。
- **`/etc` 只保留管理员的配置。** 持久的根分区上的 `/etc` 停留在第一次安装时的样子：v2 中只从 `/etc` 启用的单元在升级后的设备上没有运行，从 `/usr` 的 `.wants` 链接启用的单元正常运行。v2 新增的 sysusers 账号在升级后出现了，因为 mkosi 让每个版本的 `/usr` 时间戳比 `/etc/.updated` 新，`ConditionNeedsUpdate=/etc` 因此成立。
- **ESP 每次断电都留下“未正常卸载”。** 三次断电后内核都报 `FAT-fs (vda1): Volume was not properly unmounted`，没有发现文件损坏。最后一次在 ESP 中留下 136 MiB 的临时 UKI（`.#sysupdatea-nas_5+3-0.efi…`），下一次成功的更新之后它不见了。512 MiB 的 ESP 放得下两个 UKI 加一个临时文件，再小就不够。可以只在需要时挂载 ESP（automount），并在镜像中带上 `fsck.vfat`。
- **主机侧调用 CLI。** Debian 13 没有打包 257 引入的 `systemd-sysupdated` 与 `updatectl`；上游已宣布其 D-Bus 接口将在 263 删除、改用 Varlink。Host Agent 的更新服务应调用 `systemd-sysupdate --json=short`（`list`、`check-new`、`update`、`pending`、`--component=`）。

## 第 6 项：产品层做成 sysext 时软件包的约束

演示软件包按 Debian 习惯布局（服务、sysusers、tmpfiles、`/etc` 中的配置文件、`postinst`），做成 sysext 后：

| 软件包里的东西 | 在 sysext 中 | 实测 |
|---|---|---|
| `/usr`、`/opt` 下的文件 | 保留 | 服务的程序与单元都在 |
| `/etc/a-nas/demo.conf`（配置文件） | 丢弃：sysext 只合并 `/usr` 与 `/opt` | 构建时被剔除 |
| `postinst`（`adduser`、`systemctl enable`） | 从不执行 | `postinst` 要建的账号不存在 |
| `/usr/lib/sysusers.d/` | 生效，但要辅助单元 | 加辅助单元前账号缺失，加后存在 |
| `/usr/lib/systemd/system/*.wants/` 链接 | 生效，但要辅助单元 | 同上 |
| `/usr/lib/tmpfiles.d/` 的 `C` 行 | 生效：tmpfiles 在 sysext 之后运行 | `/etc/a-nas/demo.conf` 由 `/usr/share` 中的默认值复制而来 |
| 运行时更新 | `systemd-sysupdate --component=product update`，`systemd-sysext refresh`，重启服务 | 不重启设备，服务换成产品 2 |

据此，第 2 步设计软件包时：

1. 文件只放 `/usr` 与 `/opt`；单元放 `/usr/lib/systemd/system`，以软件包自带的 `.wants` 链接启用，不靠 `postinst` 或 `/etc` 中的链接。
2. 系统账号用 `/usr/lib/sysusers.d`，目录与默认配置用 `/usr/lib/tmpfiles.d`（`C` 从 `/usr/share` 复制、不覆盖已有文件）；不用 `adduser`、`update-alternatives`、dpkg 触发器或任何维护脚本的副作用。
3. 默认配置放 `/usr`；`/etc/a-nas/site.conf` 只放管理员按宿主机改的值，程序先读 `/usr` 的默认值再读它。
4. 产品依赖的 Debian 软件包必须在基础镜像中：sysext 没有依赖机制。新增依赖要先发一版基础镜像。
5. `extension-release` 写 `ID=debian`、`VERSION_ID=13`，所以同一个产品 sysext 能叠在任何 Debian 13 基础镜像上；它链接的库必须与基础镜像一致（Go 静态二进制没有这个问题）。要更严格地绑定，可在基础镜像的 os-release 中加 `SYSEXT_LEVEL`。
6. 同一份软件包在 Experimental NAS 这类 apt 宿主机上照常安装；`postinst` 只能做不影响结果的事，例如提醒执行 `systemctl daemon-reload`。
7. 在 257 上，基础镜像要带 `a-nas-sysext-ready.service` 与 `a-nas-system-disk.service`（见上）。

## 数据

| 项目 | 数值 |
|---|---|
| `/usr` 镜像（erofs，lz4hc） | 实占 894.5 MiB，放在 3 GiB 槽位中 |
| 下载用的 `/usr` 镜像（xz -6） | 611.7 MiB；zstd -19 为 626.8 MiB（大 2.5%），zstd -3 为 690.8 MiB |
| 本机解压 3 GiB 镜像 | xz 8.9 s，zstd -19 3.5 s（Core Ultra 5 125U，单线程） |
| UKI | 136.3 MiB：内核 11.6 MiB，initrd 124.6 MiB（其中 mkosi 的主 initrd 33.7 MiB，其余是默认附带的内核模块） |
| 一次系统更新的下载 | 784,305,306 字节（748.0 MiB），每个版本相同，无增量 |
| 一次系统更新的耗时（虚拟机） | 102–218 s；下载速率约 6–7 MB/s 压缩数据，即每秒写入槽位约 35 MB。镜像在本机解压只要 9 s，所以时间主要花在把整个 3 GiB 槽位（含末尾的零）写进虚拟磁盘（推断） |
| 没有新版本时的检查 | 282 字节（`SHA256SUMS` 与签名） |
| 模型组件（100 MiB，不可压缩） | 首次与变化时 100.0 MiB，不变时 204 字节；与系统更新互不影响 |
| 产品 sysext（演示） | 4 KiB 镜像，一次更新下载约 4.3 KB |
| 启动到 `multi-user`（KVM） | 6.9–9.5 s；本机负载高时 v2 一次为 22.2 s |
| v3 健康检查失败到回到 v2 | 4 次启动 56 s |
| v4 内核崩溃到回到 v2 | 4 次启动 78 s（每次崩溃后 `panic=10` 等 10 s） |
| 每个版本的构建 | 431–587 s（软件包已缓存；本机负载高时达 1,326 s）；首次另需下载约 500 MB 软件包 |
| 第三轮全程 | 28.5 分钟，17 次启动 |

可以减小下载量的地方（没有实施）：

- 只把设备需要的内核模块放进 initrd（mkosi 的 `KernelModulesInitrdInclude=`），UKI 可从 136 MiB 降到 50 MiB 左右（内核 11.6 MiB、主 initrd 33.7 MiB 加上少量模块），每次系统更新少下载约 85 MiB。
- 产品放在 sysext 中，系统镜像只在 Debian 或基础依赖变化时才发布；产品更新只下载 sysext。
- `/usr` 镜像按内容大小生成（systemd-repart 的 `Minimize=`），槽位仍固定 3 GiB，可以少写约 2 GiB 的零。

## 环境

- 开发机 B：Windows 11、WSL 2.7.11（Ubuntu 26.04，内核 6.18），Core Ultra 5 125U，WSL 约 7.7 GB 内存。KVM 可用，但当前账号不在 `kvm` 组，所以构建与虚拟机都在一个特权容器中运行。
- 容器 `anas-sysupdate-builder:trixie`：以系统测试的 Debian 13 镜像为底，装 Debian 13 的 mkosi 25.3、systemd-boot、systemd-ukify、systemd-repart、systemd-container、QEMU 10.0.13、OVMF、erofs-utils 1.8.6。没有用 Debian 13 以外的软件包。
- 虚拟机：QEMU q35 + KVM，2 个 vCPU，1.5 GB 内存，OVMF（不开安全启动）。系统盘与数据卷是两块 virtio 磁盘，网络为 QEMU 用户态网络。每次重启都让 QEMU 退出（`-no-reboot`），由驱动脚本重新启动，这样每次启动各有一份串口日志。
- 更新服务器：容器中的 Python HTTPS 服务器，证书由测试 CA 签发（CA 装进镜像），记录每个响应的字节数，可以按文件限速。`SHA256SUMS` 用验证时生成的 GPG 测试密钥签名，公钥装进镜像的 `/usr/lib/systemd/import-pubring.gpg`。测试密钥只用于这次验证。
- 存储：构建缓存与产物放在 D 盘上的一个 ext4 镜像文件中，由容器挂载；mkosi 的临时工作区和虚拟机磁盘放在容器自己的磁盘上，用完即删。WSL 的虚拟磁盘在 C 盘上，删文件不会还回空间，第一次运行时它把 C 盘写满过。

## 镜像与更新的布局

配置在 [`deploy/sysupdate-spike/`](../../deploy/sysupdate-spike/)。

| 分区（GPT） | 大小 | 内容 |
|---|---|---|
| `esp`（vfat） | 512 MiB | systemd-boot 与每个版本的 UKI：`/EFI/Linux/a-nas_<版本>[+<剩余>-<已用>].efi` |
| `a-nas_<版本>`（usr，erofs，lz4hc） | 3 GiB | 一个版本的 `/usr`，只读 |
| `_empty`（usr） | 3 GiB | 第二个 `/usr` 槽位；systemd-sysupdate 把标为 `_empty` 的分区当作空闲 |
| `a-nas-state`（root，ext4） | 2 GiB | 持久的根分区：`/etc` 与 `/var`（含 `/var/lib/a-nas`），切换 `/usr` 槽位时不动 |
| 数据卷（另一块磁盘，ext4） | 2 GiB | 用户数据，模拟 `/srv/a-nas`，经 `/etc/fstab` 挂载 |

- 镜像内容：Debian 13，加上 [`a-nas-host`](../../deploy/debian/a-nas-host/DEBIAN/control) 与 [`a-nas-console`](../../deploy/debian/a-nas-console/DEBIAN/control) 的全部依赖（Samba、FFmpeg、Chromium、cage、Noto CJK 字体等），所以 `/usr` 的大小与真实设备相当。
- 每个版本的 UKI 带自己的内核命令行：`mount.usr=PARTLABEL=a-nas_<版本>` 选中自己的 `/usr` 槽位，`root=PARTLABEL=a-nas-state` 指向持久的根分区，`panic=10` 让内核崩溃后重启。
- 健康检查：`a-nas-health.service` 由 `/usr` 中的 `boot-complete.target.requires/` 链接启用，`FailureAction=reboot`。它通过后 `boot-complete.target` 达成，`systemd-bless-boot` 去掉 UKI 文件名中的计数；它失败就重启，下一次启动由 systemd-boot 计数。
- 更新组件（`/usr/lib/sysupdate*.d/`）：
  - 默认组件：`50-usr.transfer` 把 `a-nas_<版本>.usr.raw.xz` 写进空闲的 `/usr` 槽位；`60-uki.transfer` 把 `a-nas_<版本>.efi` 以 `+3-0`（三次启动机会）装进 ESP。两者各保留 2 个版本，当前启动的版本受 `ProtectVersion=%A` 保护。
  - `models`：`a-nas-models_<版本>.tar` 解包到 `/var/lib/a-nas/models/models_<版本>`，`current` 链接指向最新的一个。
  - `product`：产品层 sysext `a-nas-product_<版本>.raw` 放在 `/var/lib/a-nas-sysext/`，`/var/lib/extensions/a-nas-product.raw` 链接指向当前版本。
- 五个系统版本：v1、v2 正常；v3 的健康检查故意失败；v4 的内核命令行加了 `rdinit=/nonexistent`，内核找不到 init 就崩溃；v5 正常。v2 起比 v1 多一个 sysusers 账号、一个从 `/usr` 启用的单元和一个只从 `/etc` 启用的单元。
- 产品层：一个按 Debian 习惯布局的演示软件包 `a-nas-demo`（服务、sysusers 账号、tmpfiles、`/etc` 中的配置文件、`postinst`），由驱动脚本解包，只留 `/usr` 与 `/opt`，加 `extension-release.a-nas-product`（`ID=debian`、`VERSION_ID=13`、`EXTENSION_RELOAD_MANAGER=1`），做成 erofs 镜像。这是 mkosi `Format=sysext` 会得到的内容。

## 做法

### 各项的做法

1. **升级与标记成功**：在 v1 上装好产品与模型后，发布 v2，运行 `systemd-sysupdate update`，重启。检查从 v2 启动、`systemd-bless-boot.service` 与健康检查都已运行、ESP 中 v2 的 UKI 名为 `a-nas_2.efi`。
2. **自动退回**：发布 v3（健康检查失败）与 v4（内核崩溃），各自更新、重启。驱动脚本一直重启虚拟机直到能 SSH 登录，并从每次的串口日志读出内核命令行中的 `mount.usr=PARTLABEL=a-nas_<版本>`，得到每次尝试的版本。v3 之后另跑一次 `update` 与 `systemd-sysupdate reboot`。
3. **断电**：发布 v5，在三个时刻杀掉 QEMU（`kill -9`）：
   - 写 `/usr` 槽位时：`/usr` 镜像开始下载 5 秒后，限速 20 MB/s；
   - 下载 UKI 时：UKI 开始下载 2 秒后，限速 5 MB/s；
   - 服务器发完 UKI 时：此时客户端已收到 97%。

   每次之后启动，检查启动的版本、槽位标签、ESP 中的文件与数据。最后不限速更新到 v5。

   杀掉 QEMU 丢掉的是客机内存和客机尚未写出的页缓存；QEMU 已交给宿主机的写入不会丢，不模拟磁盘自身写缓存的丢失。sysupdate 安装步骤（改分区标签、改 UKI 文件名）只有毫秒级的窗口，这次没有碰到。日志显示它先给分区打标签、再改 UKI 的名字，所以这两步之间断电时，新版本只有分区没有启动项，设备仍从旧版本启动。
4. **数据不变**：v1 首次启动后写入 `/var/lib/a-nas/data`（64 MiB 随机数据与一个小文件）、`/etc/a-nas/site.conf` 与数据卷 `/srv/a-nas`（32 MiB），记下 SHA-256；之后每次切换版本和断电后重新计算并比较。
5. **下载量**：服务器记录每个响应的字节数，驱动脚本按每次 `update` 汇总。依次测了：系统更新；没有新版本；模型不变；只有模型变化时的系统更新与模型更新。
6. **产品层**：在 v1 上以 `--component=product` 装入产品 1，先禁用辅助单元重启一次，再启用它重启一次，分别检查 sysext 是否合并、服务是否运行、sysusers 账号、`postinst` 的效果与 tmpfiles 复制的配置；再用 `Path=auto` 的副本运行 `systemd-sysupdate list`，复现缺陷 1。最后在 v5 上不重启更新到产品 2。

### 三轮运行

1. 第一轮的系统更新全部失败，由此发现缺陷 1；产品与模型组件正常，并得到缺陷 2 的“未加辅助单元”结果。这一轮的产物让 WSL 的虚拟磁盘涨满了 C 盘，之后改为把产物放在 D 盘。
2. 第二轮加入两个绕法后，六项都已通过，但有两处问题：
   - 辅助单元已被 mkosi 的 `preset-all` 启用，“未加辅助单元”那次并不成立；
   - 断电的等待上限太短，“下载 UKI 时”那次实际断在写 `/usr` 期间。
3. 第三轮修正了这两处，上面的结果与数据都来自这一轮。

## 复现

在有 Docker 与 `/dev/kvm` 的 Linux 上（WSL 可以）：

```bash
WORK=/mnt/d/A-NAS-sysupdate-verify/work.img deploy/sysupdate-spike/verify.sh
```

- 依次执行 `keys build run clean`：生成测试密钥；构建五个系统版本、两个产品 sysext 与两份模型；在虚拟机中跑完各项；把 `results.log` 与日志复制到 `WORK` 旁边的 `work-results/`，再删除容器、构建镜像与 `WORK`。
- 首次构建要从 deb.debian.org 下载约 500 MB 软件包，之后每个版本 7–22 分钟（视本机负载），全部约 1–1.5 小时；`run` 约 30 分钟。
- 构建镜像以系统测试的 `anas-systemd:trixie` 为底，先用 [`scripts/build-system-test-image.sh`](../../scripts/build-system-test-image.sh) 建好它。
- 只想重建一个版本时运行 `verify.sh os <版本> <good|health-broken> [内核参数]`。
