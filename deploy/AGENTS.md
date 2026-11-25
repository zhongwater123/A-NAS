# 部署与软件包（deploy/）

本目录定义装到宿主机上的 Debian 软件包、systemd 单元、本机设置（`site/`）与设备镜像（`image/`）。根目录的 [AGENTS.md](../AGENTS.md) 仍然适用。权威说明：[ADR 0034](../docs/adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)、[ADR 0031](../docs/adr/0031-declare-host-dependencies-in-debian-packages.md)、[部署 A-NAS 软件包](../docs/runbooks/deploy-a-nas-packages.md)。

- **结果不靠维护脚本：** 文件只放 `/usr`，单元用包内的 `.wants` 链接启用，账号与目录用 `sysusers.d`、`tmpfiles.d`；随机器而定的配置由 `site/a-nas-generator.sh` 与 `anas-site.service` 按 `/etc/a-nas/site.conf` 生成，包不改写它。同样的文件要能做成 system extension，systemd-boot 退回时不运行任何脚本。
- **维护脚本有两个调用方：** `a-nas` 的 `preinst`、`postinst` 也装到 `/usr/lib/a-nas/release/`，镜像设备的更新服务在 dpkg 之外调用。`preinst` 停下的服务两条路径都要重新启动；装新底座时没人启动它们，见 [#524](https://github.com/zhongwater123/A-NAS/issues/524) 的 M2。
- **回滚时运行新包的 `prerm`：** dpkg 运行被换下版本的 `prerm`、换上版本的 `postinst`，旧版本不认识新单元，所以新增的服务在 `prerm upgrade` 中停下（见 `debian/a-nas/DEBIAN/prerm` 开头）。要跨回滚保留的保护放进不随产品降级的包（[调查](../docs/investigations/2026-11-12-docker-policy-survives-product-rollback.md)）。系统测试的“上一 release”与本 release 布局相同，改了包的文件布局要用真实的旧包试一次回滚。
- **数据库：** 新增写数据库的服务或数据卷上的库，加进 `preinst` 的停止与复制清单和手册的恢复步骤；结构变化都带反向步骤（[`internal/sqlitedb`](../internal/sqlitedb/migrate.go)）。
- **就绪脚本：** 主进程退出后 systemd 仍等 `ExecStartPost`，脚本要看 `$MAINPID`，但套接字拉起的单元别立即失败（排队的连接会连环触发到套接字失败）；`PrivatePIDs=` 下每个进程，含测试替身，都要用 `tini` 或 `trap` 处理 SIGTERM（[调查](../docs/investigations/2026-11-25-readiness-scripts-wait-out-timeouts.md)）。
- **依赖：** 新的 Debian 依赖写进 `a-nas-host` 的 `Depends`（安装带 `--no-install-recommends`，`Recommends` 不生效）；系统测试镜像随之重建，镜像设备要先发新的基础镜像。
- **验证：** `make ops-check` 用 grep 钉住单元的约定行，例如 Host Agent 不写 `User=`，否则丢掉 `CAP_SETUID`（[调查](../docs/investigations/2026-10-08-host-agent-loses-setuid-under-systemd.md)）。沙箱效果与 `systemd-analyze verify` 只有系统测试查得到：改了单元、维护脚本或包定义，交部署前按 [system-test](../.agents/skills/system-test/SKILL.md) 跑一次。
