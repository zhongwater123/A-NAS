# 在实验 NAS 上演示剪辑中心

## 目的

把剪辑中心整条栈（#514 的 M1–M10，分支 `feat/montage-web`）装到 Experimental NAS，只在这台设备上打开剪辑中心，演示一遍：上传视频 → 选歌 → 写要求 → 后台由成员的 VNAS 规划 → 成片存进相册。

这是演示，不是发布：
- 剪辑中心的产品默认值仍是关闭，只在这台设备上用 drop-in 打开；
- allin1 的商业版权还没有结论，配乐分析只按[启用配乐分析](enable-montage-music.md)手动装在实验 NAS 上。

成功结果：成员在剪辑中心点“开始”后关掉浏览器，任务走完，成片出现在成员的个人图库，可以播放。

## 前提与风险

- **权限**：
  - 开发机 B 能运行 `make release`，有存档密钥的口令（[部署 A-NAS 软件包](deploy-a-nas-packages.md)）；
  - 能以 `anas-dev` 登录 NAS，并有 NAS 上的 root。
- **架构**：实验 NAS 是 x86_64，配乐分析的运行环境与 NATTEN、madmom 的轮子要在 x86_64 机器上编（开发机 B 的 WSL）。开发机 C（macOS arm64）编不了，也不签名。
- **VNAS**：设备上已经填好 DeepSeek 密钥（设置 → VNAS），演示账号有 VNAS。没有密钥时，任务会停在“等待规划”，页面写明原因。
- **相册分析**：卡片描述用相册 OCR 已部署的 Qwen3.5-4B，分析视频时与相册的其他 AI 工作排队。
- **服务影响**：升级会重启 Host Agent、产品服务与相册等服务，见[部署 A-NAS 软件包](deploy-a-nas-packages.md)的“前提与风险”。
- **内存**：配乐分析一首 8 分钟的歌峰值约 4.5 GiB，单元上限 5632 MiB（[配乐分析的用时与内存](../architecture/montage-music.md#用时与内存)）；分析时设备上别的大内存工作会变慢。
- **停止条件**：
  - `a-nas-check` 有 `FAIL`：按[部署 A-NAS 软件包](deploy-a-nas-packages.md)的“回滚或恢复”退回；
  - `systemctl is-active anas-montage` 不是 `active`；
  - 配乐分析在 `journalctl -u anas-montage-music` 里报内存不足：换一首更短的歌。

## 步骤

1. **开发机 B：取到要演示的提交，跑门禁**。
   ```bash
   git fetch origin feat/montage-web
   git switch --detach origin/feat/montage-web
   git status --short   # 必须为空
   make check
   ```
   - 完成标准：`make check` 通过（`src/companion/Companion.test.tsx` 的偶发超时可以单独重跑确认）。记下 `git rev-parse --short HEAD`。
2. **开发机 B：构建配乐分析的运行环境与权重**，按[启用配乐分析](enable-montage-music.md)的第 1–3 步，得到 11 个 `.deb`。
3. **开发机 B：发布并推送**，按[部署 A-NAS 软件包](deploy-a-nas-packages.md)的第 1、2 步：
   ```bash
   make release REPO=/mnt/d/A-NAS-releases/apt CONTENT=/mnt/d/A-NAS-releases/content
   ```
   用 `scripts/deploy-dev.ps1 -NasHost a-nas-dev` 推到 NAS 的暂存目录。
   - 完成标准：拿到第 1 步提交对应的版本号、签名指纹与 keyring 的 SHA-256。
4. **NAS：安装这一版**。管理员在“设置 → 系统更新”里点“检查更新”，再点“安装”；或按[部署手册](deploy-a-nas-packages.md)第 6 步用 apt。
   - 完成标准：`a-nas-check` 全部 `PASS`；`systemctl is-active anas-montage anas-montage-assistant.socket anas-montage-compute.socket` 都是 `active`。
5. **NAS：装配乐分析**，按[启用配乐分析](enable-montage-music.md)第 4 步，把第 2 步的 `.deb` 装上。
   - 完成标准：`systemctl is-active anas-montage-music.socket` 是 `active`。
6. **NAS（root）：只在这台设备上打开剪辑中心**。
   ```bash
   install -d -m 0755 /etc/systemd/system/anas-api.service.d
   printf '[Service]\nEnvironment=ANAS_MONTAGE_ENABLED=1\n' > /etc/systemd/system/anas-api.service.d/montage-demo.conf
   systemctl daemon-reload
   systemctl restart anas-api
   ```
   - 完成标准：以成员登录，桌面上有“剪辑中心”。
7. **准备演示素材**：
   - 演示成员的“音乐”文件夹里放好《Symphony》；
   - 本机有品杰一组的 11 段视频，用来在剪辑中心里上传；
   - **主页背景（可选）**：把原型的背景片放到这台设备上，主页就播它，和原型一样；不放时主页轮流播成员自己的三段视频。片子是用户的私人素材，仓库是公开的，所以它只放在设备上，不进仓库也不进软件包。从开发机 C（Mac）拷到 NAS 后，以 root：
     ```bash
     install -d -o a-nas-montage -g a-nas-montage -m 0750 /srv/a-nas/data/montage/hero
     install -o a-nas-montage -g a-nas-montage -m 0640 hero.mp4 hero.jpg /srv/a-nas/data/montage/hero/
     ```
     源文件在开发机 C 的 `~/A-NAS-datasets/剪辑对比/剪辑原型（2026-11-11）/`。完成标准：剪辑中心主页的背景是雪峰、直升机、日照金山三个镜头。
8. **演示**：
   1. 成员打开剪辑中心 → 新建项目 → 上传视频，选 11 段，上传完点“新建项目”；
   2. 在项目页选歌《Symphony》，等分析完成，试听选段；
   3. 写一句要求，点“开始”，然后关掉浏览器；
   4. 过一会儿重新登录：项目页的进度从服务端恢复，桌面提示“正在由 VNAS 规划”或“成片已存进相册”；
   5. 在“成片”里播放、下载，在相册的个人图库里也能找到“<项目名> v1.mp4”。
   - 每一步以 root 记下用时与峰值内存，写进 #561：
     ```bash
     journalctl -u anas-montage -u anas-photos -u anas-montage-compute -u anas-montage-music --since today
     systemctl show -p MemoryPeak anas-montage-music anas-montage-compute anas-ai
     ```

## 验证

- **任务走完**：剪辑中心项目页的“生成”显示“成片已存进相册”，“成片”里有第 1 版。
- **成片规格**：`ffprobe` 看成片为 1920×1080、30 帧、有音轨，时长与选段相差不超过 0.1 秒。
- **规划在后台发起**：`journalctl -u 'anas-assistant-cron@<账号>'` 有 `montage round: round … handed to Hermes`，之后有 `ended: ok`。
- **规划用后已清理**：成员 VNAS 的“定时任务”里不再有“剪辑规划”。

## 回滚或恢复

- **关掉剪辑中心**：删掉 drop-in 并重启产品服务。
  ```bash
  rm /etc/systemd/system/anas-api.service.d/montage-demo.conf
  systemctl daemon-reload && systemctl restart anas-api
  ```
  项目与成片都留着。成片在相册里，与剪辑中心无关。
- **卸掉配乐分析**：按[启用配乐分析](enable-montage-music.md)的“回滚或恢复”。
- **退回上一版本**：“设置 → 系统更新 → 回到上一版本”，或按[部署 A-NAS 软件包](deploy-a-nas-packages.md)的“回滚或恢复”。

## 关联

- 剪辑中心的规格与实施方案（`docs/specs/montage-center.md`、`docs/architecture/montage-center.md`，随 #518 合入）
- [部署 A-NAS 软件包](deploy-a-nas-packages.md)、[启用配乐分析](enable-montage-music.md)、[启用 AI 助手](enable-ai-assistant.md)
- 代码：`internal/montage`、`web/src/montage`、`ai/assistant/montage_round.py`
