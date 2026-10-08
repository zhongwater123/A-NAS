# 验收相册 M1（切片 7 实机闸门）

状态：draft（未在 Experimental NAS 执行）
更新时间：2026-10-08

## 目的

在 Experimental NAS 上完成[相册技术设计](../architecture/photo-library.md)切片 7 的实机闸门：用户功能、强制终止、断电、规模与缩略图积压。本地已覆盖的部分（磁盘故障、懒卸载、上传中途强制终止、导入中途卷满、开发机规模基线）见技术设计的切片 7 实现要点；本手册只做必须在真实硬件上完成的部分。全部通过后，把结果写入“证据”并更新[当前状态](../status/CURRENT.md)，M1 即可发布。

## 前提与风险

- **权限**：管理员 root 会话（`su -`）。开发机经 `ssh a-nas` 只做只读检查和复制测试程序；浏览器中使用管理员与一名测试成员，账号由管理员在设备上创建，密码不写入本手册、命令或日志。
- **目标**：
  - 主机名 `a-nas-dev`，使用前按 [SSH 运行手册](bootstrap-experimental-nas-ssh.md) 核对指纹。
  - 数据卷 UUID 与[当前状态](../status/CURRENT.md)记录一致，并已挂载到 `/srv/a-nas/data`。
  - 运行中的 release 已按[启用相册服务](enable-photo-service.md)部署，且包含运行期数据卷监视（`grep -a 'photo store lost' /opt/a-nas/current/anas-api` 有输出）。
- **真实磁盘写入**：
  - 测试照片写入 `photos` 子卷。
  - 规模测试在数据卷上创建临时目录 `/srv/a-nas/data/.photo-scale`，结束后删除；它不触碰 `photos` 与任何空间。
  - 断电可能丢失断电前几秒的写入；数据卷只含可丢弃的测试数据。
- **风险**：强制终止与断电期间相册、文件、SMB 与本地控制台中断；断电是对整机的冲击，只在没有其他实验进行时执行。
- **停止条件**（出现任一项立即停止，保留现场）：
  - 主机、指纹或数据卷 UUID 与记录不符。
  - `btrfs device stats /srv/a-nas/data` 出现非零计数。
  - 断电后数据卷无法挂载，或 `anas-photos` 30 秒内没有记录 `photo service ready`。
  - `photo store reconciled` 报告的 `MissingObjects` 不为 0。

## 步骤

1. 记录起点。
   - 命令：`systemctl is-active anas-host-agent anas-photos anas-api smbd`、`btrfs device stats /srv/a-nas/data`、`journalctl -u anas-photos -b --no-pager | tail -5`
   - 完成标准：服务全部 `active`，设备错误计数全为 0，相册日志最后一条为 `photo service ready`。
2. 用户功能。按[启用相册服务](enable-photo-service.md#验证)“功能”的 1–7 项操作。
   - 完成标准：七项全部符合预期；任何一项不符即停止并开调查。
3. 强制终止。成员在桌面一次选择约 20 张手机照片上传，进度过半时 root 执行 `systemctl kill --signal=KILL anas-photos`。
   - 预期：
     - 桌面报告部分照片上传失败。
     - 约 5 秒后 systemd 自动重启相册服务（`Restart=on-failure`）。
     - `journalctl -u anas-photos -b --no-pager` 出现 `photo store reconciled`，`MissingObjects` 为 0。
   - 完成标准：
     - 时间线中的每张照片都能打开原图，缩略图随后出现。
     - 重新上传失败的照片成功。
     - `ls -A /srv/a-nas/data/photos/staging` 无输出。
4. 断电。重复步骤 3 的上传，进度过半时直接切断 NAS 电源，10 秒后上电。
   - 预期：数据卷按 UUID 挂载，步骤 1 的服务全部 `active`，相册日志出现 `photo store reconciled`。
   - 完成标准：
     - 与步骤 3 相同。
     - `btrfs device stats /srv/a-nas/data` 仍全为 0。
     - 只读校验 `btrfs scrub start -B -r /srv/a-nas/data` 报告 `no errors found`。
5. 规模与缩略图积压。
   1. 开发机：在 cgo 容器中构建测试程序 `go test -c -o build/photos.test ./internal/photos`，记录 `sha256sum build/photos.test`，再 `scp build/photos.test a-nas:photos.test`。
   2. NAS（root）：核对 SHA-256 后安装，并创建临时目录：
      - `sha256sum /home/guoyi/photos.test`
      - `install -o a-nas-photos -g a-nas-photos -m 0500 /home/guoyi/photos.test /tmp/photos.test`
      - `install -d -o a-nas-photos -g a-nas-photos -m 0700 /srv/a-nas/data/.photo-scale`
   3. 以相册服务身份在数据卷上运行，约需半小时到一小时：
      - `setpriv --reuid=a-nas-photos --regid=a-nas-photos --init-groups env ANAS_PHOTO_SCALE=1 TMPDIR=/srv/a-nas/data/.photo-scale /tmp/photos.test -test.run TestScale -test.v -test.timeout 120m | tee /root/photo-scale.log`
   - 预期：输出导入速率、各读取的中位数与最大值，以及一张 12 MP 缩略图的耗时和 20,000 张的估算积压。
   - 完成标准：
     - 测试 `PASS`，即所有读取的中位数都在 100 ms 预算内。
     - 把 `/root/photo-scale.log` 的计时行抄入“证据”。
     - 清理：`rm -rf /srv/a-nas/data/.photo-scale /tmp/photos.test`。
6. 记录结果。填写“证据”，更新[当前状态](../status/CURRENT.md)，并把本手册状态改为 verified。

## 验证

- 步骤 2–5 的完成标准全部满足。
- 结束时 `btrfs device stats /srv/a-nas/data` 全为 0，`ls -A /srv/a-nas/data` 中没有 `.photo-scale`。
- `journalctl -u anas-photos -b --no-pager` 没有 `photo media job failed` 与持续的 `photo store unavailable; waiting`。

## 回滚或恢复

- **断电后数据卷无法挂载**：停止，不运行 `btrfs check --repair`；按[实机配置手册](provision-v1.0.1-experimental-storage.md)的恢复步骤处理，并开调查。
- **规模测试中断**：删除 `/srv/a-nas/data/.photo-scale` 与 `/tmp/photos.test`；该目录只含测试临时数据。
- **清除测试照片**：在桌面相册中移到回收站并清空回收站；需要整体清除时按[启用相册服务](enable-photo-service.md#回滚或恢复)的“清除测试照片”执行。

## 证据

尚未执行。

## 关联

- 规格：[相册](../specs/photo-library.md)
- 技术设计：[切片 7](../architecture/photo-library.md#开发切片)
- ADR：[0011 相册服务身份与 Catalog 授权](../adr/0011-run-the-photo-library-as-a-dedicated-service-identity.md)
- 手册：[启用相册服务](enable-photo-service.md)
- 测试：[规模基线](../../internal/photos/scale_test.go)、[卷满导入](../../internal/photos/reserve_test.go)、[端到端冒烟](../../scripts/smoke-photo-service.sh)
- 调查：不涉及
