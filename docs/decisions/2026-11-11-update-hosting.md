# 软件源与更新文件放在哪里

状态：pending
更新时间：2026-11-11

## 问题

`make release` 写出的软件源是一个目录（[ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md) 决定 1）。现在由部署机把它复制到 NAS，apt 从本机目录安装（[部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)）。联网的设备要从一个 HTTPS 地址取软件源，以后也从那里取 systemd-sysupdate 的镜像与 `SHA256SUMS`。ADR 0034 的“后果”把放在哪里留给另行决定。

签名在软件源里，放在哪里不影响信任：apt 拒绝与签名不符的文件。要考虑的是大小、下载速度与费用：

- 内容包约 4.4 GB，最大的一个（OCR 模型）2.6 GiB；每个 release 的产品包约 45 MB；设备镜像每次约 750 MiB。
- 软件源保留每个版本，回滚要用旧版本。
- 用户在中国大陆。

## 选项

- **GitHub Releases**：免费、与仓库在一起。单个附件不超过 2 GiB，放不下 OCR 模型的内容包；apt 也不能按 Releases 的地址组织 `dists/` 与 `pool/`，要另做一层转发。国内下载慢且不稳定。
- **对象存储加 CDN（阿里云 OSS、腾讯云 COS 等）**：目录原样上传，按流量计费，国内速度好；要有账号与备案的域名。
- **自建 HTTPS 服务器**：例如机房的边缘服务器（[ADR 0032](../adr/0032-reach-the-web-desktop-from-the-internet-through-an-edge-relay.md)），用 Caddy 发布目录。费用低；带宽与可用性取决于这台机器。
- **只在局域网复制**：维持现状，部署机复制目录。家里的设备不能自己更新。

## 决定

待用户决定。

## 关联

- [ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)
- [部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)
- [发布用的签名密钥由谁、怎样保管](2026-11-11-release-signing-key.md)
