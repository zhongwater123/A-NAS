# 准备实验 NAS 的热点网卡

状态：draft
更新时间：2026-11-20

## 目标

让实验 NAS 上的 USB MediaTek MT7612U（`0e8d:7612`）绑定内核的 `mt76x2u` 驱动，再核对 AP 模式与有线默认路由。

2026-11-20 在 `a-nas-dev` 的 Debian 13 内核 `6.12.111+deb13-amd64` 上检查到驱动已加载，USB 接口显示 `Driver=[none]`，驱动声明的 `mt7662.bin` 和 `mt7662_rom_patch.bin` 均缺失。[Debian 的固件包](https://packages.debian.org/trixie/all/firmware-mediatek/filelist)包含这两个文件。

同日完成 apt 快验。`firmware-mediatek` 为 `20250410-2`，`iw` 为 `6.9-1`。`lsusb -t` 显示 `mt76x2u`，`iw list` 列出 `AP`，默认路由保持 `enp2s0`，Web `/healthz` 返回 `{"status":"ok"}`。

## 构建与部署

- 正式交付使用可选包 `a-nas-hotspot-radio`。按[软件包部署手册](deploy-a-nas-packages.md)构建和暂存签名软件源。核对 `apt-cache policy a-nas-hotspot-radio` 的候选版本来自该软件源，再通过 `apt-get install --no-install-recommends --no-remove a-nas-hotspot-radio` 选择安装。
- [amd64 清单](../../deploy/upstream/hotspot-radio-amd64.json)与 [arm64 清单](../../deploy/upstream/hotspot-radio-arm64.json)分别记录包版本、路径、大小和 SHA-256。两种目标都携带 Debian 的 `all` 架构固件包，并按架构选择 `iw` 及其库。可选包沿用候选包的 `ANAS_TARGET_ARCH`。
- 如需单独组装待传输的 arm64 可选包及依赖，使用同一 release 版本作为 `VERSION`，将下列产物加入该架构候选包的校验清单。amd64 使用相同命令的 amd64 目标。

```bash
ANAS_TARGET_ARCH=arm64 bash scripts/build-hotspot-radio-package.sh "$OUT" "$VERSION"
bash scripts/fetch-upstream-packages.sh "$OUT/upstream" deploy/upstream/hotspot-radio-arm64.json
```

## 实验机快速验证

一次性的固件探测可先从可信 Debian 源执行 `apt-get -s install --no-install-recommends firmware-mediatek iw`。核对只新增这两个包和两个 `libnl` 依赖，再由 root 执行 `apt-get install --no-install-recommends --no-remove firmware-mediatek iw`，保留安装后 `dpkg-query -W` 的版本。

如果网卡仍未绑定，先用 `lsusb` 与 `lsusb -t` 确认 USB 型号、端口和接口。实验 NAS 上观测到端口 `2-2`、接口 `2-2:1.0`。确认 `/sys/bus/usb/devices/2-2/idVendor` 为 `0e8d`、`idProduct` 为 `7612`，且接口没有 `driver` 链接后，由 root 设置绑定。

```bash
printf '%s\n' '2-2:1.0' > /sys/bus/usb/drivers/mt76x2u/bind
```

## 验证与回退

- 用 `lsusb -t` 确认驱动、`iw dev` 确认无线接口，并在 `iw list` 的接口模式中核对 `AP`。重新核对 `ip -4 route show default` 的有线出口和 Web `/healthz`。
- 如需撤销绑定，在确认目标是本次测试的 USB 接口后，把接口名写入该驱动目录的 `unbind`。如需撤销安装，先模拟移除计划，再移除本次新增且没有其它设备或工具依赖的包。回退后再次核对有线默认路由与 Web 健康检查。

## 关联

- [热点可选包定义](../../deploy/debian/a-nas-hotspot-radio/DEBIAN/control)、[构建](../../scripts/build-hotspot-radio-package.sh)、[系统测试](../../scripts/system-test.sh)
- [启用局域网 Web 访问](enable-lan-web-access.md)
