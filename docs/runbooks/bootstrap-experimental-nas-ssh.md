# 配置实验 NAS 的 SSH 开发账号

状态：verified
更新时间：2026-10-06

## 目的

在全新安装的 Debian 实验 NAS 上创建无 `sudo` 权限的 `anas-dev`，并从 Windows 使用项目专用 ED25519 密钥登录。成功后仍保留原有管理员与密码登录方式；本手册不包含 SSH 加固或磁盘配置。

## 前提与风险

- 权限：Windows 本地用户、Debian 的现有管理员账号，以及可通过 `su -` 获得的 root 权限。
- 目标：从本机控制台确认的实验 NAS；IP 地址使用运行时发现值，不写入仓库。
- 风险：错误覆盖其他账号的 `authorized_keys` 会中断其登录；私钥不得复制到 NAS、仓库或聊天记录。
- 停止条件：SSH 主机指纹与控制台不一致、`anas-dev` 已存在但 home 或 UID 来源不明、目标主机或系统盘身份不明确。

## 步骤

1. 在 Debian 控制台采集只读基线。

   ```bash
   hostnamectl
   ip -br address
   systemctl is-active ssh
   findmnt /
   lsblk -e 7 -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINTS,MODEL,TRAN
   ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
   ```

   - 预期：Debian 13、SSH 为 `active`、根文件系统位于预期系统盘。
   - 完成标准：Windows 首次连接显示的 ED25519 指纹与控制台输出一致。

2. 以 root 创建非特权账号。

   ```bash
   getent passwd anas-dev
   adduser --disabled-password --gecos "" anas-dev
   id anas-dev
   ```

   - 如果 `getent` 已有输出，先核对 home、UID 和来源，不重复创建。
   - 完成标准：home 为 `/home/anas-dev`，账号不属于 `sudo` 或其他特权组。

3. 在 Windows PowerShell 创建项目专用密钥。

   ```powershell
   $keyPath = Join-Path $env:USERPROFILE ".ssh\a-nas-dev_ed25519"
   Test-Path -LiteralPath $keyPath
   ssh-keygen -t ed25519 -a 64 -f $keyPath -C "anas-dev@a-nas-dev"
   ```

   - 已存在时停止，不覆盖原密钥。
   - 私钥可设置口令，只上传 `$keyPath.pub`。

4. 通过现有管理员账号暂存公钥。

   ```powershell
   scp "$keyPath.pub" <ADMIN_USER>@<NAS_IP>:/tmp/anas-dev.pub
   ```

5. 以 root 安装公钥并核对权限。

   ```bash
   install -d -m 700 -o anas-dev -g anas-dev /home/anas-dev/.ssh
   tr -d '\r' < /tmp/anas-dev.pub > /home/anas-dev/.ssh/authorized_keys
   chown anas-dev:anas-dev /home/anas-dev/.ssh/authorized_keys
   chmod 600 /home/anas-dev/.ssh/authorized_keys
   stat -c '%U:%G %a %n' /home/anas-dev /home/anas-dev/.ssh /home/anas-dev/.ssh/authorized_keys
   ssh-keygen -lf /tmp/anas-dev.pub
   ssh-keygen -lf /home/anas-dev/.ssh/authorized_keys
   ```

   - 预期：`.ssh` 为 `700`，`authorized_keys` 为 `600`，两个公钥指纹一致。
   - 完成标准：权限和指纹全部符合预期。

6. 在 Windows 强制仅使用该公钥验证。

   ```powershell
   ssh -o IdentitiesOnly=yes -o PreferredAuthentications=publickey -o PasswordAuthentication=no -i "$keyPath" anas-dev@<NAS_IP>
   ```

   ```bash
   whoami
   id
   pwd
   groups
   ```

   - 预期：用户为 `anas-dev`，home 为 `/home/anas-dev`，不属于特权组。
   - 完成标准：只询问本地私钥口令，不询问 Debian 用户密码。

7. 成功后由 root 清理暂存文件。

   ```bash
   rm -f /tmp/anas-dev.pub
   ```

8. 可选：在 `%USERPROFILE%\.ssh\config` 添加本地别名。若 DHCP 地址变化，只更新 `HostName`。

   ```sshconfig
   Host a-nas-dev
       HostName <NAS_IP>
       User anas-dev
       IdentityFile ~/.ssh/a-nas-dev_ed25519
       IdentitiesOnly yes
   ```

## 限时自动化维护窗口

Windows 内置 OpenSSH 的 `ssh-agent` 可能拒绝 `ssh-add -t <seconds>` 的生命周期约束，并返回 `agent refused operation`。先确认代理服务已启动，再不带 `-t` 加载密钥：

```powershell
$keyPath = Join-Path $env:USERPROFILE ".ssh\a-nas-dev_ed25519"
ssh-add "$keyPath"
ssh-add -l
```

若维护窗口必须到期，可在 Debian 的对应 `authorized_keys` 行前设置 UTC 到期时间，并关闭与部署无关的转发能力：

```text
expiry-time="<YYYYMMDDHHMMSSZ>",no-agent-forwarding,no-port-forwarding,no-X11-forwarding ssh-ed25519 <PUBLIC_KEY> <COMMENT>
```

- 修改前使用 `cp -p` 创建带 UTC 时间戳的备份，并保持 `authorized_keys` 为 `600`。
- 只在 `timedatectl` 显示 NTP 已同步、目标公钥指纹和条目数量均已确认时修改。
- 修改后必须建立一条全新的 SSH 连接验证，而不能仅依赖修改前已建立的连接。
- 该到期时间能阻止文件保持不变时的后续认证，但 `authorized_keys` 当前由 `anas-dev` 所有，因此它不是抵抗该账号主动篡改的强安全边界。若需要不可自行延长的授权，应由 root 使用独立的系统级授权文件管理公钥。
- 活动窗口的精确到期时间、目标地址和备份文件名属于操作态信息，不提交到远端仓库。
- 窗口结束后仍应从 Windows 代理中删除项目密钥：

  ```powershell
  ssh-add -d "$keyPath"
  ```

## 验证

- `ssh a-nas-dev` 可以登录，且 `id` 不包含 `sudo`。
- `ssh-keygen -lf %USERPROFILE%\.ssh\a-nas-dev_ed25519.pub` 与服务器 `authorized_keys` 的指纹一致。
- `systemctl is-active ssh` 仍返回 `active`。
- 限时授权场景中，`date -u`、`timedatectl show -p NTPSynchronized` 和 `authorized_keys` 的 `expiry-time` 均与批准的维护窗口一致。

## 回滚或恢复

- 密钥错误但管理员会话仍可用：移动 `/home/anas-dev/.ssh/authorized_keys` 到 `authorized_keys.disabled`，保留证据后重新安装正确公钥。
- 私钥疑似泄露：先从 `authorized_keys` 删除对应公钥，再生成新密钥；不要只删除本地私钥。
- 只有确认 `/home/anas-dev` 不含任何需要保留的数据时，才考虑删除账号和 home；本手册不自动执行该破坏性操作。
- 本流程没有修改 `/etc/ssh/sshd_config`，因此不需要回滚 SSH 全局配置。

## 关联

- 规格：不涉及
- ADR：不涉及
- 调查：不涉及
- 当前状态：[CURRENT.md](../status/CURRENT.md)
- 本地环境：[LOCAL_ENVIRONMENT.md](../development/LOCAL_ENVIRONMENT.md)
