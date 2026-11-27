# 在实验 NAS 上演示剪辑中心

## 目的

把剪辑中心整条栈（#514 的 M1–M10）装到 Experimental NAS，只在这台设备上打开剪辑中心，演示一遍：上传视频 → 选歌 → 写要求 → 后台由成员的 VNAS 规划两个方案 → 两条成片存进相册。

这是演示，不是发布：
- 剪辑中心的产品默认值仍是关闭，只在这台设备上用 drop-in 打开；
- allin1 的商业版权还没有结论，配乐分析只按[启用配乐分析](enable-videocut-music.md)手动装在实验 NAS 上。

成功结果：成员在剪辑中心点“开始剪辑”后关掉浏览器，任务走完，方案一、方案二两条成片出现在成员的个人图库，可以播放。

## 前提与风险

- **权限**：
  - 开发机 B 能运行 `make release`，有存档密钥的口令（[部署 A-NAS 软件包](deploy-a-nas-packages.md)）；
  - 能以 `anas-dev` 登录 NAS，并有 NAS 上的 root。
- **架构**：实验 NAS 是 x86_64，配乐分析的运行环境与 NATTEN、madmom 的轮子要在 x86_64 机器上编（开发机 B 的 WSL）。开发机 C（macOS arm64）编不了，也不签名。
- **VNAS**：设备上已经填好 DeepSeek 密钥（设置 → VNAS），演示账号有 VNAS。没有密钥时，任务会停在“等待规划”，页面写明原因。规划是成员规划进程里的一次对话（[规划会话](../architecture/videocut-planning-session.md)）：有密钥时 `anas-videocut-planner` 在运行，`a-nas-check` 核对它。
- **相册分析**：卡片描述用相册 OCR 已部署的 Qwen3.5-4B，分析视频时与相册的其他 AI 工作排队。
- **服务影响**：升级会重启 Host Agent、产品服务与相册等服务，见[部署 A-NAS 软件包](deploy-a-nas-packages.md)的“前提与风险”。
- **内存**：配乐分析一首 8 分钟的歌峰值约 4.5 GiB，单元上限 5632 MiB（[配乐分析的用时与内存](../architecture/videocut-music.md#用时与内存)）；分析时设备上别的大内存工作会变慢。
- **停止条件**：
  - `a-nas-check` 有 `FAIL`：按[部署 A-NAS 软件包](deploy-a-nas-packages.md)的“回滚或恢复”退回；
  - `systemctl is-active anas-videocut` 不是 `active`；
  - 配乐分析在 `journalctl -u anas-videocut-music` 里报内存不足：换一首更短的歌。

## 步骤

1. **开发机 B：取到要演示的提交，跑门禁**。
   ```bash
   git fetch origin main
   git switch --detach origin/main
   git status --short   # 必须为空
   make check
   ```
   - 完成标准：`make check` 通过。记下 `git rev-parse --short HEAD`。
2. **开发机 B：构建配乐分析的运行环境与权重**，按[启用配乐分析](enable-videocut-music.md)的第 1–3 步，得到 11 个 `.deb`。
3. **开发机 B：发布并推送**，按[部署 A-NAS 软件包](deploy-a-nas-packages.md)的第 1、2 步：
   ```bash
   make release REPO=/mnt/d/A-NAS-releases/apt CONTENT=/mnt/d/A-NAS-releases/content
   ```
   用 `scripts/deploy-dev.ps1 -NasHost a-nas-dev` 推到 NAS 的暂存目录。
   - 完成标准：拿到第 1 步提交对应的版本号、签名指纹与 keyring 的 SHA-256。
4. **NAS：安装这一版**。管理员在“设置 → 系统更新”里点“检查更新”，再点“安装”；或按[部署手册](deploy-a-nas-packages.md)第 6 步用 apt。
   - 完成标准：`a-nas-check` 全部 `PASS`；`systemctl is-active anas-videocut anas-videocut-assistant.socket anas-videocut-compute.socket` 都是 `active`。
5. **NAS：装配乐分析**，按[启用配乐分析](enable-videocut-music.md)第 4 步，把第 2 步的 `.deb` 装上。
   - 完成标准：`systemctl is-active anas-videocut-music.socket` 是 `active`。
6. **NAS（root）：只在这台设备上打开剪辑中心**。
   ```bash
   install -d -m 0755 /etc/systemd/system/anas-api.service.d
   printf '[Service]\nEnvironment=ANAS_VIDEOCUT_ENABLED=1\n' > /etc/systemd/system/anas-api.service.d/videocut-demo.conf
   systemctl daemon-reload
   systemctl restart anas-api
   ```
   - 完成标准：以成员登录，桌面上有“剪辑中心”。
   - 这台设备装过改名前（#669 之前）的版本时，先做下面“从改名前的版本升级”的第 1 步，再接着往下。
7. **准备演示素材**：
   - 演示成员的“音乐”文件夹里放好《Symphony》；
   - 本机有品杰一组的 11 段视频，用来在剪辑中心里上传；
   - **主页背景（可选）**：把原型的背景片放到这台设备上，主页就播它，和原型一样；不放时主页没有背景片。
     - **谁能看到**：这个文件是设备级的，`GET /api/v1/videocut/hero` 把它给这台设备上每一位登录的成员，不只是放它的人。片子是用户的私人素材：只在这台设备的成员都可以看它时才放，演示完按“回滚或恢复”删掉。
     - 仓库是公开的，所以它只放在设备上，不进仓库也不进软件包。从开发机 C（Mac）拷到 NAS 后，以 root：
     ```bash
     install -d -o a-nas-videocut -g a-nas-videocut -m 0750 /srv/a-nas/data/videocut/hero
     install -o a-nas-videocut -g a-nas-videocut -m 0640 hero.mp4 hero.jpg /srv/a-nas/data/videocut/hero/
     ```
     源文件在开发机 C 的 `~/A-NAS-datasets/剪辑对比/剪辑原型（2026-11-11）/`。完成标准：剪辑中心主页的背景是雪峰、直升机、日照金山三个镜头。
8. **演示**：
   1. 成员打开剪辑中心 → 新建项目 → 上传视频，选 11 段，上传完点“新建项目”；
   2. 在项目页选歌《Symphony》，等分析完成，试听选段；
   3. 写一句要求，点“开始剪辑”，然后关掉浏览器；
   4. 过一会儿重新登录：项目页的进度从服务端恢复，13 个步骤里方案一的 5 步在前、方案二的 5 步在后；桌面提示“正在由 VNAS 规划”，两个方案都存完后提示“成片已存进相册”；
   5. 方案一存完、方案二还在规划或渲染时，“成片”里已经能播放方案一；
   6. 两个方案都存完后，“成片”里并排显示方案一、方案二，各自播放、下载；在相册的个人图库里能找到“<项目名> v1.mp4”与“<项目名> v2.mp4”。
   - 一轮规划交回了方案一、没有交回方案二时，方案一照常存进相册，项目页显示“VNAS 没有交回方案二的规划，重新规划？”。点“重新规划”后，新的一轮只规划方案二；记下出现过这种情况。
   - 等待规划时，项目页写明在等什么：“VNAS 正在规划”，或“VNAS 暂时连不上，N 分钟后再试”“规划出错，1 分钟后再试一次”。规划连续出错两次，任务停下并显示“规划出错：<原文>”，点“重新规划”再来；记下原文。
   - 每一步以 root 记下用时与峰值内存，写进 #561。两个方案各渲染一次，分开记：任务接口 `GET /api/v1/videocut/jobs/<任务 ID>` 的 `steps` 里有每一步的 `startedAt` 与 `finishedAt`，方案一是 `pick` 到 `save`，方案二是 `pick-2` 到 `save-2`。
     ```bash
     journalctl -u anas-videocut -u anas-photos -u anas-videocut-compute -u anas-videocut-music --since today
     systemctl show -p MemoryPeak anas-videocut-music anas-videocut-compute anas-ai 'anas-assistant-plan@<账号>'
     ```
   - 规划这一段的日志，以 root：
     ```bash
     journalctl -u anas-videocut-planner --since today            # 领取、交给 VNAS、结束或没开始的原因
     journalctl -u 'anas-assistant-plan@<账号>' --since today     # 成员的规划进程（hermes serve，端口 9120）
     journalctl -u anas-videocut --since today | grep -E 'planning (round claimed|step taken)'
     ```

## 从改名前的版本升级

#669 把剪辑中心的运行时名字从 montage 改成了 videocut（[规格 #639](https://github.com/zhongwater123/A-NAS/issues/639) 的“运行时改名”“不写升级迁移”）。新版不迁移旧数据：服务账号换成 `a-nas-videocut`（UID 31005），数据目录换成数据卷上的 `videocut/`，剪辑中心从空开始；旧的 `montage/` 目录、账号 `a-nas-montage`（UID 31003）留在设备上，新版不读、不碰。装过旧版的只有实验 NAS，上面是可丢弃的测试数据（2026-11-26 定）。

升级本身按上面第 1–5 步做；第 5 步装配乐分析时按[启用配乐分析](enable-videocut-music.md)第 4 步，旧包 `a-nas-montage-music` 由 apt 自动卸掉。升级完成后以 root 依次做：

1. **换演示开关**。旧的 drop-in 设的是 `ANAS_MONTAGE_ENABLED`，新版不认，升级后桌面上的剪辑中心会消失：

   ```bash
   cat /etc/systemd/system/anas-api.service.d/montage-demo.conf
   rm /etc/systemd/system/anas-api.service.d/montage-demo.conf
   printf '[Service]\nEnvironment=ANAS_VIDEOCUT_ENABLED=1\n' > /etc/systemd/system/anas-api.service.d/videocut-demo.conf
   systemctl daemon-reload
   systemctl restart anas-api
   systemctl show -p Environment anas-api | tr ' ' '\n' | grep VIDEOCUT
   ```

   - 预期：第一条显示的正是 `Environment=ANAS_MONTAGE_ENABLED=1`（不是时先停下，看清这个文件还设了什么）；最后一条输出 `ANAS_VIDEOCUT_ENABLED=1`。
   - 完成标准：以成员登录，桌面上有“剪辑中心”，项目列表为空。
   - 回滚：删掉 `videocut-demo.conf`、写回原来的 `montage-demo.conf`，`daemon-reload` 后重启 `anas-api`；只在退回改名前的版本时才需要。
2. **核对相册里的旧授权**。旧项目在相册里建的素材授权（相册库的 `montage_grants`）不随剪辑数据消失。新版第一次连上相册时对账：相册有、剪辑中心没有的授权都撤销（`internal/videocut/app/projects.go` 的 `reconcile`），撤销后授权放开素材，分析结果一周后由相册清掉（`PruneMontageAnalysis`），所以旧授权既不挡成员删照片，也不长期占空间。核对撤销确实做了：

   ```bash
   journalctl -u anas-videocut --since today -o cat | grep -c 'revoking a photo grant no project holds'
   cat > /tmp/photo-grants.py <<'PY'
   import json, socket, struct, sys
   request = {"op": "list"} if len(sys.argv) == 1 else {"op": "revoke", "grantId": sys.argv[1]}
   conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
   conn.connect("/run/a-nas-photos-grants/grants.sock")
   body = json.dumps(request).encode()
   conn.sendall(struct.pack(">I", len(body)) + body)
   length = struct.unpack(">I", conn.recv(4))[0]
   answer = b""
   while len(answer) < length:
       answer += conn.recv(length - len(answer))
   print(json.loads(answer))
   PY
   setpriv --reuid=a-nas-videocut --regid=a-nas-videocut --clear-groups python3 /tmp/photo-grants.py
   ```

   - 相册的授权套接字只回答剪辑服务的账号（UID 31005），所以要以 `a-nas-videocut` 的身份连；以别的身份连会被拒绝。
   - 预期：第一条的数字等于旧项目的个数（相册当时不可用时为 0，剪辑服务每分钟维护一次，会再对账）；`list` 回答 `"ok": True`，`grants` 里只有升级后新建项目的授权（升级后还没建项目时为空）。
   - 仍有旧授权时（`projectId` 不是新版里的任何项目）：等几分钟再看一次；还在的话按 ID 逐条撤销，撤销可以重复，不影响其他授权：

     ```bash
     setpriv --reuid=a-nas-videocut --regid=a-nas-videocut --clear-groups python3 /tmp/photo-grants.py <授权 ID>
     ```

   - 完成标准：`list` 里没有旧项目的授权。撤销后不能恢复，但它只放开相册对这些素材的引用，不删素材，也不动已存进相册的成片。做完 `rm /tmp/photo-grants.py`。
3. **（演示需要时）放回主页背景片**。背景片原来在 `/srv/a-nas/data/montage/hero/`，新版只看 `videocut/hero/`：按第 7 步，把 `/srv/a-nas/data/montage/hero/hero.mp4` 与 `hero.jpg` 用 `install` 放进 `videocut/hero/`。

### 清理旧东西（可选）

旧的账号、目录与缓存不影响运行，留着也可以。确定不再退回改名前的版本后再删：退回旧版时，旧版要用的正是 `montage/` 目录与 `a-nas-montage` 账号。下面每一项都是删除，以 root 做。

1. **数据卷上的旧目录 `/srv/a-nas/data/montage/`**（真实磁盘写操作）：
   - **目标识别**：

     ```bash
     findmnt -no SOURCE,FSTYPE,OPTIONS /srv/a-nas/data
     btrfs subvolume show /srv/a-nas/data/montage
     stat -c '%n %u %a' /srv/a-nas/data/montage /srv/a-nas/data/videocut
     ls -la /srv/a-nas/data/montage
     du -sh /srv/a-nas/data/montage
     ```

     预期：数据卷是这台设备的数据盘（btrfs，可写）；`montage` 是这个卷上的子卷（Host Agent 当年建的），属主 UID 31003、权限 700，里面只有 `montage.db`（及 `-wal`、`-shm`）、`songs/`、`hero/`、`tmp/`；`videocut` 属主 UID 31005。和预期不符时停下，不要删。
   - **确认**：`systemctl is-active anas-videocut` 是 `active`，桌面上的剪辑中心能用；`pgrep -u 31003` 没有输出（没有进程还在用旧账号）。主页背景片要保留的话，第 3 步已经放进 `videocut/hero/`。
   - **先改名，观察后再删**：

     ```bash
     mv /srv/a-nas/data/montage /srv/a-nas/data/montage.removed-$(date +%Y%m%d)
     ```

     用一两天剪辑中心与相册都正常，再删这个子卷：

     ```bash
     btrfs subvolume delete /srv/a-nas/data/montage.removed-<日期>
     ```

   - **回滚**：改名之后、删除之前，`mv` 回 `/srv/a-nas/data/montage` 即可；删除之后不能恢复（这些是可丢弃的测试数据，2026-11-26 定）。
   - **验证**：`ls /srv/a-nas/data` 里没有 `montage` 与 `montage.removed-*`；`stat -c '%U %a' /srv/a-nas/data/videocut` 仍是 `a-nas-videocut 700`；`a-nas-check` 没有 `FAIL`。
2. **旧账号 `a-nas-montage`**：在第 1 项之后删，免得留下属主不明的文件。

   ```bash
   getent passwd a-nas-montage
   find /srv/a-nas/data /var /etc -xdev -uid 31003 2>/dev/null | head
   userdel a-nas-montage
   getent group a-nas-montage && groupdel a-nas-montage
   ```

   - 预期：第一条是 `a-nas-montage:x:31003:31003:A-NAS montage service:/nonexistent:/usr/sbin/nologin`；第二条没有输出（有输出时先看清是什么，不要删账号）。
   - 回滚：只在退回改名前的版本时需要；旧版 `a-nas` 的 sysusers 配置会用同一个 UID 31003 重新建出它。
   - 验证：`getent passwd a-nas-montage`、`getent group a-nas-montage` 都没有输出，`id -u a-nas-videocut` 是 31005。
3. **系统盘上的旧状态**：

   ```bash
   ls -la /var/lib/anas-montage-planner /var/cache/a-nas-montage-music 2>&1
   rm -rf /var/lib/anas-montage-planner /var/cache/a-nas-montage-music
   ```

   - 确认：先看 `systemctl is-active anas-montage-planner`，预期不是 `active`（新版 `a-nas` 的 preinst 在升级时停掉了旧的规划发起方）。是 `active` 时先 `systemctl stop anas-montage-planner` 再删，否则它会重新写出 `state.json`。
   - `/var/lib/anas-montage-planner/state.json` 是旧规划发起方给每位成员计的轮数；新的在 `/var/lib/anas-videocut-planner/`。`/var/cache/a-nas-montage-music` 是旧配乐 Worker 的编译缓存（[启用配乐分析](enable-videocut-music.md)第 4 步也删它）。
   - 运行目录 `/run/a-nas-montage*`、`/run/anas-assistant-montage` 在内存里，重启后自然消失，不用删。
   - 验证：两个目录都不在了，`systemctl is-active anas-videocut-planner` 在有模型密钥时仍是 `active`。

## 验证

- **任务走完**：剪辑中心项目页不再显示进度，“成片”里并排有方案一（第 1 版）与方案二（第 2 版），相册的个人图库里有 v1 与 v2 两个文件。
- **成片规格**：`ffprobe` 看两条成片都是 1920×1080、30 帧、有音轨，时长与选段相差不超过 0.1 秒。
- **规划在后台发起**：`journalctl -u anas-videocut-planner` 有 `<账号>: round … handed to VNAS as conversation …`，之后有 `ended: complete` 与 `the background review … reported`。一轮规划两个方案：没有点过“重新规划”时只有一轮。
- **规划留在成员的 VNAS 历史里**：成员打开 VNAS，对话列表里有“剪辑规划 · <项目名>”，能接着说“开场太快了”。
- **VNAS 记下了做法**：成员 VNAS 的“记忆”页或技能里有这次规划后复盘写下的内容（复盘没有可记的时可能没有）。
- **规划进程用后退出**：规划结束约一分钟后 `systemctl is-active 'anas-assistant-plan@<账号>'` 不是 `active`。

## 回滚或恢复

- **关掉剪辑中心**：删掉 drop-in 并重启产品服务。
  ```bash
  rm /etc/systemd/system/anas-api.service.d/videocut-demo.conf
  systemctl daemon-reload && systemctl restart anas-api
  ```
  项目与成片都留着。成片在相册里，与剪辑中心无关。
- **删掉主页背景片**（放过的话）：`rm -r /srv/a-nas/data/videocut/hero`。主页不再有背景片。
- **卸掉配乐分析**：按[启用配乐分析](enable-videocut-music.md)的“回滚或恢复”。
- **退回上一版本**：“设置 → 系统更新 → 回到上一版本”，或按[部署 A-NAS 软件包](deploy-a-nas-packages.md)的“回滚或恢复”。

## 关联

- 剪辑中心的[规格](../specs/videocut-center.md)、[实施方案](../architecture/videocut-center.md)与[规划会话](../architecture/videocut-planning-session.md)
- [部署 A-NAS 软件包](deploy-a-nas-packages.md)、[启用配乐分析](enable-videocut-music.md)、[启用 AI 助手](enable-ai-assistant.md)
- 代码：`internal/videocut/app`、`web/src/videocut`、`ai/assistant/videocut-planner.py`、`scripts/lib/assistant-units.sh`
