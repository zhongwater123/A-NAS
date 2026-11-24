# 用开发者账号连接实验 NAS

更新时间：2026-11-23

本页写给拿到实验 NAS 开发者账号（`dev-<缩写>`）的人。账号由设备所有者按[运行手册](../runbooks/manage-developer-ssh-accounts.md)开设和撤销。

## 能做和不能做的事

能做：

- 只读排查：进程、资源、网络、已安装的软件包与其文件（`dpkg -L a-nas`）。
- 看服务日志：例如 `journalctl -u anas-api --since -1h`、`journalctl -u 'anas-*' -f`。
- 在共享目录 `/srv/eval` 中放模型、数据集，跑评测；home 中的 `~/eval` 指向它。
- 用本地端口转发访问只监听在 NAS 本机的服务：`ssh -N -L <本机端口>:127.0.0.1:<NAS 端口> a-nas`。

不能做：

- 没有 `sudo`、`docker`；需要 root 的操作写成脚本交给设备所有者，核对后由设备所有者运行。
- 读不到成员的文件，也读不到产品的数据库与配置（`/srv/a-nas`、`/var/lib/a-nas`、`/etc/a-nas`）。
- 不安装、不升级 A-NAS 软件包；部署由设备所有者按[部署手册](../runbooks/deploy-a-nas-packages.md)做。
- 不能自己加钥匙：账号只认设备所有者装在 root 名下的钥匙，`~/.ssh/authorized_keys` 不起作用。换电脑或加电脑时，把新的公钥交给设备所有者。
- 不能用密码登录，不能远程转发、代理转发或 X11 转发。

## 第一次连接

1. 在自己的电脑上生成钥匙，设一个口令。私钥只留在这台电脑上，不要复制到 NAS、仓库或聊天记录中。

   Windows PowerShell：

   ```powershell
   ssh-keygen -t ed25519 -a 64 -f "$env:USERPROFILE\.ssh\a-nas_ed25519" -C "<你的名字>@<电脑名>"
   ssh-keygen -lf "$env:USERPROFILE\.ssh\a-nas_ed25519.pub"
   ```

   macOS 或 Linux：

   ```bash
   ssh-keygen -t ed25519 -a 64 -f ~/.ssh/a-nas_ed25519 -C "<你的名字>@<电脑名>"
   ssh-keygen -lf ~/.ssh/a-nas_ed25519.pub
   ```

2. 把 `a-nas_ed25519.pub` 文件和第二条命令输出的指纹（`SHA256:` 开头）交给设备所有者。只交 `.pub` 文件。

3. 设备所有者开好账号后，会告诉你账号名、NAS 的地址与 NAS 主机钥匙的指纹。在 SSH 配置文件（Windows 是 `%USERPROFILE%\.ssh\config`，macOS 与 Linux 是 `~/.ssh/config`）中加入：

   ```sshconfig
   Host a-nas
       HostName <NAS 地址>
       User dev-<缩写>
       IdentityFile ~/.ssh/a-nas_ed25519
       IdentitiesOnly yes
   ```

   NAS 的地址由 DHCP 分配，变了只改 `HostName`。

4. 运行 `ssh a-nas`。第一次连接时 SSH 会显示 `ED25519 key fingerprint is SHA256:…`：与设备所有者给的指纹一致才输入 `yes`；不一致就停下，告诉设备所有者。

5. 登录后运行 `id`，应看到 `anas-devs`、`anas-eval` 与 `systemd-journal`。

## 共享目录 `/srv/eval`

- 组 `anas-eval` 中的每个人都能改、删这里的任何文件，新文件自动对组内可写。约定：自己正在用的放 `/srv/eval/<你的账号>/`，大家共用的模型放 `/srv/eval/models/`；Hugging Face 等下载缓存设到共享位置，例如 `export HF_HOME=/srv/eval/cache/huggingface`，免得每人各下一份。
- 它在系统盘上，与 NAS 的系统和服务共用空间。下载超过 20 GB 前先看 `df -h /srv/eval` 并告诉设备所有者。
- 不要把成员的照片、文件或其他真实个人数据放进来；评测只用公开数据集或自己做的测试数据。

## 资源

- 每个开发者账号的全部进程合计：内存超过 6 GB 会被限速回收，超过 8 GB 会被杀掉。CPU 与 NAS 上的服务共用，你的会话排在本地控制台之后。
- 跑之前看一眼 `uptime` 与 `free -h`。预计超过 1 小时或要占 4 GB 以上内存的任务，先告诉设备所有者。
- 要在断开 SSH 后继续跑，用 `nohup <命令> > <日志> 2>&1 &`；NAS 上没有装 `tmux` 与 `screen`。
- 用完删掉不再需要的模型、检查点与中间文件。

## 结束时

实习或项目结束时告诉设备所有者撤销账号，并把 `/srv/eval` 中要留下的东西交接给别人。撤销后账号的 home 会保留一段时间，但不再能登录。

## 关联

- 开设与撤销：[为开发者开、撤实验 NAS 的 SSH 账号](../runbooks/manage-developer-ssh-accounts.md)
- 决定：[实习生的 SSH 账号](../decisions/2026-11-23-developer-ssh-accounts.md)
- 开发环境入口：[LOCAL_ENVIRONMENT.md](LOCAL_ENVIRONMENT.md)
