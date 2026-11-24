# 为开发者开、撤实验 NAS 的 SSH 账号

状态：draft
更新时间：2026-11-23

## 目的

给实习生等开发者开一个只能用密钥登录的个人账号，用于在实验 NAS 宿主机上只读排查、看服务日志，以及在共享目录中跑模型与评测；人走时撤销。做法与理由见[决定记录](../decisions/2026-11-23-developer-ssh-accounts.md)，开发者那一侧的步骤见[接入说明](../development/experimental-nas-access.md)。

所有写操作都由 [`scripts/nas-dev-account.sh`](../../scripts/nas-dev-account.sh) 完成，以 root 运行：

| 命令 | 作用 |
|---|---|
| `setup` | 建组 `anas-devs`、`anas-eval`，建密钥目录 `/etc/ssh/authorized_keys` 与共享目录 `/srv/eval`，写入 sshd 规则并重载；可重复运行 |
| `add <账号> <公钥文件>` | 开 `dev-<缩写>` 账号，或给已有的开发者账号再加一把钥匙 |
| `list` | 列出开发者账号、钥匙指纹、到期状态与最近登录 |
| `revoke <账号>` | 移走钥匙、让账号过期并结束其会话；home 保留 |

开发者账号得到的权限：

| 项目 | 做法 |
|---|---|
| 登录 | 只认 root 名下 `/etc/ssh/authorized_keys/<账号>` 中的钥匙；账号自己的 `~/.ssh/authorized_keys` 不起作用，所以只有 root 能加、撤钥匙。没有可用的密码，sshd 对 `anas-devs` 只提供公钥认证 |
| 转发 | 只允许本地端口转发（`ssh -L`）；不允许远程转发、代理转发、X11 与隧道 |
| 组 | `anas-devs`（sshd 规则）、`systemd-journal`（读全部系统日志）、`anas-eval`（读写 `/srv/eval`）；不进 `sudo`、`docker`、`adm` |
| 数据 | 成员数据在 `/srv/a-nas`（其他用户 `---`），产品状态在 `/var/lib/a-nas`（`700`），开发者账号都读不到 |
| UID | 1000–1999 中第一个空闲的号，账号私有组同号；不进 A-NAS 预留的 20000 以上区间（[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)） |
| 资源 | 每个账号的 `user-<UID>.slice` 设 `MemoryHigh=6G`、`MemoryMax=8G`、`CPUWeight=20`、`IOWeight=20`。内存是兜底：超过 6G 被限速回收，超过 8G 被杀，免得系统服务被挤进交换区。权重只在 `user.slice` 内起作用，让本地控制台优先于开发者会话；`user.slice` 与系统服务之间仍按同等权重分 CPU |
| 到期 | 不设到期；人走时 `revoke` |

## 前提与风险

- 权限：设备所有者在 NAS 本机控制台或已有会话中 `su -` 得到的 root；开发机能以 `anas-dev` 登录 NAS 暂存文件（[连接手册](bootstrap-experimental-nas-ssh.md)）。
- 目标：主机名为 `a-nas-dev` 的实验 NAS；地址是 DHCP 分配的，用前确认，不写入仓库。
- 风险：
  - sshd 规则写错会影响新登录。脚本先用 `sshd -t` 校验，不通过就恢复原文件、不重载；重载不断开已有会话。
  - 加入 `systemd-journal` 后能看到全部系统日志，其中有成员账号名、文件路径与登录来源。
  - 开发者在 `/srv/eval` 中跑的任务与线上服务共用 CPU、内存与系统盘。
- 停止条件：
  - `hostnamectl --static` 不是 `a-nas-dev`；
  - 收到的公钥不是开发者本人当面或经可信渠道交来的，或指纹与开发者自己报的不一致；
  - 要开的账号名已被非开发者账号占用（脚本会拒绝，不要改名绕过前先弄清是谁的）；
  - `setup` 报 `sshd rejected the new rules`。

## 步骤

### 暂存脚本

每次用到新版本的脚本时做一次。

1. 在开发机上从当前 `main` 取脚本，以 `anas-dev` 传到 NAS，记下 SHA-256：

   ```powershell
   git fetch origin
   git show origin/main:scripts/nas-dev-account.sh | ssh a-nas-dev 'cat > ~/nas-dev-account.sh'
   ssh a-nas-dev 'sha256sum ~/nas-dev-account.sh'
   ```

   - 完成标准：开发机上 `git show origin/main:scripts/nas-dev-account.sh | sha256sum` 与 NAS 上的输出一致。

2. 在 NAS 上以 root 安装并核对（`<SHA256>` 换成上一步的值）：

   ```bash
   install -m 0700 ~anas-dev/nas-dev-account.sh /root/nas-dev-account.sh
   echo '<SHA256>  /root/nas-dev-account.sh' | sha256sum --check
   ```

   - 完成标准：输出 `/root/nas-dev-account.sh: OK`。

### 一次性准备

