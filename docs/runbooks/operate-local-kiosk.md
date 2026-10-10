# 运行实验 NAS 本地控制台

状态：implemented, display and pointer verified; confinement and VT recovery pending
更新时间：2026-10-10

## 目的

让带直连屏幕、键盘和鼠标的 Experimental NAS 在启动后进入 A-NAS Web 桌面。Cage 只承载一个最大化 Chromium，浏览器只访问回环地址 `http://127.0.0.1:8080/?local-console=1`；查询标记只启用本地显示行为，产品服务和 Host Agent 的权限边界不变。

## 安全边界

- Kiosk 以 `anas-dev` 运行，不以 root 运行 Chromium，也不使用 `--no-sandbox`。
- Cage 通过 PAM 向 `systemd-logind` 注册真实本地会话，由 logind 授予活动 seat 的显示和输入设备访问；不要预先把 `anas-dev` 加入 `input` 或 `video` 组。
- Cage 已使用 `-s` 请求允许 VT 切换，但 Experimental NAS 的实体切换尚未成功，当前恢复路径仍是 SSH；Kiosk 不是登录界面，也不提供终端。
- 页面仍来自只监听 `127.0.0.1:8080` 的产品服务；直连屏幕不会扩大网络暴露面。
- 如果机器已经存在其他 display manager，停止配置并先决定唯一图形会话所有者。

## 前提

