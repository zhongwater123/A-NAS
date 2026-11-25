# 管理发布签名密钥

状态：draft（2026-11-12 用一次性密钥在容器中验证了全部流程，见“验证”；正式密钥尚未生成）
更新时间：2026-11-12

## 目的

A-NAS 的软件源由 A-NAS 存档密钥签名，设备只信任这把密钥（[决定记录](../decisions/2026-11-11-release-signing-key.md)）。本手册用于：

- 第一次生成密钥，并在开发机 B（`D:\A-NAS`）与开发机 A（`E:\A-NAS`）上装好各自的签名子密钥；
- 每次发布时签名；
- 换另一台开发机接着发布；
- 增加、吊销子密钥，以及一台开发机丢失或泄露时的处理。

成功结果：`scripts/release-key.sh check` 在两台开发机上各打印本机的子密钥，`make release` 用它签名，设备从软件源装上同一把公钥。

## 前提与风险

- 权限：开发机的 WSL 账号，能运行 Docker 与系统测试的镜像（[`build-system-test-image.sh`](../../scripts/build-system-test-image.sh)）；检出中有本手册对应的脚本。GnuPG 只在镜像中运行，开发机不用安装。
- 目标：
  - 公钥文件 `deploy/release/a-nas-archive-key.asc`，在仓库中，`a-nas-archive-keyring` 由它构建；
  - 每台开发机的签名目录 `~/.local/share/a-nas/release-key`，只有本机的子密钥；
  - 主密钥的备份目录 `~/.local/share/a-nas/archive-key-backup`，两台开发机各一份，另建议在 U 盘上存一份。
- 密钥结构：主密钥只签发与吊销子密钥，不签软件源；开发机 B 的子密钥叫 `dev-b`，开发机 A 的叫 `dev-a`。都不设有效期。
- 风险：
  - 主密钥的私钥连同口令泄露：别人能签出任何设备都接受的软件源，只能换一把新密钥，所有设备重新做[部署手册](deploy-a-nas-packages.md)第 4 步。
  - 主密钥的全部备份丢失或忘记口令：不能再吊销或增加子密钥，处理同上。
  - 一台开发机的子密钥泄露：用主密钥吊销它（见下文），设备随下一次升级收到新的 keyring。
- 停止条件：
  - `release-key.sh check` 说签名目录里没有存档密钥的有效子密钥；
  - 公钥文件的指纹与 `release-key.sh show`、决定记录或 PR 中核对过的不一致；
  - 密钥文件的来源说不清。

## 步骤

### 第一次：生成密钥（开发机 B）

1. 在自己的终端里生成密钥。口令由自己输入两次，至少 12 个字符，记进密码管理器；不要让别人或 AI 代为输入。
   ```bash
   scripts/release-key.sh create ~/a-nas-archive-key dev-b dev-a
   ```
   - 预期：打印 `primary <指纹>`、`dev-b <指纹>`、`dev-a <指纹>`；写出 `deploy/release/a-nas-archive-key.asc`；`~/a-nas-archive-key` 中有 `primary-secret.asc`、`revocation.asc`、`signing-dev-b.asc`、`signing-dev-a.asc` 与 `subkeys.txt`。每个私钥都在口令之下。
   - 完成标准：`scripts/release-key.sh show` 列出主密钥与两把 `good` 的子密钥。
2. 装上开发机 B 的子密钥，再把私钥移进备份目录。
   ```bash
   scripts/release-key.sh install ~/a-nas-archive-key/signing-dev-b.asc
   install -d -m 0700 ~/.local/share/a-nas/archive-key-backup
   mv ~/a-nas-archive-key/* ~/.local/share/a-nas/archive-key-backup/
   rmdir ~/a-nas-archive-key
   ```
   - `install` 先用口令在临时目录中试签一次，口令不对就停下，不写签名目录。
   - 完成标准：`scripts/release-key.sh check` 打印 `dev-b` 的指纹（与 `subkeys.txt` 一致）。
3. 把整个备份目录复制到开发机 A 的同一位置，并在 U 盘上另存一份 `primary-secret.asc` 与 `revocation.asc`，离线保管。这些文件都在口令之下；口令本身不随文件传递。
4. 提交公钥：`git add deploy/release/a-nas-archive-key.asc`，开 PR。评审时核对 PR 中的主密钥指纹与第 1 步打印的一致，再把指纹写进[决定记录](../decisions/2026-11-11-release-signing-key.md)。

### 第一次：开发机 A

5. 合并后在开发机 A 的检出中：
   ```bash
   scripts/release-key.sh install ~/.local/share/a-nas/archive-key-backup/signing-dev-a.asc
   ```
   - 完成标准：`scripts/release-key.sh check` 打印 `dev-a` 的指纹。

### 每次发布

