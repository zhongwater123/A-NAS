# 用发布包暂存 release

状态：draft（2026-11-09 在一次性容器中验证了打包与暂存；尚未在 Experimental NAS 上执行）
更新时间：2026-11-09

## 目的

开发机连不到 NAS 时，用发布包暂存 release：

- 开发机跑完门禁后，把 release 写成发布包，交出它的 `SHA256SUMS` 的哈希；
- 部署机只负责把发布包复制到 NAS，并以暂存账号运行包里的 `stage.sh`，不需要 Go、Node、WSL 或仓库；
- root 之后的核对与安装不变，按[实验存储手册](provision-v1.0.1-experimental-storage.md#安装系统服务)和这一轮随发布包给出的 root 脚本执行。

`stage.sh` 暂存的文件与 `deploy-dev.ps1 -NasHost <NAS> -StageOnly` 经 SSH 上传的相同，也运行同一个 `remote-activate-release.sh`，所以两种方式暂存出的 release 目录一样。成功结果：`/home/anas-dev/apps/a-nas/releases/<12 位提交 SHA>` 已暂存，root 脚本在 `/home/anas-dev` 下。

这是近期 release 的过渡做法。长期的发布与更新方式见 [ADR 0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)（proposed）。

## 前提与风险

- 权限：
  - 开发机：能运行门禁与 `deploy-dev.ps1` 的开发机，见[开发机暂存注意事项](provision-v1.0.1-experimental-storage.md#开发机暂存注意事项)；
  - 部署机：能以 `anas-dev` 登录 NAS（SSH 密钥，见[连接手册](bootstrap-experimental-nas-ssh.md)），带 `scp` 与 `ssh` 即可；
  - NAS 上的 root：只在暂存之后、安装时需要。
- 目标：Experimental NAS 的暂存账号 `anas-dev`。暂存不改变正在运行的服务。
- 风险：内容文件（模型与运行环境）约 4.5 GB，NAS 的系统盘要放得下暂存区与 `/opt/a-nas` 中各一份。
- 停止条件：
  - `SHA256SUMS` 的哈希与开发者给出的不一致；
  - `stage.sh` 报告文件不符或缺少内容；
  - root 核对哈希失败。

  出现任何一项都不要继续，把输出发回开发者。

## 步骤

1. 开发机：在候选提交上跑完门禁（`build/.validated-build` 与提交一致），再写发布包：

   ```powershell
   scripts/deploy-dev.ps1 -BundleDirectory D:\A-NAS-releases `
     -ExtraFile build\anas-container-agent `
     -RootScript <这一轮的 root 脚本>
   ```

   - `-ExtraFile` 放进 release 目录的文件，例如容器代理或助手试用包；`-RootScript` 是这一轮的部署与回滚脚本，按需给出。
   - 预期：发布包写在 `D:\A-NAS-releases\a-nas-<提交>`，内容文件在共用的 `D:\A-NAS-releases\content`，已有的不再复制。脚本最后打印 `SHA256SUMS SHA-256`，以及 API、Host Agent 与两个宿主机软件包的 SHA-256。
   - 完成标准：把发布包目录、需要的内容文件、`SHA256SUMS` 的哈希与 root 脚本的哈希交给部署机的操作者。哈希另走一条渠道，例如聊天消息，不与发布包放在一起。
2. 部署机：把发布包复制到 NAS 的 `~/incoming`：

   ```bash
   ssh anas-dev@<NAS> 'install -d -m 0750 ~/incoming/content'
   scp -r a-nas-<提交> anas-dev@<NAS>:incoming/
   ssh anas-dev@<NAS> 'sha256sum incoming/a-nas-<提交>/SHA256SUMS'
   ```

   - 完成标准：最后一行的哈希与开发者给出的一致。不一致时停止。
3. 部署机：查出 NAS 缺哪些内容文件：

   ```bash
   ssh anas-dev@<NAS> 'bash incoming/a-nas-<提交>/stage.sh --missing'
   ```

   - 预期：每行一个路径，例如 `models/<哈希>/<文件>`。NAS 已有的内容不列出，所以平常只列出变化了的，第一次会列出全部。
   - 把列出的文件按原路径复制到 `incoming/content` 下，例如：

     ```bash
     ssh anas-dev@<NAS> 'install -d -m 0750 ~/incoming/content/models/<哈希>'
     scp content/models/<哈希>/<文件> anas-dev@<NAS>:incoming/content/models/<哈希>/
     ```

   - 完成标准：再次运行 `--missing` 没有输出。
4. 部署机：暂存：

   ```bash
   ssh anas-dev@<NAS> 'bash incoming/a-nas-<提交>/stage.sh'
   ```

   - 预期：`stage.sh` 先按 `SHA256SUMS` 核对整个发布包。它把内容文件核对后放进暂存区，再暂存 release、放入额外文件与 root 脚本。最后打印 `Staged A-NAS <版本> (<提交>) in …`。
   - 完成标准：命令以 0 结束。
5. NAS 上的 root：按开发者给出的命令核对 root 脚本的哈希并运行。与开发机经 SSH 暂存时相同，日志写在 `/var/tmp/a-nas-deploy-<提交>.log`。
6. 部署机：把 root 脚本的日志（以及要求重启后检查时的日志）复制回来，交给开发者核对：

   ```bash
   scp anas-dev@<NAS>:/var/tmp/a-nas-deploy-<提交>.log .
   ```

## 验证

- `ls ~/apps/a-nas/releases/<提交>` 中有 `anas-api`、`anas-host-agent`、`install-v1.0.1-system-services.sh`、`a-nas-host.deb` 与 `RELEASE`；`ai/` 与 `vectors/` 中的链接指向 `~/apps/a-nas` 下的内容文件。
- `stage.sh --missing` 没有输出。
- root 的部署日志全部为 OK，开发者核对后确认。

## 回滚或恢复

- 暂存不改变正在运行的服务：失败时删除 `~/apps/a-nas/releases/<提交>` 与 `~/incoming/a-nas-<提交>` 后，从第 2 步重来。暂存区中已经核对过的内容文件可以留着。
- 安装之后的回滚按[实验存储手册的回滚](provision-v1.0.1-experimental-storage.md#回滚)与这一轮的回滚脚本执行。
- 安装完成后删除 `~/incoming/a-nas-<提交>`。`~/incoming/content` 中已经暂存过的内容也可以删除，下次 `--missing` 只会列出 NAS 没有的。

## 关联

- 规格：不涉及
- ADR：[0031](../adr/0031-declare-host-dependencies-in-debian-packages.md)（宿主机软件包随 release 暂存）、[0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)（取代本手册的长期做法）
- 调查：不涉及
- 实现：[`scripts/deploy-dev.ps1`](../../scripts/deploy-dev.ps1)（`-BundleDirectory`）、[`scripts/stage-release-bundle.sh`](../../scripts/stage-release-bundle.sh)、[`scripts/remote-activate-release.sh`](../../scripts/remote-activate-release.sh)
- Issue：[#189](https://github.com/zhongwater123/A-NAS/issues/189)（发布不是一个可校验的整体）、[#171](https://github.com/zhongwater123/A-NAS/issues/171)（发布信任链）