3. 以 root 运行 `setup`，日志留给开发机读：

   ```bash
   bash /root/nas-dev-account.sh setup 2>&1 | tee /tmp/nas-dev-account.log; chmod 644 /tmp/nas-dev-account.log
   ```

   - 预期：以 `== setup done` 结束；`/srv/eval` 为 `drwxrws--- root:anas-eval`。
   - 完成标准：从另一台机器新开一个 `ssh a-nas` 会话仍能登录。

### 开一个账号

4. 开发者按[接入说明](../development/experimental-nas-access.md)生成钥匙，把 `.pub` 文件和自己电脑上看到的指纹交给设备所有者。所有者核对指纹后，以 `anas-dev` 把文件传到 NAS：

   ```powershell
   scp <文件>.pub a-nas-dev:keys/dev-<缩写>.pub
   ```

   - 账号名取 `dev-` 加 2–16 个小写字母或数字，例如 `dev-zhangs`。
   - 一个文件只放一把钥匙；接受 Ed25519、ECDSA 与不少于 3072 位的 RSA，带选项的钥匙行不接受。

5. 以 root 开账号：

   ```bash
   bash /root/nas-dev-account.sh add dev-<缩写> ~anas-dev/keys/dev-<缩写>.pub 2>&1 | tee -a /tmp/nas-dev-account.log
   ```

   - 预期：打印 UID、`added key SHA256:…`、组 `dev-<缩写> anas-devs anas-eval systemd-journal`，以及生效的 sshd 设置中 `authorizedkeysfile /etc/ssh/authorized_keys/%u`、`authenticationmethods publickey`、`passwordauthentication no`。
   - 完成标准：打印的指纹与开发者报的一致。

6. 把 NAS 的地址与主机钥匙指纹交给开发者，指纹在 NAS 上这样看：

   ```bash
   ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
   ```

7. 删掉暂存的公钥：以 `anas-dev` 运行 `rm ~/keys/dev-<缩写>.pub`。

同一个开发者换电脑或加一台电脑：用新的 `.pub` 再运行一次第 5 步，旧钥匙不动；要换掉旧钥匙，先 `revoke` 再 `add`。

### 撤销

8. 以 root 运行：

   ```bash
   bash /root/nas-dev-account.sh revoke dev-<缩写>
   ```

   - 预期：钥匙文件移到 `/etc/ssh/authorized_keys/.revoked/`，账号到期日为 1970-01-02，会话被结束。
   - 完成标准：`list` 中该账号 `expires: Jan 02, 1970`，开发者再登录被拒。

9. 账号的 home 不自动删。确认 `/home/dev-<缩写>` 中没有要留的东西、`/srv/eval` 中该账号留下的文件已有人接手后，才由 root 运行 `userdel --remove dev-<缩写>`，再删 `/etc/systemd/system/user-<UID>.slice.d/`。这一步不可撤销。

## 验证

- `bash /root/nas-dev-account.sh list` 列出每个开发者账号、钥匙指纹与 `expires: never`。
- 开发者能 `ssh` 登录，`id` 中有 `anas-devs`、`anas-eval`、`systemd-journal`，没有 `sudo`、`docker`。
- 开发者能 `journalctl -u anas-api --since -1h`，不能 `ls /srv/a-nas/data`、`sudo -n true`。
- 一个开发者在 `/srv/eval` 中写的文件，另一个开发者能改。
- `sshd -T -C user=guoyi,host=h,addr=192.0.2.1` 仍是 `passwordauthentication yes`、`authorizedkeysfile .ssh/authorized_keys .ssh/authorized_keys2`：规则只作用于 `anas-devs`。

脚本的这些行为都在一次性的 systemd Debian 13 容器中测过：只认 root 名下的钥匙、只提供公钥认证、拒绝远程转发、能读日志、共享目录互相可写、撤销后拒绝登录、`sshd -t` 失败时恢复原文件。

## 回滚或恢复

- 撤掉全部规则：root 删除 `/etc/ssh/sshd_config.d/90-a-nas-devs.conf`，`sshd -t` 通过后 `systemctl reload ssh`。之后开发者账号回到默认规则：认自己 `~/.ssh/authorized_keys` 中的钥匙，而那里是空的，所以仍然登录不了。
- `setup` 改写已有规则时，旧文件留在同目录的 `90-a-nas-devs.conf.<时间>.bak`；`.bak` 不以 `.conf` 结尾，sshd 不读。
- 某把钥匙疑似泄露：`revoke` 该账号，让开发者生成新钥匙，再 `add`。
- 误撤销：`add` 同一账号与钥匙即恢复，它会清掉到期日。

## 关联

- 脚本：[`scripts/nas-dev-account.sh`](../../scripts/nas-dev-account.sh)
- 决定：[实习生的 SSH 账号](../decisions/2026-11-23-developer-ssh-accounts.md)
- 开发者一侧：[用开发者账号连接实验 NAS](../development/experimental-nas-access.md)
- 已有的开发账号 `guoyi`、`anas-dev`：[配置实验 NAS 的 SSH 开发账号](bootstrap-experimental-nas-ssh.md)
- 规格：不涉及
- ADR：[ADR 0008](../adr/0008-use-unified-linux-identities-and-filesystem-acls.md)（A-NAS 的 UID 区间）
- 调查：不涉及
