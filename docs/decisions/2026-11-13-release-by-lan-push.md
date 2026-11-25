# 近期以局域网推送发布，GitHub 作备选

状态：decided（2026-11-13，用户）
更新时间：2026-11-13

## 问题

签名软件源怎样到达宿主机（Experimental NAS 与以后的测试机）。2026-11-12 的[决定记录](2026-11-11-update-hosting.md)把软件源发到 GitHub 的 `zhongwater123/A-NAS-dist`。用户随后说明，那只是备选；近期主要靠内网与局域网推送。还要定由哪台开发机构建与签名、哪台推送：开发机 B（`D:\A-NAS`）与宿主机不一定在同一个网络里，开发机 A（`E:\A-NAS`）与宿主机在同一个网络里。

## 选项

- **方案一：开发机 B 构建并签名，开发机 A 中转。** 开发机 B 在最新的 main 上跑门禁，用 `make release` 以子密钥 `dev-b` 签名；软件源目录交给开发机 A，由它推送到宿主机的暂存账号。开发机 A 不需要工具链、内容库与子密钥。
- **方案二：开发机 A 从远端仓库构建。** 开发机 A 自己检出、跑门禁、以 `dev-a` 签名并推送。它要有全套工具链、内容库与子密钥，并先取回最新的软件源接着发布。
- **GitHub**：开发机 `make publish` 上传，宿主机自己从 GitHub 取。

## 决定

用户 2026-11-13 说：“github发布只是作为备选，我们主要的发布形式近期内还是内网、局域网推送”，并选了方案一（“按方案一，开始做更新服务”）：

- 平时由开发机 B 构建与签名。软件源目录经开发机 A 或开发机 B 推送到宿主机的暂存账号（[部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)）；开发机 B 能连到宿主机时直接推送。
- 宿主机上的[系统更新](../specs/system-update.md)服务把推送来的软件源同步到 apt 的本机软件源，管理员在“设置 → 系统更新”中安装；推送的账号不在信任链上，apt 按存档密钥核对每个文件。
- 方案二是备用：开发机 B 不可用，或宿主机只能从开发机 A 访问时，按[管理发布签名密钥](../runbooks/manage-release-key.md)的“换另一台开发机发布”接手。同一个版本只由一台开发机构建。
- GitHub 上的 `A-NAS-dist` 与 `make publish` 保留为备选，平时不发布。

本记录取代 [2026-11-11 软件源与更新文件放在哪里](2026-11-11-update-hosting.md) 中“先用 GitHub”的结论；那篇中 GitHub 的做法（平铺形式、OCR 模型切成两份）照旧用于备选。

## 关联

- 规格：[系统更新](../specs/system-update.md)
- 运行手册：[部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)、[管理发布签名密钥](../runbooks/manage-release-key.md)
- ADR：[0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)
