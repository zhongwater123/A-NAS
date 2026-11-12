# 发布用的签名密钥由谁、怎样保管

状态：decided（2026-11-12，用户）
更新时间：2026-11-12

## 问题

A-NAS 的软件源以 GPG 签名（[ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md) 决定 1），以后设备的 systemd-sysupdate 也以 GPG 签名的 `SHA256SUMS` 校验更新（决定 2），两者可以共用一套保管办法。装了 `a-nas-archive-keyring` 的机器只信任这把密钥签的软件源：密钥丢了，已有的设备收不到更新；密钥泄露，别人签的软件源也会被接受。

现在 `make release` 用的是测试密钥：开发机上第一次运行时生成，放在 `~/.local/share/a-nas/release-test-key`，没有口令，只用于开发与测试机（[`scripts/release.sh`](../../scripts/release.sh)）。卖到家里的设备装上软件源之前，要换成正式密钥。

要定：

- 正式密钥放在哪里，谁能用它签名；
- 备份放在哪里，丢失或泄露时怎样换钥（新的 `a-nas-archive-keyring` 要先用旧密钥签名发出）；
- 测试机继续用测试密钥，还是也换成正式密钥。

## 选项

- **离线主密钥加签名子密钥**：主密钥只在离线介质上，用于签发与吊销子密钥；签名子密钥放在发布机上，`ANAS_RELEASE_GNUPGHOME` 指向它。换签名子密钥不必换 `a-nas-archive-keyring` 中的主公钥。要有人保管离线介质与口令。
- **硬件密钥（OpenPGP 智能卡，例如 YubiKey）**：私钥不出卡，签名时插卡并输入 PIN。不能在无人值守的 CI 中签名；卡要有备份卡。
- **CI 中签名**：密钥放在 GitHub Actions 的加密变量中，打标签后由 CI 构建并签名。发布不依赖某台开发机；能改工作流或读到变量的人都能签名，工作流的权限要收紧。
- **开发机上的普通密钥文件**：与现在的测试密钥相同，加口令与备份。最省事，开发机被入侵即泄露。

## 决定

用户 2026-11-12 决定，正式密钥由本台开发机（开发机 B）和另一台开发机（开发机 A）保管。同日用户确认了下面的做法（“开始做”）：

- **结构**：一把 A-NAS 存档密钥。主密钥只用于签发与吊销，不签软件源；每台开发机各有一把自己的签名子密钥（`dev-b`、`dev-a`）。设备的 `a-nas-archive-keyring` 装的是主公钥连同全部子密钥，取自仓库中的 `deploy/release/a-nas-archive-key.asc`（生成密钥后提交），所以改动公钥要经过评审。
- **保管**：每台开发机的签名目录只有自己的子密钥，没有主密钥的私钥。主密钥的私钥与吊销证书加口令导出后，在两台开发机上各存一份，放在签名目录之外；另建议在 U 盘上再存一份。
- **口令**：每次 `make release` 时输入，不存进文件；没有终端时从 `ANAS_RELEASE_PASSPHRASE_FILE` 读取。
- **不设有效期**：长期离线的家用设备不会因为密钥过期而收不到更新；泄露靠吊销处理。
- **谁发布**：平时开发机 B 发布，开发机 A 备用；接手前把软件源目录复制过去，签名时接上已发布的内容。软件源放在哪里定下之前，同一时间只由一台发布。
- **测试密钥**：只签开发机自己的测试软件源。`make release` 不会用另一把密钥签已有的软件源，所以第一个正式 release 写进新的软件源，测试密钥签的 `D:\A-NAS-releases\apt` 在用户确认后删除。

一台开发机丢失或泄露时，另一台用主密钥的备份吊销它的子密钥，并发布新的 keyring；设备随下一次更新收到，不用重装。步骤见[管理发布签名密钥](../runbooks/manage-release-key.md)。

## 关联

- [ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)
- [管理发布签名密钥](../runbooks/manage-release-key.md)
- [软件源脚本](../../scripts/apt-repository.sh)、[`make release`](../../scripts/release.sh)、[密钥脚本](../../scripts/release-key.sh)、[密钥流程的测试](../../scripts/test-release-key.sh)
- [部署 A-NAS 软件包](../runbooks/deploy-a-nas-packages.md)
- [软件源与更新文件放在哪里](2026-11-11-update-hosting.md)
