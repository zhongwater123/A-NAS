# 在实验 NAS 上启用剪辑中心的配乐分析

状态：draft（见“已验证与未验证”；实验 NAS 上的安装还没做）
更新时间：2026-11-26（#669 改名：包名 `a-nas-videocut-music`，原来叫 `a-nas-montage-music`）

## 目的

在 Experimental NAS 上装上剪辑中心的配乐分析（[配乐分析设计](../architecture/videocut-music.md)）：allin1 在单独的运行环境里分析成员选的歌。allin1 的商业版权待议，待议期间它不随任何 release 发布，只按本手册手动装在实验 NAS 上。

成功后：

- `anas-videocut-music.socket` 在监听，剪辑服务选歌后几分钟内，这首歌从 `analyzing` 变成 `ready` 或写明原因的 `unusable`；
- 一首 4 分钟的歌的用时与 Worker 峰值内存记进[配乐分析设计](../architecture/videocut-music.md#用时与内存)。

## 前提与风险

- **权限**：管理员 root 会话；暂存用日常账号 `anas-dev`。
- **目标**：主机名 `a-nas-dev`，使用前按 [SSH 运行手册](bootstrap-experimental-nas-ssh.md)核对指纹。
- **前提**：
  - 用户已同意在实验 NAS 上装 allin1（2026-11-18 决定：待议期间只在开发机与实验 NAS 上运行）。
  - 实验 NAS 已装与本手册同一提交的 `a-nas`（[部署 A-NAS 软件包](deploy-a-nas-packages.md)），剪辑中心的开关在实验 NAS 上已打开。
  - 一台 x86_64 的构建机（Windows 开发机的 WSL），有 Docker、jq、`ai/requirements-dev.txt` 固定版本的 uv：NATTEN 要按 NAS 的处理器编译，arm64 的开发机 C 编出的运行环境不能用。
- **资源**：运行环境压缩包约 300 MB，解开约 860 MB；权重约 95 MB。Worker 只在分析时运行，峰值内存见[用时与内存](../architecture/videocut-music.md#用时与内存)，单元上限 `MemoryMax`。
- **风险**：分析期间 Worker 占满它能用的核，放在 `anas.slice` 下，前台操作优先；内存超过上限时只结束 Worker，这首歌 5 分钟后再试，连续失败 3 次后记为不可用（`analysis_failed`）。
- **停止条件**（出现任一项立即停止）：
  - 构建报告下载与 `deploy/videocut-music/sources.json` 或锁文件的哈希不符、`pip check` 不通过，或 `madmom's model files are in the runtime`；
  - apt 报告依赖无法满足或版本不是 `a-nas` 的同一版本；
  - `journalctl -u anas-videocut-music` 出现 `does not match` 的权重核对失败或 `oom-kill`。

## 已验证与未验证

- **已验证**（2026-11-18，开发者的记录见 [#579](https://github.com/zhongwater123/A-NAS/pull/579) 的提交 `c627d582`、`9a8b031e`）：
  - 第 1 步在 Windows 开发机的 WSL（x86_64）上完整跑过，得到运行环境 `40109f4bba9b6c36.tar.gz`；
  - 用签名软件源里的软件包装进 Debian 13 的 systemd 容器，一首 150 秒的歌分析到 `ready`：分离 88.8 秒、allin1 302.2 秒（7 线程），峰值 2145 MiB。
  - 2026-11-19，实验 NAS 上的第 4 步（`a-nas` `99446cf5f15a`）：套接字 `active`，`systemd-analyze verify` 没有输出，Worker 没有网络、看不到数据卷，运行环境里没有 madmom 的模型文件。
- **未验证**：实验 NAS 上的第 5 步，以及 10 首对照歌在 NAS 的处理器上的结果、用时与峰值内存。

## 步骤

1. **在构建机上构建运行环境**。在检出根目录：

   ```bash
   scripts/build-videocut-music-runtime.sh
   ```

   - 预期：输出 `build/videocut-music-runtime.json`，`id` 与 `scripts/lib/content.sh` 的 `videocut_music_runtime_id` 相同。NATTEN 单文件编译时约占 2 GiB，`ANAS_NATTEN_JOBS` 默认 2。
   - 完成标准：`build/videocut-music-runtime/<id>.tar.gz` 存在。
   - 改名前（#669 之前）编过的 `montage-music-runtimes` 运行环境不能沿用：虚拟环境里写死了旧的安装路径 `/opt/a-nas/montage-music-runtimes/<id>`，`id` 也变了，要按这一步重编。

2. **备好权重**。从 Hugging Face 的固定版本下载两份权重（地址与版本见 [`allin1-harmonix-all.json`](../../deploy/models/allin1-harmonix-all.json)、[`htdemucs.json`](../../deploy/models/htdemucs.json)），按内容库的布局放好：`CONTENT/models/<清单的 sha256>/<文件>`，`CONTENT/videocut-music-runtimes/<id>.tar.gz`。
   - 完成标准：下一步的构建不报 `missing or not as pinned`（它按清单逐个核对大小与 SHA-256）。

3. **打包**：

   ```bash
   scripts/build-content-packages.sh --videocut-music build/content CONTENT
   ```

   ```bash
   scripts/build-packages.sh --videocut-music build/packages <a-nas 的版本> build
   ```

   - 预期：`a-nas-videocut-music-runtime-<id>`、9 个模型包（allin1 的 8 折各一个，htdemucs 一个）与 `a-nas-videocut-music_<版本>_all.deb`。
   - 完成标准：`dpkg-deb -I` 显示 `a-nas-videocut-music` 依赖 `a-nas (= <版本>)` 与上面 10 个包。

4. **暂存并安装**。把上一步的 11 个 `.deb` 用 `anas-dev` 复制到 NAS 的 `/var/tmp/videocut-music/`，再以 root：

   ```bash
   apt install /var/tmp/videocut-music/*.deb
   ```

   - 完成标准：`systemctl is-active anas-videocut-music.socket` 是 `active`，`a-nas-check` 没有 `FAIL`。
   - **装过改名前的包时**：`a-nas-videocut-music` 声明 `Conflicts`、`Replaces: a-nas-montage-music`，这条 `apt install` 会先卸掉 `a-nas-montage-music`（apt 列出 `REMOVED: a-nas-montage-music`，确认后继续）。旧的运行环境包是当时按文件名手动装的，不会随之卸掉，装好新包后以 root 删掉它和旧的缓存：

     ```bash
     dpkg-query -W -f '${Package} ${Status}\n' 'a-nas-montage-music*'
     apt remove $(dpkg-query -W -f '${Package}\n' 'a-nas-montage-music-runtime-*')
     rm -rf /var/cache/a-nas-montage-music
     ```

     - 预期：第一条只列出 `a-nas-montage-music-runtime-<旧 id>`（`a-nas-montage-music` 已是 `deinstall` 或不在列表里）；第二条只删这一个包，不涉及 `a-nas-model-*`（权重包按内容命名，新旧包共用）；缓存在系统盘上，只是 matplotlib 与 numba 的编译缓存。
     - 完成标准：`dpkg-query -W 'a-nas-montage-music*'` 不再列出已安装的包，`/opt/a-nas/montage-music-runtimes` 不存在，`systemctl is-active anas-videocut-music.socket` 仍是 `active`。

5. **实测**。测试账号的“音乐”文件夹里放一首 4 分钟左右的测试歌（开发机 C 的 `~/Music/QQ音乐`，只放在实验 NAS 上），在剪辑中心建项目、选这首歌。以 root 记下用时与峰值内存：

   ```bash
   journalctl -u anas-videocut-music -o cat | grep 'separated in'
   ```

   ```bash
   systemctl show anas-videocut-music -p MemoryPeak
   ```

   - 完成标准：配乐页显示选段；把用时、各步用时与 `MemoryPeak` 记进设计文档的[用时与内存](../architecture/videocut-music.md#用时与内存)。

## 验证

- 单元通过校验，没有任何输出：

  ```bash
  systemd-analyze verify --man=no /usr/lib/systemd/system/anas-videocut-music.socket /usr/lib/systemd/system/anas-videocut-music.service
  ```

- 剪辑服务的接口：`GET /api/v1/videocut/projects/<id>/song` 的 `state` 是 `ready`，`selection` 有选段与小节槽。
- Worker 没有网络、看不到数据卷：`systemctl show anas-videocut-music -p PrivateNetwork -p InaccessiblePaths` 为 `yes` 与 `-/srv/a-nas`。
- 运行环境里没有 madmom 的模型文件：

  ```bash
  find /opt/a-nas/videocut-music-runtimes -path '*/madmom/models/*' -type f ! -name '__init__.py*' ! -path '*/__pycache__/*'
  ```

  没有输出。

## 回滚或恢复

- `apt remove a-nas-videocut-music`：停掉 Worker 与套接字。剪辑服务里已分析好的歌照常可用，新选的歌停在 `analyzing`（等待 `analyzer_unavailable`）。
- 内容包随后由 `apt autoremove` 删除。

## 关联

- 设计：[剪辑中心的配乐](../architecture/videocut-music.md)
- 规格：[剪辑中心规格的“配乐”](../specs/videocut-center.md#配乐)
- ADR：[0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)（运行环境与模型的交付做法）、[0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)
- 需求：[#559](https://github.com/zhongwater123/A-NAS/issues/559)
