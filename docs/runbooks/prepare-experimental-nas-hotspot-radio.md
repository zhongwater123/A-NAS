# 准备实验 NAS 的热点网卡

状态：draft
更新时间：2026-11-26

## 目标与前提

让 `a-nas-dev` 的 USB MediaTek MT7612U（`0e8d:7612`）绑定 `mt76x2u` 驱动，再检查它是否报告 AP 模式。正式的双架构包见 [PR #615](https://github.com/zhongwater123/A-NAS/pull/615)。热点和手机上传流程见[规格](../specs/host-network-and-phone-hotspot.md)。

2026-11-20 实测，内核 `6.12.111+deb13-amd64` 已加载 `mt76x2u`，USB 接口仍为 `Driver=[none]`。`modinfo -F firmware mt76x2u` 列出的 `mt7662_rom_patch.bin` 与 `mt7662.bin` 均缺失。Debian trixie 的 [`firmware-mediatek`](https://packages.debian.org/trixie/all/firmware-mediatek/filelist) 提供这两个文件。

## 安装和探测

1. 用 `hostname`、`ip -4 route show default`、`lsusb` 和 `lsusb -t` 核对实验 NAS、有线默认路由和 USB 网卡。当前设备是 `2-2` 上的 `0e8d:7612`，接口为 `2-2:1.0`。若换了插口，先重新确定 sysfs 路径。
2. 2026-11-20 实验 NAS 一次性 apt 快验安装 `firmware-mediatek`（`20250410-2`）、`iw`（`6.9-1`）和两个 `libnl` 依赖。正式部署按[软件包部署手册](deploy-a-nas-packages.md)从签名软件源选择安装 `a-nas-hotspot-radio`，其中还包含 `hostapd` 和 `dnsmasq-base` 的依赖。
3. 如果 `/sys/bus/usb/devices/2-2:1.0/driver` 还不存在，再确认 `2-2` 的 `idVendor` 与 `idProduct`，随后在 root shell 执行 `printf '%s\n' '2-2:1.0' > /sys/bus/usb/drivers/mt76x2u/bind`。只重新探测这个 USB 接口，探测失败时用 `journalctl -k -b` 查看 `mt76x2u` 的日志。

## 临时热点试跑

1. 核对 `iw dev`、`iw reg get`、`iw list` 和 `ip -4 route show table all`。2026-11-20 的实验机预检中，MT7612U 处于空闲 `managed` 状态，`enp2s0` 是 IPv4 默认出口，`192.168.173.0/24` 无已知路由冲突，允许在 2.4 GHz 信道 6 启动 AP。
2. 在非特权账号的临时目录，用 `apt-get download` 下载 Debian trixie 的 `hostapd`、`dnsmasq-base` 和 `libnl-route-3-200`。核对归档的 SHA-256，解包并预检二进制。管理员把试跑归档复制到 root 独占的 `/run` 目录并校验。此机的 `/run` 挂载了 `noexec`，第一次试跑停在执行权限检查。修正版把工具解包到 root 独占的 `/var/tmp`，配置与密码留在 `/run`。试跑通过 `anas-hotspot-pilot.service` 管理，时限 45 分钟，服务结束时清理工具目录和密码。
3. 无线接口使用 `192.168.173.1/24`，独立 DHCP 只给无线客户端发地址。热点接口防火墙允许 DHCP 和发往这个地址的 TCP 80，丢弃热点接口的其余入站及转发。使用终端显示的随机 WPA2 密码连接 `A-NAS-Trial`，然后打开 `http://192.168.173.1/`。
4. 用 `iw dev`、`ip -4 route show default` 和 Web `/healthz` 检查 AP 与原有服务，再从手机验证登录、上传和续传。正常结束或出现异常时，由 root 执行 `systemctl stop anas-hotspot-pilot.service`。服务停止后检查无线接口恢复 `managed`、试跑地址和 `inet anas_hotspot_pilot` 防火墙表已清理，确认有线默认路由与 Web 健康检查正常。

2026-11-20 修正版试跑服务处于 `active/running`。`iw dev` 确认 AP 在 2.4 GHz 信道 6、20 MHz，地址为 `192.168.173.1/24`。手机已连接并能访问 A-NAS。热点和有线地址的 `/healthz` 均正常，默认出口为 `enp2s0`，经有线地址访问 Debian 镜像的 HTTPS 请求返回 200。

同一手机上传约 64 MB 的视频，用户估计热点用时约 20 秒（约 3.2 MB/s），日常 Wi-Fi 用时约 2 秒（约 32 MB/s）。热点上传前后无线接口 `rx_bytes` 从 `666243` 增至 `68524914`，有线默认出口仍为 `enp2s0`。对照结束后复查，试跑服务已停止，有线 `/healthz` 返回 200。两次用时均为一次目测，断网续传待实测。

约 2 秒的对照结果来自手机可以访问 NAS 内网地址的测试条件。实际使用时，手机平时无法访问该地址，可在 NAS 附近加入直连热点临时上传。

## 热点证书

2026-11-26 实验 NAS 改用公共 CA 的证书，手机不用安装根证书。域名托管在 DNSPod，按下面的步骤由 Let's Encrypt 经 DNS-01 签发。

1. 在自有域名下取一个热点专用的子域名 `<热点域名>`，在公网 DNS 加一条 A 记录指向热点地址 `192.168.173.1`。热点内由 NAS 自己的 DNS 解析；手机经移动网络的 DNS 查询时，也会回到热点。
2. 在腾讯云 CAM 建一个只开编程访问的子用户，授予自定义策略，策略只含 `dnspod:DescribeRecordList`、`dnspod:DescribeRecordFilterList`、`dnspod:CreateRecord` 与 `dnspod:DeleteRecord`。root 把这个子用户的密钥写进 `/etc/a-nas/phone-hotspot-acme/tencentcloud.env`，写成 `TENCENTCLOUD_SECRET_ID=…` 与 `TENCENTCLOUD_SECRET_KEY=…` 两行，目录权限 `0700`，文件权限 `0600`。
3. 从 Debian 安装 `acme.sh`（3.1.1），用它的 `dns_tencent`（腾讯云 API 3.0）签发：`acme.sh --home /var/lib/a-nas-hotspot-acme --server letsencrypt --issue --dns dns_tencent -d <热点域名> --keylength ec-256 --dnssleep 60 --renew-hook "/usr/local/sbin/a-nas-hotspot-cert install"`。`dns_tencent` 从环境变量 `Tencent_SecretId` 与 `Tencent_SecretKey` 读密钥，并在 `--home` 目录的 `account.conf` 另存一份（root，`0600`）。Debian 的 `lego` 4.9.1-2 用不了：它的构建去掉了 `tencentcloud`、`dnspod` 等大多数 DNS 服务商，`lego dnshelp` 却仍列出它们，运行时报 `unrecognized DNS provider`。
4. root 专属的 `/usr/local/sbin/a-nas-hotspot-cert install` 先核对证书一天内不过期、私钥与证书匹配、证书链通到系统信任库，再写入 `/etc/a-nas/phone-hotspot/` 的 `domain`、`fullchain.pem` 与 `privkey.pem`（`0600`）。`a-nas-hotspot-cert.timer` 每天运行 `acme.sh --cron`，到期前 30 天续期后再执行 `install`；热点下次开启时加载新证书。
5. 排错：acme.sh 3.1.1 查不到域名所在的区域时，不打印腾讯云返回的错误，只报 `Error adding TXT record`。这时逐个调用上面四个接口，看各自的错误码：`AuthFailure.SignatureFailure` 表示 SecretKey 不对，腾讯云的 SecretKey 是 32 位。

核对：`openssl x509 -in /etc/a-nas/phone-hotspot/fullchain.pem -noout -subject -enddate`；`systemctl list-timers a-nas-hotspot-cert.timer`；`journalctl -u a-nas-hotspot-cert.service`。

撤销：`systemctl disable --now a-nas-hotspot-cert.timer`；删除这两个单元、`/usr/local/sbin/a-nas-hotspot-cert`、`/var/lib/a-nas-hotspot-acme` 与 `/etc/a-nas/phone-hotspot-acme`；在 CAM 删除子用户的密钥。删除 `/etc/a-nas/phone-hotspot/` 后，热点会报证书不可用。

## 验证与回退

2026-11-20 已通过 SSH 复核，`lsusb -t` 显示 `mt76x2u`，`iw list` 列出 `AP`，有线默认路由保持 `enp2s0`，Web 健康检查正常。

- 为每台 NAS 准备热点专用域名及与之匹配的证书。热点内的 DNS 直接返回 NAS 热点地址，手机在热点内访问时无需连接公有 DNS。证书可以用 NAS 的有线侧提前签发并更新。
- 管理员在 NAS 上创建 root 专属的 `/etc/a-nas/phone-hotspot/`，目录权限为 `0700`。将单行域名写入 `domain`，证书链保存为 `fullchain.pem`，私钥保存为 `privkey.pem`，文件由 root 所有且私钥权限为 `0600`。公有 CA 证书使用 NAS 系统信任库。内网 CA 的根证书放在 `trust.pem`，并在连接前通过可信渠道把同一 CA 配到手机信任库。
- 从有线入口打开「设置 → 网络」，开启手机直连热点。手机连接页面显示的 Wi-Fi 后，打开页面给出的 HTTPS 地址并核对浏览器接受证书，再登录和上传。切回有线入口关闭热点，复查有线端的 `/healthz`。服务启动失败时先看设置页原因，宿主机日志由 `journalctl -u anas-phone-hotspot.service` 查询。
- 「编辑热点设置」可保存 Wi-Fi 名称、密码和系统连接提示，保存后下次开启时生效。通过「已连接设备」核对手机是否加入以及断开后是否消失。开启连接提示后，用 iPhone 和 Android 核对系统门户，点「打开 WebOS」，再在正常手机浏览器登录并上传。没有系统提示时，可打开设置页显示的 HTTPS 地址。
- 如需从控制台结束正式热点，执行 `systemctl stop anas-phone-hotspot.service`。确认无线接口回到宿主机且无热点地址，再按下文决定是否移除可选包。更新证书后，下次开启热点会加载新的证书。

- `lsusb -t` 显示无线接口绑定 `mt76x2u`，`iw dev` 有无线接口，`iw list` 的 `Supported interface modes` 含 `AP`。用 `ip -4 route show default` 确认有线出口，再从开发机验证 NAS Web `/healthz`。
- 如需撤销，仅在确认是本次绑定的设备后，把接口名写入同一驱动目录的 `unbind`。如需移除本次部署，先执行 `apt-get -s remove a-nas-hotspot-radio` 核对计划。只在确认新增的依赖无其它用途后移除它们。回退后再次核对有线默认路由与 Web 健康检查。

## 关联

- [宿主机网络与手机直连热点](../specs/host-network-and-phone-hotspot.md)
- [配置实验 NAS 的 SSH 开发账号](bootstrap-experimental-nas-ssh.md)
- [双架构打包与系统测试 PR](https://github.com/zhongwater123/A-NAS/pull/615)
