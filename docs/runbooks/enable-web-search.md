# 启用网页搜索

状态：draft（系统测试覆盖服务边界；实验 NAS 待执行）
更新时间：2026-10-30

## 目的

第一次在 Experimental NAS 上安装带网页搜索的 release（[ADR 0030](../adr/0030-ship-web-search-as-a-built-in-searxng-service.md)、[规格](../specs/web-search.md)），并在 NAS 的实际网络下测量各引擎。之后的升级按[实机配置手册](provision-v1.0.1-experimental-storage.md#安装系统服务)进行即可。

成功后：

- `anas-search.service` 以 `a-nas-search` 运行 SearXNG，只有产品服务能连接它的套接字，它只能访问公网地址。
- 所有账号的桌面上有“网页搜索”，管理员在“设置 → 网页搜索”中可以关闭它、打开种子搜索。

## 前提与风险

- **权限**：管理员 root 会话。暂存用日常账号 `anas-dev`。
- **目标**：主机名 `a-nas-dev`，使用前按 [SSH 运行手册](bootstrap-experimental-nas-ssh.md)核对指纹。
- **前提**：
  - 用户已同意部署本版本。
  - 开发机已运行 `make check`，并按[本地环境](../development/testing.md#系统测试)跑过系统测试。
  - 开发机已用 `scripts/build-search-runtime.sh` 构建 [`deploy/search/searxng.json`](../../deploy/search/searxng.json) 固定的运行环境。
- **风险**：
  - 这是第一个代成员访问外网的内置服务。实验 NAS 的出口是公司网络，公司网络能看到访问了哪些搜索引擎与种子站。
  - 测量时请求太快，引擎会对这个出口返回验证码，并在一段时间内影响所有人（[研究记录](../research/web-search-engines.md)）。测量只按下面的节奏进行。
- **资源**：运行环境压缩包约 58 MB，按内容只存一份；常驻内存约 100–150 MB，单元上限 512 MiB。
- **停止条件**（出现任一项立即停止）：
  - 安装器以退出码 2（缺少网页搜索文件或运行环境清单无效）、3（Python 版本不符，或 `a-nas-search` 账号已存在且有家目录或登录 Shell）或 4（暂存的运行环境与清单不符）结束。
  - `systemctl status anas-search` 反复重启，或日志出现 `oom-kill`。

## 步骤

1. 在开发机构建并暂存。
   - 命令（WSL）：`scripts/build-search-runtime.sh`。
   - 命令（Windows）：`scripts/deploy-dev.ps1 -NasHost a-nas-dev -StageOnly -WslDistribution Ubuntu -WslUser <WSL 用户>`。
   - 预期：运行环境上传一次，此后暂存时不再上传。
   - 完成标准：暂存 release 中有 `search/settings.yml`、`search/searxng.json`、`search/runtime.json`、`search/allow-dns.sh`，`search/runtime.tar.gz` 链接到 `apps/a-nas/search-runtimes/`；暂存脚本已核对哈希。
2. 以 root 按[实机配置手册的安装步骤](provision-v1.0.1-experimental-storage.md#安装系统服务)核对二进制哈希并运行安装器。
   - 预期：安装器创建 `a-nas-search` 账号，生成 `/etc/a-nas/search.env`，把运行环境解到 `/opt/a-nas/search-runtimes/<id>`，启用 `anas-search-dns.path` 与 `anas-search.service`，最后输出 `web search: active`。
3. 核对套接字、身份与网络边界。
   - 命令：`stat -c '%a %U %G' /run/a-nas-search /run/a-nas-search/search.sock`。
   - 预期：`750 a-nas-search a-nas-search` 与 `660 a-nas-search a-nas-search`。
   - 命令：`systemctl show -p IPAddressAllow anas-search`。
   - 预期：列出 `/etc/resolv.conf` 中的 DNS 服务器（实验 NAS 当前为 `172.18.254.101` 与 `172.16.200.200`）。
   - 命令：`stat -c '%a %U %G' /etc/a-nas/search.env`。
   - 预期：`640 root a-nas`。
4. 在桌面上搜索一次，确认能访问公网。
   - 操作：打开“网页搜索”，搜索一个常见词，再切到“视频”。
   - 预期：有结果，视频有封面；结果上方的“未响应”只列出个别引擎。
5. 按真实节奏测量各引擎，结果写入[研究记录](../research/web-search-engines.md)。
   - 操作：每个分类 2–4 个查询，每个查询只切换一次分类，两次搜索之间至少间隔 30 秒；种子只用合法内容（如 `ubuntu`、`big buck bunny`）。
   - 记录：每个引擎是否出现在“未响应”中、是否有结果，以及 `journalctl -u anas-search -b -o cat` 中的引擎错误（CAPTCHA、timeout、access denied）。
   - 完成标准：研究记录中有 NAS 一节，默认引擎列表据此调整后另行提交。
6. 核对开关。
   - 操作：在“设置 → 网页搜索”中关闭网页搜索。
   - 预期：`systemctl is-active anas-search` 为 `inactive`，`/var/lib/a-nas-host/web-search-disabled` 存在；成员的桌面在一分钟内不再显示“网页搜索”。
   - 操作：重新运行一次安装器（同一 release 也可以），再执行 `systemctl is-active anas-search`。
   - 预期：仍为 `inactive`。重新打开开关后为 `active`。

## 验证

- 服务连不上局域网：`nsenter -t "$(systemctl show -p MainPID --value anas-search)" -n` 不能用来验证，因为它不在私有网络命名空间中；改用系统测试中的替身（[`scripts/system-test-search.py`](../../scripts/system-test-search.py)），它在同一个单元的沙箱中报告每个地址是否被拒绝。
- 不记录搜索词：搜索几次后执行 `journalctl -u anas-search -u anas-api -b -o cat | grep -F '<搜索的词>'`，应没有输出。

## 回滚或恢复

- **只关闭网页搜索**：在“设置 → 网页搜索”中关闭；或以 root 执行 `touch /var/lib/a-nas-host/web-search-disabled && systemctl stop anas-search`。
- **回到不带网页搜索的版本**：按实机配置手册切回旧 release，再执行 `systemctl disable --now anas-search.service anas-search-dns.path`，删除 `/etc/systemd/system/anas-search*.service`、`anas-search-dns.path` 后执行 `systemctl daemon-reload`。`/opt/a-nas/search-runtimes`、`/etc/a-nas/search.env` 与 `a-nas-search` 账号不会自动删除，确认不再需要后另行删除。

## 关联

- 规格：[网页搜索](../specs/web-search.md)
- ADR：[ADR 0030](../adr/0030-ship-web-search-as-a-built-in-searxng-service.md)
- 代码：[`anas-search.service`](../../deploy/systemd/system/anas-search.service)、[`anas-search-dns.path`](../../deploy/systemd/system/anas-search-dns.path)、[`allow-dns.sh`](../../deploy/search/allow-dns.sh)、[安装器](../../scripts/install-v1.0.1-system-services.sh)、[运行环境构建](../../scripts/build-search-runtime.sh)
- 研究：[引擎实测](../research/web-search-engines.md)
