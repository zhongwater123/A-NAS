# 0014：屏保视频是系统盘上的长期媒体，不随 release 发布

状态：accepted；行为见[本地控制台屏保规格](../specs/local-console-screensaver.md)，操作见[本地控制台运行手册](../runbooks/operate-local-kiosk.md#更换屏保视频)。

屏保视频原本随 release 暂存，由系统安装器复制到 `/var/lib/a-nas/screensavers/<release>/`。不带视频的升级会让屏保静默失效（[issue #49](https://github.com/zhongwater123/A-NAS/issues/49)），所以每次部署都重新上传同一批约 674 MB 的视频，NAS 的暂存区和系统盘也为每个 release 各留一份副本。视频很少变化，与代码的生命周期无关。我们决定：屏保视频是 A-NAS 存放在系统盘上的长期媒体，有独立的暂存与安装命令，release 不再携带视频。

## 决定

1. **按内容存储**：每个视频按 SHA-256 在 `/var/lib/a-nas/screensavers/objects/` 中只存一份。视频池是 `pools/<池 ID>/` 下指向这些对象的硬链接加一份 `SHA256SUMS`，池 ID 是该清单 SHA-256 的前 16 位十六进制。`current` 链接指向当前池，产品服务固定读取它，每次请求重新读取目录。
2. **独立的媒体通道**：开发机用 `scripts/deploy-screensavers.ps1` 暂存一个池，NAS 上的暂存区同样按哈希保存视频，只上传它还没有的视频。root 用 release 中安装的 `/opt/a-nas/current/install-screensavers.sh <池 ID>` 复核每个哈希、存入对象并原子切换 `current`。同一命令指定已安装的池时直接切换，作为媒体回滚。
3. **release 不含视频**：`deploy-dev.ps1`、暂存脚本与系统安装器不再处理视频，升级和产品回滚都不改变 `current`。系统安装器只在首次升级、尚无 `current` 时，把旧安装器留下的最近一个池以硬链接导入对象库。
4. **放在系统盘**：视频位于 `/var/lib/a-nas`，与产品的长期状态放在一起，而不是数据卷。

## 后果

- 日常部署不传输视频。只有新增的视频需要上传一次，同一视频出现在多个池中也不占用额外空间。
- 更换或回滚视频不需要发布新版本，也不重启产品服务；脚本只重启本地控制台页面，让它重新读取清单。
- 重装系统盘会丢失视频，需要用 `deploy-screensavers.ps1` 重新上传一次。
- 暂存区与系统库各保存一份视频：root 在复核时复制而不是链接暂存文件，这样暂存账号无法改动已经校验过的内容。
- 对象不会被自动删除。清理不再被任何池引用的对象和旧的按 release 存放的池，需要另行确认。

## 未选择

- 随 release 携带视频，系统安装器在不带视频时沿用当前池：升级不再失效，但更换视频仍要发布新版本，同一视频仍会重复上传和存储。
- 放在数据卷：屏保应当在数据卷尚未初始化或离线时也能工作；v1.0.1 的数据卷只存可丢弃测试数据，可能被重新格式化；产品服务账号也被刻意禁止访问数据卷（[ADR 0008](0008-use-unified-linux-identities-and-filesystem-acls.md)）。
- 在 Web 中管理视频池：规格把视频池设置界面列为非目标，而且上传需要新的产品 API 与权限设计。

## 关联

- [本地控制台屏保规格](../specs/local-console-screensaver.md)
- [本地控制台运行手册](../runbooks/operate-local-kiosk.md)
- [ADR 0005：本地控制台使用单应用 Wayland Kiosk](0005-use-a-single-application-wayland-kiosk-for-the-local-console.md)
- [`install-screensavers.sh`](../../scripts/install-screensavers.sh)、[`remote-stage-screensavers.sh`](../../scripts/remote-stage-screensavers.sh)、[`deploy-screensavers.ps1`](../../scripts/deploy-screensavers.ps1)
- [系统测试](../../scripts/system-test.sh)
