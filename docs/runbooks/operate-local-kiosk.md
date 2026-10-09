# 运行实验 NAS 本地控制台

状态：implemented, display and pointer verified; confinement and VT recovery pending
更新时间：2026-10-09

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
- `screensavers/`（可选，只在更换视频池时携带；由部署命令以逐文件 SHA-256 验证的匿名 MP4 构成，不进入 Git）

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

屏保池的生命周期独立于产品版本。release 携带视频时，系统安装脚本逐个复核 SHA-256，把视频复制到 `/var/lib/a-nas/screensavers/<release-id>/`（目录 `root:a-nas 0750`，视频 `root:a-nas 0640`），再把同级链接 `current` 原子切到这个新池。不携带视频的 release 不建池，也不改 `current`，所以日常升级无需重新上传视频。`/etc/a-nas/anas-api.env` 固定把 `/var/lib/a-nas/screensavers/current` 交给产品服务只读扫描，Chromium 只取得匿名同源 URL，不直接访问宿主机路径或原文件名。池缺失不会阻止产品服务或 Kiosk 启动。

首次用包含 [issue #49](https://github.com/zhongwater123/A-NAS/issues/49) 修复的安装器升级时还没有 `current`，安装器指向最近安装的池，因此原来播放的池或因该缺陷停播的池会继续播放。之后若用不含该修复的旧安装器装过带视频的 release，新池不会成为 `current`；按下面的媒体回滚步骤把 `current` 切到该池。

保持 SSH 恢复会话后，首次验证可执行：

```bash
systemctl start anas-kiosk@tty1.service
systemctl status anas-kiosk@tty1.service --no-pager
journalctl -u anas-kiosk@tty1.service -b --no-pager -n 100
```

服务与 `getty@tty1` 冲突并接管该虚拟终端。启动器最多等待产品 `/healthz` 30 秒；产品尚未就绪时退出，由 systemd 自动重试，而不是把 Chromium 错误页留在屏幕上。

## 验收

1. 开机后直连屏幕显示 A-NAS 桌面，中文字体正常，键盘和鼠标可用。
2. 桌面右上角状态栏显示“模拟数据”或“实时主机”，CPU/内存仪表盘与网速有读数；打开资源管理可读取对应状态。
3. `ps` 显示 Cage 与 Chromium 均属于 `anas-dev`，Chromium 参数中不存在 `--no-sandbox`。
4. SSH 停止 `anas-api.service` 后，Kiosk 页面进入失联状态；恢复服务后自动重新连接。
5. `Ctrl+Alt+F2` 应切到恢复终端并能以 `Ctrl+Alt+F1` 返回；当前 Experimental NAS 未通过此项，见[开放调查](../investigations/2026-10-06-kiosk-browser-confinement.md)。
6. `F1`、浏览器导航、开发工具、不受控缩放和右键菜单不可逃离产品界面；当前 Experimental NAS 未通过 `F1` 和缩放项，不能把本地 Kiosk 视为面向非受信任用户的安全边界。
7. 登录后请求 `/local-console/screensavers`，应返回五个匿名 URL；逐个以 `Range: bytes=0-1023` 请求时均返回 `206` 和 1024 字节。
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

屏保池独立于产品版本：按[实机配置手册](provision-v1.0.1-experimental-storage.md#回滚)回滚产品不会改变正在播放的池，所有旧池都保留。需要回滚或切换媒体时，先从 `ls` 输出和对应 release 的 `RELEASE` 记录确认目标池，再原子切换 `current`：

```bash
pools=/var/lib/a-nas/screensavers
ls -l --time-style=long-iso "$pools"
target=<目标池的 release ID>
test -f "$pools/$target/screensaver-000.mp4"
ln -sfn -- "$target" "$pools/.current.next"
mv -Tf -- "$pools/.current.next" "$pools/current"
systemctl restart anas-kiosk@tty1.service
```

产品服务每次请求都重新读取 `current`，无需重启；重启 Kiosk 只是让已打开的页面重新读取清单。之后按验收第 7 项复核清单数量和 Range 请求。不要删除当前池作为回滚手段。旧池清理不属于常规回滚，必须另行确认精确的池目录、备份需求和 `current` 的指向后才能执行。

## 依据

- [Cage 官方 systemd 启动说明](https://github.com/cage-kiosk/cage/wiki/Starting-Cage-on-boot-with-systemd)
- [Debian 13 Cage 手册](https://manpages.debian.org/trixie/cage/cage.1.en.html)
- [systemd.exec 对 PAM、TTY 和运行时目录的说明](https://manpages.debian.org/trixie/systemd/systemd.exec.5.en.html)
- [本地控制台屏保规格](../specs/local-console-screensaver.md)
