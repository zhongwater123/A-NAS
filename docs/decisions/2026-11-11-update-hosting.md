# 软件源与更新文件放在哪里

状态：decided（2026-11-12，用户）
更新时间：2026-11-12

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

用户 2026-11-12 问“先把 github 作为分发源是否可行”，看过下面的做法后说“按方案做”：先用 GitHub，以后需要时再换。

- **单独的公开仓库** `zhongwater123/A-NAS-dist`，只放发布文件。代码仓库以后改为私有，设备照样能更新；私有仓库的附件要令牌才能下载，设备上不放令牌。
- **apt 的平铺软件源**：GitHub 的发布附件不能放在目录里，apt 要的 `dists/` 与 `pool/` 无法照搬。每个渠道是一个固定的发布（`apt-release`、`apt-dev`），签名索引（`InRelease`、`Release`、`Packages`）与全部软件包并排。索引由 `apt-ftparchive` 生成，用 A-NAS 存档密钥签名；软件包以 SHA-256 命名，因为 GitHub 会改掉文件名中的 `~` 与 `+`。开发机上的 aptly 软件源仍是正本，`make publish` 由它生成平铺的副本并上传（[`publish-github.sh`](../../scripts/publish-github.sh)）。`release` 保留每个版本，`dev` 只保留最近 20 个。
- **单个文件 2 GiB 的上限**：OCR 模型（2.59 GiB）由 llama.cpp 自带的 `llama-gguf-split` 切成两份（1.80 GB 与 0.98 GB），各成一个内容包，llama-server 从第一份读起；AI Worker 逐份核对 SHA-256（[`split-model.sh`](../../scripts/split-model.sh)，[清单](../../deploy/models/qwen3.5-4b-q4_k_m.json)）。同一模型与工具切两次，结果相同。
- **公开分发**：模型都是 Apache-2.0 或 MIT 许可，可以再分发；许可说明随内容包附带。
- **速度**：2026-11-12 从开发机与 Experimental NAS 下载 GitHub 的发布附件，都在 1.6–1.7 MB/s：产品更新约 45 MB，约 30 秒；首次装全部模型约 4.2 GB，约 45 分钟。
- **以后**：家里的设备嫌慢时，改用国内的对象存储或 CDN，只是换设备取更新的地址；签名不变。

设备的软件源写法见 [`a-nas-github.sources`](../../deploy/apt/a-nas-github.sources)，步骤见[部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)。

## 关联

- [ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)
- [部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)
- [发布用的签名密钥由谁、怎样保管](2026-11-11-release-signing-key.md)
- 代码：[`publish-github.sh`](../../scripts/publish-github.sh)、[`split-model.sh`](../../scripts/split-model.sh)、[`a-nas-github.sources`](../../deploy/apt/a-nas-github.sources)