按[实机配置手册的安装步骤](provision-v1.0.1-experimental-storage.md#安装系统服务)暂存并安装一个 release，并把 `~anas-dev/apps/a-nas/current` 切到该 release，使以下版本化文件出现在其中：

- `kiosk-launcher`
- `anas-kiosk@.service`
- `a-nas-kiosk.pam`
- `kiosk.env`（Experimental NAS 已实机校准的输出配置）

屏保视频不在 release 中，见[屏保视频](#屏保视频)。

2026-10-06 已在实机确认这些图形包和 Chromium 154 安装完成。重装或新设备由管理员执行：

```bash
apt update
apt install --no-install-recommends \
  cage \
  chromium \
  chromium-sandbox \
  chromium-l10n \
  fonts-noto-cjk \
  wlr-randr \
  libinput-tools

dpkg-query -W cage chromium chromium-sandbox chromium-l10n fonts-noto-cjk wlr-randr libinput-tools
systemctl status display-manager.service --no-pager || true
```

## 一次性安装

以下命令必须从 SSH 管理会话执行；不要在即将被接管的 `tty1` 本地 shell 中启动服务。

```bash
install -m 0644 \
  /home/anas-dev/apps/a-nas/current/a-nas-kiosk.pam \
  /etc/pam.d/a-nas-kiosk
install -m 0644 \
  /home/anas-dev/apps/a-nas/current/anas-kiosk@.service \
  /etc/systemd/system/anas-kiosk@.service
install -d -m 0755 /etc/a-nas
install -m 0644 \
  /home/anas-dev/apps/a-nas/current/kiosk.env \
  /etc/a-nas/kiosk.env

systemctl daemon-reload
systemctl set-default graphical.target
systemctl enable anas-kiosk@tty1.service
```

该单元只通过 `graphical.target.wants` 启动，不声明 `display-manager.service` 别名。Debian 13 的 systemd 会拒绝把显式模板实例别名为非模板单元，而该别名并不是 Kiosk 随 `graphical.target` 启动的必要条件。

系统级单元中的启动器路径固定为 `/home/anas-dev/apps/a-nas/current/kiosk-launcher`，不使用 `%h`；系统管理器会把 `%h` 解析为 `/root`，即使服务配置了 `User=anas-dev`。

本机屏幕已在 `DP-2` 的原生 `1536x2048@60.6Hz` 模式下实机校准为逆时针 `90` 度、缩放 `1.5`。这些硬件相关值位于 root 管理的 `/etc/a-nas/kiosk.env`；启动器会先校验配置，再在 Chromium 启动前原子应用 transform 和 scale。其他硬件不得直接复用 connector 名称。

保持 SSH 恢复会话后，首次验证可执行：

```bash
systemctl start anas-kiosk@tty1.service
systemctl status anas-kiosk@tty1.service --no-pager
journalctl -u anas-kiosk@tty1.service -b --no-pager -n 100
```

服务与 `getty@tty1` 冲突并接管该虚拟终端。启动器最多等待产品 `/healthz` 30 秒；产品尚未就绪时退出，由 systemd 自动重试，而不是把 Chromium 错误页留在屏幕上。

## 屏保视频

屏保视频是系统盘上的长期媒体，不随 release 发布，升级和产品回滚都不改变正在播放的池（[ADR 0014](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)）：

| 路径 | 内容 |
| --- | --- |
| `/var/lib/a-nas/screensavers/objects/<SHA-256>.mp4` | 每个视频只存一份，`root:a-nas 0640` |
| `/var/lib/a-nas/screensavers/pools/<池 ID>/` | 指向对象的 `screensaver-NNN.mp4` 硬链接与 `SHA256SUMS`，目录 `root:a-nas 0750` |
| `/var/lib/a-nas/screensavers/current` | 指向当前池的链接；`/etc/a-nas/anas-api.env` 固定把它交给产品服务只读扫描 |
| `/home/anas-dev/apps/a-nas/screensavers/` | 暂存区：按哈希保存的视频与各池清单 `pools/<池 ID>.sums` |

Chromium 只取得匿名同源 URL，不直接访问宿主机路径或原文件名。池缺失不会阻止产品服务或 Kiosk 启动。

### 更换屏保视频

1. 在开发机暂存新池。脚本只上传 NAS 暂存区还没有的视频；早先随 release 暂存过的视频会直接复用。`-WhatIf` 只做本地校验并列出哈希，不联网：

   ```powershell
   .\scripts\deploy-screensavers.ps1 `
     -NasHost <NAS_HOST> `
     -Video @(
       "E:\SteamLibrary\steamapps\workshop\content\431960\3667411885\BMW M5.mp4",
       "E:\SteamLibrary\steamapps\workshop\content\431960\3556095996\Penguins.mp4",
       "E:\SteamLibrary\steamapps\workshop\content\431960\3666233105\1771036359365.mp4",
       "E:\SteamLibrary\steamapps\workshop\content\431960\3679705103\妄想天使直播.mp4",
       "E:\SteamLibrary\steamapps\workshop\content\431960\3743343692\ЭКСПОНАТ - MIA BOYKA (TikTok Homelander Edit) HARDSTYLE REMIX by MilWo - MilWo (1080p, h264) (1).mp4"
     )
   ```

   单个池最多 32 条，重复内容、非 MP4 或缺失文件会在联网前拒绝。脚本输出每个视频的 SHA-256 与池 ID。不得把 Steam Workshop 目录直接授予产品服务读取权限。
2. 以 root 核对输出的池 ID 后安装：

   ```bash
   /opt/a-nas/current/install-screensavers.sh <池 ID>
   ```

   脚本把暂存的视频复制进对象库并逐个复核 SHA-256，任一不符即在改动前停止。池 ID 由清单内容计算，暂存清单与命令中的 ID 不符时拒绝。全部通过后建池、原子切换 `current`，并重启 Kiosk 让页面重新读取清单；产品服务无需重启。再次安装同一个池不会产生变化。
3. 按验收第 7 项复核清单数量和 Range 请求。

### 首次升级

[issue #49](https://github.com/zhongwater123/A-NAS/issues/49) 之前的安装器把每个池放在 `/var/lib/a-nas/screensavers/<release>/`，没有 `current`。首次安装包含 ADR 0014 的 release 时，系统安装器把其中最近安装的池以硬链接导入对象库并设为当前池，既不复制也不需要重新上传；导入失败时只打印警告，升级照常完成，本地控制台显示壁纸。

此后若又用不含 ADR 0014 的旧安装器装过带视频的 release，新视频只在 `/var/lib/a-nas/screensavers/<release>/` 中，不会播放。以 root 导入：`/opt/a-nas/current/install-screensavers.sh --import /var/lib/a-nas/screensavers/<release>`。

导入后，按 release 存放的旧池都是多余副本。清理前先确认当前池可用，并列出候选目录：

```bash
pools=/var/lib/a-nas/screensavers
readlink "$pools/current"                              # 必须是 pools/<池 ID>
(cd "$pools/current" && sha256sum --check --quiet SHA256SUMS)
find "$pools" -mindepth 1 -maxdepth 1 -type d ! -name objects ! -name pools
```

确认列表中只有旧 release 目录、且清单与 Range 请求都正常后，才逐个 `rm -r -- "$pools/<release>"`。被导入的池与对象共享数据，删除它的目录不影响播放；其他目录是独立副本，删除后释放空间。删除前逐个核对目录中每个视频的 SHA-256 都在 `current/SHA256SUMS` 中。不要删除 `objects`、`pools` 或 `current`。

单视频时期的安装可能在 `$pools` 根部留下独立的 `.mp4`（Experimental NAS 为 `computer-chip.mp4`），导入不会处理它。其 SHA-256 在 `current/SHA256SUMS` 中时是副本，可以删除；不在时是独有视频。要保留它，以 root 把它复制到暂存账号目录，下载到开发机后用 `scripts/deploy-screensavers.ps1` 与当前池的视频一起暂存为新池，安装新池并用 `cmp` 确认新池中的对应文件与原文件相同后再删除原文件。2026-10-10 Experimental NAS 按此加入后，当前池为六条。

## 验收

1. 开机后直连屏幕显示 A-NAS 桌面，中文字体正常，键盘和鼠标可用。
2. 桌面右上角状态栏显示“模拟数据”或“实时主机”，CPU/内存仪表盘与网速有读数；打开资源管理可读取对应状态。
3. `ps` 显示 Cage 与 Chromium 均属于 `anas-dev`，Chromium 参数中不存在 `--no-sandbox`。
4. SSH 停止 `anas-api.service` 后，Kiosk 页面进入失联状态；恢复服务后自动重新连接。
5. `Ctrl+Alt+F2` 应切到恢复终端并能以 `Ctrl+Alt+F1` 返回；当前 Experimental NAS 未通过此项，见[开放调查](../investigations/2026-10-06-kiosk-browser-confinement.md)。
6. `F1`、浏览器导航、开发工具、不受控缩放和右键菜单不可逃离产品界面；当前 Experimental NAS 未通过 `F1` 和缩放项，不能把本地 Kiosk 视为面向非受信任用户的安全边界。
7. 登录后请求 `/local-console/screensavers`，返回的匿名 URL 数应与 `current/SHA256SUMS` 的行数相同；逐个以 `Range: bytes=0-1023` 请求时均返回 `206` 和 1024 字节。
8. 登录后保持三分钟无输入，屏保应从池中选择一条，静音、居中铺满且无黑边；让视频自然结束，确认仍循环同一条而不切换。第一次移动鼠标或按键只退出屏保，不打开或操作下层应用；再次等待三分钟后应重新随机选择池内一条，允许与上次相同。
9. 临时使当前选中的一个已确认池文件不可读并重启页面时，应从剩余条目中补选一条；恢复权限后再验收。不要删除源文件或整个池来制造故障。

如果分辨率、方向或 UI 大小不正确，先在同一 Wayland 会话中用 `wlr-randr` 临时验证，再更新 `/etc/a-nas/kiosk.env`；不要在不知道输出名称时写死显卡或 connector。

## 回滚

从 SSH 管理会话执行：

```bash
systemctl disable --now anas-kiosk@tty1.service
systemctl set-default multi-user.target
rm -f /etc/systemd/system/anas-kiosk@.service /etc/pam.d/a-nas-kiosk
systemctl daemon-reload
systemctl reset-failed
```

这只移除本地显示会话，不停止 A-NAS API、Host Agent 或 SSH。

按[实机配置手册](provision-v1.0.1-experimental-storage.md#回滚)回滚产品不会改变正在播放的屏保池。媒体回滚就是重新安装一个已安装的池，不需要暂存区：

```bash
ls -l --time-style=long-iso /var/lib/a-nas/screensavers/pools
cat /var/lib/a-nas/screensavers/pools/<池 ID>/SHA256SUMS
/opt/a-nas/current/install-screensavers.sh <池 ID>
```

之后按验收第 7 项复核清单数量和 Range 请求。不要删除当前池作为回滚手段；对象和旧池的清理不属于常规回滚，必须另行确认。

## 依据

- [Cage 官方 systemd 启动说明](https://github.com/cage-kiosk/cage/wiki/Starting-Cage-on-boot-with-systemd)
- [Debian 13 Cage 手册](https://manpages.debian.org/trixie/cage/cage.1.en.html)
- [systemd.exec 对 PAM、TTY 和运行时目录的说明](https://manpages.debian.org/trixie/systemd/systemd.exec.5.en.html)
- [本地控制台屏保规格](../specs/local-console-screensaver.md)
- [ADR 0014：屏保视频是系统盘上的长期媒体](../adr/0014-keep-screensaver-videos-as-system-disk-media-outside-releases.md)
