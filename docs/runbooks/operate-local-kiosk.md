# 运行实验 NAS 本地控制台

状态：implemented, packages verified, activation pending
更新时间：2026-10-06

## 目的

让带直连屏幕、键盘和鼠标的 Experimental NAS 在启动后进入 A-NAS Web 桌面。Cage 只承载一个最大化 Chromium，浏览器只访问回环地址 `http://127.0.0.1:8080/`；产品服务和 Host Agent 的权限边界不变。

## 安全边界

- Kiosk 以 `anas-dev` 运行，不以 root 运行 Chromium，也不使用 `--no-sandbox`。
- Cage 通过 PAM 向 `systemd-logind` 注册真实本地会话，由 logind 授予活动 seat 的显示和输入设备访问；不要预先把 `anas-dev` 加入 `input` 或 `video` 组。
- `cage -s` 保留 VT 切换作为本地恢复路径。Kiosk 不是登录界面，也不提供终端。
- 页面仍来自只监听 `127.0.0.1:8080` 的产品服务；直连屏幕不会扩大网络暴露面。
- 如果机器已经存在其他 display manager，停止配置并先决定唯一图形会话所有者。

## 前提

先按[Web 桌面部署手册](deploy-web-preview-to-experimental-nas.md)至少部署一次，使以下版本化文件出现在 `~/apps/a-nas/current/`：

- `kiosk-launcher`
- `anas-kiosk@.service`
- `a-nas-kiosk.pam`

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

systemctl daemon-reload
systemctl set-default graphical.target
systemctl enable anas-kiosk@tty1.service
```

该单元只通过 `graphical.target.wants` 启动，不声明 `display-manager.service` 别名。Debian 13 的 systemd 会拒绝把显式模板实例别名为非模板单元，而该别名并不是 Kiosk 随 `graphical.target` 启动的必要条件。

保持 SSH 恢复会话后，首次验证可执行：

```bash
systemctl start anas-kiosk@tty1.service
systemctl status anas-kiosk@tty1.service --no-pager
journalctl -u anas-kiosk@tty1.service -b --no-pager -n 100
```

服务与 `getty@tty1` 冲突并接管该虚拟终端。启动器最多等待产品 `/healthz` 30 秒；产品尚未就绪时退出，由 systemd 自动重试，而不是把 Chromium 错误页留在屏幕上。

## 验收

1. 开机后直连屏幕显示 A-NAS 桌面，中文字体正常，键盘和鼠标可用。
2. 桌面右上角显示“模拟数据”或“实时主机”；打开资源管理可读取对应状态。
3. `ps` 显示 Cage 与 Chromium 均属于 `anas-dev`，Chromium 参数中不存在 `--no-sandbox`。
4. SSH 停止 `anas-api.service` 后，Kiosk 页面进入失联状态；恢复服务后自动重新连接。
5. `Ctrl+Alt+F2` 可切到恢复终端；SSH 执行 `systemctl restart anas-kiosk@tty1.service` 可恢复显示会话。

如果分辨率或屏幕方向不正确，先记录 `journalctl` 中的 DRM connector 名称，再在同一 Wayland 会话中用 `wlr-randr` 验证；不要在不知道输出名称时写死显卡或 connector。

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

## 依据

- [Cage 官方 systemd 启动说明](https://github.com/cage-kiosk/cage/wiki/Starting-Cage-on-boot-with-systemd)
- [Debian 13 Cage 手册](https://manpages.debian.org/trixie/cage/cage.1.en.html)
- [systemd.exec 对 PAM、TTY 和运行时目录的说明](https://manpages.debian.org/trixie/systemd/systemd.exec.5.en.html)