6. 按[部署手册](deploy-a-nas-packages.md)第 1 步运行 `make release`。它先核对门禁的记录，再核对签名目录的子密钥在公钥文件中有效，然后要口令，并先试签一次；口令不对时在构建任何东西之前停下。
   - 没有终端时（例如后台任务），用 `ANAS_RELEASE_PASSPHRASE_FILE` 指向一个只有本人能读的文件，用完立即删除；不要放进仓库或共享目录。
   - 最后一行打印 `signing key: <主密钥指纹>, subkey <子密钥指纹>` 与 keyring 的 SHA-256。
   - `make release` 不会用另一把密钥签一个已有的软件源。测试密钥签的软件源（例如 2026-11-12 从 main 构建的 `D:\A-NAS-releases\apt`）不能再用存档密钥续签：删掉它与旁边的 `.apt.aptly` 后在原处重新发布，或换一个新目录。已经装过测试 keyring 的机器重新做部署手册第 4 步。

### 换另一台开发机发布

7. 把软件源目录（`dists` 与 `pool`）原样复制到另一台开发机，不需要旁边的 `.<目录名>.aptly`。在那台机器上照常 `make release REPO=<副本>`。
   - 预期：输出 `dev: took <N> package versions from the published repository`：它把已发布的各个版本接进本机的状态，签名后旧版本都还在。
   - 软件源放在哪里定下之前，同一时间只由一台开发机发布；换回来时同样先把软件源复制回去。

### 增加子密钥

8. 例如第三台开发机 `dev-c`，在存有主密钥备份的开发机上：
   ```bash
   scripts/release-key.sh add-subkey ~/.local/share/a-nas/archive-key-backup/primary-secret.asc ~/a-nas-new-subkey dev-c
   ```
   - 它更新公钥文件，并写出 `~/a-nas-new-subkey/signing-dev-c.asc`。提交公钥并合并后，发布一次；新机器按第 5 步装上它的子密钥。

### 吊销子密钥

9. 一台开发机丢失、被入侵，或子密钥可能泄露时，在另一台开发机上：
   ```bash
   scripts/release-key.sh revoke-subkey ~/.local/share/a-nas/archive-key-backup/primary-secret.asc <子密钥指纹> compromised
   ```
   - 子密钥指纹见备份目录的 `subkeys.txt`。`compromised` 让它以前签的东西也失效；正常退役的机器用 `retired`。
   - 提交公钥并合并后，在这台开发机上发布一次：软件源全部用本机的子密钥重新签名，新的 `a-nas-archive-keyring` 不再信任被吊销的子密钥。设备按部署手册第 6 步升级时随 `a-nas-host` 装上它（`a-nas-host` 依赖本版本或更新的 keyring）。
   - 丢失的机器上还有主密钥的备份时，它与口令一起泄露的后果见“风险”。

## 验证

- `scripts/release-key.sh show`：主密钥与各子密钥的指纹与状态（`good` 或 `revoked`）。
- `scripts/release-key.sh check`：本机签名用的子密钥。
- `scripts/apt-repository.sh signed-by <软件源> deploy/release/a-nas-archive-key.asc`：软件源由存档密钥签名。
- `make release-key-test`：用一次性密钥在系统测试的镜像中走一遍上面的流程，CI 每次运行。2026-11-12 全部 31 项通过：
  - 生成；两台机器各装上子密钥；签名目录没有主密钥的私钥；口令不对时 `install` 停下；
  - 第一台签的软件源 apt 接受；第二台复制后接着发布，旧版本都在，apt 接受；测试密钥被告知软件源不是它签的；
  - 吊销第一台的子密钥：它不能再通过 `check`；仍用旧 keyring 的设备接受第二台的签名并装上新 keyring；装上新 keyring 后，apt 拒绝被吊销的子密钥签的软件源（`signing key is revoked`）；
  - 增加第三台的子密钥并用它发布，吊销仍然有效。

## 回滚或恢复

- 签名目录损坏：清空 `~/.local/share/a-nas/release-key`，用备份目录中本机的 `signing-<机器>.asc` 重新执行第 2 步或第 5 步的 `install`。
- 吊销不能撤回：误吊销了一把子密钥，就按第 8 步给那台机器加一把新的。
- 主密钥泄露、全部备份丢失或忘记口令：删除公钥文件，按第 1 步生成新的存档密钥并提交；之后的 release 写进新的软件源，所有设备重新做部署手册第 4 步。

## 关联

- 决定记录：[发布用的签名密钥由谁、怎样保管](../decisions/2026-11-11-release-signing-key.md)、[软件源与更新文件放在哪里](../decisions/2026-11-11-update-hosting.md)
- ADR：[0034](../adr/0034-release-as-signed-debian-packages-and-update-devices-with-sysupdate.md)
- 运行手册：[部署 A-NAS 软件包](deploy-a-nas-packages.md)
- 代码：[`release-key.sh`](../../scripts/release-key.sh)、[`test-release-key.sh`](../../scripts/test-release-key.sh)、[`apt-repository.sh`](../../scripts/apt-repository.sh)、[`release.sh`](../../scripts/release.sh)、[`lib/signing.sh`](../../scripts/lib/signing.sh)
- 调查：不涉及
