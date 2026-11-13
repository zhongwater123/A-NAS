# 设备镜像的更新来源、装机方式与 Secure Boot

状态：decided（2026-11-13，用户）
更新时间：2026-11-13

## 问题

ADR 0034 第 5 步把家里的设备做成整机镜像（[ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md) 决定 2）。动手之前要定三件可以改回来的事：

- 镜像设备从哪里取更新文件；
- 新设备怎样装上第一个镜像；
- 是否开 Secure Boot。

## 选项

- **更新来源**：
  - 一：与实验 NAS 一样，开发机推送到设备的暂存目录，更新服务同步到本机目录，systemd-sysupdate 从本机目录读（[近期以局域网推送发布](2026-11-13-release-by-lan-push.md)）；
  - 二：设备经 HTTPS 自己下载，地址是 GitHub 的 `A-NAS-dist` 或以后的国内 CDN；
  - 两者都支持：默认读本机目录，`site.conf` 写了 HTTPS 地址时改为下载。
- **装机方式**：
  - 一：USB 安装盘，从 U 盘启动、选择目标硬盘、确认后写入；
  - 二：在开发机上把镜像直接写到系统盘，再装进机器；
  - 试点先用二，给家里的设备用的一随后做。
- **Secure Boot**：v1 不开（第 3 步的验证也没开），以后再给 UKI 签名；或从 v1 起签名。

## 决定

用户 2026-11-13 采纳了每项的建议（“按建议，开始做”）：

- 两种更新来源都支持：镜像中的配置默认读推送来的本机目录；`site.conf` 写了 HTTPS 地址时，改为从这个地址下载。近期的测试机用推送，联网的家用设备用 HTTPS。
- 试点先在开发机上把镜像写到系统盘；USB 安装盘随后做，给家里的设备用。
- v1 不开 Secure Boot，以后再给 UKI 签名。

未记录其他理由。

## 关联

- ADR：[0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md) 第 5 步
- 决定记录：[近期以局域网推送发布](2026-11-13-release-by-lan-push.md)、[软件源与更新文件放在哪里](2026-11-11-update-hosting.md)
- 规格：[系统更新](../specs/system-update.md)
- 研究：[设备镜像与 systemd-sysupdate 的验证](../research/sysupdate-image-verification.md)
